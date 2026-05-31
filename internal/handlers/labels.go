package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
)

type LabelsHandler struct {
	DB  *db.Pool
	Cfg *config.Config
}

// GET /api/v1/labels?photoid=&offset=&limit=
func (h *LabelsHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	labels, total, err := fetchLabels(r.Context(), h.DB, photoid, offset, limit)
	if err != nil {
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
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}
	userID := middleware.MustUserID(r.Context())

	var req models.AddLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" || req.Value == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name and value are required")
		return
	}

	// Verify photo exists
	var exists bool
	_ = h.DB.QueryRow(r.Context(), `SELECT TRUE FROM photos WHERE photoid=$1 AND deleted_at IS NULL`, photoid).Scan(&exists)
	if !exists {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}

	var labelid string
	err := h.DB.QueryRow(r.Context(), `
		INSERT INTO labels (photoid, added_by_userid, name, value)
		VALUES ($1, $2, $3, $4)
		RETURNING labelid::text
	`, photoid, userID, req.Name, req.Value).Scan(&labelid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	var username string
	_ = h.DB.QueryRow(r.Context(), `SELECT username FROM users WHERE userid=$1`, userID).Scan(&username)

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

	// Fetch existing label and check ownership
	var existingName, existingValue, ownerID string
	err := h.DB.QueryRow(r.Context(), `
		SELECT name, value, added_by_userid::text
		FROM   labels
		WHERE  labelid = $1 AND deleted_at IS NULL
	`, labelid).Scan(&existingName, &existingValue, &ownerID)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "label not found")
		return
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ownerID != userID {
		middleware.WriteError(w, http.StatusForbidden, "you may only edit your own labels")
		return
	}

	newName := existingName
	if req.Name != nil {
		newName = *req.Name
	}
	newValue := existingValue
	if req.Value != nil {
		newValue = *req.Value
	}

	_, err = h.DB.Exec(r.Context(), `
		UPDATE labels SET name=$1, value=$2 WHERE labelid=$3
	`, newName, newValue, labelid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	var username string
	_ = h.DB.QueryRow(r.Context(), `SELECT username FROM users WHERE userid=$1`, userID).Scan(&username)

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

	var ownerID string
	err := h.DB.QueryRow(r.Context(), `
		SELECT added_by_userid::text FROM labels WHERE labelid=$1 AND deleted_at IS NULL
	`, labelid).Scan(&ownerID)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "label not found")
		return
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ownerID != userID {
		middleware.WriteError(w, http.StatusForbidden, "you may only delete your own labels")
		return
	}

	_, err = h.DB.Exec(r.Context(), `
		UPDATE labels SET deleted_at=NOW() WHERE labelid=$1
	`, labelid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
