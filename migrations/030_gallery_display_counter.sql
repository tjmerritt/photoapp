-- migrations/030_gallery_display_counter.sql
-- Adds a persistent, monotonically-increasing per-gallery counter used to
-- generate each display's default "Display NNN" name.
--
-- Why: migration 029's approach (count displays in the gallery, including
-- soft-deleted ones, via COUNT(*) at create time) was correct on its own,
-- but the Add Display popup's client-side name guess used the gallery's
-- *active* display_count instead (the only count the gallery list response
-- exposes) and always sent that guess as an explicit name — so the
-- server's deletion-aware COUNT(*) fallback never actually ran in
-- practice. Net effect: add two displays ("Display 001", "Display 002"),
-- delete the first, add a third — active count is back down to 1, so the
-- guess (and the name actually sent) collides with the still-existing
-- "Display 002". A real counter fixes this at the source: it only ever
-- goes up, deletions don't affect it, and it's exposed to the client
-- (GallerySummary.display_counter) so the popup's prefilled guess is the
-- exact value the server will assign, not a separate approximation that
-- can drift from it.
--
-- No schema_migrations tracking table in this project — every migration
-- re-runs in full on every `make migrate-up`, so both statements below are
-- idempotent on their own: ADD COLUMN IF NOT EXISTS, and an UPDATE guarded
-- by `display_counter = 0` so it only initializes galleries that haven't
-- been touched yet (by this backfill on an earlier run, or by a real
-- display having been created since) — re-running never resets a counter
-- that's already advanced.
--
-- Run with: psql $DATABASE_URL -f migrations/030_gallery_display_counter.sql

BEGIN;

ALTER TABLE galleries ADD COLUMN IF NOT EXISTS display_counter INTEGER NOT NULL DEFAULT 0;

-- Seed each gallery's counter from how many displays have ever existed in
-- it (including soft-deleted — same accounting migration 029's own
-- description used), so numbering continues from where the old
-- COUNT(*)-at-create-time approach would have left off, rather than
-- restarting at 1 and immediately colliding with already-assigned names.
UPDATE galleries g
SET    display_counter = sub.cnt
FROM   (
    SELECT galleryid, COUNT(*) AS cnt
    FROM   displays
    GROUP  BY galleryid
) sub
WHERE  g.galleryid = sub.galleryid
  AND  g.display_counter = 0;

COMMIT;
