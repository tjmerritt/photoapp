package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// galleriesFixture creates an exhibition with a Contributor role granting
// the full Gallery permission set to every logged-in user (mirrors
// scripts/seed-exhibition.sh) — an exhibition-scoped grant covers the whole
// Gallery→Display chain (see Checker.Check's doc comment), so no
// gallery-specific resource grant is needed for these handler-level tests;
// the resource-scoping nuances themselves are already covered by
// internal/permissions/permissions_test.go.
func galleriesFixture(t *testing.T, env *testEnv) (exhibitionID, user string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	user = testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "Contributor",
		permissions.PermGalleryView, permissions.PermGalleryCreate,
		permissions.PermGalleryModify, permissions.PermGalleryDelete)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ExhibitionID: exhibitionID,
	})
	return exhibitionID, user
}

func TestGalleriesHandler_CreateListGetUpdateDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GalleriesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, user := galleriesFixture(t, env)

	createBody, _ := json.Marshal(models.CreateGalleryRequest{Title: "Main Hall"})
	rec := doRequest(t, http.MethodPost, "/api/v1/galleries", user, exhibitionID, bytes.NewReader(createBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created models.GallerySummary
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}
	if created.Title != "Main Hall" || created.DisplayCount != 0 {
		t.Fatalf("Create: got %+v, want Title=Main Hall DisplayCount=0", created)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/galleries", user, exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp models.GalleriesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: decode: %v", err)
	}
	if len(listResp.Galleries) != 1 || listResp.Galleries[0].GalleryID != created.GalleryID {
		t.Fatalf("List: got %+v, want exactly the gallery just created", listResp.Galleries)
	}

	params := httprouter.Params{{Key: "galleryid", Value: created.GalleryID}}
	rec = doRequest(t, http.MethodGet, "/api/v1/galleries/"+created.GalleryID, user, exhibitionID, nil, params, h.Get)
	if rec.Code != http.StatusOK {
		t.Fatalf("Get: status = %d, body = %s", rec.Code, rec.Body)
	}
	var detail models.GalleryDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("Get: decode: %v", err)
	}
	if detail.Title != "Main Hall" || len(detail.Displays) != 0 {
		t.Fatalf("Get: got %+v, want Title=Main Hall and no displays yet", detail)
	}

	updateBody, _ := json.Marshal(models.UpdateGalleryRequest{Title: strPtr("West Wing")})
	rec = doRequest(t, http.MethodPatch, "/api/v1/galleries/"+created.GalleryID, user, exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("Update: status = %d, body = %s", rec.Code, rec.Body)
	}
	var updated models.GalleryDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Title != "West Wing" {
		t.Errorf("Update: Title = %q, want %q", updated.Title, "West Wing")
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/galleries/"+created.GalleryID, user, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/galleries/"+created.GalleryID, user, exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Delete (already deleted): status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGalleriesHandler_List_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GalleriesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)

	// No grant to Public/LoggedIn in this bare exhibition.
	rec := doRequest(t, http.MethodGet, "/api/v1/galleries", "", exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestGalleriesHandler_Create_RequiresTitle(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GalleriesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, user := galleriesFixture(t, env)

	body, _ := json.Marshal(models.CreateGalleryRequest{Title: ""})
	rec := doRequest(t, http.MethodPost, "/api/v1/galleries", user, exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGalleriesHandler_Get_NotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GalleriesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, user := galleriesFixture(t, env)

	params := httprouter.Params{{Key: "galleryid", Value: "00000000-0000-0000-0000-000000000000"}}
	rec := doRequest(t, http.MethodGet, "/api/v1/galleries/x", user, exhibitionID, nil, params, h.Get)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGalleriesHandler_Update_ReordersDisplays(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.GalleriesHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, user := galleriesFixture(t, env)

	galleryID := testutil.CreateGallery(t, env.Pool, exhibitionID)
	first := testutil.CreateDisplay(t, env.Pool, galleryID)
	second := testutil.CreateDisplay(t, env.Pool, galleryID)

	updateBody, _ := json.Marshal(models.UpdateGalleryRequest{DisplayOrder: []string{second, first}})
	params := httprouter.Params{{Key: "galleryid", Value: galleryID}}
	rec := doRequest(t, http.MethodPatch, "/api/v1/galleries/"+galleryID, user, exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("Update: status = %d, body = %s", rec.Code, rec.Body)
	}
	var detail models.GalleryDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if len(detail.Displays) != 2 || detail.Displays[0].DisplayID != second || detail.Displays[1].DisplayID != first {
		t.Fatalf("Update: displays = %+v, want [%s, %s] (second listed first per display_order)", detail.Displays, second, first)
	}
}
