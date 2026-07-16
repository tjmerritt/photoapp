package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/permissions"
)

// TeamsHandler handles the teams admin page's endpoints: CRUD on teams
// themselves plus managing their membership. Teams are exhibition-scoped
// (see migrations/012_permissions.sql) — a user becomes a member of the
// "Team" entity type by an entity_role_grants row referencing the team's
// teamid, so team membership is really just a convenient way to grant a
// bundle of permissions to a group of users at once.
type TeamsHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

type adminTeam struct {
	TeamID      string `json:"teamid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MemberCount int    `json:"member_count"`
}

type adminTeamMember struct {
	UserID   string `json:"userid"`
	Username string `json:"username"`
	Email    string `json:"email"`
	JoinedAt string `json:"joined_at"`
}

// teamExhibitionID resolves the exhibition a (non-deleted) team belongs to,
// used both to scope the permission check and to 404 on a bad teamid before
// doing anything else.
func teamExhibitionID(w http.ResponseWriter, r *http.Request, pool *db.Pool, teamID string) (string, bool) {
	var exhibitionID string
	err := pool.QueryRow(r.Context(), `
		SELECT exhibitionid::text FROM teams WHERE teamid = $1 AND deleted_at IS NULL
	`, teamID).Scan(&exhibitionID)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "team not found")
		return "", false
	}
	if err != nil {
		slog.Error("teamExhibitionID", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return "", false
	}
	return exhibitionID, true
}

// GET /api/v1/admin/teams?exhibitionid=&search=&offset=&limit=  (Phase 6f)
// Requires: authenticated + (PermAdmin or PermTeamAdmin).
func (h *TeamsHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermTeamAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}
	if exhibitionID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "exhibitionid is required")
		return
	}

	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)
	search := strings.TrimSpace(r.URL.Query().Get("search"))

	where := "t.deleted_at IS NULL AND t.exhibitionid = $1::uuid"
	args := []any{exhibitionID}
	n := 2
	if search != "" {
		where += fmt.Sprintf(" AND t.name ILIKE $%d", n)
		args = append(args, "%"+search+"%")
		n++
	}

	var total int
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(*) FROM teams t WHERE "+where, args...).Scan(&total); err != nil {
		slog.Error("Teams.List count", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	rowArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT t.teamid::text, t.name, COALESCE(t.description, ''),
		       COALESCE(tm.member_count, 0)
		FROM   teams t
		LEFT JOIN (
		    SELECT teamid, COUNT(*) AS member_count
		    FROM   team_members
		    GROUP  BY teamid
		) tm ON tm.teamid = t.teamid
		WHERE  %s
		ORDER  BY t.name
		LIMIT  $%d OFFSET $%d
	`, where, n, n+1), rowArgs...)
	if err != nil {
		slog.Error("Teams.List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	teams := make([]adminTeam, 0)
	for rows.Next() {
		var t adminTeam
		if err := rows.Scan(&t.TeamID, &t.Name, &t.Description, &t.MemberCount); err != nil {
			slog.Error("Teams.List", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		teams = append(teams, t)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Teams.List", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"teams":  teams,
	})
}

// POST /api/v1/admin/teams?exhibitionid=  (Phase 6f)
// Body: {"name": "...", "description": "..."}
// Requires: authenticated + (PermAdmin or PermTeamAdmin).
func (h *TeamsHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID, _ := middleware.UserID(ctx)
	exhibitionID := r.URL.Query().Get("exhibitionid")
	if exhibitionID == "" {
		exhibitionID = middleware.ExhibitionID(ctx)
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermTeamAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}
	if exhibitionID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "exhibitionid is required")
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
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

	var teamID string
	err := h.DB.QueryRow(ctx, `
		INSERT INTO teams (exhibitionid, name, description)
		VALUES ($1::uuid, $2, NULLIF($3, ''))
		RETURNING teamid::text
	`, exhibitionID, req.Name, req.Description).Scan(&teamID)
	if err != nil {
		slog.Error("Teams.Create", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not create team (name may already be in use)")
		return
	}

	middleware.WriteJSON(w, http.StatusCreated, adminTeam{
		TeamID:      teamID,
		Name:        req.Name,
		Description: req.Description,
	})
}

// PATCH /api/v1/admin/teams/:teamid  (Phase 6f)
// Body: any subset of {"name": "...", "description": "..."}
// Requires: authenticated + (PermAdmin or PermTeamAdmin).
func (h *TeamsHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	teamID := ps.ByName("teamid")
	userID, _ := middleware.UserID(ctx)

	exhibitionID, ok := teamExhibitionID(w, r, h.DB, teamID)
	if !ok {
		return
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermTeamAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == nil && req.Description == nil {
		middleware.WriteError(w, http.StatusBadRequest, "name or description is required")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		middleware.WriteError(w, http.StatusBadRequest, "name cannot be empty")
		return
	}

	hasDescription := req.Description != nil
	var descriptionVal string
	if hasDescription {
		descriptionVal = *req.Description
	}

	if _, err := h.DB.Exec(ctx, `
		UPDATE teams SET
			name        = COALESCE($1, name),
			description = CASE WHEN $2 THEN $3 ELSE description END,
			updated_at  = NOW()
		WHERE teamid = $4
	`, req.Name, hasDescription, descriptionVal, teamID); err != nil {
		slog.Error("Teams.Update", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not update team (name may already be in use)")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/admin/teams/:teamid  (Phase 6f)
// Soft-deletes the team AND strips its grants/memberships, since
// Checker.Check's Team-entity branch resolves membership via team_members
// alone and does not itself check teams.deleted_at (see
// internal/permissions/permissions.go) — leaving either behind would let a
// "deleted" team keep conferring permissions.
// Requires: authenticated + (PermAdmin or PermTeamAdmin).
func (h *TeamsHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	teamID := ps.ByName("teamid")
	userID, _ := middleware.UserID(ctx)

	exhibitionID, ok := teamExhibitionID(w, r, h.DB, teamID)
	if !ok {
		return
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermTeamAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		slog.Error("Teams.Delete begin", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE teams SET deleted_at = NOW() WHERE teamid = $1`, teamID); err != nil {
		slog.Error("Teams.Delete team", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entity_role_grants WHERE entity_type = 'Team' AND entity_ref = $1`, teamID); err != nil {
		slog.Error("Teams.Delete grants", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM team_members WHERE teamid = $1`, teamID); err != nil {
		slog.Error("Teams.Delete members", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("Teams.Delete commit", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/admin/teams/:teamid/members  (Phase 6f)
// Requires: authenticated + (PermAdmin or PermTeamAdmin).
func (h *TeamsHandler) ListMembers(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	teamID := ps.ByName("teamid")
	userID, _ := middleware.UserID(ctx)

	exhibitionID, ok := teamExhibitionID(w, r, h.DB, teamID)
	if !ok {
		return
	}
	if ok, err := h.Checker.HasAny(ctx, userID, exhibitionID, permissions.PermAdmin, permissions.PermTeamAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	rows, err := h.DB.Query(ctx, `
		SELECT u.userid::text, u.username, u.email, tm.joined_at::text
		FROM   team_members tm
		JOIN   users u ON u.userid = tm.userid
		WHERE  tm.teamid = $1 AND u.deleted_at IS NULL
		ORDER  BY u.username
	`, teamID)
	if err != nil {
		slog.Error("Teams.ListMembers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	members := make([]adminTeamMember, 0)
	for rows.Next() {
		var m adminTeamMember
		if err := rows.Scan(&m.UserID, &m.Username, &m.Email, &m.JoinedAt); err != nil {
			slog.Error("Teams.ListMembers", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Teams.ListMembers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, map[string]any{"members": members})
}

// POST /api/v1/admin/teams/:teamid/members?userid=  (Phase 6f)
// Requires: authenticated + (PermAdmin or PermTeamAdmin).
func (h *TeamsHandler) AddMember(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	teamID := ps.ByName("teamid")
	targetUserID := r.URL.Query().Get("userid")
	if targetUserID == "" {
		middleware.WriteError(w, http.StatusBadRequest, "userid is required")
		return
	}
	callerUserID, _ := middleware.UserID(ctx)

	exhibitionID, ok := teamExhibitionID(w, r, h.DB, teamID)
	if !ok {
		return
	}
	if ok, err := h.Checker.HasAny(ctx, callerUserID, exhibitionID, permissions.PermAdmin, permissions.PermTeamAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	if _, err := h.DB.Exec(ctx, `
		INSERT INTO team_members (teamid, userid) VALUES ($1, $2)
		ON CONFLICT (teamid, userid) DO NOTHING
	`, teamID, targetUserID); err != nil {
		slog.Error("Teams.AddMember", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/admin/teams/:teamid/members/:userid  (Phase 6f)
// Requires: authenticated + (PermAdmin or PermTeamAdmin).
func (h *TeamsHandler) RemoveMember(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	teamID := ps.ByName("teamid")
	targetUserID := ps.ByName("userid")
	callerUserID, _ := middleware.UserID(ctx)

	exhibitionID, ok := teamExhibitionID(w, r, h.DB, teamID)
	if !ok {
		return
	}
	if ok, err := h.Checker.HasAny(ctx, callerUserID, exhibitionID, permissions.PermAdmin, permissions.PermTeamAdmin); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	if _, err := h.DB.Exec(ctx, `DELETE FROM team_members WHERE teamid = $1 AND userid = $2`, teamID, targetUserID); err != nil {
		slog.Error("Teams.RemoveMember", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
