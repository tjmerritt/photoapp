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
// A grant row is scoped by at most one of three independent mechanisms —
// never more than one set on the same row (entity_role_grants'
// chk_grant_scope_exclusive constraint enforces this):
//
//   - organizationid (a column on entity_role_grants) — set means the grant
//     applies to every exhibition belonging to that organization, present
//     and future. This is PLAN2.md Phase 1c's "org admin" tier — see
//     migrations/021_org_admin.sql. Roles can themselves belong to an
//     organization instead of an exhibition (roles.organizationid); such a
//     role only makes sense granted at this scope.
//   - exhibitionid (a column on entity_role_grants) — NULL (and
//     organizationid also NULL) means the grant applies to every exhibition
//     ("global"); set means the grant applies to that one exhibition only
//     (covers all its Galleries/Displays/Photos). This replaced an earlier
//     resource_type = 'Exhibition' convention (see
//     migrations/018_grant_exhibitionid.sql).
//   - resource_type/resource_ref — scopes the grant to one specific Gallery,
//     Display, or Photo. Rows with a resource_type always have
//     exhibitionid = NULL and organizationid = NULL.
//
// # Permission hierarchy
//
// Grants nest along the ownership chain. A grant at a parent scope covers all
// children. The two independent chains, with PLAN2.md Phase 1c's
// organization tier now sitting between Global and Exhibition:
//
//	Gallery chain:  Global → Organization → Exhibition → Gallery → Display
//	Photo chain:    Global → Organization → Exhibition → Photo
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
//
// There is no ResourceExhibition: exhibition-level scope is expressed via the
// exhibitionid column on entity_role_grants instead (see
// migrations/018_grant_exhibitionid.sql).
const (
	ResourceGallery = "Gallery"
	ResourceDisplay = "Display"
	ResourcePhoto   = "Photo"
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
	PermPhotoCreate            = "PhotoCreate"            // upload a new photo
	PermPhotoDelete            = "PhotoDelete"            // delete a photo
	PermPrivatePhotoView       = "PrivatePhotoView"        // view photos where is_public = false
	PermPhotoDescriptionModify = "PhotoDescriptionModify" // edit a photo's title or description when not the owner
	// PermPhotoView is PLAN2.md Phase 2a's general "can view this photo"
	// permission. Defined but NOT YET ENFORCED anywhere: today's actual
	// photo-visibility gate is still the is_public column (see photo.go),
	// and Phase 2b removes is_public in favor of a "Public" label while
	// Phase 2d builds the direct + indirect (via DisplayView/GalleryView)
	// grant resolution PhotoView needs before it can safely replace that
	// check — flipping photo.go over to require PhotoView now, ahead of
	// either, would make every photo invisible until every install
	// re-granted it. It's defined now so it's valid to add to roles ahead
	// of that cutover. See SUMMARIES2.md Session 25.
	PermPhotoView = "PhotoView"
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
	// PermPhotoEmojiReact is PLAN2.md Phase 2a's single combined
	// add-or-remove-your-own-reaction permission — an alternate way to
	// grant what PermPhotoEmojiCreate + PermPhotoEmojiDelete together
	// already cover. Introduced as an additional accepted permission
	// rather than a replacement so existing role grants using the
	// Create/Delete pair keep working unchanged; React/Unreact accept
	// either. See SUMMARIES2.md Session 25.
	PermPhotoEmojiReact = "PhotoEmojiReact"
)

// Photo comment permissions.
const (
	PermPhotoCommentView   = "PhotoCommentView"
	PermPhotoCommentCreate = "PhotoCommentCreate"
	PermPhotoCommentModify = "PhotoCommentModify"
	PermPhotoCommentDelete = "PhotoCommentDelete"
	// PermCommentEmojiReact is PLAN2.md Phase 2a's permission for reacting
	// to a comment with an emoji, mirroring PermPhotoEmojiReact. Defined
	// but NOT YET ENFORCED anywhere — there is no comment-reaction feature
	// to gate yet (no emoji_reactions-equivalent table/endpoints for
	// comments exist). Building that feature is out of scope for "define
	// the permission types"; this constant makes the permission valid and
	// grantable ahead of that future feature. See SUMMARIES2.md Session 25.
	PermCommentEmojiReact = "CommentEmojiReact"
)

// Emoji type catalog permissions (site-wide, not scoped to a photo) — govern
// uploading, renaming, and deleting an emoji_types row itself, distinct from
// PermPhotoEmojiCreate/Delete/React above, which govern reacting to a photo
// with an emoji that already exists in the catalog.
const (
	PermEmojiUpload = "EmojiUpload" // upload a new custom emoji type
	// PermEmojiCreate is an alternate name for PermEmojiUpload (PLAN2.md
	// Phase 2a's naming). Both are accepted by UploadType so existing role
	// grants using PermEmojiUpload keep working unchanged.
	PermEmojiCreate = "EmojiCreate"
	// PermEmojiModify governs renaming an emoji type's alt text and
	// toggling its active state (AdminUpdateType).
	PermEmojiModify = "EmojiModify"
	// PermEmojiDelete is defined but NOT YET ENFORCED anywhere — there is
	// no emoji-type delete endpoint today (AdminUpdateType only toggles
	// is_active; there's no hard-delete). See SUMMARIES2.md Session 25.
	PermEmojiDelete = "EmojiDelete"
)

// Label name permissions — govern the shared label_names catalog (which
// names exist, their restricted/enabled flags, and display color), distinct
// from PermPhotoLabelCreate/Modify/Delete/View above, which govern
// individual label VALUES attached to one specific photo.
const (
	PermLabelNameView = "LabelNameView"
	// PermLabelNameCreate and PermLabelNameModify both apply to the same
	// upsert endpoint (UpdateName) — either is accepted rather than
	// distinguishing "does a label_names row already exist for this name"
	// with an extra query, since the endpoint's behavior (and the access
	// it should require) doesn't meaningfully differ between the two.
	PermLabelNameCreate = "LabelNameCreate"
	PermLabelNameModify = "LabelNameModify"
	// PermLabelNameDelete is defined but NOT YET ENFORCED anywhere — there
	// is no endpoint that deletes a label_names row today. See
	// SUMMARIES2.md Session 25.
	PermLabelNameDelete = "LabelNameDelete"
)

// Team permissions — a granular alternative to the blanket PermTeamAdmin,
// which every team-admin endpoint continues to accept alongside these.
const (
	PermTeamView   = "TeamView"
	PermTeamCreate = "TeamCreate"
	PermTeamModify = "TeamModify" // also covers adding/removing team members
	PermTeamDelete = "TeamDelete"
)

// Role permissions — a granular alternative to the blanket
// PermPermissionsAdmin, which every role-admin endpoint continues to accept
// alongside these.
const (
	PermRoleView   = "RoleView"
	PermRoleCreate = "RoleCreate"
	PermRoleModify = "RoleModify" // also covers adding/removing a role's permissions
	PermRoleDelete = "RoleDelete"
)

// Administrative permissions.
const (
	PermAdmin        = "Admin"
	PermLabelAdmin   = "LabelAdmin"
	PermEmojiAdmin   = "EmojiAdmin"
	PermUserAdmin    = "UserAdmin"
	PermGalleryAdmin = "GalleryAdmin"
	// PermTeamAdmin governs the teams admin page (Phase 6f): creating/editing/
	// deleting teams and managing their membership.
	PermTeamAdmin = "TeamAdmin"
	// PermPermissionsAdmin governs the permission-grants viewer (Phase 6g,
	// 6h) and the roles admin page (Phase 6i): browsing/revoking/creating
	// entity_role_grants rows (both global — exhibitionid IS NULL, see
	// Check()'s doc comment — and exhibition-scoped), and creating/editing/
	// deleting roles and the permissions they bundle.
	PermPermissionsAdmin = "PermissionsAdmin"
)

// ── Permission catalog ────────────────────────────────────────────────────────

// PermissionGroup is one category of related permissions, used to render the
// roles admin page's permission checkbox grid (Phase 6i) in a readable
// layout instead of one flat list.
type PermissionGroup struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// PermissionCatalog returns every known permission string, grouped for
// display. This is the single source of truth for "what permissions exist"
// — IsValidPermission and the roles admin page's checkbox grid both derive
// from it, so a newly added permission constant only needs to be listed
// here once.
func PermissionCatalog() []PermissionGroup {
	return []PermissionGroup{
		{Name: "Gallery", Permissions: []string{PermGalleryView, PermGalleryCreate, PermGalleryModify, PermGalleryDelete}},
		{Name: "Display", Permissions: []string{PermDisplayView, PermDisplayCreate, PermDisplayModify, PermDisplayDelete}},
		{Name: "Photo", Permissions: []string{PermPhotoCreate, PermPhotoDelete, PermPrivatePhotoView, PermPhotoDescriptionModify, PermPhotoView}},
		{Name: "Photo labels", Permissions: []string{PermPhotoLabelView, PermPhotoLabelCreate, PermPhotoLabelModify, PermPhotoLabelDelete}},
		{Name: "Photo emoji", Permissions: []string{PermPhotoEmojiView, PermPhotoEmojiCreate, PermPhotoEmojiDelete, PermPhotoEmojiReact}},
		{Name: "Photo comments", Permissions: []string{PermPhotoCommentView, PermPhotoCommentCreate, PermPhotoCommentModify, PermPhotoCommentDelete, PermCommentEmojiReact}},
		{Name: "Emoji types", Permissions: []string{PermEmojiUpload, PermEmojiCreate, PermEmojiModify, PermEmojiDelete}},
		{Name: "Label names", Permissions: []string{PermLabelNameView, PermLabelNameCreate, PermLabelNameModify, PermLabelNameDelete}},
		{Name: "Teams", Permissions: []string{PermTeamView, PermTeamCreate, PermTeamModify, PermTeamDelete}},
		{Name: "Roles", Permissions: []string{PermRoleView, PermRoleCreate, PermRoleModify, PermRoleDelete}},
		{Name: "Administrative", Permissions: []string{PermAdmin, PermLabelAdmin, PermEmojiAdmin, PermUserAdmin, PermGalleryAdmin, PermTeamAdmin, PermPermissionsAdmin}},
	}
}

// IsValidPermission reports whether s is one of the known permission
// constants listed in PermissionCatalog. Used to validate a permission name
// before adding it to a role (roles admin page, Phase 6i) — rejects typos
// or garbage instead of silently bundling an unrecognized string that would
// never match anything in Check().
func IsValidPermission(s string) bool {
	for _, g := range PermissionCatalog() {
		for _, p := range g.Permissions {
			if p == s {
				return true
			}
		}
	}
	return false
}

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
//
// PLAN2.md Phase 1c adds a fourth tier above Exhibition: an
// organization-scoped grant (entity_role_grants.organizationid set) covers
// every exhibition belonging to that organization, present and future — the
// same way an exhibitionid grant covers every gallery/display/photo within
// that one exhibition. The LEFT JOIN below resolves exhibitionID's owning
// organization once per call so the org-level OR branch can compare against
// it.
//
// exhibitionID is passed to Postgres via nullableUUID, not as a plain string,
// and NOT behind a "$3 <> '' AND ... = $3::uuid"-style SQL guard or a
// NULLIF($3, '')::uuid trick — both of those were tried (see SUMMARIES2.md
// Sessions 23-24) and both still threw "invalid input syntax for type uuid"
// for an empty exhibitionID. The reason: every occurrence of $3 in this query
// sits inside a uuid-cast expression, so Postgres infers $3's parameter type
// as uuid itself; the empty string then has to be parsed as a uuid the moment
// pgx binds it, which happens before the query — and any SQL-level guard
// inside it — ever executes. Passing an actual Go nil (via nullableUUID)
// makes pgx send a true SQL NULL on the wire for that parameter, which never
// goes through the uuid input parser at all, sidestepping the problem
// entirely regardless of query plan or parameter type inference. The same
// applies to every other "$N::uuid" cast of a value that may be "" in this
// file and in emojis.go.
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
			LEFT   JOIN exhibitions   ex  ON ex.exhibitionid = $3::uuid
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
			           -- Global grant: applies everywhere (no exhibitionid, no resource, no org)
			           (erg.exhibitionid IS NULL AND erg.resource_type IS NULL AND erg.organizationid IS NULL)
			           -- Organization-level grant: covers every exhibition under that org
			        OR (erg.organizationid IS NOT NULL AND erg.organizationid = ex.organizationid)
			           -- Exhibition-level grant: covers all galleries/displays/photos within
			        OR (erg.exhibitionid = $3::uuid
			                     AND erg.resource_type IS NULL)
			           -- Gallery-level grant: covers all displays within this gallery
			        OR ($4 <> '' AND erg.resource_type = 'Gallery'
			                     AND erg.resource_ref = $4)
			           -- Exact resource match
			        OR ($5 <> '' AND $6 <> ''
			                     AND erg.resource_type = $5
			                     AND erg.resource_ref  = $6)
			       )
		)
	`, permission, userID, nullableUUID(exhibitionID), galleryID, resourceType, resourceRef).Scan(&exists)
	return exists, err
}

// nullableUUID converts an empty string to a real Go nil so that pgx sends
// a SQL NULL on the wire for that query parameter, rather than the literal
// string "" — which fails "invalid input syntax for type uuid" the moment
// Postgres binds it to a uuid-typed parameter, before the query body (and any
// guard inside it) ever runs. Use this for any string that represents "no
// value" but is compared against a uuid column via an explicit ::uuid cast.
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
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
		LEFT   JOIN exhibitions   ex ON ex.exhibitionid = $2::uuid
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
		           -- Global grants (no exhibitionid, no resource, no org)
		           (erg.exhibitionid IS NULL AND erg.resource_type IS NULL AND erg.organizationid IS NULL)
		           -- Organization-level grants: this exhibition's organization
		        OR (erg.organizationid IS NOT NULL AND erg.organizationid = ex.organizationid)
		           -- Exhibition-level grants
		        OR (erg.exhibitionid = $2::uuid
		                     AND erg.resource_type IS NULL)
		           -- Gallery, Display, and Photo grants within this exhibition.
		           -- We include all resource-scoped grants whose role belongs to
		           -- this exhibition so the frontend has the complete picture.
		        OR (erg.resource_type IN ('Gallery', 'Display', 'Photo')
		                     AND r.exhibitionid = $2::uuid)
		       )
		ORDER  BY rp.permission, COALESCE(erg.resource_type, ''), COALESCE(erg.resource_ref, '')
	`, userID, nullableUUID(exhibitionID))
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

// GrantUserPermission idempotently grants permission to userID, scoped to
// exhibitionID only, via the singleton-role mechanism above.
//
// The grant row's exhibitionid is set to exhibitionID (not left NULL/global)
// so this never leaks into other exhibitions — see
// migrations/018_grant_exhibitionid.sql, which fixed exactly this leak for
// any grants already made before the exhibitionid column existed.
func (c *Checker) GrantUserPermission(ctx context.Context, exhibitionID, userID, permission string) error {
	roleID, err := c.ensureSingletonRole(ctx, exhibitionID, permission)
	if err != nil {
		return err
	}
	_, err = c.DB.Exec(ctx, `
		INSERT INTO entity_role_grants (roleid, entity_type, entity_ref, exhibitionid)
		SELECT $1, 'User', $2, $3::uuid
		WHERE NOT EXISTS (
			SELECT 1 FROM entity_role_grants
			WHERE roleid = $1 AND entity_type = 'User' AND entity_ref = $2
			  AND exhibitionid = $3::uuid AND resource_type IS NULL
		)
	`, roleID, userID, exhibitionID)
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
		  AND erg.exhibitionid = $1::uuid AND erg.resource_type IS NULL
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
			  AND  erg.exhibitionid = $1::uuid AND erg.resource_type IS NULL
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
