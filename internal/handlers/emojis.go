package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

type EmojisHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// GET /api/v1/emojis?photoid=&offset=&limit=
func (h *EmojisHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	photoid := r.URL.Query().Get("photoid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}

	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPhotoEmojiView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	emojis, total, err := fetchEmojis(ctx, h.DB, photoid, offset, limit, 3)
	if err != nil {
		slog.Error("List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	baseURL := fmt.Sprintf("/api/v1/emojis?photoid=%s&limit=%d", photoid, limit)
	middleware.WriteJSON(w, http.StatusOK, models.EmojisResponse{
		PhotoID: photoid,
		Offset:  offset,
		Pages:   buildPages(total, offset, limit, baseURL),
		Emojis:  emojis,
	})
}

// GET /api/v1/emoji/users?emoji=&offset=&limit=
func (h *EmojisHandler) ListUsers(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	emojiid := r.URL.Query().Get("emoji")
	photoid := r.URL.Query().Get("photoid")
	if emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "emoji is required")
		return
	}

	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPhotoEmojiView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	var total int
	err := h.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM emoji_reactions
		WHERE emojiid=$1 AND ($2='' OR photoid::text=$2)
	`, emojiid, photoid).Scan(&total)
	if err != nil {
		slog.Error("ListUsers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	users, err := fetchEmojiUsers(ctx, h.DB, photoid, emojiid, offset, limit)
	if err != nil {
		slog.Error("ListUsers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	baseURL := fmt.Sprintf("/api/v1/emoji/users?emoji=%s&photoid=%s&limit=%d", emojiid, photoid, limit)
	middleware.WriteJSON(w, http.StatusOK, models.EmojiUsersResponse{
		EmojiID: emojiid,
		Offset:  offset,
		Pages:   buildPages(total, offset, limit, baseURL),
		Users:   users,
	})
}

// POST /api/v1/emoji/react?photoid=&emojiid=  (requires auth)
// Adds the current user's reaction to a photo with the given emoji.
func (h *EmojisHandler) React(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	photoid := r.URL.Query().Get("photoid")
	emojiid := r.URL.Query().Get("emojiid")
	userID := middleware.MustUserID(ctx)

	if photoid == "" || emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid and emojiid are required")
		return
	}

	exhibitionID, err := resolvePhotoExhibition(ctx, h.DB, photoid)
	if err != nil {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}
	// PermPhotoEmojiReact (PLAN2.md Phase 2a) is an alternate, combined
	// add/remove-your-own-reaction permission — accepted alongside the
	// pre-existing PermPhotoEmojiCreate so existing role grants keep working.
	okCreate, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourcePhoto, photoid, permissions.PermPhotoEmojiCreate)
	if err != nil {
		slog.Error("React", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	okReact, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourcePhoto, photoid, permissions.PermPhotoEmojiReact)
	if err != nil {
		slog.Error("React", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !okCreate && !okReact {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Phase 6b: per-user override — see resolve.go's userCanManageOwnEmoji doc.
	if canManage, err := userCanManageOwnEmoji(ctx, h.DB, userID); err != nil {
		slog.Error("React", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !canManage {
		middleware.WriteError(w, http.StatusForbidden, "emoji reactions have been disabled for your account")
		return
	}

	// Verify emoji type exists and is active.
	var active bool
	var emojiOrgID *string
	err = h.DB.QueryRow(ctx, `SELECT is_active, organizationid::text FROM emoji_types WHERE emojiid=$1`, emojiid).Scan(&active, &emojiOrgID)
	if err == pgx.ErrNoRows || !active {
		middleware.WriteError(w, http.StatusBadRequest, "emoji not found or inactive")
		return
	}

	// Phase 1b: a custom (organization-owned) emoji type is only usable
	// within the organization that owns it; global emoji types
	// (organizationid IS NULL) are usable everywhere, which is the common
	// case, so the extra lookup below only runs when it might matter.
	if emojiOrgID != nil {
		photoOrgID, orgErr := resolveExhibitionOrganization(ctx, h.DB, exhibitionID)
		if orgErr != nil {
			slog.Error("React", "error", orgErr)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		if *emojiOrgID != photoOrgID {
			middleware.WriteError(w, http.StatusBadRequest, "emoji not available in this organization")
			return
		}
	}

	_, err = h.DB.Exec(ctx, `
		INSERT INTO emoji_reactions (photoid, emojiid, userid)
		VALUES ($1, $2, $3)
		ON CONFLICT (photoid, emojiid, userid) DO NOTHING
	`, photoid, emojiid, userID)
	if err != nil {
		slog.Error("React", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Refresh counts materialised view
	_ = h.DB.RefreshEmojiCounts(ctx)

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/emoji/react?photoid=&emojiid=  (requires auth)
// Removes the current user's reaction.
func (h *EmojisHandler) Unreact(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	photoid := r.URL.Query().Get("photoid")
	emojiid := r.URL.Query().Get("emojiid")
	userID := middleware.MustUserID(ctx)

	if photoid == "" || emojiid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid and emojiid are required")
		return
	}

	exhibitionID, err := resolvePhotoExhibition(ctx, h.DB, photoid)
	if err != nil {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}
	// PermPhotoEmojiReact (PLAN2.md Phase 2a) is an alternate, combined
	// add/remove-your-own-reaction permission — accepted alongside the
	// pre-existing PermPhotoEmojiDelete so existing role grants keep working.
	okDelete, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourcePhoto, photoid, permissions.PermPhotoEmojiDelete)
	if err != nil {
		slog.Error("Unreact", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	okReact, err := h.Checker.Check(ctx, userID, exhibitionID, "", permissions.ResourcePhoto, photoid, permissions.PermPhotoEmojiReact)
	if err != nil {
		slog.Error("Unreact", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !okDelete && !okReact {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Phase 6b: per-user override — see resolve.go's userCanManageOwnEmoji doc.
	if canManage, err := userCanManageOwnEmoji(ctx, h.DB, userID); err != nil {
		slog.Error("Unreact", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !canManage {
		middleware.WriteError(w, http.StatusForbidden, "emoji reactions have been disabled for your account")
		return
	}

	ct, err := h.DB.Exec(ctx, `
		DELETE FROM emoji_reactions
		WHERE photoid=$1 AND emojiid=$2 AND userid=$3
	`, photoid, emojiid, userID)
	if err != nil {
		slog.Error("Unreact", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "reaction not found")
		return
	}

	_ = h.DB.RefreshEmojiCounts(ctx)

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/emoji/types  – paginated, searchable list of active emoji types.
//
// Query params:
//
//	search  – filter by alt_text or tags (case-insensitive substring)
//	group   – filter by emoji_group
//	offset  – pagination offset (default 0)
//	limit   – page size (default DefaultPageSize, max MaxPageSize)
func (h *EmojisHandler) ListTypes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	group := strings.TrimSpace(q.Get("group"))
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	// Phase 1b: only global emoji types (organizationid IS NULL) plus the
	// caller's own organization's custom uploads are visible here — an
	// organization's custom emoji is not usable outside it. organizationID
	// is "" when the request's exhibition context is unknown, in which case
	// the org branch of the WHERE clause below never matches anything and
	// this degrades to global-only, not an error.
	organizationID, err := resolveExhibitionOrganization(ctx, h.DB, middleware.ExhibitionID(ctx))
	if err != nil {
		slog.Error("ListTypes resolveExhibitionOrganization", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Build WHERE clause — only return base emojis (exclude skintone variants).
	where := "is_active = TRUE AND base_hexcode IS NULL"
	args := []any{}
	n := 1
	if search != "" {
		where += fmt.Sprintf(" AND (alt_text ILIKE $%d OR tags ILIKE $%d)", n, n)
		args = append(args, "%"+search+"%")
		n++
	}
	if group != "" {
		where += fmt.Sprintf(" AND emoji_group = $%d", n)
		args = append(args, group)
		n++
	}
	where += fmt.Sprintf(" AND (organizationid IS NULL OR organizationid = $%d::uuid)", n)
	args = append(args, nullableUUID(organizationID))
	n++

	// Total count.
	var total int
	if err := h.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM emoji_types WHERE "+where,
		args...,
	).Scan(&total); err != nil {
		slog.Error("ListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Page of results with has_skintones flag and global usage_count.
	// Popular-first (5c): order by total reaction count across all photos,
	// descending, falling back to alphabetical alt_text for ties — since
	// most emoji types have zero reactions, this naturally reads as
	// "frequently-used emojis first, everything else alphabetical" while
	// still being a single stable ORDER BY that's safe to paginate over.
	args = append(args, limit, offset)
	rows, err := h.DB.Query(ctx,
		fmt.Sprintf(`
			SELECT et.emojiid::text, et.emoji_char, et.image_url, et.alt_text,
			       et.is_active, COALESCE(et.hexcode,''), et.organizationid::text,
			       EXISTS (
			           SELECT 1 FROM emoji_types v
			           WHERE v.base_hexcode = et.hexcode AND v.is_active = TRUE
			       ) AS has_skintones,
			       COALESCE(ec.usage_count, 0) AS usage_count
			FROM   emoji_types et
			LEFT JOIN (
			    SELECT emojiid, COUNT(*) AS usage_count
			    FROM   emoji_reactions
			    GROUP  BY emojiid
			) ec ON ec.emojiid = et.emojiid
			WHERE  %s
			ORDER  BY COALESCE(ec.usage_count, 0) DESC, et.alt_text ASC, et.sort_order, et.created_at
			LIMIT  $%d OFFSET $%d
		`, where, n, n+1),
		args...,
	)
	if err != nil {
		slog.Error("ListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	types := make([]models.EmojiTypeResponse, 0)
	for rows.Next() {
		var et models.EmojiTypeResponse
		if err := rows.Scan(&et.EmojiID, &et.EmojiChar, &et.ImageURL, &et.AltText,
			&et.IsActive, &et.Hexcode, &et.OrganizationID, &et.HasSkintones, &et.UsageCount); err != nil {
			slog.Error("ListTypes", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		et.ImageURL = proxyImageURLPtr(et.ImageURL)
		types = append(types, et)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	baseURL := fmt.Sprintf("/api/v1/emoji/types?search=%s&group=%s&limit=%d", search, group, limit)
	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"pages":  buildPages(total, offset, limit, baseURL),
		"emojis": types,
	})
}

// GET /api/v1/admin/emoji-types?search=&source=&status=&used_only=&offset=&limit=  (Phase 6d)
// Like ListTypes, but for the emoji admin page: doesn't exclude inactive
// emoji types by default, doesn't exclude skintone variants, and requires
// admin access rather than being publicly readable.
//
// Query params:
//
//	source     – "all" (default), "openmoji" (hexcode set — imported via
//	             cmd/import-emojis), or "custom" (hexcode unset — uploaded
//	             via POST /api/v1/emoji/types)
//	status     – "all" (default), "enabled", or "disabled"
//	used_only  – "true" to only include emoji with at least one reaction
//	             anywhere (EXISTS against emoji_reactions, not a stale count)
//
// Requires: authenticated + (PermAdmin or PermEmojiAdmin).
func (h *EmojisHandler) AdminListTypes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermEmojiAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	// Phase 1b: same visibility rule as the public ListTypes — global plus
	// the caller's own organization's custom uploads. Phase 1c's org-admin
	// grants only ever widen a user's reach to "every exhibition in my
	// organization," not across organizations, so this page stays scoped to
	// "my organization" regardless of how the caller qualified for admin
	// access. A true cross-organization view would need a separate
	// super-admin concept, which doesn't exist.
	organizationID, err := resolveExhibitionOrganization(ctx, h.DB, exhibitionID)
	if err != nil {
		slog.Error("AdminListTypes resolveExhibitionOrganization", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	source := q.Get("source")
	status := q.Get("status")
	usedOnly := q.Get("used_only") == "true"
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	where := "et.base_hexcode IS NULL"
	args := []any{}
	n := 1

	switch status {
	case "enabled":
		where += " AND et.is_active = TRUE"
	case "disabled":
		where += " AND et.is_active = FALSE"
	}

	switch source {
	case "openmoji":
		where += " AND et.hexcode IS NOT NULL AND et.hexcode <> ''"
	case "custom":
		where += " AND (et.hexcode IS NULL OR et.hexcode = '')"
	}

	if usedOnly {
		where += " AND EXISTS (SELECT 1 FROM emoji_reactions er WHERE er.emojiid = et.emojiid)"
	}

	if search != "" {
		where += fmt.Sprintf(" AND (et.alt_text ILIKE $%d OR et.tags ILIKE $%d)", n, n)
		args = append(args, "%"+search+"%")
		n++
	}

	where += fmt.Sprintf(" AND (et.organizationid IS NULL OR et.organizationid = $%d::uuid)", n)
	args = append(args, nullableUUID(organizationID))
	n++

	var total int
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM emoji_types et WHERE "+where, args...).Scan(&total); err != nil {
		slog.Error("AdminListTypes count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rowArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT et.emojiid::text, et.emoji_char, et.image_url, et.alt_text,
		       et.is_active, COALESCE(et.hexcode,''), et.organizationid::text,
		       COALESCE(ec.usage_count, 0) AS usage_count
		FROM   emoji_types et
		LEFT JOIN (
		    SELECT emojiid, COUNT(*) AS usage_count
		    FROM   emoji_reactions
		    GROUP  BY emojiid
		) ec ON ec.emojiid = et.emojiid
		WHERE  %s
		ORDER  BY et.alt_text ASC
		LIMIT  $%d OFFSET $%d
	`, where, n, n+1), rowArgs...)
	if err != nil {
		slog.Error("AdminListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	types := make([]models.EmojiTypeResponse, 0)
	for rows.Next() {
		var et models.EmojiTypeResponse
		if err := rows.Scan(&et.EmojiID, &et.EmojiChar, &et.ImageURL, &et.AltText,
			&et.IsActive, &et.Hexcode, &et.OrganizationID, &et.UsageCount); err != nil {
			slog.Error("AdminListTypes", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		et.ImageURL = proxyImageURLPtr(et.ImageURL)
		types = append(types, et)
	}
	if err := rows.Err(); err != nil {
		slog.Error("AdminListTypes", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"emojis": types,
	})
}

// PATCH /api/v1/admin/emoji-types/:emojiid  (Phase 6d)
// Body: any subset of { "is_active": bool, "alt_text": "..." } — alt_text
// (PLAN2.md Phase 2a) is the "changing uploaded emoji name" PermEmojiModify
// is meant to gate; is_active predates it (Phase 6d) and continues to work
// unchanged.
// Requires: authenticated + (PermAdmin or PermEmojiAdmin or PermEmojiModify).
func (h *EmojisHandler) AdminUpdateType(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	emojiid := ps.ByName("emojiid")
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermEmojiAdmin, permissions.PermEmojiModify); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	var req struct {
		IsActive *bool   `json:"is_active"`
		AltText  *string `json:"alt_text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.IsActive == nil && req.AltText == nil {
		middleware.WriteError(w, http.StatusBadRequest, "is_active or alt_text is required")
		return
	}
	if req.AltText != nil && strings.TrimSpace(*req.AltText) == "" {
		middleware.WriteError(w, http.StatusBadRequest, "alt_text cannot be empty")
		return
	}

	// Phase 1b: an exhibition admin may toggle global emoji types (imported
	// via cmd/import-emojis) and their own organization's custom uploads,
	// but not another organization's custom emoji.
	callerOrgID, err := resolveExhibitionOrganization(ctx, h.DB, exhibitionID)
	if err != nil {
		slog.Error("AdminUpdateType resolveExhibitionOrganization", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	var emojiOrgID *string
	if err := h.DB.QueryRow(ctx, `SELECT organizationid::text FROM emoji_types WHERE emojiid = $1`, emojiid).Scan(&emojiOrgID); err != nil {
		if err == pgx.ErrNoRows {
			middleware.WriteError(w, http.StatusNotFound, "emoji type not found")
			return
		}
		slog.Error("AdminUpdateType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if emojiOrgID != nil && *emojiOrgID != callerOrgID {
		middleware.WriteError(w, http.StatusForbidden, "emoji type belongs to a different organization")
		return
	}

	hasIsActive := req.IsActive != nil
	var isActiveVal bool
	if hasIsActive {
		isActiveVal = *req.IsActive
	}
	hasAltText := req.AltText != nil
	var altTextVal string
	if hasAltText {
		altTextVal = strings.TrimSpace(*req.AltText)
	}

	ct, err := h.DB.Exec(ctx, `
		UPDATE emoji_types SET
		    is_active = CASE WHEN $3 THEN $1 ELSE is_active END,
		    alt_text  = CASE WHEN $4 THEN $2 ELSE alt_text  END
		WHERE emojiid = $5
	`, isActiveVal, altTextVal, hasIsActive, hasAltText, emojiid)
	if err != nil {
		slog.Error("AdminUpdateType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "emoji type not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/emoji/variants?hexcode=  — returns all skintone variants for a base emoji.
// The base emoji itself is included first so the picker can offer "no skintone" too.
func (h *EmojisHandler) ListVariants(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	hexcode := strings.TrimSpace(r.URL.Query().Get("hexcode"))
	if hexcode == "" {
		middleware.WriteError(w, http.StatusBadRequest, "hexcode is required")
		return
	}

	// Phase 1b: same global-plus-own-org visibility rule as ListTypes. In
	// practice a hexcode-bearing emoji is almost always a global OpenMoji
	// import (custom uploads via UploadType never set hexcode/base_hexcode),
	// but the filter is applied for correctness rather than assuming that
	// invariant holds forever.
	organizationID, err := resolveExhibitionOrganization(ctx, h.DB, middleware.ExhibitionID(ctx))
	if err != nil {
		slog.Error("ListVariants resolveExhibitionOrganization", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Fetch the base emoji first.
	//
	// sort_order and created_at have to be part of each SELECT's own output
	// list, not just referenced bare in the trailing ORDER BY — Postgres
	// resolves a UNION ALL's ORDER BY against the combined result's output
	// columns only, not the underlying tables, so a bare `sort_order`/
	// `created_at` here (previously not selected by either branch) failed
	// at query time with "column does not exist" the moment this endpoint
	// was actually exercised by a test with more than one row to order.
	baseRows, err := h.DB.Query(ctx, `
		SELECT emojiid::text, emoji_char, image_url, alt_text, is_active,
		       COALESCE(hexcode,''), COALESCE(skintone,''), organizationid::text, sort_order, created_at
		FROM   emoji_types
		WHERE  hexcode = $1 AND base_hexcode IS NULL AND is_active = TRUE
		  AND  (organizationid IS NULL OR organizationid = $2::uuid)
		UNION ALL
		SELECT emojiid::text, emoji_char, image_url, alt_text, is_active,
		       COALESCE(hexcode,''), COALESCE(skintone,''), organizationid::text, sort_order, created_at
		FROM   emoji_types
		WHERE  base_hexcode = $1 AND is_active = TRUE
		  AND  (organizationid IS NULL OR organizationid = $2::uuid)
		ORDER  BY sort_order, created_at
	`, hexcode, nullableUUID(organizationID))
	if err != nil {
		slog.Error("ListVariants", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer baseRows.Close()

	variants := make([]models.EmojiTypeResponse, 0)
	for baseRows.Next() {
		var et models.EmojiTypeResponse
		var tone string
		var rowSortOrder int
		var rowCreatedAt time.Time
		if err := baseRows.Scan(&et.EmojiID, &et.EmojiChar, &et.ImageURL, &et.AltText,
			&et.IsActive, &et.Hexcode, &tone, &et.OrganizationID, &rowSortOrder, &rowCreatedAt); err != nil {
			slog.Error("ListVariants", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		et.ImageURL = proxyImageURLPtr(et.ImageURL)
		if tone != "" {
			et.Skintone = &tone
		}
		variants = append(variants, et)
	}
	if err := baseRows.Err(); err != nil {
		slog.Error("ListVariants", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{"variants": variants})
}

// POST /api/v1/emoji/types  – upload a new custom emoji image (requires EmojiUpload permission)
// Accepts multipart/form-data with fields:
//   - image  : the image file (PNG, GIF, WebP recommended)
//   - alttext: accessibility label (required)
func (h *EmojisHandler) UploadType(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)
	// PermEmojiCreate is PLAN2.md Phase 2a's alternate name for
	// PermEmojiUpload — accepting either keeps existing role grants working.
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermEmojiUpload, permissions.PermEmojiCreate); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Phase 1b: a custom emoji uploaded through an exhibition belongs to
	// that exhibition's organization. In the unusual case where
	// PermEmojiUpload was satisfied by a global grant with no exhibition
	// context at all (exhibitionID == ""), organizationID resolves to "" too
	// and NULLIF below stores that as a true global emoji instead — there's
	// no organization to attribute it to.
	organizationID, err := resolveExhibitionOrganization(ctx, h.DB, exhibitionID)
	if err != nil {
		slog.Error("UploadType resolveExhibitionOrganization", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := r.ParseMultipartForm(8 << 20); err != nil { // 8 MB max
		middleware.WriteError(w, http.StatusBadRequest, "could not parse form (max 8MB)")
		return
	}

	altText := strings.TrimSpace(r.FormValue("alttext"))
	if altText == "" {
		middleware.WriteError(w, http.StatusBadRequest, "alttext is required")
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "image file is required")
		return
	}
	defer file.Close()

	// Validate content type
	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	contentType := http.DetectContentType(buf[:n])
	allowed := map[string]string{
		"image/png":  ".png",
		"image/gif":  ".gif",
		"image/webp": ".webp",
		"image/jpeg": ".jpg",
	}
	ext, ok := allowed[contentType]
	if !ok {
		middleware.WriteError(w, http.StatusBadRequest, "image must be PNG, GIF, WebP or JPEG")
		return
	}

	// Seek back to start after sniffing content type
	if seeker, ok := file.(io.Seeker); ok {
		_, _ = seeker.Seek(0, io.SeekStart)
	}

	// Ensure upload directory exists
	if err := os.MkdirAll(h.Cfg.UploadDir, 0o755); err != nil {
		slog.Error("UploadType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not create upload directory")
		return
	}

	// Save file with a UUID filename to avoid collisions
	newID := uuid.New().String()
	filename := newID + ext
	_ = header // suppress unused warning
	destPath := filepath.Join(h.Cfg.UploadDir, filename)
	dest, err := os.Create(destPath)
	if err != nil {
		slog.Error("UploadType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not save file")
		return
	}
	defer dest.Close()

	if _, err := io.Copy(dest, file); err != nil {
		slog.Error("UploadType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not save file")
		return
	}

	imageURL := h.Cfg.UploadURLBase + "/" + filename

	// Insert into emoji_types (inactive until an admin activates it,
	// or set is_active=TRUE to allow immediate use — adjust per policy)
	var emojiid string
	err = h.DB.QueryRow(ctx, `
		INSERT INTO emoji_types (emojiid, image_url, alt_text, is_active, organizationid)
		VALUES ($1, $2, $3, TRUE, $4::uuid)
		RETURNING emojiid::text
	`, newID, imageURL, altText, nullableUUID(organizationID)).Scan(&emojiid)
	if err != nil {
		slog.Error("UploadType", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	resp := models.EmojiTypeResponse{
		EmojiID:  emojiid,
		ImageURL: proxyImageURLPtr(&imageURL),
		AltText:  altText,
		IsActive: true,
	}
	if organizationID != "" {
		resp.OrganizationID = &organizationID
	}
	middleware.WriteJSON(w, http.StatusCreated, resp)
}
