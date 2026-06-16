package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
)

// PhotoHandler handles GET /api/v1/photo?photoid=<id>
type PhotoHandler struct {
	DB  *db.Pool
	Cfg *config.Config
}

func (h *PhotoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	photoid := q.Get("photoid")
	random := q.Get("random") == "true" || q.Get("random") == "1"
	labelID := q.Get("label")

	if photoid == "" && !random {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}

	currentUser, _ := middleware.UserID(r.Context())
	exhibitionID := middleware.ExhibitionID(r.Context())
	canSeeNonPublic := middleware.AuthorizedNonPublic(r.Context())
	ctx := r.Context()

	// ── Core photo row ────────────────────────────────────────────────────────
	var row pgx.Row
	if random {
		row = h.DB.QueryRow(ctx, `
			SELECT
				p.photoid, p.image_url, p.image_width, p.image_height,
				COALESCE(p.title_text, ''), COALESCE(p.title_userid::text, ''),
				COALESCE(tu.username, ''),  p.owner_userid::text,
				COALESCE(p.description, '')
			FROM  photos p
			LEFT  JOIN users tu ON tu.userid = p.title_userid
			WHERE p.deleted_at IS NULL
			  AND ($1 = '' OR p.exhibitionid::text = $1)
			  AND (p.is_public OR $2)
			ORDER BY random()
			LIMIT 1
		`, exhibitionID, canSeeNonPublic)
	} else {
		row = h.DB.QueryRow(ctx, `
			SELECT
				p.photoid, p.image_url, p.image_width, p.image_height,
				COALESCE(p.title_text, ''), COALESCE(p.title_userid::text, ''),
				COALESCE(tu.username, ''),  p.owner_userid::text,
				COALESCE(p.description, '')
			FROM  photos p
			LEFT  JOIN users tu ON tu.userid = p.title_userid
			WHERE p.photoid = $1 AND p.deleted_at IS NULL
			  AND ($2 = '' OR p.exhibitionid::text = $2)
			  AND (p.is_public OR $3)
		`, photoid, exhibitionID, canSeeNonPublic)
	}

	var (
		imgURL, titleText, titleUserID, titleUsername, ownerUserID, desc string
		imgW, imgH                                                       int
	)
	err := row.Scan(&photoid, &imgURL, &imgW, &imgH,
		&titleText, &titleUserID, &titleUsername, &ownerUserID, &desc)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	canEdit := currentUser != "" && (currentUser == titleUserID || currentUser == ownerUserID)

	photo := models.Photo{
		PhotoID: photoid,
		Image:   models.ImageInfo{URL: proxyImageURL(imgURL), Width: imgW, Height: imgH},
		Title: models.TitleInfo{
			Text:     titleText,
			UserID:   titleUserID,
			Username: titleUsername,
			CanEdit:  canEdit,
		},
		Description: desc,
	}

	// ── Labels (first page) ───────────────────────────────────────────────────
	const labelLimit = 10
	labels, labelTotal, err := fetchLabels(ctx, h.DB, photoid, 0, labelLimit)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	photo.Labels = labels
	if labelTotal > labelLimit {
		u := fmt.Sprintf("/api/v1/labels?photoid=%s&limit=%d&offset=%d", photoid, labelLimit, labelLimit)
		photo.LabelsURL = &u
	}

	// ── Emojis ────────────────────────────────────────────────────────────────
	const emojiUserLimit = 3
	emojis, emojiTotal, err := fetchEmojis(ctx, h.DB, photoid, 0, 20, emojiUserLimit)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	photo.Emojis = emojis
	if emojiTotal > 20 {
		u := fmt.Sprintf("/api/v1/emojis?photoid=%s&limit=20&offset=20", photoid)
		photo.EmojisURL = &u
	}

	// ── Increment view count ──────────────────────────────────────────────────
	_, _ = h.DB.Exec(ctx, `
		UPDATE photos SET view_count = view_count + 1
		WHERE  photoid = $1 AND ($2 = '' OR exhibitionid::text = $2)
	`, photoid, exhibitionID)

	// ── Related photos ────────────────────────────────────────────────────────
	var related []models.RelatedPhoto
	if labelID != "" {
		related, err = fetchRelatedByLabel(ctx, h.DB, photoid, labelID, exhibitionID, canSeeNonPublic)
	} else {
		related, err = fetchRelated(ctx, h.DB, photoid, exhibitionID, canSeeNonPublic)
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	photo.Related = related

	// ── Comments (first page) ─────────────────────────────────────────────────
	const commentLimit = 10
	comments, commentTotal, err := fetchComments(ctx, h.DB, photoid, "", 0, commentLimit)
	if err != nil {
		slog.Error("fetchComments", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	photo.Comments = comments
	if commentTotal > commentLimit {
		u := fmt.Sprintf("/api/v1/comments?photoid=%s&limit=%d&offset=%d", photoid, commentLimit, commentLimit)
		photo.CommentsURL = &u
	}

	middleware.WriteJSON(w, http.StatusOK, photo)
}

// UserHandler handles GET /api/v1/user?userid=<id>
type UserHandler struct {
	DB *db.Pool
}

func (h *UserHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userid := r.URL.Query().Get("userid")
	if userid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "userid is required")
		return
	}

	row := h.DB.QueryRow(r.Context(), `
		SELECT userid::text, username, COALESCE(fullname,''),
		       joined_at, profile_link,
		       COALESCE(profile_image, '/avatars/' || md5(lower(trim(COALESCE(email, userid::text)))))
		FROM   users
		WHERE  userid = $1 AND deleted_at IS NULL
	`, userid)

	var u models.User
	err := row.Scan(&u.UserID, &u.Username, &u.Profile.FullName,
		&u.Profile.Joined, &u.Profile.Link, &u.Profile.Image)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, u)
}

// PatchPhotoHandler handles PATCH /api/v1/photo?photoid=<id>
// Allows the title owner or photo owner to update the title text.
type PatchPhotoHandler struct {
	DB  *db.Pool
	Cfg *config.Config
}

func (h *PatchPhotoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}

	currentUser, ok := middleware.UserID(r.Context())
	if !ok || currentUser == "" {
		middleware.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body models.UpdatePhotoTitleRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		middleware.WriteError(w, http.StatusBadRequest, "title must not be empty")
		return
	}

	ctx := r.Context()

	// Verify the photo exists and the caller is allowed to edit it.
	var ownerID, titleUserID string
	err := h.DB.QueryRow(ctx, `
		SELECT owner_userid::text, COALESCE(title_userid::text, owner_userid::text)
		FROM   photos
		WHERE  photoid = $1 AND deleted_at IS NULL
	`, photoid).Scan(&ownerID, &titleUserID)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if currentUser != ownerID && currentUser != titleUserID {
		middleware.WriteError(w, http.StatusForbidden, "not allowed to edit this title")
		return
	}

	_, err = h.DB.Exec(ctx, `
		UPDATE photos
		SET    title_text = $1, title_userid = $2, updated_at = NOW()
		WHERE  photoid = $3
	`, title, currentUser, photoid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]string{"title": title})
}
