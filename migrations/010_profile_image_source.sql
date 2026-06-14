-- migrations/010_profile_image_source.sql
-- Add profile_image_source to persist the URL of a user's real photo
-- (downloaded OAuth picture or uploaded image) separately from the
-- currently-selected display image (which may be a generated avatar).

BEGIN;
ALTER TABLE users ADD COLUMN IF NOT EXISTS profile_image_source TEXT;
COMMIT;
