package handlers

import (
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/tjmerritt/photoapp/internal/models"
)

// parsePage extracts offset and limit from query params, clamping limit to max.
func parsePage(r *http.Request, defaultLimit, maxLimit int) (offset, limit int) {
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return
}

// buildPages constructs the Pages envelope for a paginated list response.
// baseURL should be everything before &offset=N (e.g. "/api/v1/labels?photoid=x&limit=10").
func buildPages(total, offset, limit int, baseURL string) models.Pages {
	pageCount := int(math.Ceil(float64(total) / float64(limit)))
	if pageCount < 1 {
		pageCount = 1
	}
	current := offset/limit + 1

	urlAt := func(o int) string {
		return fmt.Sprintf("%s&offset=%d", baseURL, o)
	}
	lastOffset := (pageCount - 1) * limit

	var next, prev *string
	if offset+limit < total {
		s := urlAt(offset + limit)
		next = &s
	}
	if offset > 0 {
		s := urlAt(max(0, offset-limit))
		prev = &s
	}

	return models.Pages{
		Count:   pageCount,
		Current: current,
		First:   urlAt(0),
		Last:    urlAt(lastOffset),
		Next:    next,
		Prev:    prev,
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// nullableUUID converts an empty string to a real Go nil so pgx sends a SQL
// NULL on the wire for that query parameter instead of the literal string
// "". Any parameter whose only uses in a query are inside a ::uuid cast gets
// its inferred parameter type set to uuid itself by Postgres — meaning the
// empty string has to be parsed as a uuid the instant pgx binds it, before
// the query body (and any in-SQL guard, including NULLIF) ever runs. This is
// the same fix applied to permissions.Checker.Check/UserPermissions after
// the guard/NULLIF approaches both still threw "invalid input syntax for
// type uuid" in production — see SUMMARIES2.md Sessions 23-24.
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
