package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/models"
)

// createLabel POSTs a label onto fx's photo as fx.owner, mirroring what
// labels_test.go's TestLabelsHandler_CreateAndList does inline, factored out
// here since several of these admin/catalog-endpoint tests need a real
// `labels` row to exist (Names/AdminListNames/Values all read off `labels`,
// not off `label_names` directly) without caring about the Create response.
func createLabel(t *testing.T, env *testEnv, h *handlers.LabelsHandler, fx labelsFixture, name, value string) models.Label {
	t.Helper()
	body, _ := json.Marshal(models.AddLabelRequest{Name: name, Value: value})
	rec := doRequest(t, http.MethodPost, "/api/v1/labels?photoid="+fx.photoID, fx.owner, fx.exhibitionID, bytes.NewReader(body), nil, h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("createLabel(%q, %q): status = %d, body = %s", name, value, rec.Code, rec.Body)
	}
	var created models.Label
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("createLabel: decode: %v", err)
	}
	return created
}

// ── Names (GET /api/v1/label-names, public) ─────────────────────────────────

func TestLabelsHandler_Names(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	// label_names is a global catalog keyed by name alone (see
	// labels_test.go's TestLabelsHandler_Create_RestrictedName_RequiresLabelAdmin),
	// so — like that test — this name has to be unique across every
	// concurrently-running test on its own, not scoped by exhibition.
	name := "Camera-" + uuid.NewString()
	createLabel(t, env, h, fx, name, "Nikon")

	// Public endpoint — no permission grant, no exhibition context needed.
	rec := doRequest(t, http.MethodGet, "/api/v1/label-names", "", "", nil, nil, h.Names)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Names []models.LabelNameInfo `json:"names"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got *models.LabelNameInfo
	for i := range resp.Names {
		if resp.Names[i].Name == name {
			got = &resp.Names[i]
		}
	}
	if got == nil {
		t.Fatalf("name %q not found in %d names", name, len(resp.Names))
	}
	// No label_names row was ever inserted for this fresh name, so it should
	// carry fetchLabelNameInfo's documented zero-value defaults: no color
	// override, not restricted, enabled.
	if got.ColorHex != nil || got.Restricted || !got.Enabled {
		t.Errorf("got %+v, want ColorHex=nil Restricted=false Enabled=true (no label_names row exists for this name yet)", got)
	}
}

// ── AdminListNames (GET /api/v1/admin/label-names) ──────────────────────────

func TestLabelsHandler_AdminListNames(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	name := "Camera-" + uuid.NewString()
	createLabel(t, env, h, fx, name, "Nikon")
	createLabel(t, env, h, fx, name, "Canon") // same name, different value/photo — usage_count should be 2

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/label-names?search="+name, fx.labelAdmin, fx.exhibitionID, nil, nil, h.AdminListNames)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Names []models.LabelNameInfo `json:"names"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The search filter scopes this to exactly the name just created, so —
	// unlike Names above — an exact-length assertion is safe here even
	// though label_names is a global, unscoped catalog.
	if len(resp.Names) != 1 || resp.Names[0].Name != name || resp.Names[0].UsageCount != 2 {
		t.Fatalf("got %+v, want exactly one name %q with usage_count=2", resp.Names, name)
	}
}

func TestLabelsHandler_AdminListNames_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	rec := doRequest(t, http.MethodGet, "/api/v1/admin/label-names", fx.owner, fx.exhibitionID, nil, nil, h.AdminListNames)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// ── UpdateName (PATCH /api/v1/label-names?name=) ────────────────────────────

func TestLabelsHandler_UpdateName_SetsFieldsAndPartialUpdatePreservesOthers(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)
	name := "Camera-" + uuid.NewString()
	createLabel(t, env, h, fx, name, "Nikon")

	body, _ := json.Marshal(map[string]any{"color_hex": "#ff00ff", "restricted": true})
	rec := doRequest(t, http.MethodPatch, "/api/v1/label-names?name="+name, fx.labelAdmin, fx.exhibitionID, bytes.NewReader(body), nil, h.UpdateName)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp models.LabelNameInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ColorHex == nil || *resp.ColorHex != "#ff00ff" || !resp.Restricted || !resp.Enabled {
		t.Fatalf("got %+v, want ColorHex=#ff00ff Restricted=true Enabled=true (untouched, defaults true)", resp)
	}

	// A second PATCH that only sets enabled must leave color_hex/restricted
	// as they were, per UpdateName's ON CONFLICT ... CASE WHEN $n THEN ...
	// ELSE label_names.<col> END pattern — this is the one part of the
	// handler most likely to regress silently (e.g. if a future edit
	// accidentally always overwrote every column).
	body2, _ := json.Marshal(map[string]any{"enabled": false})
	rec = doRequest(t, http.MethodPatch, "/api/v1/label-names?name="+name, fx.labelAdmin, fx.exhibitionID, bytes.NewReader(body2), nil, h.UpdateName)
	if rec.Code != http.StatusOK {
		t.Fatalf("second PATCH: status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp2 models.LabelNameInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp2.ColorHex == nil || *resp2.ColorHex != "#ff00ff" || !resp2.Restricted || resp2.Enabled {
		t.Fatalf("got %+v, want ColorHex/Restricted unchanged from the first PATCH and Enabled=false", resp2)
	}

	// Clearing color_hex with an empty string clears the override rather
	// than being rejected as an invalid hex value.
	body3, _ := json.Marshal(map[string]any{"color_hex": ""})
	rec = doRequest(t, http.MethodPatch, "/api/v1/label-names?name="+name, fx.labelAdmin, fx.exhibitionID, bytes.NewReader(body3), nil, h.UpdateName)
	if rec.Code != http.StatusOK {
		t.Fatalf("third PATCH: status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp3 models.LabelNameInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &resp3); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp3.ColorHex != nil {
		t.Errorf("ColorHex = %v after clearing with an empty string, want nil", *resp3.ColorHex)
	}
}

func TestLabelsHandler_UpdateName_RequiresName(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)

	body, _ := json.Marshal(map[string]any{"restricted": true})
	rec := doRequest(t, http.MethodPatch, "/api/v1/label-names", fx.labelAdmin, fx.exhibitionID, bytes.NewReader(body), nil, h.UpdateName)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestLabelsHandler_UpdateName_RequiresAtLeastOneField(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)
	name := "Camera-" + uuid.NewString()

	rec := doRequest(t, http.MethodPatch, "/api/v1/label-names?name="+name, fx.labelAdmin, fx.exhibitionID, bytes.NewReader([]byte(`{}`)), nil, h.UpdateName)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestLabelsHandler_UpdateName_RejectsInvalidColor(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)
	name := "Camera-" + uuid.NewString()

	body, _ := json.Marshal(map[string]any{"color_hex": "not-a-color"})
	rec := doRequest(t, http.MethodPatch, "/api/v1/label-names?name="+name, fx.labelAdmin, fx.exhibitionID, bytes.NewReader(body), nil, h.UpdateName)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestLabelsHandler_UpdateName_RequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)
	name := "Camera-" + uuid.NewString()

	body, _ := json.Marshal(map[string]any{"restricted": true})
	rec := doRequest(t, http.MethodPatch, "/api/v1/label-names?name="+name, fx.owner, fx.exhibitionID, bytes.NewReader(body), nil, h.UpdateName)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// ── Values (GET /api/v1/label-values?name=, public) ─────────────────────────

func TestLabelsHandler_Values(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}
	fx := setupLabelsFixture(t, env)
	name := "Camera-" + uuid.NewString()
	createLabel(t, env, h, fx, name, "Nikon")
	createLabel(t, env, h, fx, name, "Canon")
	createLabel(t, env, h, fx, name, "Nikon") // duplicate value — DISTINCT should collapse it

	rec := doRequest(t, http.MethodGet, "/api/v1/label-values?name="+name, "", "", nil, nil, h.Values)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp struct {
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Values) != 2 || resp.Values[0] != "Canon" || resp.Values[1] != "Nikon" {
		t.Fatalf("Values = %v, want [Canon Nikon] (distinct, alphabetical)", resp.Values)
	}
}

func TestLabelsHandler_Values_RequiresName(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.LabelsHandler{DB: env.Pool, Cfg: env.Cfg, Checker: env.Checker}

	rec := doRequest(t, http.MethodGet, "/api/v1/label-values", "", "", nil, nil, h.Values)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
