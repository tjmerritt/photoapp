package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"

	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
	"github.com/tjmerritt/photoapp/internal/permissions"
	"github.com/tjmerritt/photoapp/internal/photoimport"
)

// UploadPhotosHandler handles POST /api/v1/photos/upload.
type UploadPhotosHandler struct {
	DB      *db.Pool
	Cfg     *config.Config
	Checker *permissions.Checker
}

// maxUploadMemory bounds how much of the multipart request body is held in
// memory before the multipart reader spills additional file parts to the
// OS's temp directory; it is not a hard cap on total upload size.
const maxUploadMemory = 32 << 20 // 32 MB

// allowedPhotoUploadTypes maps a sniffed content type to its file extension.
// Deliberately narrower than the emoji/avatar upload endpoints: photos need
// real pixel dimensions (photos.image_width/image_height are NOT NULL), and
// the Go standard library has no built-in WebP decoder, so WebP is not
// accepted here even though it is elsewhere in the app. This matches exactly
// the set cmd/import-photos has always supported (see internal/photoimport).
var allowedPhotoUploadTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
}

// POST /api/v1/photos/upload  (auth required; PermPhotoCreate enforced below)
//
// Accepts a multipart form with one or more files under the "files" field,
// plus an optional "labels" field containing a JSON array of
// {"name": "...", "value": "..."} objects to apply to every photo in the
// batch (in addition to each photo's own EXIF-derived labels).
//
// Each file is processed independently — one file failing (bad format,
// corrupt data, DB error) does not abort the rest of the batch. The response
// lists a per-file result so the frontend upload queue can show individual
// success/failure.
func (h *UploadPhotosHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	userID := middleware.MustUserID(ctx)
	exhibitionID := middleware.ExhibitionID(ctx)

	if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPhotoCreate); err != nil || !ok {
		middleware.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := r.ParseMultipartForm(maxUploadMemory); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "could not parse multipart form")
		return
	}

	var batchLabels []photoimport.Label
	if raw := strings.TrimSpace(r.FormValue("labels")); raw != "" {
		var reqLabels []models.AddLabelRequest
		if err := json.Unmarshal([]byte(raw), &reqLabels); err != nil {
			middleware.WriteError(w, http.StatusBadRequest, `"labels" must be a JSON array of {"name","value"} objects`)
			return
		}
		for _, l := range reqLabels {
			if l.Name == "" || l.Value == "" {
				continue
			}
			batchLabels = append(batchLabels, photoimport.Label{Name: l.Name, Value: l.Value})
		}
	}

	var files []*multipart.FileHeader
	if r.MultipartForm != nil {
		files = r.MultipartForm.File["files"]
	}
	if len(files) == 0 {
		middleware.WriteError(w, http.StatusBadRequest, `no files provided (use multipart field "files")`)
		return
	}

	results := make([]models.UploadResult, 0, len(files))
	for _, fh := range files {
		result := models.UploadResult{Filename: fh.Filename}
		photoID, err := h.uploadOne(ctx, userID, exhibitionID, fh, batchLabels)
		if err != nil {
			slog.Warn("photo upload failed", "filename", fh.Filename, "error", err)
			result.Status = "error"
			result.Error = err.Error()
		} else {
			result.Status = "ok"
			result.PhotoID = photoID
		}
		results = append(results, result)
	}

	middleware.WriteJSON(w, http.StatusOK, models.UploadPhotosResponse{Results: results})
}

// uploadOne saves one uploaded file to disk, extracts its EXIF metadata and
// dimensions, and inserts the photo + all labels (EXIF-derived, computed,
// and batch) in a single transaction so a photo never ends up committed
// without its labels (or vice versa).
//
// The new photo is always created with is_public=false. Unlike
// cmd/import-photos, this endpoint does not run face detection (that logic
// depends on gocv/OpenCV, a native dependency deliberately not linked into
// the main server binary — see internal/photoimport's package doc) — false
// is the same safe default the CLI itself falls back to without --cascade.
func (h *UploadPhotosHandler) uploadOne(
	ctx context.Context, userID, exhibitionID string,
	fh *multipart.FileHeader, batchLabels []photoimport.Label,
) (string, error) {
	f, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("opening upload: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("reading upload: %w", err)
	}

	contentType := http.DetectContentType(data)
	ext, ok := allowedPhotoUploadTypes[contentType]
	if !ok {
		return "", fmt.Errorf("unsupported image type %q (only JPEG, PNG, and GIF are accepted)", contentType)
	}

	width, height, err := photoimport.ImageDimensions(data)
	if err != nil {
		return "", fmt.Errorf("could not read image dimensions: %w", err)
	}

	if err := os.MkdirAll(h.Cfg.UploadDir, 0o755); err != nil {
		return "", fmt.Errorf("creating upload directory: %w", err)
	}
	filename := uuid.New().String() + ext
	destPath := filepath.Join(h.Cfg.UploadDir, filename)
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return "", fmt.Errorf("saving file: %w", err)
	}
	imageURL := h.Cfg.UploadURLBase + "/" + filename

	// Clean up the saved file if anything below fails, so a failed upload
	// doesn't leave an orphaned file with no corresponding photo row.
	cleanup := func() { _ = os.Remove(destPath) }

	exifLabels := photoimport.ExtractEXIF(data)
	computed := []photoimport.Label{
		{Name: "Resolution", Value: fmt.Sprintf("%dx%d", width, height)},
		{Name: "Filename", Value: fh.Filename},
	}
	allLabels := photoimport.MergeLabels(exifLabels, computed)
	allLabels = photoimport.MergeLabels(allLabels, batchLabels)

	// The photo detail page shows only the first page of labels (ordered by
	// created_at — see fetchLabels in internal/handlers/fetch.go), and
	// labels are inserted below in allLabels' slice order. A photo with a
	// rich EXIF profile can easily produce 10+ EXIF labels (camera/lens
	// info, exposure settings, GPS, etc.), which would otherwise push
	// Resolution/Filename (appended after EXIF labels by MergeLabels) past
	// that first page. Reorder so they're always inserted — and therefore
	// always shown — first, regardless of how many EXIF tags a given photo
	// has.
	allLabels = prioritizeLabels(allLabels, "Filename", "Resolution")

	title := titleFromFilename(fh.Filename)

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var photoID string
	err = tx.QueryRow(ctx, `
		INSERT INTO photos (owner_userid, image_url, image_width, image_height, title_text, title_userid, exhibitionid, is_public)
		VALUES ($1, $2, $3, $4, $5, $1, NULLIF($6,'')::uuid, FALSE)
		RETURNING photoid::text
	`, userID, imageURL, width, height, title, exhibitionID).Scan(&photoID)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("inserting photo: %w", err)
	}

	for _, l := range allLabels {
		if _, err := tx.Exec(ctx, `
			INSERT INTO labels (photoid, added_by_userid, name, value)
			VALUES ($1, $2, $3, $4)
		`, photoID, userID, l.Name, l.Value); err != nil {
			cleanup()
			return "", fmt.Errorf("inserting label %q: %w", l.Name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		cleanup()
		return "", fmt.Errorf("committing: %w", err)
	}

	return photoID, nil
}

// prioritizeLabels reorders labels so that any label matching one of
// priorityNames (in that order) comes first; every other label keeps its
// existing relative order after them. A no-op for names that aren't present.
func prioritizeLabels(labels []photoimport.Label, priorityNames ...string) []photoimport.Label {
	byName := make(map[string]photoimport.Label, len(labels))
	for _, l := range labels {
		byName[strings.ToLower(l.Name)] = l
	}

	used := make(map[string]bool, len(priorityNames))
	out := make([]photoimport.Label, 0, len(labels))
	for _, name := range priorityNames {
		key := strings.ToLower(name)
		if l, ok := byName[key]; ok {
			out = append(out, l)
			used[key] = true
		}
	}
	for _, l := range labels {
		if used[strings.ToLower(l.Name)] {
			continue
		}
		out = append(out, l)
	}
	return out
}

// titleFromFilename turns an uploaded file's original name into a
// human-readable title: strips the extension and capitalizes
// dash/underscore/space-separated words. Mirrors cmd/import-photos'
// titleFromURLPath, but operating on a plain filename rather than a URL.
func titleFromFilename(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	words := strings.FieldsFunc(base, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
