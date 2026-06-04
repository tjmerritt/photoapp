// cmd/import-emojis/main.go
// Imports OpenMoji emoji data into the emoji_types table, including skintone variants.
// Designed for incremental updates: new emojis are inserted, changed rows are updated,
// and unchanged rows are skipped.
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
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultOpenMojiURL = "https://raw.githubusercontent.com/hfg-gmuend/openmoji/master/data/openmoji.json"

// openMojiEntry mirrors the relevant fields in openmoji.json.
// Several fields use interface{} because OpenMoji represents them inconsistently
// (e.g. order and skintone can be strings, numbers, or empty strings).
type openMojiEntry struct {
	Emoji               string      `json:"emoji"`
	Hexcode             string      `json:"hexcode"`
	Group               string      `json:"group"`
	Annotation          string      `json:"annotation"`
	Tags                string      `json:"tags"`
	Skintone            interface{} `json:"skintone"`             // "" | tone name | number
	SkintoneBaseHexcode string      `json:"skintone_base_hexcode"` // non-empty = variant
	Order               interface{} `json:"order"`                 // number or ""
}

// skintoneLabel normalises the raw skintone value to a human-readable name.
// Returns "" for base emojis / unrecognised values.
func skintoneLabel(v interface{}) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
	switch s {
	case "", "0":
		return ""
	case "light", "1", "1F3FB":
		return "light"
	case "medium-light", "2", "1F3FC":
		return "medium-light"
	case "medium", "3", "1F3FD":
		return "medium"
	case "medium-dark", "4", "1F3FE":
		return "medium-dark"
	case "dark", "5", "1F3FF":
		return "dark"
	default:
		// Pass through any other string value (e.g. already a name we don't know)
		return s
	}
}

func toSortOrder(v interface{}) int {
	switch v := v.(type) {
	case float64:
		return int(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return int(f)
		}
	}
	return 0
}

func main() {
	var (
		dbURL             string
		feedURL           string
		deactivateRemoved bool
		dryRun            bool
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

	// Filter: skip entries without an emoji character.
	filtered := make([]openMojiEntry, 0, len(entries))
	baseCount, variantCount := 0, 0
	for _, e := range entries {
		if e.Emoji == "" {
			continue
		}
		filtered = append(filtered, e)
		if e.SkintoneBaseHexcode == "" {
			baseCount++
		} else {
			variantCount++
		}
	}
	slog.Info("entries to process", "base", baseCount, "skintone_variants", variantCount)

	if dryRun {
		fmt.Printf("[dry-run] would upsert %d emojis (%d base + %d skintone variants)\n",
			len(filtered), baseCount, variantCount)
		for i, e := range filtered {
			if i >= 10 {
				fmt.Printf("  ... and %d more\n", len(filtered)-10)
				break
			}
			tone := skintoneLabel(e.Skintone)
			if tone != "" {
				fmt.Printf("  %s  %s  [%s variant of %s]\n", e.Emoji, e.Annotation, tone, e.SkintoneBaseHexcode)
			} else {
				fmt.Printf("  %s  %s  (%s)\n", e.Emoji, e.Annotation, e.Group)
			}
		}
		return
	}

	// ── Connect ───────────────────────────────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// ── Load existing rows for change detection ───────────────────────────────
	type dbEmoji struct {
		emojiid      string
		altText      string
		sortOrder    int
		group        string
		tags         string
		hexcode      string
		imageURL     string
		skintone     string
		baseHexcode  string
		isActive     bool
	}
	existing := make(map[string]dbEmoji) // keyed by emoji_char

	rows, err := pool.Query(ctx, `
		SELECT emojiid::text, emoji_char, alt_text, sort_order,
		       COALESCE(emoji_group,''), COALESCE(tags,''), COALESCE(hexcode,''),
		       COALESCE(image_url,''), COALESCE(skintone,''), COALESCE(base_hexcode,''),
		       is_active
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
			&d.group, &d.tags, &d.hexcode, &d.imageURL, &d.skintone, &d.baseHexcode,
			&d.isActive); err != nil {
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
		sortOrder  := toSortOrder(e.Order)
		tone       := skintoneLabel(e.Skintone)
		imageURL   := fmt.Sprintf("https://openmoji.org/data/color/svg/%s.svg", e.Hexcode)
		baseHex    := e.SkintoneBaseHexcode

		if d, exists := existing[e.Emoji]; exists {
			if d.altText == e.Annotation && d.sortOrder == sortOrder &&
				d.group == e.Group && d.tags == e.Tags && d.hexcode == e.Hexcode &&
				d.imageURL == imageURL && d.skintone == tone && d.baseHexcode == baseHex &&
				d.isActive {
				skipped++
				continue
			}
			_, err := pool.Exec(ctx, `
				UPDATE emoji_types
				SET    alt_text      = $1,
				       sort_order    = $2,
				       emoji_group   = $3,
				       tags          = $4,
				       hexcode       = $5,
				       image_url     = $6,
				       skintone      = NULLIF($7, ''),
				       base_hexcode  = NULLIF($8, ''),
				       is_active     = TRUE
				WHERE  emojiid = $9::uuid
			`, e.Annotation, sortOrder, e.Group, e.Tags, e.Hexcode,
				imageURL, tone, baseHex, d.emojiid)
			if err != nil {
				slog.Warn("update failed", "emoji", e.Emoji, "error", err)
				continue
			}
			updated++
		} else {
			_, err := pool.Exec(ctx, `
				INSERT INTO emoji_types
				       (emoji_char, image_url, alt_text, sort_order, emoji_group, tags,
				        hexcode, skintone, base_hexcode, is_active)
				VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,''), NULLIF($9,''), TRUE)
				ON CONFLICT (emoji_char) WHERE emoji_char IS NOT NULL
				DO UPDATE SET
					image_url    = EXCLUDED.image_url,
					alt_text     = EXCLUDED.alt_text,
					sort_order   = EXCLUDED.sort_order,
					emoji_group  = EXCLUDED.emoji_group,
					tags         = EXCLUDED.tags,
					hexcode      = EXCLUDED.hexcode,
					skintone     = EXCLUDED.skintone,
					base_hexcode = EXCLUDED.base_hexcode,
					is_active    = TRUE
			`, e.Emoji, imageURL, e.Annotation, sortOrder, e.Group, e.Tags,
				e.Hexcode, tone, baseHex)
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
