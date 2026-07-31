-- migrations/022_drop_is_public.sql
-- PLAN2.md Phase 2b: photo visibility is now determined solely by the
-- "Public" label (labels.name = 'Public', labels.value = 'True'/'False'),
-- not the photos.is_public column. See internal/handlers/fetch.go's
-- photoIsPublicSQL for the read-side query fragment every visibility check
-- now uses, and internal/handlers/admin.go's SetPublic for the write side.
--
-- Backfill first, then drop the column -- folds in exactly what
-- scripts/sync_public_labels.sql did by hand (that script is now historical;
-- it references is_public and can no longer run once this migration has):
--   1. Fix any existing "Public" label whose value doesn't match is_public.
--   2. Insert a "Public" label for any photo that doesn't have one yet
--      (e.g. every photo created through the browser upload endpoint before
--      this session, which never wrote one -- see internal/handlers/upload.go).
--
-- Both backfill steps run inside a DO block and reference photos.is_public
-- through EXECUTE'd dynamic SQL rather than a plain statement. This
-- migration set has no tracking table (see SUMMARIES2.md Session 20) --
-- make migrate-up re-runs every migration file every time -- so on the
-- SECOND run of this file, is_public will already be gone (dropped by this
-- same file's own first run). A plain SQL statement referencing
-- photos.is_public would fail at PARSE time on that second run even wrapped
-- in "IF column exists" logic, because Postgres validates a statement's
-- column references before ever checking whether the surrounding condition
-- is true; only text inside EXECUTE is parsed lazily, when it actually runs.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE  table_name = 'photos' AND column_name = 'is_public'
    ) THEN
        EXECUTE '
            UPDATE labels l
            SET    value      = CASE WHEN p.is_public THEN ''True'' ELSE ''False'' END,
                   updated_at = NOW()
            FROM   photos p
            WHERE  l.photoid    = p.photoid
              AND  l.name       = ''Public''
              AND  l.deleted_at IS NULL
              AND  p.deleted_at IS NULL
              AND  l.value     != CASE WHEN p.is_public THEN ''True'' ELSE ''False'' END
        ';

        EXECUTE '
            INSERT INTO labels (photoid, added_by_userid, name, value)
            SELECT p.photoid,
                   p.owner_userid,
                   ''Public'',
                   CASE WHEN p.is_public THEN ''True'' ELSE ''False'' END
            FROM   photos p
            WHERE  p.deleted_at IS NULL
              AND  NOT EXISTS (
                       SELECT 1 FROM labels l
                       WHERE  l.photoid    = p.photoid
                         AND  l.name       = ''Public''
                         AND  l.deleted_at IS NULL
                   )
        ';
    END IF;
END $$;

DROP INDEX IF EXISTS idx_photos_is_public;
ALTER TABLE photos DROP COLUMN IF EXISTS is_public;

COMMIT;
