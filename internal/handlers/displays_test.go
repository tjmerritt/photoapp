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

// displaysFixture creates an exhibition with a Contributor role granting the
// full Display permission set to every logged-in user, plus one empty
// gallery to create displays in. Exhibition-scoped grants cover the whole
// Gallery→Display chain (Checker.Check's doc comment), so no gallery- or
// display-specific resource grant is needed here.
func displaysFixture(t *testing.T, env *testEnv) (exhibitionID, galleryID, user string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	galleryID = testutil.CreateGallery(t, env.Pool, exhibitionID)
	user = testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "Contributor",
		permissions.PermDisplayView, permissions.PermDisplayCreate,
		permissions.PermDisplayModify, permissions.PermDisplayDelete)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ExhibitionID: exhibitionID,
	})
	return exhibitionID, galleryID, user
}

func TestDisplaysHandler_CreateGetUpdateDelete(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.DisplaysHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, galleryID, user := displaysFixture(t, env)

	createBody, _ := json.Marshal(models.CreateDisplayRequest{})
	galleryParams := httprouter.Params{{Key: "galleryid", Value: galleryID}}
	rec := doRequest(t, http.MethodPost, "/api/v1/galleries/"+galleryID+"/displays", user, exhibitionID, bytes.NewReader(createBody), galleryParams, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created models.DisplayDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}
	if created.GalleryID != galleryID || len(created.Slots) != 0 {
		t.Fatalf("Create: got %+v, want GalleryID=%s and no slots (no template)", created, galleryID)
	}

	displayParams := httprouter.Params{{Key: "displayid", Value: created.DisplayID}}
	rec = doRequest(t, http.MethodGet, "/api/v1/displays/"+created.DisplayID, user, exhibitionID, nil, displayParams, h.Get)
	if rec.Code != http.StatusOK {
		t.Fatalf("Get: status = %d, body = %s", rec.Code, rec.Body)
	}
	var fetched models.DisplayDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("Get: decode: %v", err)
	}
	if fetched.DisplayID != created.DisplayID {
		t.Fatalf("Get: DisplayID = %q, want %q", fetched.DisplayID, created.DisplayID)
	}

	// Assign a photo into slot 0 via Update.
	photoOwner := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, photoOwner)
	updateBody, _ := json.Marshal(models.UpdateDisplayRequest{
		Slots: []models.SlotUpdate{{SlotIndex: 0, PhotoID: photoID}},
	})
	rec = doRequest(t, http.MethodPatch, "/api/v1/displays/"+created.DisplayID, user, exhibitionID, bytes.NewReader(updateBody), displayParams, h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("Update: status = %d, body = %s", rec.Code, rec.Body)
	}
	var updated models.DisplayDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if len(updated.Slots) != 1 || updated.Slots[0].Photo == nil || updated.Slots[0].Photo.PhotoID != photoID {
		t.Fatalf("Update: Slots = %+v, want one slot with photo %s", updated.Slots, photoID)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/displays/"+created.DisplayID, user, exhibitionID, nil, displayParams, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/displays/"+created.DisplayID, user, exhibitionID, nil, displayParams, h.Delete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Delete (already deleted): status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDisplaysHandler_Create_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.DisplaysHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	galleryID := testutil.CreateGallery(t, env.Pool, exhibitionID)
	plainUser := testutil.CreateUser(t, env.Pool)

	body, _ := json.Marshal(models.CreateDisplayRequest{})
	params := httprouter.Params{{Key: "galleryid", Value: galleryID}}
	rec := doRequest(t, http.MethodPost, "/api/v1/galleries/"+galleryID+"/displays", plainUser, exhibitionID, bytes.NewReader(body), params, h.Create)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestDisplaysHandler_Create_GalleryNotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.DisplaysHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, _, user := displaysFixture(t, env)

	body, _ := json.Marshal(models.CreateDisplayRequest{})
	params := httprouter.Params{{Key: "galleryid", Value: "00000000-0000-0000-0000-000000000000"}}
	rec := doRequest(t, http.MethodPost, "/api/v1/galleries/x/displays", user, exhibitionID, bytes.NewReader(body), params, h.Create)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDisplaysHandler_Get_NotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.DisplaysHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	params := httprouter.Params{{Key: "displayid", Value: "00000000-0000-0000-0000-000000000000"}}
	rec := doRequest(t, http.MethodGet, "/api/v1/displays/x", "", "", nil, params, h.Get)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
