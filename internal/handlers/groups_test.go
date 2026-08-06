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

// adminGroupEntry mirrors the unexported adminGroup shape groups.go's
// listing/create endpoints return.
type adminGroupEntry struct {
	GroupID        string `json:"groupid"`
	ResourceType   string `json:"resource_type"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	MemberCount    int    `json:"member_count"`
	ExhibitionID   string `json:"exhibitionid"`
	OrganizationID string `json:"organizationid"`
}

type adminGroupMemberEntry struct {
	ResourceRef string `json:"resource_ref"`
	DisplayName string `json:"display_name"`
	AddedAt     string `json:"added_at"`
}

// groupsAdmin creates an exhibition with a user granted PermAdmin — used for
// Photo/Gallery/Display group tests (exhibition-scoped).
func groupsAdmin(t *testing.T, env *testEnv) (exhibitionID, admin string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	admin = testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "GroupsAdmin", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	return exhibitionID, admin
}

func TestGroupsHandler_PhotoGroup_CreateListMembersDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := groupsAdmin(t, env)
	photoA := testutil.CreatePhoto(t, env.Pool, exhibitionID, admin)
	photoB := testutil.CreatePhoto(t, env.Pool, exhibitionID, admin)

	createBody, _ := json.Marshal(map[string]string{
		"resource_type": handlers.GroupResourcePhoto,
		"name":          "Featured",
		"description":   "Hand-picked favorites",
	})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created adminGroupEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}
	if created.ResourceType != handlers.GroupResourcePhoto || created.Name != "Featured" {
		t.Fatalf("Create: got %+v", created)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/groups?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Groups []adminGroupEntry `json:"groups"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	found := false
	for _, g := range listResp.Groups {
		if g.GroupID == created.GroupID {
			found = true
		}
	}
	if !found {
		t.Fatalf("List: created group not found in %+v", listResp.Groups)
	}

	params := httprouter.Params{{Key: "groupid", Value: created.GroupID}}

	addBody, _ := json.Marshal(map[string]string{"resource_ref": photoA})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, exhibitionID, bytes.NewReader(addBody), params, h.AddMember)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("AddMember(photoA): status = %d, body = %s", rec.Code, rec.Body)
	}
	addBody, _ = json.Marshal(map[string]string{"resource_ref": photoB})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, exhibitionID, bytes.NewReader(addBody), params, h.AddMember)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("AddMember(photoB): status = %d, body = %s", rec.Code, rec.Body)
	}
	// Re-adding the same member must be a harmless no-op (ON CONFLICT DO NOTHING).
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, exhibitionID, bytes.NewReader(addBody), params, h.AddMember)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("AddMember(photoB again): status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, exhibitionID, nil, params, h.ListMembers)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListMembers: status = %d, body = %s", rec.Code, rec.Body)
	}
	var membersResp struct {
		Members []adminGroupMemberEntry `json:"members"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &membersResp); err != nil {
		t.Fatalf("ListMembers: decode: %v", err)
	}
	if len(membersResp.Members) != 2 {
		t.Fatalf("ListMembers: got %d members, want 2: %+v", len(membersResp.Members), membersResp.Members)
	}

	removeParams := httprouter.Params{{Key: "groupid", Value: created.GroupID}, {Key: "resourceref", Value: photoA}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/groups/"+created.GroupID+"/members/"+photoA, admin, exhibitionID, nil, removeParams, h.RemoveMember)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("RemoveMember: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, exhibitionID, nil, params, h.ListMembers)
	_ = json.Unmarshal(rec.Body.Bytes(), &membersResp)
	if len(membersResp.Members) != 1 || membersResp.Members[0].ResourceRef != photoB {
		t.Fatalf("after RemoveMember: members = %+v, want just photoB", membersResp.Members)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/groups/"+created.GroupID, admin, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: status = %d, body = %s", rec.Code, rec.Body)
	}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/groups/"+created.GroupID, admin, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Delete (already deleted): status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGroupsHandler_AddMember_RejectsResourceOutsideScope(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := groupsAdmin(t, env)
	otherExhibitionID := testutil.CreateExhibition(t, env.Pool)
	outsidePhoto := testutil.CreatePhoto(t, env.Pool, otherExhibitionID, admin)

	createBody, _ := json.Marshal(map[string]string{"resource_type": handlers.GroupResourcePhoto, "name": "Scoped"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	var created adminGroupEntry
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	params := httprouter.Params{{Key: "groupid", Value: created.GroupID}}
	addBody, _ := json.Marshal(map[string]string{"resource_ref": outsidePhoto})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, exhibitionID, bytes.NewReader(addBody), params, h.AddMember)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (photo from a different exhibition must be rejected)", rec.Code, http.StatusBadRequest)
	}
}

func TestGroupsHandler_Create_ExhibitionType_RequiresOrganizationID(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := groupsAdmin(t, env)

	body, _ := json.Marshal(map[string]string{"resource_type": handlers.GroupResourceExhibition, "name": "Curated Exhibitions"})
	// Only exhibitionid supplied — Exhibition-type groups need organizationid instead.
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGroupsHandler_Create_NonExhibitionType_RequiresExhibitionID(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID, admin := orgRolesAdmin(t, env)

	body, _ := json.Marshal(map[string]string{"resource_type": handlers.GroupResourceGallery, "name": "Nope"})
	// Only organizationid supplied — Gallery-type groups need exhibitionid instead.
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?organizationid="+organizationID, admin, "", bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestGroupsHandler_ExhibitionTypeGroup_OrgScoped exercises the organization
// tier end to end: an Exhibition-type group lives under an organizationid,
// and its members are exhibitionids that must belong to that same
// organization (PLAN2.md Phase 3a's "Groups for Exhibitions").
func TestGroupsHandler_ExhibitionTypeGroup_OrgScoped(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID, admin := orgRolesAdmin(t, env)
	memberExhibition := testutil.CreateExhibitionInOrg(t, env.Pool, organizationID)
	outsideExhibition := testutil.CreateExhibition(t, env.Pool) // belongs to a different (default) organization

	createBody, _ := json.Marshal(map[string]string{"resource_type": handlers.GroupResourceExhibition, "name": "Touring Set"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?organizationid="+organizationID, admin, "", bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created adminGroupEntry
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.OrganizationID != organizationID {
		t.Fatalf("Create: OrganizationID = %q, want %q", created.OrganizationID, organizationID)
	}

	params := httprouter.Params{{Key: "groupid", Value: created.GroupID}}

	memberBody, _ := json.Marshal(map[string]string{"resource_ref": memberExhibition})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, "", bytes.NewReader(memberBody), params, h.AddMember)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("AddMember(in-org exhibition): status = %d, body = %s", rec.Code, rec.Body)
	}

	outsideBody, _ := json.Marshal(map[string]string{"resource_ref": outsideExhibition})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, "", bytes.NewReader(outsideBody), params, h.AddMember)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("AddMember(outside-org exhibition): status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, "", nil, params, h.ListMembers)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListMembers: status = %d, body = %s", rec.Code, rec.Body)
	}
	var membersResp struct {
		Members []adminGroupMemberEntry `json:"members"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &membersResp)
	if len(membersResp.Members) != 1 || membersResp.Members[0].ResourceRef != memberExhibition {
		t.Fatalf("ListMembers: got %+v, want just the in-org exhibition", membersResp.Members)
	}
}

// TestGroupsHandler_ExhibitionAdminCanManageOrgGroups confirms isOrgAdmin's
// third tier applies here too: an admin of just ONE exhibition inside an
// organization can still create/manage that organization's Exhibition-type
// groups — an exhibition admin already administers everything under their
// exhibition, so the org tier only ever adds reach, never narrows (see
// TestGrantsHandler_ListForOrganization_ExhibitionAdminWithinOrgAllowed,
// PLAN2.md Phase 2e, for the same guarantee applied to grants).
func TestGroupsHandler_ExhibitionAdminCanManageOrgGroups(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	organizationID := testutil.CreateOrganization(t, env.Pool)
	exhibitionID := testutil.CreateExhibitionInOrg(t, env.Pool, organizationID)
	exhibitionAdmin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "ExAdminForGroups", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: exhibitionAdmin, ExhibitionID: exhibitionID,
	})

	// isOrgAdmin's third tier: an admin of any one exhibition in the org can
	// still manage that org's groups — mirrors
	// TestGrantsHandler_ListForOrganization_ExhibitionAdminWithinOrgAllowed.
	body, _ := json.Marshal(map[string]string{"resource_type": handlers.GroupResourceExhibition, "name": "From Exhibition Admin"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?organizationid="+organizationID, exhibitionAdmin, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s (an exhibition admin within the org should be able to create the org's Exhibition-type groups)", rec.Code, rec.Body)
	}
}

func TestGroupsHandler_Create_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	plainUser := testutil.CreateUser(t, env.Pool)

	body, _ := json.Marshal(map[string]string{"resource_type": handlers.GroupResourceGallery, "name": "Nope"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?exhibitionid="+exhibitionID, plainUser, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestGroupsHandler_Create_InvalidResourceType(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := groupsAdmin(t, env)

	body, _ := json.Marshal(map[string]string{"resource_type": "Comment", "name": "Nope"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGroupsHandler_List_RequiresExhibitionIdOrOrganizationId(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	admin := trueGlobalAdmin(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/groups", admin, "", nil, nil, h.List)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGroupsHandler_GalleryGroup_MembershipRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GroupsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := groupsAdmin(t, env)
	galleryID := testutil.CreateGallery(t, env.Pool, exhibitionID)

	createBody, _ := json.Marshal(map[string]string{"resource_type": handlers.GroupResourceGallery, "name": "Wing A"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/groups?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	var created adminGroupEntry
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	params := httprouter.Params{{Key: "groupid", Value: created.GroupID}}
	addBody, _ := json.Marshal(map[string]string{"resource_ref": galleryID})
	rec = doRequest(t, http.MethodPost, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, exhibitionID, bytes.NewReader(addBody), params, h.AddMember)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("AddMember: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/groups/"+created.GroupID+"/members", admin, exhibitionID, nil, params, h.ListMembers)
	var membersResp struct {
		Members []adminGroupMemberEntry `json:"members"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &membersResp); err != nil {
		t.Fatalf("ListMembers: decode: %v", err)
	}
	if len(membersResp.Members) != 1 || membersResp.Members[0].DisplayName != "Test Gallery" {
		t.Fatalf("ListMembers: got %+v, want the gallery's title resolved", membersResp.Members)
	}
}
