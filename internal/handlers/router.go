package handlers

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

// NewRouter builds and returns the fully configured HTTP router.
func NewRouter(pool *db.Pool, cfg *config.Config, authHandler *AuthHandler, exhibitionHandler *ExhibitionHandler, checker *permissions.Checker) (http.Handler, error) {
	r := httprouter.New()

	// ── Image cache ───────────────────────────────────────────────────────────
	imgCache, err := NewImageCache(cfg.ImgCacheDir)
	if err != nil {
		return nil, err
	}

	// ── Handler instances ─────────────────────────────────────────────────────
	photos      := &PhotoHandler{DB: pool, Cfg: cfg, Checker: checker}
	photoList   := &ListPhotosHandler{DB: pool, Checker: checker}
	patchPhoto  := &PatchPhotoHandler{DB: pool, Cfg: cfg, Checker: checker}
	users       := &UserHandler{DB: pool}
	labels      := &LabelsHandler{DB: pool, Cfg: cfg, Checker: checker}
	emojis      := &EmojisHandler{DB: pool, Cfg: cfg, Checker: checker}
	comments    := &CommentsHandler{DB: pool, Cfg: cfg, Checker: checker}
	search      := &SearchHandler{DB: pool, Checker: checker}
	imgProxy    := &ImgProxyHandler{Cache: imgCache}
	admin       := &AdminHandler{DB: pool, Cfg: cfg, Checker: checker}
	perms       := &PermissionsHandler{Checker: checker}
	galleries   := &GalleriesHandler{DB: pool, Cfg: cfg, Checker: checker}
	displays    := &DisplaysHandler{DB: pool, Cfg: cfg, Checker: checker}
	templates   := &TemplatesHandler{DB: pool, Checker: checker}
	uploads     := &UploadPhotosHandler{DB: pool, Cfg: cfg, Checker: checker}
	teams       := &TeamsHandler{DB: pool, Cfg: cfg, Checker: checker}
	grants      := &GrantsHandler{DB: pool, Cfg: cfg, Checker: checker}
	roles       := &RolesHandler{DB: pool, Cfg: cfg, Checker: checker}
	exhibitions := &ExhibitionsHandler{DB: pool}
	scope       := &ScopeHandler{DB: pool, Cfg: cfg, Checker: checker}

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
	r.GET("/api/v1/photos", photoList.ServeHTTP)
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
	r.HandlerFunc(http.MethodGet, "/api/v1/search",       search.ServeHTTP)
	r.GET("/api/v1/permissions",                          perms.ServeHTTP)

	// Galleries and displays (view — no auth required; permission-checked in handler)
	r.GET("/api/v1/galleries",                            galleries.List)
	r.GET("/api/v1/galleries/:galleryid",                 galleries.Get)
	r.GET("/api/v1/displays/:displayid",                  displays.Get)
	r.GET("/api/v1/display-templates",                    templates.List)

	// ── Write endpoints (auth required) ───────────────────────────────────────

	// Labels
	r.POST("/api/v1/labels",                              auth(labels.Create))
	r.PATCH("/api/v1/labels/:labelid",                    auth(labels.Update))
	r.DELETE("/api/v1/labels/:labelid",                   auth(labels.Delete))

	// Label names (write — PermAdmin/PermLabelAdmin enforced in handler)
	r.PATCH("/api/v1/label-names",                        auth(labels.UpdateName))

	// Emoji reactions
	r.POST("/api/v1/emoji/react",                         auth(emojis.React))
	r.DELETE("/api/v1/emoji/react",                       auth(emojis.Unreact))

	// Emoji type upload
	r.POST("/api/v1/emoji/types",                         auth(emojis.UploadType))

	// Comments
	r.POST("/api/v1/comments",                            auth(comments.Create))
	r.PATCH("/api/v1/comments/:commentid",                auth(comments.Update))
	r.DELETE("/api/v1/comments/:commentid",               auth(comments.Delete))

	// Galleries (write — auth required; permission-checked in handler)
	r.POST("/api/v1/galleries",                           auth(galleries.Create))
	r.PATCH("/api/v1/galleries/:galleryid",               auth(galleries.Update))
	r.DELETE("/api/v1/galleries/:galleryid",              auth(galleries.Delete))

	// Displays (write — auth required; permission-checked in handler)
	r.POST("/api/v1/galleries/:galleryid/displays",       auth(displays.Create))
	r.PATCH("/api/v1/displays/:displayid",                auth(displays.Update))
	r.DELETE("/api/v1/displays/:displayid",               auth(displays.Delete))

	// Display templates (write — PermAdmin enforced in handler)
	r.POST("/api/v1/display-templates",                   auth(templates.Create))
	r.PATCH("/api/v1/display-templates/:templateid",      auth(templates.Update))
	r.DELETE("/api/v1/display-templates/:templateid",     auth(templates.Delete))

	// Photo uploads (write — auth required; PermPhotoCreate enforced in handler)
	r.POST("/api/v1/photos/upload",                       auth(uploads.ServeHTTP))

	// Exhibitions (write — auth required, no exhibition-scoped permission:
	// this is exhibition-agnostic by nature. PLAN2.md Phase 1a.)
	r.POST("/api/v1/exhibitions",                         auth(exhibitions.Create))

	// ── Admin endpoints (auth + PermAdmin enforced in handler) ───────────────────
	r.GET("/api/v1/admin/exhibitions",  auth(admin.ListExhibitions))
	r.GET("/api/v1/admin/photos",       auth(admin.ListPhotos))
	r.PATCH("/api/v1/admin/photo",      auth(admin.SetPublic))
	r.GET("/api/v1/admin/stats",        auth(admin.Stats))

	// Phase 1d: header Organization/Exhibition picker (paginated, searchable;
	// access control is baked into the query itself — see scope.go).
	r.GET("/api/v1/admin/organizations",   auth(scope.ListOrganizations))
	r.GET("/api/v1/admin/org-exhibitions", auth(scope.ListExhibitions))

	// Phase 6b: user admin (PermAdmin/PermUserAdmin enforced in handler)
	r.GET("/api/v1/admin/users",           auth(admin.ListUsers))
	r.PATCH("/api/v1/admin/users/:userid", auth(admin.UpdateUser))

	// Phase 6d: emoji admin (PermAdmin/PermEmojiAdmin enforced in handler)
	r.GET("/api/v1/admin/emoji-types",            auth(emojis.AdminListTypes))
	r.PATCH("/api/v1/admin/emoji-types/:emojiid", auth(emojis.AdminUpdateType))

	// Phase 6e: label admin (PermAdmin/PermLabelAdmin enforced in handler)
	r.GET("/api/v1/admin/label-names", auth(labels.AdminListNames))

	// Phase 6f: teams admin (PermAdmin/PermTeamAdmin enforced in handler)
	r.GET("/api/v1/admin/teams",                          auth(teams.List))
	r.POST("/api/v1/admin/teams",                         auth(teams.Create))
	r.PATCH("/api/v1/admin/teams/:teamid",                auth(teams.Update))
	r.DELETE("/api/v1/admin/teams/:teamid",               auth(teams.Delete))
	r.GET("/api/v1/admin/teams/:teamid/members",          auth(teams.ListMembers))
	r.POST("/api/v1/admin/teams/:teamid/members",         auth(teams.AddMember))
	r.DELETE("/api/v1/admin/teams/:teamid/members/:userid", auth(teams.RemoveMember))

	// Phase 6g/6h/6j: permission-grants viewer + creation + editing (PermAdmin/PermPermissionsAdmin enforced in handler)
	r.GET("/api/v1/admin/grants/global",       auth(grants.ListGlobal))
	r.GET("/api/v1/admin/grants/exhibition",   auth(grants.ListForExhibition))
	r.POST("/api/v1/admin/grants",             auth(grants.Create))
	r.PATCH("/api/v1/admin/grants/:grantid",   auth(grants.Update))
	r.DELETE("/api/v1/admin/grants/:grantid",  auth(grants.Revoke))

	// Phase 6i: roles admin (PermAdmin/PermPermissionsAdmin enforced in handler)
	r.GET("/api/v1/admin/roles",                            auth(roles.List))
	r.POST("/api/v1/admin/roles",                           auth(roles.Create))
	r.PATCH("/api/v1/admin/roles/:roleid",                  auth(roles.Update))
	r.DELETE("/api/v1/admin/roles/:roleid",                 auth(roles.Delete))
	r.POST("/api/v1/admin/roles/:roleid/permissions",       auth(roles.AddPermission))
	r.DELETE("/api/v1/admin/roles/:roleid/permissions/:permission", auth(roles.RemovePermission))
	r.GET("/api/v1/admin/permission-catalog",               auth(roles.PermissionCatalog))

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
	r.GET("/auth/facebook",              authHandler.FacebookLogin)
	r.GET("/auth/facebook/callback",     authHandler.FacebookCallback)
	r.GET("/auth/microsoft",             authHandler.MicrosoftLogin)
	r.GET("/auth/microsoft/callback",    authHandler.MicrosoftCallback)
	r.POST("/auth/register",             authHandler.Register)
	r.POST("/auth/login",                authHandler.Login)
	r.PATCH("/auth/profile",             authHandler.UpdateProfile)
	r.POST("/auth/profile/avatar",       authHandler.UploadProfileAvatar)

	// Apply global middleware: CORS → Exhibition → Auth → Logger → RequestID
	var handler http.Handler = r
	handler = middleware.Logger(handler)
	handler = middleware.Auth(cfg.AuthHeader, authHandler.LookupSession, authHandler.LookupUserFlags)(handler)
	handler = middleware.Exhibition(exhibitionHandler.Lookup, cfg.AppDir)(handler)
	handler = middleware.CORS(handler)
	handler = middleware.RequestID(handler)

	return handler, nil
}
