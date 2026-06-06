package handlers

import (
	"context"

	"github.com/tjmerritt/photoapp/internal/db"
)

// ExhibitionHandler resolves exhibitions from request hostnames.
type ExhibitionHandler struct {
	DB *db.Pool
}

// LookupByHostname returns the exhibitionid for the given hostname, or "" if not found.
func (h *ExhibitionHandler) LookupByHostname(ctx context.Context, hostname string) string {
	var id string
	_ = h.DB.QueryRow(ctx, `
		SELECT exhibitionid::text
		FROM   exhibition_hostnames
		WHERE  hostname = $1
	`, hostname).Scan(&id)
	return id
}
