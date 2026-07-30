package handlers_test

import (
	"bytes"
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
