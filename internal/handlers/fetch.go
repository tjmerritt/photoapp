package handlers

import (
	"context"
	"fmt"

	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/models"
)

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
		       l.added_by_userid::text, u.username
		FROM   labels l
		JOIN   users  u ON u.userid = l.added_by_userid
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
		if err := rows.Scan(&l.LabelID, &l.Name, &l.Value, &l.UserID, &l.Username); err != nil {
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
		SELECT u.userid::text, u.username, u.profile_image
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

// fetchRelated returns all related photos for a given photo.
func fetchRelated(ctx context.Context, pool *db.Pool, photoid string) ([]models.RelatedPhoto, error) {
	rows, err := pool.Query(ctx, `
		SELECT rp.related_photoid::text,
		       COALESCE(rp.scaled_image_url, p.image_url),
		       COALESCE(rp.click_url, '/photo?photoid=' || rp.related_photoid::text),
		       p.image_width, p.image_height
		FROM   related_photos rp
		JOIN   photos         p  ON p.photoid = rp.related_photoid
		WHERE  rp.photoid = $1
		  AND  p.deleted_at IS NULL
		ORDER  BY rp.sort_order
	`, photoid)
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
		related = append(related, rp)
	}
	return related, rows.Err()
}

// fetchRelatedByLabel returns up to 8 photos that share the same label name+value
// as the given labelID, excluding the current photo.
// The top 7 are chosen by view_count DESC; one additional is chosen at random
// from the remaining candidates (if any exist beyond the top 7).
func fetchRelatedByLabel(ctx context.Context, pool *db.Pool, photoid, labelID string) ([]models.RelatedPhoto, error) {
	rows, err := pool.Query(ctx, `
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
		),
		top_seven AS (
			SELECT * FROM candidates ORDER BY view_count DESC LIMIT 7
		),
		random_extra AS (
			SELECT * FROM candidates
			WHERE  photoid NOT IN (SELECT photoid FROM top_seven)
			ORDER  BY random()
			LIMIT  1
		)
		SELECT photoid, image_url, image_width, image_height FROM random_extra
		UNION ALL
		SELECT photoid, image_url, image_width, image_height FROM top_seven
	`, labelID, photoid)
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
	if parentID == "" {
		err = pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM comments
			WHERE  photoid = $1 AND parent_commentid IS NULL AND deleted_at IS NULL
		`, photoid).Scan(&total)
	} else {
		err = pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM comments
			WHERE  parent_commentid = $1 AND deleted_at IS NULL
		`, parentID).Scan(&total)
	}
	if err != nil {
		return nil, 0, err
	}

	var rows interface{ Next() bool; Scan(...any) error; Close(); Err() error }
	if parentID == "" {
		rows, err = pool.Query(ctx, `
			SELECT c.commentid::text, c.comment_text, c.reply_count, c.created_at,
			       u.userid::text, u.username, u.profile_image
			FROM   comments c
			JOIN   users    u ON u.userid = c.author_userid
			WHERE  c.photoid = $1
			  AND  c.parent_commentid IS NULL
			  AND  c.deleted_at IS NULL
			ORDER  BY c.created_at
			LIMIT  $2 OFFSET $3
		`, photoid, limit, offset)
	} else {
		rows, err = pool.Query(ctx, `
			SELECT c.commentid::text, c.comment_text, c.reply_count, c.created_at,
			       u.userid::text, u.username, u.profile_image
			FROM   comments c
			JOIN   users    u ON u.userid = c.author_userid
			WHERE  c.parent_commentid = $1
			  AND  c.deleted_at IS NULL
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
		); err != nil {
			return nil, 0, err
		}
		c.RepliesURL = fmt.Sprintf("/api/v1/comments?photoid=%s&parentid=%s", photoid, c.CommentID)
		comments = append(comments, c)
	}
	return comments, total, rows.Err()
}
