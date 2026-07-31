package testutil

// This file tests the test infrastructure itself, not application code.
//
// Every other *_test.go file in this repo takes testutil's fixture builders
// on faith: CreateUser returns a userid, so the test assumes a matching row
// really exists in `users` with the columns production code expects
// (provider='local', account_enabled=true, ...). If a builder silently
// inserted the wrong thing, or skipped a column whose default later changed,
// downstream tests could keep "passing" while actually asserting nothing
// meaningful — a bug in the harness masquerading as a passing suite. These
// tests check each builder's actual database effect directly (query the row
// back out, compare it against what the builder promises in its doc
// comment), rather than just calling it and checking for a Go error.
//
// Uses the same shared, unresettable TEST_DATABASE_URL as every other
// DB-backed test (see the package doc comment in testutil.go) — every
// assertion here is scoped to the exact row(s) this test's own call created,
// same discipline as everywhere else.

import (
	"context"
	"testing"

	"github.com/tjmerritt/photoapp/internal/permissions"
)

func TestRequireDB_ReturnsWorkingPool(t *testing.T) {
	pool := RequireDB(t)
	var one int
	if err := pool.QueryRow(context.Background(), `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("pool returned by RequireDB can't run a query: %v", err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 returned %d", one)
	}
}

func TestCreateUser(t *testing.T) {
	pool := RequireDB(t)
	userID := CreateUser(t, pool)
	if userID == "" {
		t.Fatal("CreateUser returned an empty userid")
	}

	var username, email, provider string
	var accountEnabled, allowMultiLogin bool
	err := pool.QueryRow(context.Background(), `
		SELECT username, email, provider, account_enabled, allow_multi_login
		FROM users WHERE userid = $1::uuid
	`, userID).Scan(&username, &email, &provider, &accountEnabled, &allowMultiLogin)
	if err != nil {
		t.Fatalf("querying back the row CreateUser claims to have inserted: %v", err)
	}

	if username == "" || email == "" {
		t.Errorf("username/email empty: username=%q email=%q", username, email)
	}
	// Several other tests (e.g. admin_test.go's UpdateUser, auth_test.go's
	// UploadProfileAvatar success case) rely on fixture users defaulting to
	// provider='local' and account_enabled=true without CreateUser setting
	// either explicitly — if a migration ever changes those column
	// defaults, this is the test that should catch it, not a handler test
	// asserting something unrelated.
	if provider != "local" {
		t.Errorf("provider = %q, want %q (CreateUser doesn't set it explicitly, relies on the column default)", provider, "local")
	}
	if !accountEnabled {
		t.Error("account_enabled = false, want true (column default)")
	}
	if allowMultiLogin {
		t.Error("allow_multi_login = true, want false (column default)")
	}
}

func TestCreateExhibition(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)
	if exhibitionID == "" {
		t.Fatal("CreateExhibition returned an empty exhibitionid")
	}

	var name string
	err := pool.QueryRow(context.Background(),
		`SELECT name FROM exhibitions WHERE exhibitionid = $1::uuid`, exhibitionID,
	).Scan(&name)
	if err != nil {
		t.Fatalf("querying back the row CreateExhibition claims to have inserted: %v", err)
	}
	if name == "" {
		t.Error("exhibition name is empty")
	}
}

func TestAddUserToExhibition(t *testing.T) {
	pool := RequireDB(t)
	userID := CreateUser(t, pool)
	exhibitionID := CreateExhibition(t, pool)

	AddUserToExhibition(t, pool, userID, exhibitionID)

	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM user_exhibitions WHERE userid = $1::uuid AND exhibitionid = $2::uuid
	`, userID, exhibitionID).Scan(&count)
	if err != nil {
		t.Fatalf("querying user_exhibitions: %v", err)
	}
	if count != 1 {
		t.Errorf("user_exhibitions rows for this (userid, exhibitionid) pair = %d, want 1", count)
	}
}

func TestCreatePhoto(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)
	ownerID := CreateUser(t, pool)

	photoID := CreatePhoto(t, pool, exhibitionID, ownerID)
	if photoID == "" {
		t.Fatal("CreatePhoto returned an empty photoid")
	}

	var gotExhibitionID, gotOwnerID string
	var width, height int
	err := pool.QueryRow(context.Background(), `
		SELECT exhibitionid::text, owner_userid::text, image_width, image_height
		FROM photos WHERE photoid = $1::uuid
	`, photoID).Scan(&gotExhibitionID, &gotOwnerID, &width, &height)
	if err != nil {
		t.Fatalf("querying back the row CreatePhoto claims to have inserted: %v", err)
	}
	if gotExhibitionID != exhibitionID {
		t.Errorf("exhibitionid = %q, want %q", gotExhibitionID, exhibitionID)
	}
	if gotOwnerID != ownerID {
		t.Errorf("owner_userid = %q, want %q", gotOwnerID, ownerID)
	}
	if width != 800 || height != 600 {
		t.Errorf("image dimensions = %dx%d, want 800x600 (per CreatePhoto's doc comment)", width, height)
	}
	// MakePhotoPublic's own doc comment (and several handler tests, e.g.
	// admin_test.go's SetPublic toggle test) depend on freshly created
	// photos defaulting to private — i.e. no "Public" label at all yet.
	var labelCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM labels WHERE photoid = $1::uuid AND name = 'Public' AND deleted_at IS NULL`, photoID,
	).Scan(&labelCount); err != nil {
		t.Fatalf("counting Public labels: %v", err)
	}
	if labelCount != 0 {
		t.Error("a fresh photo already has a Public label — CreatePhoto's fixtures are supposed to default private (no label at all, see fetch.go's photoIsPublicSQL)")
	}
}

func TestMakePhotoPublic(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)
	ownerID := CreateUser(t, pool)
	photoID := CreatePhoto(t, pool, exhibitionID, ownerID)

	MakePhotoPublic(t, pool, photoID)

	var labelValue string
	err := pool.QueryRow(context.Background(),
		`SELECT value FROM labels WHERE photoid = $1::uuid AND name = 'Public' AND deleted_at IS NULL`, photoID,
	).Scan(&labelValue)
	if err != nil {
		t.Fatalf("querying Public label: %v", err)
	}
	if labelValue != "True" {
		t.Errorf("Public label value = %q after MakePhotoPublic, want %q", labelValue, "True")
	}
}

func TestCreateGallery(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)

	galleryID := CreateGallery(t, pool, exhibitionID)
	if galleryID == "" {
		t.Fatal("CreateGallery returned an empty galleryid")
	}

	var gotExhibitionID, title string
	err := pool.QueryRow(context.Background(),
		`SELECT exhibitionid::text, title FROM galleries WHERE galleryid = $1::uuid`, galleryID,
	).Scan(&gotExhibitionID, &title)
	if err != nil {
		t.Fatalf("querying back the row CreateGallery claims to have inserted: %v", err)
	}
	if gotExhibitionID != exhibitionID {
		t.Errorf("exhibitionid = %q, want %q", gotExhibitionID, exhibitionID)
	}
	if title == "" {
		t.Error("gallery title is empty")
	}
}

func TestCreateDisplay(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)
	galleryID := CreateGallery(t, pool, exhibitionID)

	displayID := CreateDisplay(t, pool, galleryID)
	if displayID == "" {
		t.Fatal("CreateDisplay returned an empty displayid")
	}

	var gotGalleryID string
	err := pool.QueryRow(context.Background(),
		`SELECT galleryid::text FROM displays WHERE displayid = $1::uuid`, displayID,
	).Scan(&gotGalleryID)
	if err != nil {
		t.Fatalf("querying back the row CreateDisplay claims to have inserted: %v", err)
	}
	if gotGalleryID != galleryID {
		t.Errorf("galleryid = %q, want %q", gotGalleryID, galleryID)
	}
}

func TestPlacePhotoInSlot(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)
	owner := CreateUser(t, pool)
	photoID := CreatePhoto(t, pool, exhibitionID, owner)
	galleryID := CreateGallery(t, pool, exhibitionID)
	displayID := CreateDisplay(t, pool, galleryID)

	PlacePhotoInSlot(t, pool, displayID, photoID)

	var gotPhotoID string
	var gotSlotIndex int
	err := pool.QueryRow(context.Background(),
		`SELECT photoid::text, slot_index FROM display_slots WHERE displayid = $1::uuid`, displayID,
	).Scan(&gotPhotoID, &gotSlotIndex)
	if err != nil {
		t.Fatalf("querying back the row PlacePhotoInSlot claims to have inserted: %v", err)
	}
	if gotPhotoID != photoID {
		t.Errorf("photoid = %q, want %q", gotPhotoID, photoID)
	}
	if gotSlotIndex != 0 {
		t.Errorf("slot_index = %d, want 0", gotSlotIndex)
	}
}

func TestCreateTeam(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)

	teamID := CreateTeam(t, pool, exhibitionID)
	if teamID == "" {
		t.Fatal("CreateTeam returned an empty teamid")
	}

	var gotExhibitionID, name string
	err := pool.QueryRow(context.Background(),
		`SELECT exhibitionid::text, name FROM teams WHERE teamid = $1::uuid`, teamID,
	).Scan(&gotExhibitionID, &name)
	if err != nil {
		t.Fatalf("querying back the row CreateTeam claims to have inserted: %v", err)
	}
	if gotExhibitionID != exhibitionID {
		t.Errorf("exhibitionid = %q, want %q", gotExhibitionID, exhibitionID)
	}
	if name == "" {
		t.Error("team name is empty")
	}
}

func TestAddTeamMember(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)
	teamID := CreateTeam(t, pool, exhibitionID)
	userID := CreateUser(t, pool)

	AddTeamMember(t, pool, teamID, userID)

	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM team_members WHERE teamid = $1::uuid AND userid = $2::uuid
	`, teamID, userID).Scan(&count)
	if err != nil {
		t.Fatalf("querying team_members: %v", err)
	}
	if count != 1 {
		t.Errorf("team_members rows for this (teamid, userid) pair = %d, want 1", count)
	}
}

func TestCreateEmojiType(t *testing.T) {
	pool := RequireDB(t)
	emojiID := CreateEmojiType(t, pool)
	if emojiID == "" {
		t.Fatal("CreateEmojiType returned an empty emojiid")
	}

	var altText string
	var isActive bool
	err := pool.QueryRow(context.Background(),
		`SELECT alt_text, is_active FROM emoji_types WHERE emojiid = $1::uuid`, emojiID,
	).Scan(&altText, &isActive)
	if err != nil {
		t.Fatalf("querying back the row CreateEmojiType claims to have inserted: %v", err)
	}
	if altText != "test emoji" {
		t.Errorf("alt_text = %q, want %q", altText, "test emoji")
	}
	if !isActive {
		t.Error("is_active = false, want true (per CreateEmojiType's doc comment)")
	}
}

func TestCreateRole(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)

	roleID := CreateRole(t, pool, exhibitionID, "Test Role", permissions.PermGalleryView, permissions.PermGalleryCreate)
	if roleID == "" {
		t.Fatal("CreateRole returned an empty roleid")
	}

	var gotExhibitionID, name string
	err := pool.QueryRow(context.Background(),
		`SELECT exhibitionid::text, name FROM roles WHERE roleid = $1::uuid`, roleID,
	).Scan(&gotExhibitionID, &name)
	if err != nil {
		t.Fatalf("querying back the row CreateRole claims to have inserted: %v", err)
	}
	if gotExhibitionID != exhibitionID {
		t.Errorf("exhibitionid = %q, want %q", gotExhibitionID, exhibitionID)
	}
	if name != "Test Role" {
		t.Errorf("name = %q, want %q", name, "Test Role")
	}

	rows, err := pool.Query(context.Background(),
		`SELECT permission FROM role_permissions WHERE roleid = $1::uuid ORDER BY permission`, roleID,
	)
	if err != nil {
		t.Fatalf("querying role_permissions: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scanning role_permissions: %v", err)
		}
		got = append(got, p)
	}
	want := []string{permissions.PermGalleryCreate, permissions.PermGalleryView} // alphabetical
	if len(got) != len(want) {
		t.Fatalf("role_permissions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("role_permissions[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCreateRole_NoPermissions(t *testing.T) {
	// perms is variadic — CreateRole must handle zero permissions cleanly
	// (a role that carries no permissions itself is a legitimate fixture,
	// e.g. an "empty role" used to test AddPermission).
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)
	roleID := CreateRole(t, pool, exhibitionID, "Empty Role")

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM role_permissions WHERE roleid = $1::uuid`, roleID,
	).Scan(&count); err != nil {
		t.Fatalf("querying role_permissions: %v", err)
	}
	if count != 0 {
		t.Errorf("role_permissions count = %d, want 0", count)
	}
}

func TestGrant(t *testing.T) {
	pool := RequireDB(t)
	exhibitionID := CreateExhibition(t, pool)
	userID := CreateUser(t, pool)
	roleID := CreateRole(t, pool, exhibitionID, "Grant Target Role")

	t.Run("exhibition-scoped user grant", func(t *testing.T) {
		Grant(t, pool, roleID, GrantOptions{
			EntityType:   permissions.EntityUser,
			EntityRef:    userID,
			ExhibitionID: exhibitionID,
		})

		var entityType string
		var entityRef, gotExhibitionID string
		var resourceType, resourceRef *string
		err := pool.QueryRow(context.Background(), `
			SELECT entity_type, entity_ref, exhibitionid::text, resource_type, resource_ref
			FROM entity_role_grants
			WHERE roleid = $1::uuid AND entity_ref = $2 AND exhibitionid = $3::uuid
		`, roleID, userID, exhibitionID).Scan(&entityType, &entityRef, &gotExhibitionID, &resourceType, &resourceRef)
		if err != nil {
			t.Fatalf("querying back the row Grant claims to have inserted: %v", err)
		}
		if entityType != permissions.EntityUser || entityRef != userID {
			t.Errorf("entity_type/entity_ref = %q/%q, want %q/%q", entityType, entityRef, permissions.EntityUser, userID)
		}
		if gotExhibitionID != exhibitionID {
			t.Errorf("exhibitionid = %q, want %q", gotExhibitionID, exhibitionID)
		}
		if resourceType != nil || resourceRef != nil {
			t.Errorf("resource_type/resource_ref = %v/%v, want nil/nil for an exhibition-scoped grant", resourceType, resourceRef)
		}
	})

	t.Run("resource-scoped public grant", func(t *testing.T) {
		galleryID := CreateGallery(t, pool, exhibitionID)
		Grant(t, pool, roleID, GrantOptions{
			EntityType:   permissions.EntityPublic,
			ResourceType: permissions.ResourceGallery,
			ResourceRef:  galleryID,
		})

		var entityType string
		var entityRef *string
		var exhibitionIDCol *string
		var resourceType, resourceRef string
		err := pool.QueryRow(context.Background(), `
			SELECT entity_type, entity_ref, exhibitionid::text, resource_type, resource_ref
			FROM entity_role_grants
			WHERE roleid = $1::uuid AND resource_type = $2 AND resource_ref = $3
		`, roleID, permissions.ResourceGallery, galleryID).Scan(&entityType, &entityRef, &exhibitionIDCol, &resourceType, &resourceRef)
		if err != nil {
			t.Fatalf("querying back the row Grant claims to have inserted: %v", err)
		}
		if entityType != permissions.EntityPublic {
			t.Errorf("entity_type = %q, want %q", entityType, permissions.EntityPublic)
		}
		if entityRef != nil {
			t.Errorf("entity_ref = %v, want nil for a Public grant", *entityRef)
		}
		if exhibitionIDCol != nil {
			t.Errorf("exhibitionid = %v, want nil for a resource-scoped grant", *exhibitionIDCol)
		}
		if resourceType != permissions.ResourceGallery || resourceRef != galleryID {
			t.Errorf("resource_type/resource_ref = %q/%q, want %q/%q", resourceType, resourceRef, permissions.ResourceGallery, galleryID)
		}
	})

	t.Run("true global grant has every optional column NULL", func(t *testing.T) {
		Grant(t, pool, roleID, GrantOptions{EntityType: permissions.EntityLoggedIn})

		var entityType string
		var entityRef, exhibitionIDCol, resourceType, resourceRef *string
		err := pool.QueryRow(context.Background(), `
			SELECT entity_type, entity_ref, exhibitionid::text, resource_type, resource_ref
			FROM entity_role_grants
			WHERE roleid = $1::uuid AND entity_type = $2
			  AND exhibitionid IS NULL AND resource_type IS NULL
		`, roleID, permissions.EntityLoggedIn).Scan(&entityType, &entityRef, &exhibitionIDCol, &resourceType, &resourceRef)
		if err != nil {
			t.Fatalf("querying back the row Grant claims to have inserted: %v", err)
		}
		if entityRef != nil || exhibitionIDCol != nil || resourceType != nil || resourceRef != nil {
			t.Errorf("expected every optional column NULL for a true global grant, got entity_ref=%v exhibitionid=%v resource_type=%v resource_ref=%v",
				entityRef, exhibitionIDCol, resourceType, resourceRef)
		}
	})
}
