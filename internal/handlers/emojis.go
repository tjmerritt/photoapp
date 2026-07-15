package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/tjmerritt/photoapp/internal/permissions"
)

type EmojisHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// GET /api/v1/emojis?photoid=&offset=&limit=
func (h *EmojisHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}

	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPhotoEmojiView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	emojis, total, err := fetchEmojis(ctx, h.DB, photoid, offset, limit, 3)
	if err != nil {
		slog.Error("List", "error", err)
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
	ctx := r.Context()
	emojiid := r.URL.Query().Get("emoji")
	photoid := r.URL.Query().Get("photoid")
	if emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "emoji is required")
		return
	}

	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPhotoEmojiView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	var total int
	err := h.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM emoji_reactions
		WHERE emojiid=$1 AND ($2='' OR photoid::text=$2)
	`, emojiid, photoid).Scan(&total)
	if err != nil {
		slog.Error("ListUsers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	users, err := fetchEmojiUsers(ctx, h.DB, photoid, emojiid, offset, limit)
	if err != nil {
		slog.Error("ListUsers", "error", err)
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
	ctx := r.Context()
	photoid := r.URL.Query().Get("photoid")
	emojiid := r.URL.Query().Get("emojiid")
	userID := middleware.MustUserID(ctx)

	if photoid == "" || emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid and emojiid are required")
		return
	}

	exhibitionID, err := resolvePhotoExhibition(ctx, h.DB, photoid)
	if err != nil {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourcePhoto, photoid, permissions.PermPhotoEmojiCreate); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Phase 6b: per-user override — see resolve.go's userCanManageOwnEmoji doc.
	if canManage, err := userCanManageOwnEmoji(ctx, h.DB, userID); err != nil {
		slog.Error("React", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !canManage {
		middleware.WriteError(w, http.StatusForbidden, "emoji reactions have been disabled for your account")
		return
	}

	// Verify emoji type exists and is active
	var active bool
	err = h.DB.QueryRow(ctx, `SELECT is_active FROM emoji_types WHERE emojiid=$1`, emojiid).Scan(&active)
	if err == pgx.ErrNoRows || !active {
		middleware.WriteError(w, http.StatusBadRequest, "emoji not found or inactive")
		return
	}

	_, err = h.DB.Exec(ctx, `
		INSERT INTO emoji_reactions (photoid, emojiid, userid)
		VALUES ($1, $2, $3)
		ON CONFLICT (photoid, emojiid, userid) DO NOTHING
	`, photoid, emojiid, userID)
	if err != nil {
		slog.Error("React", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Refresh counts materialised view
	_ = h.DB.RefreshEmojiCounts(ctx)

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/emoji/react?photoid=&emojiid=  (requires auth)
// Removes the current user's reaction.
func (h *EmojisHandler) Unreact(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	photoid := r.URL.Query().Get("photoid")
	emojiid := r.URL.Query().Get("emojiid")
	userID := middleware.MustUserID(ctx)

	if photoid == "" || emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid and emojiid are required")
		return
	}

	exhibitionID, err := resolvePhotoExhibition(ctx, h.DB, photoid)
	if err != nil {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourcePhoto, photoid, permissions.PermPhotoEmojiDelete); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Phase 6b: per-user override — see resolve.go's userCanManageOwnEmoji doc.
	if canManage, err := userCanManageOwnEmoji(ctx, h.DB, userID); err != nil {
		slog.Error("Unreact", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !canManage {
		middleware.WriteError(w, http.StatusForbidden, "emoji reactions have been disabled for your account")
		return
	}

	ct, err := h.DB.Exec(ctx, `
		DELETE FROM emoji_reactions
		WHERE photoid=$1 AND emojiid=$2 AND userid=$3
	`, photoid, emojiid, userID)
	if err != nil {
		slog.Error("Unreact", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "reaction not found")
		return
	}

	_ = h.DB.RefreshEmojiCounts(ctx)

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/emoji/types  – paginated, searchable list of active emoji types.
//
// Query params:
//
//	search  – filter by alt_text or tags (case-insensitive substring)
//	group   – filter by emoji_group
//	offset  – pagination offset (default 0)
//	limit   – page size (default DefaultPageSize, max MaxPageSize)
func (h *EmojisHandler) ListTypes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	group := strings.TrimSpace(q.Get("group"))
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	// Build WHERE clause — only return base emojis (exclude skintone variants).
	where := "is_active = TRUE AND base_hexcode IS NULL"
	args := []any{}
	n := 1
	if search != "" {
		where += fmt.Sprintf(" AND (alt_text ILIKE $%d OR tags ILIKE $%d)", n, n)
		args = append(args, "%"+search+"%")
		n++
	}
	if group != "" {
		where += fmt.Sprintf(" AND emoji_group = $%d", n)
		args = append(args, group)
		n++
	}

	// Total count.
	var total int
	if err := h.DB.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM emoji_types WHERE "+where,
		args...,
	).Scan(&total); err != nil {
		slog.Error("ListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Page of results with has_skintones flag and global usage_count.
	// Popular-first (5c): order by total reaction count across all photos,
	// descending, falling back to alphabetical alt_text for ties — since
	// most emoji types have zero reactions, this naturally reads as
	// "frequently-used emojis first, everything else alphabetical" while
	// still being a single stable ORDER BY that's safe to paginate over.
	args = append(args, limit, offset)
	rows, err := h.DB.Query(r.Context(),
		fmt.Sprintf(`
			SELECT et.emojiid::text, et.emoji_char, et.image_url, et.alt_text,
			       et.is_active, COALESCE(et.hexcode,''),
			       EXISTS (
			           SELECT 1 FROM emoji_types v
			           WHERE v.base_hexcode = et.hexcode AND v.is_active = TRUE
			       ) AS has_skintones,
			       COALESCE(ec.usage_count, 0) AS usage_count
			FROM   emoji_types et
			LEFT JOIN (
			    SELECT emojiid, COUNT(*) AS usage_count
			    FROM   emoji_reactions
			    GROUP  BY emojiid
			) ec ON ec.emojiid = et.emojiid
			WHERE  %s
			ORDER  BY COALESCE(ec.usage_count, 0) DESC, et.alt_text ASC, et.sort_order, et.created_at
			LIMIT  $%d OFFSET $%d
		`, where, n, n+1),
		args...,
	)
	if err != nil {
		slog.Error("ListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	types := make([]models.EmojiTypeResponse, 0)
	for rows.Next() {
		var et models.EmojiTypeResponse
		if err := rows.Scan(&et.EmojiID, &et.EmojiChar, &et.ImageURL, &et.AltText,
			&et.IsActive, &et.Hexcode, &et.HasSkintones, &et.UsageCount); err != nil {
			slog.Error("ListTypes", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		et.ImageURL = proxyImageURLPtr(et.ImageURL)
		types = append(types, et)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	baseURL := fmt.Sprintf("/api/v1/emoji/types?search=%s&group=%s&limit=%d", search, group, limit)
	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"pages":  buildPages(total, offset, limit, baseURL),
		"emojis": types,
	})
}

// GET /api/v1/admin/emoji-types?search=&include_disabled=&offset=&limit=  (Phase 6d)
// Like ListTypes, but for the emoji admin page: includes inactive emoji
// types (unless include_disabled is left off), doesn't exclude skintone
// variants, and requires admin access rather than being publicly readable.
// Requires: authenticated + (PermAdmin or PermEmojiAdmin).
func (h *EmojisHandler) AdminListTypes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermEmojiAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	includeDisabled := q.Get("include_disabled") == "true"
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	where := "base_hexcode IS NULL"
	args := []any{}
	n := 1
	if !includeDisabled {
		where += " AND is_active = TRUE"
	}
	if search != "" {
		where += fmt.Sprintf(" AND (alt_text ILIKE $%d OR tags ILIKE $%d)", n, n)
		args = append(args, "%"+search+"%")
		n++
	}

	var total int
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM emoji_types WHERE "+where, args...).Scan(&total); err != nil {
		slog.Error("AdminListTypes count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rowArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT et.emojiid::text, et.emoji_char, et.image_url, et.alt_text,
		       et.is_active, COALESCE(et.hexcode,''),
		       COALESCE(ec.usage_count, 0) AS usage_count
		FROM   emoji_types et
		LEFT JOIN (
		    SELECT emojiid, COUNT(*) AS usage_count
		    FROM   emoji_reactions
		    GROUP  BY emojiid
		) ec ON ec.emojiid = et.emojiid
		WHERE  %s
		ORDER  BY et.alt_text ASC
		LIMIT  $%d OFFSET $%d
	`, where, n, n+1), rowArgs...)
	if err != nil {
		slog.Error("AdminListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	types := make([]models.EmojiTypeResponse, 0)
	for rows.Next() {
		var et models.EmojiTypeResponse
		if err := rows.Scan(&et.EmojiID, &et.EmojiChar, &et.ImageURL, &et.AltText,
			&et.IsActive, &et.Hexcode, &et.UsageCount); err != nil {
			slog.Error("AdminListTypes", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		et.ImageURL = proxyImageURLPtr(et.ImageURL)
		types = append(types, et)
	}
	if err := rows.Err(); err != nil {
		slog.Error("AdminListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"emojis": types,
	})
}

// PATCH /api/v1/admin/emoji-types/:emojiid  (Phase 6d)
// Body: { "is_active": bool }
// Requires: authenticated + (PermAdmin or PermEmojiAdmin).
func (h *EmojisHandler) AdminUpdateType(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	emojiid := ps.ByName("emojiid")
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermEmojiAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	var req struct {
		IsActive *bool `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.IsActive == nil {
		middleware.WriteError(w, http.StatusBadRequest, "is_active is required")
		return
	}

	ct, err := h.DB.Exec(ctx, `UPDATE emoji_types SET is_active = $1 WHERE emojiid = $2`, *req.IsActive, emojiid)
	if err != nil {
		slog.Error("AdminUpdateType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "emoji type not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/emoji/variants?hexcode=  — returns all skintone variants for a base emoji.
// The base emoji itself is included first so the picker can offer "no skintone" too.
func (h *EmojisHandler) ListVariants(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	hexcode := strings.TrimSpace(r.URL.Query().Get("hexcode"))
	if hexcode == "" {
		middleware.WriteError(w, http.StatusBadRequest, "hexcode is required")
		return
	}

	// Fetch the base emoji first.
	baseRows, err := h.DB.Query(r.Context(), `
		SELECT emojiid::text, emoji_char, image_url, alt_text, is_active,
		       COALESCE(hexcode,''), COALESCE(skintone,'')
		FROM   emoji_types
		WHERE  hexcode = $1 AND base_hexcode IS NULL AND is_active = TRUE
		UNION ALL
		SELECT emojiid::text, emoji_char, image_url, alt_text, is_active,
		       COALESCE(hexcode,''), COALESCE(skintone,'')
		FROM   emoji_types
		WHERE  base_hexcode = $1 AND is_active = TRUE
		ORDER  BY sort_order, created_at
	`, hexcode)
	if err != nil {
		slog.Error("ListVariants", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer baseRows.Close()

	variants := make([]models.EmojiTypeResponse, 0)
	for baseRows.Next() {
		var et models.EmojiTypeResponse
		var tone string
		if err := baseRows.Scan(&et.EmojiID, &et.EmojiChar, &et.ImageURL, &et.AltText,
			&et.IsActive, &et.Hexcode, &tone); err != nil {
			slog.Error("ListVariants", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		et.ImageURL = proxyImageURLPtr(et.ImageURL)
		if tone != "" {
			et.Skintone = &tone
		}
		variants = append(variants, et)
	}
	if err := baseRows.Err(); err != nil {
		slog.Error("ListVariants", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{"variants": variants})
}

// POST /api/v1/emoji/types  – upload a new custom emoji image (requires EmojiUpload permission)
// Accepts multipart/form-data with fields:
//   - image  : the image file (PNG, GIF, WebP recommended)
//   - alttext: accessibility label (required)
func (h *EmojisHandler) UploadType(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermEmojiUpload); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

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
		slog.Error("UploadType", "error", err)
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
		slog.Error("UploadType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not save file")
		return
	}
	defer dest.Close()

	if _, err := io.Copy(dest, file); err != nil {
		slog.Error("UploadType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not save file")
		return
	}

	imageURL := h.Cfg.UploadURLBase + "/" + filename

	// Insert into emoji_types (inactive until an admin activates it,
	// or set is_active=TRUE to allow immediate use — adjust per policy)
	var emojiid string
	err = h.DB.QueryRow(ctx, `
		INSERT INTO emoji_types (emojiid, image_url, alt_text, is_active)
		VALUES ($1, $2, $3, TRUE)
		RETURNING emojiid::text
	`, newID, imageURL, altText).Scan(&emojiid)
	if err != nil {
		slog.Error("UploadType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusCreated, models.EmojiTypeResponse{
		EmojiID:  emojiid,
		ImageURL: proxyImageURLPtr(&imageURL),
		AltText:  altText,
		IsActive: true,
	})
}
