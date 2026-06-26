package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ── Context keys ──────────────────────────────────────────────────────────────

type ctxKey string

const (
	ctxUserID              ctxKey = "userid"
	ctxRequestID           ctxKey = "requestid"
	ctxExhibitionID        ctxKey = "exhibitionid"
	ctxAuthorizedNonPublic ctxKey = "authorized_non_public"
	ctxUsername            ctxKey = "username"
	ctxSessionID           ctxKey = "sessionid"
)

// ExhibitionID retrieves the current exhibition ID from the context.
// Returns "" when no exhibition was resolved for this request.
func ExhibitionID(ctx context.Context) string {
	v, _ := ctx.Value(ctxExhibitionID).(string)
	return v
}

// ExhibitionLookup resolves an exhibition ID from a hostname.
// Returns (id, true) when the hostname is registered, ("", false) when not.
type ExhibitionLookup func(ctx context.Context, hostname string) (string, bool)

// Exhibition extracts the Host header and looks up the corresponding
// exhibitionid. Tries the full host:port first, then host only.
// When the hostname is not registered in exhibition_hostnames, every request
// (including API calls) receives the contents of appDir/newdomain.html with
// Cache-Control: max-age=60. The file is read once and cached in memory.
func Exhibition(lookup ExhibitionLookup, appDir string) func(http.Handler) http.Handler {
	var (
		fileMu   sync.Mutex
		body     []byte
		readErr  error
		loadedAt time.Time
	)
	load := func() {
		fileMu.Lock()
		defer fileMu.Unlock()
		if time.Since(loadedAt) > 60*time.Second {
			body, readErr = os.ReadFile(filepath.Join(appDir, "newdomain.html"))
			loadedAt = time.Now()
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if lookup != nil {
				hostport := r.Host
				id, known := lookup(r.Context(), hostport)
				if !known {
					// Strip port and try bare hostname.
					if host, _, err := net.SplitHostPort(hostport); err == nil {
						id, known = lookup(r.Context(), host)
					}
				}
				if !known {
					load()
					if readErr != nil {
						http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
						return
					}
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.Header().Set("Cache-Control", "max-age=60")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(body)
					return
				}
				if id != "" {
					r = r.WithContext(context.WithValue(r.Context(), ctxExhibitionID, id))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Username retrieves the authenticated username from the request context.
// Returns "" for unauthenticated requests.
func Username(ctx context.Context) string {
	v, _ := ctx.Value(ctxUsername).(string)
	return v
}

// SessionID retrieves the short session identifier from the request context.
// Returns "" for unauthenticated requests or test-user (header) auth.
func SessionID(ctx context.Context) string {
	v, _ := ctx.Value(ctxSessionID).(string)
	return v
}

// UserID retrieves the authenticated user ID from the request context.
// Returns ("", false) when no user is present (unauthenticated request).
func UserID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxUserID).(string)
	return v, ok && v != ""
}

// AuthorizedNonPublic reports whether the current user is allowed to see
// non-public photos. Returns false for unauthenticated requests and for users
// whose authorized_non_public flag is not set.
func AuthorizedNonPublic(ctx context.Context) bool {
	v, _ := ctx.Value(ctxAuthorizedNonPublic).(bool)
	return v
}

// MustUserID retrieves the user ID and panics if missing.
// Only use in handlers that are protected by the Auth middleware.
func MustUserID(ctx context.Context) string {
	id, ok := UserID(ctx)
	if !ok {
		panic("MustUserID called on unauthenticated context")
	}
	return id
}

// ── Error helper ──────────────────────────────────────────────────────────────

type apiError struct {
	Error string `json:"error"`
}

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Error: msg})
}

// WriteJSON writes a JSON success response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ── Middleware chain ──────────────────────────────────────────────────────────

// RequestID attaches a UUID request ID to the context and response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.New().String()
		ctx := context.WithValue(r.Context(), ctxRequestID, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Logger logs method, path, status, latency, IP, host, username, and session
// for every request. It runs inside Auth so all context values are available.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		ctx := r.Context()

		// Client IP: prefer X-Forwarded-For (first entry), fall back to RemoteAddr.
		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				ip = h
			} else {
				ip = r.RemoteAddr
			}
		} else if idx := strings.IndexByte(ip, ','); idx != -1 {
			ip = strings.TrimSpace(ip[:idx])
		}

		attrs := []any{
			"method",      r.Method,
			"path",        r.URL.RequestURI(),
			"status",      rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"ip",          ip,
			"host",        r.Host,
		}
		if rid, ok := ctx.Value(ctxRequestID).(string); ok && rid != "" {
			attrs = append(attrs, "request_id", rid)
		}
		if user := Username(ctx); user != "" {
			attrs = append(attrs, "user", user)
		} else if uid, ok := UserID(ctx); ok {
			attrs = append(attrs, "user", uid) // fall back to userid when username not loaded
		}
		if sid := SessionID(ctx); sid != "" {
			attrs = append(attrs, "session", sid)
		}

		slog.Info("request", attrs...)
	})
}

// SessionLookup is a function that resolves a session token to a user ID.
// Returning "" means the session is invalid or not found.
type SessionLookup func(ctx context.Context, token string) string

// UserFlagsLookup fetches per-user permission flags for the given user ID.
// It is called once per request after the user is resolved.
type UserFlagsLookup func(ctx context.Context, userID string) UserFlags

// UserFlags holds per-user data loaded once per request after auth resolves.
type UserFlags struct {
	AuthorizedNonPublic bool
	Username            string
}

// Auth resolves the acting user from:
//  1. The session cookie (real auth), via the provided lookup function.
//  2. The X-User-ID header (dev/test fallback), only when no cookie is present.
//
// If flagsLookup is non-nil it is called to load per-user flags (e.g.
// authorized_non_public) and store them in the context.
//
// Unauthenticated requests pass through with an empty user ID in context.
func Auth(headerName string, sessionLookup SessionLookup, flagsLookup UserFlagsLookup) func(http.Handler) http.Handler {
	const cookieName = "photoapp_session"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var uid, sessionID string

			// 1. Session cookie.
			if cookie, err := r.Cookie(cookieName); err == nil && cookie.Value != "" {
				if sessionLookup != nil {
					uid = sessionLookup(r.Context(), cookie.Value)
				}
				if uid != "" {
					// Store first 8 hex chars as a short, safe session identifier.
					n := min(8, len(cookie.Value))
					sessionID = cookie.Value[:n]
				}
			}

			// 2. Dev/test header fallback (only when no valid session cookie).
			if uid == "" {
				uid = r.Header.Get(headerName)
			}

			if uid != "" {
				ctx := context.WithValue(r.Context(), ctxUserID, uid)
				if sessionID != "" {
					ctx = context.WithValue(ctx, ctxSessionID, sessionID)
				}
				if flagsLookup != nil {
					flags := flagsLookup(ctx, uid)
					ctx = context.WithValue(ctx, ctxAuthorizedNonPublic, flags.AuthorizedNonPublic)
					if flags.Username != "" {
						ctx = context.WithValue(ctx, ctxUsername, flags.Username)
					}
				}
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth rejects requests that have no authenticated user.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserID(r.Context()); !ok {
			WriteError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CORS adds permissive CORS headers suitable for local development.
// Tighten AllowOrigin for production.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}
