package permissions_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// testutil has no group-creation helpers of its own (groups.go's own
// validation logic — resource_type-appropriate scope, member existence —
// belongs at the handler layer, not in shared test fixtures), so every test
// below inserts resource_groups/resource_group_members/
// resource_group_label_rules directly, the same way other test files insert
// directly into label_names or entity_role_grants when testutil doesn't
// already have a builder.

func TestCheck_GroupGrant_PhotoType_StaticMembership(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	owner := testutil.CreateUser(t, pool)
	viewer := testutil.CreateUser(t, pool)
	photoA := testutil.CreatePhoto(t, pool, exhibitionID, owner)
	photoB := testutil.CreatePhoto(t, pool, exhibitionID, owner)

	var groupID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO resource_groups (resource_type, exhibitionid, name)
		VALUES ('Photo', $1::uuid, $2)
		RETURNING groupid::text
	`, exhibitionID, "Featured-"+uuid.NewString()).Scan(&groupID); err != nil {
		t.Fatalf("seed resource_groups: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO resource_group_members (groupid, resource_ref) VALUES ($1::uuid, $2)
	`, groupID, photoA); err != nil {
		t.Fatalf("seed resource_group_members: %v", err)
	}

	role := testutil.CreateRole(t, pool, exhibitionID, "GroupViewer", permissions.PermPhotoView)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ResourceType: permissions.ResourceGroup, ResourceRef: groupID,
	})

	// checker.Check's Team-membership branch casts userID to ::uuid via a
	// non-correlated subquery, which Postgres evaluates regardless of
	// whether entity_type='Team' actually matches any row — so userID must
	// always be "" or a real UUID here, never an arbitrary string (this bit
	// Session 45's own first test-writing pass; see SUMMARIES2.md).
	ok, err := checker.Check(ctx, viewer, exhibitionID, "", permissions.ResourcePhoto, photoA, permissions.PermPhotoView)
	if err != nil {
		t.Fatalf("Check(photoA): %v", err)
	}
	if !ok {
		t.Error("Check(photoA) = false, want true (photoA is a member of the granted group)")
	}

	ok, err = checker.Check(ctx, viewer, exhibitionID, "", permissions.ResourcePhoto, photoB, permissions.PermPhotoView)
	if err != nil {
		t.Fatalf("Check(photoB): %v", err)
	}
	if ok {
		t.Error("Check(photoB) = true, want false (photoB is NOT a member of the granted group)")
	}
}

func TestCheck_GroupGrant_PhotoType_DynamicMembership(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	owner := testutil.CreateUser(t, pool)
	viewer := testutil.CreateUser(t, pool)
	photoA := testutil.CreatePhoto(t, pool, exhibitionID, owner)
	photoB := testutil.CreatePhoto(t, pool, exhibitionID, owner)

	labelName := "Sensitive-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO labels (photoid, added_by_userid, name, value) VALUES ($1::uuid, $2::uuid, $3, 'yes')
	`, photoA, owner, labelName); err != nil {
		t.Fatalf("seed labels: %v", err)
	}

	var groupID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO resource_groups (resource_type, exhibitionid, name, is_dynamic)
		VALUES ('Photo', $1::uuid, $2, TRUE)
		RETURNING groupid::text
	`, exhibitionID, "Sensitive Photos-"+uuid.NewString()).Scan(&groupID); err != nil {
		t.Fatalf("seed resource_groups: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO resource_group_label_rules (groupid, label_name, label_value) VALUES ($1::uuid, $2, 'yes')
	`, groupID, labelName); err != nil {
		t.Fatalf("seed resource_group_label_rules: %v", err)
	}

	role := testutil.CreateRole(t, pool, exhibitionID, "SensitiveViewer", permissions.PermPrivatePhotoView)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ResourceType: permissions.ResourceGroup, ResourceRef: groupID,
	})

	ok, err := checker.Check(ctx, viewer, exhibitionID, "", permissions.ResourcePhoto, photoA, permissions.PermPrivatePhotoView)
	if err != nil {
		t.Fatalf("Check(photoA): %v", err)
	}
	if !ok {
		t.Error("Check(photoA) = false, want true (photoA carries the matching label, so it's a dynamic member)")
	}

	ok, err = checker.Check(ctx, viewer, exhibitionID, "", permissions.ResourcePhoto, photoB, permissions.PermPrivatePhotoView)
	if err != nil {
		t.Fatalf("Check(photoB): %v", err)
	}
	if ok {
		t.Error("Check(photoB) = true, want false (photoB has no matching label)")
	}
}

// TestCheck_GroupGrant_ExhibitionType confirms the other new branch: an
// Exhibition-type group's grant reaches every exhibition that's a member,
// evaluated through exhibitionID (HasAny's own parameter), not
// resourceType/resourceRef — mirroring how a direct exhibitionid grant
// reaches everything within one exhibition.
func TestCheck_GroupGrant_ExhibitionType(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	organizationID := testutil.CreateOrganization(t, pool)
	memberExhibition := testutil.CreateExhibitionInOrg(t, pool, organizationID)
	outsideExhibition := testutil.CreateExhibition(t, pool)
	viewer := testutil.CreateUser(t, pool)

	var groupID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO resource_groups (resource_type, organizationid, name)
		VALUES ('Exhibition', $1::uuid, $2)
		RETURNING groupid::text
	`, organizationID, "Touring-"+uuid.NewString()).Scan(&groupID); err != nil {
		t.Fatalf("seed resource_groups: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO resource_group_members (groupid, resource_ref) VALUES ($1::uuid, $2)
	`, groupID, memberExhibition); err != nil {
		t.Fatalf("seed resource_group_members: %v", err)
	}

	role := testutil.CreateOrgRole(t, pool, organizationID, "TouringAdmin", permissions.PermAdmin)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ResourceType: permissions.ResourceGroup, ResourceRef: groupID,
	})

	ok, err := checker.HasAny(ctx, viewer, memberExhibition, permissions.PermAdmin)
	if err != nil {
		t.Fatalf("HasAny(memberExhibition): %v", err)
	}
	if !ok {
		t.Error("HasAny(memberExhibition) = false, want true (memberExhibition is a member of the granted Exhibition-type group)")
	}

	ok, err = checker.HasAny(ctx, viewer, outsideExhibition, permissions.PermAdmin)
	if err != nil {
		t.Fatalf("HasAny(outsideExhibition): %v", err)
	}
	if ok {
		t.Error("HasAny(outsideExhibition) = true, want false (outsideExhibition is NOT a member of the granted group)")
	}
}

func TestCheck_GroupGrant_DoesNotCascadeToDisplaysWithinMemberGallery(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	galleryID := testutil.CreateGallery(t, pool, exhibitionID)
	displayID := testutil.CreateDisplay(t, pool, galleryID)
	viewer := testutil.CreateUser(t, pool)

	var groupID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO resource_groups (resource_type, exhibitionid, name)
		VALUES ('Gallery', $1::uuid, $2)
		RETURNING groupid::text
	`, exhibitionID, "Wing-"+uuid.NewString()).Scan(&groupID); err != nil {
		t.Fatalf("seed resource_groups: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO resource_group_members (groupid, resource_ref) VALUES ($1::uuid, $2)
	`, groupID, galleryID); err != nil {
		t.Fatalf("seed resource_group_members: %v", err)
	}

	role := testutil.CreateRole(t, pool, exhibitionID, "GalleryGroupViewer", permissions.PermGalleryView)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ResourceType: permissions.ResourceGroup, ResourceRef: groupID,
	})

	// The Gallery itself is reachable via the group grant.
	ok, err := checker.Check(ctx, viewer, exhibitionID, "", permissions.ResourceGallery, galleryID, permissions.PermGalleryView)
	if err != nil {
		t.Fatalf("Check(gallery): %v", err)
	}
	if !ok {
		t.Error("Check(gallery) = false, want true")
	}

	// But a Display inside that gallery is NOT — a Group grant doesn't
	// cascade down the Gallery->Display ownership chain the way a direct
	// Gallery grant does (see Checker.Check's own doc comment).
	ok, err = checker.Check(ctx, viewer, exhibitionID, galleryID, permissions.ResourceDisplay, displayID, permissions.PermGalleryView)
	if err != nil {
		t.Fatalf("Check(display): %v", err)
	}
	if ok {
		t.Error("Check(display) = true, want false (Group grants don't cascade to child resources)")
	}
}
