package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"

	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// uniqueHexcode returns a fresh value to use as emoji_types.hexcode in
// tests. hexcode has no unique constraint (only emoji_char does, per
// migrations/004_emoji_unique.sql), so any distinct string works — using a
// UUID just guarantees no collision with rows other concurrent tests, or
// past runs against this shared, never-truncated database, may have
// inserted.
func uniqueHexcode() string { return uuid.NewString() }

// multipartEmojiUploadBody builds a multipart/form-data body matching what
// EmojisHandler.UploadType expects: an "image" file field plus an
// "alttext" field.
func multipartEmojiUploadBody(t *testing.T, imageBytes []byte, altText string) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = &bytes.Buffer{}
	w := multipart.NewWriter(body)
	if err := w.WriteField("alttext", altText); err != nil {
		t.Fatalf("write alttext field: %v", err)
	}
	part, err := w.CreateFormFile("image", "emoji.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(imageBytes); err != nil {
		t.Fatalf("write image bytes: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, w.FormDataContentType()
}

// doMultipartUploadRequest is like auth_test.go's doMultipartRequest, but
// generalized to any target path/handler and threading exhibitionID through
// too (auth.go's UploadProfileAvatar doesn't need an exhibition, but
// EmojisHandler.UploadType does, since PermEmojiUpload is checked against
// it).
func doMultipartUploadRequest(t *testing.T, target, userID, exhibitionID, contentType string, body io.Reader, handle httprouter.Handle) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, body)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	req.Header.Set("Content-Type", contentType)

	var final http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, nil)
	})
	final = middleware.Auth("X-User-ID", nil, nil)(final)
	lookup := func(_ context.Context, _ string) (string, bool) { return exhibitionID, true }
	final = middleware.Exhibition(lookup, "")(final)

	rec := httptest.NewRecorder()
	final.ServeHTTP(rec, req)
	return rec
}

// ── ListUsers (GET /api/v1/emoji/users) ─────────────────────────────────────

func TestEmojisHandler_ListUsers(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupEmojiFixture(t, env)

	reactURL := "/api/v1/emoji/react?photoid=" + fx.photoID + "&emojiid=" + fx.emojiID
	if rec := doRequest(t, http.MethodPost, reactURL, fx.user, fx.exhibitionID, nil, nil, h.React); rec.Code != http.StatusNoContent {
		t.Fatalf("setup React: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec := doRequest(t, http.MethodGet, "/api/v1/emoji/users?emoji="+fx.emojiID+"&photoid="+fx.photoID, fx.user, fx.exhibitionID, nil, nil, h.ListUsers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp models.EmojiUsersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Users) != 1 || resp.Users[0].ID != fx.user {
		t.Fatalf("Users = %+v, want exactly one user with ID %s", resp.Users, fx.user)
	}
}

func TestEmojisHandler_ListUsers_RequiresEmojiParam(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupEmojiFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/emoji/users?photoid="+fx.photoID, fx.user, fx.exhibitionID, nil, nil, h.ListUsers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// ── ListTypes (GET /api/v1/emoji/types, public) ─────────────────────────────

func TestEmojisHandler_ListTypes(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	emojiID := testutil.CreateEmojiType(t, env.Pool)

	// Public endpoint — no permission grant, no exhibition context needed.
	rec := doRequest(t, http.MethodGet, "/api/v1/emoji/types", "", "", nil, nil, h.ListTypes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Emojis []models.EmojiTypeResponse `json:"emojis"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, e := range resp.Emojis {
		if e.EmojiID == emojiID {
			found = true
		}
	}
	if !found {
		t.Fatalf("emojiid %s not found in %d types", emojiID, len(resp.Emojis))
	}
}

// ── AdminListTypes (GET /api/v1/admin/emoji-types) ──────────────────────────

func TestEmojisHandler_AdminListTypes(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	admin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "EmojiAdmin", permissions.PermEmojiAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	emojiID := testutil.CreateEmojiType(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/emoji-types", admin, exhibitionID, nil, nil, h.AdminListTypes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Emojis []models.EmojiTypeResponse `json:"emojis"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, e := range resp.Emojis {
		if e.EmojiID == emojiID {
			found = true
		}
	}
	if !found {
		t.Fatalf("emojiid %s not found in %d types", emojiID, len(resp.Emojis))
	}
}

func TestEmojisHandler_AdminListTypes_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	plain := testutil.CreateUser(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/emoji-types", plain, exhibitionID, nil, nil, h.AdminListTypes)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// ── AdminUpdateType (PATCH /api/v1/admin/emoji-types/:emojiid) ─────────────

func TestEmojisHandler_AdminUpdateType(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	admin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "EmojiAdmin2", permissions.PermEmojiAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	emojiID := testutil.CreateEmojiType(t, env.Pool)

	body, _ := json.Marshal(map[string]bool{"is_active": false})
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/emoji-types/"+emojiID, admin, exhibitionID, bytes.NewReader(body), httprouter.Params{{Key: "emojiid", Value: emojiID}}, h.AdminUpdateType)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var isActive bool
	if err := env.Pool.QueryRow(t.Context(), `SELECT is_active FROM emoji_types WHERE emojiid = $1::uuid`, emojiID).Scan(&isActive); err != nil {
		t.Fatalf("query is_active: %v", err)
	}
	if isActive {
		t.Error("is_active = true after AdminUpdateType(false), want false")
	}
}

func TestEmojisHandler_AdminUpdateType_RequiresIsActive(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	admin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "EmojiAdmin3", permissions.PermEmojiAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	emojiID := testutil.CreateEmojiType(t, env.Pool)

	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/emoji-types/"+emojiID, admin, exhibitionID, bytes.NewReader([]byte(`{}`)), httprouter.Params{{Key: "emojiid", Value: emojiID}}, h.AdminUpdateType)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestEmojisHandler_AdminUpdateType_NotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	admin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "EmojiAdmin4", permissions.PermEmojiAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})

	body, _ := json.Marshal(map[string]bool{"is_active": true})
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/emoji-types/00000000-0000-0000-0000-000000000000", admin, exhibitionID, bytes.NewReader(body), httprouter.Params{{Key: "emojiid", Value: "00000000-0000-0000-0000-000000000000"}}, h.AdminUpdateType)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body)
	}
}

func TestEmojisHandler_AdminUpdateType_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	plain := testutil.CreateUser(t, env.Pool)
	emojiID := testutil.CreateEmojiType(t, env.Pool)

	body, _ := json.Marshal(map[string]bool{"is_active": false})
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/emoji-types/"+emojiID, plain, exhibitionID, bytes.NewReader(body), httprouter.Params{{Key: "emojiid", Value: emojiID}}, h.AdminUpdateType)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// ── ListVariants (GET /api/v1/emoji/variants, public) ───────────────────────

func TestEmojisHandler_ListVariants(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	hexcode := uniqueHexcode()
	var baseID, variantID string
	if err := env.Pool.QueryRow(t.Context(), `
		INSERT INTO emoji_types (image_url, alt_text, is_active, hexcode)
		VALUES ($1, 'base emoji', TRUE, $2)
		RETURNING emojiid::text
	`, "https://example.invalid/"+hexcode+"-base.png", hexcode).Scan(&baseID); err != nil {
		t.Fatalf("seed base emoji: %v", err)
	}
	if err := env.Pool.QueryRow(t.Context(), `
		INSERT INTO emoji_types (image_url, alt_text, is_active, base_hexcode, skintone)
		VALUES ($1, 'variant emoji', TRUE, $2, 'dark')
		RETURNING emojiid::text
	`, "https://example.invalid/"+hexcode+"-variant.png", hexcode).Scan(&variantID); err != nil {
		t.Fatalf("seed variant emoji: %v", err)
	}

	rec := doRequest(t, http.MethodGet, "/api/v1/emoji/variants?hexcode="+hexcode, "", "", nil, nil, h.ListVariants)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Variants []models.EmojiTypeResponse `json:"variants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Variants) != 2 {
		t.Fatalf("Variants = %+v, want 2 (base + one skintone variant)", resp.Variants)
	}
	if resp.Variants[0].EmojiID != baseID {
		t.Errorf("Variants[0].EmojiID = %q, want base emoji %q first", resp.Variants[0].EmojiID, baseID)
	}
	if resp.Variants[1].EmojiID != variantID || resp.Variants[1].Skintone == nil || *resp.Variants[1].Skintone != "dark" {
		t.Errorf("Variants[1] = %+v, want variant %q with skintone \"dark\"", resp.Variants[1], variantID)
	}
}

func TestEmojisHandler_ListVariants_RequiresHexcode(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	rec := doRequest(t, http.MethodGet, "/api/v1/emoji/variants", "", "", nil, nil, h.ListVariants)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// ── UploadType (POST /api/v1/emoji/types, multipart) ────────────────────────

func TestEmojisHandler_UploadType(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.UploadDir = t.TempDir()
	env.Cfg.UploadURLBase = "/uploads"
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	uploader := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "EmojiUploader", permissions.PermEmojiUpload)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: uploader, ExhibitionID: exhibitionID,
	})

	body, contentType := multipartEmojiUploadBody(t, pngBytes(t), "a red square")
	rec := doMultipartUploadRequest(t, "/api/v1/emoji/types", uploader, exhibitionID, contentType, body, h.UploadType)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp models.EmojiTypeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AltText != "a red square" || resp.EmojiID == "" || !resp.IsActive {
		t.Errorf("got %+v, want AltText=%q IsActive=true and a non-empty EmojiID", resp, "a red square")
	}
}

func TestEmojisHandler_UploadType_RequiresAltText(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.UploadDir = t.TempDir()
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	uploader := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "EmojiUploader2", permissions.PermEmojiUpload)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: uploader, ExhibitionID: exhibitionID,
	})

	body, contentType := multipartEmojiUploadBody(t, pngBytes(t), "")
	rec := doMultipartUploadRequest(t, "/api/v1/emoji/types", uploader, exhibitionID, contentType, body, h.UploadType)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestEmojisHandler_UploadType_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.UploadDir = t.TempDir()
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	plain := testutil.CreateUser(t, env.Pool)

	body, contentType := multipartEmojiUploadBody(t, pngBytes(t), "a red square")
	rec := doMultipartUploadRequest(t, "/api/v1/emoji/types", plain, exhibitionID, contentType, body, h.UploadType)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
