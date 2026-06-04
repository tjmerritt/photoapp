-- migrations/005_emoji_skintone.sql
-- Add skintone variant support to emoji_types.

BEGIN;

ALTER TABLE emoji_types ADD COLUMN IF NOT EXISTS skintone     TEXT;  -- null = base emoji; e.g. "light", "medium", "dark"
ALTER TABLE emoji_types ADD COLUMN IF NOT EXISTS base_hexcode TEXT;  -- null = base emoji; hexcode of parent for variants

CREATE INDEX IF NOT EXISTS idx_emoji_base_hexcode ON emoji_types (base_hexcode) WHERE base_hexcode IS NOT NULL;

COMMIT;
