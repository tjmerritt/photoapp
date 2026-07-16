package handlers

import (
	"context"
	"sync"

	//"github.com/jackc/pgx/v5"
	"github.com/tjmerritt/photoapp/internal/db"
)

// syncMap is a generic, goroutine-safe map. It mirrors the essential API of
// the proposed sync/v2 Map type (which is not yet finalized) using a
// sync.RWMutex over a plain map, giving compile-time type safety without
// depending on an unstable package.
type syncMap[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

func (s *syncMap[K, V]) Load(key K) (V, bool) {
	s.mu.RLock()
	v, ok := s.m[key]
	s.mu.RUnlock()
	return v, ok
}

func (s *syncMap[K, V]) Store(key K, val V) {
	s.mu.Lock()
	if s.m == nil {
		s.m = make(map[K]V)
	}
	s.m[key] = val
	s.mu.Unlock()
}

// photoExhibitionCache maps photoid → exhibitionid.
// A photo's exhibitionid is immutable — photos are never moved between
// exhibitions after creation. It is therefore safe to cache these mappings
// for the lifetime of the process without any invalidation logic.
var photoExhibitionCache syncMap[string, string]

// resolvePhotoExhibition returns the exhibitionid for the given photoid.
//
// On the first call for a given photoid the result is fetched from the
// database and stored in the process-wide cache. Subsequent calls for the
// same photoid return the cached value without hitting the database.
//
// Returns ("", pgx.ErrNoRows) when the photo does not exist or has been
// soft-deleted. Errors from a deleted photo are not cached so that an
// accidental lookup before deletion completes does not permanently suppress
// the result for a photoid that might be reused (though in practice UUIDs
// are never reused).
func resolvePhotoExhibition(ctx context.Context, pool *db.Pool, photoid string) (string, error) {
	if v, ok := photoExhibitionCache.Load(photoid); ok {
		return v, nil
	}

	var exhibitionID string
	err := pool.QueryRow(ctx, `
		SELECT exhibitionid::text FROM photos
		WHERE  photoid = $1 AND deleted_at IS NULL
	`, photoid).Scan(&exhibitionID)
	if err != nil {
		// Do not cache failures: a missing photo might be a race with an
		// in-progress insert, and pgx.ErrNoRows is the caller's signal to
		// return 404.
		return "", err
	}

	// Store only on success. If two goroutines race here the second Store
	// simply overwrites with the same value — harmless.
	photoExhibitionCache.Store(photoid, exhibitionID)
	return exhibitionID, nil
}

// displayGalleryCache maps displayid → galleryid.
// A display's galleryid is immutable — displays are never moved between
// galleries after creation.
var displayGalleryCache syncMap[string, string]

// resolveDisplayGallery returns the galleryid for the given displayid.
// Returns ("", pgx.ErrNoRows) when the display does not exist or has been
// soft-deleted. Errors are not cached.
func resolveDisplayGallery(ctx context.Context, pool *db.Pool, displayid string) (string, error) {
	if v, ok := displayGalleryCache.Load(displayid); ok {
		return v, nil
	}

	var galleryID string
	err := pool.QueryRow(ctx, `
		SELECT galleryid::text FROM displays
		WHERE  displayid = $1 AND deleted_at IS NULL
	`, displayid).Scan(&galleryID)
	if err != nil {
		return "", err
	}

	displayGalleryCache.Store(displayid, galleryID)
	return galleryID, nil
}

// ── Phase 6b: per-user "manage my own X" overrides ───────────────────────────
//
// These read the users.can_manage_own_* columns added in
// migrations/017_admin_phase6.sql. They are deliberately NOT cached like the
// lookups above — an admin flipping one of these toggles needs to take
// effect on the very next request, not whenever a stale cache entry expires.
// Each is a hard AND on top of the normal permission check in its caller: a
// user who fails this still needs PhotoLabelCreate/PhotoEmojiCreate&Delete/
// PhotoCommentCreate as usual, but passing the permission check alone is no
// longer sufficient once an admin has switched this off for them.

func userCanManageOwnLabels(ctx context.Context, pool *db.Pool, userID string) (bool, error) {
	var v bool
	err := pool.QueryRow(ctx, `SELECT can_manage_own_labels FROM users WHERE userid=$1`, userID).Scan(&v)
	return v, err
}

func userCanManageOwnEmoji(ctx context.Context, pool *db.Pool, userID string) (bool, error) {
	var v bool
	err := pool.QueryRow(ctx, `SELECT can_manage_own_emoji FROM users WHERE userid=$1`, userID).Scan(&v)
	return v, err
}

func userCanManageOwnComments(ctx context.Context, pool *db.Pool, userID string) (bool, error) {
	var v bool
	err := pool.QueryRow(ctx, `SELECT can_manage_own_comments FROM users WHERE userid=$1`, userID).Scan(&v)
	return v, err
}
