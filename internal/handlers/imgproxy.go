package handlers

import (
        "io"
        "net/http"
        "strings"
        "time"
)

// imgproxyClient is a shared HTTP client with a reasonable timeout.
var imgproxyClient = &http.Client{Timeout: 30 * time.Second}

// ImgProxyHandler proxies external http:// image URLs through the HTTPS app
// server to avoid mixed-content errors in the browser.
//
// GET /api/v1/imgproxy?url=http://...
//
// Security: only plain http:// URLs are proxied; https:// and anything else
// are rejected. This prevents the endpoint from being used as a general SSRF
// relay for non-image resources.
type ImgProxyHandler struct{}

func (h *ImgProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
        target := r.URL.Query().Get("url")
        if target == "" {
                http.Error(w, "url parameter required", http.StatusBadRequest)
                return
        }

        // Only proxy plain http:// URLs — reject anything else.
        if !strings.HasPrefix(target, "http://") {
                http.Error(w, "only http:// URLs may be proxied", http.StatusBadRequest)
                return
        }

        resp, err := imgproxyClient.Get(target)
        if err != nil {
                http.Error(w, "upstream fetch failed", http.StatusBadGateway)
                return
        }
        defer resp.Body.Close()

        // Forward content-type and cache headers from upstream.
        if ct := resp.Header.Get("Content-Type"); ct != "" {
                w.Header().Set("Content-Type", ct)
        }
        if cl := resp.Header.Get("Content-Length"); cl != "" {
                w.Header().Set("Content-Length", cl)
        }
        // Cache aggressively — these are static photo assets.
        w.Header().Set("Cache-Control", "public, max-age=86400")

        w.WriteHeader(resp.StatusCode)
        io.Copy(w, resp.Body) //nolint:errcheck
}
