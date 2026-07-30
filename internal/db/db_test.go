package db_test

import (
	"context"
	"testing"

	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/testutil"
)

func TestNew_InvalidDSN_ReturnsError(t *testing.T) {
	_, err := db.New(context.Background(), "not a valid dsn at all")
	if err == nil {
		t.Fatal("New did not error on a malformed DSN")
	}
}

func TestNew_ConnectsAndPings(t *testing.T) {
	// testutil.RequireDB already calls db.New internally (and skips this
	// test when TEST_DATABASE_URL isn't set) — this re-confirms New/Ping
	// succeed against a real database and directly exercises
	// RefreshEmojiCounts, which the labels/emoji handler tests only ever
	// exercise indirectly through the React/Unreact handlers.
	pool := testutil.RequireDB(t)
	if err := pool.RefreshEmojiCounts(context.Background()); err != nil {
		t.Fatalf("RefreshEmojiCounts: %v", err)
	}
}
