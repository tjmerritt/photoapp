package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/julienschmidt/httprouter"

	"github.com/tjmerritt/photoapp/internal/handlers"
)

func TestServeAvatar(t *testing.T) {
	hash := handlers.AvatarURL("someone@example.invalid")
	hash = strings.TrimPrefix(hash, "/avatars/")

	req := httptest.NewRequest(http.MethodGet, "/avatars/"+hash, nil)
	rec := httptest.NewRecorder()
	handlers.ServeAvatar(rec, req, httprouter.Params{{Key: "hash", Value: hash}})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want %q", ct, "image/svg+xml")
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want it to include %q", cc, "immutable")
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Errorf("body doesn't look like SVG: %s", rec.Body.String())
	}
}

func TestServeAvatar_Deterministic(t *testing.T) {
	hash := strings.TrimPrefix(handlers.AvatarURL("someone@example.invalid"), "/avatars/")

	req1 := httptest.NewRequest(http.MethodGet, "/avatars/"+hash, nil)
	rec1 := httptest.NewRecorder()
	handlers.ServeAvatar(rec1, req1, httprouter.Params{{Key: "hash", Value: hash}})

	req2 := httptest.NewRequest(http.MethodGet, "/avatars/"+hash, nil)
	rec2 := httptest.NewRecorder()
	handlers.ServeAvatar(rec2, req2, httprouter.Params{{Key: "hash", Value: hash}})

	if rec1.Body.String() != rec2.Body.String() {
		t.Error("ServeAvatar produced different SVGs for the same hash on two calls, want identical")
	}
}

func TestServeAvatar_EmptyHash_NotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/avatars/", nil)
	rec := httptest.NewRecorder()
	handlers.ServeAvatar(rec, req, httprouter.Params{{Key: "hash", Value: ""}})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestAvatarURL_SameEmailSameURL_CaseAndWhitespaceInsensitive(t *testing.T) {
	base := handlers.AvatarURL("Someone@Example.invalid")
	padded := handlers.AvatarURL("  someone@example.invalid  ")
	different := handlers.AvatarURL("someone-else@example.invalid")

	if base != padded {
		t.Errorf("AvatarURL differs by case/whitespace: %q vs %q, want equal", base, padded)
	}
	if base == different {
		t.Errorf("AvatarURL is the same for two different emails: %q", base)
	}
	if !strings.HasPrefix(base, "/avatars/") {
		t.Errorf("AvatarURL = %q, want a /avatars/ prefix", base)
	}
}
