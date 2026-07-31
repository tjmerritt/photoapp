-- migrations/023_display_slots_photoid_index.sql
-- PLAN2.md Phase 2d: internal/handlers/fetch.go's photoAccessibleViaDisplaySQL
-- correlates display_slots against a candidate photo (ds.photoid = ...) once
-- per row of every photo-visibility query (photo.go, fetch.go, search.go).
-- display_slots had no index usable for that lookup before this migration --
-- only a (displayid, slot_index) unique index, which doesn't help a query
-- keyed by photoid. Partial (WHERE photoid IS NOT NULL) since slots are
-- frequently unfilled (see 014_galleries_displays.sql's own comment on
-- photoid being nullable while a slot is unfilled).
-- Run with: psql $DATABASE_URL -f migrations/023_display_slots_photoid_index.sql

BEGIN;

CREATE INDEX IF NOT EXISTS idx_display_slots_photoid
    ON display_slots (photoid) WHERE photoid IS NOT NULL;

COMMIT;
