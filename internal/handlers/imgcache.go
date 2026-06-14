package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sync/singleflight"
)

// imgFetchClient is the HTTP client used to download upstream images.
var imgFetchClient = &http.Client{Timeout: 30 * time.Second}

// ImageCache is a permanent disk-backed cache for proxied images.
//
// Layout under the root directory:
//
//	orig/<xx>/<hash>      — raw original bytes
//	orig/<xx>/<hash>.ct   — content-type of the original (plain text)
//	scaled/<xx>/<hash>    — re-encoded JPEG of a downscaled variant
//
// Keys are SHA-256 hashes; the first two hex chars are used as a shard
// directory to keep individual directories manageable.
//
// No TTL or eviction — images are assumed immutable at their source URLs.
type ImageCache struct {
	dir string
	sf  singleflight.Group
}

// NewImageCache creates (or opens) a cache rooted at dir.
func NewImageCache(dir string) (*ImageCache, error) {
	for _, sub := range []string{"orig", "scaled"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &ImageCache{dir: dir}, nil
}

// ── key helpers ──────────────────────────────────────────────────────────────

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (c *ImageCache) origPath(key string) string {
	return filepath.Join(c.dir, "orig", key[:2], key)
}

func (c *ImageCache) origCTPath(key string) string {
	return filepath.Join(c.dir, "orig", key[:2], key+".ct")
}

func (c *ImageCache) scaledPath(key string) string {
	return filepath.Join(c.dir, "scaled", key[:2], key)
}

// ── original cache ───────────────────────────────────────────────────────────

// GetOriginal returns the cached original for url, or ok=false on miss.
func (c *ImageCache) GetOriginal(url string) (data []byte, ct string, ok bool) {
	key := hashHex(url)
	data, err := os.ReadFile(c.origPath(key))
	if err != nil {
		return nil, "", false
	}
	ctBytes, err := os.ReadFile(c.origCTPath(key))
	if err != nil {
		return nil, "", false
	}
	return data, string(ctBytes), true
}

// PutOriginal writes the original image and its content-type to disk.
// Errors are non-fatal — the caller should still serve the data.
func (c *ImageCache) PutOriginal(url string, data []byte, ct string) error {
	key := hashHex(url)
	dir := filepath.Join(c.dir, "orig", key[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(c.origPath(key), data); err != nil {
		return err
	}
	return writeAtomic(c.origCTPath(key), []byte(ct))
}

// ── scaled cache ─────────────────────────────────────────────────────────────

// GetScaled returns the cached JPEG for (url, targetW), or ok=false on miss.
func (c *ImageCache) GetScaled(url string, targetW int) (data []byte, ok bool) {
	key := hashHex(url + ":w=" + strconv.Itoa(targetW))
	data, err := os.ReadFile(c.scaledPath(key))
	if err != nil {
		return nil, false
	}
	return data, true
}

// PutScaled writes a scaled JPEG for (url, targetW) to disk.
func (c *ImageCache) PutScaled(url string, targetW int, data []byte) error {
	key := hashHex(url + ":w=" + strconv.Itoa(targetW))
	dir := filepath.Join(c.dir, "scaled", key[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeAtomic(c.scaledPath(key), data)
}

// ── fetch-or-download ─────────────────────────────────────────────────────────

type origResult struct {
	data []byte
	ct   string
}

// FetchOriginal returns the original image for url, downloading and caching
// it if necessary. Concurrent calls for the same URL are coalesced via
// singleflight so the upstream is fetched at most once.
func (c *ImageCache) FetchOriginal(url string) ([]byte, string, error) {
	// Fast path: already cached.
	if data, ct, ok := c.GetOriginal(url); ok {
		return data, ct, nil
	}

	v, err, _ := c.sf.Do("orig:"+url, func() (interface{}, error) {
		// Re-check inside singleflight — another goroutine may have just cached it.
		if data, ct, ok := c.GetOriginal(url); ok {
			return &origResult{data, ct}, nil
		}

		resp, err := imgFetchClient.Get(url)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		ct := resp.Header.Get("Content-Type")
		_ = c.PutOriginal(url, data, ct) // best-effort; don't fail the request
		return &origResult{data, ct}, nil
	})
	if err != nil {
		return nil, "", err
	}
	r := v.(*origResult)
	return r.data, r.ct, nil
}

// ── atomic write ─────────────────────────────────────────────────────────────

// writeAtomic writes data to path via a temp file + rename, so a partial
// write never leaves a corrupt cache entry.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		// If the dir doesn't exist yet, that's a caller bug — propagate.
		return err
	}
	tmpName := tmp.Name()

	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(tmpName)
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
