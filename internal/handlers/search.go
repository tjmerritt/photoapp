package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
	"github.com/tjmerritt/photoapp/internal/models"
)

// SearchHandler handles GET /api/v1/search?q=<query>
//
// Supported qualifiers (combinable with each other and with free text):
//
//	@username / user:@username   — photos the user has interacted with
//	label:Name                   — photos with a label matching Name
//	label:Name=Value             — photos with that label Name and Value
//	Name:Value                   — shorthand for label:Name=Value
//	emoji:Name                   — photos with an emoji reaction matching Name
//	comment:Text                 — photos with a comment matching Text
//	title:Text                   — photos with a title matching Text
//	desc:Text / description:Text — photos with a description matching Text
//
// Any remaining tokens are scored as free text across all fields.
type SearchHandler struct {
	DB *db.Pool
}

// ── Query Parsing ─────────────────────────────────────────────────────────────

type labelFilter struct {
	Name  string
	Value string // empty = name-only match
}

type parsedQuery struct {
	FreeTerms    []string
	Users        []string
	Labels       []labelFilter
	EmojiNames   []string
	CommentTexts []string
	TitleTexts   []string
	DescTexts    []string
}

func (pq parsedQuery) isEmpty() bool {
	return len(pq.FreeTerms) == 0 && len(pq.Users) == 0 && len(pq.Labels) == 0 &&
		len(pq.EmojiNames) == 0 && len(pq.CommentTexts) == 0 &&
		len(pq.TitleTexts) == 0 && len(pq.DescTexts) == 0
}

// reservedPrefixes prevents keyword names from being parsed as label:Name=Value shorthands.
var reservedPrefixes = map[string]bool{
	"user": true, "label": true, "emoji": true,
	"comment": true, "title": true, "desc": true, "description": true,
}

func parseSearchQuery(raw string) parsedQuery {
	var pq parsedQuery
	seen := map[string]bool{}

	for _, tok := range strings.Fields(raw) {
		lo := strings.ToLower(tok)

		switch {
		case strings.HasPrefix(lo, "@"):
			// @username
			if u := lo[1:]; u != "" {
				pq.Users = append(pq.Users, u)
			}

		case strings.HasPrefix(lo, "user:"):
			// user:@username or user:username
			u := strings.TrimPrefix(lo[5:], "@")
			if u != "" {
				pq.Users = append(pq.Users, u)
			}

		case strings.HasPrefix(lo, "label:"):
			// label:Name or label:Name=Value
			rest := lo[6:]
			parts := strings.SplitN(rest, "=", 2)
			lf := labelFilter{Name: parts[0]}
			if len(parts) == 2 {
				lf.Value = parts[1]
			}
			if lf.Name != "" {
				pq.Labels = append(pq.Labels, lf)
			}

		case strings.HasPrefix(lo, "emoji:"):
			if v := lo[6:]; v != "" {
				pq.EmojiNames = append(pq.EmojiNames, v)
			}

		case strings.HasPrefix(lo, "comment:"):
			if v := lo[8:]; v != "" {
				pq.CommentTexts = append(pq.CommentTexts, v)
			}

		case strings.HasPrefix(lo, "title:"):
			if v := lo[6:]; v != "" {
				pq.TitleTexts = append(pq.TitleTexts, v)
			}

		case strings.HasPrefix(lo, "desc:"):
			if v := lo[5:]; v != "" {
				pq.DescTexts = append(pq.DescTexts, v)
			}

		case strings.HasPrefix(lo, "description:"):
			if v := lo[12:]; v != "" {
				pq.DescTexts = append(pq.DescTexts, v)
			}

		default:
			// Name:Value → label shorthand when the prefix is not a reserved keyword
			if idx := strings.IndexByte(lo, ':'); idx > 0 && !reservedPrefixes[lo[:idx]] {
				name, value := lo[:idx], lo[idx+1:]
				if name != "" && value != "" {
					pq.Labels = append(pq.Labels, labelFilter{Name: name, Value: value})
					continue
				}
			}
			// Plain free-text term
			if lo != "" && !seen[lo] {
				seen[lo] = true
				pq.FreeTerms = append(pq.FreeTerms, lo)
			}
		}
	}

	return pq
}

// ── SQL Builder ───────────────────────────────────────────────────────────────

// buildSearchSQL constructs a parameterised query from the parsed query.
//
// Fixed parameters:
//
//	$1 = exhibitionID string ('' means all exhibitions)
//	$2 = canSeeNonPublic bool
//
// Additional parameters are appended dynamically starting at $3.
func buildSearchSQL(pq parsedQuery, exhibitionID string, canSeeNonPublic bool) (string, []interface{}) {
	args := []interface{}{exhibitionID, canSeeNonPublic}
	argN := 2

	// next registers a new query argument and returns its placeholder.
	next := func(v interface{}) string {
		argN++
		args = append(args, v)
		return fmt.Sprintf("$%d", argN)
	}

	// ilike builds a case-insensitive substring match expression.
	// col is a SQL column reference; arg is a parameter placeholder like $3.
	ilike := func(col, arg string) string {
		return fmt.Sprintf("%s ILIKE '%%' || %s || '%%'", col, arg)
	}

	var b strings.Builder

	// ── Scoring CTE (only when free text is present) ──────────────────────────
	if len(pq.FreeTerms) > 0 {
		ta := next(pq.FreeTerms)
		fmt.Fprintf(&b, `WITH terms(term) AS (
    SELECT unnest(%s::text[])
),
score_rows AS (
    SELECT p.photoid, COUNT(*) * 4 AS score
    FROM   photos p CROSS JOIN terms t
    WHERE  p.deleted_at IS NULL AND p.title_text ILIKE '%%' || t.term || '%%'
    GROUP  BY p.photoid
    UNION ALL
    SELECT p.photoid, COUNT(*) * 3 AS score
    FROM   photos p CROSS JOIN terms t
    WHERE  p.deleted_at IS NULL AND p.description ILIKE '%%' || t.term || '%%'
    GROUP  BY p.photoid
    UNION ALL
    SELECT l.photoid, COUNT(*) * 2 AS score
    FROM   labels l CROSS JOIN terms t
    WHERE  l.deleted_at IS NULL
      AND  (l.name ILIKE '%%' || t.term || '%%' OR l.value ILIKE '%%' || t.term || '%%')
    GROUP  BY l.photoid
    UNION ALL
    SELECT er.photoid, COUNT(*) * 2 AS score
    FROM   emoji_reactions er
    JOIN   emoji_types et ON et.emojiid = er.emojiid
    CROSS  JOIN terms t
    WHERE  et.alt_text ILIKE '%%' || t.term || '%%'
    GROUP  BY er.photoid
    UNION ALL
    SELECT c.photoid, COUNT(*) * 1 AS score
    FROM   comments c CROSS JOIN terms t
    WHERE  c.deleted_at IS NULL AND c.comment_text ILIKE '%%' || t.term || '%%'
    GROUP  BY c.photoid
),
scores(photoid, total_score) AS (
    SELECT photoid, SUM(score) FROM score_rows GROUP BY photoid
)
`, ta)
	}

	// ── SELECT ────────────────────────────────────────────────────────────────
	if len(pq.FreeTerms) > 0 {
		b.WriteString("SELECT p.photoid::text, p.image_url, p.image_width, p.image_height, MAX(sc.total_score) AS sort_score\n")
	} else {
		b.WriteString("SELECT p.photoid::text, p.image_url, p.image_width, p.image_height, 0 AS sort_score\n")
	}
	b.WriteString("FROM photos p\n")

	// ── JOINs ─────────────────────────────────────────────────────────────────

	// Free text: INNER JOIN ensures only photos with a positive score are included.
	if len(pq.FreeTerms) > 0 {
		b.WriteString("JOIN scores sc ON sc.photoid = p.photoid\n")
	}

	// User filter: photos the user has interacted with (comment, label, or emoji).
	for i, user := range pq.Users {
		ua := next(user)
		fmt.Fprintf(&b, `JOIN (
    SELECT DISTINCT photoid FROM comments c
    JOIN users u ON u.userid = c.author_userid
    WHERE c.deleted_at IS NULL AND %s
    UNION
    SELECT DISTINCT photoid FROM labels l
    JOIN users u ON u.userid = l.added_by_userid
    WHERE l.deleted_at IS NULL AND %s
    UNION
    SELECT DISTINCT photoid FROM emoji_reactions er
    JOIN users u ON u.userid = er.userid
    WHERE %s
) uf%d ON uf%d.photoid = p.photoid
`,
			ilike("u.username", ua),
			ilike("u.username", ua),
			ilike("u.username", ua),
			i, i)
	}

	// Label filters: each qualifier adds an INNER JOIN that restricts the result.
	for i, lf := range pq.Labels {
		alias := fmt.Sprintf("lf%d", i)
		na := next(lf.Name)
		nameExpr := ilike(alias+".name", na)
		if lf.Value != "" {
			va := next(lf.Value)
			valueExpr := ilike(alias+".value", va)
			fmt.Fprintf(&b, "JOIN labels %s ON %s.photoid = p.photoid AND %s.deleted_at IS NULL AND %s AND %s\n",
				alias, alias, alias, nameExpr, valueExpr)
		} else {
			fmt.Fprintf(&b, "JOIN labels %s ON %s.photoid = p.photoid AND %s.deleted_at IS NULL AND %s\n",
				alias, alias, alias, nameExpr)
		}
	}

	// Emoji filter.
	for i, em := range pq.EmojiNames {
		ea := next(em)
		fmt.Fprintf(&b, `JOIN (
    SELECT DISTINCT er.photoid FROM emoji_reactions er
    JOIN emoji_types et ON et.emojiid = er.emojiid
    WHERE %s
) emf%d ON emf%d.photoid = p.photoid
`, ilike("et.alt_text", ea), i, i)
	}

	// Comment filter.
	for i, ct := range pq.CommentTexts {
		ca := next(ct)
		fmt.Fprintf(&b, `JOIN (
    SELECT DISTINCT photoid FROM comments
    WHERE deleted_at IS NULL AND %s
) cmf%d ON cmf%d.photoid = p.photoid
`, ilike("comment_text", ca), i, i)
	}

	// ── WHERE ─────────────────────────────────────────────────────────────────
	b.WriteString("WHERE p.deleted_at IS NULL\n")
	b.WriteString("  AND ($1 = '' OR p.exhibitionid::text = $1)\n")
	b.WriteString("  AND (p.is_public OR $2)\n")

	for _, tt := range pq.TitleTexts {
		ta := next(tt)
		fmt.Fprintf(&b, "  AND %s\n", ilike("p.title_text", ta))
	}
	for _, dt := range pq.DescTexts {
		da := next(dt)
		fmt.Fprintf(&b, "  AND %s\n", ilike("p.description", da))
	}

	// ── GROUP BY + ORDER ───────────────────────────────────────────────────────
	// GROUP BY collapses duplicate rows from multi-row JOINs (e.g. multiple labels).
	// created_at is included so it can be used as a tiebreaker without a subquery.
	b.WriteString("GROUP BY p.photoid, p.image_url, p.image_width, p.image_height, p.created_at\n")
	b.WriteString("ORDER BY sort_score DESC, p.created_at DESC, p.photoid\n")
	b.WriteString("LIMIT 13\n")

	return b.String(), args
}

// ── Handler ───────────────────────────────────────────────────────────────────

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

	pq := parseSearchQuery(query)
	if pq.isEmpty() {
		middleware.WriteJSON(w, http.StatusOK, models.SearchResponse{
			Query:   query,
			Total:   0,
			Results: []models.SearchResult{},
		})
		return
	}

	ctx := r.Context()
	exhibitionID := middleware.ExhibitionID(ctx)
	canSeeNonPublic := middleware.AuthorizedNonPublic(ctx)

	sql, args := buildSearchSQL(pq, exhibitionID, canSeeNonPublic)

	rows, err := h.DB.Query(ctx, sql, args...)
	if err != nil {
		slog.Error("ServeHTTP", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	results := make([]models.SearchResult, 0)
	for rows.Next() {
		var sr models.SearchResult
		var sortScore int64 // consumed for ordering, not returned to client
		if err := rows.Scan(&sr.PhotoID, &sr.ImageURL, &sr.Width, &sr.Height, &sortScore); err != nil {
			slog.Error("ServeHTTP", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "db error")
			return
		}
		sr.ImageURL = proxyImageURL(sr.ImageURL)
		results = append(results, sr)
	}
	if err := rows.Err(); err != nil {
		slog.Error("ServeHTTP", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}

	middleware.WriteJSON(w, http.StatusOK, models.SearchResponse{
		Query:   query,
		Total:   len(results),
		Results: results,
	})
}
