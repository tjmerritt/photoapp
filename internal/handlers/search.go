package handlers

import (
	"net/http"
	"strings"

	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
)

// SearchHandler handles GET /api/v1/search?q=<query>
//
// Returns up to 13 photos ranked by relevance across:
//   - title_text      (weight 4)
//   - description     (weight 3)
//   - label name/value (weight 2 each)
//   - emoji alt_text  (weight 2)
//   - comment_text    (weight 1)
//
// The response contains the top result first; remaining results (up to 12)
// are intended for the "related photos" sidebar on the frontend.
type SearchHandler struct {
	DB *db.Pool
}

func (h *SearchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		middleware.WriteJSON(w, http.StatusOK, models.SearchResponse{
			Query:   "",
			Total:   0,
			Results: []models.SearchResult{},
		})
		return
	}

	// Tokenise: split on whitespace, deduplicate, drop empties.
	seen := map[string]bool{}
	var terms []string
	for _, t := range strings.Fields(query) {
		t = strings.ToLower(t)
		if t != "" && !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		middleware.WriteJSON(w, http.StatusOK, models.SearchResponse{
			Query:   query,
			Total:   0,
			Results: []models.SearchResult{},
		})
		return
	}

	ctx := r.Context()
	exhibitionID := middleware.ExhibitionID(r.Context())
	canSeeNonPublic := middleware.AuthorizedNonPublic(r.Context())

	// ── Scoring query ─────────────────────────────────────────────────────────
	//
	// For each search term we measure how many times it appears in each field
	// of each photo and accumulate a weighted score.  Photos that are not
	// visible in the current exhibition (or are private) are excluded from the
	// final result even if they scored points from labels/comments.
	//
	// Weights:  title=4, description=3, label name/value=2, emoji=2, comment=1
	const sqlSearch = `
WITH terms AS (
    SELECT unnest($1::text[]) AS term
),
title_hits AS (
    SELECT p.photoid, COUNT(*) * 4 AS score
    FROM   photos p
    CROSS  JOIN terms t
    WHERE  p.deleted_at IS NULL
      AND  p.title_text ILIKE '%' || t.term || '%'
    GROUP  BY p.photoid
),
desc_hits AS (
    SELECT p.photoid, COUNT(*) * 3 AS score
    FROM   photos p
    CROSS  JOIN terms t
    WHERE  p.deleted_at IS NULL
      AND  p.description ILIKE '%' || t.term || '%'
    GROUP  BY p.photoid
),
label_hits AS (
    SELECT l.photoid, COUNT(*) * 2 AS score
    FROM   labels l
    CROSS  JOIN terms t
    WHERE  l.deleted_at IS NULL
      AND  (l.name  ILIKE '%' || t.term || '%'
            OR l.value ILIKE '%' || t.term || '%')
    GROUP  BY l.photoid
),
emoji_hits AS (
    SELECT er.photoid, COUNT(*) * 2 AS score
    FROM   emoji_reactions er
    JOIN   emoji_types et ON et.emojiid = er.emojiid
    CROSS  JOIN terms t
    WHERE  et.alt_text ILIKE '%' || t.term || '%'
    GROUP  BY er.photoid
),
comment_hits AS (
    SELECT c.photoid, COUNT(*) * 1 AS score
    FROM   comments c
    CROSS  JOIN terms t
    WHERE  c.deleted_at IS NULL
      AND  c.comment_text ILIKE '%' || t.term || '%'
    GROUP  BY c.photoid
),
all_scores AS (
    SELECT photoid, score FROM title_hits
    UNION ALL
    SELECT photoid, score FROM desc_hits
    UNION ALL
    SELECT photoid, score FROM label_hits
    UNION ALL
    SELECT photoid, score FROM emoji_hits
    UNION ALL
    SELECT photoid, score FROM comment_hits
),
ranked AS (
    SELECT photoid, SUM(score) AS total_score
    FROM   all_scores
    GROUP  BY photoid
    ORDER  BY total_score DESC, photoid   -- stable tiebreak
    LIMIT  13
)
SELECT r.photoid::text, p.image_url, p.image_width, p.image_height
FROM   ranked r
JOIN   photos p ON p.photoid = r.photoid
WHERE  p.deleted_at IS NULL
  AND  ($2 = '' OR p.exhibitionid::text = $2)
  AND  (p.is_public OR $3)
ORDER  BY r.total_score DESC
`

	rows, err := h.DB.Query(ctx, sqlSearch, terms, exhibitionID, canSeeNonPublic)
	if err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	results := make([]models.SearchResult, 0)
	for rows.Next() {
		var sr models.SearchResult
		if err := rows.Scan(&sr.PhotoID, &sr.ImageURL, &sr.Width, &sr.Height); err != nil {
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		sr.ImageURL = proxyImageURL(sr.ImageURL)
		results = append(results, sr)
	}
	if err := rows.Err(); err != nil {
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, models.SearchResponse{
		Query:   query,
		Total:   len(results),
		Results: results,
	})
}
