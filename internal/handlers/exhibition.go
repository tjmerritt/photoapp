package handlers

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tjmerritt/photoapp/internal/db"
)

const exhibitionCacheTTL = 60 * time.Second

// ExhibitionHandler resolves exhibitions from request hostnames using an
// in-process cache. Reads are lock-free via an atomic pointer; a mutex
// serializes concurrent refreshes so only one goroutine queries the DB.
type ExhibitionHandler struct {
	DB *db.Pool

	mu          sync.Mutex // serializes refresh; never held during reads
	hostnames   atomic.Pointer[map[string]string]
	lastRefresh atomic.Int64 // UnixNano of last successful refresh
}

// Lookup returns the exhibitionid for the given hostname and true when the
// hostname is registered. When the hostname is absent from the cache and the
// cache is older than exhibitionCacheTTL, the cache is refreshed from the DB
// and the refresh event is logged together with the triggering domain.
func (h *ExhibitionHandler) Lookup(ctx context.Context, hostname string) (id string, known bool) {
	// Fast path: atomic load — no lock, no contention.
	if m := h.hostnames.Load(); m != nil {
		if id, known = (*m)[hostname]; known {
			return id, true
		}
	}

	// Cache miss. Only refresh when the cache is stale.
	if time.Since(time.Unix(0, h.lastRefresh.Load())) <= exhibitionCacheTTL {
		return "", false
	}

	// Stale + miss — serialize the refresh with a mutex.
	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check: another goroutine may have refreshed while we waited.
	if m := h.hostnames.Load(); m != nil {
		if id, known = (*m)[hostname]; known {
			return id, true
		}
	}
	if time.Since(time.Unix(0, h.lastRefresh.Load())) <= exhibitionCacheTTL {
		return "", false
	}

	h.doRefresh(ctx, hostname)

	if m := h.hostnames.Load(); m != nil {
		id, known = (*m)[hostname]
	}
	return id, known
}

func (h *ExhibitionHandler) doRefresh(ctx context.Context, triggerDomain string) {
	rows, err := h.DB.Query(ctx, `SELECT hostname, exhibitionid::text FROM exhibition_hostnames`)
	if err != nil {
		slog.Error("exhibition hostname cache refresh failed", "error", err)
		return
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var hostname, exhibitionID string
		if err := rows.Scan(&hostname, &exhibitionID); err == nil {
			m[hostname] = exhibitionID
		}
	}

	h.hostnames.Store(&m)
	h.lastRefresh.Store(time.Now().UnixNano())
	slog.Info("exhibition hostname cache refreshed",
		"trigger_domain", triggerDomain,
		"hostname_count", len(m))
}
