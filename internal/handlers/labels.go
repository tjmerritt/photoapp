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

type LabelsHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

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

	// TODO(phase-5b): if label_names.restricted = TRUE for req.Name, also require PermLabelAdmin.

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
		LabelID:  labelid,
		Name:     req.Name,
		Value:    req.Value,
		UserID:   userID,
		Username: username,
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

	middleware.WriteJSON(w, http.StatusOK, models.Label{
		LabelID:  labelid,
		Name:     newName,
		Value:    newValue,
		UserID:   userID,
		Username: username,
	})
}

// DELETE /api/v1/labels/:labelid  (requires auth; only the creator may delete)
func (h *LabelsHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	labelid := ps.ByName("labelid")
	userID := middleware.MustUserID(r.Context())

	ctx := r.Context()

	var ownerID, photoid string
	err := h.DB.QueryRow(ctx, `
		SELECT added_by_userid::text, photoid::text FROM labels WHERE labelid=$1 AND deleted_at IS NULL
	`, labelid).Scan(&ownerID, &photoid)
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

// GET /api/v1/label-names  — distinct label names across all non-deleted labels.
func (h *LabelsHandler) Names(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	rows, err := h.DB.Query(r.Context(), `
		SELECT DISTINCT name FROM labels WHERE deleted_at IS NULL ORDER BY name
	`)
	if err != nil {
		slog.Error("Names", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	middleware.WriteJSON(w, http.StatusOK, map[string][]string{"names": names})
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
