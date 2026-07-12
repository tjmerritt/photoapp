// Package photoimport holds the image-metadata logic shared between the
// command-line bulk importer (cmd/import-photos) and the browser upload
// endpoint (internal/handlers/upload.go), so the two entry points can never
// drift apart on what EXIF fields get surfaced as labels or how image
// dimensions are measured.
//
// Deliberately NOT included here: face-detection-based public/private
// classification. That logic lives only in cmd/import-photos because it
// depends on gocv (OpenCV cgo bindings), a heavy native dependency the main
// server binary does not otherwise need. Photos uploaded through the browser
// endpoint are created with is_public=false — the same safe default
// cmd/import-photos itself falls back to when run without --cascade.
package photoimport

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/rwcarlsen/goexif/exif"
)

// Label is a name/value metadata pair, either extracted from EXIF or supplied
// by the caller (e.g. batch labels typed in by the uploader).
type Label struct {
	Name  string
	Value string
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

// ExtractEXIF returns labels parsed from EXIF metadata. Missing tags are
// skipped; a file with no EXIF data (or a critical parse error) returns nil,
// not an error — callers treat "no metadata" as a normal, common case.
func ExtractEXIF(data []byte) []Label {
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil && exif.IsCriticalError(err) {
		return nil
	}
	if x == nil {
		return nil
	}

	var labels []Label

	for _, f := range exifFields {
		tag, err := x.Get(f.tag)
		if err != nil {
			continue
		}
		val := strings.Trim(strings.TrimSpace(tag.String()), `"`)
		if val == "" {
			continue
		}
		labels = append(labels, Label{f.label, val})
	}

	if t, err := x.DateTime(); err == nil {
		labels = append(labels, Label{"Date Taken", t.Format("2006-01-02 15:04:05")})
	}

	if lat, long, err := x.LatLong(); err == nil {
		labels = append(labels, Label{"GPS", fmt.Sprintf("%.6f, %.6f", lat, long)})
	}

	return labels
}

// ImageDimensions decodes width/height from raw image bytes. Supports GIF,
// JPEG, and PNG (the formats registered via the blank imports above) — the
// same set cmd/import-photos has always supported. Notably not WebP: the Go
// standard library has no built-in WebP decoder, and photos.image_width/
// image_height are NOT NULL, so callers accepting uploads must reject WebP
// (or any other undecodable format) before calling this.
func ImageDimensions(data []byte) (int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("decode image config: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// MergeLabels combines base and extra; extra overrides on duplicate names
// (case-insensitive). Order of base is preserved; extra-only labels are
// appended at the end.
func MergeLabels(base, extra []Label) []Label {
	if len(extra) == 0 {
		return base
	}
	override := make(map[string]string, len(extra))
	for _, l := range extra {
		override[strings.ToLower(l.Name)] = l.Value
	}
	out := make([]Label, 0, len(base)+len(extra))
	for _, l := range base {
		if v, ok := override[strings.ToLower(l.Name)]; ok {
			out = append(out, Label{l.Name, v})
			delete(override, strings.ToLower(l.Name))
		} else {
			out = append(out, l)
		}
	}
	for _, l := range extra {
		if _, still := override[strings.ToLower(l.Name)]; still {
			out = append(out, l)
		}
	}
	return out
}
