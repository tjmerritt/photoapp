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

// templatesAdmin creates an exhibition with a single admin user (granted
// PermAdmin) — display_templates are global (not exhibition-scoped, see
// migrations/014_galleries_displays.sql), but Checker.Check still needs an
// exhibition to scope the grant to.
func templatesAdmin(t *testing.T, env *testEnv) (exhibitionID, admin string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	admin = testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "Admin", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	return exhibitionID, admin
}

func TestTemplatesHandler_CreateListUpdateDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.TemplatesHandler{DB: env.Pool, Checker: env.Checker}
	exhibitionID, admin := templatesAdmin(t, env)

	name := "Triptych-" + uuid.NewString()
	createBody, _ := json.Marshal(models.CreateTemplateRequest{Name: name, PhotoCount: 3})
	rec := doRequest(t, http.MethodPost, "/api/v1/display-templates", admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created models.DisplayTemplate
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}
	if created.Name != name || created.PhotoCount != 3 {
		t.Fatalf("Create: got %+v", created)
	}

	// List (no auth required).
	rec = doRequest(t, http.MethodGet, "/api/v1/display-templates", "", "", nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp models.TemplatesResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	found := false
	for _, tpl := range listResp.Templates {
		if tpl.TemplateID == created.TemplateID {
			found = true
		}
	}
	if !found {
		t.Fatalf("List: created template %s not found in %+v", created.TemplateID, listResp.Templates)
	}

	// Update.
	newCount := 4
	updateBody, _ := json.Marshal(models.UpdateTemplateRequest{PhotoCount: &newCount})
	params := httprouter.Params{{Key: "templateid", Value: created.TemplateID}}
	rec = doRequest(t, http.MethodPatch, "/api/v1/display-templates/"+created.TemplateID, admin, exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("Update: status = %d, body = %s", rec.Code, rec.Body)
	}
	var updated models.DisplayTemplate
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.PhotoCount != 4 {
		t.Errorf("Update: PhotoCount = %d, want 4", updated.PhotoCount)
	}

	// Delete.
	rec = doRequest(t, http.MethodDelete, "/api/v1/display-templates/"+created.TemplateID, admin, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/display-templates/"+created.TemplateID, admin, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Delete (already deleted): status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestTemplatesHandler_Create_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.TemplatesHandler{DB: env.Pool, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	plainUser := testutil.CreateUser(t, env.Pool)

	body, _ := json.Marshal(models.CreateTemplateRequest{Name: "Nope-" + uuid.NewString(), PhotoCount: 1})
	rec := doRequest(t, http.MethodPost, "/api/v1/display-templates", plainUser, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestTemplatesHandler_Create_RequiresPositivePhotoCount(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.TemplatesHandler{DB: env.Pool, Checker: env.Checker}
	exhibitionID, admin := templatesAdmin(t, env)

	body, _ := json.Marshal(models.CreateTemplateRequest{Name: "Zero-" + uuid.NewString(), PhotoCount: 0})
	rec := doRequest(t, http.MethodPost, "/api/v1/display-templates", admin, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
