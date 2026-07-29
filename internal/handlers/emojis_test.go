package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

type emojiFixture struct {
	exhibitionID string
	photoID      string
	emojiID      string
	user         string
	other        string
}

// setupEmojiFixture grants PhotoEmojiView/Create/Delete to every logged-in
// user (mirrors the seeded Contributor role) and nothing to Public.
func setupEmojiFixture(t *testing.T, env *testEnv) emojiFixture {
	t.Helper()

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	user := testutil.CreateUser(t, env.Pool)
	other := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)
	emojiID := testutil.CreateEmojiType(t, env.Pool)

	contributor := testutil.CreateRole(t, env.Pool, exhibitionID, "Contributor",
		permissions.PermPhotoEmojiView, permissions.PermPhotoEmojiCreate, permissions.PermPhotoEmojiDelete)
	testutil.Grant(t, env.Pool, contributor, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ExhibitionID: exhibitionID,
	})

	return emojiFixture{exhibitionID: exhibitionID, photoID: photoID, emojiID: emojiID, user: user, other: other}
}

func TestEmojisHandler_ReactListUnreact(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupEmojiFixture(t, env)

	reactURL := "/api/v1/emoji/react?photoid=" + fx.photoID + "&emojiid=" + fx.emojiID

	rec := doRequest(t, http.MethodPost, reactURL, fx.user, fx.exhibitionID, nil, nil, h.React)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("React: status = %d, body = %s", rec.Code, rec.Body)
	}

	// Reacting again with the same user+emoji is idempotent (ON CONFLICT DO NOTHING).
	rec = doRequest(t, http.MethodPost, reactURL, fx.user, fx.exhibitionID, nil, nil, h.React)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("React (duplicate): status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/emojis?photoid="+fx.photoID, fx.user, fx.exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp models.EmojisResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: decode response: %v", err)
	}
	if len(listResp.Emojis) != 1 || listResp.Emojis[0].Count != 1 {
		t.Fatalf("List: got %+v, want exactly one emoji type with count 1", listResp.Emojis)
	}

	// Unreacting as a user who never reacted 404s.
	unreactURL := "/api/v1/emoji/react?photoid=" + fx.photoID + "&emojiid=" + fx.emojiID
	rec = doRequest(t, http.MethodDelete, unreactURL, fx.other, fx.exhibitionID, nil, nil, h.Unreact)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Unreact (never reacted): status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, unreactURL, fx.user, fx.exhibitionID, nil, nil, h.Unreact)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Unreact: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/emojis?photoid="+fx.photoID, fx.user, fx.exhibitionID, nil, nil, h.List)
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Emojis) != 0 {
		t.Fatalf("List (after unreact): got %+v, want no emojis", listResp.Emojis)
	}
}

func TestEmojisHandler_React_RespectsCanManageOwnEmojiOverride(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupEmojiFixture(t, env)

	// Phase 6b per-user override (migrations/017_admin_phase6.sql): an admin
	// can disable one user's ability to react even though the broad
	// Contributor grant still gives them PhotoEmojiCreate.
	if _, err := env.Pool.Exec(t.Context(), `
		UPDATE users SET can_manage_own_emoji = FALSE WHERE userid = $1::uuid
	`, fx.user); err != nil {
		t.Fatalf("disable can_manage_own_emoji: %v", err)
	}

	reactURL := "/api/v1/emoji/react?photoid=" + fx.photoID + "&emojiid=" + fx.emojiID
	rec := doRequest(t, http.MethodPost, reactURL, fx.user, fx.exhibitionID, nil, nil, h.React)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("React (disabled account): status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body)
	}
}

func TestEmojisHandler_React_InactiveEmojiTypeRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.EmojisHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupEmojiFixture(t, env)

	if _, err := env.Pool.Exec(t.Context(), `
		UPDATE emoji_types SET is_active = FALSE WHERE emojiid = $1::uuid
	`, fx.emojiID); err != nil {
		t.Fatalf("deactivate emoji type: %v", err)
	}

	reactURL := "/api/v1/emoji/react?photoid=" + fx.photoID + "&emojiid=" + fx.emojiID
	rec := doRequest(t, http.MethodPost, reactURL, fx.user, fx.exhibitionID, nil, nil, h.React)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("React (inactive emoji type): status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body)
	}
}
