package photoimport_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/google/uuid"
	"github.com/tjmerritt/photoapp/internal/photoimport"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

// ── MergeLabels ───────────────────────────────────────────────────────────────

func TestMergeLabels_ExtraOverridesOnDuplicateNameCaseInsensitive(t *testing.T) {
	base := []photoimport.Label{{Name: "Camera Make", Value: "Fuji"}, {Name: "ISO", Value: "100"}}
	extra := []photoimport.Label{{Name: "camera make", Value: "Canon"}}

	got := photoimport.MergeLabels(base, extra)

	if len(got) != 2 {
		t.Fatalf("got %d labels, want 2 (base length preserved, no extra rows for an overridden name)", len(got))
	}
	if got[0].Name != "Camera Make" || got[0].Value != "Canon" {
		t.Errorf("got[0] = %+v, want {Camera Make Canon} (extra's value, base's Name casing)", got[0])
	}
	if got[1].Value != "100" {
		t.Errorf("got[1] = %+v, want ISO unchanged", got[1])
	}
}

func TestMergeLabels_ExtraOnlyLabelsAppended(t *testing.T) {
	base := []photoimport.Label{{Name: "ISO", Value: "100"}}
	extra := []photoimport.Label{{Name: "Location", Value: "Yosemite"}}

	got := photoimport.MergeLabels(base, extra)

	if len(got) != 2 || got[0].Name != "ISO" || got[1].Name != "Location" {
		t.Errorf("got = %+v, want [ISO Location] in that order", got)
	}
}

func TestMergeLabels_EmptyExtraReturnsBaseUnchanged(t *testing.T) {
	base := []photoimport.Label{{Name: "ISO", Value: "100"}}
	got := photoimport.MergeLabels(base, nil)
	if len(got) != 1 || got[0] != base[0] {
		t.Errorf("got = %+v, want base unchanged", got)
	}
}

// ── Names ─────────────────────────────────────────────────────────────────────

func TestNames_ExtractsNameField(t *testing.T) {
	labels := []photoimport.Label{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	got := photoimport.Names(labels)
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Errorf("Names() = %v, want [A B]", got)
	}
}

func TestNames_EmptyInput(t *testing.T) {
	got := photoimport.Names(nil)
	if len(got) != 0 {
		t.Errorf("Names(nil) = %v, want empty", got)
	}
}

// ── ImageDimensions ───────────────────────────────────────────────────────────

func TestImageDimensions_DecodesPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 37, 21))
	for y := 0; y < 21; y++ {
		for x := 0; x < 37; x++ {
			img.Set(x, y, color.White)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding fixture PNG: %v", err)
	}

	w, h, err := photoimport.ImageDimensions(buf.Bytes())
	if err != nil {
		t.Fatalf("ImageDimensions: %v", err)
	}
	if w != 37 || h != 21 {
		t.Errorf("ImageDimensions = (%d, %d), want (37, 21)", w, h)
	}
}

func TestImageDimensions_ErrorsOnGarbageData(t *testing.T) {
	if _, _, err := photoimport.ImageDimensions([]byte("not an image")); err == nil {
		t.Error("ImageDimensions did not error on non-image data")
	}
}

// ── ExtractEXIF ───────────────────────────────────────────────────────────────

func TestExtractEXIF_NoMetadataReturnsNil(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding fixture PNG: %v", err)
	}

	// PNGs (and this synthetic buffer generally) carry no EXIF data — a
	// plain, un-decodable-as-EXIF file is the common case ExtractEXIF is
	// documented to handle by returning nil rather than an error.
	got := photoimport.ExtractEXIF(buf.Bytes())
	if got != nil {
		t.Errorf("ExtractEXIF = %v, want nil for a file with no EXIF data", got)
	}
}

func TestExtractEXIF_GarbageDataReturnsNilNotPanic(t *testing.T) {
	got := photoimport.ExtractEXIF([]byte("not an image at all"))
	if got != nil {
		t.Errorf("ExtractEXIF = %v, want nil", got)
	}
}

// ── MarkNamesRestricted (DB-backed) ────────────────────────────────────────────

func TestMarkNamesRestricted_UpsertsAndSkipsEmptyNames(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := t.Context()
	exhibitionID := testutil.CreateExhibition(t, pool)

	err := photoimport.MarkNamesRestricted(ctx, pool, exhibitionID, []string{"Camera Make", "", "ISO"})
	if err != nil {
		t.Fatalf("MarkNamesRestricted: %v", err)
	}

	for _, name := range []string{"Camera Make", "ISO"} {
		var restricted bool
		if err := pool.QueryRow(ctx, `SELECT restricted FROM label_names WHERE exhibitionid = $1 AND name = $2`, exhibitionID, name).Scan(&restricted); err != nil {
			t.Fatalf("querying label_names for %q: %v", name, err)
		}
		if !restricted {
			t.Errorf("label_names[%q].restricted = false, want true", name)
		}
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM label_names WHERE exhibitionid = $1 AND name = ''`, exhibitionID).Scan(&count); err != nil {
		t.Fatalf("querying label_names for empty name: %v", err)
	}
	if count != 0 {
		t.Error("MarkNamesRestricted inserted a row for the empty name, want it skipped")
	}
}

func TestMarkNamesRestricted_IsNoOpForEmptyExhibitionID(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := t.Context()

	// No resolved exhibition context (e.g. a browser upload with no
	// recognized hostname) must be a safe no-op, not an error — there's no
	// valid exhibition to attach a label_names row to.
	if err := photoimport.MarkNamesRestricted(ctx, pool, "", []string{"Camera Make"}); err != nil {
		t.Fatalf("MarkNamesRestricted with empty exhibitionID: %v", err)
	}
}

func TestMarkNamesRestricted_NeverUnrestricts(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := t.Context()
	exhibitionID := testutil.CreateExhibition(t, pool)

	// label_names is scoped per exhibition (PRIMARY KEY (exhibitionid,
	// name)); this seed row uses a plain INSERT rather than
	// MarkNamesRestricted's own ON CONFLICT upsert — so unlike "Camera
	// Make"/"ISO" in the test above (which go through the real idempotent
	// upsert and are safe to reuse indefinitely), this name needs to be
	// unique per run or a second run of this test would fail on the
	// (exhibitionid, name) uniqueness constraint.
	name := "Location-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO label_names (exhibitionid, name, restricted) VALUES ($1, $2, TRUE)`, exhibitionID, name); err != nil {
		t.Fatalf("seed label_names: %v", err)
	}

	// Calling it again (idempotent upsert) must leave restricted = TRUE, not
	// reset or clear it.
	if err := photoimport.MarkNamesRestricted(ctx, pool, exhibitionID, []string{name}); err != nil {
		t.Fatalf("MarkNamesRestricted: %v", err)
	}

	var restricted bool
	if err := pool.QueryRow(ctx, `SELECT restricted FROM label_names WHERE exhibitionid = $1 AND name = $2`, exhibitionID, name).Scan(&restricted); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !restricted {
		t.Error("restricted was cleared, want it to remain true")
	}
}

// TestMarkNamesRestricted_DoesNotReRestrictAdminUnrestrictedName covers the
// actual production bug this behavior exists to prevent: an admin using
// PATCH /api/v1/label-names to explicitly unrestrict a name (e.g. relaxing
// "Filename") must have that decision stick even after a later photo
// upload/import calls MarkNamesRestricted with that same name again. Before
// this fix, MarkNamesRestricted's ON CONFLICT clause unconditionally forced
// restricted back to TRUE on every call, silently undoing the admin's
// change on the next upload.
func TestMarkNamesRestricted_DoesNotReRestrictAdminUnrestrictedName(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := t.Context()
	exhibitionID := testutil.CreateExhibition(t, pool)

	name := "Filename-" + uuid.NewString()
	// Simulate: name already has a row (e.g. created by an earlier upload),
	// and an admin has since explicitly unrestricted it.
	if _, err := pool.Exec(ctx, `INSERT INTO label_names (exhibitionid, name, restricted) VALUES ($1, $2, FALSE)`, exhibitionID, name); err != nil {
		t.Fatalf("seed label_names: %v", err)
	}

	// A later upload/import sees this name again and calls MarkNamesRestricted.
	if err := photoimport.MarkNamesRestricted(ctx, pool, exhibitionID, []string{name}); err != nil {
		t.Fatalf("MarkNamesRestricted: %v", err)
	}

	var restricted bool
	if err := pool.QueryRow(ctx, `SELECT restricted FROM label_names WHERE exhibitionid = $1 AND name = $2`, exhibitionID, name).Scan(&restricted); err != nil {
		t.Fatalf("query: %v", err)
	}
	if restricted {
		t.Error("restricted was flipped back to true, want the admin's unrestrict to stick")
	}
}

// TestMarkNamesRestricted_SameNameDifferentExhibitionsIndependent is the
// direct regression test for the bug this whole migration fixes: the same
// label name in two different exhibitions must have independent restricted
// flags, not share one global row.
func TestMarkNamesRestricted_SameNameDifferentExhibitionsIndependent(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := t.Context()
	exhibitionA := testutil.CreateExhibition(t, pool)
	exhibitionB := testutil.CreateExhibition(t, pool)
	name := "Location-" + uuid.NewString()

	// Exhibition A: admin has explicitly unrestricted this name.
	if _, err := pool.Exec(ctx, `INSERT INTO label_names (exhibitionid, name, restricted) VALUES ($1, $2, FALSE)`, exhibitionA, name); err != nil {
		t.Fatalf("seed label_names for exhibition A: %v", err)
	}

	// Exhibition B sees this name for the first time via MarkNamesRestricted
	// — it should default to restricted, independent of exhibition A's
	// setting for the same name.
	if err := photoimport.MarkNamesRestricted(ctx, pool, exhibitionB, []string{name}); err != nil {
		t.Fatalf("MarkNamesRestricted for exhibition B: %v", err)
	}

	var restrictedA, restrictedB bool
	if err := pool.QueryRow(ctx, `SELECT restricted FROM label_names WHERE exhibitionid = $1 AND name = $2`, exhibitionA, name).Scan(&restrictedA); err != nil {
		t.Fatalf("query exhibition A: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT restricted FROM label_names WHERE exhibitionid = $1 AND name = $2`, exhibitionB, name).Scan(&restrictedB); err != nil {
		t.Fatalf("query exhibition B: %v", err)
	}
	if restrictedA {
		t.Error("exhibition A's restricted flag changed, want it to remain false (unaffected by exhibition B)")
	}
	if !restrictedB {
		t.Error("exhibition B's restricted flag = false, want true (first time seen in this exhibition)")
	}
}
