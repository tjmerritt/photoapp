-- migrations/004_emoji_unique.sql
-- Add group column and unique constraint on emoji_char to support incremental upserts.

BEGIN;

ALTER TABLE emoji_types ADD COLUMN IF NOT EXISTS emoji_group TEXT;
ALTER TABLE emoji_types ADD COLUMN IF NOT EXISTS tags        TEXT;
ALTER TABLE emoji_types ADD COLUMN IF NOT EXISTS hexcode     TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS uq_emoji_types_char
    ON emoji_types (emoji_char)
    WHERE emoji_char IS NOT NULL;

COMMIT;
