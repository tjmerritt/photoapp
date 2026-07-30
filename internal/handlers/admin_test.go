package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// adminExhibitionEntry/adminPhotoEntry/adminUserEntry mirror the unexported
// response shapes admin.go's endpoints return.
type adminExhibitionEntry struct {
	ExhibitionID string `json:"exhibitionid"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
}

type adminPhotoEntry struct {
	PhotoID  string `json:"photoid"`
	ImageURL string `json:"imageurl"`
	Title    string `json:"title"`
	IsPublic bool   `json:"is_public"`
}

type adminUserEntry struct {
	UserID               string `json:"userid"`
	Username             string `json:"username"`
	Email                string `json:"email"`
	AccountEnabled       bool   `json:"account_enabled"`
	CanViewPrivate       bool   `json:"can_view_private"`
	CanManageOwnLabels   bool   `json:"can_manage_own_labels"`
	CanManageOwnEmoji    bool   `json:"can_manage_own_emoji"`
	CanManageOwnComments bool   `json:"can_manage_own_comments"`
}

// adminFixture creates an exhibition, an admin user (granted PermAdmin,
// scoped to the exhibition, and added as a user_exhibitions member so
// ListExhibitions/ListUsers/Stats' user_count can see them), and one plain
// exhibition member with no admin grant.
func adminFixture(t *testing.T, env *testEnv) (exhibitionID, admin, member string) {
	t.Helper()
	exhibitionID = testutil.CreateExhibition(t, env.Pool)
	admin = testutil.CreateUser(t, env.Pool)
	member = testutil.CreateUser(t, env.Pool)
	testutil.AddUserToExhibition(t, env.Pool, admin, exhibitionID)
	testutil.AddUserToExhibition(t, env.Pool, member, exhibitionID)

	role := testutil.CreateRole(t, env.Pool, exhibitionID, "Admin", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{
		EntityType: permissions.EntityUser, EntityRef: admin, ExhibitionID: exhibitionID,
	})
	return exhibitionID, admin, member
}

func TestAdminHandler_ListExhibitions(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, _ := adminFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/exhibitions", admin, exhibitionID, nil, nil, h.ListExhibitions)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Exhibitions []adminExhibitionEntry `json:"exhibitions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// ListExhibitions is scoped to this specific admin's own user_exhibitions
	// membership rows, which only this test created for this randomly
	// generated admin — safe to assert an exact count.
	if len(resp.Exhibitions) != 1 || resp.Exhibitions[0].ExhibitionID != exhibitionID {
		t.Fatalf("got %+v, want exactly [%s]", resp.Exhibitions, exhibitionID)
	}
}

func TestAdminHandler_ListExhibitions_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, _, member := adminFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/exhibitions", member, exhibitionID, nil, nil, h.ListExhibitions)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAdminHandler_ListPhotos_ScopedToExhibition(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, _ := adminFixture(t, env)
	otherExhibitionID := testutil.CreateExhibition(t, env.Pool)

	photoA := testutil.CreatePhoto(t, env.Pool, exhibitionID, admin)
	testutil.CreatePhoto(t, env.Pool, otherExhibitionID, admin) // different exhibition, must not show up

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/photos?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.ListPhotos)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Total  int               `json:"total"`
		Photos []adminPhotoEntry `json:"photos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || len(resp.Photos) != 1 || resp.Photos[0].PhotoID != photoA {
		t.Fatalf("got %+v, want exactly the one photo in this exhibition", resp)
	}
}

func TestAdminHandler_ListPhotos_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, _, member := adminFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/photos", member, exhibitionID, nil, nil, h.ListPhotos)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAdminHandler_SetPublic_TogglesFlagAndSyncsLabel(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, _ := adminFixture(t, env)
	photoID := testutil.CreatePhoto(t, env.Pool, exhibitionID, admin) // private by default

	body, _ := json.Marshal(map[string]bool{"is_public": true})
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/photo?photoid="+photoID, admin, exhibitionID, bytes.NewReader(body), nil, h.SetPublic)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("SetPublic(true): status = %d, body = %s", rec.Code, rec.Body)
	}

	var isPublic bool
	if err := env.Pool.QueryRow(context.Background(), `SELECT is_public FROM photos WHERE photoid = $1::uuid`, photoID).Scan(&isPublic); err != nil {
		t.Fatalf("query is_public: %v", err)
	}
	if !isPublic {
		t.Error("is_public was not set to true")
	}
	var labelValue string
	if err := env.Pool.QueryRow(context.Background(),
		`SELECT value FROM labels WHERE photoid = $1::uuid AND name = 'Public' AND deleted_at IS NULL`, photoID,
	).Scan(&labelValue); err != nil {
		t.Fatalf("query Public label: %v", err)
	}
	if labelValue != "True" {
		t.Errorf("Public label value = %q, want %q", labelValue, "True")
	}

	// Flip back to false — must UPDATE the existing label row, not insert a
	// second one.
	body, _ = json.Marshal(map[string]bool{"is_public": false})
	rec = doRequest(t, http.MethodPatch, "/api/v1/admin/photo?photoid="+photoID, admin, exhibitionID, bytes.NewReader(body), nil, h.SetPublic)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("SetPublic(false): status = %d, body = %s", rec.Code, rec.Body)
	}
	var labelCount int
	if err := env.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM labels WHERE photoid = $1::uuid AND name = 'Public' AND deleted_at IS NULL`, photoID,
	).Scan(&labelCount); err != nil {
		t.Fatalf("count Public labels: %v", err)
	}
	if labelCount != 1 {
		t.Errorf("Public label count = %d, want 1 (updated in place, not duplicated)", labelCount)
	}
}

func TestAdminHandler_SetPublic_RequiresPhotoID(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, _ := adminFixture(t, env)

	body, _ := json.Marshal(map[string]bool{"is_public": true})
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/photo", admin, exhibitionID, bytes.NewReader(body), nil, h.SetPublic)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestAdminHandler_SetPublic_NotFound(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, _ := adminFixture(t, env)

	body, _ := json.Marshal(map[string]bool{"is_public": true})
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/photo?photoid=00000000-0000-0000-0000-000000000000", admin, exhibitionID, bytes.NewReader(body), nil, h.SetPublic)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestAdminHandler_Stats_ExhibitionScopedCounts(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, _ := adminFixture(t, env) // admin + member already added as members
	testutil.CreatePhoto(t, env.Pool, exhibitionID, admin)
	testutil.CreatePhoto(t, env.Pool, exhibitionID, admin)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/stats?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.Stats)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var stats struct {
		UserCount  int `json:"user_count"`
		PhotoCount int `json:"photo_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// UserCount/PhotoCount are scoped to this specific fresh exhibitionid,
	// so exact assertions are safe. ActiveLabelCount/ActiveEmojiCount are
	// deliberately not asserted on: label_names/emoji_types are site-wide
	// tables with no exhibitionid column (see Stats's own doc comment), so
	// their counts reflect every test's data, not just this one.
	if stats.UserCount != 2 {
		t.Errorf("UserCount = %d, want 2 (admin + member)", stats.UserCount)
	}
	if stats.PhotoCount != 2 {
		t.Errorf("PhotoCount = %d, want 2", stats.PhotoCount)
	}
}

func TestAdminHandler_Stats_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, _, member := adminFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/stats?exhibitionid="+exhibitionID, member, exhibitionID, nil, nil, h.Stats)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAdminHandler_ListUsers_ScopedToExhibitionMembership(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, member := adminFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/users?exhibitionid="+exhibitionID, admin, exhibitionID, nil, nil, h.ListUsers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Total int              `json:"total"`
		Users []adminUserEntry `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Scoped to user_exhibitions membership in this specific fresh
	// exhibition, so an exact count of 2 (admin + member) is safe.
	if resp.Total != 2 || len(resp.Users) != 2 {
		t.Fatalf("got %+v, want exactly 2 users (admin + member)", resp)
	}
	for _, u := range resp.Users {
		if u.UserID == member && u.CanViewPrivate {
			t.Error("member.CanViewPrivate = true, want false (never granted)")
		}
	}
}

func TestAdminHandler_ListUsers_RequiresExhibitionID(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	// ListUsers checks HasAny(..., exhibitionID, ...) *before* checking
	// whether exhibitionID is empty — an exhibition-scoped grant (like
	// adminFixture's) would fail that check first and produce 403, not the
	// 400 this test wants, since it wouldn't apply to an empty
	// exhibitionID. Needs a grant with no exhibitionid at all to reach the
	// 400 path. Unlike permissions_test.go's global-Public-grant case, this
	// is scoped to one specific User entity_ref (this test's own randomly
	// generated admin), not to Public/LoggedIn — no other test's checks for
	// any other user are affected even without cleanup, but delete it
	// anyway for tidiness.
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	admin := testutil.CreateUser(t, env.Pool)
	role := testutil.CreateRole(t, env.Pool, exhibitionID, "GlobalAdmin", permissions.PermAdmin)
	testutil.Grant(t, env.Pool, role, testutil.GrantOptions{EntityType: permissions.EntityUser, EntityRef: admin})
	t.Cleanup(func() {
		if _, err := env.Pool.Exec(context.Background(), `DELETE FROM roles WHERE roleid = $1`, role); err != nil {
			t.Errorf("cleanup: delete global-grant role: %v", err)
		}
	})

	// No exhibitionid query param and no exhibition context.
	rec := doRequest(t, http.MethodGet, "/api/v1/admin/users", admin, "", nil, nil, h.ListUsers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestAdminHandler_UpdateUser_TogglesFlagsAndCanViewPrivate(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, member := adminFixture(t, env)

	body, _ := json.Marshal(map[string]bool{
		"can_manage_own_labels": true,
		"can_view_private":      true,
	})
	params := httprouter.Params{{Key: "userid", Value: member}}
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/users/"+member+"?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(body), params, h.UpdateUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var canManageOwnLabels bool
	if err := env.Pool.QueryRow(context.Background(), `SELECT can_manage_own_labels FROM users WHERE userid = $1::uuid`, member).Scan(&canManageOwnLabels); err != nil {
		t.Fatalf("query can_manage_own_labels: %v", err)
	}
	if !canManageOwnLabels {
		t.Error("can_manage_own_labels was not set to true")
	}

	has, err := env.Checker.HasDirectUserGrant(context.Background(), exhibitionID, member, permissions.PermPrivatePhotoView)
	if err != nil {
		t.Fatalf("HasDirectUserGrant: %v", err)
	}
	if !has {
		t.Error("can_view_private=true did not grant PermPrivatePhotoView via the singleton mechanism")
	}

	// Flip can_view_private back off — must revoke, not just leave the grant dangling.
	body, _ = json.Marshal(map[string]bool{"can_view_private": false})
	rec = doRequest(t, http.MethodPatch, "/api/v1/admin/users/"+member+"?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(body), params, h.UpdateUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	has, err = env.Checker.HasDirectUserGrant(context.Background(), exhibitionID, member, permissions.PermPrivatePhotoView)
	if err != nil {
		t.Fatalf("HasDirectUserGrant (after revoke): %v", err)
	}
	if has {
		t.Error("can_view_private=false did not revoke the grant")
	}
}

func TestAdminHandler_UpdateUser_CannotDisableOwnAccount(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, admin, _ := adminFixture(t, env)

	body, _ := json.Marshal(map[string]bool{"account_enabled": false})
	params := httprouter.Params{{Key: "userid", Value: admin}}
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/users/"+admin+"?exhibitionid="+exhibitionID, admin, exhibitionID, bytes.NewReader(body), params, h.UpdateUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (admin cannot disable their own account)", rec.Code, http.StatusBadRequest)
	}
}

func TestAdminHandler_UpdateUser_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.AdminHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	exhibitionID, _, member := adminFixture(t, env)

	body, _ := json.Marshal(map[string]bool{"account_enabled": false})
	params := httprouter.Params{{Key: "userid", Value: member}}
	rec := doRequest(t, http.MethodPatch, "/api/v1/admin/users/"+member+"?exhibitionid="+exhibitionID, member, exhibitionID, bytes.NewReader(body), params, h.UpdateUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
