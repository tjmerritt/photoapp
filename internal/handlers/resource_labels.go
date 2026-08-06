package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

// ResourceLabelsHandler powers labels on Galleries, Displays, and
// Exhibitions (PLAN2.md Phase 3b) — Photos already have their own
// labels/LabelsHandler (internal/handlers/labels.go), which this
// deliberately does not touch; see migrations/025_resource_labels.sql for
// why a separate table (and this separate handler) exists instead of
// generalizing the photo one.
//
// label_names (the exhibition-scoped color/restricted/enabled catalog) is
// reused as-is — fetchLabelNameInfo (labels.go) already takes a bare
// exhibitionID + name with no photo-specific assumptions, so a "Featured"
// label looks the same whether it's on a photo, a gallery, a display, or
// the exhibition itself.
//
// Unlike GroupsHandler, every resource type here is exhibition-scoped (a
// Gallery/Display's own exhibitionid, or the Exhibition's own id) — there's
// no organization-tier branch to make, since labeling one specific
// exhibition doesn't need a container the way grouping several exhibitions
// together does.
type ResourceLabelsHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

type resourceLabel struct {
	LabelID       string  `json:"labelid"`
	Name          string  `json:"name"`
	Value         string  `json:"value"`
	AddedByUserID string  `json:"added_by_userid"`
	Username      string  `json:"username"`
	ColorHex      *string `json:"color_hex,omitempty"`
	Restricted    bool    `json:"restricted"`
	CreatedAt     string  `json:"created_at"`
}

func isValidResourceLabelType(s string) bool {
	switch s {
	case GroupResourceGallery, GroupResourceDisplay, GroupResourceExhibition:
		return true
	default:
		return false
	}
}

// resolveResourceLabelExhibition returns the exhibitionid a Gallery/Display/
// Exhibition resource belongs to (for Exhibition, that's the resource
// itself), used both to scope the label_names lookup and the permission
// check. Returns pgx.ErrNoRows if the resource doesn't exist or is
// soft-deleted. Not cached like resolvePhotoExhibition/resolveDisplayGallery
// in resolve.go — these endpoints are low-traffic admin/editorial actions,
// not hot paths.
func resolveResourceLabelExhibition(ctx context.Context, pool *db.Pool, resourceType, resourceRef string) (string, error) {
	var query string
	switch resourceType {
	case GroupResourceGallery:
		query = `SELECT exhibitionid::text FROM galleries WHERE galleryid = $1::uuid AND deleted_at IS NULL`
	case GroupResourceDisplay:
		query = `
			SELECT g.exhibitionid::text FROM displays d JOIN galleries g ON g.galleryid = d.galleryid
			WHERE  d.displayid = $1::uuid AND d.deleted_at IS NULL AND g.deleted_at IS NULL`
	default: // GroupResourceExhibition
		query = `SELECT exhibitionid::text FROM exhibitions WHERE exhibitionid = $1::uuid AND deleted_at IS NULL`
	}
	var exhibitionID string
	err := pool.QueryRow(ctx, query, resourceRef).Scan(&exhibitionID)
	return exhibitionID, err
}

// checkResourceLabelAccess applies the permission tier appropriate for
// resourceType: Gallery/Display labels ride on that resource's own
// View/Modify permission (labeling it is treated as a facet of editing it,
// the same way a title or sort_order change would be), and Exhibition
// labels require PermAdmin — there's no dedicated "exhibition view/modify"
// permission today (ExhibitionsHandler has no Update endpoint at all yet),
// and treating exhibition-level tagging as an admin action is the safer
// default until that changes.
func (h *ResourceLabelsHandler) checkResourceLabelAccess(ctx context.Context, userID, exhibitionID, resourceType string, write bool) (bool, error) {
	switch resourceType {
	case GroupResourceGallery:
		if write {
			return h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermGalleryModify)
		}
		return h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermGalleryView)
	case GroupResourceDisplay:
		if write {
			return h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermDisplayModify)
		}
		return h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermDisplayView)
	default: // GroupResourceExhibition
		return h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin)
	}
}

// GET /api/v1/resource-labels?resource_type=&resource_ref=
func (h *ResourceLabelsHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	resourceType := strings.TrimSpace(r.URL.Query().Get("resource_type"))
	resourceRef := strings.TrimSpace(r.URL.Query().Get("resource_ref"))
	if !isValidResourceLabelType(resourceType) {
		middleware.WriteError(w, http.StatusBadRequest, "resource_type must be one of Gallery, Display, Exhibition")
		return
	}
	if resourceRef == "" {
		middleware.WriteError(w, http.StatusBadRequest, "resource_ref is required")
		return
	}

	userID, _ := middleware.UserID(ctx)
	exhibitionID, err := resolveResourceLabelExhibition(ctx, h.DB, resourceType, resourceRef)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "resource not found")
		return
	}
	if err != nil {
		slog.Error("ResourceLabels.List resolve", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ok, err := h.checkResourceLabelAccess(ctx, userID, exhibitionID, resourceType, false); err != nil {
		slog.Error("ResourceLabels.List check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	rows, err := h.DB.Query(ctx, `
		SELECT l.labelid::text, l.name, l.value,
		       l.added_by_userid::text, u.username,
		       ln.color_hex, COALESCE(ln.restricted, FALSE),
		       l.created_at::text
		FROM   resource_labels l
		JOIN   users u ON u.userid = l.added_by_userid
		LEFT   JOIN label_names ln ON ln.name = l.name AND ln.exhibitionid = $3
		WHERE  l.resource_type = $1 AND l.resource_ref = $2 AND l.deleted_at IS NULL
		ORDER  BY l.created_at
	`, resourceType, resourceRef, exhibitionID)
	if err != nil {
		slog.Error("ResourceLabels.List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	labels := make([]resourceLabel, 0)
	for rows.Next() {
		var l resourceLabel
		if err := rows.Scan(&l.LabelID, &l.Name, &l.Value, &l.AddedByUserID, &l.Username,
			&l.ColorHex, &l.Restricted, &l.CreatedAt); err != nil {
			slog.Error("ResourceLabels.List", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		labels = append(labels, l)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ResourceLabels.List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{"labels": labels})
}

// POST /api/v1/resource-labels?resource_type=&resource_ref=
// Body: {"name": "...", "value": "..."}
func (h *ResourceLabelsHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	resourceType := strings.TrimSpace(r.URL.Query().Get("resource_type"))
	resourceRef := strings.TrimSpace(r.URL.Query().Get("resource_ref"))
	if !isValidResourceLabelType(resourceType) {
		middleware.WriteError(w, http.StatusBadRequest, "resource_type must be one of Gallery, Display, Exhibition")
		return
	}
	if resourceRef == "" {
		middleware.WriteError(w, http.StatusBadRequest, "resource_ref is required")
		return
	}
	userID := middleware.MustUserID(ctx)

	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" || req.Value == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name and value are required")
		return
	}

	exhibitionID, err := resolveResourceLabelExhibition(ctx, h.DB, resourceType, resourceRef)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "resource not found")
		return
	}
	if err != nil {
		slog.Error("ResourceLabels.Create resolve", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ok, err := h.checkResourceLabelAccess(ctx, userID, exhibitionID, resourceType, true); err != nil {
		slog.Error("ResourceLabels.Create check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// A restricted label name (label_names.restricted) requires PermLabelAdmin
	// to attach here too — same rule PhotoLabel Create enforces
	// (internal/handlers/labels.go), applied to the same shared,
	// exhibition-scoped label_names catalog.
	_, restricted, enabled, err := fetchLabelNameInfo(ctx, h.DB, exhibitionID, req.Name)
	if err != nil {
		slog.Error("ResourceLabels.Create fetchLabelNameInfo", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !enabled {
		middleware.WriteError(w, http.StatusForbidden, "this label name is disabled")
		return
	}
	if restricted {
		if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin); err != nil {
			slog.Error("ResourceLabels.Create restricted check", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		} else if !ok {
			middleware.WriteError(w, http.StatusForbidden, "this label name is restricted")
			return
		}
	}

	var labelID string
	err = h.DB.QueryRow(ctx, `
		INSERT INTO resource_labels (resource_type, resource_ref, added_by_userid, name, value)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING labelid::text
	`, resourceType, resourceRef, userID, req.Name, req.Value).Scan(&labelID)
	if err != nil {
		slog.Error("ResourceLabels.Create", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusCreated, map[string]string{"labelid": labelID})
}

// DELETE /api/v1/resource-labels/:labelid
// Soft-deletes the label. resource_type/resource_ref (and therefore the
// admin-check tier) are resolved from the label row itself rather than
// requiring the caller to pass them, so this can't be tricked into checking
// permission against the wrong resource.
func (h *ResourceLabelsHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	labelID := ps.ByName("labelid")
	userID, _ := middleware.UserID(ctx)

	var resourceType, resourceRef string
	err := h.DB.QueryRow(ctx, `
		SELECT resource_type, resource_ref FROM resource_labels WHERE labelid = $1 AND deleted_at IS NULL
	`, labelID).Scan(&resourceType, &resourceRef)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "label not found")
		return
	}
	if err != nil {
		slog.Error("ResourceLabels.Delete lookup", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	exhibitionID, err := resolveResourceLabelExhibition(ctx, h.DB, resourceType, resourceRef)
	if err == pgx.ErrNoRows {
		// The label outlived its resource (e.g. the gallery was deleted
		// after the label was attached) — fall back to requiring PermAdmin
		// rather than 404ing a label that genuinely still exists.
		if ok, err := h.Checker.HasAny(ctx, userID, "", permissions.PermAdmin); err != nil || !ok {
			middleware.WriteError(w, http.StatusForbidden, "forbidden")
			return
		}
	} else if err != nil {
		slog.Error("ResourceLabels.Delete resolve", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if ok, err := h.checkResourceLabelAccess(ctx, userID, exhibitionID, resourceType, true); err != nil {
		slog.Error("ResourceLabels.Delete check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	if _, err := h.DB.Exec(ctx, `UPDATE resource_labels SET deleted_at = NOW() WHERE labelid = $1`, labelID); err != nil {
		slog.Error("ResourceLabels.Delete", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
