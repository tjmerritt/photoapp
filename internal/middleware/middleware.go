package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// ── Context keys ──────────────────────────────────────────────────────────────

type ctxKey string

const (
	ctxUserID    ctxKey = "userid"
	ctxRequestID ctxKey = "requestid"
)

// UserID retrieves the authenticated user ID from the request context.
// Returns ("", false) when no user is present (unauthenticated request).
func UserID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxUserID).(string)
	return v, ok && v != ""
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

// Auth reads the X-User-ID header and injects the user ID into the context.
// This is a placeholder; replace with real JWT / session validation later.
// Requests without the header are treated as anonymous (user ID = "").
func Auth(headerName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid := r.Header.Get(headerName)
			if uid != "" {
				r = r.WithContext(context.WithValue(r.Context(), ctxUserID, uid))
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
