package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

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
	GrantID        string `json:"grantid"`
	EntityType     string `json:"entity_type"`
	EntityRef      string `json:"entity_ref,omitempty"`
	EntityName     string `json:"entity_name"`
	RoleID         string `json:"roleid"`
	RoleName       string `json:"role_name"`
	ExhibitionID   string `json:"exhibitionid"`
	ExhibitionName string `json:"exhibition_name"`
	ResourceType   string `json:"resource_type,omitempty"`
	ResourceRef    string `json:"resource_ref,omitempty"`
	ResourceName   string `json:"resource_name,omitempty"`
	GrantedAt      string `json:"granted_at"`
	GrantedBy      string `json:"granted_by,omitempty"`
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
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin).
func (h *GrantsHandler) ListGlobal(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
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

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	var total int
	if err := h.DB.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM   entity_role_grants erg
		JOIN   roles r ON r.roleid = erg.roleid
		WHERE  erg.exhibitionid IS NULL AND erg.resource_type IS NULL AND r.deleted_at IS NULL
	`).Scan(&total); err != nil {
		slog.Error("Grants.ListGlobal count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rows, err := h.DB.Query(ctx, `
		SELECT erg.id::text, erg.entity_type, COALESCE(erg.entity_ref, ''),
		       `+grantEntityNameCase+` AS entity_name,
		       r.roleid::text, r.name,
		       ex.exhibitionid::text, ex.name,
		       erg.granted_at::text,
		       COALESCE(gb.username, '')
		FROM   entity_role_grants erg
		JOIN   roles       r  ON r.roleid = erg.roleid
		JOIN   exhibitions ex ON ex.exhibitionid = r.exhibitionid
		`+grantEntityJoins+`
		WHERE  erg.exhibitionid IS NULL AND erg.resource_type IS NULL AND r.deleted_at IS NULL
		ORDER  BY `+grantEntityRank+`, ex.name, r.name
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

// DELETE /api/v1/admin/grants/:grantid?exhibitionid=  (Phase 6g)
// Revokes a single entity_role_grants row outright. Does not touch the
// underlying role or its permissions — just the one grant of that role to
// that entity/resource.
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin).
func (h *GrantsHandler) Revoke(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	grantID := ps.ByName("grantid")
	userID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermPermissionsAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	ct, err := h.DB.Exec(ctx, `DELETE FROM entity_role_grants WHERE id = $1`, grantID)
	if err != nil {
		slog.Error("Grants.Revoke", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		middleware.WriteError(w, http.StatusNotFound, "grant not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// createGrantRequest is the JSON body accepted by POST /api/v1/admin/grants.
//
// ExhibitionID and ResourceType/ResourceRef are mutually exclusive (mirrors
// entity_role_grants' chk_grant_scope_exclusive constraint — see
// migrations/018_grant_exhibitionid.sql and migrations/021_org_admin.sql):
//   - both empty            -> global grant (every exhibition)
//   - ExhibitionID set      -> scoped to that one exhibition
//   - ResourceType/Ref set  -> scoped to that one Gallery/Display/Photo
//
// This endpoint doesn't accept an OrganizationID — organization-scoped
// grants (PLAN2.md Phase 1c) are only created today by
// ExhibitionsHandler.grantOrgAdmin when a new organization is created; there
// is no org-admin management API/UI yet (that's Phase 1d/2 territory).
type createGrantRequest struct {
	RoleID       string `json:"roleid"`
	EntityType   string `json:"entity_type"`
	EntityRef    string `json:"entity_ref"`
	ExhibitionID string `json:"exhibitionid"`
	ResourceType string `json:"resource_type"`
	ResourceRef  string `json:"resource_ref"`
}

// validateGrantRequest trims req's fields in place and checks them against
// the same rules the database itself enforces (entity_type/entity_ref
// pairing, exhibitionid vs. resource_type/resource_ref mutual exclusivity,
// resource_type/resource_ref pairing). Returns an empty string when valid,
// otherwise a message suitable for a 400 response. Shared by Create and
// Update so a grant's scope is validated identically whether it's being
// created fresh or edited in place.
func validateGrantRequest(req *createGrantRequest) string {
	req.RoleID = strings.TrimSpace(req.RoleID)
	req.EntityType = strings.TrimSpace(req.EntityType)
	req.EntityRef = strings.TrimSpace(req.EntityRef)
	req.ExhibitionID = strings.TrimSpace(req.ExhibitionID)
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

	if req.ExhibitionID != "" && req.ResourceType != "" {
		return "a grant cannot be scoped to both an exhibition and a specific resource"
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
// Creates a new entity_role_grants row. The exhibitionid query param is only
// used for the permission check (which exhibition's admin panel is making
// the request) — the grant's own scope comes entirely from the request body.
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin).
func (h *GrantsHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
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

	var req createGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := validateGrantRequest(&req); msg != "" {
		middleware.WriteError(w, http.StatusBadRequest, msg)
		return
	}

	// exhibitionid and granted_by go through nullableUUID rather than SQL-side
	// NULLIF: an organization-level or global grant legitimately has an empty
	// ExhibitionID, and when a parameter's only use in a query is inside a
	// ::uuid cast, Postgres infers its type as uuid itself and fails to bind
	// the empty string before NULLIF ever runs. See permissions.nullableUUID's
	// doc comment and SUMMARIES2.md Session 24.
	var grantID string
	err := h.DB.QueryRow(ctx, `
		INSERT INTO entity_role_grants
		    (roleid, entity_type, entity_ref, exhibitionid, resource_type, resource_ref, granted_by)
		VALUES
		    ($1::uuid, $2, NULLIF($3, ''), $4::uuid, NULLIF($5, ''), NULLIF($6, ''), $7::uuid)
		RETURNING id::text
	`, req.RoleID, req.EntityType, req.EntityRef, nullableUUID(req.ExhibitionID), req.ResourceType, req.ResourceRef, nullableUUID(userID)).Scan(&grantID)
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
// Requires: authenticated + (PermAdmin or PermPermissionsAdmin).
func (h *GrantsHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	grantID := ps.ByName("grantid")
	userID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermPermissionsAdmin); err != nil || !ok {
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

	ct, err := h.DB.Exec(ctx, `
		UPDATE entity_role_grants SET
		    roleid        = $1::uuid,
		    entity_type   = $2,
		    entity_ref    = NULLIF($3, ''),
		    exhibitionid  = $4::uuid,
		    resource_type = NULLIF($5, ''),
		    resource_ref  = NULLIF($6, '')
		WHERE id = $7
	`, req.RoleID, req.EntityType, req.EntityRef, nullableUUID(req.ExhibitionID), req.ResourceType, req.ResourceRef, grantID)
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
