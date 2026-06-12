package handlers

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	_ "image/gif" // register GIF decoder
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder
)

// imgproxyClient is a shared HTTP client with a reasonable timeout.
var imgproxyClient = &http.Client{Timeout: 30 * time.Second}

// ImgProxyHandler proxies external http:// image URLs through the HTTPS app
// server to avoid mixed-content errors in the browser.
//
// GET /api/v1/imgproxy?url=http://...&w=<pixels>
//
// If w is provided (target display width in actual pixels, e.g. CSS width × DPR),
// and the source image is meaningfully wider than w, the image is downscaled
// using bilinear interpolation before delivery. Only JPEG and PNG sources are
// resized; other formats pass through unchanged.
//
// Security: only plain http:// URLs are proxied; https:// and anything else
// are rejected to prevent SSRF abuse.
type ImgProxyHandler struct{}

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

	resp, err := imgproxyClient.Get(target)
	if err != nil {
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")

	// Only attempt resize for JPEG/PNG when a target width is requested.
	if targetW > 0 && isResizeable(ct) {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "upstream read failed", http.StatusBadGateway)
			return
		}

		resized, newCT, originalW, err := resizeImage(body, ct, targetW)
		if err == nil && resized != nil {
			// Successfully resized.
			w.Header().Set("Content-Type", newCT)
			w.Header().Set("Content-Length", strconv.Itoa(len(resized)))
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Header().Set("X-Original-Width", strconv.Itoa(originalW))
			w.Header().Set("X-Resized-Width", strconv.Itoa(targetW))
			w.WriteHeader(resp.StatusCode)
			w.Write(resized) //nolint:errcheck
			return
		}
		// Resize failed (unsupported format, decode error, etc.) — fall back
		// to streaming the original body we already read.
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Original-Width", "unknown")
		w.WriteHeader(resp.StatusCode)
		w.Write(body) //nolint:errcheck
		return
	}

	// No resize requested — stream through as before.
	w.Header().Set("Content-Type", ct)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// isResizeable reports whether we know how to decode+resize this content type.
func isResizeable(ct string) bool {
	return strings.Contains(ct, "jpeg") ||
		strings.Contains(ct, "jpg") ||
		strings.Contains(ct, "png") ||
		strings.Contains(ct, "webp")
}

// resizeImage decodes src, downscales to targetW (preserving aspect ratio),
// and re-encodes as JPEG. Returns nil if the image is already small enough
// (≤ targetW * 1.2) or if decoding fails.
// Returns: encoded bytes, content-type, original width, error.
func resizeImage(src []byte, ct string, targetW int) ([]byte, string, int, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, "", 0, err
	}

	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	// Don't resize if already within 20% of target — not worth re-encoding.
	if srcW <= int(float64(targetW)*1.2) {
		return nil, "", srcW, fmt.Errorf("already small enough (%dpx)", srcW)
	}

	// Compute destination size preserving aspect ratio.
	dstH := srcH * targetW / srcW
	dst := image.NewRGBA(image.Rect(0, 0, targetW, dstH))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", srcW, err
	}
	return buf.Bytes(), "image/jpeg", srcW, nil
}
