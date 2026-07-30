package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/crypto/bcrypt"

	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// sessionCookieName mirrors auth.go's unexported sessionCookieName constant
// ("photoapp_session") — duplicated here since this is an external test
// package and can't reference it directly.
const sessionCookieName = "photoapp_session"

// doPublicRequest calls handle directly with no middleware wrapping at all —
// for AuthHandler endpoints (Register, Login, Config, Logout) that never
// read anything out of request context via the Auth/Exhibition middleware,
// so doRequest's chain would just be unused overhead, and — for Register in
// particular — doRequest doesn't allow setting the X-Forwarded-For header
// each of these tests needs to avoid tripping the shared, package-level,
// in-memory registration rate limiter (internal/handlers/ratelimit.go's
// regRecords map: every httptest.NewRequest defaults to the same
// RemoteAddr, so without a distinct X-Forwarded-For per test, the second
// Register call anywhere in this file would 429 instead of exercising
// whatever it's actually meant to test).
func doPublicRequest(t *testing.T, method, target string, body io.Reader, headers map[string]string, handle httprouter.Handle) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handle(rec, req, nil)
	return rec
}

// uniqueIP returns a fresh, never-reused IPv4 address to pass as
// X-Forwarded-For — see doPublicRequest's doc comment. Must be a real,
// parseable IP address, not just an opaque unique string: createSession
// stores it in sessions.ip_address, which is a Postgres `inet` column, so a
// non-IP value like a bare UUID fails with "invalid input syntax for type
// inet" once Register gets far enough to create a session.
var uniqueIPCounter atomic.Uint32

func uniqueIP() string {
	n := uniqueIPCounter.Add(1)
	return fmt.Sprintf("10.%d.%d.%d", byte(n>>16), byte(n>>8), byte(n))
}

// doMultipartRequest runs a multipart/form-data request through the same
// Auth+Exhibition middleware chain doRequest uses (so middleware.UserID
// works inside the handler), but — unlike doRequest — doesn't hardcode
// Content-Type to application/json, which would break multipart boundary
// parsing.
func doMultipartRequest(t *testing.T, userID, contentType string, body io.Reader, handle httprouter.Handle) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/profile/avatar", body)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	req.Header.Set("Content-Type", contentType)

	var final http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, nil)
	})
	final = middleware.Auth("X-User-ID", nil, nil)(final)
	lookup := func(_ context.Context, _ string) (string, bool) { return "", true }
	final = middleware.Exhibition(lookup, "")(final)

	rec := httptest.NewRecorder()
	final.ServeHTTP(rec, req)
	return rec
}

// createLocalUser inserts a local (email/password) user directly, bypassing
// Register, so Login-only tests (wrong password, disabled account, ...)
// don't depend on Register/the rate limiter at all.
func createLocalUser(t *testing.T, pool *db.Pool, password string) (userID, email string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("createLocalUser: hash password: %v", err)
	}
	email = "test-" + uuid.NewString() + "@example.invalid"
	err = pool.QueryRow(context.Background(), `
		INSERT INTO users (username, email, password_hash, provider)
		VALUES ($1, $2, $3, 'local')
		RETURNING userid::text
	`, "test-"+uuid.NewString(), email, string(hash)).Scan(&userID)
	if err != nil {
		t.Fatalf("createLocalUser: insert: %v", err)
	}
	return userID, email
}

// pngBytes returns a minimal valid 4x4 PNG, for tests exercising
// UploadProfileAvatar's content-type sniffing.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("pngBytes: encode: %v", err)
	}
	return buf.Bytes()
}

// ── Config ───────────────────────────────────────────────────────────────────

func TestAuthHandler_Config(t *testing.T) {
	h := &handlers.AuthHandler{Cfg: &config.Config{GoogleClientID: "g", FacebookClientID: "f"}}

	rec := doPublicRequest(t, http.MethodGet, "/auth/config", nil, nil, h.Config)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp["googleEnabled"] || !resp["facebookEnabled"] || resp["appleEnabled"] || resp["microsoftEnabled"] {
		t.Errorf("got %+v, want googleEnabled=true facebookEnabled=true appleEnabled=false microsoftEnabled=false", resp)
	}
}

// ── Register ─────────────────────────────────────────────────────────────────

func TestAuthHandler_Register_Success(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	email := "test-" + uuid.NewString() + "@example.invalid"
	body, _ := json.Marshal(map[string]string{"username": "reg-" + uuid.NewString(), "email": email, "password": "correct horse battery"})
	rec := doPublicRequest(t, http.MethodPost, "/auth/register", bytes.NewReader(body), map[string]string{"X-Forwarded-For": uniqueIP()}, h.Register)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		UserID string `json:"userid"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Email != email || resp.UserID == "" {
		t.Errorf("got %+v, want Email=%s and a non-empty UserID", resp, email)
	}
	if cookies := rec.Result().Cookies(); len(cookies) == 0 || cookies[0].Name != sessionCookieName {
		t.Errorf("Set-Cookie = %+v, want a %s cookie", rec.Result().Cookies(), sessionCookieName)
	}
}

func TestAuthHandler_Register_ValidationErrors(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	cases := []struct {
		name string
		body map[string]string
	}{
		{"missing email", map[string]string{"username": "u", "password": "correct horse battery"}},
		{"missing password", map[string]string{"username": "u", "email": "x@example.invalid"}},
		{"short password", map[string]string{"username": "u", "email": "x@example.invalid", "password": "short"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, _ := json.Marshal(c.body)
			rec := doPublicRequest(t, http.MethodPost, "/auth/register", bytes.NewReader(body), map[string]string{"X-Forwarded-For": uniqueIP()}, h.Register)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}
}

func TestAuthHandler_Register_DuplicateEmailRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	email := "test-" + uuid.NewString() + "@example.invalid"

	body, _ := json.Marshal(map[string]string{"username": "first-" + uuid.NewString(), "email": email, "password": "correct horse battery"})
	rec := doPublicRequest(t, http.MethodPost, "/auth/register", bytes.NewReader(body), map[string]string{"X-Forwarded-For": uniqueIP()}, h.Register)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first Register: status = %d, body = %s", rec.Code, rec.Body)
	}

	// Different X-Forwarded-For so this attempt isn't rejected by the rate
	// limiter before ever reaching the duplicate-email check.
	body2, _ := json.Marshal(map[string]string{"username": "second-" + uuid.NewString(), "email": email, "password": "correct horse battery"})
	rec = doPublicRequest(t, http.MethodPost, "/auth/register", bytes.NewReader(body2), map[string]string{"X-Forwarded-For": uniqueIP()}, h.Register)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second Register (duplicate email): status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body)
	}
}

func TestAuthHandler_Register_RateLimitedOnSecondAttemptFromSameIP(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	ip := uniqueIP()

	body, _ := json.Marshal(map[string]string{"username": "first-" + uuid.NewString(), "email": "test-" + uuid.NewString() + "@example.invalid", "password": "correct horse battery"})
	rec := doPublicRequest(t, http.MethodPost, "/auth/register", bytes.NewReader(body), map[string]string{"X-Forwarded-For": ip}, h.Register)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first Register: status = %d, body = %s", rec.Code, rec.Body)
	}

	body2, _ := json.Marshal(map[string]string{"username": "second-" + uuid.NewString(), "email": "test-" + uuid.NewString() + "@example.invalid", "password": "correct horse battery"})
	rec = doPublicRequest(t, http.MethodPost, "/auth/register", bytes.NewReader(body2), map[string]string{"X-Forwarded-For": ip}, h.Register)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second Register (same IP): status = %d, want %d, body = %s", rec.Code, http.StatusTooManyRequests, rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header not set on rate-limited response")
	}
}

// ── Login ────────────────────────────────────────────────────────────────────

func TestAuthHandler_Login_Success(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	_, email := createLocalUser(t, env.Pool, "correct horse battery")

	body, _ := json.Marshal(map[string]string{"email": email, "password": "correct horse battery"})
	rec := doPublicRequest(t, http.MethodPost, "/auth/login", bytes.NewReader(body), nil, h.Login)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if cookies := rec.Result().Cookies(); len(cookies) == 0 || cookies[0].Name != sessionCookieName {
		t.Errorf("Set-Cookie = %+v, want a %s cookie", rec.Result().Cookies(), sessionCookieName)
	}
}

func TestAuthHandler_Login_WrongPasswordRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	_, email := createLocalUser(t, env.Pool, "correct horse battery")

	body, _ := json.Marshal(map[string]string{"email": email, "password": "wrong password entirely"})
	rec := doPublicRequest(t, http.MethodPost, "/auth/login", bytes.NewReader(body), nil, h.Login)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthHandler_Login_UnknownEmailRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	body, _ := json.Marshal(map[string]string{"email": "nobody-" + uuid.NewString() + "@example.invalid", "password": "whatever12345"})
	rec := doPublicRequest(t, http.MethodPost, "/auth/login", bytes.NewReader(body), nil, h.Login)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthHandler_Login_DisabledAccountRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID, email := createLocalUser(t, env.Pool, "correct horse battery")
	if _, err := env.Pool.Exec(context.Background(), `UPDATE users SET account_enabled = FALSE WHERE userid = $1::uuid`, userID); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"email": email, "password": "correct horse battery"})
	rec := doPublicRequest(t, http.MethodPost, "/auth/login", bytes.NewReader(body), nil, h.Login)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// ── Me ───────────────────────────────────────────────────────────────────────

func TestAuthHandler_Me_LoggedOut(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	rec := doRequest(t, http.MethodGet, "/auth/me", "", "", nil, nil, h.Me)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["loggedIn"] != false {
		t.Errorf("loggedIn = %v, want false", resp["loggedIn"])
	}
}

func TestAuthHandler_Me_LoggedIn(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID := testutil.CreateUser(t, env.Pool)

	rec := doRequest(t, http.MethodGet, "/auth/me", userID, "", nil, nil, h.Me)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["loggedIn"] != true || resp["userid"] != userID {
		t.Errorf("got %+v, want loggedIn=true userid=%s", resp, userID)
	}
}

// ── Logout ───────────────────────────────────────────────────────────────────

func TestAuthHandler_Logout_RevokesSessionAndClearsCookie(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID := testutil.CreateUser(t, env.Pool)

	token := uuid.NewString()
	if _, err := env.Pool.Exec(context.Background(), `
		INSERT INTO sessions (userid, token_hash, expires_at) VALUES ($1::uuid, $2, NOW() + interval '1 day')
	`, userID, token); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	h.Logout(rec, req, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			found = true
			if c.MaxAge >= 0 {
				t.Errorf("cleared cookie MaxAge = %d, want negative", c.MaxAge)
			}
		}
	}
	if !found {
		t.Error("no cleared session cookie in response")
	}

	var revoked bool
	if err := env.Pool.QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM sessions WHERE token_hash = $1`, token,
	).Scan(&revoked); err != nil {
		t.Fatalf("query session: %v", err)
	}
	if !revoked {
		t.Error("session was not revoked")
	}
}

func TestAuthHandler_Logout_NoCookie_StillSucceeds(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rec := httptest.NewRecorder()
	h.Logout(rec, req, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

// ── UpdateProfile ────────────────────────────────────────────────────────────

func TestAuthHandler_UpdateProfile_Success(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID := testutil.CreateUser(t, env.Pool)

	body, _ := json.Marshal(map[string]string{"profileImage": "/avatars/some-preset.png"})
	rec := doRequest(t, http.MethodPatch, "/auth/profile", userID, "", bytes.NewReader(body), nil, h.UpdateProfile)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var stored string
	if err := env.Pool.QueryRow(context.Background(), `SELECT profile_image FROM users WHERE userid = $1::uuid`, userID).Scan(&stored); err != nil {
		t.Fatalf("query profile_image: %v", err)
	}
	if stored != "/avatars/some-preset.png" {
		t.Errorf("profile_image = %q, want %q", stored, "/avatars/some-preset.png")
	}
}

func TestAuthHandler_UpdateProfile_RejectsExternalURL(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID := testutil.CreateUser(t, env.Pool)

	body, _ := json.Marshal(map[string]string{"profileImage": "https://evil.example/x.png"})
	rec := doRequest(t, http.MethodPatch, "/auth/profile", userID, "", bytes.NewReader(body), nil, h.UpdateProfile)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestAuthHandler_UpdateProfile_RequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	body, _ := json.Marshal(map[string]string{"profileImage": "/avatars/x.png"})
	rec := doRequest(t, http.MethodPatch, "/auth/profile", "", "", bytes.NewReader(body), nil, h.UpdateProfile)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// ── UploadProfileAvatar ──────────────────────────────────────────────────────

func multipartImageBody(t *testing.T, imageBytes []byte) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = &bytes.Buffer{}
	w := multipart.NewWriter(body)
	part, err := w.CreateFormFile("image", "avatar.png")
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

func TestAuthHandler_UploadProfileAvatar_Success(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.UploadDir = t.TempDir()
	env.Cfg.UploadURLBase = "/uploads"
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID := testutil.CreateUser(t, env.Pool) // provider defaults to 'local'

	body, contentType := multipartImageBody(t, pngBytes(t))
	rec := doMultipartRequest(t, userID, contentType, body, h.UploadProfileAvatar)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		ProfileImage string `json:"profileImage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ProfileImage == "" {
		t.Error("ProfileImage was empty in response")
	}

	var stored string
	if err := env.Pool.QueryRow(context.Background(), `SELECT profile_image FROM users WHERE userid = $1::uuid`, userID).Scan(&stored); err != nil {
		t.Fatalf("query profile_image: %v", err)
	}
	if stored != resp.ProfileImage {
		t.Errorf("stored profile_image = %q, want %q (from response)", stored, resp.ProfileImage)
	}
}

func TestAuthHandler_UploadProfileAvatar_RejectsNonLocalAccount(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.UploadDir = t.TempDir()
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	var userID string
	if err := env.Pool.QueryRow(context.Background(), `
		INSERT INTO users (username, email, provider, google_id)
		VALUES ($1, $2, 'google', $3)
		RETURNING userid::text
	`, "oauth-"+uuid.NewString(), "test-"+uuid.NewString()+"@example.invalid", uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("seed google user: %v", err)
	}

	body, contentType := multipartImageBody(t, pngBytes(t))
	rec := doMultipartRequest(t, userID, contentType, body, h.UploadProfileAvatar)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAuthHandler_UploadProfileAvatar_RequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.UploadDir = t.TempDir()
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	body, contentType := multipartImageBody(t, pngBytes(t))
	rec := doMultipartRequest(t, "", contentType, body, h.UploadProfileAvatar)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthHandler_UploadProfileAvatar_RejectsNonImageContent(t *testing.T) {
	env := newTestEnv(t)
	env.Cfg.UploadDir = t.TempDir()
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID := testutil.CreateUser(t, env.Pool)

	body, contentType := multipartImageBody(t, []byte("not an image at all, just plain text bytes"))
	rec := doMultipartRequest(t, userID, contentType, body, h.UploadProfileAvatar)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// ── ListUsers (multi-login switcher) ────────────────────────────────────────

func TestAuthHandler_ListUsers_RequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}

	rec := doRequest(t, http.MethodGet, "/auth/users", "", "", nil, nil, h.ListUsers)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthHandler_ListUsers_RequiresMultiLoginEnabled(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID := testutil.CreateUser(t, env.Pool) // allow_multi_login defaults to FALSE

	rec := doRequest(t, http.MethodGet, "/auth/users", userID, "", nil, nil, h.ListUsers)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAuthHandler_ListUsers_Success(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AuthHandler{DB: env.Pool, Cfg: env.Cfg}
	userID := testutil.CreateUser(t, env.Pool)
	if _, err := env.Pool.Exec(context.Background(), `UPDATE users SET allow_multi_login = TRUE WHERE userid = $1::uuid`, userID); err != nil {
		t.Fatalf("enable multi-login: %v", err)
	}

	rec := doRequest(t, http.MethodGet, "/auth/users", userID, "", nil, nil, h.ListUsers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Users []struct {
			UserID string `json:"userid"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// ListUsers returns every non-deleted user in the whole database with no
	// scoping at all (by design — it's the account-switcher dropdown), so
	// this is a found-in-list check, not an exact count: every other test
	// that has ever created a user via testutil.CreateUser shows up here
	// too.
	found := false
	for _, u := range resp.Users {
		if u.UserID == userID {
			found = true
		}
	}
	if !found {
		t.Fatalf("caller's own userid %s not found in %d users", userID, len(resp.Users))
	}
}
