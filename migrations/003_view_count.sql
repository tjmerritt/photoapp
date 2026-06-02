-- migrations/003_view_count.sql
-- Add view_count to photos for popularity-based related photo ranking.

BEGIN;

ALTER TABLE photos ADD COLUMN IF NOT EXISTS view_count INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_photos_view_count ON photos (view_count DESC) WHERE deleted_at IS NULL;

COMMIT;
