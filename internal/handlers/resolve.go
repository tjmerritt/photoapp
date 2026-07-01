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
