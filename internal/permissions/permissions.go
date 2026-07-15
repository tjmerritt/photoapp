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
// # Ownership model
//
// Photos and Galleries both belong directly to an Exhibition.
// Displays belong to a Gallery. Labels, Emojis, and Comments belong to a Photo.
// Displays reference Photos via slots but do not own them.
//
//	Exhibition
//	├── Gallery
//	│   └── Display          (slots reference Photos)
//	└── Photo
//	    ├── Label
//	    ├── Emoji reaction
//	    └── Comment
//
// # Resource scope types
//
//   - Global (nil scope) — applies to the whole exhibition
//   - Exhibition         — a specific exhibition
//   - Gallery            — a specific gallery (covers Displays within it)
//   - Display            — a specific display
//   - Photo              — a specific photo (covers its Labels, Emojis, Comments)
//
// # Permission hierarchy
//
// Grants nest along the ownership chain. A grant at a parent scope covers all
// children. The two independent chains are:
//
//	Gallery chain:  Global → Exhibition → Gallery → Display
//	Photo chain:    Global → Exhibition → Photo
//
// Photos are NOT under the Gallery chain. galleryID is only meaningful when
// checking Display permissions. For all Photo, Label, Emoji, and Comment
// permission checks, pass galleryID = "".
//
//	Call site                       exhibitionID  galleryID  resourceType  resourceRef
//	───────────────────────────────────────────────────────────────────────────────────
//	Check gallery G                 E             ""         Gallery       G
//	Check display D in gallery G    E             G          Display       D
//	Check photo P                   E             ""         Photo         P
//	Check labels/emojis/comments    E             ""         Photo         P
//	Check a global/admin permission E             ""         ""            ""
//
// # Permissions
//
// Gallery-level (checking a Gallery resource or a global grant):
//
//	GalleryView, GalleryCreate, GalleryModify, GalleryDelete
//
// Display-level (checking a Display resource, or a Gallery/Exhibition/global grant):
//
//	DisplayView, DisplayCreate, DisplayModify, DisplayDelete
//
// Photo-level:
//
//	PhotoCreate, PhotoDelete, PrivatePhotoView, PhotoDescriptionModify
//
// Photo label permissions:
//
//	PhotoLabelView, PhotoLabelCreate, PhotoLabelModify, PhotoLabelDelete
//
// Photo emoji permissions (no Modify — reactions are add or remove only):
//
//	PhotoEmojiView, PhotoEmojiCreate, PhotoEmojiDelete
//
// Photo comment permissions:
//
//	PhotoCommentView, PhotoCommentCreate, PhotoCommentModify, PhotoCommentDelete
//
// Emoji type permissions (site-wide):
//
//	EmojiUpload
//
// Administrative permissions:
//
//	Admin, LabelAdmin, EmojiAdmin, UserAdmin, GalleryAdmin
//
// Handlers check the fine-grained permission for the specific action being
// performed. Ownership checks (e.g. "can only modify your own label") are
// enforced in the handler after the permission check passes.
package permissions

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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

// Gallery permissions.
const (
	PermGalleryView   = "GalleryView"
	PermGalleryCreate = "GalleryCreate"
	PermGalleryModify = "GalleryModify"
	PermGalleryDelete = "GalleryDelete"
)

// Display permissions.
const (
	PermDisplayView   = "DisplayView"
	PermDisplayCreate = "DisplayCreate"
	PermDisplayModify = "DisplayModify"
	PermDisplayDelete = "DisplayDelete"
)

// Photo permissions.
const (
	PermPhotoCreate             = "PhotoCreate"             // upload a new photo
	PermPhotoDelete             = "PhotoDelete"             // delete a photo
	PermPrivatePhotoView        = "PrivatePhotoView"        // view photos where is_public = false
	PermPhotoDescriptionModify  = "PhotoDescriptionModify"  // edit a photo's title or description when not the owner
)

// Photo label permissions.
const (
	PermPhotoLabelView   = "PhotoLabelView"
	PermPhotoLabelCreate = "PhotoLabelCreate"
	PermPhotoLabelModify = "PhotoLabelModify"
	PermPhotoLabelDelete = "PhotoLabelDelete"
)

// Photo emoji permissions.
// There is no PhotoEmojiModify — reactions are added or removed, not edited.
const (
	PermPhotoEmojiView   = "PhotoEmojiView"
	PermPhotoEmojiCreate = "PhotoEmojiCreate"
	PermPhotoEmojiDelete = "PhotoEmojiDelete"
)

// Photo comment permissions.
const (
	PermPhotoCommentView   = "PhotoCommentView"
	PermPhotoCommentCreate = "PhotoCommentCreate"
	PermPhotoCommentModify = "PhotoCommentModify"
	PermPhotoCommentDelete = "PhotoCommentDelete"
)

// Emoji type permissions (site-wide, not scoped to a photo).
const (
	PermEmojiUpload = "EmojiUpload" // upload a new custom emoji type
)

// Administrative permissions.
const (
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

// Check reports whether the user identified by userID holds permission on the
// given resource.
//
// Parameters:
//
//	userID       — the authenticated user's ID; "" for unauthenticated (Public)
//	exhibitionID — the current exhibition; matched against Exhibition-scoped grants
//	galleryID    — the gallery a Display belongs to; pass "" for everything else.
//	               Gallery-scoped grants satisfy Display permission checks when
//	               galleryID matches. Photos, Labels, Emojis, and Comments do not
//	               go through the Gallery tier — always pass "" for those.
//	resourceType — the type being acted on (ResourceGallery, ResourceDisplay,
//	               ResourcePhoto); "" to check a global/admin permission only
//	resourceRef  — the UUID of the specific resource; "" when resourceType is ""
//	permission   — the permission string (e.g. PermDisplayModify, PermPhotoLabelCreate)
//
// Matching follows the ownership chain:
//
//	Display: Global → Exhibition → Gallery → Display
//	Photo:   Global → Exhibition → Photo
//
// A grant at any ancestor tier satisfies the check.
func (c *Checker) Check(
	ctx context.Context,
	userID, exhibitionID, galleryID,
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
			           -- Global grant: applies everywhere
			           erg.resource_type IS NULL
			           -- Exhibition-level grant: covers all galleries/displays/photos within
			        OR ($3 <> '' AND erg.resource_type = 'Exhibition'
			                     AND erg.resource_ref = $3)
			           -- Gallery-level grant: covers all displays within this gallery
			        OR ($4 <> '' AND erg.resource_type = 'Gallery'
			                     AND erg.resource_ref = $4)
			           -- Exact resource match
			        OR ($5 <> '' AND $6 <> ''
			                     AND erg.resource_type = $5
			                     AND erg.resource_ref  = $6)
			       )
		)
	`, permission, userID, exhibitionID, galleryID, resourceType, resourceRef).Scan(&exists)
	return exists, err
}

// MustCheck is like Check but panics on database error. Use only in contexts
// where the error is unrecoverable (e.g. startup validation).
func (c *Checker) MustCheck(
	ctx context.Context,
	userID, exhibitionID, galleryID,
	resourceType, resourceRef,
	permission string,
) bool {
	ok, err := c.Check(ctx, userID, exhibitionID, galleryID, resourceType, resourceRef, permission)
	if err != nil {
		panic(fmt.Sprintf("permissions.MustCheck: %v", err))
	}
	return ok
}

// UserPermissions returns all effective permission grants for userID within
// the current exhibition. Returns global grants, exhibition-scoped grants, and
// all resource-scoped grants (Gallery, Display, Photo) so the frontend can
// reason about per-resource access without additional round-trips.
//
// The result is deduplicated: the same (permission, resource_type, resource_ref)
// triple appears only once even if it is reachable via multiple roles or entity
// memberships.
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
		           -- Global grants
		           erg.resource_type IS NULL
		           -- Exhibition-level grants
		        OR ($2 <> '' AND erg.resource_type = 'Exhibition'
		                     AND erg.resource_ref = $2)
		           -- Gallery, Display, and Photo grants within this exhibition.
		           -- We include all resource-scoped grants whose role belongs to
		           -- this exhibition so the frontend has the complete picture.
		        OR ($2 <> '' AND erg.resource_type IN ('Gallery', 'Display', 'Photo')
		                     AND r.exhibitionid = $2::uuid)
		       )
		ORDER  BY rp.permission, COALESCE(erg.resource_type, ''), COALESCE(erg.resource_ref, '')
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

// ── Per-user singleton grants (Phase 6b) ─────────────────────────────────────
//
// A handful of admin toggles (currently just "access to private photos") only
// ever need to ADD a permission to one specific user on top of their normal
// role-derived grants — never revoke one that a broader role already confers.
// That fits the existing additive entity_role_grants model perfectly, so
// rather than inventing a new table we auto-create one small, well-known role
// per permission ("__grant:<permission>") the first time it's needed, and
// grant/revoke it directly to/from the target user (entity_type='User',
// global scope). This is invisible anywhere else in the system: it's just an
// ordinary role that happens to bundle exactly one permission and get granted
// to exactly one user at a time.
//
// This mechanism is NOT used for permissions that a broad role (e.g. the
// seeded "Contributor" role, granted to the LoggedIn entity) already confers
// to everyone — revoking a single user's access to something everyone else
// has requires an actual override column checked in the handler, since
// Check() only ever ORs grants together. See migrations/017_admin_phase6.sql
// for the columns used for those cases.

func singletonGrantRoleName(permission string) string {
	return "__grant:" + permission
}

// ensureSingletonRole returns the roleid of the auto-managed singleton role
// for permission within exhibitionID, creating it (and its one
// role_permissions row) if it doesn't already exist.
func (c *Checker) ensureSingletonRole(ctx context.Context, exhibitionID, permission string) (string, error) {
	name := singletonGrantRoleName(permission)

	var roleID string
	err := c.DB.QueryRow(ctx, `
		INSERT INTO roles (exhibitionid, name, description)
		VALUES ($1::uuid, $2, 'Auto-managed: per-user grant of ' || $3)
		ON CONFLICT (exhibitionid, name) DO NOTHING
		RETURNING roleid::text
	`, exhibitionID, name, permission).Scan(&roleID)
	if err == pgx.ErrNoRows {
		// Row already existed (ON CONFLICT DO NOTHING returns nothing) — fetch it.
		err = c.DB.QueryRow(ctx, `
			SELECT roleid::text FROM roles WHERE exhibitionid = $1::uuid AND name = $2
		`, exhibitionID, name).Scan(&roleID)
	}
	if err != nil {
		return "", err
	}

	if _, err := c.DB.Exec(ctx, `
		INSERT INTO role_permissions (roleid, permission)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, roleID, permission); err != nil {
		return "", err
	}

	return roleID, nil
}

// GrantUserPermission idempotently grants permission to userID, scoped
// globally within exhibitionID, via the singleton-role mechanism above.
func (c *Checker) GrantUserPermission(ctx context.Context, exhibitionID, userID, permission string) error {
	roleID, err := c.ensureSingletonRole(ctx, exhibitionID, permission)
	if err != nil {
		return err
	}
	_, err = c.DB.Exec(ctx, `
		INSERT INTO entity_role_grants (roleid, entity_type, entity_ref)
		SELECT $1, 'User', $2
		WHERE NOT EXISTS (
			SELECT 1 FROM entity_role_grants
			WHERE roleid = $1 AND entity_type = 'User' AND entity_ref = $2
			  AND resource_type IS NULL
		)
	`, roleID, userID)
	return err
}

// RevokeUserPermission removes a previously-granted singleton-role grant of
// permission from userID within exhibitionID. A no-op if none exists.
func (c *Checker) RevokeUserPermission(ctx context.Context, exhibitionID, userID, permission string) error {
	name := singletonGrantRoleName(permission)
	_, err := c.DB.Exec(ctx, `
		DELETE FROM entity_role_grants erg
		USING roles r
		WHERE erg.roleid = r.roleid
		  AND r.exhibitionid = $1::uuid AND r.name = $2
		  AND erg.entity_type = 'User' AND erg.entity_ref = $3
		  AND erg.resource_type IS NULL
	`, exhibitionID, name, userID)
	return err
}

// HasDirectUserGrant reports whether userID has been individually granted
// permission via the singleton-role mechanism (as opposed to holding it
// through a broader role like Admin). Used by the admin UI to render the
// current toggle state.
func (c *Checker) HasDirectUserGrant(ctx context.Context, exhibitionID, userID, permission string) (bool, error) {
	var exists bool
	err := c.DB.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM   entity_role_grants erg
			JOIN   roles              r ON r.roleid = erg.roleid
			WHERE  r.exhibitionid = $1::uuid AND r.name = $2
			  AND  erg.entity_type = 'User' AND erg.entity_ref = $3
			  AND  erg.resource_type IS NULL
		)
	`, exhibitionID, singletonGrantRoleName(permission), userID).Scan(&exists)
	return exists, err
}

// HasAny reports whether the user holds at least one of the given permissions
// at the global or exhibition level. Useful for showing/hiding UI sections
// without knowing a specific resource ID.
func (c *Checker) HasAny(
	ctx context.Context,
	userID, exhibitionID string,
	perms ...string,
) (bool, error) {
	for _, p := range perms {
		ok, err := c.Check(ctx, userID, exhibitionID, "", "", "", p)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
