package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/models"
)

// proxyImageURL rewrites any external (http/https) image URL to go through the
// /api/v1/imgproxy endpoint so the browser never hits external origins, which
// would be blocked by the app's CSP. Relative paths are returned unchanged.
func proxyImageURL(u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return "/api/v1/imgproxy?url=" + u
	}
	return u
}

// proxyImageURLPtr is the pointer variant for *string fields.
func proxyImageURLPtr(u *string) *string {
	if u == nil {
		return nil
	}
	proxied := proxyImageURL(*u)
	return &proxied
}

// photoIsPublicSQL returns a boolean SQL expression that's TRUE exactly when
// the photo identified by photoIDExpr (e.g. "p.photoid", or bare "photoid"
// when the query has no alias) is public.
//
// PLAN2.md Phase 2b: this replaced the photos.is_public column (dropped by
// migrations/022_drop_is_public.sql) as the single source of truth for photo
// visibility. A photo is public iff it has a non-deleted label named
// "Public" whose value is exactly "True" — the canonical casing every write
// path uses (admin.go's SetPublic, upload.go, cmd/import-photos); a photo
// with no such label (never explicitly toggled, or a value of "False") is
// private, matching is_public's old default-FALSE behavior. See
// scripts/sync_public_labels.sql (historical — folded into migration 022's
// backfill) for how existing photos got this label before the column was
// dropped.
func photoIsPublicSQL(photoIDExpr string) string {
	return fmt.Sprintf(`EXISTS (
	    SELECT 1 FROM labels pub
	    WHERE  pub.photoid    = %s
	      AND  pub.name       = 'Public'
	      AND  pub.value      = 'True'
	      AND  pub.deleted_at IS NULL
	)`, photoIDExpr)
}

// photoAccessibleViaDisplaySQL returns a boolean SQL expression that's TRUE
// when the caller identified by userIDExpr holds DisplayView or GalleryView
// — scoped to a display containing the photo, that display's gallery, its
// exhibition, its organization, or globally — for at least one non-deleted
// display currently showing the photo identified by photoIDExpr.
//
// PLAN2.md Phase 2d ("Photo access resolution: indirect grants via
// display/gallery View permissions"; TODO2.md: "DisplayView grants
// PhotoView for all photos used within the displays for which the
// permission is granted" / "GalleryView grants PhotoView for all photos
// used within displays within the galleries for which the permission is
// granted"). DisplayView and GalleryView are checked independently — a
// grant of one does not imply the other (see Checker.Check's own doc);
// either is independently sufficient here because both TODO2.md bullets
// independently promise a PhotoView cascade.
//
// This mirrors Checker.Check's own Global → Organization → Exhibition →
// Gallery → Display resolution chain (see permissions.go) but correlates
// against display_slots instead of one fixed galleryID/resourceRef pair,
// since a photo can appear in several displays/galleries at once and any
// single one granting access is enough. NULLIF(userIDExpr, '')::uuid
// mirrors Checker.Check's own team-membership subquery cast — safe here for
// the same reason it's safe there: userIDExpr is always also used elsewhere
// in the same overall query as a plain text comparison (the owner-exception
// clause every call site already has), so Postgres infers its parameter
// type as text, not uuid, and this is just an explicit, NULLIF-guarded
// runtime cast rather than a native uuid-typed bind parameter (see
// nullableUUID's doc in permissions.go for why that distinction matters).
//
// This is an alternative path to holding PhotoView directly, not to
// PrivatePhotoView — a photo found accessible this way is still separately
// gated by the Public-label/PrivatePhotoView check every visibility call
// site already applies (see the permissions package doc's "Photo
// visibility" section). Checked once per candidate photo row, so this is
// necessarily more expensive than the single-boolean PermPhotoView check;
// migrations/023_display_slots_photoid_index.sql adds the supporting index.
func photoAccessibleViaDisplaySQL(photoIDExpr, userIDExpr string) string {
	return fmt.Sprintf(`EXISTS (
	    SELECT 1
	    FROM   display_slots     ds
	    JOIN   displays           d   ON d.displayid   = ds.displayid AND d.deleted_at IS NULL
	    JOIN   galleries          g   ON g.galleryid   = d.galleryid  AND g.deleted_at  IS NULL
	    LEFT   JOIN exhibitions   ex  ON ex.exhibitionid = g.exhibitionid
	    JOIN   entity_role_grants erg ON TRUE
	    JOIN   role_permissions   rp  ON rp.roleid = erg.roleid
	    JOIN   roles               r  ON r.roleid  = erg.roleid AND r.deleted_at IS NULL
	    WHERE  ds.photoid = %[1]s
	      AND  (
	               erg.entity_type = 'Public'
	            OR (%[2]s <> '' AND erg.entity_type = 'LoggedIn')
	            OR (%[2]s <> '' AND erg.entity_type = 'User' AND erg.entity_ref = %[2]s)
	            OR (%[2]s <> '' AND erg.entity_type = 'Team'
	                             AND erg.entity_ref IN (
	                                     SELECT teamid::text
	                                     FROM   team_members
	                                     WHERE  userid = NULLIF(%[2]s, '')::uuid
	                                 ))
	           )
	      AND  (
	               (rp.permission = 'DisplayView' AND (
	                        (erg.resource_type = 'Display' AND erg.resource_ref = d.displayid::text)
	                     OR (erg.resource_type = 'Gallery' AND erg.resource_ref = g.galleryid::text)
	                     OR (erg.exhibitionid = g.exhibitionid AND erg.resource_type IS NULL)
	                     OR (erg.organizationid IS NOT NULL AND erg.organizationid = ex.organizationid)
	                     OR (erg.exhibitionid IS NULL AND erg.resource_type IS NULL AND erg.organizationid IS NULL)
	               ))
	            OR (rp.permission = 'GalleryView' AND (
	                        (erg.resource_type = 'Gallery' AND erg.resource_ref = g.galleryid::text)
	                     OR (erg.exhibitionid = g.exhibitionid AND erg.resource_type IS NULL)
	                     OR (erg.organizationid IS NOT NULL AND erg.organizationid = ex.organizationid)
	                     OR (erg.exhibitionid IS NULL AND erg.resource_type IS NULL AND erg.organizationid IS NULL)
	               ))
	           )
	)`, photoIDExpr, userIDExpr)
}

// fetchLabels returns a page of labels for a photo plus the total count.
func fetchLabels(ctx context.Context, pool *db.Pool, photoid string, offset, limit int) ([]models.Label, int, error) {
	// total count
	var total int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM labels
		WHERE  photoid = $1 AND deleted_at IS NULL
	`, photoid).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := pool.Query(ctx, `
		SELECT l.labelid::text, l.name, l.value,
		       l.added_by_userid::text, u.username,
		       ln.color_hex, COALESCE(ln.restricted, FALSE)
		FROM   labels l
		JOIN   users  u  ON u.userid = l.added_by_userid
		LEFT   JOIN label_names ln ON ln.name = l.name
		WHERE  l.photoid = $1 AND l.deleted_at IS NULL
		ORDER  BY l.created_at
		LIMIT  $2 OFFSET $3
	`, photoid, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	labels := make([]models.Label, 0)
	for rows.Next() {
		var l models.Label
		if err := rows.Scan(&l.LabelID, &l.Name, &l.Value, &l.UserID, &l.Username,
			&l.ColorHex, &l.Restricted); err != nil {
			return nil, 0, err
		}
		labels = append(labels, l)
	}
	return labels, total, rows.Err()
}

// fetchEmojis returns a page of emojis for a photo, each with up to userLimit users.
func fetchEmojis(ctx context.Context, pool *db.Pool, photoid string, offset, limit, userLimit int) ([]models.Emoji, int, error) {
	var total int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT ec.emojiid)
		FROM   emoji_counts ec
		WHERE  ec.photoid = $1
	`, photoid).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Fetch emoji types + counts for this photo
	rows, err := pool.Query(ctx, `
		SELECT et.emojiid::text, et.emoji_char, et.image_url, et.alt_text,
		       ec.reaction_count
		FROM   emoji_counts ec
		JOIN   emoji_types  et ON et.emojiid = ec.emojiid
		WHERE  ec.photoid = $1
		ORDER  BY ec.reaction_count DESC
		LIMIT  $2 OFFSET $3
	`, photoid, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	emojis := make([]models.Emoji, 0)
	for rows.Next() {
		var e models.Emoji
		if err := rows.Scan(&e.EmojiID, &e.EmojiChar, &e.ImageURL, &e.AltText, &e.Count); err != nil {
			return nil, 0, err
		}
		e.ImageURL = proxyImageURLPtr(e.ImageURL)
		emojis = append(emojis, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// For each emoji, fetch the first N users
	for i := range emojis {
		users, err := fetchEmojiUsers(ctx, pool, photoid, emojis[i].EmojiID, 0, userLimit)
		if err != nil {
			return nil, 0, err
		}
		emojis[i].Users = users
		if emojis[i].Count > userLimit {
			u := fmt.Sprintf("/api/v1/emoji/users?emoji=%s&limit=10&offset=%d", emojis[i].EmojiID, userLimit)
			emojis[i].UsersURL = &u
		}
	}
	return emojis, total, nil
}

// fetchEmojiUsers returns a page of users who reacted with a specific emoji on a photo.
func fetchEmojiUsers(ctx context.Context, pool *db.Pool, photoid, emojiid string, offset, limit int) ([]models.EmojiUser, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.userid::text, u.username,
		       COALESCE(u.profile_image, '/avatars/' || md5(lower(trim(COALESCE(u.email, u.userid::text)))))
		FROM   emoji_reactions er
		JOIN   users           u  ON u.userid = er.userid
		WHERE  er.photoid = $1 AND er.emojiid = $2
		ORDER  BY er.reacted_at
		LIMIT  $3 OFFSET $4
	`, photoid, emojiid, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]models.EmojiUser, 0)
	for rows.Next() {
		var u models.EmojiUser
		if err := rows.Scan(&u.ID, &u.Name, &u.TN); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// fetchRelated returns all related photos for a given photo, scoped to the exhibition.
// canSeePrivate and hasPhotoView (held directly or indirectly via a
// DisplayView/GalleryView grant — PLAN2.md Phase 2d) together gate
// visibility per the permissions package doc's "Photo visibility" section;
// currentUserID additionally always includes the caller's own photos (see
// the owner exception in PhotoHandler.ServeHTTP for why).
func fetchRelated(ctx context.Context, pool *db.Pool, photoid, exhibitionID string, canSeePrivate, hasPhotoView bool, currentUserID string) ([]models.RelatedPhoto, error) {
	rows, err := pool.Query(ctx, fmt.Sprintf(`
		SELECT rp.related_photoid::text,
		       COALESCE(rp.scaled_image_url, p.image_url),
		       COALESCE(rp.click_url, '/photo?photoid=' || rp.related_photoid::text),
		       p.image_width, p.image_height
		FROM   related_photos rp
		JOIN   photos         p  ON p.photoid = rp.related_photoid
		WHERE  rp.photoid = $1
		  AND  p.deleted_at IS NULL
		  AND  ($2 = '' OR p.exhibitionid::text = $2)
		  AND  (($4 <> '' AND p.owner_userid::text = $4) OR (($5 OR %s) AND (%s OR $3)))
		ORDER  BY rp.sort_order
	`, photoAccessibleViaDisplaySQL("p.photoid", "$4"), photoIsPublicSQL("p.photoid")), photoid, exhibitionID, canSeePrivate, currentUserID, hasPhotoView)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	related := make([]models.RelatedPhoto, 0)
	for rows.Next() {
		var rp models.RelatedPhoto
		if err := rows.Scan(&rp.PhotoID, &rp.ImageURL, &rp.ClickURL, &rp.Width, &rp.Height); err != nil {
			return nil, err
		}
		rp.ImageURL = proxyImageURL(rp.ImageURL)
		related = append(related, rp)
	}
	return related, rows.Err()
}

// fetchRelatedByLabel returns up to 8 photos that share the same label name+value
// as the given labelID, excluding the current photo, scoped to the exhibition.
// canSeePrivate and hasPhotoView (held directly or indirectly via a
// DisplayView/GalleryView grant — PLAN2.md Phase 2d) together gate
// visibility per the permissions package doc's "Photo visibility" section;
// currentUserID additionally always includes the caller's own photos (see
// the owner exception in PhotoHandler.ServeHTTP for why).
func fetchRelatedByLabel(ctx context.Context, pool *db.Pool, photoid, labelID, exhibitionID string, canSeePrivate, hasPhotoView bool, currentUserID string) ([]models.RelatedPhoto, error) {
	rows, err := pool.Query(ctx, fmt.Sprintf(`
		WITH label_info AS (
			SELECT name, value FROM labels WHERE labelid = $1 AND deleted_at IS NULL
		),
		candidates AS (
			SELECT DISTINCT p.photoid::text, p.image_url, p.image_width, p.image_height, p.view_count
			FROM   photos  p
			JOIN   labels  l  ON l.photoid = p.photoid
			JOIN   label_info li ON l.name = li.name AND l.value = li.value
			WHERE  p.photoid != $2::uuid
			  AND  p.deleted_at IS NULL
			  AND  l.deleted_at IS NULL
			  AND  ($3 = '' OR p.exhibitionid::text = $3)
			  AND  (($5 <> '' AND p.owner_userid::text = $5) OR (($6 OR %s) AND (%s OR $4)))
		),
		top_ten AS (
			SELECT * FROM candidates ORDER BY view_count DESC LIMIT 10
		),
		random_three AS (
			SELECT * FROM candidates
			WHERE  photoid NOT IN (SELECT photoid FROM top_ten)
			ORDER  BY random()
			LIMIT  3
		)
		SELECT photoid, image_url, image_width, image_height FROM random_three
		UNION ALL
		SELECT photoid, image_url, image_width, image_height FROM top_ten
	`, photoAccessibleViaDisplaySQL("p.photoid", "$5"), photoIsPublicSQL("p.photoid")), labelID, photoid, exhibitionID, canSeePrivate, currentUserID, hasPhotoView)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	related := make([]models.RelatedPhoto, 0)
	for rows.Next() {
		var rp models.RelatedPhoto
		if err := rows.Scan(&rp.PhotoID, &rp.ImageURL, &rp.Width, &rp.Height); err != nil {
			return nil, err
		}
		rp.ImageURL = proxyImageURL(rp.ImageURL)
		rp.ClickURL = fmt.Sprintf("/?photoid=%s&label=%s", rp.PhotoID, labelID)
		related = append(related, rp)
	}
	return related, rows.Err()
}

// fetchComments returns a page of comments for a photo or replies to a parent comment.
// Pass parentID = "" for top-level comments.
func fetchComments(ctx context.Context, pool *db.Pool, photoid, parentID string, offset, limit int) ([]models.Comment, int, error) {
	var (
		total int
		err   error
	)
	// A deleted comment is included only when it still has replies, so that
	// those replies remain visible in context. Its text is blanked server-side.
	// countFilter: used in single-table COUNT queries (no alias).
	// joinFilter:  used in JOIN queries where comments is aliased as c.
	const countFilter = `(deleted_at IS NULL OR reply_count > 0)`
	const joinFilter = `(c.deleted_at IS NULL OR c.reply_count > 0)`

	if parentID == "" {
		err = pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM comments
			WHERE  photoid = $1 AND parent_commentid IS NULL AND `+countFilter,
			photoid).Scan(&total)
	} else {
		err = pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM comments
			WHERE  parent_commentid = $1 AND `+countFilter,
			parentID).Scan(&total)
	}
	if err != nil {
		return nil, 0, err
	}

	const selectCols = `
		c.commentid::text,
		CASE WHEN c.deleted_at IS NOT NULL THEN '' ELSE c.comment_text END,
		c.reply_count, c.created_at,
		u.userid::text, u.username,
		COALESCE(u.profile_image, '/avatars/' || md5(lower(trim(COALESCE(u.email, u.userid::text))))),
		c.deleted_at IS NOT NULL`

	var rows interface {
		Next() bool
		Scan(...any) error
		Close()
		Err() error
	}
	if parentID == "" {
		rows, err = pool.Query(ctx, `
			SELECT `+selectCols+`
			FROM   comments c
			JOIN   users    u ON u.userid = c.author_userid
			WHERE  c.photoid = $1
			  AND  c.parent_commentid IS NULL
			  AND  `+joinFilter+`
			ORDER  BY c.created_at
			LIMIT  $2 OFFSET $3
		`, photoid, limit, offset)
	} else {
		rows, err = pool.Query(ctx, `
			SELECT `+selectCols+`
			FROM   comments c
			JOIN   users    u ON u.userid = c.author_userid
			WHERE  c.parent_commentid = $1
			  AND  `+joinFilter+`
			ORDER  BY c.created_at
			LIMIT  $2 OFFSET $3
		`, parentID, limit, offset)
	}
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	comments := make([]models.Comment, 0)
	for rows.Next() {
		var c models.Comment
		if err := rows.Scan(
			&c.CommentID, &c.Comment, &c.ReplyCount, &c.Date,
			&c.Author.UserID, &c.Author.Username, &c.Author.TN,
			&c.Deleted,
		); err != nil {
			return nil, 0, err
		}
		c.RepliesURL = fmt.Sprintf("/api/v1/comments?photoid=%s&parentid=%s", photoid, c.CommentID)
		comments = append(comments, c)
	}
	return comments, total, rows.Err()
}
