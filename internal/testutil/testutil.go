// Package testutil provides database test infrastructure for photoapp.
//
// Tests that need a real Postgres connection call RequireDB(t), which:
//
//  1. Skips the test (via t.Skip, not a failure) when TEST_DATABASE_URL is
//     unset — so `go test ./...` stays usable without a database configured
//     (e.g. a contributor's first run, or a CI stage that only lints).
//  2. Connects to it and returns a *db.Pool. That's it — no truncation, no
//     migration step, no locking.
//
// TEST_DATABASE_URL must point at an already-migrated database: run
// migrations against it once, the same way you would for any real
// deployment, before running tests —
//
//	make test-db-create
//	DATABASE_URL=postgres://photoapp:photoapp@localhost:5432/photoapp_test?sslmode=disable make migrate-up
//	TEST_DATABASE_URL=postgres://photoapp:photoapp@localhost:5432/photoapp_test?sslmode=disable go test ./...
//
// Tests run concurrently against this one shared database — every package,
// every test, no locking, no per-test/per-package schema, no truncation
// between tests. That's deliberate, not an oversight: this is a multi-tenant
// app (every resource lives under an exhibitionid), so requests from
// different tenants already have to coexist safely in one shared schema in
// production; tests should exercise that same property rather than being
// artificially given a database to themselves. Two earlier versions of this
// package tried to paper over that instead — first with Postgres advisory
// locks serializing access to one shared schema, then with a private schema
// per test binary — and both were reverted: a lock just took away the
// parallelism `go test ./...` is supposed to give you without actually
// making concurrent access safe, and a private schema meant tests were no
// longer exercising the same multi-tenant-on-one-schema model production
// actually runs under.
//
// What actually makes concurrent tests safe here is on the caller's side,
// not this package's:
//
//   - Every fixture builder below (CreateUser, CreateExhibition, ...)
//     generates a random, collision-free key for anything with a uniqueness
//     constraint (uuid.NewString() in usernames, emails, exhibition names,
//     image URLs, team names, ...). Two tests running at the same instant
//     never insert the same row.
//   - Write tests so their assertions are scoped to the fixtures they
//     created — filter by the exhibitionid, photoid, userid, etc. your test
//     just made, never assert an exact global count/list across a table
//     another concurrently-running test could also be writing to.
//     Handlers here already work this way (nearly everything is scoped by
//     exhibitionid), so this is usually automatic, not something you have to
//     work to maintain — but it's the property that keeps this safe, so bear
//     it in mind when adding a test against something that ISN'T naturally
//     tenant-scoped (e.g. a hypothetical "list every user" admin endpoint).
//
// One consequence: data from every test run accumulates in
// TEST_DATABASE_URL forever (nothing ever deletes it). Harmless — rows are
// inert and scoped away from each other — but if that bothers you,
// periodically recreate the database (`make test-db-drop test-db-create` +
// re-migrate).
//
// Fixture builders (CreateUser, CreateExhibition, CreateRole, Grant, ...)
// mirror the rows scripts/seed-exhibition.sh creates for a real deployment,
// scaled down to exactly what a given test needs.
package testutil

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/tjmerritt/photoapp/internal/db"
)

// ── Setup ─────────────────────────────────────────────────────────────────────

// RequireDB returns a connection pool to the database named by
// TEST_DATABASE_URL. See the package doc comment for the skip condition,
// the shared-database design, and what keeps concurrent tests from
// colliding in it.
func RequireDB(t *testing.T) *db.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test (see internal/testutil doc comment)")
	}

	ctx := context.Background()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("testutil: connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var hasUsers bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.users') IS NOT NULL`).Scan(&hasUsers); err != nil {
		t.Fatalf("testutil: check TEST_DATABASE_URL is migrated: %v", err)
	}
	if !hasUsers {
		t.Fatalf("testutil: TEST_DATABASE_URL is not migrated (no users table found) — " +
			"run migrations against it first, e.g. DATABASE_URL=$TEST_DATABASE_URL make migrate-up")
	}

	return pool
}

// ── Fixture builders ──────────────────────────────────────────────────────────
//
// Every builder fails the test (t.Fatalf) on error rather than returning one
// — fixture setup isn't the thing under test, so a fixture failure should
// point straight at the setup line, not require the caller to check errors
// that are never meant to happen.

// CreateUser inserts a user with a randomly generated username/email and
// returns its userid.
func CreateUser(t *testing.T, pool *db.Pool) string {
	t.Helper()
	suffix := uuid.NewString()
	var userID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (username, email)
		VALUES ($1, $2)
		RETURNING userid::text
	`, "test-"+suffix, "test-"+suffix+"@example.invalid").Scan(&userID)
	if err != nil {
		t.Fatalf("testutil.CreateUser: %v", err)
	}
	return userID
}

// CreateExhibition inserts an exhibition with a randomly generated name and
// returns its exhibitionid.
func CreateExhibition(t *testing.T, pool *db.Pool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO exhibitions (name) VALUES ($1)
		RETURNING exhibitionid::text
	`, "test-exhibition-"+uuid.NewString()).Scan(&id)
	if err != nil {
		t.Fatalf("testutil.CreateExhibition: %v", err)
	}
	return id
}

// CreateOrganization inserts an organization with a randomly generated name
// and returns its organizationid. Pass it to CreateExhibitionInOrg (or set
// exhibitions.organizationid directly) to associate exhibitions with it, and
// to CreateOrgRole to give it Phase 1c organization-scoped roles.
func CreateOrganization(t *testing.T, pool *db.Pool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO organizations (name) VALUES ($1)
		RETURNING organizationid::text
	`, "test-org-"+uuid.NewString()).Scan(&id)
	if err != nil {
		t.Fatalf("testutil.CreateOrganization: %v", err)
	}
	return id
}

// CreateExhibitionInOrg is CreateExhibition, but the new exhibition belongs
// to organizationID instead of getting the default Legacy Organization
// (migrations/019_organizations.sql).
func CreateExhibitionInOrg(t *testing.T, pool *db.Pool, organizationID string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO exhibitions (name, organizationid) VALUES ($1, $2::uuid)
		RETURNING exhibitionid::text
	`, "test-exhibition-"+uuid.NewString(), organizationID).Scan(&id)
	if err != nil {
		t.Fatalf("testutil.CreateExhibitionInOrg: %v", err)
	}
	return id
}

// CreateOrgRole is CreateRole, but scoped to organizationID (PLAN2.md Phase
// 1c) instead of an exhibition — see migrations/021_org_admin.sql. name
// must be unique within organizationID (matches uq_role_name_org).
func CreateOrgRole(t *testing.T, pool *db.Pool, organizationID, name string, perms ...string) string {
	t.Helper()
	ctx := context.Background()

	var roleID string
	err := pool.QueryRow(ctx, `
		INSERT INTO roles (organizationid, name) VALUES ($1::uuid, $2)
		RETURNING roleid::text
	`, organizationID, name).Scan(&roleID)
	if err != nil {
		t.Fatalf("testutil.CreateOrgRole: %v", err)
	}

	for _, p := range perms {
		if _, err := pool.Exec(ctx, `
			INSERT INTO role_permissions (roleid, permission) VALUES ($1, $2)
		`, roleID, p); err != nil {
			t.Fatalf("testutil.CreateOrgRole: add permission %q: %v", p, err)
		}
	}
	return roleID
}

// AddUserToExhibition inserts a user_exhibitions membership row, mirroring
// what a real join/registration flow leaves behind. Several admin endpoints
// (AdminHandler.ListExhibitions, ListUsers, Stats' user_count) only see
// users who have this row, not just any user in the users table.
func AddUserToExhibition(t *testing.T, pool *db.Pool, userID, exhibitionID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO user_exhibitions (userid, exhibitionid) VALUES ($1::uuid, $2::uuid)
	`, userID, exhibitionID)
	if err != nil {
		t.Fatalf("testutil.AddUserToExhibition: %v", err)
	}
}

// CreatePhoto inserts a photo owned by ownerUserID within exhibitionID and
// returns its photoid.
func CreatePhoto(t *testing.T, pool *db.Pool, exhibitionID, ownerUserID string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO photos (owner_userid, exhibitionid, image_url, image_width, image_height)
		VALUES ($1::uuid, $2::uuid, $3, 800, 600)
		RETURNING photoid::text
	`, ownerUserID, exhibitionID, "https://example.invalid/"+uuid.NewString()+".jpg").Scan(&id)
	if err != nil {
		t.Fatalf("testutil.CreatePhoto: %v", err)
	}
	return id
}

// MakePhotoPublic sets an explicit "Public" = "True" label on photoID,
// updating one in place if it already has a "Public" label (e.g. from a
// prior MakePhotoPublic call) rather than inserting a second row. Photos
// created by CreatePhoto default to private — no "Public" label at all,
// which internal/handlers/fetch.go's photoIsPublicSQL treats as private,
// matching production's default for both cmd/import-photos (without
// --cascade) and the browser upload endpoint. PLAN2.md Phase 2b removed the
// photos.is_public column this used to flip directly
// (migrations/022_drop_is_public.sql) — the "Public" label is now the sole
// source of truth for visibility.
func MakePhotoPublic(t *testing.T, pool *db.Pool, photoID string) {
	t.Helper()
	ctx := context.Background()

	ct, err := pool.Exec(ctx, `
		UPDATE labels
		SET    value = 'True', updated_at = NOW()
		WHERE  photoid = $1::uuid AND name = 'Public' AND deleted_at IS NULL
	`, photoID)
	if err != nil {
		t.Fatalf("testutil.MakePhotoPublic: update: %v", err)
	}
	if ct.RowsAffected() > 0 {
		return
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO labels (photoid, added_by_userid, name, value)
		SELECT photoid, owner_userid, 'Public', 'True'
		FROM   photos WHERE photoid = $1::uuid
	`, photoID); err != nil {
		t.Fatalf("testutil.MakePhotoPublic: insert: %v", err)
	}
}

// CreateGallery inserts a gallery within exhibitionID and returns its
// galleryid.
func CreateGallery(t *testing.T, pool *db.Pool, exhibitionID string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO galleries (exhibitionid, title) VALUES ($1::uuid, 'Test Gallery')
		RETURNING galleryid::text
	`, exhibitionID).Scan(&id)
	if err != nil {
		t.Fatalf("testutil.CreateGallery: %v", err)
	}
	return id
}

// CreateDisplay inserts a template-less display within galleryID and returns
// its displayid.
func CreateDisplay(t *testing.T, pool *db.Pool, galleryID string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO displays (galleryid) VALUES ($1::uuid)
		RETURNING displayid::text
	`, galleryID).Scan(&id)
	if err != nil {
		t.Fatalf("testutil.CreateDisplay: %v", err)
	}
	return id
}

// PlacePhotoInSlot inserts a display_slots row putting photoID into slot 0
// of displayID (PLAN2.md Phase 2d — indirect PhotoView resolution via
// DisplayView/GalleryView correlates against this table; see
// internal/handlers/fetch.go's photoAccessibleViaDisplaySQL). No handler
// exposes a plain "put this photo in this slot" endpoint in a single call
// (DisplaysHandler's SlotUpdate does, but pulling in the full handler/HTTP
// plumbing just to seed a fixture would be more indirection than the direct
// insert below), so this writes the row directly, mirroring the same
// direct-SQL-fixture pattern already used elsewhere in this package (e.g.
// CreateEmojiType's variant rows in labels/emojis tests).
func PlacePhotoInSlot(t *testing.T, pool *db.Pool, displayID, photoID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO display_slots (displayid, slot_index, photoid)
		VALUES ($1::uuid, 0, $2::uuid)
	`, displayID, photoID)
	if err != nil {
		t.Fatalf("testutil.PlacePhotoInSlot: %v", err)
	}
}

// CreateTeam inserts a team within exhibitionID and returns its teamid.
func CreateTeam(t *testing.T, pool *db.Pool, exhibitionID string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO teams (exhibitionid, name) VALUES ($1::uuid, $2)
		RETURNING teamid::text
	`, exhibitionID, "test-team-"+uuid.NewString()).Scan(&id)
	if err != nil {
		t.Fatalf("testutil.CreateTeam: %v", err)
	}
	return id
}

// AddTeamMember adds userID to teamID.
func AddTeamMember(t *testing.T, pool *db.Pool, teamID, userID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO team_members (teamid, userid) VALUES ($1::uuid, $2::uuid)
	`, teamID, userID)
	if err != nil {
		t.Fatalf("testutil.AddTeamMember: %v", err)
	}
}

// CreateEmojiType inserts an active, image-based emoji type (avoiding the
// unique index on emoji_char, so this is safe to call more than once per
// test) and returns its emojiid.
func CreateEmojiType(t *testing.T, pool *db.Pool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO emoji_types (image_url, alt_text, is_active)
		VALUES ($1, 'test emoji', TRUE)
		RETURNING emojiid::text
	`, "https://example.invalid/emoji/"+uuid.NewString()+".png").Scan(&id)
	if err != nil {
		t.Fatalf("testutil.CreateEmojiType: %v", err)
	}
	return id
}

// CreateRole inserts a role named name within exhibitionID, bundling perms
// (permissions.PermXxx constants), and returns its roleid. name must be
// unique within exhibitionID (matches the uq_role_name constraint) — pass a
// distinct name per role created within the same exhibition fixture.
func CreateRole(t *testing.T, pool *db.Pool, exhibitionID, name string, perms ...string) string {
	t.Helper()
	ctx := context.Background()

	var roleID string
	err := pool.QueryRow(ctx, `
		INSERT INTO roles (exhibitionid, name) VALUES ($1::uuid, $2)
		RETURNING roleid::text
	`, exhibitionID, name).Scan(&roleID)
	if err != nil {
		t.Fatalf("testutil.CreateRole: %v", err)
	}

	for _, p := range perms {
		if _, err := pool.Exec(ctx, `
			INSERT INTO role_permissions (roleid, permission) VALUES ($1, $2)
		`, roleID, p); err != nil {
			t.Fatalf("testutil.CreateRole: add permission %q: %v", p, err)
		}
	}
	return roleID
}

// GrantOptions describes one entity_role_grants row, mirroring the shape
// documented in migrations/012_permissions.sql, 018_grant_exhibitionid.sql,
// and 021_org_admin.sql. At most one of ExhibitionID, ResourceType/
// ResourceRef, or OrganizationID should be set (or none, for a true global
// grant) — the database enforces this via chk_grant_scope_exclusive.
type GrantOptions struct {
	// EntityType is one of permissions.EntityPublic, EntityLoggedIn,
	// EntityTeam, or EntityUser.
	EntityType string
	// EntityRef is the teamid or userid being granted to; "" for Public/LoggedIn.
	EntityRef string
	// ExhibitionID scopes the grant to one exhibition; "" for a grant not
	// scoped this way (global, resource-scoped, or organization-scoped).
	ExhibitionID string
	// ResourceType is one of permissions.ResourceGallery, ResourceDisplay,
	// or ResourcePhoto; "" for a grant not scoped to one specific resource.
	ResourceType string
	// ResourceRef is the resource's UUID; "" when ResourceType is "".
	ResourceRef string
	// OrganizationID scopes the grant to every exhibition under that
	// organization (PLAN2.md Phase 1c); "" for a grant not scoped this way.
	// roleID must reference an organization-scoped role (see CreateOrgRole)
	// when this is set.
	OrganizationID string
}

// Grant inserts one entity_role_grants row for roleID per opt.
func Grant(t *testing.T, pool *db.Pool, roleID string, opt GrantOptions) {
	t.Helper()

	var entityRef, exhibitionID, resourceType, resourceRef, organizationID any
	if opt.EntityRef != "" {
		entityRef = opt.EntityRef
	}
	if opt.ExhibitionID != "" {
		exhibitionID = opt.ExhibitionID
	}
	if opt.ResourceType != "" {
		resourceType = opt.ResourceType
	}
	if opt.ResourceRef != "" {
		resourceRef = opt.ResourceRef
	}
	if opt.OrganizationID != "" {
		organizationID = opt.OrganizationID
	}

	_, err := pool.Exec(context.Background(), `
		INSERT INTO entity_role_grants (roleid, entity_type, entity_ref, exhibitionid, resource_type, resource_ref, organizationid)
		VALUES ($1, $2, $3, $4::uuid, $5, $6, $7::uuid)
	`, roleID, opt.EntityType, entityRef, exhibitionID, resourceType, resourceRef, organizationID)
	if err != nil {
		t.Fatalf("testutil.Grant: %v", err)
	}
}
