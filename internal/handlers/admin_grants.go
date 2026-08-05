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

// GrantsHandler powers the permission-grants viewer admin page (Phase 6g):
// browsing (and revoking) raw entity_role_grants rows, split into two views
// per the permissions model's own split (see
// internal/permissions/permissions.go's Check() doc comment and
// migrations/018_grant_exhibitionid.sql):
//
//   - "Global" grants (exhibitionid IS NULL AND resource_type IS NULL) apply
//     everywhere, across every exhibition, regardless of which exhibition
//     the granting role calls "home".
//   - "Exhibition-specific" grants are scoped to one exhibition, either
//     directly (exhibitionid set, resource_type NULL — covers every
//     Gallery/Display/Photo within it) or via one specific Gallery/Display/
//     Photo resource that belongs to that exhibition.
type GrantsHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// adminGrant is the shape returned by both grants-listing endpoints.
//
// The permissions a role bundles are intentionally NOT included here — the
// grant is fundamentally an (entity, resource) -> role mapping, and showing
// each role's full permission list on every one of its grants would repeat
// the same details over and over across rows and bury the mapping itself.
// That detail belongs on the roles admin page (admin-roles.html) instead.
type adminGrant struct {
	GrantID          string `json:"grantid"`
	EntityType       string `json:"entity_type"`
	EntityRef        string `json:"entity_ref,omitempty"`
	EntityName       string `json:"entity_name"`
	RoleID           string `json:"roleid"`
	RoleName         string `json:"role_name"`
	ExhibitionID     string `json:"exhibitionid,omitempty"`
	ExhibitionName   string `json:"exhibition_name,omitempty"`
	OrganizationID   string `json:"organizationid,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	ResourceType     string `json:"resource_type,omitempty"`
	ResourceRef      string `json:"resource_ref,omitempty"`
	ResourceName     string `json:"resource_name,omitempty"`
	GrantedAt        string `json:"granted_at"`
	GrantedBy        string `json:"granted_by,omitempty"`
}

// The following SQL fragments are shared by ListGlobal and ListForExhibition
// below — both resolve the grant's entity to a friendly name the same way;
// only the WHERE clause and (for the exhibition-scoped one) the extra
// resource-name resolution differ.
const grantEntityNameCase = `
	CASE erg.entity_type
	    WHEN 'Public'   THEN 'Public'
	    WHEN 'LoggedIn' THEN 'Logged-in users'
	    WHEN 'User'     THEN COALESCE(u.username, '(deleted user)')
	    WHEN 'Team'     THEN COALESCE(tm.name, '(deleted team)')
	END`

const grantEntityJoins = `
	LEFT JOIN users u  ON erg.entity_type = 'User' AND u.userid::text = erg.entity_ref
	LEFT JOIN teams tm ON erg.entity_type = 'Team' AND tm.teamid::text = erg.entity_ref
	LEFT JOIN users gb ON gb.userid = erg.granted_by`

const grantPermsSubquery = `
	COALESCE((SELECT array_agg(rp.permission ORDER BY rp.permission)
	          FROM role_permissions rp WHERE rp.roleid = r.roleid), '{}')`

// grantEntityRank orders rows broadest-to-narrowest by entity type: Public
// (everyone) is broader than LoggedIn, which is broader than Team, which is
// broader than a single User. Used as the primary ORDER BY key in both
// listing queries below, matching the (entity, resource) -> role column
// order the admin page displays.
const grantEntityRank = `
	CASE erg.entity_type
	    WHEN 'Public'   THEN 0
	    WHEN 'LoggedIn' THEN 1
	    WHEN 'Team'     THEN 2
	    WHEN 'User'     THEN 3
	    ELSE 4
	END`

// grantResourceRank orders rows broadest-to-narrowest by resource scope:
// the whole exhibition (no resource_type) is broader than a Gallery, which
// is broader than a Display, which is broader than a single Photo. Used as
// the secondary ORDER BY key in ListForExhibition.
const grantResourceRank = `
	CASE COALESCE(erg.resource_type, '')
	    WHEN ''        THEN 0
	    WHEN 'Gallery' THEN 1
	    WHEN 'Display' THEN 2
	    WHEN 'Photo'   THEN 3
	    ELSE 4
	END`

// GET /api/v1/admin/grants/global?offset=&limit=  (Phase 6g)
// Lists grants that apply to literally every organization and exhibition in
// the install (exhibitionid, resource_type, AND organizationid all NULL —
// Checker.Check's own "Global grant" branch).
//
// Requires a true global grant of the caller's own (PLAN2.md Phase 2e).
// Before Phase 2e this only checked HasAny against whichever exhibitionid
// happened to be in the query string/request context — which let any single
// exhibition's admin view, create, edit, and revoke grants that reached
// every OTHER exhibition too, a real privilege-escalation gap: "admin of one
// exhibition" was being treated as sufficient to administer something that
// applies everywhere. isOrgAdmin's sibling for this tier is simpler —
// Checker.HasAny(ctx, userID, "", ...) IS the true-global check, the same
// one Checker.Check's own "Global grant" branch is built on (see its doc
// comment) and the same one ScopeHandler.ListOrganizations already uses to
// decide whether a caller can see every organization.
func (h *GrantsHandler) ListGlobal(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	if ok, err := h.Checker.HasAny(ctx, userID, "", permissions.PermAdmin, permissions.PermPermissionsAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "global admin access required")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	var total int
	if err := h.DB.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM   entity_role_grants erg
		JOIN   roles r ON r.roleid = erg.roleid
		WHERE  erg.exhibitionid IS NULL AND erg.resource_type IS NULL AND erg.organizationid IS NULL AND r.deleted_at IS NULL
	`).Scan(&total); err != nil {
		slog.Error("Grants.ListGlobal count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// A truly-global grant's role can itself be scoped to either an
	// exhibition or an organization (roles.exhibitionid/organizationid are
	// mutually exclusive but independent of the GRANT's own scope — see
	// migrations/021_org_admin.sql's chk_roles_scope_exclusive) purely as a
	// "who manages this role" label, so both are LEFT JOINed (never INNER —
	// an INNER JOIN to exhibitions alone silently dropped every global grant
	// whose role belonged to an organization instead, which is exactly how
	// this endpoint used to make org-scoped-role global grants invisible
	// without erroring).
	rows, err := h.DB.Query(ctx, `
		SELECT erg.id::text, erg.entity_type, COALESCE(erg.entity_ref, ''),
		       `+grantEntityNameCase+` AS entity_name,
		       r.roleid::text, r.name,
		       COALESCE(ex.exhibitionid::text, ''), COALESCE(ex.name, ''),
		       COALESCE(org.organizationid::text, ''), COALESCE(org.name, ''),
		       erg.granted_at::text,
		       COALESCE(gb.username, '')
		FROM   entity_role_grants erg
		JOIN   roles          r   ON r.roleid = erg.roleid
		LEFT   JOIN exhibitions   ex  ON ex.exhibitionid  = r.exhibitionid
		LEFT   JOIN organizations org ON org.organizationid = r.organizationid
		`+grantEntityJoins+`
		WHERE  erg.exhibitionid IS NULL AND erg.resource_type IS NULL AND erg.organizationid IS NULL AND r.deleted_at IS NULL
		ORDER  BY `+grantEntityRank+`, COALESCE(ex.name, org.name), r.name
		LIMIT  $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		slog.Error("Grants.ListGlobal", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	grants := make([]adminGrant, 0)
	for rows.Next() {
		var g adminGrant
		if err := rows.Scan(&g.GrantID, &g.EntityType, &g.EntityRef, &g.EntityName,
			&g.RoleID, &g.RoleName, &g.ExhibitionID, &g.ExhibitionName,
			&g.OrganizationID, &g.OrganizationName,
			&g.GrantedAt, &g.GrantedBy); err != nil {
			slog.Error("Grants.ListGlobal", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Grants.ListGlobal", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"grants": grants,
	})
}

// GET /api/v1/admin/grants/organization?organizationid=&offset=&limit=  (Phase 2e)
// Lists grants scoped directly to one organization (organizationid set —
// covers every exhibition under it, present and future). Unlike
// ListForExhibition, there's no cascading resource tier to also include:
// migrations/021_org_admin.sql's chk_grant_scope_exclusive forbids a grant
// from combining organizationid with resource_type, so a direct-only filter
// is already complete.
// Requires: authenticated + isOrgAdmin(organizationid, PermAdmin or PermPermissionsAdmin).
func (h *GrantsHandler) ListForOrganization(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	organizationID := strings.TrimSpace(r.URL.Query().Get("organizationid"))
	if organizationID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "organizationid is required")
		return
	}
	if ok, err := isOrgAdmin(ctx, h.DB, h.Checker, userID, organizationID, permissions.PermAdmin, permissions.PermPermissionsAdmin); err != nil || !ok {
		if err != nil {
			slog.Error("Grants.ListForOrganization check", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	var total int
	if err := h.DB.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM   entity_role_grants erg
		JOIN   roles r ON r.roleid = erg.roleid
		WHERE  erg.organizationid = $1::uuid AND r.deleted_at IS NULL
	`, organizationID).Scan(&total); err != nil {
		slog.Error("Grants.ListForOrganization count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rows, err := h.DB.Query(ctx, `
		SELECT erg.id::text, erg.entity_type, COALESCE(erg.entity_ref, ''),
		       `+grantEntityNameCase+` AS entity_name,
		       r.roleid::text, r.name,
		       org.organizationid::text, org.name,
		       erg.granted_at::text,
		       COALESCE(gb.username, '')
		FROM   entity_role_grants erg
		JOIN   roles         r   ON r.roleid = erg.roleid
		JOIN   organizations org ON org.organizationid = erg.organizationid
		`+grantEntityJoins+`
		WHERE  erg.organizationid = $1::uuid AND r.deleted_at IS NULL
		ORDER  BY `+grantEntityRank+`, r.name
		LIMIT  $2 OFFSET $3
	`, organizationID, limit, offset)
	if err != nil {
		slog.Error("Grants.ListForOrganization", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	grants := make([]adminGrant, 0)
	for rows.Next() {
		var g adminGrant
		if err := rows.Scan(&g.GrantID, &g.EntityType, &g.EntityRef, &g.EntityName,
			&g.RoleID, &g.RoleName, &g.OrganizationID, &g.OrganizationName,
			&g.GrantedAt, &g.GrantedBy); err != nil {
			slog.Error("Grants.ListForOrganization", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Grants.ListForOrganization", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"grants": grants,
	})
}

// GET /api/v1/admin/grants/exhibition?exhibitionid=&offset=&limit=  (Phase 6g)
// Lists grants scoped to one exhibition: directly (exhibitionid set,
// resource_type NULL) or via a Gallery/Display/Photo resource owned by it.
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin).
func (h *GrantsHandler) ListForExhibition(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermPermissionsAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}
	if exhibitionID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "exhibitionid is required")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	const where = `
		r.deleted_at IS NULL
		AND (
		       (erg.exhibitionid = $1::uuid AND erg.resource_type IS NULL)
		    OR (erg.resource_type IN ('Gallery', 'Display', 'Photo') AND r.exhibitionid = $1::uuid)
		)`

	var total int
	if err := h.DB.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM   entity_role_grants erg
		JOIN   roles r ON r.roleid = erg.roleid
		WHERE  `+where, exhibitionID).Scan(&total); err != nil {
		slog.Error("Grants.ListForExhibition count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rows, err := h.DB.Query(ctx, `
		SELECT erg.id::text, erg.entity_type, COALESCE(erg.entity_ref, ''),
		       `+grantEntityNameCase+` AS entity_name,
		       r.roleid::text, r.name,
		       r.exhibitionid::text, ex.name,
		       COALESCE(erg.resource_type, ''), COALESCE(erg.resource_ref, ''),
		       COALESCE(
		           CASE erg.resource_type
		               WHEN 'Gallery' THEN COALESCE(gal.title, '(deleted gallery)')
		               WHEN 'Display' THEN 'Display in ' || COALESCE(dispgal.title, '(deleted gallery)')
		               WHEN 'Photo'   THEN COALESCE(pho.title_text, '(untitled photo)')
		           END,
		           ''
		       ) AS resource_name,
		       erg.granted_at::text,
		       COALESCE(gb.username, '')
		FROM   entity_role_grants erg
		JOIN   roles       r  ON r.roleid = erg.roleid
		JOIN   exhibitions ex ON ex.exhibitionid = r.exhibitionid
		`+grantEntityJoins+`
		LEFT JOIN galleries gal     ON erg.resource_type = 'Gallery' AND gal.galleryid::text = erg.resource_ref
		LEFT JOIN displays  disp    ON erg.resource_type = 'Display' AND disp.displayid::text = erg.resource_ref
		LEFT JOIN galleries dispgal ON dispgal.galleryid = disp.galleryid
		LEFT JOIN photos    pho     ON erg.resource_type = 'Photo' AND pho.photoid::text = erg.resource_ref
		WHERE  `+where+`
		ORDER  BY `+grantEntityRank+`, `+grantResourceRank+`, resource_name, r.name
		LIMIT  $2 OFFSET $3
	`, exhibitionID, limit, offset)
	if err != nil {
		slog.Error("Grants.ListForExhibition", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	grants := make([]adminGrant, 0)
	for rows.Next() {
		var g adminGrant
		if err := rows.Scan(&g.GrantID, &g.EntityType, &g.EntityRef, &g.EntityName,
			&g.RoleID, &g.RoleName, &g.ExhibitionID, &g.ExhibitionName,
			&g.ResourceType, &g.ResourceRef, &g.ResourceName,
			&g.GrantedAt, &g.GrantedBy); err != nil {
			slog.Error("Grants.ListForExhibition", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Grants.ListForExhibition", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"grants": grants,
	})
}

// grantScope resolves how an existing entity_role_grants row is
// administered: organizationID is set for an organization-scoped grant,
// exhibitionID is set for an exhibition-scoped OR resource-scoped grant
// (for a resource-scoped one, that's the owning ROLE's exhibitionid, not a
// lookup of the resource itself — the same substitution
// ListForExhibition's own WHERE clause already relies on, since a
// Gallery/Display/Photo grant's role always belongs to the exhibition that
// resource is in), and isGlobal is true only when none of
// organizationid/exhibitionid/resource_type are set at all. found is false
// if no such grant exists. Used by Revoke and Update (for the row's CURRENT
// scope, before any edit) so both require the matching admin tier —
// PLAN2.md Phase 2e closes the gap where any exhibition admin could revoke
// or rewrite a grant that actually applies to an entire organization or the
// whole install, just because the query string happened to carry along
// some exhibitionid.
func (h *GrantsHandler) grantScope(ctx context.Context, grantID string) (organizationID, exhibitionID string, isGlobal, found bool, err error) {
	var org, exh, roleExh, resourceType *string
	err = h.DB.QueryRow(ctx, `
		SELECT erg.organizationid::text, erg.exhibitionid::text, r.exhibitionid::text, erg.resource_type
		FROM   entity_role_grants erg
		JOIN   roles r ON r.roleid = erg.roleid
		WHERE  erg.id = $1
	`, grantID).Scan(&org, &exh, &roleExh, &resourceType)
	if err == pgx.ErrNoRows {
		return "", "", false, false, nil
	}
	if err != nil {
		return "", "", false, false, err
	}
	switch {
	case org != nil:
		return *org, "", false, true, nil
	case exh != nil:
		return "", *exh, false, true, nil
	case resourceType != nil && roleExh != nil:
		return "", *roleExh, false, true, nil
	default:
		return "", "", true, true, nil
	}
}

// canManageGrantScope checks admin access for whichever tier grantScope (or
// the equivalent reasoning applied to a createGrantRequest's target scope)
// resolved to.
func (h *GrantsHandler) canManageGrantScope(ctx context.Context, userID, organizationID, exhibitionID string, isGlobal bool) (bool, error) {
	if isGlobal {
		return h.Checker.HasAny(ctx, userID, "", permissions.PermAdmin, permissions.PermPermissionsAdmin)
	}
	if organizationID != "" {
		return isOrgAdmin(ctx, h.DB, h.Checker, userID, organizationID, permissions.PermAdmin, permissions.PermPermissionsAdmin)
	}
	return h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermPermissionsAdmin)
}

// canManageRequestedScope is canManageGrantScope's counterpart for a
// createGrantRequest that hasn't been written to the database yet (Create,
// and Update's new target state) — req must already be validateGrantRequest-
// clean. r is only consulted for the resource-scoped case, to recover the
// admin page's "current exhibition" the way Create always has (a
// Gallery/Display/Photo grant carries no exhibitionid of its own in the
// request body, by design — see createGrantRequest's doc comment).
func (h *GrantsHandler) canManageRequestedScope(ctx context.Context, r *http.Request, userID string, req *createGrantRequest) (bool, error) {
	switch {
	case req.OrganizationID != "":
		return isOrgAdmin(ctx, h.DB, h.Checker, userID, req.OrganizationID, permissions.PermAdmin, permissions.PermPermissionsAdmin)
	case req.ExhibitionID != "" || req.ResourceType != "":
		exhibitionID := req.ExhibitionID
		if exhibitionID == "" {
			exhibitionID = r.URL.Query().Get("exhibitionid")
			if exhibitionID == "" {
				exhibitionID = middleware.ExhibitionID(r.Context())
			}
		}
		return h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermPermissionsAdmin)
	default:
		// Every scope field empty = a true global grant, reaching every
		// organization and exhibition in the install — only an existing
		// true global admin may create or rewrite one into existing.
		return h.Checker.HasAny(ctx, userID, "", permissions.PermAdmin, permissions.PermPermissionsAdmin)
	}
}

// DELETE /api/v1/admin/grants/:grantid  (Phase 6g)
// Revokes a single entity_role_grants row outright. Does not touch the
// underlying role or its permissions — just the one grant of that role to
// that entity/resource.
// Requires: authenticated + admin access matching the grant's OWN scope
// (organization/exhibition/global — see grantScope), not merely admin
// access to whatever exhibition the caller's admin page happens to be open
// to (PLAN2.md Phase 2e; previously any exhibition admin could revoke a
// grant scoped to an entirely different organization or to the whole
// install).
func (h *GrantsHandler) Revoke(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	grantID := ps.ByName("grantid")
	userID, _ := middleware.UserID(ctx)

	organizationID, exhibitionID, isGlobal, found, err := h.grantScope(ctx, grantID)
	if err != nil {
		slog.Error("Grants.Revoke scope", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !found {
		middleware.WriteError(w, http.StatusNotFound, "grant not found")
		return
	}
	if ok, err := h.canManageGrantScope(ctx, userID, organizationID, exhibitionID, isGlobal); err != nil {
		slog.Error("Grants.Revoke check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	if _, err := h.DB.Exec(ctx, `DELETE FROM entity_role_grants WHERE id = $1`, grantID); err != nil {
		slog.Error("Grants.Revoke", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// createGrantRequest is the JSON body accepted by POST /api/v1/admin/grants.
//
// ExhibitionID, ResourceType/ResourceRef, and OrganizationID are mutually
// exclusive (mirrors entity_role_grants' chk_grant_scope_exclusive
// constraint — see migrations/018_grant_exhibitionid.sql and
// migrations/021_org_admin.sql):
//   - all empty             -> global grant (every organization and exhibition)
//   - OrganizationID set    -> scoped to that one organization (every exhibition under it)
//   - ExhibitionID set      -> scoped to that one exhibition
//   - ResourceType/Ref set  -> scoped to that one Gallery/Display/Photo
//
// Before PLAN2.md Phase 2e this endpoint had no OrganizationID field at all
// — organization-scoped grants (Phase 1c) were only ever created by
// ExhibitionsHandler.grantOrgAdmin when a new organization is created, with
// no general-purpose admin API to create, edit, or browse another one.
type createGrantRequest struct {
	RoleID         string `json:"roleid"`
	EntityType     string `json:"entity_type"`
	EntityRef      string `json:"entity_ref"`
	ExhibitionID   string `json:"exhibitionid"`
	OrganizationID string `json:"organizationid"`
	ResourceType   string `json:"resource_type"`
	ResourceRef    string `json:"resource_ref"`
}

// validateGrantRequest trims req's fields in place and checks them against
// the same rules the database itself enforces (entity_type/entity_ref
// pairing, exhibitionid/resource_type/organizationid mutual exclusivity,
// resource_type/resource_ref pairing). Returns an empty string when valid,
// otherwise a message suitable for a 400 response. Shared by Create and
// Update so a grant's scope is validated identically whether it's being
// created fresh or edited in place.
func validateGrantRequest(req *createGrantRequest) string {
	req.RoleID = strings.TrimSpace(req.RoleID)
	req.EntityType = strings.TrimSpace(req.EntityType)
	req.EntityRef = strings.TrimSpace(req.EntityRef)
	req.ExhibitionID = strings.TrimSpace(req.ExhibitionID)
	req.OrganizationID = strings.TrimSpace(req.OrganizationID)
	req.ResourceType = strings.TrimSpace(req.ResourceType)
	req.ResourceRef = strings.TrimSpace(req.ResourceRef)

	if req.RoleID == "" {
		return "roleid is required"
	}

	switch req.EntityType {
	case permissions.EntityPublic, permissions.EntityLoggedIn:
		if req.EntityRef != "" {
			return "entity_ref must be empty for Public/LoggedIn grants"
		}
	case permissions.EntityTeam, permissions.EntityUser:
		if req.EntityRef == "" {
			return "entity_ref is required for Team/User grants"
		}
	default:
		return "entity_type must be one of Public, LoggedIn, Team, User"
	}

	scopeFieldsSet := 0
	if req.ExhibitionID != "" {
		scopeFieldsSet++
	}
	if req.ResourceType != "" {
		scopeFieldsSet++
	}
	if req.OrganizationID != "" {
		scopeFieldsSet++
	}
	if scopeFieldsSet > 1 {
		return "a grant cannot be scoped to more than one of: exhibition, resource, organization"
	}

	switch req.ResourceType {
	case "", permissions.ResourceGallery, permissions.ResourceDisplay, permissions.ResourcePhoto:
		// ok
	default:
		return "resource_type must be one of Gallery, Display, Photo, or empty"
	}
	if req.ResourceType != "" && req.ResourceRef == "" {
		return "resource_ref is required when resource_type is set"
	}
	if req.ResourceType == "" && req.ResourceRef != "" {
		return "resource_ref must be empty when resource_type is empty"
	}

	return ""
}

// POST /api/v1/admin/grants?exhibitionid=  (Phase 6h)
// Creates a new entity_role_grants row. Which admin tier is required depends
// on the SCOPE THE REQUEST BODY ASKS FOR, not the caller's current exhibition
// admin page: an organizationid grant requires isOrgAdmin for that org, a
// fully-empty (global) grant requires a true global admin, and only an
// exhibitionid/resource-scoped grant falls back to the query-string
// exhibitionid the way this endpoint has always worked (PLAN2.md Phase 2e —
// see canManageRequestedScope). Previously this checked HasAny against the
// query-string exhibitionid unconditionally, before the body was even
// decoded, which let any exhibition admin create a grant reaching every
// other exhibition or organization in the install.
// Requires: authenticated + admin access matching the requested grant's scope.
func (h *GrantsHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)

	var req createGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := validateGrantRequest(&req); msg != "" {
		middleware.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	if ok, err := h.canManageRequestedScope(ctx, r, userID, &req); err != nil {
		slog.Error("Grants.Create check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	// exhibitionid/organizationid and granted_by go through nullableUUID
	// rather than SQL-side NULLIF: an organization-level or global grant
	// legitimately has an empty ExhibitionID/OrganizationID, and when a
	// parameter's only use in a query is inside a ::uuid cast, Postgres
	// infers its type as uuid itself and fails to bind the empty string
	// before NULLIF ever runs. See permissions.nullableUUID's doc comment
	// and SUMMARIES2.md Session 24.
	var grantID string
	err := h.DB.QueryRow(ctx, `
		INSERT INTO entity_role_grants
		    (roleid, entity_type, entity_ref, exhibitionid, organizationid, resource_type, resource_ref, granted_by)
		VALUES
		    ($1::uuid, $2, NULLIF($3, ''), $4::uuid, $5::uuid, NULLIF($6, ''), NULLIF($7, ''), $8::uuid)
		RETURNING id::text
	`, req.RoleID, req.EntityType, req.EntityRef, nullableUUID(req.ExhibitionID), nullableUUID(req.OrganizationID), req.ResourceType, req.ResourceRef, nullableUUID(userID)).Scan(&grantID)
	if err != nil {
		slog.Error("Grants.Create", "error", err, "roleid", req.RoleID, "entity_type", req.EntityType)
		middleware.WriteError(w, http.StatusBadRequest, "could not create grant — check that the role and entity/resource all exist")
		return
	}

	middleware.WriteJSON(w, http.StatusCreated, map[string]any{"grantid": grantID})
}

// PATCH /api/v1/admin/grants/:grantid?exhibitionid=  (Phase 6j)
// Replaces a grant's role/entity/resource scope in place — same validation
// as Create, applied to an existing row instead of inserting a new one.
// Requires admin access for BOTH the grant's current scope (grantScope,
// checked before the edit — otherwise an org admin could hijack an
// unrelated exhibition's existing grant just by PATCHing it into their own
// org) AND the requested new scope (canManageRequestedScope, the same check
// Create uses — otherwise any exhibition admin could escalate an existing
// grant they're allowed to touch into a global or different-organization
// one). PLAN2.md Phase 2e; previously this only checked the query-string
// exhibitionid, for neither the grant's real current scope nor its
// requested new one.
// Requires: authenticated + admin access matching both scopes.
func (h *GrantsHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	grantID := ps.ByName("grantid")
	userID, _ := middleware.UserID(ctx)

	organizationID, exhibitionID, isGlobal, found, err := h.grantScope(ctx, grantID)
	if err != nil {
		slog.Error("Grants.Update scope", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !found {
		middleware.WriteError(w, http.StatusNotFound, "grant not found")
		return
	}
	if ok, err := h.canManageGrantScope(ctx, userID, organizationID, exhibitionID, isGlobal); err != nil {
		slog.Error("Grants.Update check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	var req createGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := validateGrantRequest(&req); msg != "" {
		middleware.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	if ok, err := h.canManageRequestedScope(ctx, r, userID, &req); err != nil {
		slog.Error("Grants.Update target check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required for the requested scope")
		return
	}

	ct, err := h.DB.Exec(ctx, `
		UPDATE entity_role_grants SET
		    roleid         = $1::uuid,
		    entity_type    = $2,
		    entity_ref     = NULLIF($3, ''),
		    exhibitionid   = $4::uuid,
		    organizationid = $5::uuid,
		    resource_type  = NULLIF($6, ''),
		    resource_ref   = NULLIF($7, '')
		WHERE id = $8
	`, req.RoleID, req.EntityType, req.EntityRef, nullableUUID(req.ExhibitionID), nullableUUID(req.OrganizationID), req.ResourceType, req.ResourceRef, grantID)
	if err != nil {
		slog.Error("Grants.Update", "error", err, "grantid", grantID)
		middleware.WriteError(w, http.StatusBadRequest, "could not update grant — check that the role and entity/resource all exist")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "grant not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
