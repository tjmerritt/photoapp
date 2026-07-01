package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

type TemplatesHandler struct {
	DB      *db.Pool
	Checker *permissions.Checker
}

// GET /api/v1/display-templates — list all active templates (no auth required)
func (h *TemplatesHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()

	rows, err := h.DB.Query(ctx, `
		SELECT templateid::text, name, photo_count,
		       slot_positions::text, presentation::text
		FROM   display_templates
		WHERE  deleted_at IS NULL
		ORDER  BY name
	`)
	if err != nil {
		slog.Error("List templates", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	templates := []models.DisplayTemplate{}
	for rows.Next() {
		var t models.DisplayTemplate
		var slotPos, pres string
		if err := rows.Scan(&t.TemplateID, &t.Name, &t.PhotoCount, &slotPos, &pres); err != nil {
			slog.Error("List templates scan", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		t.SlotPositions = json.RawMessage(slotPos)
		t.Presentation = json.RawMessage(pres)
		templates = append(templates, t)
	}
	if err := rows.Err(); err != nil {
		slog.Error("List templates", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, models.TemplatesResponse{Templates: templates})
}

// POST /api/v1/display-templates  (requires auth + PermAdmin)
func (h *TemplatesHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req models.CreateTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.PhotoCount <= 0 {
		middleware.WriteError(w, http.StatusBadRequest, "photo_count must be greater than 0")
		return
	}

	slotPos := "[]"
	if len(req.SlotPositions) > 0 && string(req.SlotPositions) != "null" {
		slotPos = string(req.SlotPositions)
	}
	pres := "{}"
	if len(req.Presentation) > 0 && string(req.Presentation) != "null" {
		pres = string(req.Presentation)
	}

	var t models.DisplayTemplate
	var slotPosOut, presOut string
	if err := h.DB.QueryRow(ctx, `
		INSERT INTO display_templates (name, photo_count, slot_positions, presentation)
		VALUES ($1, $2, $3::jsonb, $4::jsonb)
		RETURNING templateid::text, name, photo_count, slot_positions::text, presentation::text
	`, req.Name, req.PhotoCount, slotPos, pres).Scan(
		&t.TemplateID, &t.Name, &t.PhotoCount, &slotPosOut, &presOut,
	); err != nil {
		slog.Error("Create template", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	t.SlotPositions = json.RawMessage(slotPosOut)
	t.Presentation = json.RawMessage(presOut)

	middleware.WriteJSON(w, http.StatusCreated, t)
}

// PATCH /api/v1/display-templates/:templateid  (requires auth + PermAdmin)
func (h *TemplatesHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	templateID := ps.ByName("templateid")
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req models.UpdateTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Fetch current values to apply partial updates.
	var cur models.DisplayTemplate
	var slotPos, pres string
	if err := h.DB.QueryRow(ctx, `
		SELECT templateid::text, name, photo_count, slot_positions::text, presentation::text
		FROM   display_templates
		WHERE  templateid=$1 AND deleted_at IS NULL
	`, templateID).Scan(&cur.TemplateID, &cur.Name, &cur.PhotoCount, &slotPos, &pres); err != nil {
		if err == pgx.ErrNoRows {
			middleware.WriteError(w, http.StatusNotFound, "template not found")
			return
		}
		slog.Error("Update template fetch", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if req.Name != nil {
		cur.Name = *req.Name
	}
	if req.PhotoCount != nil {
		cur.PhotoCount = *req.PhotoCount
	}
	if len(req.SlotPositions) > 0 && string(req.SlotPositions) != "null" {
		slotPos = string(req.SlotPositions)
	}
	if len(req.Presentation) > 0 && string(req.Presentation) != "null" {
		pres = string(req.Presentation)
	}

	if cur.Name == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name must not be empty")
		return
	}
	if cur.PhotoCount <= 0 {
		middleware.WriteError(w, http.StatusBadRequest, "photo_count must be greater than 0")
		return
	}

	var t models.DisplayTemplate
	var slotPosOut, presOut string
	if err := h.DB.QueryRow(ctx, `
		UPDATE display_templates
		SET    name=$1, photo_count=$2, slot_positions=$3::jsonb, presentation=$4::jsonb
		WHERE  templateid=$5
		RETURNING templateid::text, name, photo_count, slot_positions::text, presentation::text
	`, cur.Name, cur.PhotoCount, slotPos, pres, templateID).Scan(
		&t.TemplateID, &t.Name, &t.PhotoCount, &slotPosOut, &presOut,
	); err != nil {
		slog.Error("Update template", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	t.SlotPositions = json.RawMessage(slotPosOut)
	t.Presentation = json.RawMessage(presOut)

	middleware.WriteJSON(w, http.StatusOK, t)
}

// DELETE /api/v1/display-templates/:templateid  (requires auth + PermAdmin)
func (h *TemplatesHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	templateID := ps.ByName("templateid")
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	result, err := h.DB.Exec(ctx, `
		UPDATE display_templates SET deleted_at=NOW()
		WHERE  templateid=$1 AND deleted_at IS NULL
	`, templateID)
	if err != nil {
		slog.Error("Delete template", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if result.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "template not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
