// cmd/import-photos/main.go
// Reads photo URLs from a file (or stdin) and inserts them into the database.
// EXIF metadata is extracted from each image and stored as labels.
//
// Usage:
//
//	import-photos [flags] [file]
//	cat urls.txt | import-photos [flags]
//
// Input format (one entry per line, lines starting with # are ignored):
//
//	<url>
//	<url>,<photoid>   — if photoid exists in DB with the same URL, update labels only
//
// Flags:
//
//	--db              PostgreSQL DSN (default: $DATABASE_URL)
//	--owner           UUID of the owning user (required)
//	--exhibition      Exhibition UUID or name to associate photos with (required)
//	--title           Fixed title text for every photo (optional)
//	--title-from-url  Derive title from the URL path basename (default true)
//	--label           Extra label in Name=Value format; may be repeated
//	--output          Write url,photoid results to this file
//	--refresh-exif    Re-download images and update labels even when photoid already exists
//	--dry-run         Print what would be inserted without writing to the DB
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rwcarlsen/goexif/exif"
	"gocv.io/x/gocv"
)

// label is a name/value pair.
type label struct{ name, value string }

// labelFlag is a flag.Value that accumulates --label Name=Value entries.
type labelFlag []label

func (l *labelFlag) String() string { return fmt.Sprintf("%v", *l) }
func (l *labelFlag) Set(s string) error {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("label must be in Name=Value format, got %q", s)
	}
	*l = append(*l, label{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])})
	return nil
}

func main() {
	var (
		dbURL          string
		ownerID        string
		exhibition     string
		fixedTitle     string
		titleFromURL   bool
		dryRun         bool
		refreshEXIF    bool
		outputFile     string
		extraLabels    labelFlag
		cascadeXMLPath string
	)

	flag.StringVar(&dbURL, "db", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (env: DATABASE_URL)")
	flag.StringVar(&ownerID, "owner", "", "UUID of the photo owner (required)")
	flag.StringVar(&exhibition, "exhibition", "", "Exhibition UUID or name to assign photos to (required)")
	flag.StringVar(&fixedTitle, "title", "", "Fixed title for every photo (overrides --title-from-url)")
	flag.BoolVar(&titleFromURL, "title-from-url", true, "Derive title from URL basename")
	flag.BoolVar(&dryRun, "dry-run", false, "Print rows without inserting")
	flag.BoolVar(&refreshEXIF, "refresh-exif", false, "Re-download images and replace labels even when photoid already exists")
	flag.StringVar(&outputFile, "output", "", "Write url,photoid results to this CSV file")
	flag.Var(&extraLabels, "label", "Extra label in Name=Value format (repeatable)")
	flag.StringVar(&cascadeXMLPath, "cascade", os.Getenv("HAAR_CASCADE_XML"),
		"Path to haarcascade_frontalface_default.xml (env: HAAR_CASCADE_XML); disables face detection when empty")
	flag.Parse()

	if ownerID == "" {
		fmt.Fprintln(os.Stderr, "error: --owner is required")
		flag.Usage()
		os.Exit(1)
	}
	if exhibition == "" {
		fmt.Fprintln(os.Stderr, "error: --exhibition is required")
		flag.Usage()
		os.Exit(1)
	}
	if dbURL == "" && !dryRun {
		fmt.Fprintln(os.Stderr, "error: --db or DATABASE_URL is required")
		flag.Usage()
		os.Exit(1)
	}

	// ── Face-detection classifier (loaded once for all photos) ────────────────
	var classifier *gocv.CascadeClassifier
	if cascadeXMLPath != "" {
		c := gocv.NewCascadeClassifier()
		if !c.Load(cascadeXMLPath) {
			fmt.Fprintf(os.Stderr, "error: could not load cascade classifier from %q\n", cascadeXMLPath)
			os.Exit(1)
		}
		defer c.Close()
		classifier = &c
		slog.Info("face classifier loaded", "path", cascadeXMLPath)
	} else {
		slog.Warn("--cascade not set; all photos will be imported as non-public (is_public=false)")
	}

	// ── Open input ────────────────────────────────────────────────────────────
	var input *os.File
	if flag.NArg() > 0 {
		f, err := os.Open(flag.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error opening file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		input = f
	} else {
		input = os.Stdin
	}

	// ── Open output CSV ───────────────────────────────────────────────────────
	var csvWriter *csv.Writer
	if outputFile != "" && !dryRun {
		f, err := os.Create(outputFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		csvWriter = csv.NewWriter(f)
		defer csvWriter.Flush()
		_ = csvWriter.Write([]string{"url", "photoid", "action"})
	}

	// ── Connect to DB ─────────────────────────────────────────────────────────
	var pool *pgxpool.Pool
	if !dryRun {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		pool, err = pgxpool.New(ctx, dbURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error connecting to database: %v\n", err)
			os.Exit(1)
		}
		defer pool.Close()

		// Resolve --exhibition: try as UUID first, then as name.
		var exhibitionID string
		err = pool.QueryRow(ctx,
			`SELECT exhibitionid::text FROM exhibitions WHERE exhibitionid::text = $1 OR name = $1 AND deleted_at IS NULL`,
			exhibition,
		).Scan(&exhibitionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: exhibition %q not found in database\n", exhibition)
			os.Exit(1)
		}
		exhibition = exhibitionID
		slog.Info("exhibition resolved", "id", exhibition)
	}

	// ── Process URLs ──────────────────────────────────────────────────────────
	client := &http.Client{Timeout: 30 * time.Second}
	reader := csv.NewReader(input)
	reader.Comment = '#'
	reader.FieldsPerRecord = -1 // allow variable number of fields
	reader.TrimLeadingSpace = true
	inserted, updated, unchanged, skipped := 0, 0, 0, 0

	for {
		fields, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Warn("skipping malformed line", "error", err)
			skipped++
			continue
		}
		if len(fields) == 0 {
			continue
		}

		rawURL := strings.TrimSpace(fields[0])
		if rawURL == "" || rawURL == "url" {
			continue // skip blank lines and the header row
		}

		hintID := ""
		if len(fields) >= 2 {
			hintID = strings.TrimSpace(fields[1])
		}

		ctx := context.Background()

		// When a photoid hint is present and --refresh-exif is off, check the DB
		// first. If the URL matches we can skip downloading entirely.
		if hintID != "" && !refreshEXIF && !dryRun {
			var existingURL string
			err := pool.QueryRow(ctx,
				`SELECT image_url FROM photos WHERE photoid = $1 AND deleted_at IS NULL`,
				hintID,
			).Scan(&existingURL)
			if err == nil && existingURL == rawURL {
				action := "unchanged"
				if len(extraLabels) > 0 {
					if err := patchLabels(ctx, pool, hintID, ownerID, []label(extraLabels)); err != nil {
						slog.Warn("failed to patch labels", "photoid", hintID, "error", err)
					} else {
						action = "updated"
						updated++
					}
				} else {
					unchanged++
				}
				slog.Info(action, "photoid", hintID, "url", rawURL)
				if csvWriter != nil {
					_ = csvWriter.Write([]string{rawURL, hintID, action})
				}
				continue
			}
			// URL mismatch or not found — fall through to download + insert.
			if err == nil {
				slog.Warn("photoid URL mismatch — inserting new photo",
					"photoid", hintID, "db_url", existingURL, "input_url", rawURL)
			} else {
				slog.Warn("photoid not found — inserting new photo", "photoid", hintID)
			}
			hintID = "" // don't try to update a mismatched/missing row
		}

		imgBytes, err := fetchImage(client, rawURL)
		if err != nil {
			slog.Warn("skipping URL: download failed", "url", rawURL, "error", err)
			skipped++
			continue
		}

		w, h, err := imageDimensions(imgBytes)
		if err != nil {
			slog.Warn("skipping URL: could not decode image dimensions", "url", rawURL, "error", err)
			skipped++
			continue
		}

		exifLabels := extractEXIF(imgBytes)
		allLabels := mergeLabels(exifLabels, []label(extraLabels))

		isPublic := detectIsPublic(imgBytes, classifier)

		if dryRun {
			action := "insert"
			if hintID != "" {
				action = fmt.Sprintf("update labels for %s", hintID)
			}
			fmt.Printf("[dry-run] %s url=%s title=%q w=%d h=%d is_public=%v\n",
				action, rawURL, titleFor(rawURL, fixedTitle, titleFromURL), w, h, isPublic)
			for _, l := range allLabels {
				fmt.Printf("           label %q = %q\n", l.name, l.value)
			}
			inserted++
			continue
		}

		photoID, action, err := upsertPhoto(ctx, pool, upsertParams{
			hintID:       hintID,
			rawURL:       rawURL,
			ownerID:      ownerID,
			exhibitionID: exhibition,
			title:        titleFor(rawURL, fixedTitle, titleFromURL),
			width:        w,
			height:       h,
			labels:       allLabels,
			isPublic:     isPublic,
		})
		if err != nil {
			slog.Warn("skipping URL", "url", rawURL, "error", err)
			skipped++
			continue
		}

		slog.Info(action, "photoid", photoID, "url", rawURL, "labels", len(allLabels))
		if action == "inserted" {
			inserted++
		} else {
			updated++
		}

		if csvWriter != nil {
			_ = csvWriter.Write([]string{rawURL, photoID, action})
		}
	}


	fmt.Printf("done: %d inserted, %d updated, %d unchanged, %d skipped\n", inserted, updated, unchanged, skipped)
}

type upsertParams struct {
	hintID       string
	rawURL       string
	ownerID      string
	exhibitionID string
	title        string
	width        int
	height       int
	labels       []label
	isPublic     bool
}

// upsertPhoto either inserts a new photo or, when hintID is supplied and the
// DB row's image_url matches rawURL, replaces all labels for that photo.
// Returns the photoid and a string describing what happened.
func upsertPhoto(ctx context.Context, pool *pgxpool.Pool, p upsertParams) (string, string, error) {
	if p.hintID != "" {
		// Check whether the existing photo has the same URL.
		var existingURL string
		err := pool.QueryRow(ctx,
			`SELECT image_url FROM photos WHERE photoid = $1 AND deleted_at IS NULL`,
			p.hintID,
		).Scan(&existingURL)
		if err == nil && existingURL == p.rawURL {
			// URL matches — update labels only.
			if err := replaceLabels(ctx, pool, p.hintID, p.ownerID, p.labels); err != nil {
				return "", "", fmt.Errorf("replacing labels: %w", err)
			}
			return p.hintID, "updated", nil
		}
		if err == nil {
			slog.Warn("photoid found but URL mismatch — inserting new photo",
				"photoid", p.hintID, "db_url", existingURL, "input_url", p.rawURL)
		} else {
			slog.Warn("photoid not found in DB — inserting new photo",
				"photoid", p.hintID, "error", err)
		}
	}

	// Insert new photo.
	var photoID string
	err := pool.QueryRow(ctx, `
		INSERT INTO photos (owner_userid, image_url, image_width, image_height, title_text, title_userid, exhibitionid, is_public)
		VALUES ($1, $2, $3, $4, $5, $1, NULLIF($6,'')::uuid, $7)
		RETURNING photoid::text
	`, p.ownerID, p.rawURL, p.width, p.height, p.title, p.exhibitionID, p.isPublic).Scan(&photoID)
	if err != nil {
		return "", "", fmt.Errorf("inserting photo: %w", err)
	}

	if err := replaceLabels(ctx, pool, photoID, p.ownerID, p.labels); err != nil {
		return photoID, "inserted", fmt.Errorf("inserting labels: %w", err)
	}
	return photoID, "inserted", nil
}

// patchLabels upserts only the supplied labels by name, leaving all other
// existing labels for the photo untouched.
func patchLabels(ctx context.Context, pool *pgxpool.Pool, photoID, ownerID string, labels []label) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, l := range labels {
		// Delete any existing label with this name, then re-insert.
		if _, err := tx.Exec(ctx,
			`DELETE FROM labels WHERE photoid = $1 AND name = $2`,
			photoID, l.name,
		); err != nil {
			return fmt.Errorf("deleting label %q: %w", l.name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO labels (photoid, added_by_userid, name, value)
			VALUES ($1, $2, $3, $4)
		`, photoID, ownerID, l.name, l.value); err != nil {
			return fmt.Errorf("inserting label %q: %w", l.name, err)
		}
	}

	return tx.Commit(ctx)
}

// replaceLabels deletes existing labels for a photo and inserts the new set.
func replaceLabels(ctx context.Context, pool *pgxpool.Pool, photoID, ownerID string, labels []label) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`DELETE FROM labels WHERE photoid = $1`, photoID,
	); err != nil {
		return err
	}

	for _, l := range labels {
		if _, err := tx.Exec(ctx, `
			INSERT INTO labels (photoid, added_by_userid, name, value)
			VALUES ($1, $2, $3, $4)
		`, photoID, ownerID, l.name, l.value); err != nil {
			return fmt.Errorf("inserting label %q: %w", l.name, err)
		}
	}

	return tx.Commit(ctx)
}

// titleFor resolves a photo title from the available inputs.
func titleFor(rawURL, fixed string, fromURL bool) string {
	if fixed != "" {
		return fixed
	}
	if fromURL {
		return titleFromURLPath(rawURL)
	}
	return ""
}

// fetchImage downloads the full image body into memory.
func fetchImage(client *http.Client, imageURL string) ([]byte, error) {
	resp, err := client.Get(imageURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// imageDimensions decodes width/height from raw image bytes.
func imageDimensions(data []byte) (int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("decode image config: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// exifFields lists the EXIF tags to surface as labels.
var exifFields = []struct {
	tag   exif.FieldName
	label string
}{
	{exif.Make, "Camera Make"},
	{exif.Model, "Camera Model"},
	{exif.LensMake, "Lens Make"},
	{exif.LensModel, "Lens Model"},
	{exif.ExposureTime, "Shutter Speed"},
	{exif.FNumber, "Aperture"},
	{exif.ISOSpeedRatings, "ISO"},
	{exif.FocalLength, "Focal Length"},
	{exif.FocalLengthIn35mmFilm, "Focal Length (35mm)"},
	{exif.Flash, "Flash"},
	{exif.WhiteBalance, "White Balance"},
	{exif.ExposureMode, "Exposure Mode"},
	{exif.ExposureProgram, "Exposure Program"},
	{exif.Artist, "Artist"},
	{exif.Copyright, "Copyright"},
	{exif.Software, "Software"},
	{exif.ImageDescription, "Description"},
}

// extractEXIF returns labels parsed from EXIF metadata. Missing tags are skipped.
func extractEXIF(data []byte) []label {
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil && exif.IsCriticalError(err) {
		return nil
	}
	if x == nil {
		return nil
	}

	var labels []label

	for _, f := range exifFields {
		tag, err := x.Get(f.tag)
		if err != nil {
			continue
		}
		val := strings.Trim(strings.TrimSpace(tag.String()), `"`)
		if val == "" {
			continue
		}
		labels = append(labels, label{f.label, val})
	}

	if t, err := x.DateTime(); err == nil {
		labels = append(labels, label{"Date Taken", t.Format("2006-01-02 15:04:05")})
	}

	if lat, long, err := x.LatLong(); err == nil {
		labels = append(labels, label{"GPS", fmt.Sprintf("%.6f, %.6f", lat, long)})
	}

	return labels
}

// mergeLabels combines base and extra; extra overrides on duplicate names.
func mergeLabels(base, extra []label) []label {
	if len(extra) == 0 {
		return base
	}
	override := make(map[string]string, len(extra))
	for _, l := range extra {
		override[strings.ToLower(l.name)] = l.value
	}
	out := make([]label, 0, len(base)+len(extra))
	for _, l := range base {
		if v, ok := override[strings.ToLower(l.name)]; ok {
			out = append(out, label{l.name, v})
			delete(override, strings.ToLower(l.name))
		} else {
			out = append(out, l)
		}
	}
	for _, l := range extra {
		if _, still := override[strings.ToLower(l.name)]; still {
			out = append(out, l)
		}
	}
	return out
}

// detectIsPublic returns true when the image contains no detectable faces.
// classifier must already be loaded; pass nil to skip detection entirely
// (all photos treated as non-public, the safe default).
func detectIsPublic(imgBytes []byte, classifier *gocv.CascadeClassifier) bool {
	if classifier == nil {
		return false
	}

	mat, err := gocv.IMDecode(imgBytes, gocv.IMReadColor)
	if err != nil || mat.Empty() {
		slog.Warn("detectIsPublic: could not decode image for face detection")
		return false
	}
	defer mat.Close()

	faces := classifier.DetectMultiScale(mat)
	isPublic := len(faces) == 0
	slog.Debug("detectIsPublic", "faces", len(faces), "is_public", isPublic)
	return isPublic
}

// titleFromURLPath turns the last path segment into a human-readable title.
func titleFromURLPath(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	base = strings.SplitN(base, "?", 2)[0]
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	words := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
