package handlers

// This file is `package handlers` (not handlers_test) specifically to reach
// auth.go's unexported helpers (usernameFromName, uniqueUsername,
// finishLogin, findOrCreateOAuthUser) that auth_test.go's external test
// package can't call directly. LookupUserFlags/LookupSession are exported
// and already covered from the outside in auth_test.go's sibling tests, but
// are re-touched here only where a same-package test made more sense to
// group alongside these.
//
// Every DB-backed test below calls downloadExternalImage indirectly only
// through paths where picture=="" (findOrCreateOAuthUser's tests), so
// nothing here makes a real network call — downloadExternalImage itself
// (and its two OAuth-callback-only callers with a non-empty picture URL)
// stays untested, same as the rest of the OAuth provider surface, per the
// long-standing "needs a mocked external IdP" deferral.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// ── usernameFromName (pure) ──────────────────────────────────────────────────

func TestUsernameFromName(t *testing.T) {
	cases := []struct {
		name, email, want string
	}{
		{"Jane Doe", "", "jane_doe"},
		{"", "jane.doe@example.invalid", "jane.doe"},
		{"", "", "user"},
		{"Weird Name", "", "weird_name"}, // non-alnum -> '_'
		{"O'Brien", "fallback@example.invalid", "o_brien"},
	}
	for _, c := range cases {
		if got := usernameFromName(c.name, c.email); got != c.want {
			t.Errorf("usernameFromName(%q, %q) = %q, want %q", c.name, c.email, got, c.want)
		}
	}
}

// ── uniqueUsername ───────────────────────────────────────────────────────────

func TestUniqueUsername(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	ctx := context.Background()

	fresh := "unclaimed-" + uuid.NewString()
	if got := h.uniqueUsername(ctx, fresh); got != fresh {
		t.Errorf("uniqueUsername(%q) = %q, want the base itself unchanged when nothing collides", fresh, got)
	}

	taken := "taken-" + uuid.NewString()
	insertRawUser(t, pool, taken, "collide-"+uuid.NewString()+"@example.invalid")

	got := h.uniqueUsername(ctx, taken)
	if got == taken {
		t.Fatalf("uniqueUsername(%q) = %q, want a disambiguated suffix since %q is already taken", taken, got, taken)
	}
	if got != taken+"2" {
		t.Errorf("uniqueUsername(%q) = %q, want %q (first available numeric suffix)", taken, got, taken+"2")
	}

	// And the disambiguated name it just picked should itself be usable —
	// i.e. genuinely free, not just "different from the input".
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)`, got).Scan(&exists); err != nil {
		t.Fatalf("checking suggested username is free: %v", err)
	}
	if exists {
		t.Errorf("uniqueUsername returned %q, but that username is already taken too", got)
	}
}

// insertRawUser inserts a user with an explicit username/email, unlike
// testutil.CreateUser (which always generates its own random ones) —
// several tests here need to deliberately provoke a username collision.
func insertRawUser(t *testing.T, pool *db.Pool, username, email string) string {
	t.Helper()
	var userID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (username, email) VALUES ($1, $2)
		RETURNING userid::text
	`, username, email).Scan(&userID)
	if err != nil {
		t.Fatalf("insertRawUser: %v", err)
	}
	return userID
}

// ── LookupUserFlags / LookupSession ──────────────────────────────────────────

func TestLookupUserFlags(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	ctx := context.Background()
	userID := testutil.CreateUser(t, pool)

	var wantUsername string
	if err := pool.QueryRow(ctx, `SELECT username FROM users WHERE userid = $1::uuid`, userID).Scan(&wantUsername); err != nil {
		t.Fatalf("query expected username: %v", err)
	}

	flags := h.LookupUserFlags(ctx, userID)
	if flags.Username != wantUsername {
		t.Errorf("LookupUserFlags(%q).Username = %q, want %q", userID, flags.Username, wantUsername)
	}
}

func TestLookupUserFlags_DeletedUser_ReturnsZeroValue(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	ctx := context.Background()
	userID := testutil.CreateUser(t, pool)

	if _, err := pool.Exec(ctx, `UPDATE users SET deleted_at = NOW() WHERE userid = $1::uuid`, userID); err != nil {
		t.Fatalf("soft-delete user: %v", err)
	}

	flags := h.LookupUserFlags(ctx, userID)
	if flags.Username != "" {
		t.Errorf("LookupUserFlags for a soft-deleted user = %+v, want a zero-value UserFlags (query filters deleted_at IS NULL)", flags)
	}
}

func TestLookupSession(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	ctx := context.Background()
	userID := testutil.CreateUser(t, pool)

	seedSession := func(token string, revoked, expired bool) {
		t.Helper()
		expiresAt := "NOW() + interval '1 day'"
		if expired {
			expiresAt = "NOW() - interval '1 day'"
		}
		revokedClause := "NULL"
		if revoked {
			revokedClause = "NOW()"
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO sessions (userid, token_hash, expires_at, revoked_at)
			VALUES ($1::uuid, $2, `+expiresAt+`, `+revokedClause+`)
		`, userID, token)
		if err != nil {
			t.Fatalf("seed session: %v", err)
		}
	}

	valid := "valid-" + uuid.NewString()
	seedSession(valid, false, false)
	if got := h.LookupSession(ctx, valid); got != userID {
		t.Errorf("LookupSession(valid token) = %q, want %q", got, userID)
	}

	revoked := "revoked-" + uuid.NewString()
	seedSession(revoked, true, false)
	if got := h.LookupSession(ctx, revoked); got != "" {
		t.Errorf("LookupSession(revoked token) = %q, want \"\"", got)
	}

	expired := "expired-" + uuid.NewString()
	seedSession(expired, false, true)
	if got := h.LookupSession(ctx, expired); got != "" {
		t.Errorf("LookupSession(expired token) = %q, want \"\"", got)
	}

	if got := h.LookupSession(ctx, "no-such-token-"+uuid.NewString()); got != "" {
		t.Errorf("LookupSession(unknown token) = %q, want \"\"", got)
	}
}

func TestLookupSession_DisabledAccount(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	ctx := context.Background()
	userID := testutil.CreateUser(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE users SET account_enabled = FALSE WHERE userid = $1::uuid`, userID); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	token := "disabled-account-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (userid, token_hash, expires_at) VALUES ($1::uuid, $2, NOW() + interval '1 day')
	`, userID, token); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// LookupSession's doc comment: a disabled account's existing sessions
	// must stop authenticating immediately, without needing to hunt down
	// and revoke the session row itself.
	if got := h.LookupSession(ctx, token); got != "" {
		t.Errorf("LookupSession(token for disabled account) = %q, want \"\"", got)
	}
}

// ── finishLogin ───────────────────────────────────────────────────────────────

func TestFinishLogin(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	userID := testutil.CreateUser(t, pool)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/callback", nil)
	rec := httptest.NewRecorder()
	h.finishLogin(rec, req, userID)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q", loc, "/")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != sessionCookieName {
		t.Fatalf("cookies = %+v, want a %s cookie", cookies, sessionCookieName)
	}

	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM sessions WHERE userid = $1::uuid AND token_hash = $2
	`, userID, cookies[0].Value).Scan(&count); err != nil {
		t.Fatalf("query sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("sessions rows for the cookie's token = %d, want 1", count)
	}
}

// ── findOrCreateOAuthUser ─────────────────────────────────────────────────────

func TestFindOrCreateOAuthUser_CreatesNewUser(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	ctx := context.Background()

	sub := "google-sub-" + uuid.NewString()
	email := "oauth-" + uuid.NewString() + "@example.invalid"

	// picture == "" skips downloadExternalImage entirely — no network call,
	// falls back to AvatarURL(email) per findOrCreateOAuthUser's doc comment.
	userID, err := h.findOrCreateOAuthUser(ctx, "google", sub, email, "New User", "")
	if err != nil {
		t.Fatalf("findOrCreateOAuthUser: %v", err)
	}
	if userID == "" {
		t.Fatal("findOrCreateOAuthUser returned an empty userid")
	}

	var gotGoogleID, gotProvider, gotEmail string
	var gotProfileImage *string
	if err := pool.QueryRow(ctx, `
		SELECT google_id, provider, email, profile_image FROM users WHERE userid = $1::uuid
	`, userID).Scan(&gotGoogleID, &gotProvider, &gotEmail, &gotProfileImage); err != nil {
		t.Fatalf("query created user: %v", err)
	}
	if gotGoogleID != sub || gotProvider != "google" || gotEmail != email {
		t.Errorf("got google_id=%q provider=%q email=%q, want %q/%q/%q", gotGoogleID, gotProvider, gotEmail, sub, "google", email)
	}
	if gotProfileImage == nil || *gotProfileImage != AvatarURL(email) {
		t.Errorf("profile_image = %v, want the generated AvatarURL(%q) fallback since no picture was supplied", gotProfileImage, email)
	}
}

func TestFindOrCreateOAuthUser_SecondCallReturnsSameUser(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	ctx := context.Background()

	sub := "google-sub-" + uuid.NewString()
	email := "oauth-" + uuid.NewString() + "@example.invalid"

	first, err := h.findOrCreateOAuthUser(ctx, "google", sub, email, "New User", "")
	if err != nil {
		t.Fatalf("first findOrCreateOAuthUser: %v", err)
	}

	second, err := h.findOrCreateOAuthUser(ctx, "google", sub, email, "New User", "")
	if err != nil {
		t.Fatalf("second findOrCreateOAuthUser: %v", err)
	}
	if second != first {
		t.Errorf("second call returned a different userid (%q) than the first (%q), want the same existing user found by google_id", second, first)
	}
}

func TestFindOrCreateOAuthUser_LinksExistingLocalAccountByEmail(t *testing.T) {
	pool := testutil.RequireDB(t)
	h := &AuthHandler{DB: pool, Cfg: &config.Config{}}
	ctx := context.Background()

	email := "local-" + uuid.NewString() + "@example.invalid"
	localUserID := insertRawUser(t, pool, "local-"+uuid.NewString(), email)

	sub := "google-sub-" + uuid.NewString()
	linked, err := h.findOrCreateOAuthUser(ctx, "google", sub, email, "Existing User", "")
	if err != nil {
		t.Fatalf("findOrCreateOAuthUser: %v", err)
	}
	if linked != localUserID {
		t.Errorf("findOrCreateOAuthUser linked to userid %q, want it to reuse the existing local account %q with the same email", linked, localUserID)
	}

	var gotGoogleID string
	if err := pool.QueryRow(ctx, `SELECT google_id FROM users WHERE userid = $1::uuid`, localUserID).Scan(&gotGoogleID); err != nil {
		t.Fatalf("query linked user: %v", err)
	}
	if gotGoogleID != sub {
		t.Errorf("google_id = %q after linking, want %q", gotGoogleID, sub)
	}
}
