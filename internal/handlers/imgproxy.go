package handlers

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/gif"  // register GIF decoder
	_ "image/png"  // register PNG decoder
	_ "image/gif" // register GIF decoder
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder
)

// ImgProxyHandler proxies external http:// image URLs through the HTTPS app
// server to avoid mixed-content errors in the browser.
//
// GET /api/v1/imgproxy?url=http://...&w=<pixels>
//
// Cache behaviour:
//  1. If w is given and a scaled variant is cached → serve it immediately.
//  2. Otherwise, fetch (or load) the original via ImageCache.FetchOriginal.
//  3. If w is given and the original is wide enough to benefit, downscale,
//     cache the result, and serve the scaled JPEG.
//  4. If no w, or resize is not possible, serve the original bytes.
//
// Security: only http:// URLs are accepted to prevent SSRF via the proxy.
type ImgProxyHandler struct {
	Cache *ImageCache
}

func (h *ImgProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	target := q.Get("url")
	if target == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(target, "http://") {
		http.Error(w, "only http:// URLs may be proxied", http.StatusBadRequest)
		return
	}

	targetW := 0
	if wStr := q.Get("w"); wStr != "" {
		targetW, _ = strconv.Atoi(wStr)
	}

	// ── 1. Scaled cache hit ───────────────────────────────────────────────────
	if targetW > 0 {
		if scaled, ok := h.Cache.GetScaled(target, targetW); ok {
			serveImage(w, scaled, "image/jpeg", "HIT-SCALED")
			return
		}
	}

	// ── 2. Fetch original (cache or upstream) ─────────────────────────────────
	origData, origCT, err := h.Cache.FetchOriginal(target)
	if err != nil {
		http.Error(w, "upstream fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// ── 3. Resize if requested and possible ───────────────────────────────────
	if targetW > 0 && isResizeable(origCT) {
		scaled, origW, err := resizeToWidth(origData, targetW)
		if err == nil && scaled != nil {
			_ = h.Cache.PutScaled(target, targetW, scaled)
			w.Header().Set("X-Original-Width", strconv.Itoa(origW))
			w.Header().Set("X-Resized-Width", strconv.Itoa(targetW))
			serveImage(w, scaled, "image/jpeg", "MISS-SCALED")
			return
		}
	}

	// ── 4. Serve original ─────────────────────────────────────────────────────
	serveImage(w, origData, origCT, "HIT-ORIG")
}

// serveImage writes image bytes with appropriate headers.
func serveImage(w http.ResponseWriter, data []byte, ct, cacheStatus string) {
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Cache", cacheStatus)
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// isResizeable reports whether we know how to decode+resize this content type.
func isResizeable(ct string) bool {
	return strings.Contains(ct, "jpeg") ||
		strings.Contains(ct, "jpg") ||
		strings.Contains(ct, "png") ||
		strings.Contains(ct, "webp")
}

// resizeToWidth decodes src, downscales to targetW preserving aspect ratio,
// and re-encodes as JPEG 85. Returns (nil, srcW, err) if the image is already
// small enough (≤ targetW × 1.2) — caller should serve the original instead.
func resizeToWidth(src []byte, targetW int) (scaled []byte, origW int, err error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, 0, err
	}

	bounds := img.Bounds()
	srcW   := bounds.Dx()
	srcH   := bounds.Dy()

	if srcW <= int(float64(targetW)*1.2) {
		return nil, srcW, fmt.Errorf("already small enough (%dpx)", srcW)
	}

	dstH := srcH * targetW / srcW
	dst  := image.NewRGBA(image.Rect(0, 0, targetW, dstH))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, srcW, err
	}
	return buf.Bytes(), srcW, nil
}
