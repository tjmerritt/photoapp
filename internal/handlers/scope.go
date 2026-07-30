package handlers

import (
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

// ScopeHandler powers the admin header's Organization/Exhibition picker
// (PLAN2.md Phase 1d): paginated, searchable lists of the organizations and
// exhibitions the caller can administer, meant to be fetched a page at a
// time as the picker popup is opened/searched/scrolled rather than loaded
// in full up front — an install can have far more of either than
// comfortably fits in a dropdown.
//
// Neither endpoint requires a specific permission to call — the query
// itself only ever returns organizations/exhibitions the caller can
// administer (PermAdmin), so there's nothing to leak by letting any
// authenticated user ask "which of these can I get into." This is also
// what makes these safe to call before any exhibition context has been
// established, unlike AdminHandler.ListExhibitions (which requires PermAdmin
// on the current exhibition just to be probed at all).
type ScopeHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

type scopeOrganization struct {
	OrganizationID string `json:"organizationid"`
	Name           string `json:"name"`
}

type scopeExhibition struct {
	ExhibitionID string `json:"exhibitionid"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
}

// entityMatchOwn is the "does this entity_role_grants row (aliased erg)
// apply to userID" fragment, referencing the given positional parameter
// (userID) twice — once for a direct User grant, once for Team membership.
// Deliberately narrower than Checker.Check's full Public/LoggedIn/Team/User
// match: a Public- or LoggedIn-scoped organization/exhibition Admin grant is
// something the schema allows but nothing in this codebase creates, and
// omitting it here only affects what shows up in this picker, not
// Checker.Check's actual access decisions.
func entityMatchOwn(userIDParam int) string {
	return fmt.Sprintf(`(
		    (erg.entity_type = 'User' AND erg.entity_ref = $%d)
		 OR (erg.entity_type = 'Team' AND erg.entity_ref IN (
		         SELECT teamid::text FROM team_members WHERE userid = $%d::uuid
		     ))
	)`, userIDParam, userIDParam)
}

// GET /api/v1/admin/organizations?search=&offset=&limit=
// Requires: authenticated.
func (h *ScopeHandler) ListOrganizations(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID := middleware.MustUserID(ctx)
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	// A true global (site-wide) Admin grant sees every organization —
	// there's no per-organization row to reach it through, since a global
	// grant has exhibitionid, organizationid, AND resource_type all NULL.
	isGlobalAdmin, err := h.Checker.Check(ctx, userID, "", "", "", "", permissions.PermAdmin)
	if err != nil {
		slog.Error("ListOrganizations global check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	where := "o.deleted_at IS NULL"
	args := []any{}
	n := 1
	if search != "" {
		where += fmt.Sprintf(" AND o.name ILIKE $%d", n)
		args = append(args, "%"+search+"%")
		n++
	}
	if !isGlobalAdmin {
		where += fmt.Sprintf(` AND (
			EXISTS (
				SELECT 1 FROM entity_role_grants erg
				JOIN   role_permissions rp ON rp.roleid = erg.roleid
				JOIN   roles r ON r.roleid = erg.roleid AND r.deleted_at IS NULL
				WHERE  erg.organizationid = o.organizationid
				  AND  rp.permission = $%d
				  AND  %s
			)
			OR EXISTS (
				SELECT 1 FROM exhibitions e
				JOIN   entity_role_grants erg ON erg.exhibitionid = e.exhibitionid
				JOIN   role_permissions rp ON rp.roleid = erg.roleid
				JOIN   roles r ON r.roleid = erg.roleid AND r.deleted_at IS NULL
				WHERE  e.organizationid = o.organizationid
				  AND  e.deleted_at IS NULL
				  AND  rp.permission = $%d
				  AND  %s
			)
		)`, n, entityMatchOwn(n+1), n, entityMatchOwn(n+1))
		args = append(args, permissions.PermAdmin, userID)
		n += 2
	}

	var total int
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM organizations o WHERE "+where, args...).Scan(&total); err != nil {
		slog.Error("ListOrganizations count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rowArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT o.organizationid::text, o.name
		FROM   organizations o
		WHERE  %s
		ORDER  BY o.name
		LIMIT  $%d OFFSET $%d
	`, where, n, n+1), rowArgs...)
	if err != nil {
		slog.Error("ListOrganizations", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	orgs := make([]scopeOrganization, 0)
	for rows.Next() {
		var o scopeOrganization
		if err := rows.Scan(&o.OrganizationID, &o.Name); err != nil {
			slog.Error("ListOrganizations", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		orgs = append(orgs, o)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ListOrganizations", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":         total,
		"offset":        offset,
		"limit":         limit,
		"organizations": orgs,
	})
}

// GET /api/v1/admin/org-exhibitions?organizationid=&search=&offset=&limit=
// Requires: authenticated. organizationid is required — this lists
// exhibitions within one organization, not across all of them (that's what
// ListOrganizations is for).
func (h *ScopeHandler) ListExhibitions(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID := middleware.MustUserID(ctx)
	organizationID := strings.TrimSpace(r.URL.Query().Get("organizationid"))
	if organizationID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "organizationid is required")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	isGlobalAdmin, err := h.Checker.Check(ctx, userID, "", "", "", "", permissions.PermAdmin)
	if err != nil {
		slog.Error("ListExhibitions(scope) global check", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	where := "e.organizationid = $1::uuid AND e.deleted_at IS NULL"
	args := []any{organizationID}
	n := 2
	if search != "" {
		where += fmt.Sprintf(" AND e.name ILIKE $%d", n)
		args = append(args, "%"+search+"%")
		n++
	}
	if !isGlobalAdmin {
		where += fmt.Sprintf(` AND (
			-- Organization-level Admin grant covers every exhibition in it.
			EXISTS (
				SELECT 1 FROM entity_role_grants erg
				JOIN   role_permissions rp ON rp.roleid = erg.roleid
				JOIN   roles r ON r.roleid = erg.roleid AND r.deleted_at IS NULL
				WHERE  erg.organizationid = $1::uuid
				  AND  rp.permission = $%d
				  AND  %s
			)
			-- Exhibition-level Admin grant covers just this one exhibition.
			OR EXISTS (
				SELECT 1 FROM entity_role_grants erg
				JOIN   role_permissions rp ON rp.roleid = erg.roleid
				JOIN   roles r ON r.roleid = erg.roleid AND r.deleted_at IS NULL
				WHERE  erg.exhibitionid = e.exhibitionid
				  AND  rp.permission = $%d
				  AND  %s
			)
		)`, n, entityMatchOwn(n+1), n, entityMatchOwn(n+1))
		args = append(args, permissions.PermAdmin, userID)
		n += 2
	}

	var total int
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM exhibitions e WHERE "+where, args...).Scan(&total); err != nil {
		slog.Error("ListExhibitions(scope) count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rowArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT e.exhibitionid::text, e.name,
		       COALESCE((
		           SELECT eh.hostname
		           FROM   exhibition_hostnames eh
		           WHERE  eh.exhibitionid = e.exhibitionid
		           ORDER  BY eh.created_at
		           LIMIT  1
		       ), '') AS hostname
		FROM   exhibitions e
		WHERE  %s
		ORDER  BY e.name
		LIMIT  $%d OFFSET $%d
	`, where, n, n+1), rowArgs...)
	if err != nil {
		slog.Error("ListExhibitions(scope)", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	exs := make([]scopeExhibition, 0)
	for rows.Next() {
		var e scopeExhibition
		if err := rows.Scan(&e.ExhibitionID, &e.Name, &e.Hostname); err != nil {
			slog.Error("ListExhibitions(scope)", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		exs = append(exs, e)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ListExhibitions(scope)", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":       total,
		"offset":      offset,
		"limit":       limit,
		"exhibitions": exs,
	})
}
