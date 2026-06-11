package handlers

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
)

// NewRouter builds and returns the fully configured HTTP router.
func NewRouter(pool *db.Pool, cfg *config.Config, authHandler *AuthHandler, exhibitionHandler *ExhibitionHandler) http.Handler {
	r := httprouter.New()

	// ── Handler instances ─────────────────────────────────────────────────────
	photos      := &PhotoHandler{DB: pool, Cfg: cfg}
	patchPhoto  := &PatchPhotoHandler{DB: pool, Cfg: cfg}
	users       := &UserHandler{DB: pool}
	labels      := &LabelsHandler{DB: pool, Cfg: cfg}
	emojis      := &EmojisHandler{DB: pool, Cfg: cfg}
	comments    := &CommentsHandler{DB: pool, Cfg: cfg}
	imgProxy    := &ImgProxyHandler{}

	// Convenience: wrap a httprouter.Handle with RequireAuth
	auth := func(h httprouter.Handle) httprouter.Handle {
		return func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
			if _, ok := middleware.UserID(req.Context()); !ok {
				middleware.WriteError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			h(w, req, ps)
		}
	}

	// ── Read endpoints (no auth required) ─────────────────────────────────────
	r.HandlerFunc(http.MethodGet, "/api/v1/imgproxy", imgProxy.ServeHTTP)
	r.HandlerFunc(http.MethodGet, "/api/v1/photo", photos.ServeHTTP)
	r.PATCH("/api/v1/photo", auth(func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
		patchPhoto.ServeHTTP(w, req)
	}))
	r.HandlerFunc(http.MethodGet, "/api/v1/user",         users.ServeHTTP)
	r.GET("/api/v1/labels",                               labels.List)
	r.GET("/api/v1/label-names",                          labels.Names)
	r.GET("/api/v1/label-values",                         labels.Values)
	r.GET("/api/v1/emojis",                               emojis.List)
	r.GET("/api/v1/emoji/users",                          emojis.ListUsers)
	r.GET("/api/v1/emoji/types",                          emojis.ListTypes)
	r.GET("/api/v1/emoji/variants",                       emojis.ListVariants)
	r.GET("/api/v1/comments",                             comments.List)

	// ── Write endpoints (auth required) ───────────────────────────────────────

	// Labels
	r.POST("/api/v1/labels",                              auth(labels.Create))
	r.PATCH("/api/v1/labels/:labelid",                    auth(labels.Update))
	r.DELETE("/api/v1/labels/:labelid",                   auth(labels.Delete))

	// Emoji reactions
	r.POST("/api/v1/emoji/react",                         auth(emojis.React))
	r.DELETE("/api/v1/emoji/react",                       auth(emojis.Unreact))

	// Emoji type upload
	r.POST("/api/v1/emoji/types",                         auth(emojis.UploadType))

	// Comments
	r.POST("/api/v1/comments",                            auth(comments.Create))
	r.PATCH("/api/v1/comments/:commentid",                auth(comments.Update))
	r.DELETE("/api/v1/comments/:commentid",               auth(comments.Delete))

	// ── Static file serving for uploaded emoji images ─────────────────────────
	r.ServeFiles("/uploads/*filepath", http.Dir(cfg.UploadDir))

	// ── Frontend — serve AppDir for all non-/api paths ───────────────────────
	appFS := http.FileServer(http.Dir(cfg.AppDir))
	r.NotFound = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if len(req.URL.Path) >= 4 && req.URL.Path[:4] == "/api" {
			middleware.WriteError(w, http.StatusNotFound, "not found")
			return
		}
		appFS.ServeHTTP(w, req)
	})

	// ── Health check ──────────────────────────────────────────────────────────
	r.GET("/healthz", func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		middleware.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// ── Avatar ────────────────────────────────────────────────────────────────
	r.GET("/avatars/:hash", ServeAvatar)

	// ── Auth routes ───────────────────────────────────────────────────────────
	r.GET("/auth/config",                authHandler.Config)
	r.GET("/auth/me",                    authHandler.Me)
	r.GET("/auth/users",                 authHandler.ListUsers)
	r.POST("/auth/logout",               authHandler.Logout)
	r.GET("/auth/google",                authHandler.GoogleLogin)
	r.GET("/auth/google/callback",       authHandler.GoogleCallback)
	r.GET("/auth/apple",                 authHandler.AppleLogin)
	r.POST("/auth/apple/callback",       authHandler.AppleCallback)
	r.POST("/auth/register",             authHandler.Register)
	r.POST("/auth/login",                authHandler.Login)
	r.PATCH("/auth/profile",             authHandler.UpdateProfile)
	r.POST("/auth/profile/avatar",       authHandler.UploadProfileAvatar)

	// Apply global middleware: CORS → Exhibition → Auth → Logger → RequestID
	var handler http.Handler = r
	handler = middleware.Logger(handler)
	handler = middleware.Auth(cfg.AuthHeader, authHandler.LookupSession, authHandler.LookupUserFlags)(handler)
	handler = middleware.Exhibition(exhibitionHandler.LookupByHostname)(handler)
	handler = middleware.CORS(handler)
	handler = middleware.RequestID(handler)

	return handler
}
