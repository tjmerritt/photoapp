package permissions_test

import (
	"context"
	"testing"

	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// ── Pure, DB-free tests ──────────────────────────────────────────────────────
// These always run, even without TEST_DATABASE_URL set, since they don't
// touch the database.

func TestIsValidPermission(t *testing.T) {
	if !permissions.IsValidPermission(permissions.PermAdmin) {
		t.Errorf("IsValidPermission(%q) = false, want true", permissions.PermAdmin)
	}
	if !permissions.IsValidPermission(permissions.PermPhotoLabelCreate) {
		t.Errorf("IsValidPermission(%q) = false, want true", permissions.PermPhotoLabelCreate)
	}
	if permissions.IsValidPermission("NotARealPermission") {
		t.Error("IsValidPermission(\"NotARealPermission\") = true, want false")
	}
	if permissions.IsValidPermission("") {
		t.Error("IsValidPermission(\"\") = true, want false")
	}
}

// TestPermissionCatalog_SelfConsistent guards against the catalog and
// IsValidPermission drifting apart — every permission the catalog lists must
// also be considered valid, since the roles admin page (Phase 6i) relies on
// exactly that invariant to validate permission names before saving a role.
func TestPermissionCatalog_SelfConsistent(t *testing.T) {
	groups := permissions.PermissionCatalog()
	if len(groups) == 0 {
		t.Fatal("PermissionCatalog() returned no groups")
	}
	seen := map[string]bool{}
	for _, g := range groups {
		if g.Name == "" {
			t.Error("PermissionCatalog: group with empty Name")
		}
		if len(g.Permissions) == 0 {
			t.Errorf("PermissionCatalog: group %q has no permissions", g.Name)
		}
		for _, p := range g.Permissions {
			if !permissions.IsValidPermission(p) {
				t.Errorf("PermissionCatalog: %q listed in group %q but IsValidPermission(%q) = false", p, g.Name, p)
			}
			if seen[p] {
				t.Errorf("PermissionCatalog: %q listed more than once", p)
			}
			seen[p] = true
		}
	}
}

// ── Database-backed tests ─────────────────────────────────────────────────────
// These skip automatically when TEST_DATABASE_URL is unset — see
// internal/testutil's package doc comment.

func TestCheck_PublicGrant_ScopedToItsOwnExhibitionOnly(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	otherExhibitionID := testutil.CreateExhibition(t, pool)

	role := testutil.CreateRole(t, pool, exhibitionID, "Viewer", permissions.PermGalleryView)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType:   permissions.EntityPublic,
		ExhibitionID: exhibitionID,
	})

	ok, err := checker.Check(ctx, "", exhibitionID, "", "", "", permissions.PermGalleryView)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("Check() = false for the exhibition the grant is scoped to, want true")
	}

	// Regression guard for the leak fixed by migrations/018_grant_exhibitionid.sql:
	// an exhibition-scoped grant must NOT apply to a different exhibition.
	ok, err = checker.Check(ctx, "", otherExhibitionID, "", "", "", permissions.PermGalleryView)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ok {
		t.Error("Check() = true for an unrelated exhibition, want false (grant must not leak across exhibitions)")
	}
}

func TestCheck_LoggedInGrant_RequiresAuthentication(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	user := testutil.CreateUser(t, pool)

	role := testutil.CreateRole(t, pool, exhibitionID, "Contributor", permissions.PermPhotoLabelCreate)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType:   permissions.EntityLoggedIn,
		ExhibitionID: exhibitionID,
	})

	ok, err := checker.Check(ctx, user, exhibitionID, "", "", "", permissions.PermPhotoLabelCreate)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("Check() = false for an authenticated user against a LoggedIn grant, want true")
	}

	ok, err = checker.Check(ctx, "", exhibitionID, "", "", "", permissions.PermPhotoLabelCreate)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ok {
		t.Error("Check() = true for an unauthenticated caller against a LoggedIn grant, want false")
	}
}

func TestCheck_TeamGrant_OnlyAppliesToMembers(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	member := testutil.CreateUser(t, pool)
	nonMember := testutil.CreateUser(t, pool)
	team := testutil.CreateTeam(t, pool, exhibitionID)
	testutil.AddTeamMember(t, pool, team, member)

	role := testutil.CreateRole(t, pool, exhibitionID, "Admins", permissions.PermAdmin)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType:   permissions.EntityTeam,
		EntityRef:    team,
		ExhibitionID: exhibitionID,
	})

	ok, err := checker.Check(ctx, member, exhibitionID, "", "", "", permissions.PermAdmin)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("Check() = false for a team member, want true")
	}

	ok, err = checker.Check(ctx, nonMember, exhibitionID, "", "", "", permissions.PermAdmin)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ok {
		t.Error("Check() = true for a user outside the team, want false")
	}
}

func TestCheck_UserDirectGrant_ScopedToOneResource(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	owner := testutil.CreateUser(t, pool)
	other := testutil.CreateUser(t, pool)
	photoA := testutil.CreatePhoto(t, pool, exhibitionID, owner)
	photoB := testutil.CreatePhoto(t, pool, exhibitionID, owner)

	role := testutil.CreateRole(t, pool, exhibitionID, "PhotoBEditor", permissions.PermPhotoDescriptionModify)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType:   permissions.EntityUser,
		EntityRef:    other,
		ResourceType: permissions.ResourcePhoto,
		ResourceRef:  photoB,
	})

	ok, err := checker.Check(ctx, other, exhibitionID, "", permissions.ResourcePhoto, photoB, permissions.PermPhotoDescriptionModify)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("Check() = false for the granted user against the granted photo, want true")
	}

	ok, err = checker.Check(ctx, other, exhibitionID, "", permissions.ResourcePhoto, photoA, permissions.PermPhotoDescriptionModify)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ok {
		t.Error("Check() = true for a different photo than the one granted, want false")
	}

	ok, err = checker.Check(ctx, owner, exhibitionID, "", permissions.ResourcePhoto, photoB, permissions.PermPhotoDescriptionModify)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ok {
		t.Error("Check() = true for a user the grant was never issued to, want false")
	}
}

func TestCheck_ExhibitionGrant_CoversPhotosDirectly(t *testing.T) {
	// Photo chain is Global -> Exhibition -> Photo (no Gallery tier — see
	// permissions.go's package doc comment).
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	user := testutil.CreateUser(t, pool)
	photo := testutil.CreatePhoto(t, pool, exhibitionID, user)

	role := testutil.CreateRole(t, pool, exhibitionID, "Admin", permissions.PermPhotoLabelView)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType:   permissions.EntityUser,
		EntityRef:    user,
		ExhibitionID: exhibitionID,
	})

	ok, err := checker.Check(ctx, user, exhibitionID, "", permissions.ResourcePhoto, photo, permissions.PermPhotoLabelView)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("Check() = false for a photo covered by an exhibition-level grant, want true")
	}
}

func TestCheck_GalleryGrant_CoversDisplaysWithinIt_NotPhotos(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	user := testutil.CreateUser(t, pool)
	gallery := testutil.CreateGallery(t, pool, exhibitionID)
	display := testutil.CreateDisplay(t, pool, gallery)
	photo := testutil.CreatePhoto(t, pool, exhibitionID, user)

	role := testutil.CreateRole(t, pool, exhibitionID, "GalleryEditor", permissions.PermDisplayModify)
	testutil.Grant(t, pool, role, testutil.GrantOptions{
		EntityType:   permissions.EntityUser,
		EntityRef:    user,
		ResourceType: permissions.ResourceGallery,
		ResourceRef:  gallery,
	})

	// Gallery grant covers a Display within it (Gallery chain: ... -> Gallery -> Display).
	ok, err := checker.Check(ctx, user, exhibitionID, gallery, permissions.ResourceDisplay, display, permissions.PermDisplayModify)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("Check() = false for a display inside the granted gallery, want true")
	}

	// But a Gallery grant must NOT cover a Photo — the doc comment on
	// permissions.go is explicit that Photos are not under the Gallery chain.
	roleB := testutil.CreateRole(t, pool, exhibitionID, "GalleryEditorPhotoPerm", permissions.PermPhotoLabelView)
	testutil.Grant(t, pool, roleB, testutil.GrantOptions{
		EntityType:   permissions.EntityUser,
		EntityRef:    user,
		ResourceType: permissions.ResourceGallery,
		ResourceRef:  gallery,
	})
	ok, err = checker.Check(ctx, user, exhibitionID, "", permissions.ResourcePhoto, photo, permissions.PermPhotoLabelView)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ok {
		t.Error("Check() = true for a photo via a Gallery-scoped grant, want false (photos are not under the Gallery chain)")
	}
}

func TestCheck_GlobalGrant_AppliesToEveryExhibition(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionA := testutil.CreateExhibition(t, pool)
	exhibitionB := testutil.CreateExhibition(t, pool)

	// A global grant still needs a "home" exhibition for the role row itself
	// (roles.exhibitionid is NOT NULL), but the grant row has both
	// exhibitionid and resource_type/resource_ref left NULL, which Check()
	// treats as applying everywhere.
	role := testutil.CreateRole(t, pool, exhibitionA, "SuperAdmin", permissions.PermAdmin)
	testutil.Grant(t, pool, role, testutil.GrantOptions{EntityType: permissions.EntityPublic})

	for _, exhibitionID := range []string{exhibitionA, exhibitionB, ""} {
		ok, err := checker.Check(ctx, "", exhibitionID, "", "", "", permissions.PermAdmin)
		if err != nil {
			t.Fatalf("Check(exhibitionID=%q): %v", exhibitionID, err)
		}
		if !ok {
			t.Errorf("Check(exhibitionID=%q) = false for a global grant, want true", exhibitionID)
		}
	}
}

func TestUserPermissions_DedupsAcrossMultipleGrantPaths(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	user := testutil.CreateUser(t, pool)

	// Grant the same permission to this user via two independent paths:
	// once to LoggedIn (everyone) and once directly to the user.
	roleA := testutil.CreateRole(t, pool, exhibitionID, "Contributor", permissions.PermPhotoCommentCreate)
	testutil.Grant(t, pool, roleA, testutil.GrantOptions{EntityType: permissions.EntityLoggedIn, ExhibitionID: exhibitionID})

	roleB := testutil.CreateRole(t, pool, exhibitionID, "DirectGrant", permissions.PermPhotoCommentCreate)
	testutil.Grant(t, pool, roleB, testutil.GrantOptions{EntityType: permissions.EntityUser, EntityRef: user, ExhibitionID: exhibitionID})

	grants, err := checker.UserPermissions(ctx, user, exhibitionID)
	if err != nil {
		t.Fatalf("UserPermissions: %v", err)
	}

	count := 0
	for _, g := range grants {
		if g.Permission == permissions.PermPhotoCommentCreate && g.ResourceType == "" && g.ResourceRef == "" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("UserPermissions: found %d global-scope entries for %s reachable via two roles, want exactly 1 (deduped)", count, permissions.PermPhotoCommentCreate)
	}
}

func TestHasAny_TrueIfAnyPermissionMatches(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	user := testutil.CreateUser(t, pool)

	role := testutil.CreateRole(t, pool, exhibitionID, "LabelAdminOnly", permissions.PermLabelAdmin)
	testutil.Grant(t, pool, role, testutil.GrantOptions{EntityType: permissions.EntityUser, EntityRef: user, ExhibitionID: exhibitionID})

	ok, err := checker.HasAny(ctx, user, exhibitionID, permissions.PermAdmin, permissions.PermLabelAdmin)
	if err != nil {
		t.Fatalf("HasAny: %v", err)
	}
	if !ok {
		t.Error("HasAny() = false when the user holds one of the listed permissions, want true")
	}

	ok, err = checker.HasAny(ctx, user, exhibitionID, permissions.PermAdmin, permissions.PermUserAdmin)
	if err != nil {
		t.Fatalf("HasAny: %v", err)
	}
	if ok {
		t.Error("HasAny() = true when the user holds none of the listed permissions, want false")
	}
}

func TestSingletonGrant_GrantAndRevoke(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	checker := &permissions.Checker{DB: pool}

	exhibitionID := testutil.CreateExhibition(t, pool)
	user := testutil.CreateUser(t, pool)

	has, err := checker.HasDirectUserGrant(ctx, exhibitionID, user, permissions.PermPrivatePhotoView)
	if err != nil {
		t.Fatalf("HasDirectUserGrant (before grant): %v", err)
	}
	if has {
		t.Fatal("HasDirectUserGrant() = true before GrantUserPermission was ever called")
	}

	if err := checker.GrantUserPermission(ctx, exhibitionID, user, permissions.PermPrivatePhotoView); err != nil {
		t.Fatalf("GrantUserPermission: %v", err)
	}
	// Idempotent: granting twice must not error or duplicate the grant row.
	if err := checker.GrantUserPermission(ctx, exhibitionID, user, permissions.PermPrivatePhotoView); err != nil {
		t.Fatalf("GrantUserPermission (second call): %v", err)
	}

	has, err = checker.HasDirectUserGrant(ctx, exhibitionID, user, permissions.PermPrivatePhotoView)
	if err != nil {
		t.Fatalf("HasDirectUserGrant (after grant): %v", err)
	}
	if !has {
		t.Error("HasDirectUserGrant() = false after GrantUserPermission, want true")
	}

	ok, err := checker.Check(ctx, user, exhibitionID, "", "", "", permissions.PermPrivatePhotoView)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Error("Check() = false for a permission granted via the singleton-role mechanism, want true")
	}

	if err := checker.RevokeUserPermission(ctx, exhibitionID, user, permissions.PermPrivatePhotoView); err != nil {
		t.Fatalf("RevokeUserPermission: %v", err)
	}

	has, err = checker.HasDirectUserGrant(ctx, exhibitionID, user, permissions.PermPrivatePhotoView)
	if err != nil {
		t.Fatalf("HasDirectUserGrant (after revoke): %v", err)
	}
	if has {
		t.Error("HasDirectUserGrant() = true after RevokeUserPermission, want false")
	}
}
