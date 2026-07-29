package middleware_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjmerritt/photoapp/internal/middleware"
)

// echoHandler writes back what the middleware chain resolved into the
// context, so tests can assert on it without needing exported accessors for
// every unexported context key.
func echoHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID, authenticated := middleware.UserID(ctx)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"userID":        userID,
			"authenticated": authenticated,
			"username":      middleware.Username(ctx),
			"sessionID":     middleware.SessionID(ctx),
		})
	}
}

func decodeEcho(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestAuth_HeaderFallback_NoCookie(t *testing.T) {
	h := middleware.Auth("X-User-ID", nil, nil)(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-ID", "user-from-header")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := decodeEcho(t, rec)
	if got["userID"] != "user-from-header" {
		t.Errorf("userID = %v, want %q", got["userID"], "user-from-header")
	}
	if got["authenticated"] != true {
		t.Errorf("authenticated = %v, want true", got["authenticated"])
	}
}

func TestAuth_NoCredentials_PassesThroughUnauthenticated(t *testing.T) {
	h := middleware.Auth("X-User-ID", nil, nil)(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := decodeEcho(t, rec)
	if got["userID"] != "" {
		t.Errorf("userID = %v, want empty string", got["userID"])
	}
	if got["authenticated"] != false {
		t.Errorf("authenticated = %v, want false", got["authenticated"])
	}
}

func TestAuth_ValidCookie_PreferredOverHeader(t *testing.T) {
	sessionLookup := func(ctx context.Context, token string) string {
		if token == "valid-token" {
			return "user-from-cookie"
		}
		return ""
	}
	h := middleware.Auth("X-User-ID", sessionLookup, nil)(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-ID", "user-from-header")
	req.AddCookie(&http.Cookie{Name: "photoapp_session", Value: "valid-token"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := decodeEcho(t, rec)
	if got["userID"] != "user-from-cookie" {
		t.Errorf("userID = %v, want %q (cookie must win over header)", got["userID"], "user-from-cookie")
	}
	if got["sessionID"] != "valid-to" { // first 8 chars of "valid-token"
		t.Errorf("sessionID = %v, want %q", got["sessionID"], "valid-to")
	}
}

func TestAuth_InvalidCookie_FallsBackToHeader(t *testing.T) {
	sessionLookup := func(ctx context.Context, token string) string { return "" } // always invalid
	h := middleware.Auth("X-User-ID", sessionLookup, nil)(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-ID", "user-from-header")
	req.AddCookie(&http.Cookie{Name: "photoapp_session", Value: "bogus-token"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := decodeEcho(t, rec)
	if got["userID"] != "user-from-header" {
		t.Errorf("userID = %v, want %q (should fall back to header when the cookie doesn't resolve)", got["userID"], "user-from-header")
	}
	if got["sessionID"] != "" {
		t.Errorf("sessionID = %v, want empty (header fallback has no session id)", got["sessionID"])
	}
}

func TestAuth_FlagsLookup_SetsUsername(t *testing.T) {
	flagsLookup := func(ctx context.Context, userID string) middleware.UserFlags {
		return middleware.UserFlags{Username: "resolved-" + userID}
	}
	h := middleware.Auth("X-User-ID", nil, flagsLookup)(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-ID", "abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := decodeEcho(t, rec)
	if got["username"] != "resolved-abc" {
		t.Errorf("username = %v, want %q", got["username"], "resolved-abc")
	}
}

func TestRequireAuth_RejectsUnauthenticated(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := middleware.RequireAuth(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Error("inner handler was called for an unauthenticated request")
	}
}

func TestRequireAuth_AllowsAuthenticated(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	// Chain Auth (sets the context) -> RequireAuth (checks it), same as the router does.
	h := middleware.Auth("X-User-ID", nil, nil)(middleware.RequireAuth(inner))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-ID", "abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !called {
		t.Error("inner handler was not called for an authenticated request")
	}
}

func TestCORS_OptionsShortCircuits(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := middleware.CORS(inner)

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if called {
		t.Error("inner handler was called for an OPTIONS preflight request")
	}
}

func TestCORS_SetsHeadersAndCallsNext(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := middleware.CORS(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Error("inner handler was not called for a non-OPTIONS request")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
}

func TestRequestID_SetsResponseHeader(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := middleware.RequestID(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	id := rec.Header().Get("X-Request-ID")
	if id == "" {
		t.Error("X-Request-ID header not set")
	}
}

func TestWriteError_WritesStatusAndJSONBody(t *testing.T) {
	rec := httptest.NewRecorder()
	middleware.WriteError(rec, http.StatusForbidden, "nope")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != "nope" {
		t.Errorf("error = %q, want %q", body.Error, "nope")
	}
}

func TestWriteJSON_WritesStatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	middleware.WriteJSON(rec, http.StatusCreated, map[string]string{"hello": "world"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["hello"] != "world" {
		t.Errorf("body = %v, want {hello: world}", body)
	}
}

func TestMustUserID_PanicsWhenUnauthenticated(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustUserID did not panic for an unauthenticated context")
		}
	}()
	middleware.MustUserID(context.Background())
}

func TestMustUserID_ReturnsIDWhenAuthenticated(t *testing.T) {
	var gotID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = middleware.MustUserID(r.Context())
	})
	h := middleware.Auth("X-User-ID", nil, nil)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-ID", "user-123")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotID != "user-123" {
		t.Errorf("MustUserID = %q, want %q", gotID, "user-123")
	}
}

func TestExhibitionID_DefaultsToEmpty(t *testing.T) {
	if got := middleware.ExhibitionID(context.Background()); got != "" {
		t.Errorf("ExhibitionID = %q, want empty for a context with none set", got)
	}
}

func TestExhibition_KnownHostname_SetsExhibitionIDInContext(t *testing.T) {
	lookup := func(_ context.Context, hostname string) (string, bool) {
		if hostname == "gallery.example.com" {
			return "exhibition-42", true
		}
		return "", false
	}

	var gotExhibitionID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotExhibitionID = middleware.ExhibitionID(r.Context())
	})
	h := middleware.Exhibition(lookup, "")(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "gallery.example.com"
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotExhibitionID != "exhibition-42" {
		t.Errorf("ExhibitionID = %q, want %q", gotExhibitionID, "exhibition-42")
	}
}

func TestExhibition_UnknownHostname_ServesNewDomainPage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "newdomain.html"), []byte("<html>new domain</html>"), 0o644); err != nil {
		t.Fatalf("write newdomain.html fixture: %v", err)
	}

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	lookup := func(_ context.Context, _ string) (string, bool) { return "", false }
	h := middleware.Exhibition(lookup, dir)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "unregistered.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Error("inner handler was called for an unregistered hostname")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "new domain") {
		t.Errorf("body = %q, want the newdomain.html fixture content", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=60" {
		t.Errorf("Cache-Control = %q, want max-age=60", got)
	}
}

func TestExhibition_NilLookup_PassesThrough(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := middleware.Exhibition(nil, "")(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Error("inner handler was not called when lookup is nil")
	}
}

func TestLogger_LogsRequestDetailsAndPreservesResponse(t *testing.T) {
	var buf strings.Builder
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prevLogger)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("ok"))
	})
	h := middleware.Logger(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d (Logger must not alter the response)", rec.Code, http.StatusTeapot)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ok")
	}

	logged := buf.String()
	if !strings.Contains(logged, "GET") || !strings.Contains(logged, "/api/v1/thing") {
		t.Errorf("log line = %q, want it to mention the method and path", logged)
	}
	if !strings.Contains(logged, "418") {
		t.Errorf("log line = %q, want it to mention the response status (418)", logged)
	}
}
