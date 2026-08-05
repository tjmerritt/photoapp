package handlers

import (
	"context"
	"encoding/json"
	"fmt"
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

// RolesHandler powers the roles admin page (Phase 6i): creating, editing,
// and deleting roles, and managing which permissions each one bundles. Also
// serves the "Add grant" popup's Role dropdown (Phase 6h), since that's the
// same underlying "list roles for this exhibition" capability.
//
// Auto-managed singleton roles (name starting with singletonRolePrefix —
// see permissions.Checker's per-user grant mechanism behind the Users admin
// page's "view private photos" toggle, Session 57) are excluded from every
// listing here and cannot be edited or deleted through these endpoints:
// they're an internal implementation detail, not something an admin should
// hand-edit. Breaking one would silently break that toggle for whichever
// user it was granted to.
type RolesHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// singletonRolePrefix matches permissions.singletonGrantRoleName's "__grant:"
// prefix. Duplicated here (rather than exported from the permissions
// package) because it's purely a display/guard concern for this admin page,
// not part of the permission-checking engine itself.
const singletonRolePrefix = "__grant:"

// adminRole is the shape returned by both role-listing endpoints.
type adminRole struct {
	RoleID      string   `json:"roleid"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	GrantCount  int      `json:"grant_count"`
}

// roleScope resolves whether a (non-deleted, non-singleton) role belongs to
// an exhibition or an organization, and which one — used both to pick the
// matching admin check (HasAny for an exhibition-scoped role, isOrgAdmin for
// an organization-scoped one) and to 404 on a bad/singleton roleid before
// doing anything else.
//
// Before PLAN2.md Phase 2e this only ever resolved an exhibitionid — a
// role's exhibitionid has been nullable since migrations/021_org_admin.sql
// (Phase 1c) added organization-scoped roles (roles.organizationid,
// mutually exclusive with exhibitionid via chk_roles_scope_exclusive), but
// this whole handler treated a NULL exhibitionid the same as "not found,"
// so an organization-scoped role could never be edited, permissioned, or
// deleted through this API — only created, via grantOrgAdmin in
// exhibitions.go. kind is "exhibition" or "organization"; scopeID is that
// tier's ID.
func roleScope(w http.ResponseWriter, r *http.Request, pool *db.Pool, roleID string) (kind, scopeID string, ok bool) {
	var exhibitionID, organizationID *string
	var name string
	err := pool.QueryRow(r.Context(), `
		SELECT exhibitionid::text, organizationid::text, name FROM roles WHERE roleid = $1 AND deleted_at IS NULL
	`, roleID).Scan(&exhibitionID, &organizationID, &name)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "role not found")
		return "", "", false
	}
	if err != nil {
		slog.Error("roleScope", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return "", "", false
	}
	if strings.HasPrefix(name, singletonRolePrefix) {
		middleware.WriteError(w, http.StatusBadRequest, "this role is auto-managed and cannot be edited here")
		return "", "", false
	}
	switch {
	case exhibitionID != nil:
		return "exhibition", *exhibitionID, true
	case organizationID != nil:
		return "organization", *organizationID, true
	default:
		// chk_roles_scope_exclusive guarantees exactly one of these is set
		// for any real row reaching here — this branch means the row itself
		// is malformed, not a legitimate "not found."
		slog.Error("roleScope: role has neither exhibitionid nor organizationid", "roleid", roleID)
		middleware.WriteError(w, http.StatusInternalServerError, "role has no scope")
		return "", "", false
	}
}

// checkRoleScopeAdmin applies the admin check matching kind/scopeID as
// resolved by roleScope — HasAny for an exhibition-scoped role, isOrgAdmin
// for an organization-scoped one. Shared by every roles.go endpoint that
// operates on an existing role (Update, Delete, AddPermission,
// RemovePermission).
func (h *RolesHandler) checkRoleScopeAdmin(ctx context.Context, userID, kind, scopeID string, perms ...string) (bool, error) {
	if kind == "organization" {
		return isOrgAdmin(ctx, h.DB, h.Checker, userID, scopeID, perms...)
	}
	return h.Checker.HasAny(ctx, userID, scopeID, perms...)
}

// GET /api/v1/admin/roles?exhibitionid=&search=&offset=&limit=  (Phase 6h, 6i)
// GET /api/v1/admin/roles?organizationid=&search=&offset=&limit=  (Phase 2e)
// organizationid and exhibitionid are mutually exclusive — pass exactly one
// to list that tier's roles. Requires: authenticated + (PermAdmin or
// PermPermissionsAdmin or PermRoleView) at the requested tier (isOrgAdmin
// for organizationid, HasAny for exhibitionid).
func (h *RolesHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	organizationID := strings.TrimSpace(r.URL.Query().Get("organizationid"))
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if organizationID == "" && exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}

	var where string
	var args []any
	var ok bool
	var err error
	n := 2
	if organizationID != "" {
		ok, err = isOrgAdmin(ctx, h.DB, h.Checker, userID, organizationID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleView)
		where = "r.organizationid = $1::uuid AND r.deleted_at IS NULL AND LEFT(r.name, 8) <> '" + singletonRolePrefix + "'"
		args = []any{organizationID}
	} else {
		if exhibitionID == "" {
			middleware.WriteError(w, http.StatusBadRequest, "exhibitionid or organizationid is required")
			return
		}
		ok, err = h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleView)
		where = "r.exhibitionid = $1::uuid AND r.deleted_at IS NULL AND LEFT(r.name, 8) <> '" + singletonRolePrefix + "'"
		args = []any{exhibitionID}
	}
	if err != nil {
		slog.Error("Roles.List check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)
	search := strings.TrimSpace(r.URL.Query().Get("search"))

	if search != "" {
		where += fmt.Sprintf(" AND r.name ILIKE $%d", n)
		args = append(args, "%"+search+"%")
		n++
	}

	var total int
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM roles r WHERE "+where, args...).Scan(&total); err != nil {
		slog.Error("Roles.List count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rowArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT r.roleid::text, r.name, COALESCE(r.description, ''),
		       `+grantPermsSubquery+` AS perms,
		       (SELECT COUNT(*) FROM entity_role_grants erg WHERE erg.roleid = r.roleid) AS grant_count
		FROM   roles r
		WHERE  %s
		ORDER  BY r.name
		LIMIT  $%d OFFSET $%d
	`, where, n, n+1), rowArgs...)
	if err != nil {
		slog.Error("Roles.List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	roles := make([]adminRole, 0)
	for rows.Next() {
		var role adminRole
		if err := rows.Scan(&role.RoleID, &role.Name, &role.Description, &role.Permissions, &role.GrantCount); err != nil {
			slog.Error("Roles.List", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Roles.List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"roles":  roles,
	})
}

// POST /api/v1/admin/roles?exhibitionid=  (Phase 6i)
// POST /api/v1/admin/roles?organizationid=  (Phase 2e)
// Body: {"name": "...", "description": "..."}. Creates a role with an empty
// permission bundle — permissions are added afterward via AddPermission.
// organizationid and exhibitionid are mutually exclusive — pass exactly one
// to create a role at that tier. Requires: authenticated + (PermAdmin or
// PermPermissionsAdmin or PermRoleCreate) at the requested tier.
func (h *RolesHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	organizationID := strings.TrimSpace(r.URL.Query().Get("organizationid"))
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if organizationID == "" && exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}

	var ok bool
	var err error
	if organizationID != "" {
		ok, err = isOrgAdmin(ctx, h.DB, h.Checker, userID, organizationID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleCreate)
	} else {
		if exhibitionID == "" {
			middleware.WriteError(w, http.StatusBadRequest, "exhibitionid or organizationid is required")
			return
		}
		ok, err = h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleCreate)
	}
	if err != nil {
		slog.Error("Roles.Create check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if strings.HasPrefix(req.Name, singletonRolePrefix) {
		middleware.WriteError(w, http.StatusBadRequest, "role names cannot start with "+singletonRolePrefix)
		return
	}

	var roleID string
	if organizationID != "" {
		err = h.DB.QueryRow(ctx, `
			INSERT INTO roles (organizationid, name, description)
			VALUES ($1::uuid, $2, NULLIF($3, ''))
			RETURNING roleid::text
		`, organizationID, req.Name, req.Description).Scan(&roleID)
	} else {
		err = h.DB.QueryRow(ctx, `
			INSERT INTO roles (exhibitionid, name, description)
			VALUES ($1::uuid, $2, NULLIF($3, ''))
			RETURNING roleid::text
		`, exhibitionID, req.Name, req.Description).Scan(&roleID)
	}
	if err != nil {
		slog.Error("Roles.Create", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not create role (name may already be in use)")
		return
	}

	middleware.WriteJSON(w, http.StatusCreated, adminRole{
		RoleID:      roleID,
		Name:        req.Name,
		Description: req.Description,
		Permissions: []string{},
	})
}

// PATCH /api/v1/admin/roles/:roleid  (Phase 6i)
// Body: any subset of {"name": "...", "description": "..."}
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin or PermRoleModify).
func (h *RolesHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	roleID := ps.ByName("roleid")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, ok := roleScope(w, r, h.DB, roleID)
	if !ok {
		return
	}
	if ok, err := h.checkRoleScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleModify); err != nil {
		slog.Error("Roles.Update check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == nil && req.Description == nil {
		middleware.WriteError(w, http.StatusBadRequest, "name or description is required")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name cannot be empty")
		return
	}
	if req.Name != nil && strings.HasPrefix(strings.TrimSpace(*req.Name), singletonRolePrefix) {
		middleware.WriteError(w, http.StatusBadRequest, "role names cannot start with "+singletonRolePrefix)
		return
	}

	hasDescription := req.Description != nil
	var descriptionVal string
	if hasDescription {
		descriptionVal = *req.Description
	}

	if _, err := h.DB.Exec(ctx, `
		UPDATE roles SET
			name        = COALESCE($1, name),
			description = CASE WHEN $2 THEN $3 ELSE description END,
			updated_at  = NOW()
		WHERE roleid = $4
	`, req.Name, hasDescription, descriptionVal, roleID); err != nil {
		slog.Error("Roles.Update", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not update role (name may already be in use)")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/admin/roles/:roleid  (Phase 6i)
// Soft-deletes the role. Every permission query already filters on
// r.deleted_at IS NULL (see internal/permissions/permissions.go and
// admin_grants.go), so this alone fully and immediately revokes everything
// the role granted — the role_permissions and entity_role_grants cleanup
// below is proactive hygiene (no dangling rows left referencing a dead
// role), not a correctness fix.
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin or PermRoleDelete).
func (h *RolesHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	roleID := ps.ByName("roleid")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, ok := roleScope(w, r, h.DB, roleID)
	if !ok {
		return
	}
	if ok, err := h.checkRoleScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleDelete); err != nil {
		slog.Error("Roles.Delete check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		slog.Error("Roles.Delete begin", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE roles SET deleted_at = NOW() WHERE roleid = $1`, roleID); err != nil {
		slog.Error("Roles.Delete role", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entity_role_grants WHERE roleid = $1`, roleID); err != nil {
		slog.Error("Roles.Delete grants", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE roleid = $1`, roleID); err != nil {
		slog.Error("Roles.Delete permissions", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("Roles.Delete commit", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/admin/roles/:roleid/permissions  (Phase 6i)
// Body: {"permission": "..."}
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin or PermRoleModify).
func (h *RolesHandler) AddPermission(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	roleID := ps.ByName("roleid")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, ok := roleScope(w, r, h.DB, roleID)
	if !ok {
		return
	}
	if ok, err := h.checkRoleScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleModify); err != nil {
		slog.Error("Roles.AddPermission check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	var req struct {
		Permission string `json:"permission"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Permission = strings.TrimSpace(req.Permission)
	if !permissions.IsValidPermission(req.Permission) {
		middleware.WriteError(w, http.StatusBadRequest, "unknown permission")
		return
	}

	if _, err := h.DB.Exec(ctx, `
		INSERT INTO role_permissions (roleid, permission) VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, roleID, req.Permission); err != nil {
		slog.Error("Roles.AddPermission", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/admin/roles/:roleid/permissions/:permission  (Phase 6i)
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin or PermRoleModify).
func (h *RolesHandler) RemovePermission(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	roleID := ps.ByName("roleid")
	permission := ps.ByName("permission")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, ok := roleScope(w, r, h.DB, roleID)
	if !ok {
		return
	}
	if ok, err := h.checkRoleScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleModify); err != nil {
		slog.Error("Roles.RemovePermission check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	if _, err := h.DB.Exec(ctx, `
		DELETE FROM role_permissions WHERE roleid = $1 AND permission = $2
	`, roleID, permission); err != nil {
		slog.Error("Roles.RemovePermission", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/admin/permission-catalog  (Phase 6i)
// Returns every known permission, grouped for display — populates the roles
// admin page's permission checkbox grid. Static data; still gated behind
// admin auth for consistency with every other /api/v1/admin/* endpoint.
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin or PermRoleView).
func (h *RolesHandler) PermissionCatalog(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermPermissionsAdmin, permissions.PermRoleView); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"groups": permissions.PermissionCatalog(),
	})
}
