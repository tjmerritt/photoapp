package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

// PhotoHandler handles GET /api/v1/photo?photoid=<id>
type PhotoHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
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
	ctx := r.Context()
	canSeePrivate, _ := h.Checker.Check(ctx, currentUser, exhibitionID, "", "", "", permissions.PermPrivatePhotoView)
	hasPhotoView, _ := h.Checker.Check(ctx, currentUser, exhibitionID, "", "", "", permissions.PermPhotoView)

	// ── Core photo row ────────────────────────────────────────────────────────
	// Visibility (see permissions package doc's "Photo visibility" section):
	// the photo's own owner can always see it; otherwise the caller needs
	// PermPhotoView *and* (the photo is Public-labeled OR the caller holds
	// PermPrivatePhotoView). The owner exception exists so a user without
	// PhotoView/PrivatePhotoView who just uploaded a photo (browser uploads
	// are always created private — see upload.go) would have no way to ever
	// see their own photo again, despite the upload having actually
	// succeeded.
	var row pgx.Row
	if random {
		row = h.DB.QueryRow(ctx, fmt.Sprintf(`
			SELECT
				p.photoid, p.image_url, p.image_width, p.image_height,
				COALESCE(p.title_text, ''), COALESCE(p.title_userid::text, ''),
				COALESCE(tu.username, ''),  p.owner_userid::text,
				COALESCE(p.description, '')
			FROM  photos p
			LEFT  JOIN users tu ON tu.userid = p.title_userid
			WHERE p.deleted_at IS NULL
			  AND ($1 = '' OR p.exhibitionid::text = $1)
			  AND (($3 <> '' AND p.owner_userid::text = $3) OR ($4 AND (%s OR $2)))
			ORDER BY random()
			LIMIT 1
		`, photoIsPublicSQL("p.photoid")), exhibitionID, canSeePrivate, currentUser, hasPhotoView)
	} else {
		row = h.DB.QueryRow(ctx, fmt.Sprintf(`
			SELECT
				p.photoid, p.image_url, p.image_width, p.image_height,
				COALESCE(p.title_text, ''), COALESCE(p.title_userid::text, ''),
				COALESCE(tu.username, ''),  p.owner_userid::text,
				COALESCE(p.description, '')
			FROM  photos p
			LEFT  JOIN users tu ON tu.userid = p.title_userid
			WHERE p.photoid = $1 AND p.deleted_at IS NULL
			  AND ($2 = '' OR p.exhibitionid::text = $2)
			  AND (($4 <> '' AND p.owner_userid::text = $4) OR ($5 AND (%s OR $3)))
		`, photoIsPublicSQL("p.photoid")), photoid, exhibitionID, canSeePrivate, currentUser, hasPhotoView)
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
		slog.Error("ServeHTTP", "error", err)
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
		slog.Error("ServeHTTP", "error", err)
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
		slog.Error("ServeHTTP", "error", err)
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
		related, err = fetchRelatedByLabel(ctx, h.DB, photoid, labelID, exhibitionID, canSeePrivate, hasPhotoView, currentUser)
	} else {
		related, err = fetchRelated(ctx, h.DB, photoid, exhibitionID, canSeePrivate, hasPhotoView, currentUser)
	}
	if err != nil {
		slog.Error("ServeHTTP", "error", err)
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

// ListPhotosHandler handles GET /api/v1/photos?limit=N&offset=N
// Returns a paginated list of photos for the exhibition wall.
// Permission-checks PermPrivatePhotoView to include private photos.
type ListPhotosHandler struct {
	DB      *db.Pool
	Checker *permissions.Checker
}

func (h *ListPhotosHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	canSeePrivate, _ := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPrivatePhotoView)
	hasPhotoView, _ := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPhotoView)

	limit := 40
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	// Visibility (see permissions package doc's "Photo visibility" section):
	// same owner-can-always-see-their-own-photo exception as
	// PhotoHandler.ServeHTTP above — otherwise a just-uploaded (private by
	// default) photo would never appear in the uploader's own wall/gallery
	// view unless they separately held PermPhotoView/PermPrivatePhotoView.
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT p.photoid::text, p.image_url, p.image_width, p.image_height,
		       COUNT(*) OVER() AS total
		FROM   photos p
		WHERE  p.deleted_at IS NULL
		  AND  ($1 = '' OR p.exhibitionid::text = $1)
		  AND  (($3 <> '' AND p.owner_userid::text = $3) OR ($4 AND (%s OR $2)))
		ORDER  BY p.created_at DESC, p.photoid
		LIMIT  $5 OFFSET $6
	`, photoIsPublicSQL("p.photoid")), exhibitionID, canSeePrivate, userID, hasPhotoView, limit, offset)
	if err != nil {
		slog.Error("ListPhotos", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	total := 0
	photos := []models.PhotoListItem{}
	for rows.Next() {
		var p models.PhotoListItem
		if err := rows.Scan(&p.PhotoID, &p.ImageURL, &p.Width, &p.Height, &total); err != nil {
			slog.Error("ListPhotos scan", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		p.ImageURL = proxyImageURL(p.ImageURL)
		photos = append(photos, p)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ListPhotos", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, models.PhotoListResponse{
		Total:  total,
		Offset: offset,
		Limit:  limit,
		Photos: photos,
	})
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
		slog.Error("ServeHTTP", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, u)
}

// PatchPhotoHandler handles PATCH /api/v1/photo?photoid=<id>
// Allows the title owner or photo owner to update the title/description.
// Users holding PermPhotoDescriptionModify may edit any photo's title.
type PatchPhotoHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
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
		slog.Error("ServeHTTP", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if currentUser != ownerID && currentUser != titleUserID {
		exhibitionID, _ := resolvePhotoExhibition(ctx, h.DB, photoid)
		if ok, _ := h.Checker.Check(ctx, currentUser, exhibitionID, "", "", "", permissions.PermPhotoDescriptionModify); !ok {
			middleware.WriteError(w, http.StatusForbidden, "not allowed to edit this title")
			return
		}
	}

	_, err = h.DB.Exec(ctx, `
		UPDATE photos
		SET    title_text = $1, title_userid = $2, updated_at = NOW()
		WHERE  photoid = $3
	`, title, currentUser, photoid)
	if err != nil {
		slog.Error("ServeHTTP", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]string{"title": title})
}
