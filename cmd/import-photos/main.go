// cmd/import-photos/main.go
// Reads photo URLs from a file (or stdin) and inserts them into the database.
//
// Usage:
//
//	import-photos [flags] [file]
//	cat urls.txt | import-photos [flags]
//
// Flags:
//
//	--db          PostgreSQL DSN (default: $DATABASE_URL)
//	--owner       UUID of the owning user (required)
//	--title       Fixed title text for every photo (optional)
//	--title-from-url  Derive title from the URL path basename (default true)
//	--dry-run     Print what would be inserted without writing to the DB
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	var (
		dbURL        string
		ownerID      string
		fixedTitle   string
		titleFromURL bool
		dryRun       bool
	)

	flag.StringVar(&dbURL, "db", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (env: DATABASE_URL)")
	flag.StringVar(&ownerID, "owner", "", "UUID of the photo owner (required)")
	flag.StringVar(&fixedTitle, "title", "", "Fixed title for every photo (overrides --title-from-url)")
	flag.BoolVar(&titleFromURL, "title-from-url", true, "Derive title from URL basename")
	flag.BoolVar(&dryRun, "dry-run", false, "Print rows without inserting")
	flag.Parse()

	if ownerID == "" {
		fmt.Fprintln(os.Stderr, "error: --owner is required")
		flag.Usage()
		os.Exit(1)
	}
	if dbURL == "" && !dryRun {
		fmt.Fprintln(os.Stderr, "error: --db or DATABASE_URL is required")
		flag.Usage()
		os.Exit(1)
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
	}

	// ── Process URLs ──────────────────────────────────────────────────────────
	client := &http.Client{Timeout: 30 * time.Second}
	scanner := bufio.NewScanner(input)
	inserted, skipped := 0, 0

	for scanner.Scan() {
		rawURL := strings.TrimSpace(scanner.Text())
		if rawURL == "" || strings.HasPrefix(rawURL, "#") {
			continue
		}

		title := fixedTitle
		if title == "" && titleFromURL {
			title = titleFromURLPath(rawURL)
		}

		w, h, err := fetchDimensions(client, rawURL)
		if err != nil {
			slog.Warn("skipping URL: could not fetch dimensions", "url", rawURL, "error", err)
			skipped++
			continue
		}

		if dryRun {
			fmt.Printf("[dry-run] INSERT photo url=%s title=%q w=%d h=%d owner=%s\n", rawURL, title, w, h, ownerID)
			inserted++
			continue
		}

		ctx := context.Background()
		var photoID string
		err = pool.QueryRow(ctx, `
			INSERT INTO photos (owner_userid, image_url, image_width, image_height, title_text, title_userid)
			VALUES ($1, $2, $3, $4, $5, $1)
			RETURNING photoid::text
		`, ownerID, rawURL, w, h, title).Scan(&photoID)
		if err != nil {
			slog.Warn("skipping URL: insert failed", "url", rawURL, "error", err)
			skipped++
			continue
		}

		slog.Info("inserted photo", "photoid", photoID, "url", rawURL, "title", title, "width", w, "height", h)
		inserted++
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading input: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("done: %d inserted, %d skipped\n", inserted, skipped)
}

// fetchDimensions downloads just enough of the image to decode its header.
func fetchDimensions(client *http.Client, imageURL string) (int, int, error) {
	resp, err := client.Get(imageURL)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	cfg, _, err := image.DecodeConfig(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("decode image config: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// titleFromURLPath turns the last path segment into a human-readable title.
// e.g. "photo-1506905925346-21bda4d32df4" → "Photo 1506905925346 21bda4d32df4"
func titleFromURLPath(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	// Strip query-style suffixes that sneak into the path (e.g. "file?w=1200")
	base = strings.SplitN(base, "?", 2)[0]
	// Strip extension
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	// Replace dashes/underscores with spaces and title-case
	words := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
