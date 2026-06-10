-- migrations/009_public_flag.sql
-- Adds per-photo public visibility flag and per-user authorization for non-public photos.
--
-- is_public on photos:
--   TRUE  → visible to everyone, including unauthenticated users and users whose
--            authorized_non_public flag is FALSE.
--   FALSE → only visible to users whose authorized_non_public flag is TRUE.
--
-- authorized_non_public on users:
--   TRUE  → the user may see all photos regardless of is_public.
--   FALSE → the user (or unauthenticated visitor) may only see public photos.

BEGIN;

-- ── Photos: add is_public flag ────────────────────────────────────────────────
ALTER TABLE photos ADD COLUMN IF NOT EXISTS is_public BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_photos_is_public
    ON photos (is_public) WHERE deleted_at IS NULL;

-- ── Users: add authorized_non_public flag ─────────────────────────────────────
ALTER TABLE users ADD COLUMN IF NOT EXISTS authorized_non_public BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
