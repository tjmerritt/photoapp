// Package permissions provides the permission-checking engine for photoapp.
//
// # Model overview
//
// An Entity is who is asking:
//   - Public     — any request, authenticated or not
//   - LoggedIn   — any authenticated user
//   - Team       — members of a named team (entity_ref = teamid)
//   - User       — a specific user (entity_ref = userid)
//
// A Resource is what is being acted on:
//   - Global (nil scope) — applies to the whole exhibition
//   - Exhibition         — a specific exhibition
//   - Gallery            — a specific gallery
//   - Display            — a specific display
//   - Photo              — a specific photo
//
// A Permission is what the entity wants to do:
//   - View, Create, Modify, Delete
//   - Admin, LabelAdmin, EmojiAdmin, UserAdmin, GalleryAdmin
//
// Roles bundle a set of permissions and are granted to entities, optionally
// scoped to a resource. Checking is hierarchical: a grant on Exhibition X
// covers any Gallery/Display/Photo within it; a global (nil) grant covers
// everything in the exhibition.
package permissions

import (
	"context"
	"fmt"

	"github.com/tjmerritt/photoapp/internal/db"
)

// ── Constants ─────────────────────────────────────────────────────────────────

// Entity types — who is asking.
const (
	EntityPublic   = "Public"
	EntityLoggedIn = "LoggedIn"
	EntityTeam     = "Team"
	EntityUser     = "User"
)

// Resource types — what is being acted on.
const (
	ResourceExhibition = "Exhibition"
	ResourceGallery    = "Gallery"
	ResourceDisplay    = "Display"
	ResourcePhoto      = "Photo"
)

// Permission values — what action is requested.
const (
	PermView         = "View"
	PermCreate       = "Create"
	PermModify       = "Modify"
	PermDelete       = "Delete"
	PermAdmin        = "Admin"
	PermLabelAdmin   = "LabelAdmin"
	PermEmojiAdmin   = "EmojiAdmin"
	PermUserAdmin    = "UserAdmin"
	PermGalleryAdmin = "GalleryAdmin"
)

// ── Types ─────────────────────────────────────────────────────────────────────

// Grant is one effective permission entry returned by UserPermissions.
// ResourceType and ResourceRef are empty strings when the grant is global.
type Grant struct {
	Permission   string `json:"permission"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceRef  string `json:"resource_ref,omitempty"`
}

// ── Checker ───────────────────────────────────────────────────────────────────

// Checker evaluates permissions by querying the database.
// It is safe for concurrent use.
type Checker struct {
	DB *db.Pool
}

// Check reports whether the user identified by userID holds permission on
// (resourceType, resourceRef) within exhibitionID.
//
//   - Pass userID = "" for unauthenticated (Public) requests.
//   - Pass resourceType = "" / resourceRef = "" to check a global permission
//     (not scoped to any specific resource).
//   - exhibitionID is used for exhibition-level grant matching; pass "" to
//     skip that tier.
func (c *Checker) Check(
	ctx context.Context,
	userID, exhibitionID,
	resourceType, resourceRef,
	permission string,
) (bool, error) {
	var exists bool
	err := c.DB.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM   entity_role_grants erg
			JOIN   role_permissions   rp  ON rp.roleid = erg.roleid
			JOIN   roles              r   ON r.roleid  = erg.roleid
			WHERE  rp.permission = $1
			  AND  r.deleted_at  IS NULL
			  AND  (
			           erg.entity_type = 'Public'
			        OR ($2 <> '' AND erg.entity_type = 'LoggedIn')
			        OR ($2 <> '' AND erg.entity_type = 'User'
			                     AND erg.entity_ref = $2)
			        OR ($2 <> '' AND erg.entity_type = 'Team'
			                     AND erg.entity_ref IN (
			                             SELECT teamid::text
			                             FROM   team_members
			                             WHERE  userid = NULLIF($2, '')::uuid
			                         ))
			       )
			  AND  (
			           erg.resource_type IS NULL
			        OR ($3 <> '' AND erg.resource_type = 'Exhibition'
			                     AND erg.resource_ref = $3)
			        OR ($4 <> '' AND $5 <> ''
			                     AND erg.resource_type = $4
			                     AND erg.resource_ref  = $5)
			       )
		)
	`, permission, userID, exhibitionID, resourceType, resourceRef).Scan(&exists)
	return exists, err
}

// MustCheck is like Check but panics on database error. Use only in contexts
// where the error is unrecoverable (e.g. startup validation).
func (c *Checker) MustCheck(
	ctx context.Context,
	userID, exhibitionID,
	resourceType, resourceRef,
	permission string,
) bool {
	ok, err := c.Check(ctx, userID, exhibitionID, resourceType, resourceRef, permission)
	if err != nil {
		panic(fmt.Sprintf("permissions.MustCheck: %v", err))
	}
	return ok
}

// UserPermissions returns all effective permission grants for userID within
// exhibitionID. The result set is deduplicated: if the same permission is
// granted via multiple paths (role, entity, or resource scope), it appears
// only once per unique (permission, resource_type, resource_ref) triple.
//
// Pass userID = "" to get permissions for an unauthenticated visitor.
func (c *Checker) UserPermissions(
	ctx context.Context,
	userID, exhibitionID string,
) ([]Grant, error) {
	rows, err := c.DB.Query(ctx, `
		SELECT DISTINCT
		       rp.permission,
		       COALESCE(erg.resource_type, ''),
		       COALESCE(erg.resource_ref,  '')
		FROM   entity_role_grants erg
		JOIN   role_permissions   rp ON rp.roleid = erg.roleid
		JOIN   roles              r  ON r.roleid  = erg.roleid
		WHERE  r.deleted_at IS NULL
		  AND  (
		           erg.entity_type = 'Public'
		        OR ($1 <> '' AND erg.entity_type = 'LoggedIn')
		        OR ($1 <> '' AND erg.entity_type = 'User'
		                     AND erg.entity_ref = $1)
		        OR ($1 <> '' AND erg.entity_type = 'Team'
		                     AND erg.entity_ref IN (
		                             SELECT teamid::text
		                             FROM   team_members
		                             WHERE  userid = NULLIF($1, '')::uuid
		                         ))
		       )
		  AND  (
		           erg.resource_type IS NULL
		        OR ($2 <> '' AND erg.resource_type = 'Exhibition'
		                     AND erg.resource_ref = $2)
		       )
		ORDER  BY rp.permission, erg.resource_type, erg.resource_ref
	`, userID, exhibitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Permission, &g.ResourceType, &g.ResourceRef); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// HasAny reports whether the user holds at least one of the given permissions
// at the global or exhibition level. Useful for showing/hiding UI sections.
func (c *Checker) HasAny(
	ctx context.Context,
	userID, exhibitionID string,
	permissions ...string,
) (bool, error) {
	for _, p := range permissions {
		ok, err := c.Check(ctx, userID, exhibitionID, "", "", p)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
