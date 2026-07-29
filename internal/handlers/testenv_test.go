package handlers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// testEnv bundles the dependencies every handler struct needs (DB, Cfg,
// Checker), backed by a freshly truncated database — see
// testutil.RequireDB. Skips the calling test when TEST_DATABASE_URL isn't
// configured.
type testEnv struct {
	Pool    *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	pool := testutil.RequireDB(t)
	return &testEnv{
		Pool:    pool,
		Cfg:     &config.Config{DefaultPageSize: 10, MaxPageSize: 100},
		Checker: &permissions.Checker{DB: pool},
	}
}

// doRequest builds a request for method/target, optionally authenticated as
// userID (via the X-User-ID dev header — no header at all when userID ==
// ""), running it through the same Auth and Exhibition middleware
// production traffic passes through before reaching handle. This way
// middleware.UserID / middleware.ExhibitionID inside the handler under test
// behave exactly as they would for a real request, without the test needing
// access to middleware's unexported context keys.
func doRequest(
	t *testing.T,
	method, target, userID, exhibitionID string,
	body io.Reader,
	params httprouter.Params,
	handle httprouter.Handle,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, body)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	req.Header.Set("Content-Type", "application/json")

	var final http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, params)
	})
	final = middleware.Auth("X-User-ID", nil, nil)(final)
	lookup := func(_ context.Context, _ string) (string, bool) { return exhibitionID, true }
	final = middleware.Exhibition(lookup, "")(final)

	rec := httptest.NewRecorder()
	final.ServeHTTP(rec, req)
	return rec
}

func strPtr(s string) *string { return &s }

// adapt wraps a plain (w, r) handler method — several handlers in this
// package (PhotoHandler, UserHandler, PatchPhotoHandler, SearchHandler, ...)
// implement plain http.HandlerFunc rather than httprouter.Handle, since
// router.go registers them via r.HandlerFunc(...) instead of
// r.GET(...)/r.POST(...) — as an httprouter.Handle so it can be passed to
// doRequest like every other handler under test.
func adapt(h func(http.ResponseWriter, *http.Request)) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		h(w, r)
	}
}
