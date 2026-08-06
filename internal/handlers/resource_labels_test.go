package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// resourceLabelEntry mirrors the unexported resourceLabel shape
// resource_labels.go's List/Create endpoints return.
type resourceLabelEntry struct {
	LabelID    string `json:"labelid"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	Restricted bool   `json:"restricted"`
}

// resourceLabelsFixture creates an exhibition, a gallery + display within
// it, and a user granted full Gallery/Display view+modify — used for the
// happy-path Create/List/Delete round trips below.
func resourceLabelsFixture(t *testing.T, env *testEnv) (exhibitionID, galleryID, displayID, user string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	galleryID = testutil.CreateGallery(t, env.Pool, exhibitionID)
	displayID = testutil.CreateDisplay(t, env.Pool, galleryID)
	user = testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "GalleryDisplayEditor",
		permissions.PermGalleryView, permissions.PermGalleryModify,
		permissions.PermDisplayView, permissions.PermDisplayModify)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: user, ExhibitionID: exhibitionID,
	})
	return exhibitionID, galleryID, displayID, user
}

func TestResourceLabelsHandler_Gallery_CreateListDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ResourceLabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, galleryID, _, user := resourceLabelsFixture(t, env)

	createBody, _ := json.Marshal(map[string]string{"name": "Featured", "value": "yes"})
	url := "/api/v1/resource-labels?resource_type=" + handlers.GroupResourceGallery + "&resource_ref=" + galleryID
	rec := doRequest(t, http.MethodPost, url, user, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		LabelID string `json:"labelid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}

	rec = doRequest(t, http.MethodGet, url, user, exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp struct {
		Labels []resourceLabelEntry `json:"labels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: decode: %v", err)
	}
	if len(listResp.Labels) != 1 || listResp.Labels[0].Name != "Featured" || listResp.Labels[0].Value != "yes" {
		t.Fatalf("List: got %+v, want one Featured=yes label", listResp.Labels)
	}

	params := httprouter.Params{{Key: "labelid", Value: created.LabelID}}
	rec = doRequest(t, http.MethodDelete, "/api/v1/resource-labels/"+created.LabelID, user, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, url, user, exhibitionID, nil, nil, h.List)
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Labels) != 0 {
		t.Fatalf("List after Delete: got %+v, want none", listResp.Labels)
	}
}

func TestResourceLabelsHandler_Display_CreateListDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ResourceLabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, _, displayID, user := resourceLabelsFixture(t, env)

	createBody, _ := json.Marshal(map[string]string{"name": "Needs Repair", "value": "true"})
	url := "/api/v1/resource-labels?resource_type=" + handlers.GroupResourceDisplay + "&resource_ref=" + displayID
	rec := doRequest(t, http.MethodPost, url, user, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, url, user, exhibitionID, nil, nil, h.List)
	var listResp struct {
		Labels []resourceLabelEntry `json:"labels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: decode: %v", err)
	}
	if len(listResp.Labels) != 1 || listResp.Labels[0].Name != "Needs Repair" {
		t.Fatalf("List: got %+v", listResp.Labels)
	}
}

func TestResourceLabelsHandler_Exhibition_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ResourceLabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, _, _, user := resourceLabelsFixture(t, env) // Gallery/Display perms only, not PermAdmin
	admin := testutil.CreateUser(t, env.Pool)
	adminRole := testutil.CreateRole(t, env.Pool, exhibitionID, "ExLabelAdmin", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, adminRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})

	url := "/api/v1/resource-labels?resource_type=" + handlers.GroupResourceExhibition + "&resource_ref=" + exhibitionID
	createBody, _ := json.Marshal(map[string]string{"name": "Archived", "value": "true"})

	rec := doRequest(t, http.MethodPost, url, user, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Create as non-admin: status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	rec = doRequest(t, http.MethodPost, url, admin, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create as admin: status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestResourceLabelsHandler_Create_RequiresModifyNotJustView(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ResourceLabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	galleryID := testutil.CreateGallery(t, env.Pool, exhibitionID)
	viewOnlyUser := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "GalleryViewer", permissions.PermGalleryView)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: viewOnlyUser, ExhibitionID: exhibitionID,
	})

	url := "/api/v1/resource-labels?resource_type=" + handlers.GroupResourceGallery + "&resource_ref=" + galleryID

	rec := doRequest(t, http.MethodGet, url, viewOnlyUser, exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List with only GalleryView: status = %d, want %d", rec.Code, http.StatusOK)
	}

	body, _ := json.Marshal(map[string]string{"name": "X", "value": "y"})
	rec = doRequest(t, http.MethodPost, url, viewOnlyUser, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Create with only GalleryView: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestResourceLabelsHandler_Create_ResourceNotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ResourceLabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	_, _, _, user := resourceLabelsFixture(t, env)

	body, _ := json.Marshal(map[string]string{"name": "X", "value": "y"})
	url := "/api/v1/resource-labels?resource_type=" + handlers.GroupResourceGallery + "&resource_ref=" + uuid.NewString()
	rec := doRequest(t, http.MethodPost, url, user, "", bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestResourceLabelsHandler_Create_RestrictedName_RequiresLabelAdmin mirrors
// labels_test.go's photo-label equivalent — resource_labels reuses the same
// exhibition-scoped label_names catalog (color/restricted/enabled), so a
// name marked restricted there must be enforced identically for a Gallery
// label as it already is for a Photo one.
func TestResourceLabelsHandler_Create_RestrictedName_RequiresLabelAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ResourceLabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, galleryID, _, user := resourceLabelsFixture(t, env)
	labelAdmin := testutil.CreateUser(t, env.Pool)
	labelAdminRole := testutil.CreateRole(t, env.Pool, exhibitionID, "GalleryLabelAdmin",
		permissions.PermGalleryView, permissions.PermGalleryModify, permissions.PermLabelAdmin)
	testutil.Grant(t, env.Pool, labelAdminRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: labelAdmin, ExhibitionID: exhibitionID,
	})

	restrictedName := "Sensitive-" + uuid.NewString()
	if _, err := env.Pool.Exec(t.Context(), `
		INSERT INTO label_names (exhibitionid, name, restricted) VALUES ($1, $2, TRUE)
	`, exhibitionID, restrictedName); err != nil {
		t.Fatalf("seed restricted label_names row: %v", err)
	}

	url := "/api/v1/resource-labels?resource_type=" + handlers.GroupResourceGallery + "&resource_ref=" + galleryID
	body, _ := json.Marshal(map[string]string{"name": restrictedName, "value": "yes"})

	rec := doRequest(t, http.MethodPost, url, user, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Create (restricted, non-LabelAdmin): status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	rec = doRequest(t, http.MethodPost, url, labelAdmin, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create (restricted, LabelAdmin): status = %d, body = %s", rec.Code, rec.Body)
	}
}
