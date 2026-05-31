package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
)

type EmojisHandler struct {
	DB  *db.Pool
	Cfg *config.Config
}

// GET /api/v1/emojis?photoid=&offset=&limit=
func (h *EmojisHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	emojis, total, err := fetchEmojis(r.Context(), h.DB, photoid, offset, limit, 3)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	baseURL := fmt.Sprintf("/api/v1/emojis?photoid=%s&limit=%d", photoid, limit)
	middleware.WriteJSON(w, http.StatusOK, models.EmojisResponse{
		PhotoID: photoid,
		Offset:  offset,
		Pages:   buildPages(total, offset, limit, baseURL),
		Emojis:  emojis,
	})
}

// GET /api/v1/emoji/users?emoji=&offset=&limit=
func (h *EmojisHandler) ListUsers(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	emojiid := r.URL.Query().Get("emoji")
	photoid := r.URL.Query().Get("photoid")
	if emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "emoji is required")
		return
	}
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	var total int
	err := h.DB.QueryRow(r.Context(), `
		SELECT COUNT(*) FROM emoji_reactions
		WHERE emojiid=$1 AND ($2='' OR photoid::text=$2)
	`, emojiid, photoid).Scan(&total)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	users, err := fetchEmojiUsers(r.Context(), h.DB, photoid, emojiid, offset, limit)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	baseURL := fmt.Sprintf("/api/v1/emoji/users?emoji=%s&photoid=%s&limit=%d", emojiid, photoid, limit)
	middleware.WriteJSON(w, http.StatusOK, models.EmojiUsersResponse{
		EmojiID: emojiid,
		Offset:  offset,
		Pages:   buildPages(total, offset, limit, baseURL),
		Users:   users,
	})
}

// POST /api/v1/emoji/react?photoid=&emojiid=  (requires auth)
// Adds the current user's reaction to a photo with the given emoji.
func (h *EmojisHandler) React(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	photoid := r.URL.Query().Get("photoid")
	emojiid := r.URL.Query().Get("emojiid")
	userID := middleware.MustUserID(r.Context())

	if photoid == "" || emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid and emojiid are required")
		return
	}

	// Verify emoji type exists and is active
	var active bool
	err := h.DB.QueryRow(r.Context(), `SELECT is_active FROM emoji_types WHERE emojiid=$1`, emojiid).Scan(&active)
	if err == pgx.ErrNoRows || !active {
		middleware.WriteError(w, http.StatusBadRequest, "emoji not found or inactive")
		return
	}

	_, err = h.DB.Exec(r.Context(), `
		INSERT INTO emoji_reactions (photoid, emojiid, userid)
		VALUES ($1, $2, $3)
		ON CONFLICT (photoid, emojiid, userid) DO NOTHING
	`, photoid, emojiid, userID)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Refresh counts materialised view
	_ = h.DB.RefreshEmojiCounts(r.Context())

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/emoji/react?photoid=&emojiid=  (requires auth)
// Removes the current user's reaction.
func (h *EmojisHandler) Unreact(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	photoid := r.URL.Query().Get("photoid")
	emojiid := r.URL.Query().Get("emojiid")
	userID := middleware.MustUserID(r.Context())

	if photoid == "" || emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid and emojiid are required")
		return
	}

	ct, err := h.DB.Exec(r.Context(), `
		DELETE FROM emoji_reactions
		WHERE photoid=$1 AND emojiid=$2 AND userid=$3
	`, photoid, emojiid, userID)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "reaction not found")
		return
	}

	_ = h.DB.RefreshEmojiCounts(r.Context())

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/emoji/types  – list all active emoji types (the picker palette)
func (h *EmojisHandler) ListTypes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	rows, err := h.DB.Query(r.Context(), `
		SELECT emojiid::text, emoji_char, image_url, alt_text, is_active
		FROM   emoji_types
		WHERE  is_active = TRUE
		ORDER  BY sort_order, created_at
	`)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	types := make([]models.EmojiTypeResponse, 0)
	for rows.Next() {
		var et models.EmojiTypeResponse
		if err := rows.Scan(&et.EmojiID, &et.EmojiChar, &et.ImageURL, &et.AltText, &et.IsActive); err != nil {
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		types = append(types, et)
	}
	if err := rows.Err(); err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, types)
}

// POST /api/v1/emoji/types  – upload a new custom emoji image (requires auth)
// Accepts multipart/form-data with fields:
//   - image  : the image file (PNG, GIF, WebP recommended)
//   - alttext: accessibility label (required)
func (h *EmojisHandler) UploadType(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	_ = middleware.MustUserID(r.Context()) // any authenticated user may upload

	if err := r.ParseMultipartForm(8 << 20); err != nil { // 8 MB max
		middleware.WriteError(w, http.StatusBadRequest, "could not parse form (max 8MB)")
		return
	}

	altText := strings.TrimSpace(r.FormValue("alttext"))
	if altText == "" {
		middleware.WriteError(w, http.StatusBadRequest, "alttext is required")
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "image file is required")
		return
	}
	defer file.Close()

	// Validate content type
	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	contentType := http.DetectContentType(buf[:n])
	allowed := map[string]string{
		"image/png":  ".png",
		"image/gif":  ".gif",
		"image/webp": ".webp",
		"image/jpeg": ".jpg",
	}
	ext, ok := allowed[contentType]
	if !ok {
		middleware.WriteError(w, http.StatusBadRequest, "image must be PNG, GIF, WebP or JPEG")
		return
	}

	// Seek back to start after sniffing content type
	if seeker, ok := file.(io.Seeker); ok {
		_, _ = seeker.Seek(0, io.SeekStart)
	}

	// Ensure upload directory exists
	if err := os.MkdirAll(h.Cfg.UploadDir, 0o755); err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "could not create upload directory")
		return
	}

	// Save file with a UUID filename to avoid collisions
	newID := uuid.New().String()
	filename := newID + ext
	_ = header // suppress unused warning
	destPath := filepath.Join(h.Cfg.UploadDir, filename)
	dest, err := os.Create(destPath)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "could not save file")
		return
	}
	defer dest.Close()

	if _, err := io.Copy(dest, file); err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "could not save file")
		return
	}

	imageURL := h.Cfg.UploadURLBase + "/" + filename

	// Insert into emoji_types (inactive until an admin activates it,
	// or set is_active=TRUE to allow immediate use — adjust per policy)
	var emojiid string
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO emoji_types (emojiid, image_url, alt_text, is_active)
		VALUES ($1, $2, $3, TRUE)
		RETURNING emojiid::text
	`, newID, imageURL, altText).Scan(&emojiid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusCreated, models.EmojiTypeResponse{
		EmojiID:  emojiid,
		ImageURL: &imageURL,
		AltText:  altText,
		IsActive: true,
	})
}
