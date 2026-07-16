package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

type GalleriesHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// GET /api/v1/galleries?offset=&limit=
func (h *GalleriesHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermGalleryView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	var total int
	if err := h.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM galleries
		WHERE  exhibitionid = $1 AND deleted_at IS NULL
	`, exhibitionID).Scan(&total); err != nil {
		slog.Error("List galleries", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rows, err := h.DB.Query(ctx, `
		SELECT g.galleryid::text, g.title, g.sort_order,
		       COUNT(d.displayid) FILTER (WHERE d.deleted_at IS NULL) AS display_count,
		       g.created_at, g.updated_at
		FROM   galleries g
		LEFT   JOIN displays d ON d.galleryid = g.galleryid
		WHERE  g.exhibitionid = $1 AND g.deleted_at IS NULL
		GROUP  BY g.galleryid
		ORDER  BY g.sort_order, g.created_at
		LIMIT  $2 OFFSET $3
	`, exhibitionID, limit, offset)
	if err != nil {
		slog.Error("List galleries", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	galleries := []models.GallerySummary{}
	for rows.Next() {
		var g models.GallerySummary
		if err := rows.Scan(&g.GalleryID, &g.Title, &g.SortOrder, &g.DisplayCount, &g.CreatedAt, &g.UpdatedAt); err != nil {
			slog.Error("List galleries", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		galleries = append(galleries, g)
	}
	if err := rows.Err(); err != nil {
		slog.Error("List galleries", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	baseURL := fmt.Sprintf("/api/v1/galleries?limit=%d", limit)
	middleware.WriteJSON(w, http.StatusOK, models.GalleriesResponse{
		ExhibitionID: exhibitionID,
		Offset:       offset,
		Pages:        buildPages(total, offset, limit, baseURL),
		Galleries:    galleries,
	})
}

// POST /api/v1/galleries  (requires auth + GalleryCreate)
func (h *GalleriesHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermGalleryCreate); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req models.CreateGalleryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Title == "" {
		middleware.WriteError(w, http.StatusBadRequest, "title is required")
		return
	}

	sortOrder := 0
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}

	var g models.GallerySummary
	if err := h.DB.QueryRow(ctx, `
		INSERT INTO galleries (exhibitionid, title, sort_order)
		VALUES ($1, $2, $3)
		RETURNING galleryid::text, title, sort_order, 0, created_at, updated_at
	`, exhibitionID, req.Title, sortOrder).Scan(
		&g.GalleryID, &g.Title, &g.SortOrder, &g.DisplayCount, &g.CreatedAt, &g.UpdatedAt,
	); err != nil {
		slog.Error("Create gallery", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusCreated, g)
}

// GET /api/v1/galleries/:galleryid
func (h *GalleriesHandler) Get(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	galleryID := ps.ByName("galleryid")
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourceGallery, galleryID, permissions.PermGalleryView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Fetch gallery + placard defaults (LEFT JOIN: not all galleries have defaults).
	var g models.GalleryDetail
	var placardJSON *string
	err := h.DB.QueryRow(ctx, `
		SELECT g.galleryid::text, g.title, g.sort_order,
		       pd.defaults::text,
		       g.created_at, g.updated_at
		FROM   galleries g
		LEFT   JOIN placard_defaults pd ON pd.galleryid = g.galleryid
		WHERE  g.galleryid = $1 AND g.exhibitionid = $2 AND g.deleted_at IS NULL
	`, galleryID, exhibitionID).Scan(
		&g.GalleryID, &g.Title, &g.SortOrder,
		&placardJSON,
		&g.CreatedAt, &g.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "gallery not found")
		return
	}
	if err != nil {
		slog.Error("Get gallery", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if placardJSON != nil {
		g.PlacardsDefault = json.RawMessage(*placardJSON)
	}

	// Fetch display summaries ordered by sort_order.
	rows, err := h.DB.Query(ctx, `
		SELECT d.displayid::text, d.sort_order,
		       t.templateid::text, t.name, t.photo_count,
		       COUNT(s.slotid)                                          AS slot_count,
		       COUNT(s.slotid) FILTER (WHERE s.photoid IS NOT NULL)     AS filled_slots,
		       d.created_at, d.updated_at
		FROM   displays d
		LEFT   JOIN display_templates t ON t.templateid = d.templateid AND t.deleted_at IS NULL
		LEFT   JOIN display_slots     s ON s.displayid  = d.displayid
		WHERE  d.galleryid = $1 AND d.deleted_at IS NULL
		GROUP  BY d.displayid, t.templateid
		ORDER  BY d.sort_order, d.created_at
	`, galleryID)
	if err != nil {
		slog.Error("Get gallery displays", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	g.Displays = []models.DisplaySummary{}
	for rows.Next() {
		var d models.DisplaySummary
		var tmplID, tmplName *string
		var tmplCount *int
		if err := rows.Scan(
			&d.DisplayID, &d.SortOrder,
			&tmplID, &tmplName, &tmplCount,
			&d.SlotCount, &d.FilledSlots,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			slog.Error("Get gallery displays scan", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		if tmplID != nil {
			d.Template = &models.TemplateSummary{
				TemplateID: *tmplID,
				Name:       *tmplName,
				PhotoCount: *tmplCount,
			}
		}
		g.Displays = append(g.Displays, d)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Get gallery displays", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, g)
}

// PATCH /api/v1/galleries/:galleryid  (requires auth + GalleryModify)
func (h *GalleriesHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	galleryID := ps.ByName("galleryid")
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourceGallery, galleryID, permissions.PermGalleryModify); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Verify gallery exists in this exhibition.
	var exists bool
	if err := h.DB.QueryRow(ctx, `
		SELECT TRUE FROM galleries WHERE galleryid=$1 AND exhibitionid=$2 AND deleted_at IS NULL
	`, galleryID, exhibitionID).Scan(&exists); err != nil {
		if err == pgx.ErrNoRows {
			middleware.WriteError(w, http.StatusNotFound, "gallery not found")
			return
		}
		slog.Error("Update gallery check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	var req models.UpdateGalleryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Apply title and sort_order updates.
	if req.Title != nil {
		if _, err := h.DB.Exec(ctx, `
			UPDATE galleries SET title=$1 WHERE galleryid=$2
		`, *req.Title, galleryID); err != nil {
			slog.Error("Update gallery title", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}
	if req.SortOrder != nil {
		if _, err := h.DB.Exec(ctx, `
			UPDATE galleries SET sort_order=$1 WHERE galleryid=$2
		`, *req.SortOrder, galleryID); err != nil {
			slog.Error("Update gallery sort_order", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}

	// Upsert placard defaults if provided (non-nil, non-null JSON).
	if len(req.PlacardsDefault) > 0 && string(req.PlacardsDefault) != "null" {
		placardStr := string(req.PlacardsDefault)
		if _, err := h.DB.Exec(ctx, `
			INSERT INTO placard_defaults (galleryid, defaults)
			VALUES ($1, $2::jsonb)
			ON CONFLICT (galleryid) DO UPDATE SET defaults = EXCLUDED.defaults
		`, galleryID, placardStr); err != nil {
			slog.Error("Update gallery placard_defaults", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}

	// Reorder displays if a new ordering is provided.
	if len(req.DisplayOrder) > 0 {
		for i, displayID := range req.DisplayOrder {
			if _, err := h.DB.Exec(ctx, `
				UPDATE displays SET sort_order=$1
				WHERE  displayid=$2 AND galleryid=$3 AND deleted_at IS NULL
			`, i, displayID, galleryID); err != nil {
				slog.Error("Update gallery display order", "error", err)
				middleware.WriteError(w, http.StatusInternalServerError, "db error")
				return
			}
		}
	}

	// Return the updated gallery detail.
	h.Get(w, r, ps)
}

// DELETE /api/v1/galleries/:galleryid  (requires auth + GalleryDelete)
func (h *GalleriesHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	galleryID := ps.ByName("galleryid")
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourceGallery, galleryID, permissions.PermGalleryDelete); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	result, err := h.DB.Exec(ctx, `
		UPDATE galleries SET deleted_at=NOW()
		WHERE  galleryid=$1 AND exhibitionid=$2 AND deleted_at IS NULL
	`, galleryID, exhibitionID)
	if err != nil {
		slog.Error("Delete gallery", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if result.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "gallery not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
