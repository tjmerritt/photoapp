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

// rolesAdminRole mirrors the unexported adminRole shape roles.go's List and
// Create endpoints return.
type rolesAdminRole struct {
	RoleID      string   `json:"roleid"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	GrantCount  int      `json:"grant_count"`
}

// rolesAdmin creates an exhibition with a user granted PermAdmin. The fixture
// itself creates one role ("SuperAdmin") to carry that grant, so list-based
// assertions in these tests use a found-in-list check rather than an exact
// count of 1.
func rolesAdmin(t *testing.T, env *testEnv) (exhibitionID, admin string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	admin = testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "SuperAdmin", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	return exhibitionID, admin
}

func TestRolesHandler_CreateListUpdateDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := rolesAdmin(t, env)

	createBody, _ := json.Marshal(map[string]string{"name": "Curator", "description": "Curates the wall"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/roles?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created rolesAdminRole
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}
	if created.Name != "Curator" || created.Description != "Curates the wall" || len(created.Permissions) != 0 {
		t.Fatalf("Create: got %+v, want Name=Curator Description set Permissions=[]", created)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/roles?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Total int              `json:"total"`
		Roles []rolesAdminRole `json:"roles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: decode: %v", err)
	}
	found := false
	for _, role := range listResp.Roles {
		if role.RoleID == created.RoleID {
			found = true
		}
	}
	if !found {
		t.Fatalf("List: created role not found in %+v", listResp.Roles)
	}

	params := httprouter.Params{{Key: "roleid", Value: created.RoleID}}

	// AddPermission.
	addBody, _ := json.Marshal(map[string]string{"permission": permissions.PermGalleryView})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/roles/"+created.RoleID+"/permissions", admin, exhibitionID, bytes.NewReader(addBody), params, h.AddPermission)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("AddPermission: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/roles?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.List)
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	var afterAdd rolesAdminRole
	for _, role := range listResp.Roles {
		if role.RoleID == created.RoleID {
			afterAdd = role
		}
	}
	if len(afterAdd.Permissions) != 1 || afterAdd.Permissions[0] != permissions.PermGalleryView {
		t.Fatalf("after AddPermission: Permissions = %+v, want [%s]", afterAdd.Permissions, permissions.PermGalleryView)
	}

	// RemovePermission.
	removeParams := httprouter.Params{{Key: "roleid", Value: created.RoleID}, {Key: "permission", Value: permissions.PermGalleryView}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/roles/"+created.RoleID+"/permissions/"+permissions.PermGalleryView, admin, exhibitionID, nil, removeParams, h.RemovePermission)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("RemovePermission: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/roles?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.List)
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	for _, role := range listResp.Roles {
		if role.RoleID == created.RoleID && len(role.Permissions) != 0 {
			t.Fatalf("after RemovePermission: Permissions = %+v, want none", role.Permissions)
		}
	}

	// Update.
	updateBody, _ := json.Marshal(map[string]string{"name": "Head Curator"})
	rec = doRequest(t, http.MethodPatch, "/api/v1/admin/roles/"+created.RoleID, admin, exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Update: status = %d, body = %s", rec.Code, rec.Body)
	}
	rec = doRequest(t, http.MethodGet, "/api/v1/admin/roles?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.List)
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	renamed := false
	for _, role := range listResp.Roles {
		if role.RoleID == created.RoleID && role.Name == "Head Curator" {
			renamed = true
		}
	}
	if !renamed {
		t.Fatalf("after Update: role %s not renamed to Head Curator in %+v", created.RoleID, listResp.Roles)
	}

	// Delete.
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/roles/"+created.RoleID, admin, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/roles/"+created.RoleID, admin, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Delete (already deleted): status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRolesHandler_Create_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	plainUser := testutil.CreateUser(t, env.Pool)

	body, _ := json.Marshal(map[string]string{"name": "Nope"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/roles?exhibitionid="+exhibitionID, plainUser, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestRolesHandler_AddPermission_UnknownPermissionRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := rolesAdmin(t, env)
	roleID := testutil.CreateRole(t, env.Pool, exhibitionID, "Empty")

	body, _ := json.Marshal(map[string]string{"permission": "NotARealPermission"})
	params := httprouter.Params{{Key: "roleid", Value: roleID}}
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/roles/"+roleID+"/permissions", admin, exhibitionID, bytes.NewReader(body), params, h.AddPermission)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRolesHandler_Update_RoleNotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	_, admin := rolesAdmin(t, env)

	body, _ := json.Marshal(map[string]string{"name": "x"})
	params := httprouter.Params{{Key: "roleid", Value: "00000000-0000-0000-0000-000000000000"}}
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/roles/x", admin, "", bytes.NewReader(body), params, h.Update)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestRolesHandler_Update_SingletonRoleRejected guards the auto-managed
// per-user "view private photos" grant mechanism (permissions package's
// singletonGrantRoleName, prefix "__grant:") from being hand-edited through
// this admin surface — see roles.go's package doc comment.
func TestRolesHandler_Update_SingletonRoleRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := rolesAdmin(t, env)
	singletonRoleID := testutil.CreateRole(t, env.Pool, exhibitionID, "__grant:some-user-id")

	body, _ := json.Marshal(map[string]string{"name": "Hijacked"})
	params := httprouter.Params{{Key: "roleid", Value: singletonRoleID}}
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/roles/"+singletonRoleID, admin, exhibitionID, bytes.NewReader(body), params, h.Update)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (singleton roles are auto-managed and not editable here)", rec.Code, http.StatusBadRequest)
	}
}

// orgRolesAdmin creates an organization with a user granted a genuinely
// organization-scoped PermAdmin grant (organizationid set on the grant, not
// tied to any one exhibition — mirrors grantOrgAdmin in exhibitions.go). The
// fixture itself creates one org-scoped role ("OrgSuperAdmin") to carry that
// grant, so list-based assertions in these tests use a found-in-list check
// rather than an exact count of 1, the same convention rolesAdmin uses for
// its exhibition-scoped equivalent.
func orgRolesAdmin(t *testing.T, env *testEnv) (organizationID, admin string) {
	t.Helper()
	organizationID = testutil.CreateOrganization(t, env.Pool)
	admin = testutil.CreateUser(t, env.Pool)
	role := testutil.CreateOrgRole(t, env.Pool, organizationID, "OrgSuperAdmin", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, OrganizationID: organizationID,
	})
	return organizationID, admin
}

// TestRolesHandler_OrgScoped_CreateListUpdateDelete is
// TestRolesHandler_CreateListUpdateDelete's organization-scoped counterpart
// (PLAN2.md Phase 2e) — before this phase, roleScope (then called
// roleExhibitionID) treated any role with a NULL exhibitionid as "not
// found," so an organization-scoped role could be created (via
// grantOrgAdmin) but never edited, permissioned, or deleted through this
// API. Exercises the full round trip using ?organizationid= in place of
// ?exhibitionid= throughout.
func TestRolesHandler_OrgScoped_CreateListUpdateDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID, admin := orgRolesAdmin(t, env)

	createBody, _ := json.Marshal(map[string]string{"name": "OrgCurator", "description": "Curates across the whole org"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/roles?organizationid="+organizationID, admin, "", bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created rolesAdminRole
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/roles?organizationid="+organizationID, admin, "", nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Roles []rolesAdminRole `json:"roles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: decode: %v", err)
	}
	found := false
	for _, role := range listResp.Roles {
		if role.RoleID == created.RoleID {
			found = true
		}
	}
	if !found {
		t.Fatalf("List: created org role not found in %+v", listResp.Roles)
	}

	params := httprouter.Params{{Key: "roleid", Value: created.RoleID}}

	addBody, _ := json.Marshal(map[string]string{"permission": permissions.PermGalleryView})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/roles/"+created.RoleID+"/permissions", admin, "", bytes.NewReader(addBody), params, h.AddPermission)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("AddPermission: status = %d, body = %s", rec.Code, rec.Body)
	}

	updateBody, _ := json.Marshal(map[string]string{"name": "Org Head Curator"})
	rec = doRequest(t, http.MethodPatch, "/api/v1/admin/roles/"+created.RoleID, admin, "", bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Update: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/roles?organizationid="+organizationID, admin, "", nil, nil, h.List)
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	var afterUpdate rolesAdminRole
	for _, role := range listResp.Roles {
		if role.RoleID == created.RoleID {
			afterUpdate = role
		}
	}
	if afterUpdate.Name != "Org Head Curator" || len(afterUpdate.Permissions) != 1 || afterUpdate.Permissions[0] != permissions.PermGalleryView {
		t.Fatalf("after Update/AddPermission: role = %+v, want Name='Org Head Curator' Permissions=[%s]", afterUpdate, permissions.PermGalleryView)
	}

	removeParams := httprouter.Params{{Key: "roleid", Value: created.RoleID}, {Key: "permission", Value: permissions.PermGalleryView}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/roles/"+created.RoleID+"/permissions/"+permissions.PermGalleryView, admin, "", nil, removeParams, h.RemovePermission)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("RemovePermission: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/roles/"+created.RoleID, admin, "", nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: status = %d, body = %s", rec.Code, rec.Body)
	}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/roles/"+created.RoleID, admin, "", nil, params, h.Delete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Delete (already deleted): status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestRolesHandler_OrgScoped_Create_RequiresOrgAdmin is the cross-organization
// isolation guard: an admin of organization A must not be able to create a
// role under organization B.
func TestRolesHandler_OrgScoped_Create_RequiresOrgAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	_, adminA := orgRolesAdmin(t, env)
	organizationB := testutil.CreateOrganization(t, env.Pool)

	body, _ := json.Marshal(map[string]string{"name": "Sneaky"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/roles?organizationid="+organizationB, adminA, "", bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (org A's admin must not create a role under org B)", rec.Code, http.StatusForbidden)
	}
}

// TestRolesHandler_OrgScoped_ExhibitionAdminWithinOrgAllowed mirrors
// TestGrantsHandler_ListForOrganization_ExhibitionAdminWithinOrgAllowed for
// roles: an admin of just one exhibition within an organization can still
// list that organization's roles — isOrgAdmin's third tier.
func TestRolesHandler_OrgScoped_ExhibitionAdminWithinOrgAllowed(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID := testutil.CreateOrganization(t, env.Pool)
	exhibitionID := testutil.CreateExhibitionInOrg(t, env.Pool, organizationID)
	exhibitionAdmin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "ExAdminForOrgRoles", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: exhibitionAdmin, ExhibitionID: exhibitionID,
	})

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/roles?organizationid="+organizationID, exhibitionAdmin, exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s (an exhibition admin within the org should see the org's roles)", rec.Code, rec.Body)
	}
}

// TestRolesHandler_OrgScoped_UpdateDelete_RequiresOrgAdmin covers Update/
// Delete/AddPermission/RemovePermission's roleScope-based check: an admin of
// a DIFFERENT organization must not be able to touch an existing org-scoped
// role, even knowing its roleid.
func TestRolesHandler_OrgScoped_UpdateDelete_RequiresOrgAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID, admin := orgRolesAdmin(t, env)
	roleID := testutil.CreateOrgRole(t, env.Pool, organizationID, "TargetOrgRole")
	t.Cleanup(func() {
		if _, err := env.Pool.Exec(context.Background(), `DELETE FROM roles WHERE roleid = $1`, roleID); err != nil {
			t.Errorf("cleanup: delete target org role: %v", err)
		}
	})
	_, otherAdmin := orgRolesAdmin(t, env)

	body, _ := json.Marshal(map[string]string{"name": "Hijacked"})
	params := httprouter.Params{{Key: "roleid", Value: roleID}}
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/roles/"+roleID, otherAdmin, "", bytes.NewReader(body), params, h.Update)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Update by other org's admin: status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// The role's own admin can still edit it, confirming the 403 above is
	// really about cross-org isolation and not a broken check.
	rec = doRequest(t, http.MethodPatch, "/api/v1/admin/roles/"+roleID, admin, "", bytes.NewReader(body), params, h.Update)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Update by the role's own org admin: status = %d, body = %s", rec.Code, rec.Body)
	}
}

// TestRolesHandler_List_RequiresExhibitionIdOrOrganizationId covers List's
// 400 path when neither scope is resolvable — same ordering guard as
// TestGrantsHandler_ListForExhibition_RequiresExhibitionID: an org-scoped
// admin grant (not tied to any one exhibition) is needed so the permission
// check itself doesn't 403 first.
func TestRolesHandler_List_RequiresExhibitionIdOrOrganizationId(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	admin := trueGlobalAdmin(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/roles", admin, "", nil, nil, h.List)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRolesHandler_PermissionCatalog(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.RolesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := rolesAdmin(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/permission-catalog?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.PermissionCatalog)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Groups []struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Groups) == 0 {
		t.Fatal("PermissionCatalog: got no groups")
	}
}
