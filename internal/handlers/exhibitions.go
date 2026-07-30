package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"

	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

// ExhibitionsHandler lets an authenticated user create a new exhibition.
// PLAN2.md Phase 1a. There is no permission gate on Create itself — starting
// a new exhibition is the entry point into the product, available to any
// logged-in user, the same way registering an account is. No Cfg field —
// unlike most handlers, Create doesn't need pagination defaults or an
// upload dir; add one if a later endpoint on this handler does.
type ExhibitionsHandler struct {
	DB *db.Pool
}

type exhibitionResponse struct {
	ExhibitionID   string `json:"exhibitionid"`
	Name           string `json:"name"`
	OrganizationID string `json:"organizationid"`
}

// viewerPermissions, contributorPermissions, and adminPermissions mirror the
// three standard roles scripts/seed-exhibition.sh bootstraps for a
// manually-created exhibition. Kept in sync with that script by hand — there
// is currently no single shared source of truth for "what a default
// Viewer/Contributor/Admin role grants," since the script predates this
// handler and duplicating it in Go (rather than shelling out) is what lets
// Create run inside one transaction alongside the exhibition insert itself.
var (
	viewerPermissions = []string{
		permissions.PermGalleryView, permissions.PermDisplayView,
		permissions.PermPhotoLabelView, permissions.PermPhotoEmojiView, permissions.PermPhotoCommentView,
	}
	contributorPermissions = []string{
		permissions.PermGalleryView, permissions.PermDisplayView, permissions.PermPhotoCreate,
		permissions.PermPhotoLabelView, permissions.PermPhotoLabelCreate, permissions.PermPhotoLabelModify, permissions.PermPhotoLabelDelete,
		permissions.PermPhotoEmojiView, permissions.PermPhotoEmojiCreate, permissions.PermPhotoEmojiDelete,
		permissions.PermPhotoCommentView, permissions.PermPhotoCommentCreate, permissions.PermPhotoCommentModify, permissions.PermPhotoCommentDelete,
	}
	adminPermissions = []string{
		permissions.PermGalleryView, permissions.PermGalleryCreate, permissions.PermGalleryModify, permissions.PermGalleryDelete,
		permissions.PermDisplayView, permissions.PermDisplayCreate, permissions.PermDisplayModify, permissions.PermDisplayDelete,
		permissions.PermPhotoCreate, permissions.PermPhotoDelete, permissions.PermPrivatePhotoView, permissions.PermPhotoDescriptionModify,
		permissions.PermPhotoLabelView, permissions.PermPhotoLabelCreate, permissions.PermPhotoLabelModify, permissions.PermPhotoLabelDelete,
		permissions.PermPhotoEmojiView, permissions.PermPhotoEmojiCreate, permissions.PermPhotoEmojiDelete,
		permissions.PermPhotoCommentView, permissions.PermPhotoCommentCreate, permissions.PermPhotoCommentModify, permissions.PermPhotoCommentDelete,
		permissions.PermEmojiUpload,
		permissions.PermAdmin, permissions.PermLabelAdmin, permissions.PermEmojiAdmin, permissions.PermUserAdmin,
		permissions.PermGalleryAdmin, permissions.PermTeamAdmin, permissions.PermPermissionsAdmin,
	}
)

// POST /api/v1/exhibitions
// Body: {"name": "...", "organizationName": "..."}  — organizationName is
// optional and only used when this turns out to be the caller's first
// exhibition (see resolveOrganizationID below); ignored otherwise.
// Requires: authenticated (no exhibition-scoped permission applies — there
// is no exhibition yet).
func (h *ExhibitionsHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID := middleware.MustUserID(ctx)

	var req struct {
		Name             string `json:"name"`
		OrganizationName string `json:"organizationName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	organizationID, err := h.resolveOrganizationID(ctx, userID, strings.TrimSpace(req.OrganizationName))
	if err != nil {
		slog.Error("Exhibitions.Create resolveOrganizationID", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		slog.Error("Exhibitions.Create begin", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(ctx)

	var exhibitionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO exhibitions (name, organizationid)
		VALUES ($1, $2::uuid)
		RETURNING exhibitionid::text
	`, req.Name, organizationID).Scan(&exhibitionID); err != nil {
		slog.Error("Exhibitions.Create insert exhibition", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO user_exhibitions (userid, exhibitionid) VALUES ($1::uuid, $2::uuid)
	`, userID, exhibitionID); err != nil {
		slog.Error("Exhibitions.Create insert user_exhibitions", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := bootstrapExhibitionRoles(ctx, tx, exhibitionID, userID); err != nil {
		slog.Error("Exhibitions.Create bootstrapExhibitionRoles", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("Exhibitions.Create commit", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusCreated, exhibitionResponse{
		ExhibitionID:   exhibitionID,
		Name:           req.Name,
		OrganizationID: organizationID,
	})
}

// resolveOrganizationID implements PLAN2.md 1a's "auto-create an
// organization for a user when they create their first exhibition."
//
// There is no formal notion of organization membership yet — that's Phase
// 1c (org-scoped admin permissions), not built yet — so this uses the best
// available stand-in: does the caller directly hold the Admin permission on
// any existing exhibition? If so, treat them as already belonging to that
// exhibition's organization and reuse it, so a user's second, third, ...
// exhibition doesn't fragment into its own orphaned single-exhibition org.
// If not, this really is their first exhibition: create a new organization
// for them. Once Phase 1c adds real org membership, this lookup should be
// replaced with a direct membership check rather than inferring it from
// exhibition-level grants.
func (h *ExhibitionsHandler) resolveOrganizationID(ctx context.Context, userID, requestedOrgName string) (string, error) {
	var existingOrgID string
	err := h.DB.QueryRow(ctx, `
		SELECT e.organizationid::text
		FROM   exhibitions e
		JOIN   entity_role_grants erg ON erg.exhibitionid = e.exhibitionid
		JOIN   role_permissions   rp  ON rp.roleid = erg.roleid
		JOIN   roles              r   ON r.roleid  = erg.roleid AND r.deleted_at IS NULL
		WHERE  erg.entity_type = 'User' AND erg.entity_ref = $1
		  AND  rp.permission = $2
		  AND  e.deleted_at IS NULL
		LIMIT 1
	`, userID, permissions.PermAdmin).Scan(&existingOrgID)
	if err == nil {
		return existingOrgID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	orgName := requestedOrgName
	if orgName == "" {
		var username string
		if err := h.DB.QueryRow(ctx, `SELECT username FROM users WHERE userid = $1::uuid`, userID).Scan(&username); err != nil {
			return "", err
		}
		orgName = fmt.Sprintf("%s's Organization", username)
	}

	var newOrgID string
	if err := h.DB.QueryRow(ctx, `
		INSERT INTO organizations (name) VALUES ($1)
		RETURNING organizationid::text
	`, orgName).Scan(&newOrgID); err != nil {
		return "", err
	}
	return newOrgID, nil
}

// bootstrapExhibitionRoles creates the standard Viewer/Contributor/Admin
// roles for a brand new exhibition and grants them — Viewer to Public,
// Contributor to LoggedIn, Admin directly to creatorUserID — mirroring
// scripts/seed-exhibition.sh's outcome. Unlike the script, Admin is granted
// directly to the creating user rather than via an intermediate "Admins"
// team: a brand new, single-admin exhibition doesn't need a team yet, and
// one can be created later through the normal Teams admin UI if the owner
// wants to add co-admins.
func bootstrapExhibitionRoles(ctx context.Context, tx pgx.Tx, exhibitionID, creatorUserID string) error {
	viewerRoleID, err := createRole(ctx, tx, exhibitionID, "Viewer", "Can view photos, labels, emojis, and comments.", viewerPermissions)
	if err != nil {
		return fmt.Errorf("create Viewer role: %w", err)
	}
	contributorRoleID, err := createRole(ctx, tx, exhibitionID, "Contributor", "Can view and contribute labels, emoji reactions, and comments.", contributorPermissions)
	if err != nil {
		return fmt.Errorf("create Contributor role: %w", err)
	}
	adminRoleID, err := createRole(ctx, tx, exhibitionID, "Admin", "Full administrative access to the exhibition.", adminPermissions)
	if err != nil {
		return fmt.Errorf("create Admin role: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_role_grants (roleid, entity_type, exhibitionid) VALUES ($1, 'Public', $2::uuid)
	`, viewerRoleID, exhibitionID); err != nil {
		return fmt.Errorf("grant Viewer to Public: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_role_grants (roleid, entity_type, exhibitionid) VALUES ($1, 'LoggedIn', $2::uuid)
	`, contributorRoleID, exhibitionID); err != nil {
		return fmt.Errorf("grant Contributor to LoggedIn: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_role_grants (roleid, entity_type, entity_ref, exhibitionid) VALUES ($1, 'User', $2, $3::uuid)
	`, adminRoleID, creatorUserID, exhibitionID); err != nil {
		return fmt.Errorf("grant Admin to creator: %w", err)
	}
	return nil
}

// createRole inserts one role plus its bundled permissions within tx.
func createRole(ctx context.Context, tx pgx.Tx, exhibitionID, name, description string, perms []string) (string, error) {
	var roleID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO roles (exhibitionid, name, description) VALUES ($1::uuid, $2, $3)
		RETURNING roleid::text
	`, exhibitionID, name, description).Scan(&roleID); err != nil {
		return "", err
	}
	for _, p := range perms {
		if _, err := tx.Exec(ctx, `
			INSERT INTO role_permissions (roleid, permission) VALUES ($1, $2)
		`, roleID, p); err != nil {
			return "", fmt.Errorf("add permission %q: %w", p, err)
		}
	}
	return roleID, nil
}
