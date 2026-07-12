package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

type LabelsHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// fetchLabelNameInfo returns a label name's color override and restricted
// flag from label_names (Phase 5a/5b). A name with no row at all has no
// color override and is not restricted — both columns' zero values are
// exactly the right defaults, and a name only ever gains a row once
// something explicitly marks it (EXIF import, --restrict-labels, or the
// PATCH /api/v1/label-names endpoint below).
func fetchLabelNameInfo(ctx context.Context, pool *db.Pool, name string) (colorHex *string, restricted bool, err error) {
	err = pool.QueryRow(ctx, `SELECT color_hex, restricted FROM label_names WHERE name = $1`, name).
		Scan(&colorHex, &restricted)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	return colorHex, restricted, err
}

// isLabelNameRestricted reports whether name has been marked restricted.
func isLabelNameRestricted(ctx context.Context, pool *db.Pool, name string) (bool, error) {
	_, restricted, err := fetchLabelNameInfo(ctx, pool, name)
	return restricted, err
}

var hexColorRe = regexp.MustCompile(`(?i)^#[0-9a-f]{6}$`)

// GET /api/v1/labels?photoid=&offset=&limit=
func (h *LabelsHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}

	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPhotoLabelView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	labels, total, err := fetchLabels(ctx, h.DB, photoid, offset, limit)
	if err != nil {
		slog.Error("List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	baseURL := fmt.Sprintf("/api/v1/labels?photoid=%s&limit=%d", photoid, limit)
	middleware.WriteJSON(w, http.StatusOK, models.LabelsResponse{
		PhotoID: photoid,
		Offset:  offset,
		Pages:   buildPages(total, offset, limit, baseURL),
		Labels:  labels,
	})
}

// POST /api/v1/labels?photoid=  (requires auth)
func (h *LabelsHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}
	userID := middleware.MustUserID(ctx)

	var req models.AddLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" || req.Value == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name and value are required")
		return
	}

	// Resolve exhibition for this photo (also verifies photo exists).
	exhibitionID, err := resolvePhotoExhibition(ctx, h.DB, photoid)
	if err != nil {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}

	// Require PhotoLabelCreate permission scoped to this photo.
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourcePhoto, photoid, permissions.PermPhotoLabelCreate); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Phase 5a/5b: look up this name's color override and restricted flag —
	// the latter gates who may proceed, the former just rides along on the
	// response so the frontend doesn't need a second round-trip.
	colorHex, restricted, err := fetchLabelNameInfo(ctx, h.DB, req.Name)
	if err != nil {
		slog.Error("Create", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if restricted {
		if ok, _ := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin); !ok {
			middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is restricted", req.Name))
			return
		}
	}

	var labelid string
	err = h.DB.QueryRow(ctx, `
		INSERT INTO labels (photoid, added_by_userid, name, value)
		VALUES ($1, $2, $3, $4)
		RETURNING labelid::text
	`, photoid, userID, req.Name, req.Value).Scan(&labelid)
	if err != nil {
		slog.Error("Create", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	var username string
	_ = h.DB.QueryRow(ctx, `SELECT username FROM users WHERE userid=$1`, userID).Scan(&username)

	middleware.WriteJSON(w, http.StatusCreated, models.Label{
		LabelID:    labelid,
		Name:       req.Name,
		Value:      req.Value,
		UserID:     userID,
		Username:   username,
		ColorHex:   colorHex,
		Restricted: restricted,
	})
}

// PATCH /api/v1/labels/:labelid  (requires auth; only the creator may edit)
func (h *LabelsHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	labelid := ps.ByName("labelid")
	userID := middleware.MustUserID(r.Context())

	var req models.UpdateLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == nil && req.Value == nil {
		middleware.WriteError(w, http.StatusBadRequest, "name or value required")
		return
	}

	ctx := r.Context()

	// Fetch existing label, ownership, and photoid for permission resolution.
	var existingName, existingValue, ownerID, photoid string
	err := h.DB.QueryRow(ctx, `
		SELECT name, value, added_by_userid::text, photoid::text
		FROM   labels
		WHERE  labelid = $1 AND deleted_at IS NULL
	`, labelid).Scan(&existingName, &existingValue, &ownerID, &photoid)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "label not found")
		return
	}
	if err != nil {
		slog.Error("Update", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ownerID != userID {
		exhibitionID, _ := resolvePhotoExhibition(ctx, h.DB, photoid)
		if ok, _ := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermLabelAdmin); !ok {
			middleware.WriteError(w, http.StatusForbidden, "you may only edit your own labels")
			return
		}
	}

	// Phase 5b: even the label's own creator may not modify it once its name
	// is restricted — only Admin/LabelAdmin may. Checks both the label's
	// current name and, if this request renames it, the new name too.
	namesToCheck := []string{existingName}
	if req.Name != nil && *req.Name != existingName {
		namesToCheck = append(namesToCheck, *req.Name)
	}
	for _, n := range namesToCheck {
		restricted, err := isLabelNameRestricted(ctx, h.DB, n)
		if err != nil {
			slog.Error("Update", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		if !restricted {
			continue
		}
		exhibitionID, _ := resolvePhotoExhibition(ctx, h.DB, photoid)
		if ok, _ := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin); !ok {
			middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is restricted", n))
			return
		}
	}

	newName := existingName
	if req.Name != nil {
		newName = *req.Name
	}
	newValue := existingValue
	if req.Value != nil {
		newValue = *req.Value
	}

	_, err = h.DB.Exec(ctx, `
		UPDATE labels SET name=$1, value=$2 WHERE labelid=$3
	`, newName, newValue, labelid)
	if err != nil {
		slog.Error("Update", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	var username string
	_ = h.DB.QueryRow(ctx, `SELECT username FROM users WHERE userid=$1`, userID).Scan(&username)

	colorHex, restricted, err := fetchLabelNameInfo(ctx, h.DB, newName)
	if err != nil {
		slog.Error("Update", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, models.Label{
		LabelID:    labelid,
		Name:       newName,
		Value:      newValue,
		UserID:     userID,
		Username:   username,
		ColorHex:   colorHex,
		Restricted: restricted,
	})
}

// DELETE /api/v1/labels/:labelid  (requires auth; only the creator may delete)
func (h *LabelsHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	labelid := ps.ByName("labelid")
	userID := middleware.MustUserID(r.Context())

	ctx := r.Context()

	var ownerID, photoid, name string
	err := h.DB.QueryRow(ctx, `
		SELECT added_by_userid::text, photoid::text, name FROM labels WHERE labelid=$1 AND deleted_at IS NULL
	`, labelid).Scan(&ownerID, &photoid, &name)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "label not found")
		return
	}
	if err != nil {
		slog.Error("Delete", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ownerID != userID {
		exhibitionID, _ := resolvePhotoExhibition(ctx, h.DB, photoid)
		if ok, _ := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermLabelAdmin); !ok {
			middleware.WriteError(w, http.StatusForbidden, "you may only delete your own labels")
			return
		}
	}

	// Phase 5b: even the label's own creator may not delete it once its name
	// is restricted — only Admin/LabelAdmin may.
	if restricted, err := isLabelNameRestricted(ctx, h.DB, name); err != nil {
		slog.Error("Delete", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if restricted {
		exhibitionID, _ := resolvePhotoExhibition(ctx, h.DB, photoid)
		if ok, _ := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin); !ok {
			middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is restricted", name))
			return
		}
	}

	_, err = h.DB.Exec(ctx, `
		UPDATE labels SET deleted_at=NOW() WHERE labelid=$1
	`, labelid)
	if err != nil {
		slog.Error("Delete", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/label-names  — distinct label names across all non-deleted
// labels, annotated with each name's color override (Phase 5a) and
// restricted flag (Phase 5b) where set.
func (h *LabelsHandler) Names(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	rows, err := h.DB.Query(r.Context(), `
		SELECT DISTINCT l.name, ln.color_hex, COALESCE(ln.restricted, FALSE)
		FROM   labels l
		LEFT   JOIN label_names ln ON ln.name = l.name
		WHERE  l.deleted_at IS NULL
		ORDER  BY l.name
	`)
	if err != nil {
		slog.Error("Names", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	names := []models.LabelNameInfo{}
	for rows.Next() {
		var n models.LabelNameInfo
		if err := rows.Scan(&n.Name, &n.ColorHex, &n.Restricted); err == nil {
			names = append(names, n)
		}
	}
	middleware.WriteJSON(w, http.StatusOK, map[string][]models.LabelNameInfo{"names": names})
}

// PATCH /api/v1/label-names?name=<name>  (requires Admin or LabelAdmin)
// Body: { "color_hex": "#rrggbb" | "" , "restricted": bool } — either or
// both fields; only the fields present in the request are changed. An empty
// string for color_hex clears the override (falling back to the client-side
// hash-based color again).
func (h *LabelsHandler) UpdateName(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req struct {
		ColorHex   *string `json:"color_hex"`
		Restricted *bool   `json:"restricted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ColorHex == nil && req.Restricted == nil {
		middleware.WriteError(w, http.StatusBadRequest, "color_hex or restricted is required")
		return
	}

	hasColor := req.ColorHex != nil
	var colorVal *string
	if hasColor && *req.ColorHex != "" {
		if !hexColorRe.MatchString(*req.ColorHex) {
			middleware.WriteError(w, http.StatusBadRequest, "color_hex must look like #rrggbb")
			return
		}
		colorVal = req.ColorHex
	}
	// hasColor && *req.ColorHex == "" leaves colorVal nil — i.e. "clear it".

	hasRestricted := req.Restricted != nil
	var restrictedVal bool
	if hasRestricted {
		restrictedVal = *req.Restricted
	}

	if _, err := h.DB.Exec(ctx, `
		INSERT INTO label_names (name, color_hex, restricted)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET
			color_hex  = CASE WHEN $4 THEN $2 ELSE label_names.color_hex  END,
			restricted = CASE WHEN $5 THEN $3 ELSE label_names.restricted END,
			updated_at = NOW()
	`, name, colorVal, restrictedVal, hasColor, hasRestricted); err != nil {
		slog.Error("UpdateName", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	colorHex, restricted, err := fetchLabelNameInfo(ctx, h.DB, name)
	if err != nil {
		slog.Error("UpdateName", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	middleware.WriteJSON(w, http.StatusOK, models.LabelNameInfo{
		Name:       name,
		ColorHex:   colorHex,
		Restricted: restricted,
	})
}

// GET /api/v1/label-values?name=  — distinct values for a given label name.
func (h *LabelsHandler) Values(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	name := r.URL.Query().Get("name")
	if name == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	rows, err := h.DB.Query(r.Context(), `
		SELECT DISTINCT value FROM labels WHERE name=$1 AND deleted_at IS NULL ORDER BY value
	`, name)
	if err != nil {
		slog.Error("Values", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	values := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err == nil {
			values = append(values, v)
		}
	}
	middleware.WriteJSON(w, http.StatusOK, map[string][]string{"values": values})
}
