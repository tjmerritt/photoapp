package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
)

// AdminHandler handles admin-only photo management endpoints.
type AdminHandler struct {
	DB  *db.Pool
	Cfg *config.Config
}

// adminPhoto is the shape returned by GET /api/v1/admin/photos.
type adminPhoto struct {
	PhotoID  string `json:"photoid"`
	ImageURL string `json:"imageurl"`
	Title    string `json:"title"`
	IsPublic bool   `json:"is_public"`
}

// GET /api/v1/admin/photos?offset=&limit=
// Returns all photos (across all exhibitions) with their is_public flag.
// Requires: authenticated + authorized_non_public.
func (h *AdminHandler) ListPhotos(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if !middleware.AuthorizedNonPublic(r.Context()) {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	offset, limit := parsePage(r, 50, 200)

	var total int
	if err := h.DB.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM photos WHERE deleted_at IS NULL`,
	).Scan(&total); err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rows, err := h.DB.Query(r.Context(), `
		SELECT p.photoid::text,
		       p.image_url,
		       COALESCE(p.title_text, ''),
		       p.is_public
		FROM   photos p
		WHERE  p.deleted_at IS NULL
		ORDER  BY p.created_at DESC
		LIMIT  $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	photos := make([]adminPhoto, 0, limit)
	for rows.Next() {
		var p adminPhoto
		if err := rows.Scan(&p.PhotoID, &p.ImageURL, &p.Title, &p.IsPublic); err != nil {
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		p.ImageURL = proxyImageURL(p.ImageURL)
		photos = append(photos, p)
	}
	if err := rows.Err(); err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"photos": photos,
	})
}

// PATCH /api/v1/admin/photo?photoid=
// Body: {"is_public": true|false}
// Requires: authenticated + authorized_non_public.
func (h *AdminHandler) SetPublic(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if !middleware.AuthorizedNonPublic(r.Context()) {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}

	var body struct {
		IsPublic bool `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ct, err := h.DB.Exec(r.Context(), `
		UPDATE photos SET is_public = $1, updated_at = NOW()
		WHERE  photoid = $2 AND deleted_at IS NULL
	`, body.IsPublic, photoid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
