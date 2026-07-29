// Package testutil provides database test infrastructure for photoapp.
//
// Tests that need a real Postgres connection call RequireDB(t), which:
//
//  1. Skips the test (via t.Skip, not a failure) when TEST_DATABASE_URL is
//     unset, or when psql is not found on PATH — so `go test ./...` stays
//     usable without a database configured (e.g. a contributor's first run,
//     or a CI stage that only lints).
//  2. Applies every schema migration in migrations/ (in numeric order) via
//     `psql -f`, the same mechanism the Makefile's migrate-up target uses.
//     This runs at most once per test binary invocation — every migration
//     file uses IF NOT EXISTS / IF EXISTS guards, so it's safe to point
//     TEST_DATABASE_URL at an already-migrated database too.
//  3. Truncates every table so each test starts from an empty database,
//     regardless of what earlier tests in the same run left behind.
//
// Point TEST_DATABASE_URL at a *disposable* database — tests truncate all
// data in it before every test. A local throwaway database works well:
//
//	createdb photoapp_test
//	TEST_DATABASE_URL=postgres://photoapp:photoapp@localhost:5432/photoapp_test?sslmode=disable go test ./...
//
// Fixture builders (CreateUser, CreateExhibition, CreateRole, Grant, ...)
// mirror the rows scripts/seed-exhibition.sh creates for a real deployment,
// scaled down to exactly what a given test needs.
package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/tjmerritt/photoapp/internal/db"
)

// ── Setup / teardown ─────────────────────────────────────────────────────────

var (
	migrationsOnce sync.Once
	migrationsErr  error
)

// RequireDB returns a connection pool to the database named by
// TEST_DATABASE_URL, with every table truncated so the test starts empty.
// See the package doc comment for the skip conditions and setup steps.
func RequireDB(t *testing.T) *db.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test (see internal/testutil doc comment)")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not found on PATH; skipping database-backed test (needed to apply migrations)")
	}

	migrationsOnce.Do(func() {
		migrationsErr = applyMigrations(dsn)
	})
	if migrationsErr != nil {
		t.Fatalf("testutil: apply migrations: %v", migrationsErr)
	}

	ctx := context.Background()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("testutil: connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	TruncateAll(t, pool)
	return pool
}

// migrationsDir locates the repo's migrations/ directory relative to this
// source file, so it resolves correctly regardless of which package's tests
// invoke RequireDB (Go tests run with the package directory as cwd).
func migrationsDir() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("could not determine testutil.go's own path")
	}
	// thisFile: <repo>/internal/testutil/testutil.go
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return filepath.Join(root, "migrations"), nil
}

// migrationLockKey is an arbitrary fixed advisory-lock key used to
// serialize migration application across concurrent processes (see
// applyMigrations). Value has no meaning beyond being unique to this
// project's migration-locking use — chosen once, must never change.
const migrationLockKey = 8743028917

// applyMigrations runs every migrations/*.sql file against dsn via `psql -f`,
// in numeric filename order, skipping *_seed.sql files — those are dev-only
// sample data (see `make seed`), not schema, and inserting them would leak
// fake fixture rows into every test.
//
// `go test ./...` runs each package's tests in a separate OS process, and by
// default runs multiple packages' processes concurrently (see `go help
// build`'s -p flag) — so with a shared TEST_DATABASE_URL, two packages can
// call this at the same moment. migrationsOnce (testutil.go) only dedupes
// within a single process, not across them, so every package's process
// re-applies every migration file independently. Each file is written to be
// idempotent (CREATE TABLE IF NOT EXISTS, etc.), but Postgres does not make
// "IF NOT EXISTS" DDL safe under true concurrency: two sessions can both see
// "not exists" and both attempt the CREATE, and one loses with a duplicate
// object error instead of silently no-op'ing. A Postgres advisory lock held
// for the whole migration run (not just one file) closes that race by
// ensuring only one process runs `psql -f` at a time; every other
// concurrent process simply blocks until the lock is free, then proceeds
// (the migrations it "re-applies" are genuine no-ops at that point).
//
// This does NOT make it safe to run different packages' DB-backed test
// suites concurrently against one shared database beyond this migration
// step — see TruncateAll's doc comment. `make test-go` passes `-p 1` for
// exactly that reason.
func applyMigrations(dsn string) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect for migration lock: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(migrationLockKey)); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", int64(migrationLockKey))

	dir, err := migrationsDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %s: %w", dir, err)
	}

	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if strings.HasSuffix(name, "_seed.sql") {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files) // "001_..." < "002_..." < ... < "018_..." sorts correctly as plain strings

	for _, name := range files {
		cmd := exec.Command("psql", dsn, "-v", "ON_ERROR_STOP=1", "-f", filepath.Join(dir, name))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("apply migration %s: %w\n%s", name, err, out)
		}
	}
	return nil
}

// TruncateAll empties every table in the public schema and refreshes the
// emoji_counts materialized view (which TRUNCATE doesn't touch, since it's a
// view over emoji_reactions rather than a table itself). RequireDB calls
// this before returning; tests normally don't need to call it directly.
//
// This is only safe when nothing else is concurrently reading or writing
// the same database. `go test ./...` runs different packages' tests in
// separate, concurrent OS processes by default (see `go help build`'s -p
// flag) — with a shared TEST_DATABASE_URL, one package's TruncateAll can
// wipe rows a different package's test is mid-assertion on, causing
// spurious, non-reproducible failures with no useful error beyond "row not
// found" or a permission check unexpectedly failing. `make test-go` passes
// `-p 1` so packages run one at a time against a shared test database;
// don't drop that flag unless every package gets its own database/schema
// instead.
func TruncateAll(t *testing.T, pool *db.Pool) {
	t.Helper()
	ctx := context.Background()

	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatalf("testutil: list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("testutil: scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("testutil: list tables: %v", err)
	}
	if len(tables) == 0 {
		return
	}

	quoted := make([]string, len(tables))
	for i, name := range tables {
		quoted[i] = pgx.Identifier{name}.Sanitize()
	}
	stmt := "TRUNCATE TABLE " + strings.Join(quoted, ", ") + " CASCADE"
	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("testutil: truncate tables: %v", err)
	}

	if err := pool.RefreshEmojiCounts(ctx); err != nil {
		t.Fatalf("testutil: refresh emoji_counts: %v", err)
	}
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

// MakePhotoPublic flips is_public to TRUE for photoID. Photos created by
// CreatePhoto default to private (is_public defaults to FALSE — see
// migrations/009_public_flag.sql), matching production's default for both
// cmd/import-photos (without --cascade) and the browser upload endpoint.
func MakePhotoPublic(t *testing.T, pool *db.Pool, photoID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE photos SET is_public = TRUE WHERE photoid = $1::uuid
	`, photoID); err != nil {
		t.Fatalf("testutil.MakePhotoPublic: %v", err)
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
// documented in migrations/012_permissions.sql and
// migrations/018_grant_exhibitionid.sql. Exactly one of ExhibitionID or
// ResourceType/ResourceRef should be set (or neither, for a true global
// grant) — the database enforces this via chk_exhibitionid_resource_exclusive.
type GrantOptions struct {
	// EntityType is one of permissions.EntityPublic, EntityLoggedIn,
	// EntityTeam, or EntityUser.
	EntityType string
	// EntityRef is the teamid or userid being granted to; "" for Public/LoggedIn.
	EntityRef string
	// ExhibitionID scopes the grant to one exhibition; "" for a grant not
	// scoped this way (global, or resource-scoped).
	ExhibitionID string
	// ResourceType is one of permissions.ResourceGallery, ResourceDisplay,
	// or ResourcePhoto; "" for a grant not scoped to one specific resource.
	ResourceType string
	// ResourceRef is the resource's UUID; "" when ResourceType is "".
	ResourceRef string
}

// Grant inserts one entity_role_grants row for roleID per opt.
func Grant(t *testing.T, pool *db.Pool, roleID string, opt GrantOptions) {
	t.Helper()

	var entityRef, exhibitionID, resourceType, resourceRef any
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

	_, err := pool.Exec(context.Background(), `
		INSERT INTO entity_role_grants (roleid, entity_type, entity_ref, exhibitionid, resource_type, resource_ref)
		VALUES ($1, $2, $3, $4::uuid, $5, $6)
	`, roleID, opt.EntityType, entityRef, exhibitionID, resourceType, resourceRef)
	if err != nil {
		t.Fatalf("testutil.Grant: %v", err)
	}
}
