package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

type permissionsResponseBody struct {
	Grants  []permissions.Grant `json:"grants"`
	Summary []string            `json:"summary"`
}

func TestPermissionsHandler_ServeHTTP_Unauthenticated_NoGrants(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PermissionsHandler{Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/api/v1/permissions", "", exhibitionID, nil, nil, h.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp permissionsResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A brand-new exhibition with no grants at all: both slices must be
	// present-but-empty JSON arrays ([]), not null — ServeHTTP explicitly
	// guards against a nil Grants slice, and Summary is built with make(...,
	// 0) rather than left as a nil var.
	if resp.Grants == nil || len(resp.Grants) != 0 {
		t.Errorf("Grants = %v, want an empty (non-nil) slice", resp.Grants)
	}
	if resp.Summary == nil || len(resp.Summary) != 0 {
		t.Errorf("Summary = %v, want an empty (non-nil) slice", resp.Summary)
	}
}

func TestPermissionsHandler_ServeHTTP_DedupsSummaryAcrossRoles(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PermissionsHandler{Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	user := testutil.CreateUser(t, env.Pool)

	// Two different roles, both granting PermGalleryView to this same user
	// in the same exhibition — UserPermissions is documented to dedup the
	// same (permission, resource_type, resource_ref) triple across multiple
	// roles/entity paths, and ServeHTTP's own summary-building loop adds a
	// second layer of dedup on top of that. Either one collapsing would
	// hide a regression in the other, so this checks the end-to-end result:
	// exactly one "GalleryView" in Summary despite two matching grants.
	role1 := testutil.CreateRole(t, env.Pool, exhibitionID, "Role1", permissions.PermGalleryView)
	testutil.Grant(t, env.Pool, role1, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: user, ExhibitionID: exhibitionID,
	})
	role2 := testutil.CreateRole(t, env.Pool, exhibitionID, "Role2", permissions.PermGalleryView)
	testutil.Grant(t, env.Pool, role2, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ExhibitionID: exhibitionID,
	})

	// Also grant a resource-scoped permission (a specific gallery) — this
	// must show up in Grants (with its resource_type/resource_ref) but NOT
	// in Summary, since ServeHTTP only summarizes grants with an empty
	// ResourceType.
	galleryID := testutil.CreateGallery(t, env.Pool, exhibitionID)
	galleryRole := testutil.CreateRole(t, env.Pool, exhibitionID, "GalleryModRole", permissions.PermGalleryModify)
	testutil.Grant(t, env.Pool, galleryRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: user,
		ResourceType: permissions.ResourceGallery, ResourceRef: galleryID,
	})

	rec := doRequest(t, http.MethodGet, "/api/v1/permissions", user, exhibitionID, nil, nil, h.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp permissionsResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	viewCount := 0
	for _, p := range resp.Summary {
		if p == permissions.PermGalleryView {
			viewCount++
		}
		if p == permissions.PermGalleryModify {
			t.Errorf("Summary contains %q, want it excluded (that grant is resource-scoped, not global/exhibition)", permissions.PermGalleryModify)
		}
	}
	if viewCount != 1 {
		t.Errorf("Summary contains %q %d times, want exactly 1 despite two matching grants", permissions.PermGalleryView, viewCount)
	}

	foundResourceGrant := false
	for _, g := range resp.Grants {
		if g.Permission == permissions.PermGalleryModify && g.ResourceType == permissions.ResourceGallery && g.ResourceRef == galleryID {
			foundResourceGrant = true
		}
	}
	if !foundResourceGrant {
		t.Errorf("Grants = %+v, want the resource-scoped GalleryModify/%s grant present", resp.Grants, galleryID)
	}
}
