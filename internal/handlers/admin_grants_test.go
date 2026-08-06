package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// adminGrantEntry mirrors the unexported adminGrant shape admin_grants.go's
// listing endpoints return.
type adminGrantEntry struct {
	GrantID        string `json:"grantid"`
	EntityType     string `json:"entity_type"`
	EntityRef      string `json:"entity_ref"`
	RoleID         string `json:"roleid"`
	ExhibitionID   string `json:"exhibitionid"`
	OrganizationID string `json:"organizationid"`
	ResourceType   string `json:"resource_type"`
	ResourceRef    string `json:"resource_ref"`
	ResourceName   string `json:"resource_name"`
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

// ── PLAN2.md Phase 3d: Group as a grant resource ─────────────────────────────

// seedResourceGroup inserts a static resource_groups row directly (testutil
// has no group-creation helper — see groups_test.go's own comment on the
// same gap) and returns its groupid. Pass exhibitionID XOR organizationID,
// mirroring chk_resource_groups_exhibition_type.
func seedResourceGroup(t *testing.T, env *testEnv, resourceType, exhibitionID, organizationID string) string {
	t.Helper()
	var groupID string
	var err error
	if organizationID != "" {
		err = env.Pool.QueryRow(context.Background(), `
			INSERT INTO resource_groups (resource_type, organizationid, name)
			VALUES ($1, $2::uuid, $3)
			RETURNING groupid::text
		`, resourceType, organizationID, "test-group-"+uuid.NewString()).Scan(&groupID)
	} else {
		err = env.Pool.QueryRow(context.Background(), `
			INSERT INTO resource_groups (resource_type, exhibitionid, name)
			VALUES ($1, $2::uuid, $3)
			RETURNING groupid::text
		`, resourceType, exhibitionID, "test-group-"+uuid.NewString()).Scan(&groupID)
	}
	if err != nil {
		t.Fatalf("seedResourceGroup: %v", err)
	}
	return groupID
}

// TestGrantsHandler_Create_GroupScope_ExhibitionScopedGroup_UsesExhibitionAdminTier
// confirms a Group-scoped grant targeting an exhibition-scoped (Photo/
// Gallery/Display-type) group is gated by ordinary exhibition-tier admin
// access — canManageRequestedScope resolves the group's own real
// exhibitionid via resourceGroupOwnScope, not the query-string exhibitionid,
// but for a group that genuinely belongs to the caller's own exhibition
// these should agree.
func TestGrantsHandler_Create_GroupScope_ExhibitionScopedGroup_UsesExhibitionAdminTier(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, targetRoleID := grantsFixture(t, env)
	groupID := seedResourceGroup(t, env, handlers.GroupResourcePhoto, exhibitionID, "")

	createBody, _ := json.Marshal(map[string]string{
		"roleid":        targetRoleID,
		"entity_type":   permissions.EntityLoggedIn,
		"resource_type": permissions.ResourceGroup,
		"resource_ref":  groupID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}

	plainUser := testutil.CreateUser(t, env.Pool)
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, plainUser, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Create as plain user: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestGrantsHandler_Create_GroupScope_ExhibitionTypeGroup_UsesOrgAdminTier is
// the case that motivated resourceGroupOwnScope in the first place: an
// Exhibition-type group is organization-scoped, so a Group-scoped grant
// targeting one must be gated by isOrgAdmin for that ORGANIZATION, not by
// exhibition-tier HasAny for whatever exhibitionid happens to be in the
// query string — an ordinary exhibition admin (even one within the same
// org) with no org-level grant must be rejected.
func TestGrantsHandler_Create_GroupScope_ExhibitionTypeGroup_UsesOrgAdminTier(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID, _, orgAdmin, targetRoleID := orgGrantsFixture(t, env)
	groupID := seedResourceGroup(t, env, handlers.GroupResourceExhibition, "", organizationID)

	createBody, _ := json.Marshal(map[string]string{
		"roleid":        targetRoleID,
		"entity_type":   permissions.EntityLoggedIn,
		"resource_type": permissions.ResourceGroup,
		"resource_ref":  groupID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants", orgAdmin, "", bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create as org admin: status = %d, body = %s", rec.Code, rec.Body)
	}

	// An admin of a wholly unrelated exhibition (a different, default
	// organization — testutil.CreateExhibition doesn't attach to
	// organizationID) must be rejected — canManageRequestedScope resolves
	// this Group grant's tier from the target group's own organization, not
	// from whatever exhibitionid the caller happens to supply. (Deliberately
	// NOT testing "an admin of some other exhibition WITHIN organizationID"
	// here — that case legitimately succeeds per isOrgAdmin's own third
	// tier, see TestGroupsHandler_ExhibitionAdminCanManageOrgGroups, so it
	// wouldn't isolate this fix.)
	outsideExhibitionID := testutil.CreateExhibition(t, env.Pool)
	outsideAdmin := testutil.CreateUser(t, env.Pool)
	outsideRole := testutil.CreateRole(t, env.Pool, outsideExhibitionID, "OutsideExhibitionAdmin", permissions.PermPermissionsAdmin)
	testutil.Grant(t, env.Pool, outsideRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: outsideAdmin, ExhibitionID: outsideExhibitionID,
	})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+outsideExhibitionID, outsideAdmin, outsideExhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Create as unrelated exhibition's admin: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestGrantsHandler_ListForExhibition_IncludesGroupGrant confirms a
// Group-scoped grant targeting an exhibition-scoped group shows up in
// ListForExhibition with its resource_type/resource_ref/resource_name
// populated, resolved via the target group's own exhibitionid (not the
// granting role's).
func TestGrantsHandler_ListForExhibition_IncludesGroupGrant(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, targetRoleID := grantsFixture(t, env)
	groupID := seedResourceGroup(t, env, handlers.GroupResourceGallery, exhibitionID, "")

	createBody, _ := json.Marshal(map[string]string{
		"roleid":        targetRoleID,
		"entity_type":   permissions.EntityLoggedIn,
		"resource_type": permissions.ResourceGroup,
		"resource_ref":  groupID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		GrantID string `json:"grantid"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/grants/exhibition?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.ListForExhibition)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListForExhibition: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Grants []adminGrantEntry `json:"grants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, g := range listResp.Grants {
		if g.GrantID == created.GrantID {
			found = true
			if g.ResourceType != permissions.ResourceGroup || g.ResourceRef != groupID {
				t.Errorf("found grant = %+v, want ResourceType=%s ResourceRef=%s", g, permissions.ResourceGroup, groupID)
			}
			if g.ResourceName == "" {
				t.Error("found grant: ResourceName is empty, want the group's name resolved")
			}
		}
	}
	if !found {
		t.Fatalf("ListForExhibition: created grant not found in %+v", listResp.Grants)
	}
}

// TestGrantsHandler_ListForOrganization_IncludesGroupGrant is
// ListForExhibition's counterpart for an Exhibition-type (org-scoped) group
// — the grant's own row never carries organizationid directly
// (chk_grant_scope_exclusive forbids combining it with resource_type), so
// this exercises the LEFT JOIN resource_groups fallback ListForOrganization
// needs to surface it at all.
func TestGrantsHandler_ListForOrganization_IncludesGroupGrant(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID, _, admin, targetRoleID := orgGrantsFixture(t, env)
	groupID := seedResourceGroup(t, env, handlers.GroupResourceExhibition, "", organizationID)

	createBody, _ := json.Marshal(map[string]string{
		"roleid":        targetRoleID,
		"entity_type":   permissions.EntityLoggedIn,
		"resource_type": permissions.ResourceGroup,
		"resource_ref":  groupID,
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
		Grants []adminGrantEntry `json:"grants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, g := range listResp.Grants {
		if g.GrantID == created.GrantID {
			found = true
			if g.OrganizationID != organizationID {
				t.Errorf("found grant: OrganizationID = %q, want %q (resolved via the target group, not erg.organizationid)", g.OrganizationID, organizationID)
			}
			if g.ResourceType != permissions.ResourceGroup || g.ResourceRef != groupID {
				t.Errorf("found grant = %+v, want ResourceType=%s ResourceRef=%s", g, permissions.ResourceGroup, groupID)
			}
		}
	}
	if !found {
		t.Fatalf("ListForOrganization: created grant not found in %+v", listResp.Grants)
	}
}

// TestGrantsHandler_Revoke_GroupGrant_UsesGroupOwnScope confirms Revoke
// resolves a Group-scoped grant's admin tier from the target group's own
// scope (grantScope's Group case), not from the granting role's own
// exhibitionid/organization the way a Gallery/Display/Photo-scoped grant
// does. The granting role deliberately lives in a SECOND, unrelated
// organization's exhibition — this is the only way to actually distinguish
// "used the group's real organization" from "used the role's" (using the
// SAME organization's own exhibition for both wouldn't discriminate: an
// admin of any exhibition inside that org would legitimately pass either
// way, per isOrgAdmin's third tier).
func TestGrantsHandler_Revoke_GroupGrant_UsesGroupOwnScope(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID, _, orgAdmin, _ := orgGrantsFixture(t, env)
	groupID := seedResourceGroup(t, env, handlers.GroupResourceExhibition, "", organizationID)

	otherOrgID := testutil.CreateOrganization(t, env.Pool)
	otherOrgExhibitionID := testutil.CreateExhibitionInOrg(t, env.Pool, otherOrgID)
	exhibitionScopedRole := testutil.CreateRole(t, env.Pool, otherOrgExhibitionID, "OtherOrgExhibitionRole", permissions.PermGalleryView)

	createBody, _ := json.Marshal(map[string]string{
		"roleid":        exhibitionScopedRole,
		"entity_type":   permissions.EntityLoggedIn,
		"resource_type": permissions.ResourceGroup,
		"resource_ref":  groupID,
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants", orgAdmin, "", bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		GrantID string `json:"grantid"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// An admin of the OTHER organization's exhibition (the granting role's
	// own home) must not be able to revoke this grant — it targets a group
	// belonging to the FIRST organization, not the second. If grantScope
	// buggily fell back to the role's own exhibition/organization for a
	// Group resource_type, this admin would incorrectly pass.
	otherOrgAdmin := testutil.CreateUser(t, env.Pool)
	otherOrgAdminRole := testutil.CreateRole(t, env.Pool, otherOrgExhibitionID, "OtherOrgAdmin", permissions.PermPermissionsAdmin)
	testutil.Grant(t, env.Pool, otherOrgAdminRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: otherOrgAdmin, ExhibitionID: otherOrgExhibitionID,
	})

	params := httprouter.Params{{Key: "grantid", Value: created.GrantID}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/grants/"+created.GrantID+"?exhibitionid="+otherOrgExhibitionID, otherOrgAdmin, otherOrgExhibitionID, nil, params, h.Revoke)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Revoke as the OTHER organization's exhibition admin: status = %d, want %d (grantScope must resolve the group's own organization, not the granting role's)", rec.Code, http.StatusForbidden)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/grants/"+created.GrantID, orgAdmin, "", nil, params, h.Revoke)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Revoke as the group's own org admin: status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestGrantsHandler_Create_GroupScope_ResourceTypeInvalid(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GrantsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, targetRoleID := grantsFixture(t, env)

	body, _ := json.Marshal(map[string]string{
		"roleid":        targetRoleID,
		"entity_type":   permissions.EntityLoggedIn,
		"resource_type": "NotAResourceType",
		"resource_ref":  "00000000-0000-0000-0000-000000000000",
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/grants?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
