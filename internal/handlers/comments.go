package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
)

type CommentsHandler struct {
	DB  *db.Pool
	Cfg *config.Config
}

// GET /api/v1/comments?photoid=&parentid=&offset=&limit=
func (h *CommentsHandler) List(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	photoid := r.URL.Query().Get("photoid")
	parentID := r.URL.Query().Get("parentid")
	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}
	offset, limit := parsePage(r, h.Cfg.DefaultPageSize, h.Cfg.MaxPageSize)

	comments, total, err := fetchComments(r.Context(), h.DB, photoid, parentID, offset, limit)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	var baseURL string
	if parentID != "" {
		baseURL = fmt.Sprintf("/api/v1/comments?photoid=%s&parentid=%s&limit=%d", photoid, parentID, limit)
	} else {
		baseURL = fmt.Sprintf("/api/v1/comments?photoid=%s&limit=%d", photoid, limit)
	}

	resp := models.CommentsResponse{
		PhotoID:  photoid,
		Offset:   offset,
		Pages:    buildPages(total, offset, limit, baseURL),
		Comments: comments,
	}
	if parentID != "" {
		resp.ParentID = &parentID
	}

	middleware.WriteJSON(w, http.StatusOK, resp)
}

// POST /api/v1/comments?photoid=&parentid=  (requires auth)
func (h *CommentsHandler) Create(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	photoid := r.URL.Query().Get("photoid")
	parentID := r.URL.Query().Get("parentid")
	userID := middleware.MustUserID(r.Context())

	if photoid == "" {
		middleware.WriteError(w, http.StatusBadRequest, "photoid is required")
		return
	}

	var req models.AddCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Comment == "" {
		middleware.WriteError(w, http.StatusBadRequest, "comment is required")
		return
	}

	// Verify photo exists
	var exists bool
	_ = h.DB.QueryRow(r.Context(), `SELECT TRUE FROM photos WHERE photoid=$1 AND deleted_at IS NULL`, photoid).Scan(&exists)
	if !exists {
		middleware.WriteError(w, http.StatusNotFound, "photo not found")
		return
	}

	// If replying, verify parent comment exists and belongs to same photo
	if parentID != "" {
		var parentPhoto string
		err := h.DB.QueryRow(r.Context(), `
			SELECT photoid::text FROM comments WHERE commentid=$1 AND deleted_at IS NULL
		`, parentID).Scan(&parentPhoto)
		if err == pgx.ErrNoRows {
			middleware.WriteError(w, http.StatusNotFound, "parent comment not found")
			return
		}
		if parentPhoto != photoid {
			middleware.WriteError(w, http.StatusBadRequest, "parent comment belongs to a different photo")
			return
		}
	}

	var (
		commentid string
		date      interface{}
	)
	var err error
	if parentID != "" {
		err = h.DB.QueryRow(r.Context(), `
			INSERT INTO comments (photoid, parent_commentid, author_userid, comment_text)
			VALUES ($1, $2, $3, $4)
			RETURNING commentid::text, created_at
		`, photoid, parentID, userID, req.Comment).Scan(&commentid, &date)
	} else {
		err = h.DB.QueryRow(r.Context(), `
			INSERT INTO comments (photoid, author_userid, comment_text)
			VALUES ($1, $2, $3)
			RETURNING commentid::text, created_at
		`, photoid, userID, req.Comment).Scan(&commentid, &date)
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	var username string
	var profileImage *string
	_ = h.DB.QueryRow(r.Context(), `SELECT username, profile_image FROM users WHERE userid=$1`, userID).
		Scan(&username, &profileImage)

	// Re-fetch the full comment to get proper typed date
	comments, _, err := fetchComments(r.Context(), h.DB, photoid, "", 0, 1)
	// find our new comment
	for _, c := range comments {
		if c.CommentID == commentid {
			middleware.WriteJSON(w, http.StatusCreated, c)
			return
		}
	}

	// Fallback: return minimal object
	middleware.WriteJSON(w, http.StatusCreated, map[string]any{
		"commentid": commentid,
		"author":    map[string]any{"userid": userID, "username": username, "tn": profileImage},
		"comment":   req.Comment,
	})
}

// PATCH /api/v1/comments/:commentid  (requires auth; only the author may edit)
func (h *CommentsHandler) Update(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	commentid := ps.ByName("commentid")
	userID := middleware.MustUserID(r.Context())

	var req models.UpdateCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Comment == "" {
		middleware.WriteError(w, http.StatusBadRequest, "comment is required")
		return
	}

	var authorID, photoid string
	err := h.DB.QueryRow(r.Context(), `
		SELECT author_userid::text, photoid::text FROM comments
		WHERE  commentid=$1 AND deleted_at IS NULL
	`, commentid).Scan(&authorID, &photoid)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "comment not found")
		return
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if authorID != userID {
		middleware.WriteError(w, http.StatusForbidden, "you may only edit your own comments")
		return
	}

	_, err = h.DB.Exec(r.Context(), `
		UPDATE comments SET comment_text=$1 WHERE commentid=$2
	`, req.Comment, commentid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	// Return updated comment
	var username string
	var profileImage *string
	_ = h.DB.QueryRow(r.Context(), `SELECT username, profile_image FROM users WHERE userid=$1`, userID).
		Scan(&username, &profileImage)

	row := h.DB.QueryRow(r.Context(), `
		SELECT c.commentid::text, c.comment_text, c.reply_count, c.created_at,
		       u.userid::text, u.username,
		       COALESCE(u.profile_image, '/avatars/' || md5(lower(trim(COALESCE(u.email, u.userid::text)))))
		FROM   comments c
		JOIN   users    u ON u.userid = c.author_userid
		WHERE  c.commentid=$1
	`, commentid)

	var c models.Comment
	if err := row.Scan(
		&c.CommentID, &c.Comment, &c.ReplyCount, &c.Date,
		&c.Author.UserID, &c.Author.Username, &c.Author.TN,
	); err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	c.RepliesURL = fmt.Sprintf("/api/v1/comments?photoid=%s&parentid=%s", photoid, c.CommentID)

	middleware.WriteJSON(w, http.StatusOK, c)
}

// DELETE /api/v1/comments/:commentid  (requires auth; only the author may delete)
func (h *CommentsHandler) Delete(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	commentid := ps.ByName("commentid")
	userID := middleware.MustUserID(r.Context())

	var authorID string
	err := h.DB.QueryRow(r.Context(), `
		SELECT author_userid::text FROM comments WHERE commentid=$1 AND deleted_at IS NULL
	`, commentid).Scan(&authorID)
	if err == pgx.ErrNoRows {
		middleware.WriteError(w, http.StatusNotFound, "comment not found")
		return
	}
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	if authorID != userID {
		middleware.WriteError(w, http.StatusForbidden, "you may only delete your own comments")
		return
	}

	// Soft-delete: the trigger will decrement the parent's reply_count
	_, err = h.DB.Exec(r.Context(), `
		UPDATE comments SET deleted_at=NOW() WHERE commentid=$1
	`, commentid)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
