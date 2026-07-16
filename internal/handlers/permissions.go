package handlers

import (
	"log/slog"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

// PermissionsHandler serves GET /api/v1/permissions.
type PermissionsHandler struct {
	Checker *permissions.Checker
}

// permissionsResponse is the JSON shape returned by GET /api/v1/permissions.
type permissionsResponse struct {
	// Grants lists every effective (permission, resource_type, resource_ref)
	// triple for the calling user in the current exhibition context.
	// resource_type and resource_ref are omitted (empty) for global grants.
	Grants []permissions.Grant `json:"grants"`

	// Summary is a flat set of permission strings the user holds at the
	// global or exhibition level — convenient for simple "can I do X?" checks
	// in the frontend without iterating the full grant list.
	Summary []string `json:"summary"`
}

// GET /api/v1/permissions
// Returns the effective permissions for the calling user in the current
// exhibition. Works for both authenticated and unauthenticated requests.
func (h *PermissionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	grants, err := h.Checker.UserPermissions(ctx, userID, exhibitionID)
	if err != nil {
		slog.Error("GET /api/v1/permissions", "error", err, "userid", userID, "exhibitionid", exhibitionID)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if grants == nil {
		grants = []permissions.Grant{}
	}

	// Build a deduplicated summary of top-level (global/exhibition) permissions.
	seen := make(map[string]struct{})
	summary := make([]string, 0)
	for _, g := range grants {
		if g.ResourceType == "" {
			if _, ok := seen[g.Permission]; !ok {
				seen[g.Permission] = struct{}{}
				summary = append(summary, g.Permission)
			}
		}
	}

	middleware.WriteJSON(w, http.StatusOK, permissionsResponse{
		Grants:  grants,
		Summary: summary,
	})
}
