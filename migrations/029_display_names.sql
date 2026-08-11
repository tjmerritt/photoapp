-- migrations/029_display_names.sql
-- Adds a persistent, user-editable name to each display, so the display list
-- (Gallery Manager) and the Display Manager editor can show something more
-- stable than "Display N" — the position-based number displayed elsewhere
-- (e.g. "Display 3 of 7" on the editing page's breadcrumb) shifts whenever
-- displays are reordered (see the new thumb-grip drag-to-reorder feature),
-- which is confusing when it's also the only identifier a display has. The
-- name is set once at creation (default "Display NNN", NNN a zero-padded
-- count of every display ever created in that gallery, deleted or not, so
-- the number is never reused and never changes on its own) and is free-form
-- text after that — editable by clicking it, same as a gallery's title.
--
-- No schema_migrations tracking table in this project — every migration
-- re-runs in full on every `make migrate-up`, so both statements below have
-- to be idempotent on their own: ADD COLUMN IF NOT EXISTS, and an UPDATE
-- that only touches rows whose name is still the empty-string column
-- default (i.e. never actually set — by this backfill, by the Go create
-- handler going forward, or by a user's own rename), so re-running this
-- migration after either of those has happened is a safe no-op.
--
-- Run with: psql $DATABASE_URL -f migrations/029_display_names.sql

BEGIN;

ALTER TABLE displays ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';

-- One-time backfill for displays created before this column existed.
-- Numbers reflect creation order within each gallery (sort_order, then
-- created_at as a tiebreaker) at the moment this migration first runs —
-- after that, the name is independent of sort_order/reordering, same as
-- for any display created going forward.
UPDATE displays
SET    name = 'Display ' || lpad(numbered.rn::text, 3, '0')
FROM   (
    SELECT displayid, row_number() OVER (PARTITION BY galleryid ORDER BY sort_order, created_at) AS rn
    FROM   displays
) numbered
WHERE  displays.displayid = numbered.displayid
  AND  displays.name = '';

COMMIT;
