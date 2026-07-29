package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

type commentsFixture struct {
	exhibitionID string
	photoID      string
	author       string
	other        string
	admin        string
}

// setupCommentsFixture grants PhotoCommentView/Create/Modify/Delete to every
// logged-in user (mirrors the seeded Contributor role) and nothing to
// Public, plus a separate Admin (comments.go's non-author override requires
// PermAdmin specifically, unlike labels' PermLabelAdmin).
func setupCommentsFixture(t *testing.T, env *testEnv) commentsFixture {
	t.Helper()

	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	owner := testutil.CreateUser(t, env.Pool)
	author := testutil.CreateUser(t, env.Pool)
	other := testutil.CreateUser(t, env.Pool)
	admin := testutil.CreateUser(t, env.Pool)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, owner)

	contributor := testutil.CreateRole(t, env.Pool, exhibitionID, "Contributor",
		permissions.PermPhotoCommentView, permissions.PermPhotoCommentCreate,
		permissions.PermPhotoCommentModify, permissions.PermPhotoCommentDelete)
	testutil.Grant(t, env.Pool, contributor, testutil.GrantOptions{
		EntityType: permissions.EntityLoggedIn, ExhibitionID: exhibitionID,
	})

	adminRole := testutil.CreateRole(t, env.Pool, exhibitionID, "Admin", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, adminRole, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})

	return commentsFixture{exhibitionID: exhibitionID, photoID: photoID, author: author, other: other, admin: admin}
}

func TestCommentsHandler_CreateAndList(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.CommentsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupCommentsFixture(t, env)

	body, _ := json.Marshal(models.AddCommentRequest{Comment: "Great shot!"})
	rec := doRequest(t, http.MethodPost, "/api/v1/comments?photoid="+fx.photoID, fx.author, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created models.Comment
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: decode: %v", err)
	}
	if created.Comment != "Great shot!" || created.Author.UserID != fx.author {
		t.Fatalf("Create: got %+v", created)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/comments?photoid="+fx.photoID, fx.author, fx.exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: status = %d, body = %s", rec.Code, rec.Body)
	}
	var listResp models.CommentsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: decode: %v", err)
	}
	if len(listResp.Comments) != 1 || listResp.Comments[0].CommentID != created.CommentID {
		t.Fatalf("List: got %+v, want exactly the comment just created", listResp.Comments)
	}
}

func TestCommentsHandler_Create_EmptyOrWhitespaceRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.CommentsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupCommentsFixture(t, env)

	body, _ := json.Marshal(models.AddCommentRequest{Comment: "   "})
	rec := doRequest(t, http.MethodPost, "/api/v1/comments?photoid="+fx.photoID, fx.author, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCommentsHandler_Create_ReplyToWrongPhotoRejected(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.CommentsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupCommentsFixture(t, env)

	otherPhoto := testutil.CreatePhoto(t, env.Pool, fx.exhibitionID, fx.author)

	body, _ := json.Marshal(models.AddCommentRequest{Comment: "top-level on other photo"})
	rec := doRequest(t, http.MethodPost, "/api/v1/comments?photoid="+otherPhoto, fx.author, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	var parent models.Comment
	_ = json.Unmarshal(rec.Body.Bytes(), &parent)

	replyBody, _ := json.Marshal(models.AddCommentRequest{Comment: "reply pointing at the wrong photo"})
	rec = doRequest(t, http.MethodPost, "/api/v1/comments?photoid="+fx.photoID+"&parentid="+parent.CommentID, fx.author, fx.exhibitionID, bytes.NewReader(replyBody), nil, h.Create)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (parent belongs to a different photo)", rec.Code, http.StatusBadRequest)
	}
}

func TestCommentsHandler_Create_ReplyIncrementsParentReplyCount(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.CommentsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupCommentsFixture(t, env)

	body, _ := json.Marshal(models.AddCommentRequest{Comment: "parent"})
	rec := doRequest(t, http.MethodPost, "/api/v1/comments?photoid="+fx.photoID, fx.author, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	var parent models.Comment
	_ = json.Unmarshal(rec.Body.Bytes(), &parent)

	replyBody, _ := json.Marshal(models.AddCommentRequest{Comment: "reply"})
	rec = doRequest(t, http.MethodPost, "/api/v1/comments?photoid="+fx.photoID+"&parentid="+parent.CommentID, fx.other, fx.exhibitionID, bytes.NewReader(replyBody), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply Create: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodGet, "/api/v1/comments?photoid="+fx.photoID, fx.author, fx.exhibitionID, nil, nil, h.List)
	var listResp models.CommentsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	for _, c := range listResp.Comments {
		if c.CommentID == parent.CommentID && c.ReplyCount != 1 {
			t.Errorf("parent ReplyCount = %d, want 1 after one reply", c.ReplyCount)
		}
	}
}

func TestCommentsHandler_Update_AuthorAllowed_OthersForbidden_AdminAllowed(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.CommentsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupCommentsFixture(t, env)

	body, _ := json.Marshal(models.AddCommentRequest{Comment: "original"})
	rec := doRequest(t, http.MethodPost, "/api/v1/comments?photoid="+fx.photoID, fx.author, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	var created models.Comment
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	params := httprouter.Params{{Key: "commentid", Value: created.CommentID}}

	updateBody, _ := json.Marshal(models.UpdateCommentRequest{Comment: "edited by stranger"})
	rec = doRequest(t, http.MethodPatch, "/api/v1/comments/"+created.CommentID, fx.other, fx.exhibitionID, bytes.NewReader(updateBody), params, h.Update)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-author: status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	updateBody2, _ := json.Marshal(models.UpdateCommentRequest{Comment: "edited by author"})
	rec = doRequest(t, http.MethodPatch, "/api/v1/comments/"+created.CommentID, fx.author, fx.exhibitionID, bytes.NewReader(updateBody2), params, h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("author: status = %d, body = %s", rec.Code, rec.Body)
	}

	updateBody3, _ := json.Marshal(models.UpdateCommentRequest{Comment: "edited by admin"})
	rec = doRequest(t, http.MethodPatch, "/api/v1/comments/"+created.CommentID, fx.admin, fx.exhibitionID, bytes.NewReader(updateBody3), params, h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestCommentsHandler_Delete_AuthorAllowed_OthersForbidden(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.CommentsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupCommentsFixture(t, env)

	body, _ := json.Marshal(models.AddCommentRequest{Comment: "to be deleted"})
	rec := doRequest(t, http.MethodPost, "/api/v1/comments?photoid="+fx.photoID, fx.author, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	var created models.Comment
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	params := httprouter.Params{{Key: "commentid", Value: created.CommentID}}

	rec = doRequest(t, http.MethodDelete, "/api/v1/comments/"+created.CommentID, fx.other, fx.exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-author: status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/comments/"+created.CommentID, fx.author, fx.exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("author: status = %d, body = %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, http.MethodDelete, "/api/v1/comments/"+created.CommentID, fx.author, fx.exhibitionID, nil, params, h.Delete)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("already deleted: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestCommentsHandler_List_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.CommentsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupCommentsFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/comments?photoid="+fx.photoID, "", fx.exhibitionID, nil, nil, h.List)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
