package handlers

import (
	"fmt"
	"net/http"

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
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}

	currentUser, _ := middleware.UserID(r.Context())
	ctx := r.Context()

	// ── Core photo row ────────────────────────────────────────────────────────
	row := h.DB.QueryRow(ctx, `
		SELECT
			p.photoid, p.image_url, p.image_width, p.image_height,
			COALESCE(p.title_text, ''), COALESCE(p.title_userid::text, ''),
			COALESCE(tu.username, ''),  p.owner_userid::text,
			COALESCE(p.description, '')
		FROM  photos p
		LEFT  JOIN users tu ON tu.userid = p.title_userid
		WHERE p.photoid = $1 AND p.deleted_at IS NULL
	`, photoid)

	var (
		imgURL, titleText, titleUserID, titleUsername, ownerUserID, desc string
		imgW, imgH                                                         int
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
		Image:   models.ImageInfo{URL: imgURL, Width: imgW, Height: imgH},
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

	// ── Related photos ────────────────────────────────────────────────────────
	related, err := fetchRelated(ctx, h.DB, photoid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	photo.Related = related

	// ── Comments (first page) ─────────────────────────────────────────────────
	const commentLimit = 10
	comments, commentTotal, err := fetchComments(ctx, h.DB, photoid, "", 0, commentLimit)
	if err != nil {
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
		       joined_at, profile_link, profile_image
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
