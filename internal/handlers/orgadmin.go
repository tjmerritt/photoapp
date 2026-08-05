package handlers

import (
	"context"

	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

// isOrgAdmin reports whether userID holds at least one of perms scoped to
// organizationID — PLAN2.md Phase 2e's "per-org admin tooling" gate, the
// organization-level analog of Checker.HasAny(ctx, userID, exhibitionID,
// perms...).
//
// Three tiers reach an organization, mirroring Checker.Check's own tiering
// (internal/permissions/permissions.go) exactly:
//  1. A true global grant (no exhibitionid, resource_type, OR organizationid
//     at all) reaches every organization, the same way it reaches every
//     exhibition — checked first via Checker.HasAny with an empty
//     exhibitionID, which is precisely how Checker.Check's own "Global
//     grant" branch is triggered (see its doc comment).
//  2. A grant scoped directly to this organization
//     (entity_role_grants.organizationid = organizationID).
//  3. A grant scoped to any one exhibition that belongs to this
//     organization — deliberately included: an exhibition admin already
//     administers everything under their own exhibition, and Phase 1c's org
//     tier only ever ADDS reach beyond a single exhibition, so this must be
//     at least as permissive as "admin of any exhibition in this org," not
//     a separate, narrower check. This does NOT mean the reverse holds —
//     an org-level grant does not make someone an admin of unrelated
//     organizations, and isOrgAdmin never conflates the two.
//
// The entity-match clause (User/Team/Public/LoggedIn) mirrors Checker.Check's
// own exactly, unlike ScopeHandler's narrower entityMatchOwn helper (which
// deliberately omits Public/LoggedIn since nothing in this codebase creates
// such a grant at org scope today) — isOrgAdmin is an actual access-control
// gate, not a picker-population convenience, so it can't afford to
// under-recognize a grant Checker.Check itself would honor.
func isOrgAdmin(ctx context.Context, pool *db.Pool, checker *permissions.Checker, userID, organizationID string, perms ...string) (bool, error) {
	if organizationID == "" || userID == "" || len(perms) == 0 {
		return false, nil
	}

	if ok, err := checker.HasAny(ctx, userID, "", perms...); err != nil || ok {
		return ok, err
	}

	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM   entity_role_grants erg
			JOIN   role_permissions   rp ON rp.roleid = erg.roleid
			JOIN   roles              r  ON r.roleid  = erg.roleid AND r.deleted_at IS NULL
			WHERE  rp.permission = ANY($1)
			  AND  (
			           erg.organizationid = $2::uuid
			        OR (erg.exhibitionid IN (
			                SELECT exhibitionid FROM exhibitions
			                WHERE  organizationid = $2::uuid AND deleted_at IS NULL
			            ) AND erg.resource_type IS NULL)
			       )
			  AND  (
			           erg.entity_type = 'Public'
			        OR erg.entity_type = 'LoggedIn'
			        OR (erg.entity_type = 'User' AND erg.entity_ref = $3)
			        OR (erg.entity_type = 'Team' AND erg.entity_ref IN (
			                SELECT teamid::text FROM team_members WHERE userid = $3::uuid
			            ))
			       )
		)
	`, perms, organizationID, userID).Scan(&exists)
	return exists, err
}
