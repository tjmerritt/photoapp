package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// ── PhotoHandler (GET /api/v1/photo) ─────────────────────────────────────────

func TestPhotoHandler_PublicPhoto_VisibleToAnonymous(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)
	testutil.MakePhotoPublic(t, env.Pool, photoID)

	rec := doRequest(t, http.MethodGet, "/api/v1/photo?photoid="+photoID, "", exhibitionID, nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var p models.Photo
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.PhotoID != photoID {
		t.Errorf("PhotoID = %q, want %q", p.PhotoID, photoID)
	}
}

func TestPhotoHandler_PrivatePhoto_HiddenFromAnonymous_VisibleToOwner(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	other := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner) // private by default

	rec := doRequest(t, http.MethodGet, "/api/v1/photo?photoid="+photoID, "", exhibitionID, nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous: status = %d, want %d (private photos are invisible, not forbidden)", rec.Code, http.StatusNotFound)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/photo?photoid="+photoID, other, exhibitionID, nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other user: status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/photo?photoid="+photoID, owner, exhibitionID, nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner: status = %d, body = %s, want %d (owner can always see their own upload)", rec.Code, rec.Body, http.StatusOK)
	}
}

func TestPhotoHandler_PrivatePhoto_VisibleWithPrivatePhotoViewGrant(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	viewer := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)

	role := testutil.CreateRole(t, env.Pool, exhibitionID, "PrivateViewer", permissions.PermPrivatePhotoView)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: viewer, ExhibitionID: exhibitionID,
	})

	rec := doRequest(t, http.MethodGet, "/api/v1/photo?photoid="+photoID, viewer, exhibitionID, nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestPhotoHandler_NotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/api/v1/photo?photoid=00000000-0000-0000-0000-000000000000", "", exhibitionID, nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestPhotoHandler_MissingPhotoID_BadRequest(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/api/v1/photo", "", exhibitionID, nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// ── ListPhotosHandler (GET /api/v1/photos) ───────────────────────────────────

func TestListPhotosHandler_OnlyPublicVisibleToAnonymous(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ListPhotosHandler{DB: env.Pool, Checker: env.Checker}

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	publicPhoto := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)
	testutil.MakePhotoPublic(t, env.Pool, publicPhoto)
	testutil.CreatePhoto(t, env.Pool, exhibitionID, owner) // private, should be excluded

	rec := doRequest(t, http.MethodGet, "/api/v1/photos", "", exhibitionID, nil, nil, h.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp models.PhotoListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || len(resp.Photos) != 1 || resp.Photos[0].PhotoID != publicPhoto {
		t.Fatalf("got %+v, want exactly the one public photo", resp)
	}
}

func TestListPhotosHandler_PrivateIncludedWithGrant(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ListPhotosHandler{DB: env.Pool, Checker: env.Checker}

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	viewer := testutil.CreateUser(t, env.Pool)
	testutil.CreatePhoto(t, env.Pool, exhibitionID, owner) // private

	role := testutil.CreateRole(t, env.Pool, exhibitionID, "PrivateViewer", permissions.PermPrivatePhotoView)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: viewer, ExhibitionID: exhibitionID,
	})

	rec := doRequest(t, http.MethodGet, "/api/v1/photos", viewer, exhibitionID, nil, nil, h.ServeHTTP)
	var resp models.PhotoListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("Total = %d, want 1 (viewer holds PrivatePhotoView)", resp.Total)
	}
}

// ── UserHandler (GET /api/v1/user) ───────────────────────────────────────────

func TestUserHandler_Found(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.UserHandler{DB: env.Pool}
	userID := testutil.CreateUser(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/api/v1/user?userid="+userID, "", "", nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var u models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if u.UserID != userID {
		t.Errorf("UserID = %q, want %q", u.UserID, userID)
	}
}

func TestUserHandler_NotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.UserHandler{DB: env.Pool}

	rec := doRequest(t, http.MethodGet, "/api/v1/user?userid=00000000-0000-0000-0000-000000000000", "", "", nil, nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// ── PatchPhotoHandler (PATCH /api/v1/photo) ──────────────────────────────────

func TestPatchPhotoHandler_OwnerCanEditTitle(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PatchPhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)

	body, _ := json.Marshal(models.UpdatePhotoTitleRequest{Title: "New Title"})
	rec := doRequest(t, http.MethodPatch, "/api/v1/photo?photoid="+photoID, owner, exhibitionID, bytes.NewReader(body), nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestPatchPhotoHandler_NonOwnerForbiddenWithoutPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PatchPhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	other := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)

	body, _ := json.Marshal(models.UpdatePhotoTitleRequest{Title: "Hijacked"})
	rec := doRequest(t, http.MethodPatch, "/api/v1/photo?photoid="+photoID, other, exhibitionID, bytes.NewReader(body), nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPatchPhotoHandler_NonOwnerAllowedWithPhotoDescriptionModify(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PatchPhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	editor := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)

	role := testutil.CreateRole(t, env.Pool, exhibitionID, "TitleEditor", permissions.PermPhotoDescriptionModify)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: editor, ExhibitionID: exhibitionID,
	})

	body, _ := json.Marshal(models.UpdatePhotoTitleRequest{Title: "Edited By Admin"})
	rec := doRequest(t, http.MethodPatch, "/api/v1/photo?photoid="+photoID, editor, exhibitionID, bytes.NewReader(body), nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestPatchPhotoHandler_Unauthenticated(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PatchPhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)

	body, _ := json.Marshal(models.UpdatePhotoTitleRequest{Title: "x"})
	rec := doRequest(t, http.MethodPatch, "/api/v1/photo?photoid="+photoID, "", exhibitionID, bytes.NewReader(body), nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestPatchPhotoHandler_EmptyTitleRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.PatchPhotoHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)

	body, _ := json.Marshal(models.UpdatePhotoTitleRequest{Title: "   "})
	rec := doRequest(t, http.MethodPatch, "/api/v1/photo?photoid="+photoID, owner, exhibitionID, bytes.NewReader(body), nil, adapt(h.ServeHTTP))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

