package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

// AdminHandler handles admin-only photo management endpoints.
type AdminHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
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
	Hostname     string `json:"hostname"`
}

// adminStats is the shape returned by GET /api/v1/admin/stats (Phase 6a).
type adminStats struct {
	UserCount        int `json:"user_count"`        // members of this exhibition
	PhotoCount       int `json:"photo_count"`        // non-deleted photos in this exhibition
	ActiveLabelCount int `json:"active_label_count"` // distinct, enabled label names in use (site-wide — label_names has no exhibitionid)
	ActiveEmojiCount int `json:"active_emoji_count"` // active emoji types (site-wide — emoji_types has no exhibitionid)
}

// adminUser is the shape returned by GET /api/v1/admin/users (Phase 6b).
type adminUser struct {
	UserID               string `json:"userid"`
	Username             string `json:"username"`
	Email                string `json:"email"`
	FullName             string `json:"fullname"`
	JoinedAt             string `json:"joined_at"`
	AccountEnabled       bool   `json:"account_enabled"`
	CanViewPrivate       bool   `json:"can_view_private"`
	CanManageOwnLabels   bool   `json:"can_manage_own_labels"`
	CanManageOwnEmoji    bool   `json:"can_manage_own_emoji"`
	CanManageOwnComments bool   `json:"can_manage_own_comments"`
}

// GET /api/v1/admin/exhibitions
// Returns exhibitions the logged-in user is a member of.
// Requires: authenticated + PermAdmin.
func (h *AdminHandler) ListExhibitions(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	userID, _ := middleware.UserID(r.Context())
	exhibitionID := middleware.ExhibitionID(r.Context())
	ok, err := h.Checker.Check(r.Context(), userID, exhibitionID, "", "", "", permissions.PermAdmin)
	if err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	rows, err := h.DB.Query(r.Context(), `
		SELECT e.exhibitionid::text, e.name,
		       COALESCE((
		           SELECT eh.hostname
		           FROM   exhibition_hostnames eh
		           WHERE  eh.exhibitionid = e.exhibitionid
		           ORDER  BY eh.created_at
		           LIMIT  1
		       ), '') AS hostname
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
		if err := rows.Scan(&e.ExhibitionID, &e.Name, &e.Hostname); err != nil {
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
// Requires: authenticated + PermAdmin.
func (h *AdminHandler) ListPhotos(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	userID, _ := middleware.UserID(r.Context())
	ok, err := h.Checker.Check(r.Context(), userID, middleware.ExhibitionID(r.Context()), "", "", "", permissions.PermAdmin)
	if err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	offset, limit := parsePage(r, 50, 200)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	// Phase 6c: optional free-text filter over title (falls back to matching
	// the photoid itself, so pasting a UUID also works as a quick lookup).
	search := strings.TrimSpace(r.URL.Query().Get("search"))

	// ── Count ────────────────────────────────────────────────────────────────
	countWhere := "deleted_at IS NULL"
	countArgs := []any{}
	n := 1
	if exhibitionID != "" {
		countWhere += fmt.Sprintf(" AND exhibitionid = $%d::uuid", n)
		countArgs = append(countArgs, exhibitionID)
		n++
	}
	if search != "" {
		countWhere += fmt.Sprintf(" AND (title_text ILIKE $%d OR photoid::text ILIKE $%d)", n, n)
		countArgs = append(countArgs, "%"+search+"%")
		n++
	}
	var total int
	if err := h.DB.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM photos WHERE "+countWhere, countArgs...,
	).Scan(&total); err != nil {
		slog.Error("ListPhotos count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// ── Rows ─────────────────────────────────────────────────────────────────
	// Reuses the same WHERE clause/args built above, then appends LIMIT/OFFSET.
	rowArgs := append(append([]any{}, countArgs...), limit, offset)
	rows, queryErr := h.DB.Query(r.Context(), fmt.Sprintf(`
		SELECT p.photoid::text,
		       p.image_url,
		       COALESCE(p.title_text, ''),
		       p.is_public
		FROM   photos p
		WHERE  %s
		ORDER  BY p.created_at DESC
		LIMIT  $%d OFFSET $%d
	`, countWhere, n, n+1), rowArgs...)
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
// Requires: authenticated + PermAdmin.
func (h *AdminHandler) SetPublic(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	userID, _ := middleware.UserID(r.Context())
	exhibitionID := middleware.ExhibitionID(r.Context())
	ok, err := h.Checker.Check(r.Context(), userID, exhibitionID, "", "", "", permissions.PermAdmin)
	if err != nil || !ok {
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

// GET /api/v1/admin/stats?exhibitionid=  (Phase 6a)
// Quick-stats panel for the master admin page.
// Requires: authenticated + PermAdmin.
func (h *AdminHandler) Stats(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	var stats adminStats
	if exhibitionID != "" {
		if err := h.DB.QueryRow(ctx, `
			SELECT COUNT(*) FROM user_exhibitions WHERE exhibitionid = $1::uuid
		`, exhibitionID).Scan(&stats.UserCount); err != nil {
			slog.Error("Stats user count", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		if err := h.DB.QueryRow(ctx, `
			SELECT COUNT(*) FROM photos WHERE deleted_at IS NULL AND exhibitionid = $1::uuid
		`, exhibitionID).Scan(&stats.PhotoCount); err != nil {
			slog.Error("Stats photo count", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}
	// label_names / emoji_types are site-wide tables (no exhibitionid column),
	// so these two counts are the same regardless of which exhibition is
	// selected — see migrations/017_admin_phase6.sql's comment for why.
	if err := h.DB.QueryRow(ctx, `
		SELECT COUNT(DISTINCT l.name)
		FROM   labels l
		LEFT   JOIN label_names ln ON ln.name = l.name
		WHERE  l.deleted_at IS NULL AND COALESCE(ln.enabled, TRUE)
	`).Scan(&stats.ActiveLabelCount); err != nil {
		slog.Error("Stats label count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := h.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM emoji_types WHERE is_active = TRUE
	`).Scan(&stats.ActiveEmojiCount); err != nil {
		slog.Error("Stats emoji count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, stats)
}

// GET /api/v1/admin/users?exhibitionid=&search=&offset=&limit=  (Phase 6b)
// Lists users who are members of the given exhibition, along with their
// account-enabled flag and the four Phase 6b toggle states.
// Requires: authenticated + (PermAdmin or PermUserAdmin).
func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermUserAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}
	if exhibitionID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "exhibitionid is required")
		return
	}

	offset, limit := parsePage(r, 50, 200)
	search := strings.TrimSpace(r.URL.Query().Get("search"))

	where := "u.deleted_at IS NULL AND ue.exhibitionid = $1::uuid"
	args := []any{exhibitionID}
	n := 2
	if search != "" {
		where += fmt.Sprintf(" AND (u.username ILIKE $%d OR u.email ILIKE $%d)", n, n)
		args = append(args, "%"+search+"%")
		n++
	}

	var total int
	if err := h.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM users u JOIN user_exhibitions ue ON ue.userid = u.userid WHERE "+where,
		args...,
	).Scan(&total); err != nil {
		slog.Error("ListUsers count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// can_view_private is derived from the singleton-role mechanism (Phase
	// 6b's private-photo-view toggle) rather than a column — see
	// permissions.Checker.HasDirectUserGrant.
	rowArgs := append(append([]any{}, args...), exhibitionID, permissions.PermPrivatePhotoView, limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT u.userid::text, u.username, u.email, COALESCE(u.fullname, ''),
		       u.joined_at::text,
		       u.account_enabled, u.can_manage_own_labels, u.can_manage_own_emoji, u.can_manage_own_comments,
		       EXISTS (
		           SELECT 1
		           FROM   entity_role_grants erg
		           JOIN   roles              r ON r.roleid = erg.roleid
		           WHERE  r.exhibitionid = $%d::uuid AND r.name = '__grant:' || $%d
		             AND  erg.entity_type = 'User' AND erg.entity_ref = u.userid::text
		             AND  erg.resource_type IS NULL
		       ) AS can_view_private
		FROM   users u
		JOIN   user_exhibitions ue ON ue.userid = u.userid
		WHERE  %s
		ORDER  BY u.username
		LIMIT  $%d OFFSET $%d
	`, n, n+1, where, n+2, n+3), rowArgs...)
	if err != nil {
		slog.Error("ListUsers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	users := make([]adminUser, 0, limit)
	for rows.Next() {
		var u adminUser
		if err := rows.Scan(&u.UserID, &u.Username, &u.Email, &u.FullName, &u.JoinedAt,
			&u.AccountEnabled, &u.CanManageOwnLabels, &u.CanManageOwnEmoji, &u.CanManageOwnComments,
			&u.CanViewPrivate); err != nil {
			slog.Error("ListUsers", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ListUsers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"users":  users,
	})
}

// PATCH /api/v1/admin/users/:userid?exhibitionid=  (Phase 6b)
// Body: any subset of
//
//	{ "account_enabled": bool, "can_view_private": bool,
//	  "can_manage_own_labels": bool, "can_manage_own_emoji": bool,
//	  "can_manage_own_comments": bool }
//
// Requires: authenticated + (PermAdmin or PermUserAdmin).
func (h *AdminHandler) UpdateUser(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	targetUserID := ps.ByName("userid")
	callerUserID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.HasAny(ctx, callerUserID, exhibitionID, permissions.PermAdmin, permissions.PermUserAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}
	if exhibitionID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "exhibitionid is required")
		return
	}

	var req struct {
		AccountEnabled       *bool `json:"account_enabled"`
		CanViewPrivate       *bool `json:"can_view_private"`
		CanManageOwnLabels   *bool `json:"can_manage_own_labels"`
		CanManageOwnEmoji    *bool `json:"can_manage_own_emoji"`
		CanManageOwnComments *bool `json:"can_manage_own_comments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Guard against an admin locking themselves out by disabling their own
	// account — they'd have no way to re-enable it without direct DB access.
	if req.AccountEnabled != nil && !*req.AccountEnabled && targetUserID == callerUserID {
		middleware.WriteError(w, http.StatusBadRequest, "you cannot disable your own account")
		return
	}

	if req.AccountEnabled != nil {
		if _, err := h.DB.Exec(ctx, `UPDATE users SET account_enabled = $1 WHERE userid = $2`,
			*req.AccountEnabled, targetUserID); err != nil {
			slog.Error("UpdateUser account_enabled", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}
	if req.CanManageOwnLabels != nil {
		if _, err := h.DB.Exec(ctx, `UPDATE users SET can_manage_own_labels = $1 WHERE userid = $2`,
			*req.CanManageOwnLabels, targetUserID); err != nil {
			slog.Error("UpdateUser can_manage_own_labels", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}
	if req.CanManageOwnEmoji != nil {
		if _, err := h.DB.Exec(ctx, `UPDATE users SET can_manage_own_emoji = $1 WHERE userid = $2`,
			*req.CanManageOwnEmoji, targetUserID); err != nil {
			slog.Error("UpdateUser can_manage_own_emoji", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}
	if req.CanManageOwnComments != nil {
		if _, err := h.DB.Exec(ctx, `UPDATE users SET can_manage_own_comments = $1 WHERE userid = $2`,
			*req.CanManageOwnComments, targetUserID); err != nil {
			slog.Error("UpdateUser can_manage_own_comments", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}
	if req.CanViewPrivate != nil {
		var grantErr error
		if *req.CanViewPrivate {
			grantErr = h.Checker.GrantUserPermission(ctx, exhibitionID, targetUserID, permissions.PermPrivatePhotoView)
		} else {
			grantErr = h.Checker.RevokeUserPermission(ctx, exhibitionID, targetUserID, permissions.PermPrivatePhotoView)
		}
		if grantErr != nil {
			slog.Error("UpdateUser can_view_private", "error", grantErr)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
