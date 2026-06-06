package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
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
)

// ExhibitionID retrieves the current exhibition ID from the context.
// Returns "" when no exhibition was resolved for this request.
func ExhibitionID(ctx context.Context) string {
	v, _ := ctx.Value(ctxExhibitionID).(string)
	return v
}

// ExhibitionLookup resolves an exhibition ID from a hostname.
type ExhibitionLookup func(ctx context.Context, hostname string) string

// Exhibition extracts the Host header and looks up the corresponding
// exhibitionid. Tries the full host:port first, then host only.
// The result is injected into the context; requests with no match get "".
func Exhibition(lookup ExhibitionLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if lookup != nil {
				hostport := r.Host
				id := lookup(r.Context(), hostport)
				if id == "" {
					// Strip port and try bare hostname.
					if host, _, err := net.SplitHostPort(hostport); err == nil {
						id = lookup(r.Context(), host)
					}
				}
				if id != "" {
					r = r.WithContext(context.WithValue(r.Context(), ctxExhibitionID, id))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
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

// Logger logs method, path, status and latency for every request.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// SessionLookup is a function that resolves a session token to a user ID.
// Returning "" means the session is invalid or not found.
type SessionLookup func(ctx context.Context, token string) string

// UserFlagsLookup fetches per-user permission flags for the given user ID.
// It is called once per request after the user is resolved.
type UserFlagsLookup func(ctx context.Context, userID string) UserFlags

// UserFlags holds permission flags loaded for an authenticated user.
type UserFlags struct {
	AuthorizedNonPublic bool
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
			var uid string

			// 1. Session cookie.
			if cookie, err := r.Cookie(cookieName); err == nil && cookie.Value != "" {
				if sessionLookup != nil {
					uid = sessionLookup(r.Context(), cookie.Value)
				}
			}

			// 2. Dev/test header fallback (only when no valid session cookie).
			if uid == "" {
				uid = r.Header.Get(headerName)
			}

			if uid != "" {
				ctx := context.WithValue(r.Context(), ctxUserID, uid)
				if flagsLookup != nil {
					flags := flagsLookup(ctx, uid)
					ctx = context.WithValue(ctx, ctxAuthorizedNonPublic, flags.AuthorizedNonPublic)
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
