package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
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

// adminExhibition is the shape returned by GET /api/v1/admin/exhibitions.
type adminExhibition struct {
	ExhibitionID string `json:"exhibitionid"`
	Name         string `json:"name"`
}

// GET /api/v1/admin/exhibitions
// Returns exhibitions the logged-in user is a member of.
// Requires: authenticated + authorized_non_public.
func (h *AdminHandler) ListExhibitions(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if !middleware.AuthorizedNonPublic(r.Context()) {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	userID, _ := middleware.UserID(r.Context())

	rows, err := h.DB.Query(r.Context(), `
		SELECT e.exhibitionid::text, e.name
		FROM   exhibitions e
		JOIN   user_exhibitions ue ON ue.exhibitionid = e.exhibitionid
		WHERE  ue.userid = $1
		  AND  e.deleted_at IS NULL
		ORDER  BY e.name
	`, userID)
	if err != nil {
		slog.Error("ListExhibitions", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	exhibitions := make([]adminExhibition, 0)
	for rows.Next() {
		var e adminExhibition
		if err := rows.Scan(&e.ExhibitionID, &e.Name); err != nil {
			slog.Error("ListExhibitions", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		exhibitions = append(exhibitions, e)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ListExhibitions", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"exhibitions": exhibitions,
	})
}

// GET /api/v1/admin/photos?offset=&limit=&exhibitionid=
// Returns photos, optionally filtered to a single exhibition.
// Requires: authenticated + authorized_non_public.
func (h *AdminHandler) ListPhotos(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if !middleware.AuthorizedNonPublic(r.Context()) {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	offset, limit := parsePage(r, 50, 200)
	exhibitionID := r.URL.Query().Get("exhibitionid")

	// ── Count ────────────────────────────────────────────────────────────────
	var total int
	var countErr error
	if exhibitionID != "" {
		countErr = h.DB.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM photos WHERE deleted_at IS NULL AND exhibitionid = $1::uuid`,
			exhibitionID,
		).Scan(&total)
	} else {
		countErr = h.DB.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM photos WHERE deleted_at IS NULL`,
		).Scan(&total)
	}
	if countErr != nil {
		slog.Error("ListPhotos count", "error", countErr)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// ── Rows ─────────────────────────────────────────────────────────────────
	var rows pgx.Rows
	var queryErr error
	if exhibitionID != "" {
		rows, queryErr = h.DB.Query(r.Context(), `
			SELECT p.photoid::text,
			       p.image_url,
			       COALESCE(p.title_text, ''),
			       p.is_public
			FROM   photos p
			WHERE  p.deleted_at IS NULL
			  AND  p.exhibitionid = $3::uuid
			ORDER  BY p.created_at DESC
			LIMIT  $1 OFFSET $2
		`, limit, offset, exhibitionID)
	} else {
		rows, queryErr = h.DB.Query(r.Context(), `
			SELECT p.photoid::text,
			       p.image_url,
			       COALESCE(p.title_text, ''),
			       p.is_public
			FROM   photos p
			WHERE  p.deleted_at IS NULL
			ORDER  BY p.created_at DESC
			LIMIT  $1 OFFSET $2
		`, limit, offset)
	}
	if queryErr != nil {
		slog.Error("ListPhotos", "error", queryErr)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	photos := make([]adminPhoto, 0, limit)
	for rows.Next() {
		var p adminPhoto
		if err := rows.Scan(&p.PhotoID, &p.ImageURL, &p.Title, &p.IsPublic); err != nil {
			slog.Error("ListPhotos", "error", err)
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
		slog.Error("SetPublic", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}

	// Keep the "Public" label in sync with is_public.
	publicVal := "False"
	if body.IsPublic {
		publicVal = "True"
	}
	userID, _ := middleware.UserID(r.Context())

	// Update an existing "Public" label if one exists.
	ct2, err := h.DB.Exec(r.Context(), `
		UPDATE labels
		SET    value = $1, updated_at = NOW()
		WHERE  photoid = $2 AND name = 'Public' AND deleted_at IS NULL
	`, publicVal, photoid)
	if err != nil {
		slog.Error("SetPublic update label", "error", err)
	} else if ct2.RowsAffected() == 0 {
		// No existing label – insert one.
		_, err = h.DB.Exec(r.Context(), `
			INSERT INTO labels (photoid, added_by_userid, name, value)
			VALUES ($1, $2, 'Public', $3)
		`, photoid, userID, publicVal)
		if err != nil {
			slog.Error("SetPublic insert label", "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
