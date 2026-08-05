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

// fetchLabelNameInfo returns a label name's color override, restricted flag
// (Phase 5b), and enabled flag (Phase 6e) from label_names, scoped to the
// given exhibition — label_names is per-exhibition (each exhibition has its
// own independent catalog; the same name in two different exhibitions can
// have different colors/restricted/enabled settings). A name with no row at
// all in this exhibition has no color override, is not restricted, and is
// enabled — those are exactly the right zero-value defaults, and a name
// only ever gains a row once something explicitly marks it for this
// exhibition (EXIF import, --restrict-labels, or the PATCH
// /api/v1/label-names endpoint below).
func fetchLabelNameInfo(ctx context.Context, pool *db.Pool, exhibitionID, name string) (colorHex *string, restricted bool, enabled bool, err error) {
	err = pool.QueryRow(ctx, `SELECT color_hex, restricted, enabled FROM label_names WHERE exhibitionid = $1 AND name = $2`, exhibitionID, name).
		Scan(&colorHex, &restricted, &enabled)
	if err == pgx.ErrNoRows {
		return nil, false, true, nil
	}
	return colorHex, restricted, enabled, err
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

	// label_names is per-exhibition, so this needs the photo's own
	// exhibition rather than the request context's — exhibitionID above can
	// be "" (e.g. an unresolved hostname combined with a genuinely global
	// PhotoLabelView grant), but the photo itself always belongs to a real
	// exhibition.
	photoExhibitionID, err := resolvePhotoExhibition(ctx, h.DB, photoid)
	if err != nil {
		slog.Error("List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	labels, total, err := fetchLabels(ctx, h.DB, photoExhibitionID, photoid, offset, limit)
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

	// Phase 6b: an admin can revoke one specific user's ability to add labels
	// even though PhotoLabelCreate above is granted broadly to every logged-in
	// user via the seeded Contributor role — see resolve.go's comment on why
	// this needs a real column instead of another permission grant.
	if canManage, err := userCanManageOwnLabels(ctx, h.DB, userID); err != nil {
		slog.Error("Create", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !canManage {
		middleware.WriteError(w, http.StatusForbidden, "label management has been disabled for your account")
		return
	}

	// Phase 5a/5b/6e: look up this name's color override, restricted flag,
	// and enabled flag — the latter two gate who may proceed, the color just
	// rides along on the response so the frontend doesn't need a second
	// round-trip.
	colorHex, restricted, enabled, err := fetchLabelNameInfo(ctx, h.DB, exhibitionID, req.Name)
	if err != nil {
		slog.Error("Create", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if restricted || !enabled {
		if ok, _ := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin); !ok {
			if !enabled {
				middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is disabled", req.Name))
			} else {
				middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is restricted", req.Name))
			}
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
	exhibitionID, err := resolvePhotoExhibition(ctx, h.DB, photoid)
	if err != nil {
		slog.Error("Update", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if ownerID != userID {
		if ok, _ := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermLabelAdmin); !ok {
			middleware.WriteError(w, http.StatusForbidden, "you may only edit your own labels")
			return
		}
	}

	// Phase 5b/6e: even the label's own creator may not modify it once its
	// name is restricted or disabled — only Admin/LabelAdmin may. Checks both
	// the label's current name and, if this request renames it, the new name
	// too.
	namesToCheck := []string{existingName}
	if req.Name != nil && *req.Name != existingName {
		namesToCheck = append(namesToCheck, *req.Name)
	}
	for _, n := range namesToCheck {
		_, restricted, enabled, err := fetchLabelNameInfo(ctx, h.DB, exhibitionID, n)
		if err != nil {
			slog.Error("Update", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		if !restricted && enabled {
			continue
		}
		if ok, _ := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin); !ok {
			if !enabled {
				middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is disabled", n))
			} else {
				middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is restricted", n))
			}
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

	colorHex, restricted, _, err := fetchLabelNameInfo(ctx, h.DB, exhibitionID, newName)
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
	exhibitionID, err := resolvePhotoExhibition(ctx, h.DB, photoid)
	if err != nil {
		slog.Error("Delete", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if ownerID != userID {
		if ok, _ := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermLabelAdmin); !ok {
			middleware.WriteError(w, http.StatusForbidden, "you may only delete your own labels")
			return
		}
	}

	// Phase 5b/6e: even the label's own creator may not delete it once its
	// name is restricted or disabled — only Admin/LabelAdmin may.
	if _, restricted, enabled, err := fetchLabelNameInfo(ctx, h.DB, exhibitionID, name); err != nil {
		slog.Error("Delete", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if restricted || !enabled {
		if ok, _ := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin); !ok {
			if !enabled {
				middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is disabled", name))
			} else {
				middleware.WriteError(w, http.StatusForbidden, fmt.Sprintf("label name %q is restricted", name))
			}
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
// labels *in the current exhibition*, annotated with each name's color
// override (Phase 5a), restricted flag (Phase 5b), and enabled flag (Phase
// 6e) — all three per-exhibition, since label_names is scoped per
// exhibition (the same name in a different exhibition can have different
// settings). Returns every name regardless of enabled/restricted state —
// this feeds the color/lock-icon rendering for labels that already exist on
// photos, not just the "add a new label" suggestion list (which does its
// own client-side filtering).
func (h *LabelsHandler) Names(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	exhibitionID := middleware.ExhibitionID(ctx)
	if exhibitionID == "" {
		// No resolved exhibition context (e.g. an unrecognized hostname) —
		// nothing to scope to, so there's nothing to return. Short-circuit
		// before the query below, since binding "" against the exhibitionid
		// uuid column would fail at the database level, not just match zero
		// rows.
		middleware.WriteJSON(w, http.StatusOK, map[string][]models.LabelNameInfo{"names": {}})
		return
	}
	rows, err := h.DB.Query(ctx, `
		SELECT DISTINCT l.name, ln.color_hex, COALESCE(ln.restricted, FALSE), COALESCE(ln.enabled, TRUE)
		FROM   labels l
		JOIN   photos p ON p.photoid = l.photoid
		LEFT   JOIN label_names ln ON ln.name = l.name AND ln.exhibitionid = p.exhibitionid
		WHERE  l.deleted_at IS NULL AND p.exhibitionid = $1
		ORDER  BY l.name
	`, exhibitionID)
	if err != nil {
		slog.Error("Names", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	names := []models.LabelNameInfo{}
	for rows.Next() {
		var n models.LabelNameInfo
		if err := rows.Scan(&n.Name, &n.ColorHex, &n.Restricted, &n.Enabled); err == nil {
			names = append(names, n)
		}
	}
	middleware.WriteJSON(w, http.StatusOK, map[string][]models.LabelNameInfo{"names": names})
}

// GET /api/v1/admin/label-names?search=&include_disabled=&offset=&limit=  (Phase 6e)
// Paginated admin listing of every distinct label name in use *within the
// current exhibition*, each annotated with its (per-exhibition) color
// override, restricted flag, enabled flag, and usage count (how many
// non-deleted labels in this exhibition currently use it).
// Requires: authenticated + (PermAdmin or PermLabelAdmin or PermLabelNameView).
func (h *LabelsHandler) AdminListNames(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin, permissions.PermLabelNameView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}
	if exhibitionID == "" {
		// No resolved exhibition context — nothing to scope to, so nothing
		// to list. Short-circuit before the query below, since binding ""
		// against the exhibitionid uuid column would fail at the database
		// level, not just match zero rows.
		middleware.WriteJSON(w, http.StatusOK, map[string]any{
			"total": 0, "offset": 0, "limit": h.Cfg.DefaultPageSize, "names": []models.LabelNameInfo{},
		})
		return
	}

	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	includeDisabled := q.Get("include_disabled") == "true"
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	// $1 is always the current exhibition — scopes both the labels source
	// (via the photos join) and the label_names join, since label_names is
	// per-exhibition.
	where := "l.deleted_at IS NULL AND p.exhibitionid = $1"
	args := []any{exhibitionID}
	n := 2
	if !includeDisabled {
		where += " AND COALESCE(ln.enabled, TRUE)"
	}
	if search != "" {
		where += fmt.Sprintf(" AND l.name ILIKE $%d", n)
		args = append(args, "%"+search+"%")
		n++
	}

	var total int
	if err := h.DB.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(DISTINCT l.name)
		FROM   labels l
		JOIN   photos p ON p.photoid = l.photoid
		LEFT   JOIN label_names ln ON ln.name = l.name AND ln.exhibitionid = p.exhibitionid
		WHERE  %s
	`, where), args...).Scan(&total); err != nil {
		slog.Error("AdminListNames count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rowArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT l.name, ln.color_hex, COALESCE(ln.restricted, FALSE), COALESCE(ln.enabled, TRUE),
		       COUNT(*) AS usage_count
		FROM   labels l
		JOIN   photos p ON p.photoid = l.photoid
		LEFT   JOIN label_names ln ON ln.name = l.name AND ln.exhibitionid = p.exhibitionid
		WHERE  %s
		GROUP  BY l.name, ln.color_hex, ln.restricted, ln.enabled
		ORDER  BY l.name
		LIMIT  $%d OFFSET $%d
	`, where, n, n+1), rowArgs...)
	if err != nil {
		slog.Error("AdminListNames", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	names := make([]models.LabelNameInfo, 0)
	for rows.Next() {
		var ln models.LabelNameInfo
		if err := rows.Scan(&ln.Name, &ln.ColorHex, &ln.Restricted, &ln.Enabled, &ln.UsageCount); err != nil {
			slog.Error("AdminListNames", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		names = append(names, ln)
	}
	if err := rows.Err(); err != nil {
		slog.Error("AdminListNames", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"names":  names,
	})
}

// PATCH /api/v1/label-names?name=<name>  (requires Admin, LabelAdmin,
// LabelNameCreate, or LabelNameModify)
// Body: { "color_hex": "#rrggbb" | "" , "restricted": bool, "enabled": bool }
// — any subset of fields; only the fields present in the request are
// changed. An empty string for color_hex clears the override (falling back
// to the client-side hash-based color again).
//
// This is an upsert (ON CONFLICT DO UPDATE below), scoped to the calling
// exhibition — it creates a label_names row for this exhibition if none
// exists yet, or updates the one that does, without touching any other
// exhibition's settings for the same name. LabelNameCreate and
// LabelNameModify are both accepted rather than distinguishing the two with
// an extra existence-check query first; either is sufficient to perform
// this single endpoint's one action. See permissions.PermLabelNameCreate's
// doc comment.
func (h *LabelsHandler) UpdateName(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID,
		permissions.PermAdmin, permissions.PermLabelAdmin,
		permissions.PermLabelNameCreate, permissions.PermLabelNameModify,
	); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	if exhibitionID == "" {
		// label_names rows require a real owning exhibition (NOT NULL FK) —
		// no resolved exhibition context (e.g. an unrecognized hostname)
		// means there's nothing valid to write into.
		middleware.WriteError(w, http.StatusBadRequest, "no exhibition context")
		return
	}

	var req struct {
		ColorHex   *string `json:"color_hex"`
		Restricted *bool   `json:"restricted"`
		Enabled    *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ColorHex == nil && req.Restricted == nil && req.Enabled == nil {
		middleware.WriteError(w, http.StatusBadRequest, "color_hex, restricted, or enabled is required")
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

	hasEnabled := req.Enabled != nil
	enabledVal := true
	if hasEnabled {
		enabledVal = *req.Enabled
	}

	if _, err := h.DB.Exec(ctx, `
		INSERT INTO label_names (exhibitionid, name, color_hex, restricted, enabled)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (exhibitionid, name) DO UPDATE SET
			color_hex  = CASE WHEN $6 THEN $3 ELSE label_names.color_hex  END,
			restricted = CASE WHEN $7 THEN $4 ELSE label_names.restricted END,
			enabled    = CASE WHEN $8 THEN $5 ELSE label_names.enabled    END,
			updated_at = NOW()
	`, exhibitionID, name, colorVal, restrictedVal, enabledVal, hasColor, hasRestricted, hasEnabled); err != nil {
		slog.Error("UpdateName", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	colorHex, restricted, enabled, err := fetchLabelNameInfo(ctx, h.DB, exhibitionID, name)
	if err != nil {
		slog.Error("UpdateName", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	middleware.WriteJSON(w, http.StatusOK, models.LabelNameInfo{
		Name:       name,
		ColorHex:   colorHex,
		Restricted: restricted,
		Enabled:    enabled,
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
