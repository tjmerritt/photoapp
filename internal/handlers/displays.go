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
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

type DisplaysHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// GET /api/v1/displays/:displayid
func (h *DisplaysHandler) Get(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	displayID := ps.ByName("displayid")
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	// Resolve galleryID for the display permission check.
	galleryID, err := resolveDisplayGallery(ctx, h.DB, displayID)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "display not found")
		return
	}
	if err != nil {
		slog.Error("Get display resolve gallery", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, galleryID, permissions.ResourceDisplay, displayID, permissions.PermDisplayView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var d models.DisplayDetail
	var tmplID, tmplName, tmplSlotPositions, tmplPresentation *string
	var tmplCount *int
	err = h.DB.QueryRow(ctx, `
		SELECT d.displayid::text, d.galleryid::text, d.sort_order,
		       t.templateid::text, t.name, t.photo_count, t.slot_positions::text, t.presentation::text,
		       d.created_at, d.updated_at
		FROM   displays d
		JOIN   galleries g ON g.galleryid = d.galleryid
		LEFT   JOIN display_templates t ON t.templateid = d.templateid AND t.deleted_at IS NULL
		WHERE  d.displayid = $1 AND d.deleted_at IS NULL AND g.exhibitionid = $2
	`, displayID, exhibitionID).Scan(
		&d.DisplayID, &d.GalleryID, &d.SortOrder,
		&tmplID, &tmplName, &tmplCount, &tmplSlotPositions, &tmplPresentation,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "display not found")
		return
	}
	if err != nil {
		slog.Error("Get display", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if tmplID != nil {
		d.Template = &models.TemplateSummary{
			TemplateID: *tmplID,
			Name:       *tmplName,
			PhotoCount: *tmplCount,
		}
		if tmplSlotPositions != nil {
			d.Template.SlotPositions = json.RawMessage(*tmplSlotPositions)
		}
		if tmplPresentation != nil {
			d.Template.Presentation = json.RawMessage(*tmplPresentation)
		}
	}

	// Fetch slots with photo data (LEFT JOIN: unfilled slots have no photo).
	rows, err := h.DB.Query(ctx, `
		SELECT s.slotid::text, s.slot_index,
		       p.photoid::text, p.image_url, p.image_width, p.image_height, p.title_text,
		       s.rich_text,
		       s.placard::text
		FROM   display_slots s
		LEFT   JOIN photos p ON p.photoid = s.photoid AND p.deleted_at IS NULL
		WHERE  s.displayid = $1
		ORDER  BY s.slot_index
	`, displayID)
	if err != nil {
		slog.Error("Get display slots", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	d.Slots = []models.DisplaySlot{}
	for rows.Next() {
		var s models.DisplaySlot
		var photoID, imageURL, photoTitle *string
		var width, height *int
		var placardText *string
		if err := rows.Scan(
			&s.SlotID, &s.SlotIndex,
			&photoID, &imageURL, &width, &height, &photoTitle,
			&s.RichText,
			&placardText,
		); err != nil {
			slog.Error("Get display slots scan", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		if photoID != nil {
			s.Photo = &models.SlotPhoto{
				PhotoID:  *photoID,
				ImageURL: *imageURL,
				Width:    *width,
				Height:   *height,
			}
			if photoTitle != nil {
				s.Photo.Title = *photoTitle
			}
		}
		if placardText != nil {
			s.Placard = json.RawMessage(*placardText)
		}
		d.Slots = append(d.Slots, s)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Get display slots", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Attach labels to each slot's photo so the frontend can resolve
	// gallery placard {LabelName} substitutions without a per-photo fetch.
	photoIDs := make([]string, 0, len(d.Slots))
	for i := range d.Slots {
		if d.Slots[i].Photo != nil {
			photoIDs = append(photoIDs, d.Slots[i].Photo.PhotoID)
		}
	}
	if len(photoIDs) > 0 {
		labelRows, err := h.DB.Query(ctx, `
			SELECT l.photoid::text, l.labelid::text, l.name, l.value,
			       l.added_by_userid::text, u.username
			FROM   labels l
			JOIN   users  u ON u.userid = l.added_by_userid
			WHERE  l.photoid = ANY($1::uuid[]) AND l.deleted_at IS NULL
			ORDER  BY l.created_at
		`, photoIDs)
		if err != nil {
			slog.Error("Get display slot labels", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		labelsByPhoto := map[string][]models.Label{}
		for labelRows.Next() {
			var photoID string
			var lbl models.Label
			if err := labelRows.Scan(&photoID, &lbl.LabelID, &lbl.Name, &lbl.Value, &lbl.UserID, &lbl.Username); err != nil {
				labelRows.Close()
				slog.Error("Get display slot labels scan", "error", err)
				middleware.WriteError(w, http.StatusInternalServerError, "db error")
				return
			}
			labelsByPhoto[photoID] = append(labelsByPhoto[photoID], lbl)
		}
		labelRows.Close()
		if err := labelRows.Err(); err != nil {
			slog.Error("Get display slot labels", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		for i := range d.Slots {
			if d.Slots[i].Photo != nil {
				d.Slots[i].Photo.Labels = labelsByPhoto[d.Slots[i].Photo.PhotoID]
			}
		}
	}

	middleware.WriteJSON(w, http.StatusOK, d)
}

// POST /api/v1/galleries/:galleryid/displays  (requires auth + DisplayCreate)
func (h *DisplaysHandler) Create(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	galleryID := ps.ByName("galleryid")
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, galleryID, "", "", permissions.PermDisplayCreate); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Verify the gallery exists in this exhibition.
	var galleryExists bool
	if err := h.DB.QueryRow(ctx, `
		SELECT TRUE FROM galleries WHERE galleryid=$1 AND exhibitionid=$2 AND deleted_at IS NULL
	`, galleryID, exhibitionID).Scan(&galleryExists); err != nil {
		if err == pgx.ErrNoRows {
			middleware.WriteError(w, http.StatusNotFound, "gallery not found")
			return
		}
		slog.Error("Create display gallery check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	var req models.CreateDisplayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	sortOrder := 0
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}

	// templateid is nullable; NULLIF converts empty string to NULL.
	var templateID *string
	if req.TemplateID != nil && *req.TemplateID != "" {
		templateID = req.TemplateID
	}

	var d models.DisplayDetail
	if err := h.DB.QueryRow(ctx, `
		INSERT INTO displays (galleryid, templateid, sort_order)
		VALUES ($1, NULLIF($2::text, '')::uuid, $3)
		RETURNING displayid::text, galleryid::text, sort_order, created_at, updated_at
	`, galleryID, templateID, sortOrder).Scan(
		&d.DisplayID, &d.GalleryID, &d.SortOrder, &d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		slog.Error("Create display", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// If a template was specified, pre-populate empty slots.
	if templateID != nil {
		var photoCount int
		if err := h.DB.QueryRow(ctx, `
			SELECT photo_count FROM display_templates WHERE templateid=$1 AND deleted_at IS NULL
		`, *templateID).Scan(&photoCount); err == nil {
			for i := 0; i < photoCount; i++ {
				_, _ = h.DB.Exec(ctx, `
					INSERT INTO display_slots (displayid, slot_index)
					VALUES ($1, $2)
					ON CONFLICT (displayid, slot_index) DO NOTHING
				`, d.DisplayID, i)
			}
		}

		// Fetch template summary for the response (including layout + styling,
		// same as Get, so a freshly-created display renders correctly without
		// a follow-up fetch).
		var t models.TemplateSummary
		var slotPositions, presentation *string
		if err := h.DB.QueryRow(ctx, `
			SELECT templateid::text, name, photo_count, slot_positions::text, presentation::text
			FROM   display_templates WHERE templateid=$1
		`, *templateID).Scan(&t.TemplateID, &t.Name, &t.PhotoCount, &slotPositions, &presentation); err == nil {
			if slotPositions != nil {
				t.SlotPositions = json.RawMessage(*slotPositions)
			}
			if presentation != nil {
				t.Presentation = json.RawMessage(*presentation)
			}
			d.Template = &t
		}
	}

	d.Slots = []models.DisplaySlot{}
	middleware.WriteJSON(w, http.StatusCreated, d)
}

// PATCH /api/v1/displays/:displayid  (requires auth + DisplayModify)
func (h *DisplaysHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	displayID := ps.ByName("displayid")
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	galleryID, err := resolveDisplayGallery(ctx, h.DB, displayID)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "display not found")
		return
	}
	if err != nil {
		slog.Error("Update display resolve gallery", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, galleryID, permissions.ResourceDisplay, displayID, permissions.PermDisplayModify); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req models.UpdateDisplayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Apply sort_order change.
	if req.SortOrder != nil {
		if _, err := h.DB.Exec(ctx, `
			UPDATE displays SET sort_order=$1 WHERE displayid=$2
		`, *req.SortOrder, displayID); err != nil {
			slog.Error("Update display sort_order", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}

	// Apply template change (nil = no change; "" = clear template).
	if req.TemplateID != nil {
		if _, err := h.DB.Exec(ctx, `
			UPDATE displays SET templateid=NULLIF($1,'')::uuid WHERE displayid=$2
		`, *req.TemplateID, displayID); err != nil {
			slog.Error("Update display templateid", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		// Pre-populate new slots if switching to a template with more slots.
		if *req.TemplateID != "" {
			var photoCount int
			if err := h.DB.QueryRow(ctx, `
				SELECT photo_count FROM display_templates
				WHERE templateid=NULLIF($1,'')::uuid AND deleted_at IS NULL
			`, *req.TemplateID).Scan(&photoCount); err == nil {
				for i := 0; i < photoCount; i++ {
					_, _ = h.DB.Exec(ctx, `
						INSERT INTO display_slots (displayid, slot_index)
						VALUES ($1, $2)
						ON CONFLICT (displayid, slot_index) DO NOTHING
					`, displayID, i)
				}
			}
		}
	}

	// Upsert individual slot updates.
	for _, slot := range req.Slots {
		// Convert empty photoid to nil for NULLIF to handle; placard nil/null to nil.
		var placardArg *string
		if len(slot.Placard) > 0 && string(slot.Placard) != "null" {
			s := string(slot.Placard)
			placardArg = &s
		}
		if _, err := h.DB.Exec(ctx, `
			INSERT INTO display_slots (displayid, slot_index, photoid, rich_text, placard)
			VALUES ($1, $2, NULLIF($3,'')::uuid, NULLIF($4,''), $5::jsonb)
			ON CONFLICT (displayid, slot_index) DO UPDATE SET
			    photoid   = EXCLUDED.photoid,
			    rich_text = EXCLUDED.rich_text,
			    placard   = EXCLUDED.placard
		`, displayID, slot.SlotIndex, slot.PhotoID, slot.RichText, placardArg); err != nil {
			slog.Error("Update display slot upsert", "slot_index", slot.SlotIndex, "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}

	// Return updated display.
	h.Get(w, r, ps)
}

// DELETE /api/v1/displays/:displayid  (requires auth + DisplayDelete)
func (h *DisplaysHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	displayID := ps.ByName("displayid")
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	galleryID, err := resolveDisplayGallery(ctx, h.DB, displayID)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "display not found")
		return
	}
	if err != nil {
		slog.Error("Delete display resolve gallery", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, galleryID, permissions.ResourceDisplay, displayID, permissions.PermDisplayDelete); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Verify display belongs to this exhibition before deleting.
	result, err := h.DB.Exec(ctx, `
		UPDATE displays d SET deleted_at=NOW()
		FROM   galleries g
		WHERE  d.displayid = $1 AND d.deleted_at IS NULL
		  AND  d.galleryid = g.galleryid AND g.exhibitionid = $2
	`, displayID, exhibitionID)
	if err != nil {
		slog.Error("Delete display", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if result.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "display not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
