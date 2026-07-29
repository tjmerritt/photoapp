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

func teamsAdmin(t *testing.T, env *testEnv) (exhibitionID, admin string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	admin = testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "TeamAdminRole", permissions.PermTeamAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	return exhibitionID, admin
}

func TestTeamsHandler_CreateListUpdateDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.TeamsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := teamsAdmin(t, env)

	createBody, _ := json.Marshal(map[string]string{"name": "Curators", "description": "Exhibition curators"})
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/teams?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		TeamID string `json:"teamid"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}
	if created.Name != "Curators" {
		t.Fatalf("Create: Name = %q, want Curators", created.Name)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/teams?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Teams []struct {
			TeamID      string `json:"teamid"`
			MemberCount int    `json:"member_count"`
		} `json:"teams"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	found := false
	for _, team := range listResp.Teams {
		if team.TeamID == created.TeamID {
			found = true
			if team.MemberCount != 0 {
				t.Errorf("MemberCount = %d, want 0 before any members are added", team.MemberCount)
			}
		}
	}
	if !found {
		t.Fatalf("List: created team not found in %+v", listResp.Teams)
	}

	params := httprouter.Params{{Key: "teamid", Value: created.TeamID}}
	updateBody, _ := json.Marshal(map[string]string{"description": "Updated description"})
	rec = doRequest(t, http.MethodPatch, "/api/v1/admin/teams/"+created.TeamID, admin, exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Update: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/teams/"+created.TeamID, admin, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestTeamsHandler_List_RequiresAdminOrTeamAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.TeamsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	plainUser := testutil.CreateUser(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/teams?exhibitionid="+exhibitionID, plainUser, exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestTeamsHandler_MembershipAddAndRemove(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.TeamsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := teamsAdmin(t, env)
	member := testutil.CreateUser(t, env.Pool)
	teamID := testutil.CreateTeam(t, env.Pool, exhibitionID)

	addParams := httprouter.Params{{Key: "teamid", Value: teamID}}
	rec := doRequest(t, http.MethodPost, "/api/v1/admin/teams/"+teamID+"/members?userid="+member, admin, exhibitionID, nil, addParams, h.AddMember)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("AddMember: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/teams/"+teamID+"/members", admin, exhibitionID, nil, addParams, h.ListMembers)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListMembers: status = %d, body = %s", rec.Code, rec.Body)
	}
	var membersResp struct {
		Members []struct {
			UserID string `json:"userid"`
		} `json:"members"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &membersResp)
	if len(membersResp.Members) != 1 || membersResp.Members[0].UserID != member {
		t.Fatalf("ListMembers: got %+v, want exactly [%s]", membersResp.Members, member)
	}

	removeParams := httprouter.Params{{Key: "teamid", Value: teamID}, {Key: "userid", Value: member}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/admin/teams/"+teamID+"/members/"+member, admin, exhibitionID, nil, removeParams, h.RemoveMember)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("RemoveMember: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/admin/teams/"+teamID+"/members", admin, exhibitionID, nil, addParams, h.ListMembers)
	_ = json.Unmarshal(rec.Body.Bytes(), &membersResp)
	if len(membersResp.Members) != 0 {
		t.Fatalf("ListMembers (after removal): got %+v, want none", membersResp.Members)
	}
}

func TestTeamsHandler_TeamNotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.TeamsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin := teamsAdmin(t, env)

	params := httprouter.Params{{Key: "teamid", Value: "00000000-0000-0000-0000-000000000000"}}
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/teams/x", admin, exhibitionID, bytes.NewReader([]byte(`{"name":"x"}`)), params, h.Update)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
