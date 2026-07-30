package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// labelsFixture sets up one exhibition with a Contributor role (PhotoLabel
// View/Create/Modify/Delete granted to every logged-in user, mirroring
// scripts/seed-exhibition.sh) plus a separate LabelAdmin user, one photo, and
// two ordinary users. Nothing is granted to Public, so an unauthenticated
// caller holds no label permissions at all — that asymmetry is exercised
// directly by TestLabelsHandler_List_RequiresPermission.
type labelsFixture struct {
	exhibitionID string
	photoID      string
	owner        string
	other        string
	labelAdmin   string
}

func setupLabelsFixture(t *testing.T, env *testEnv) labelsFixture {
	t.Helper()

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	other := testutil.CreateUser(t, env.Pool)
	labelAdmin := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)

	contributor := testutil.CreateRole(t, env.Pool, exhibitionID, "Contributor",
		permissions.PermPhotoLabelView, permissions.PermPhotoLabelCreate,
		permissions.PermPhotoLabelModify, permissions.PermPhotoLabelDelete)
	testutil.Grant(t, env.Pool, contributor, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ExhibitionID: exhibitionID,
	})

	labelAdminRole := testutil.CreateRole(t, env.Pool, exhibitionID, "LabelAdminRole", permissions.PermLabelAdmin)
	testutil.Grant(t, env.Pool, labelAdminRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: labelAdmin, ExhibitionID: exhibitionID,
	})

	return labelsFixture{
		exhibitionID: exhibitionID,
		photoID:      photoID,
		owner:        owner,
		other:        other,
		labelAdmin:   labelAdmin,
	}
}

func TestLabelsHandler_CreateAndList(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	body, _ := json.Marshal(models.AddLabelRequest{Name: "Location", Value: "Yosemite"})
	rec := doRequest(t, http.MethodPost, "/api/v1/labels?photoid="+fx.photoID, fx.owner, fx.exhibitionID,
		bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created models.Label
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode response: %v", err)
	}
	if created.Name != "Location" || created.Value != "Yosemite" || created.UserID != fx.owner {
		t.Fatalf("Create: got %+v, want Name=Location Value=Yosemite UserID=%s", created, fx.owner)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/labels?photoid="+fx.photoID, fx.owner, fx.exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp models.LabelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: decode response: %v", err)
	}
	if len(listResp.Labels) != 1 || listResp.Labels[0].LabelID != created.LabelID {
		t.Fatalf("List: got %+v, want exactly the label just created", listResp.Labels)
	}
}

func TestLabelsHandler_List_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	// No grant to Public in this fixture, so an unauthenticated request must
	// be forbidden rather than silently returning an empty list.
	rec := doRequest(t, http.MethodGet, "/api/v1/labels?photoid="+fx.photoID, "", fx.exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("List (unauthenticated): status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body)
	}
}

func TestLabelsHandler_Update_OwnerAllowed_OthersForbidden_AdminAllowed(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	body, _ := json.Marshal(models.AddLabelRequest{Name: "Location", Value: "Yosemite"})
	rec := doRequest(t, http.MethodPost, "/api/v1/labels?photoid="+fx.photoID, fx.owner, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created models.Label
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	params := httprouter.Params{{Key: "labelid", Value: created.LabelID}}

	// A non-owner without LabelAdmin cannot edit someone else's label.
	updateBody, _ := json.Marshal(models.UpdateLabelRequest{Value: strPtr("Denali")})
	rec = doRequest(t, http.MethodPatch, "/api/v1/labels/"+created.LabelID, fx.other, fx.exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Update (non-owner): status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body)
	}

	// The owner can edit their own label.
	rec = doRequest(t, http.MethodPatch, "/api/v1/labels/"+created.LabelID, fx.owner, fx.exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("Update (owner): status = %d, body = %s", rec.Code, rec.Body)
	}
	var updated models.Label
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Value != "Denali" {
		t.Fatalf("Update (owner): Value = %q, want %q", updated.Value, "Denali")
	}

	// A LabelAdmin can edit someone else's label too.
	updateBody2, _ := json.Marshal(models.UpdateLabelRequest{Value: strPtr("Zion")})
	rec = doRequest(t, http.MethodPatch, "/api/v1/labels/"+created.LabelID, fx.labelAdmin, fx.exhibitionID, bytes.NewReader(updateBody2), params, h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("Update (LabelAdmin): status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestLabelsHandler_Delete_OwnerAllowed_OthersForbidden(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	body, _ := json.Marshal(models.AddLabelRequest{Name: "Location", Value: "Yosemite"})
	rec := doRequest(t, http.MethodPost, "/api/v1/labels?photoid="+fx.photoID, fx.owner, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	var created models.Label
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	params := httprouter.Params{{Key: "labelid", Value: created.LabelID}}

	rec = doRequest(t, http.MethodDelete, "/api/v1/labels/"+created.LabelID, fx.other, fx.exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Delete (non-owner): status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/labels/"+created.LabelID, fx.owner, fx.exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete (owner): status = %d, body = %s", rec.Code, rec.Body)
	}

	// Deleting again (already soft-deleted) must 404, not silently succeed.
	rec = doRequest(t, http.MethodDelete, "/api/v1/labels/"+created.LabelID, fx.owner, fx.exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Delete (already deleted): status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body)
	}
}

func TestLabelsHandler_Create_RestrictedName_RequiresLabelAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	// label_names is a global, app-wide catalog (name is its primary key),
	// not scoped to an exhibition — unlike every other fixture here, this
	// row has to be unique across every concurrently-running test on its
	// own, not via the exhibition it happens to be used from.
	restrictedName := "Sensitive-" + uuid.NewString()
	if _, err := env.Pool.Exec(t.Context(), `
		INSERT INTO label_names (name, restricted) VALUES ($1, TRUE)
	`, restrictedName); err != nil {
		t.Fatalf("seed restricted label_names row: %v", err)
	}

	body, _ := json.Marshal(models.AddLabelRequest{Name: restrictedName, Value: "yes"})

	// An ordinary contributor (has PhotoLabelCreate but not LabelAdmin) is forbidden.
	rec := doRequest(t, http.MethodPost, "/api/v1/labels?photoid="+fx.photoID, fx.owner, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Create (restricted, non-admin): status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body)
	}

	// A LabelAdmin may still add it.
	rec = doRequest(t, http.MethodPost, "/api/v1/labels?photoid="+fx.photoID, fx.labelAdmin, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create (restricted, LabelAdmin): status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body)
	}
}
