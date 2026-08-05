package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// adminGrantEntry mirrors the unexported adminGrant shape admin_grants.go's
// listing endpoints return.
type adminGrantEntry struct {
	GrantID      string `json:"grantid"`
	EntityType   string `json:"entity_type"`
	EntityRef    string `json:"entity_ref"`
	RoleID       string `json:"roleid"`
	ExhibitionID string `json:"exhibitionid"`
	ResourceType string `json:"resource_type"`
	ResourceRef  string `json:"resource_ref"`
}

// grantsFixture creates an exhibition with an admin user granted
// PermPermissionsAdmin, plus a separate role (unattached to any grant) to
// use as the target of Create/Update calls in these tests.
func grantsFixture(t *testing.T, env *testEnv) (exhibitionID, admin, targetRoleID string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	admin = testutil.CreateUser(t, env.Pool)
	adminRole := testutil.CreateRole(t, env.Pool, exhibitionID, "GrantsAdmin", permissions.PermPermissionsAdmin)
	testutil.Grant(t, env.Pool, adminRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	targetRoleID = testutil.CreateRole(t, env.Pool, exhibitionID, "Viewer", permissions.PermGalleryView)
	return exhibitionID, admin, targetRoleID
}

func TestGrantsHandler_CreateListRevoke_ExhibitionScoped(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, targetRoleID := grantsFixture(t, env)

	createBody, _ := json.Marshal(map[string]string{
		"roleid":       targetRoleID,
		"entity_type":  permissions.EntityLoggedIn,
		"exhibitionid": exhibitionID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		GrantID string `json:"grantid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/grants/exhibition?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.ListForExhibition)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListForExhibition: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Total  int               `json:"total"`
		Grants []adminGrantEntry `json:"grants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("ListForExhibition: decode: %v", err)
	}
	// grantsFixture's own admin grant (PermPermissionsAdmin) also shows up
	// here — it's exhibition-scoped too — so this is a found-in-list check,
	// not an exact count of 1.
	found := false
	for _, g := range listResp.Grants {
		if g.GrantID == created.GrantID {
			found = true
			if g.RoleID != targetRoleID || g.EntityType != permissions.EntityLoggedIn {
				t.Errorf("found grant = %+v, want RoleID=%s EntityType=%s", g, targetRoleID, permissions.EntityLoggedIn)
			}
		}
	}
	if !found {
		t.Fatalf("ListForExhibition: created grant not found in %+v", listResp.Grants)
	}

	params := httprouter.Params{{Key: "grantid", Value: created.GrantID}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/grants/"+created.GrantID+"?exhibitionid="+exhibitionID, admin, exhibitionID, nil, params, h.Revoke)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Revoke: status = %d, body = %s", rec.Code, rec.Body)
	}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/grants/"+created.GrantID+"?exhibitionid="+exhibitionID, admin, exhibitionID, nil, params, h.Revoke)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Revoke (already revoked): status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// trueGlobalAdmin creates a user holding a genuinely global PermAdmin +
// PermPermissionsAdmin grant — entity_type=User, no exhibitionid,
// resource_type, OR organizationid at all, the only shape
// Checker.Check's own "Global grant" branch (and, since PLAN2.md Phase 2e,
// every global-scope check in admin_grants.go/roles.go) honors. Scoped to
// this one random userID, so — like TestGrantsHandler_
// ListForExhibition_RequiresExhibitionID's fixture — it doesn't contaminate
// other tests even without cleanup (no other test ever checks permissions
// for this exact user again), but the role is deleted anyway for tidiness.
func trueGlobalAdmin(t *testing.T, env *testEnv) string {
	t.Helper()
	homeExhibition := testutil.CreateExhibition(t, env.Pool)
	admin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, homeExhibition, "TrueGlobalAdmin", permissions.PermAdmin, permissions.PermPermissionsAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{EntityType: permissions.EntityUser, EntityRef: admin})
	t.Cleanup(func() {
		if _, err := env.Pool.Exec(context.Background(), `DELETE FROM roles WHERE roleid = $1`, role); err != nil {
			t.Errorf("cleanup: delete true-global-admin role: %v", err)
		}
	})
	return admin
}

// TestGrantsHandler_CreateListGlobal exercises a genuinely global grant
// (exhibitionid, resource_type, AND organizationid all empty), which — like
// permissions_test.go's TestCheck_GlobalGrant_AppliesToEveryExhibition —
// isn't scoped away from other tests by construction. ListGlobal returns
// every global grant in the whole database, so this uses a found-in-list
// check and cleans up the grant it creates via t.Cleanup rather than
// leaving it (and the PermAdmin it carries) live for every other test that
// runs after it, forever. Both Create and ListGlobal are called as a true
// global admin (PLAN2.md Phase 2e) — grantsFixture's exhibition-scoped
// admin is deliberately NOT used here; see
// TestGrantsHandler_ListGlobal_RequiresTrueGlobalAdmin for the negative
// case this distinction exists to guard.
func TestGrantsHandler_CreateListGlobal(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	globalAdmin := trueGlobalAdmin(t, env)
	globalRoleID := testutil.CreateRole(t, env.Pool, exhibitionID, "GlobalRole", permissions.PermPhotoLabelView)
	t.Cleanup(func() {
		if _, err := env.Pool.Exec(context.Background(), `DELETE FROM roles WHERE roleid = $1`, globalRoleID); err != nil {
			t.Errorf("cleanup: delete global role: %v", err)
		}
	})

	createBody, _ := json.Marshal(map[string]string{
		"roleid":      globalRoleID,
		"entity_type": permissions.EntityLoggedIn,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants", globalAdmin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		GrantID string `json:"grantid"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	t.Cleanup(func() {
		_, _ = env.Pool.Exec(context.Background(), `DELETE FROM entity_role_grants WHERE id = $1::uuid`, created.GrantID)
	})

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/grants/global", globalAdmin, exhibitionID, nil, nil, h.ListGlobal)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListGlobal: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Grants []adminGrantEntry `json:"grants"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	found := false
	for _, g := range listResp.Grants {
		if g.GrantID == created.GrantID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListGlobal: created grant not found in %d grants", len(listResp.Grants))
	}
}

// TestGrantsHandler_ListGlobal_RequiresTrueGlobalAdmin is the direct
// regression test for PLAN2.md Phase 2e's central fix: before it, ListGlobal
// only checked HasAny against the caller's own exhibition, so ANY exhibition
// admin — not just a true global one — could view (and, via Create/Update/
// Revoke, edit) grants that reach every other exhibition and organization in
// the install. grantsFixture's admin holds PermPermissionsAdmin scoped to
// just its own exhibition, which must no longer be sufficient.
func TestGrantsHandler_ListGlobal_RequiresTrueGlobalAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, exhibitionAdmin, _ := grantsFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/grants/global", exhibitionAdmin, exhibitionID, nil, nil, h.ListGlobal)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (exhibition-scoped admin must not see global grants)", rec.Code, http.StatusForbidden)
	}
}

// TestGrantsHandler_Create_GlobalScope_RequiresTrueGlobalAdmin covers the
// Create side of the same fix: an exhibition-scoped admin submitting a
// request with every scope field empty (roleid/entity_type only) must be
// rejected, not silently create a grant that reaches every organization and
// exhibition in the install.
func TestGrantsHandler_Create_GlobalScope_RequiresTrueGlobalAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, exhibitionAdmin, targetRoleID := grantsFixture(t, env)

	body, _ := json.Marshal(map[string]string{
		"roleid":      targetRoleID,
		"entity_type": permissions.EntityLoggedIn,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, exhibitionAdmin, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (exhibition-scoped admin must not create a global grant)", rec.Code, http.StatusForbidden)
	}
}

// TestGrantsHandler_Revoke_GlobalGrant_RequiresTrueGlobalAdmin covers Revoke:
// an exhibition-scoped admin must not be able to delete a grant that applies
// to every organization and exhibition just because the query string (or
// request context) happens to carry along their own exhibitionid — Revoke's
// authorization must come from the GRANT's own scope, not the caller's
// current admin page.
func TestGrantsHandler_Revoke_GlobalGrant_RequiresTrueGlobalAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, exhibitionAdmin, _ := grantsFixture(t, env)
	globalAdmin := trueGlobalAdmin(t, env)
	globalRoleID := testutil.CreateRole(t, env.Pool, exhibitionID, "GlobalRoleForRevoke", permissions.PermPhotoLabelView)
	t.Cleanup(func() {
		if _, err := env.Pool.Exec(context.Background(), `DELETE FROM roles WHERE roleid = $1`, globalRoleID); err != nil {
			t.Errorf("cleanup: delete global role: %v", err)
		}
	})

	createBody, _ := json.Marshal(map[string]string{
		"roleid":      globalRoleID,
		"entity_type": permissions.EntityLoggedIn,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants", globalAdmin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create (as global admin): status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		GrantID string `json:"grantid"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	t.Cleanup(func() {
		_, _ = env.Pool.Exec(context.Background(), `DELETE FROM entity_role_grants WHERE id = $1::uuid`, created.GrantID)
	})

	params := httprouter.Params{{Key: "grantid", Value: created.GrantID}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/grants/"+created.GrantID+"?exhibitionid="+exhibitionID, exhibitionAdmin, exhibitionID, nil, params, h.Revoke)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Revoke as exhibition admin: status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/grants/"+created.GrantID, globalAdmin, exhibitionID, nil, params, h.Revoke)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Revoke as global admin: status = %d, body = %s", rec.Code, rec.Body)
	}
}

// orgGrantsFixture creates an organization with an admin user granted
// PermPermissionsAdmin scoped directly to the organization (organizationid
// set on the grant, mirroring grantOrgAdmin in exhibitions.go), plus one
// exhibition under that org and one org-scoped role (unattached to any
// grant) to use as the target of Create calls.
func orgGrantsFixture(t *testing.T, env *testEnv) (organizationID, exhibitionID, admin, targetRoleID string) {
	t.Helper()
	organizationID = testutil.CreateOrganization(t, env.Pool)
	exhibitionID = testutil.CreateExhibitionInOrg(t, env.Pool, organizationID)
	admin = testutil.CreateUser(t, env.Pool)
	adminRole := testutil.CreateOrgRole(t, env.Pool, organizationID, "OrgGrantsAdmin", permissions.PermPermissionsAdmin)
	testutil.Grant(t, env.Pool, adminRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, OrganizationID: organizationID,
	})
	targetRoleID = testutil.CreateOrgRole(t, env.Pool, organizationID, "OrgViewer", permissions.PermGalleryView)
	return organizationID, exhibitionID, admin, targetRoleID
}

// TestGrantsHandler_ListForOrganization_OrgAdmin covers the basic
// organization-scoped Create + ListForOrganization round trip (PLAN2.md
// Phase 2e) — an org-level PermPermissionsAdmin grant (not tied to any one
// exhibition) creates and then sees an organization-scoped grant.
func TestGrantsHandler_ListForOrganization_OrgAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID, _, admin, targetRoleID := orgGrantsFixture(t, env)

	createBody, _ := json.Marshal(map[string]string{
		"roleid":         targetRoleID,
		"entity_type":    permissions.EntityLoggedIn,
		"organizationid": organizationID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants", admin, "", bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		GrantID string `json:"grantid"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/grants/organization?organizationid="+organizationID, admin, "", nil, nil, h.ListForOrganization)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListForOrganization: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Total  int               `json:"total"`
		Grants []adminGrantEntry `json:"grants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// orgGrantsFixture's own admin grant is also organization-scoped, so
	// this is a found-in-list check, not an exact count of 1.
	found := false
	for _, g := range listResp.Grants {
		if g.GrantID == created.GrantID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListForOrganization: created grant not found in %+v", listResp.Grants)
	}

	params := httprouter.Params{{Key: "grantid", Value: created.GrantID}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/grants/"+created.GrantID, admin, "", nil, params, h.Revoke)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Revoke: status = %d, body = %s", rec.Code, rec.Body)
	}
}

// TestGrantsHandler_ListForOrganization_ExhibitionAdminWithinOrgAllowed
// confirms isOrgAdmin's third tier: an admin of just ONE exhibition within
// an organization can still see that organization's own grants — an
// exhibition admin already administers everything under their exhibition,
// so the org tier only ever adds reach, never narrows what an exhibition
// admin can already see.
func TestGrantsHandler_ListForOrganization_ExhibitionAdminWithinOrgAllowed(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID := testutil.CreateOrganization(t, env.Pool)
	exhibitionID := testutil.CreateExhibitionInOrg(t, env.Pool, organizationID)
	exhibitionAdmin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "ExAdmin", permissions.PermPermissionsAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: exhibitionAdmin, ExhibitionID: exhibitionID,
	})

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/grants/organization?organizationid="+organizationID, exhibitionAdmin, exhibitionID, nil, nil, h.ListForOrganization)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s (an exhibition admin within the org should see the org's grants)", rec.Code, rec.Body)
	}
}

// TestGrantsHandler_ListForOrganization_DifferentOrgForbidden is the
// cross-organization isolation guard: an admin of organization A must not
// be able to browse organization B's grants.
func TestGrantsHandler_ListForOrganization_DifferentOrgForbidden(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	_, _, adminA, _ := orgGrantsFixture(t, env)
	organizationB := testutil.CreateOrganization(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/grants/organization?organizationid="+organizationB, adminA, "", nil, nil, h.ListForOrganization)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (org A's admin must not see org B's grants)", rec.Code, http.StatusForbidden)
	}
}

func TestGrantsHandler_Create_ValidatesMutualExclusivity(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, targetRoleID := grantsFixture(t, env)
	galleryID := testutil.CreateGallery(t, env.Pool, exhibitionID)

	body, _ := json.Marshal(map[string]string{
		"roleid":        targetRoleID,
		"entity_type":   permissions.EntityLoggedIn,
		"exhibitionid":  exhibitionID,
		"resource_type": permissions.ResourceGallery,
		"resource_ref":  galleryID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (exhibitionid and resource_type are mutually exclusive)", rec.Code, http.StatusBadRequest)
	}
}

func TestGrantsHandler_Create_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	plainUser := testutil.CreateUser(t, env.Pool)
	roleID := testutil.CreateRole(t, env.Pool, exhibitionID, "Viewer", permissions.PermGalleryView)

	body, _ := json.Marshal(map[string]string{
		"roleid":       roleID,
		"entity_type":  permissions.EntityLoggedIn,
		"exhibitionid": exhibitionID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, plainUser, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestGrantsHandler_Update_ChangesScope(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, targetRoleID := grantsFixture(t, env)
	otherUser := testutil.CreateUser(t, env.Pool)

	createBody, _ := json.Marshal(map[string]string{
		"roleid":       targetRoleID,
		"entity_type":  permissions.EntityLoggedIn,
		"exhibitionid": exhibitionID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	var created struct {
		GrantID string `json:"grantid"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	updateBody, _ := json.Marshal(map[string]string{
		"roleid":       targetRoleID,
		"entity_type":  permissions.EntityUser,
		"entity_ref":   otherUser,
		"exhibitionid": exhibitionID,
	})
	params := httprouter.Params{{Key: "grantid", Value: created.GrantID}}
	rec = doRequest(t, http.MethodPatch, "/api/v1/admin/grants/"+created.GrantID+"?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Update: status = %d, body = %s", rec.Code, rec.Body)
	}

	var entityType, entityRef string
	if err := env.Pool.QueryRow(context.Background(),
		`SELECT entity_type, entity_ref FROM entity_role_grants WHERE id = $1::uuid`, created.GrantID,
	).Scan(&entityType, &entityRef); err != nil {
		t.Fatalf("query updated grant: %v", err)
	}
	if entityType != permissions.EntityUser || entityRef != otherUser {
		t.Errorf("after Update: entity_type=%q entity_ref=%q, want %q %q", entityType, entityRef, permissions.EntityUser, otherUser)
	}
}

func TestGrantsHandler_Revoke_NotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, _ := grantsFixture(t, env)

	params := httprouter.Params{{Key: "grantid", Value: "00000000-0000-0000-0000-000000000000"}}
	rec := doRequest(t, http.MethodDelete, "/api/v1/admin/grants/x?exhibitionid="+exhibitionID, admin, exhibitionID, nil, params, h.Revoke)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGrantsHandler_ListForExhibition_RequiresExhibitionID(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	// Same ordering issue as TestAdminHandler_ListUsers_RequiresExhibitionID:
	// the HasAny permission check runs before the empty-exhibitionid check,
	// so grantsFixture's exhibition-scoped admin would 403 first. Needs a
	// grant with no exhibitionid at all — scoped to this one specific User
	// entity_ref, not Public/LoggedIn, so no other test is affected even
	// without cleanup, but delete it anyway for tidiness.
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	admin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "GlobalGrantsAdmin", permissions.PermPermissionsAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{EntityType: permissions.EntityUser, EntityRef: admin})
	t.Cleanup(func() {
		if _, err := env.Pool.Exec(context.Background(), `DELETE FROM roles WHERE roleid = $1`, role); err != nil {
			t.Errorf("cleanup: delete global-grant role: %v", err)
		}
	})

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/grants/exhibition", admin, "", nil, nil, h.ListForExhibition)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
