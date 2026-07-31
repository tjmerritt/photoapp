package handlers

// Pure, DB-free and network-free unit tests for the small helper functions
// scattered across the handlers package. Deliberately `package handlers`
// (not handlers_test) so these can reach the unexported functions directly
// without needing a database or an HTTP round-trip through a real handler.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tjmerritt/photoapp/internal/photoimport"
)

// ── pagination.go ────────────────────────────────────────────────────────────

func TestParsePage(t *testing.T) {
	cases := []struct {
		name             string
		query            string
		defaultL, maxL   int
		wantOff, wantLim int
	}{
		{"defaults when absent", "", 10, 100, 0, 10},
		{"explicit values", "offset=20&limit=5", 10, 100, 20, 5},
		{"negative offset clamped to 0", "offset=-5", 10, 100, 0, 10},
		{"non-positive limit falls back to default", "limit=0", 10, 100, 0, 10},
		{"limit above max is clamped", "limit=500", 10, 100, 0, 100},
		{"garbage values ignored", "offset=abc&limit=xyz", 10, 100, 0, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
			offset, limit := parsePage(r, tc.defaultL, tc.maxL)
			if offset != tc.wantOff || limit != tc.wantLim {
				t.Errorf("parsePage(%q) = (%d, %d), want (%d, %d)", tc.query, offset, limit, tc.wantOff, tc.wantLim)
			}
		})
	}
}

func TestBuildPages(t *testing.T) {
	p := buildPages(25, 10, 10, "/api/v1/labels?photoid=x&limit=10")

	if p.Count != 3 {
		t.Errorf("Count = %d, want 3", p.Count)
	}
	if p.Current != 2 {
		t.Errorf("Current = %d, want 2", p.Current)
	}
	if p.First != "/api/v1/labels?photoid=x&limit=10&offset=0" {
		t.Errorf("First = %q", p.First)
	}
	if p.Last != "/api/v1/labels?photoid=x&limit=10&offset=20" {
		t.Errorf("Last = %q", p.Last)
	}
	if p.Next == nil || *p.Next != "/api/v1/labels?photoid=x&limit=10&offset=20" {
		t.Errorf("Next = %v, want offset=20", p.Next)
	}
	if p.Prev == nil || *p.Prev != "/api/v1/labels?photoid=x&limit=10&offset=0" {
		t.Errorf("Prev = %v, want offset=0", p.Prev)
	}
}

func TestBuildPages_FirstPage_NoPrev(t *testing.T) {
	p := buildPages(25, 0, 10, "/x?limit=10")
	if p.Prev != nil {
		t.Errorf("Prev = %v, want nil on the first page", *p.Prev)
	}
	if p.Next == nil {
		t.Error("Next = nil, want a link to the next page")
	}
}

func TestBuildPages_LastPage_NoNext(t *testing.T) {
	p := buildPages(25, 20, 10, "/x?limit=10")
	if p.Next != nil {
		t.Errorf("Next = %v, want nil on the last page", *p.Next)
	}
}

func TestBuildPages_ZeroTotal_StillOnePage(t *testing.T) {
	p := buildPages(0, 0, 10, "/x?limit=10")
	if p.Count != 1 {
		t.Errorf("Count = %d, want 1 even with zero results", p.Count)
	}
}

func TestMax(t *testing.T) {
	if max(3, 5) != 5 {
		t.Error("max(3, 5) != 5")
	}
	if max(5, 3) != 5 {
		t.Error("max(5, 3) != 5")
	}
	if max(4, 4) != 4 {
		t.Error("max(4, 4) != 4")
	}
}

// ── search.go ─────────────────────────────────────────────────────────────────

func TestParseSearchQuery_AtUsername(t *testing.T) {
	pq := parseSearchQuery("@alice")
	if len(pq.Users) != 1 || pq.Users[0] != "alice" {
		t.Errorf("Users = %v, want [alice]", pq.Users)
	}
}

func TestParseSearchQuery_UserPrefix(t *testing.T) {
	pq := parseSearchQuery("user:@bob user:carol")
	if len(pq.Users) != 2 || pq.Users[0] != "bob" || pq.Users[1] != "carol" {
		t.Errorf("Users = %v, want [bob carol]", pq.Users)
	}
}

func TestParseSearchQuery_LabelNameOnly(t *testing.T) {
	pq := parseSearchQuery("label:Location")
	if len(pq.Labels) != 1 || pq.Labels[0].Name != "location" || pq.Labels[0].Value != "" {
		t.Errorf("Labels = %+v, want [{location }]", pq.Labels)
	}
}

func TestParseSearchQuery_LabelNameEqualsValue(t *testing.T) {
	pq := parseSearchQuery("label:Location=Yosemite")
	if len(pq.Labels) != 1 || pq.Labels[0].Name != "location" || pq.Labels[0].Value != "yosemite" {
		t.Errorf("Labels = %+v, want [{location yosemite}]", pq.Labels)
	}
}

func TestParseSearchQuery_NameColonValueShorthand(t *testing.T) {
	pq := parseSearchQuery("Location:Yosemite")
	if len(pq.Labels) != 1 || pq.Labels[0].Name != "location" || pq.Labels[0].Value != "yosemite" {
		t.Errorf("Labels = %+v, want [{location yosemite}] via shorthand", pq.Labels)
	}
}

func TestParseSearchQuery_ReservedPrefixNotTreatedAsLabelShorthand(t *testing.T) {
	// "title:" is a reserved qualifier, not a Name:Value label shorthand.
	pq := parseSearchQuery("title:Sunset")
	if len(pq.Labels) != 0 {
		t.Errorf("Labels = %+v, want none (title: is reserved)", pq.Labels)
	}
	if len(pq.TitleTexts) != 1 || pq.TitleTexts[0] != "sunset" {
		t.Errorf("TitleTexts = %v, want [sunset]", pq.TitleTexts)
	}
}

func TestParseSearchQuery_EmojiCommentDescQualifiers(t *testing.T) {
	pq := parseSearchQuery("emoji:heart comment:nice desc:mountains description:snow")
	if len(pq.EmojiNames) != 1 || pq.EmojiNames[0] != "heart" {
		t.Errorf("EmojiNames = %v", pq.EmojiNames)
	}
	if len(pq.CommentTexts) != 1 || pq.CommentTexts[0] != "nice" {
		t.Errorf("CommentTexts = %v", pq.CommentTexts)
	}
	if len(pq.DescTexts) != 2 || pq.DescTexts[0] != "mountains" || pq.DescTexts[1] != "snow" {
		t.Errorf("DescTexts = %v, want [mountains snow] (desc: and description: both populate it)", pq.DescTexts)
	}
}

func TestParseSearchQuery_FreeTextDedupedAndLowercased(t *testing.T) {
	pq := parseSearchQuery("Sunset sunset MOUNTAIN")
	if len(pq.FreeTerms) != 2 || pq.FreeTerms[0] != "sunset" || pq.FreeTerms[1] != "mountain" {
		t.Errorf("FreeTerms = %v, want [sunset mountain] (case-folded, deduped)", pq.FreeTerms)
	}
}

func TestParsedQuery_IsEmpty(t *testing.T) {
	if !parseSearchQuery("   ").isEmpty() {
		t.Error("isEmpty() = false for a query with only whitespace")
	}
	if parseSearchQuery("sunset").isEmpty() {
		t.Error("isEmpty() = true for a non-empty free-text query")
	}
}

func TestBuildSearchSQL_FixedParamsAlwaysFirstFour(t *testing.T) {
	pq := parseSearchQuery("sunset")
	sql, args := buildSearchSQL(pq, "exhib-1", true, "user-1", true)

	if len(args) < 4 || args[0] != "exhib-1" || args[1] != true || args[2] != "user-1" || args[3] != true {
		t.Fatalf("args[:4] = %v, want [exhib-1 true user-1 true]", args[:min(4, len(args))])
	}
	if sql == "" {
		t.Fatal("buildSearchSQL returned empty SQL")
	}
}

func TestBuildSearchSQL_FreeTextAddsScoringCTE(t *testing.T) {
	pq := parseSearchQuery("sunset")
	sql, _ := buildSearchSQL(pq, "", false, "", false)
	if !strings.Contains(sql, "WITH terms(term)") {
		t.Error("expected a scoring CTE when free text terms are present")
	}
}

func TestBuildSearchSQL_NoFreeText_NoScoringCTE(t *testing.T) {
	pq := parseSearchQuery("label:Location")
	sql, _ := buildSearchSQL(pq, "", false, "", false)
	if strings.Contains(sql, "WITH terms(term)") {
		t.Error("did not expect a scoring CTE when there are no free text terms")
	}
}

func TestBuildSearchSQL_LabelFilterAddsJoinAndParams(t *testing.T) {
	pq := parseSearchQuery("label:Location=Yosemite")
	sql, args := buildSearchSQL(pq, "", false, "", false)
	if !strings.Contains(sql, "JOIN labels lf0") {
		t.Errorf("expected a labels JOIN for the label filter, got:\n%s", sql)
	}
	// exhibitionID, canSeePrivate, currentUserID, hasPhotoView, name, value
	if len(args) != 6 || args[4] != "location" || args[5] != "yosemite" {
		t.Errorf("args = %v, want [.. .. .. .. location yosemite]", args)
	}
}

// ── avatar.go ─────────────────────────────────────────────────────────────────

func TestEmailHash_DeterministicAndNormalized(t *testing.T) {
	a := emailHash("Person@Example.com")
	b := emailHash("  person@example.com  ")
	if a != b {
		t.Errorf("emailHash not case/whitespace normalized: %q != %q", a, b)
	}
	if len(a) != 32 {
		t.Errorf("emailHash length = %d, want 32 (MD5 hex)", len(a))
	}
}

func TestAvatarURL(t *testing.T) {
	got := AvatarURL("person@example.com")
	want := "/avatars/" + emailHash("person@example.com")
	if got != want {
		t.Errorf("AvatarURL = %q, want %q", got, want)
	}
}

// ── upload.go ─────────────────────────────────────────────────────────────────

func TestPrioritizeLabels(t *testing.T) {
	labels := []photoimport.Label{
		{Name: "Camera Make", Value: "Fuji"},
		{Name: "Filename", Value: "img.jpg"},
		{Name: "ISO", Value: "400"},
		{Name: "Resolution", Value: "800x600"},
	}
	out := prioritizeLabels(labels, "Filename", "Resolution")

	wantOrder := []string{"Filename", "Resolution", "Camera Make", "ISO"}
	if len(out) != len(wantOrder) {
		t.Fatalf("got %d labels, want %d", len(out), len(wantOrder))
	}
	for i, name := range wantOrder {
		if out[i].Name != name {
			t.Errorf("out[%d].Name = %q, want %q (full order: %v)", i, out[i].Name, name, out)
		}
	}
}

func TestPrioritizeLabels_MissingPriorityNameIsNoOp(t *testing.T) {
	labels := []photoimport.Label{{Name: "ISO", Value: "400"}}
	out := prioritizeLabels(labels, "Filename")
	if len(out) != 1 || out[0].Name != "ISO" {
		t.Errorf("got %+v, want the input unchanged when the priority name isn't present", out)
	}
}

func TestTitleFromFilename(t *testing.T) {
	cases := map[string]string{
		"sunset-over-mountains.jpg": "Sunset Over Mountains",
		"my_vacation_photo.png":     "My Vacation Photo",
		"IMG 1234.jpeg":             "IMG 1234",
		"noext":                     "Noext",
	}
	for in, want := range cases {
		if got := titleFromFilename(in); got != want {
			t.Errorf("titleFromFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── ratelimit.go ─────────────────────────────────────────────────────────────

func TestClientIP_PrefersXForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.5:1234")
	r.RemoteAddr = "10.0.0.1:5678"
	if got := clientIP(r); got != "203.0.113.5" {
		t.Errorf("clientIP = %q, want %q", got, "203.0.113.5")
	}
}

func TestClientIP_XForwardedForWithoutPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.5")
	if got := clientIP(r); got != "203.0.113.5" {
		t.Errorf("clientIP = %q, want %q", got, "203.0.113.5")
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "10.0.0.1:5678"
	if got := clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want %q", got, "10.0.0.1")
	}
}

func TestCheckRegistrationLimit_FirstAttemptAllowed(t *testing.T) {
	ip := fmt.Sprintf("198.51.100.%d", time.Now().UnixNano()%256) // unique-ish per run
	allowed, retryAfter := checkRegistrationLimit(ip)
	if !allowed {
		t.Error("first attempt from a fresh IP was not allowed")
	}
	if retryAfter != 0 {
		t.Errorf("retryAfter = %v on first (allowed) attempt, want 0", retryAfter)
	}
}

func TestCheckRegistrationLimit_ImmediateRetryBlocked(t *testing.T) {
	ip := fmt.Sprintf("198.51.100.%d-second", time.Now().UnixNano())
	if allowed, _ := checkRegistrationLimit(ip); !allowed {
		t.Fatal("first attempt should be allowed")
	}
	allowed, retryAfter := checkRegistrationLimit(ip)
	if allowed {
		t.Error("immediate second attempt was allowed, want rate-limited")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want > 0 when rate-limited", retryAfter)
	}
}

// ── resolve.go ────────────────────────────────────────────────────────────────

func TestSyncMap_LoadMissReturnsFalse(t *testing.T) {
	var m syncMap[string, string]
	if _, ok := m.Load("missing"); ok {
		t.Error("Load on an empty map returned ok=true")
	}
}

func TestSyncMap_StoreThenLoad(t *testing.T) {
	var m syncMap[string, string]
	m.Store("key", "value")
	v, ok := m.Load("key")
	if !ok || v != "value" {
		t.Errorf("Load() = (%q, %v), want (\"value\", true)", v, ok)
	}
}

func TestSyncMap_StoreOverwrites(t *testing.T) {
	var m syncMap[string, int]
	m.Store("key", 1)
	m.Store("key", 2)
	v, _ := m.Load("key")
	if v != 2 {
		t.Errorf("Load() = %d, want 2 after overwriting", v)
	}
}
