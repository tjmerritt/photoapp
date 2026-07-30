package handlers_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/tjmerritt/photoapp/internal/handlers"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

func TestExhibitionHandler_Lookup_ColdCacheRefreshesAndFinds(t *testing.T) {
	env := newTestEnv(t)
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	hostname := "test-" + uuid.NewString() + ".example.invalid"
	if _, err := env.Pool.Exec(context.Background(), `
		INSERT INTO exhibition_hostnames (exhibitionid, hostname) VALUES ($1::uuid, $2)
	`, exhibitionID, hostname); err != nil {
		t.Fatalf("seed exhibition_hostnames: %v", err)
	}

	// A fresh handler's cache is empty and its lastRefresh zero value is the
	// Unix epoch — far older than exhibitionCacheTTL — so this first Lookup
	// call must go through doRefresh's DB query rather than returning a
	// stale "not found" from an empty in-memory map.
	h := &handlers.ExhibitionHandler{DB: env.Pool}
	id, known := h.Lookup(context.Background(), hostname)
	if !known {
		t.Fatal("Lookup() known = false on first call for a hostname that exists in the DB, want true")
	}
	if id != exhibitionID {
		t.Errorf("Lookup() id = %q, want %q", id, exhibitionID)
	}
}

func TestExhibitionHandler_Lookup_UnknownHostname(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ExhibitionHandler{DB: env.Pool}

	id, known := h.Lookup(context.Background(), "definitely-not-registered-"+uuid.NewString()+".example.invalid")
	if known {
		t.Errorf("Lookup() known = true for an unregistered hostname (id=%q), want false", id)
	}
}

func TestExhibitionHandler_Lookup_CachesWithinTTL(t *testing.T) {
	env := newTestEnv(t)
	h := &handlers.ExhibitionHandler{DB: env.Pool}
	ctx := context.Background()

	// Prime the cache with a first (cold, refreshing) lookup.
	seedHostname := "seed-" + uuid.NewString() + ".example.invalid"
	exhibitionID := testutil.CreateExhibition(t, env.Pool)
	if _, err := env.Pool.Exec(ctx, `
		INSERT INTO exhibition_hostnames (exhibitionid, hostname) VALUES ($1::uuid, $2)
	`, exhibitionID, seedHostname); err != nil {
		t.Fatalf("seed exhibition_hostnames: %v", err)
	}
	if _, known := h.Lookup(ctx, seedHostname); !known {
		t.Fatal("priming Lookup() didn't find the seeded hostname")
	}

	// Insert a second hostname directly, bypassing the handler's cache
	// entirely (mirrors another process registering a new hostname while
	// this one's cache is still warm).
	freshHostname := "fresh-" + uuid.NewString() + ".example.invalid"
	freshExhibitionID := testutil.CreateExhibition(t, env.Pool)
	if _, err := env.Pool.Exec(ctx, `
		INSERT INTO exhibition_hostnames (exhibitionid, hostname) VALUES ($1::uuid, $2)
	`, freshExhibitionID, freshHostname); err != nil {
		t.Fatalf("seed second exhibition_hostnames row: %v", err)
	}

	// Looked up immediately (well within exhibitionCacheTTL of the priming
	// refresh), this must NOT trigger another DB query — Lookup returns
	// "not found" from the still-warm, now-stale-relative-to-the-DB cache,
	// rather than always reflecting the database's current state. This is
	// what actually exercises the mutex/atomic staleness bookkeeping in
	// Lookup, not just doRefresh's query itself.
	if _, known := h.Lookup(ctx, freshHostname); known {
		t.Error("Lookup() found a hostname inserted after the cache was primed, want it to still be using the cached (stale) data within the TTL window")
	}
}
