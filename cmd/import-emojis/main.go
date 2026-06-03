// cmd/import-emojis/main.go
// Imports OpenMoji emoji data into the emoji_types table.
// Designed for incremental updates: new emojis are inserted, changed annotations
// are updated, and unchanged rows are skipped.
//
// Usage:
//
//	import-emojis [flags]
//
// Flags:
//
//	--db                 PostgreSQL DSN (default: $DATABASE_URL)
//	--url                OpenMoji JSON URL (default: GitHub master)
//	--deactivate-removed Mark emojis absent from the feed as inactive
//	--dry-run            Print what would change without writing to DB
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultOpenMojiURL = "https://raw.githubusercontent.com/hfg-gmuend/openmoji/master/data/openmoji.json"

// openMojiEntry mirrors the relevant fields in openmoji.json.
type openMojiEntry struct {
	Emoji      string  `json:"emoji"`
	Hexcode    string  `json:"hexcode"`
	Group      string  `json:"group"`
	Subgroups  string  `json:"subgroups"`
	Annotation string  `json:"annotation"`
	Tags       string  `json:"tags"`
	Skintone   string  `json:"skintone"`
	Order      float64 `json:"order"`
}

func main() {
	var (
		dbURL              string
		feedURL            string
		deactivateRemoved  bool
		dryRun             bool
	)

	flag.StringVar(&dbURL, "db", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (env: DATABASE_URL)")
	flag.StringVar(&feedURL, "url", defaultOpenMojiURL, "OpenMoji JSON feed URL")
	flag.BoolVar(&deactivateRemoved, "deactivate-removed", false, "Mark emojis absent from feed as inactive")
	flag.BoolVar(&dryRun, "dry-run", false, "Print changes without writing to DB")
	flag.Parse()

	if dbURL == "" && !dryRun {
		fmt.Fprintln(os.Stderr, "error: --db or DATABASE_URL is required")
		os.Exit(1)
	}

	// ── Fetch feed ────────────────────────────────────────────────────────────
	slog.Info("fetching OpenMoji data", "url", feedURL)
	entries, err := fetchFeed(feedURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching feed: %v\n", err)
		os.Exit(1)
	}
	slog.Info("fetched entries", "count", len(entries))

	// Filter: skip skintone variants and entries without an emoji character.
	filtered := make([]openMojiEntry, 0, len(entries))
	for _, e := range entries {
		if e.Emoji == "" || e.Skintone != "" {
			continue
		}
		filtered = append(filtered, e)
	}
	slog.Info("after filtering skintone variants", "count", len(filtered))

	if dryRun {
		fmt.Printf("[dry-run] would upsert %d emojis\n", len(filtered))
		for i, e := range filtered {
			if i >= 10 {
				fmt.Printf("  ... and %d more\n", len(filtered)-10)
				break
			}
			fmt.Printf("  %s  %s  (%s)\n", e.Emoji, e.Annotation, e.Group)
		}
		return
	}

	// ── Connect ───────────────────────────────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// ── Load existing emoji chars from DB for change detection ────────────────
	type dbEmoji struct {
		emojiid    string
		altText    string
		sortOrder  int
		group      string
		tags       string
		hexcode    string
		isActive   bool
	}
	existing := make(map[string]dbEmoji) // keyed by emoji_char

	rows, err := pool.Query(ctx, `
		SELECT emojiid::text, emoji_char, alt_text, sort_order,
		       COALESCE(emoji_group,''), COALESCE(tags,''), COALESCE(hexcode,''), is_active
		FROM   emoji_types
		WHERE  emoji_char IS NOT NULL
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error querying existing emojis: %v\n", err)
		os.Exit(1)
	}
	for rows.Next() {
		var emojiChar string
		var d dbEmoji
		if err := rows.Scan(&d.emojiid, &emojiChar, &d.altText, &d.sortOrder,
			&d.group, &d.tags, &d.hexcode, &d.isActive); err != nil {
			fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
			os.Exit(1)
		}
		existing[emojiChar] = d
	}
	rows.Close()
	slog.Info("existing emojis in DB", "count", len(existing))

	// ── Upsert ────────────────────────────────────────────────────────────────
	inserted, updated, skipped := 0, 0, 0
	seenChars := make(map[string]bool)

	for _, e := range filtered {
		seenChars[e.Emoji] = true
		sortOrder := int(e.Order)

		if d, exists := existing[e.Emoji]; exists {
			// Check if anything changed.
			if d.altText == e.Annotation && d.sortOrder == sortOrder &&
				d.group == e.Group && d.tags == e.Tags && d.hexcode == e.Hexcode && d.isActive {
				skipped++
				continue
			}
			_, err := pool.Exec(ctx, `
				UPDATE emoji_types
				SET    alt_text   = $1,
				       sort_order = $2,
				       emoji_group = $3,
				       tags       = $4,
				       hexcode    = $5,
				       is_active  = TRUE
				WHERE  emojiid = $6::uuid
			`, e.Annotation, sortOrder, e.Group, e.Tags, e.Hexcode, d.emojiid)
			if err != nil {
				slog.Warn("update failed", "emoji", e.Emoji, "error", err)
				continue
			}
			updated++
		} else {
			_, err := pool.Exec(ctx, `
				INSERT INTO emoji_types (emoji_char, alt_text, sort_order, emoji_group, tags, hexcode, is_active)
				VALUES ($1, $2, $3, $4, $5, $6, TRUE)
				ON CONFLICT (emoji_char) WHERE emoji_char IS NOT NULL
				DO UPDATE SET
					alt_text    = EXCLUDED.alt_text,
					sort_order  = EXCLUDED.sort_order,
					emoji_group = EXCLUDED.emoji_group,
					tags        = EXCLUDED.tags,
					hexcode     = EXCLUDED.hexcode,
					is_active   = TRUE
			`, e.Emoji, e.Annotation, sortOrder, e.Group, e.Tags, e.Hexcode)
			if err != nil {
				slog.Warn("insert failed", "emoji", e.Emoji, "error", err)
				continue
			}
			inserted++
		}
	}

	// ── Optionally deactivate removed emojis ──────────────────────────────────
	deactivated := 0
	if deactivateRemoved {
		for char, d := range existing {
			if !seenChars[char] && d.isActive {
				_, err := pool.Exec(ctx, `
					UPDATE emoji_types SET is_active = FALSE WHERE emojiid = $1::uuid
				`, d.emojiid)
				if err != nil {
					slog.Warn("deactivate failed", "emoji", char, "error", err)
					continue
				}
				deactivated++
			}
		}
	}

	fmt.Printf("done: %d inserted, %d updated, %d skipped, %d deactivated\n",
		inserted, updated, skipped, deactivated)
}

func fetchFeed(url string) ([]openMojiEntry, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var entries []openMojiEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}
	return entries, nil
}
