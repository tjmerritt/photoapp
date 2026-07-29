package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── key helpers ──────────────────────────────────────────────────────────────

func TestHashHex_DeterministicAndDistinct(t *testing.T) {
	a := hashHex("https://example.invalid/a.jpg")
	b := hashHex("https://example.invalid/a.jpg")
	c := hashHex("https://example.invalid/b.jpg")
	if a != b {
		t.Error("hashHex is not deterministic for the same input")
	}
	if a == c {
		t.Error("hashHex produced the same hash for two different inputs")
	}
	if len(a) != 64 {
		t.Errorf("hashHex length = %d, want 64 (SHA-256 hex)", len(a))
	}
}

func TestImageCache_PathHelpers_ShardByFirstTwoHexChars(t *testing.T) {
	c := &ImageCache{dir: "/cache"}
	key := hashHex("https://example.invalid/a.jpg")
	shard := key[:2]

	if got := c.origPath(key); got != "/cache/orig/"+shard+"/"+key {
		t.Errorf("origPath = %q", got)
	}
	if got := c.origCTPath(key); got != "/cache/orig/"+shard+"/"+key+".ct" {
		t.Errorf("origCTPath = %q", got)
	}
	if got := c.scaledPath(key); got != "/cache/scaled/"+shard+"/"+key {
		t.Errorf("scaledPath = %q", got)
	}
}

// ── on-disk round trips ───────────────────────────────────────────────────────

func TestImageCache_OriginalRoundTrip(t *testing.T) {
	c, err := NewImageCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewImageCache: %v", err)
	}

	if _, _, ok := c.GetOriginal("https://example.invalid/miss.jpg"); ok {
		t.Fatal("GetOriginal reported a hit before anything was ever put")
	}

	url := "https://example.invalid/photo.jpg"
	want := []byte("fake jpeg bytes")
	if err := c.PutOriginal(url, want, "image/jpeg"); err != nil {
		t.Fatalf("PutOriginal: %v", err)
	}

	got, ct, ok := c.GetOriginal(url)
	if !ok {
		t.Fatal("GetOriginal missed right after PutOriginal")
	}
	if string(got) != string(want) {
		t.Errorf("GetOriginal data = %q, want %q", got, want)
	}
	if ct != "image/jpeg" {
		t.Errorf("GetOriginal content-type = %q, want %q", ct, "image/jpeg")
	}
}

func TestImageCache_ScaledRoundTrip(t *testing.T) {
	c, err := NewImageCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewImageCache: %v", err)
	}

	url := "https://example.invalid/photo.jpg"
	if _, ok := c.GetScaled(url, 400); ok {
		t.Fatal("GetScaled reported a hit before anything was ever put")
	}

	want := []byte("fake scaled jpeg")
	if err := c.PutScaled(url, 400, want); err != nil {
		t.Fatalf("PutScaled: %v", err)
	}

	got, ok := c.GetScaled(url, 400)
	if !ok || string(got) != string(want) {
		t.Errorf("GetScaled = (%q, %v), want (%q, true)", got, ok, want)
	}

	// A different target width is a different cache entry.
	if _, ok := c.GetScaled(url, 800); ok {
		t.Error("GetScaled(url, 800) hit a cache entry written for width 400")
	}
}

// ── fetch-or-download ─────────────────────────────────────────────────────────

func TestImageCache_FetchOriginal_DownloadsOnMissThenCaches(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("downloaded bytes"))
	}))
	defer srv.Close()

	c, err := NewImageCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewImageCache: %v", err)
	}

	data, ct, err := c.FetchOriginal(srv.URL)
	if err != nil {
		t.Fatalf("FetchOriginal: %v", err)
	}
	if string(data) != "downloaded bytes" {
		t.Errorf("data = %q", data)
	}
	if ct != "image/png" {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	if requests != 1 {
		t.Fatalf("upstream received %d requests, want 1", requests)
	}

	// Second call should be served from disk, not hit the upstream again.
	data2, _, err := c.FetchOriginal(srv.URL)
	if err != nil {
		t.Fatalf("FetchOriginal (cached): %v", err)
	}
	if string(data2) != "downloaded bytes" {
		t.Errorf("cached data = %q", data2)
	}
	if requests != 1 {
		t.Errorf("upstream received %d requests after a cache hit, want still 1", requests)
	}
}
