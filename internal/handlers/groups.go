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

// GroupsHandler powers resource-group administration (PLAN2.md Phase 3a):
// named collections of Photos, Galleries, Displays, or Exhibitions. A group
// is either STATIC (resource_group_members rows explicitly added/removed
// through AddMember/RemoveMember below) or DYNAMIC (PLAN2.md Phase 3c —
// membership computed live from a label rule; see Create's doc comment and
// migrations/027_dynamic_groups_and_group_grants.sql's
// resource_group_effective_members view, which every membership read in
// this file goes through so it doesn't need to know which kind it's
// looking at).
//
// A group needs a container to be listed/searched within, and that
// container differs by resource_type (see migrations/026_resource_groups.sql
// for the full reasoning): a Photo/Gallery/Display group's members all live
// inside one exhibition, so those groups are exhibition-scoped. An
// Exhibition group's MEMBERS ARE EXHIBITIONS, so those groups are scoped to
// the organization the member exhibitions belong to instead. Every endpoint
// below picks its admin-check tier (Checker.HasAny for exhibition scope,
// isOrgAdmin for organization scope — the same two-tier dispatch
// roles.go/admin_grants.go established in PLAN2.md Phase 2e) from whichever
// scope actually applies to the group in question, not from a caller-chosen
// query param the way exhibitionid alone works for most other admin
// endpoints.
type GroupsHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// Group resource-type constants. Values intentionally match
// permissions.ResourcePhoto/ResourceGallery/ResourceDisplay exactly (groups
// and permission grants are different concepts reusing the same resource
// vocabulary), plus GroupResourceExhibition, which has no permissions._
// equivalent — see permissions.go's "There is no ResourceExhibition"
// comment; that's specifically about entity_role_grants' scope model, not
// resource_groups' membership model, where "a group of exhibitions" is a
// perfectly valid thing to have.
const (
	GroupResourcePhoto      = permissions.ResourcePhoto
	GroupResourceGallery    = permissions.ResourceGallery
	GroupResourceDisplay    = permissions.ResourceDisplay
	GroupResourceExhibition = "Exhibition"
)

func isValidGroupResourceType(s string) bool {
	switch s {
	case GroupResourcePhoto, GroupResourceGallery, GroupResourceDisplay, GroupResourceExhibition:
		return true
	default:
		return false
	}
}

type adminGroup struct {
	GroupID        string `json:"groupid"`
	ResourceType   string `json:"resource_type"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	MemberCount    int    `json:"member_count"`
	ExhibitionID   string `json:"exhibitionid,omitempty"`
	OrganizationID string `json:"organizationid,omitempty"`
	// IsDynamic and the two fields below are PLAN2.md Phase 3c: a dynamic
	// group has no explicit resource_group_members rows of its own — its
	// membership is computed live from every resource (of the group's own
	// resource_type) carrying a label matching LabelName (and, if set,
	// LabelValue) — see migrations/027_dynamic_groups_and_group_grants.sql's
	// resource_group_effective_members view, which both ListMembers and
	// MemberCount below are computed from. Immutable after creation, like
	// ResourceType — see Create's validation and Update's doc comment.
	IsDynamic  bool   `json:"is_dynamic"`
	LabelName  string `json:"label_name,omitempty"`
	LabelValue string `json:"label_value,omitempty"`
}

type adminGroupMember struct {
	ResourceRef string `json:"resource_ref"`
	DisplayName string `json:"display_name"`
	AddedAt     string `json:"added_at"`
}

// groupScope resolves a (non-deleted) group's admin-check tier
// ("exhibition" or "organization"), that tier's scope ID, the group's
// resource_type, and whether it's dynamic (PLAN2.md Phase 3c — a dynamic
// group rejects AddMember/RemoveMember, since its membership isn't stored
// in resource_group_members at all) — used both to pick the matching admin
// check and to validate new members against the right scope. 404s a bad
// groupid before doing anything else.
func groupScope(w http.ResponseWriter, r *http.Request, pool *db.Pool, groupID string) (kind, scopeID, resourceType string, isDynamic, ok bool) {
	var exhibitionID, organizationID *string
	err := pool.QueryRow(r.Context(), `
		SELECT exhibitionid::text, organizationid::text, resource_type, is_dynamic
		FROM   resource_groups
		WHERE  groupid = $1 AND deleted_at IS NULL
	`, groupID).Scan(&exhibitionID, &organizationID, &resourceType, &isDynamic)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "group not found")
		return "", "", "", false, false
	}
	if err != nil {
		slog.Error("groupScope", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return "", "", "", false, false
	}
	if organizationID != nil {
		return "organization", *organizationID, resourceType, isDynamic, true
	}
	// chk_resource_groups_exhibition_type guarantees exhibitionid is set
	// whenever organizationid isn't, for any real row reaching here.
	return "exhibition", *exhibitionID, resourceType, isDynamic, true
}

// checkGroupScopeAdmin applies the admin check matching kind/scopeID as
// resolved by groupScope — HasAny for an exhibition-scoped group, isOrgAdmin
// for an organization-scoped one. Mirrors roles.go's checkRoleScopeAdmin
// exactly.
func (h *GroupsHandler) checkGroupScopeAdmin(ctx context.Context, userID, kind, scopeID string, perms ...string) (bool, error) {
	if kind == "organization" {
		return isOrgAdmin(ctx, h.DB, h.Checker, userID, scopeID, perms...)
	}
	return h.Checker.HasAny(ctx, userID, scopeID, perms...)
}

// GET /api/v1/admin/groups?exhibitionid=&search=&offset=&limit=
// GET /api/v1/admin/groups?organizationid=&search=&offset=&limit=
// organizationid and exhibitionid are mutually exclusive — pass exactly
// one; only Exhibition-type groups live under an organizationid (see
// migrations/026_resource_groups.sql). resource_type optionally narrows the
// listing to one kind of group.
// Requires: authenticated + (PermAdmin or PermGroupView) at the requested tier.
func (h *GroupsHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
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
		ok, err = isOrgAdmin(ctx, h.DB, h.Checker, userID, organizationID, permissions.PermAdmin, permissions.PermGroupView)
		where = "g.organizationid = $1::uuid AND g.deleted_at IS NULL"
		args = []any{organizationID}
	} else {
		if exhibitionID == "" {
			middleware.WriteError(w, http.StatusBadRequest, "exhibitionid or organizationid is required")
			return
		}
		ok, err = h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermGroupView)
		where = "g.exhibitionid = $1::uuid AND g.deleted_at IS NULL"
		args = []any{exhibitionID}
	}
	if err != nil {
		slog.Error("Groups.List check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	resourceType := strings.TrimSpace(r.URL.Query().Get("resource_type"))

	if resourceType != "" {
		if !isValidGroupResourceType(resourceType) {
			middleware.WriteError(w, http.StatusBadRequest, "resource_type must be one of Photo, Gallery, Display, Exhibition")
			return
		}
		where += fmt.Sprintf(" AND g.resource_type = $%d", n)
		args = append(args, resourceType)
		n++
	}
	if search != "" {
		where += fmt.Sprintf(" AND g.name ILIKE $%d", n)
		args = append(args, "%"+search+"%")
		n++
	}

	var total int
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM resource_groups g WHERE "+where, args...).Scan(&total); err != nil {
		slog.Error("Groups.List count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// member_count is computed from resource_group_effective_members (PLAN2.md
	// Phase 3c), not resource_group_members directly, so a dynamic group's
	// count reflects its live, label-derived membership rather than always
	// showing 0 (it has no resource_group_members rows of its own at all).
	rowArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT g.groupid::text, g.resource_type, g.name, COALESCE(g.description, ''),
		       COALESCE(gm.member_count, 0),
		       COALESCE(g.exhibitionid::text, ''), COALESCE(g.organizationid::text, ''),
		       g.is_dynamic, COALESCE(rule.label_name, ''), COALESCE(rule.label_value, '')
		FROM   resource_groups g
		LEFT JOIN (
		    SELECT groupid, COUNT(*) AS member_count
		    FROM   resource_group_effective_members
		    GROUP  BY groupid
		) gm ON gm.groupid = g.groupid
		LEFT JOIN resource_group_label_rules rule ON rule.groupid = g.groupid
		WHERE  %s
		ORDER  BY g.name
		LIMIT  $%d OFFSET $%d
	`, where, n, n+1), rowArgs...)
	if err != nil {
		slog.Error("Groups.List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	groups := make([]adminGroup, 0)
	for rows.Next() {
		var g adminGroup
		if err := rows.Scan(&g.GroupID, &g.ResourceType, &g.Name, &g.Description,
			&g.MemberCount, &g.ExhibitionID, &g.OrganizationID,
			&g.IsDynamic, &g.LabelName, &g.LabelValue); err != nil {
			slog.Error("Groups.List", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Groups.List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"groups": groups,
	})
}

// POST /api/v1/admin/groups?exhibitionid=  (for Photo/Gallery/Display groups)
// POST /api/v1/admin/groups?organizationid=  (for Exhibition groups)
// Body: {"resource_type": "...", "name": "...", "description": "...",
//        "is_dynamic": false, "label_name": "...", "label_value": "..."}
// is_dynamic/label_name/label_value are PLAN2.md Phase 3c: a dynamic group's
// membership is computed live from every resource carrying a label named
// label_name (any value, if label_value is empty; that exact value
// otherwise) instead of explicit AddMember/RemoveMember calls — see
// migrations/027_dynamic_groups_and_group_grants.sql's
// resource_group_effective_members view. Both are set once at creation and
// immutable afterward, same as resource_type (see Update's own doc comment)
// — changing a group from static to dynamic (or vice versa) once other code
// may already depend on its membership shape is a bigger, deliberately
// out-of-scope change.
// Requires: authenticated + (PermAdmin or PermGroupCreate) at the requested tier.
func (h *GroupsHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	organizationID := strings.TrimSpace(r.URL.Query().Get("organizationid"))
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if organizationID == "" && exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}

	var req struct {
		ResourceType string `json:"resource_type"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		IsDynamic    bool   `json:"is_dynamic"`
		LabelName    string `json:"label_name"`
		LabelValue   string `json:"label_value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.ResourceType = strings.TrimSpace(req.ResourceType)
	req.Name = strings.TrimSpace(req.Name)
	req.LabelName = strings.TrimSpace(req.LabelName)
	req.LabelValue = strings.TrimSpace(req.LabelValue)
	if !isValidGroupResourceType(req.ResourceType) {
		middleware.WriteError(w, http.StatusBadRequest, "resource_type must be one of Photo, Gallery, Display, Exhibition")
		return
	}
	if req.Name == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.IsDynamic && req.LabelName == "" {
		middleware.WriteError(w, http.StatusBadRequest, "label_name is required for a dynamic group")
		return
	}
	if !req.IsDynamic && (req.LabelName != "" || req.LabelValue != "") {
		middleware.WriteError(w, http.StatusBadRequest, "label_name/label_value must be empty unless is_dynamic is true")
		return
	}
	// Mirrors migrations/026_resource_groups.sql's chk_resource_groups_exhibition_type
	// with a clean 400 instead of a raw constraint-violation error.
	if req.ResourceType == GroupResourceExhibition && organizationID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "organizationid is required for Exhibition-type groups")
		return
	}
	if req.ResourceType != GroupResourceExhibition && exhibitionID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "exhibitionid is required for this resource_type")
		return
	}

	var ok bool
	var err error
	if req.ResourceType == GroupResourceExhibition {
		ok, err = isOrgAdmin(ctx, h.DB, h.Checker, userID, organizationID, permissions.PermAdmin, permissions.PermGroupCreate)
	} else {
		ok, err = h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermGroupCreate)
	}
	if err != nil {
		slog.Error("Groups.Create check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		slog.Error("Groups.Create begin", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(ctx)

	var groupID string
	if req.ResourceType == GroupResourceExhibition {
		err = tx.QueryRow(ctx, `
			INSERT INTO resource_groups (resource_type, organizationid, name, description, is_dynamic)
			VALUES ($1, $2::uuid, $3, NULLIF($4, ''), $5)
			RETURNING groupid::text
		`, req.ResourceType, organizationID, req.Name, req.Description, req.IsDynamic).Scan(&groupID)
	} else {
		err = tx.QueryRow(ctx, `
			INSERT INTO resource_groups (resource_type, exhibitionid, name, description, is_dynamic)
			VALUES ($1, $2::uuid, $3, NULLIF($4, ''), $5)
			RETURNING groupid::text
		`, req.ResourceType, exhibitionID, req.Name, req.Description, req.IsDynamic).Scan(&groupID)
	}
	if err != nil {
		slog.Error("Groups.Create", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not create group (name may already be in use for this resource type)")
		return
	}

	if req.IsDynamic {
		if _, err := tx.Exec(ctx, `
			INSERT INTO resource_group_label_rules (groupid, label_name, label_value)
			VALUES ($1::uuid, $2, NULLIF($3, ''))
		`, groupID, req.LabelName, req.LabelValue); err != nil {
			slog.Error("Groups.Create label rule", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("Groups.Create commit", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	resp := adminGroup{
		GroupID:      groupID,
		ResourceType: req.ResourceType,
		Name:         req.Name,
		Description:  req.Description,
		IsDynamic:    req.IsDynamic,
		LabelName:    req.LabelName,
		LabelValue:   req.LabelValue,
	}
	if req.ResourceType == GroupResourceExhibition {
		resp.OrganizationID = organizationID
	} else {
		resp.ExhibitionID = exhibitionID
	}
	middleware.WriteJSON(w, http.StatusCreated, resp)
}

// PATCH /api/v1/admin/groups/:groupid
// Body: any subset of {"name": "...", "description": "..."}. resource_type
// is immutable after creation — changing it would orphan every existing
// member against migrations/026_resource_groups.sql's own invariants.
// Requires: authenticated + (PermAdmin or PermGroupModify) at the group's own tier.
func (h *GroupsHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	groupID := ps.ByName("groupid")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, _, _, ok := groupScope(w, r, h.DB, groupID)
	if !ok {
		return
	}
	if ok, err := h.checkGroupScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermGroupModify); err != nil {
		slog.Error("Groups.Update check", "error", err)
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

	hasDescription := req.Description != nil
	var descriptionVal string
	if hasDescription {
		descriptionVal = *req.Description
	}

	if _, err := h.DB.Exec(ctx, `
		UPDATE resource_groups SET
			name        = COALESCE($1, name),
			description = CASE WHEN $2 THEN $3 ELSE description END,
			updated_at  = NOW()
		WHERE groupid = $4
	`, req.Name, hasDescription, descriptionVal, groupID); err != nil {
		slog.Error("Groups.Update", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not update group (name may already be in use)")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/admin/groups/:groupid
// Soft-deletes the group. resource_group_members rows cascade-delete via
// their FK to resource_groups (ON DELETE CASCADE) only on a hard delete,
// which this isn't — but every listing query already filters on
// deleted_at IS NULL, so a soft-deleted group's membership stops being
// reachable immediately regardless (including resource_group_effective_
// members, whose static branch already filters on
// g.deleted_at IS NULL — migrations/027_dynamic_groups_and_group_grants.sql).
// A soft-deleted group referenced by an existing Group-scoped
// entity_role_grants row (PLAN2.md Phase 3d) simply stops granting anything
// — no entity_role_grants row needs cleanup here, unlike TeamsHandler.Delete,
// since the grant becomes inert on its own once the group can't resolve any
// members.
// Requires: authenticated + (PermAdmin or PermGroupDelete) at the group's own tier.
func (h *GroupsHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	groupID := ps.ByName("groupid")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, _, _, ok := groupScope(w, r, h.DB, groupID)
	if !ok {
		return
	}
	if ok, err := h.checkGroupScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermGroupDelete); err != nil {
		slog.Error("Groups.Delete check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	if _, err := h.DB.Exec(ctx, `UPDATE resource_groups SET deleted_at = NOW() WHERE groupid = $1`, groupID); err != nil {
		slog.Error("Groups.Delete", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// groupMemberDisplayQuery returns the resource-type-specific SQL that joins
// a group's effective members to the underlying resource table for a
// human-readable display name, mirroring admin_grants.go's per-resource-type
// name resolution. Unlike that one, a single group has exactly one
// resource_type, so this picks ONE query rather than a CASE covering every
// type at once.
//
// Sources from resource_group_effective_members (PLAN2.md Phase 3c —
// migrations/027_dynamic_groups_and_group_grants.sql), not
// resource_group_members directly, so this returns the right rows for both
// static and dynamic groups without the caller needing to know which kind
// it's looking at. added_at is NULL for a dynamic membership row (there's
// no "added" event — see the view's own doc comment), hence COALESCE to ''
// rather than a raw ::text cast, which would otherwise render Go's
// zero-value time as a real (wrong) timestamp string.
func groupMemberDisplayQuery(resourceType string) string {
	switch resourceType {
	case GroupResourcePhoto:
		return `
			SELECT rgm.resource_ref, COALESCE(p.title_text, '(untitled photo)'), COALESCE(rgm.added_at::text, '')
			FROM   resource_group_effective_members rgm
			LEFT   JOIN photos p ON p.photoid::text = rgm.resource_ref
			WHERE  rgm.groupid = $1
			ORDER  BY rgm.resource_ref`
	case GroupResourceGallery:
		return `
			SELECT rgm.resource_ref, COALESCE(gal.title, '(deleted gallery)'), COALESCE(rgm.added_at::text, '')
			FROM   resource_group_effective_members rgm
			LEFT   JOIN galleries gal ON gal.galleryid::text = rgm.resource_ref
			WHERE  rgm.groupid = $1
			ORDER  BY rgm.resource_ref`
	case GroupResourceDisplay:
		return `
			SELECT rgm.resource_ref, 'Display in ' || COALESCE(g2.title, '(deleted gallery)'), COALESCE(rgm.added_at::text, '')
			FROM   resource_group_effective_members rgm
			LEFT   JOIN displays  d  ON d.displayid::text = rgm.resource_ref
			LEFT   JOIN galleries g2 ON g2.galleryid = d.galleryid
			WHERE  rgm.groupid = $1
			ORDER  BY rgm.resource_ref`
	default: // GroupResourceExhibition
		return `
			SELECT rgm.resource_ref, COALESCE(ex.name, '(deleted exhibition)'), COALESCE(rgm.added_at::text, '')
			FROM   resource_group_effective_members rgm
			LEFT   JOIN exhibitions ex ON ex.exhibitionid::text = rgm.resource_ref
			WHERE  rgm.groupid = $1
			ORDER  BY rgm.resource_ref`
	}
}

// GET /api/v1/admin/groups/:groupid/members
// Requires: authenticated + (PermAdmin or PermGroupView) at the group's own tier.
func (h *GroupsHandler) ListMembers(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	groupID := ps.ByName("groupid")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, resourceType, _, ok := groupScope(w, r, h.DB, groupID)
	if !ok {
		return
	}
	if ok, err := h.checkGroupScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermGroupView); err != nil {
		slog.Error("Groups.ListMembers check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	rows, err := h.DB.Query(ctx, groupMemberDisplayQuery(resourceType), groupID)
	if err != nil {
		slog.Error("Groups.ListMembers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	members := make([]adminGroupMember, 0)
	for rows.Next() {
		var m adminGroupMember
		if err := rows.Scan(&m.ResourceRef, &m.DisplayName, &m.AddedAt); err != nil {
			slog.Error("Groups.ListMembers", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Groups.ListMembers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{"members": members})
}

// validateGroupMember confirms resourceRef actually exists (isn't
// soft-deleted) and belongs to the group's own scope before it's allowed to
// be added — resourceType determines both which table to check and which
// scope column applies (exhibitionid for Photo/Gallery/Display, since
// scopeID is the group's exhibitionid for those; organizationid for
// Exhibition, since scopeID is the group's organizationid there — see
// groupScope). Returns a message suitable for a 400 response, or "" if
// valid.
func validateGroupMember(ctx context.Context, pool *db.Pool, resourceType, scopeID, resourceRef string) (string, error) {
	var query string
	switch resourceType {
	case GroupResourcePhoto:
		query = `SELECT 1 FROM photos WHERE photoid = $1::uuid AND exhibitionid = $2::uuid AND deleted_at IS NULL`
	case GroupResourceGallery:
		query = `SELECT 1 FROM galleries WHERE galleryid = $1::uuid AND exhibitionid = $2::uuid AND deleted_at IS NULL`
	case GroupResourceDisplay:
		query = `
			SELECT 1 FROM displays d JOIN galleries g ON g.galleryid = d.galleryid
			WHERE  d.displayid = $1::uuid AND g.exhibitionid = $2::uuid
			  AND  d.deleted_at IS NULL AND g.deleted_at IS NULL`
	default: // GroupResourceExhibition
		query = `SELECT 1 FROM exhibitions WHERE exhibitionid = $1::uuid AND organizationid = $2::uuid AND deleted_at IS NULL`
	}
	var exists int
	err := pool.QueryRow(ctx, query, resourceRef, scopeID).Scan(&exists)
	if err == pgx.ErrNoRows {
		return resourceType + " not found within this group's scope", nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

// POST /api/v1/admin/groups/:groupid/members
// Body: {"resource_ref": "..."}
// Requires: authenticated + (PermAdmin or PermGroupModify) at the group's own tier.
func (h *GroupsHandler) AddMember(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	groupID := ps.ByName("groupid")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, resourceType, isDynamic, ok := groupScope(w, r, h.DB, groupID)
	if !ok {
		return
	}
	if ok, err := h.checkGroupScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermGroupModify); err != nil {
		slog.Error("Groups.AddMember check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}
	// A dynamic group's membership is computed from its label rule (PLAN2.md
	// Phase 3c) — there's no resource_group_members row to add here at all;
	// attach the label to the resource instead (internal/handlers/
	// resource_labels.go for Gallery/Display/Exhibition, labels.go for Photo).
	if isDynamic {
		middleware.WriteError(w, http.StatusBadRequest, "cannot manually add members to a dynamic group — its membership is derived from its label rule")
		return
	}

	var req struct {
		ResourceRef string `json:"resource_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.ResourceRef = strings.TrimSpace(req.ResourceRef)
	if req.ResourceRef == "" {
		middleware.WriteError(w, http.StatusBadRequest, "resource_ref is required")
		return
	}

	if msg, err := validateGroupMember(ctx, h.DB, resourceType, scopeID, req.ResourceRef); err != nil {
		slog.Error("Groups.AddMember validate", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if msg != "" {
		middleware.WriteError(w, http.StatusBadRequest, msg)
		return
	}

	if _, err := h.DB.Exec(ctx, `
		INSERT INTO resource_group_members (groupid, resource_ref, added_by_userid)
		VALUES ($1, $2, $3)
		ON CONFLICT (groupid, resource_ref) DO NOTHING
	`, groupID, req.ResourceRef, userID); err != nil {
		slog.Error("Groups.AddMember", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/admin/groups/:groupid/members/:resourceref
// Requires: authenticated + (PermAdmin or PermGroupModify) at the group's own tier.
func (h *GroupsHandler) RemoveMember(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	groupID := ps.ByName("groupid")
	resourceRef := ps.ByName("resourceref")
	userID, _ := middleware.UserID(ctx)

	kind, scopeID, _, isDynamic, ok := groupScope(w, r, h.DB, groupID)
	if !ok {
		return
	}
	if ok, err := h.checkGroupScopeAdmin(ctx, userID, kind, scopeID, permissions.PermAdmin, permissions.PermGroupModify); err != nil {
		slog.Error("Groups.RemoveMember check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	} else if !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}
	if isDynamic {
		middleware.WriteError(w, http.StatusBadRequest, "cannot manually remove members from a dynamic group — remove the label from the resource instead")
		return
	}

	if _, err := h.DB.Exec(ctx, `
		DELETE FROM resource_group_members WHERE groupid = $1 AND resource_ref = $2
	`, groupID, resourceRef); err != nil {
		slog.Error("Groups.RemoveMember", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
