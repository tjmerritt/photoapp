-- migrations/020_emoji_ownership.sql
-- PLAN2.md Phase 1b: emoji ownership.
--
-- Uploaded (custom) emoji types are now owned by an organization; emoji
-- types with no organization are global and usable in every organization.
-- No backfill needed: NULL is exactly the right value for every existing
-- row (both the seeded set from migrations/002_seed.sql and anything
-- imported via cmd/import-emojis are meant to stay global), so leaving the
-- column nullable with no default does that for free.
--
-- ON DELETE SET NULL rather than CASCADE: deleting an organization
-- shouldn't destroy its custom emoji images/rows (and, transitively, every
-- photo's reaction history using them) — it just makes them global instead.
-- There is no organization-delete endpoint yet, so this is a low-stakes
-- choice today, but SET NULL is the non-destructive default either way.
--
-- Run with: psql $DATABASE_URL -f migrations/020_emoji_ownership.sql

BEGIN;

ALTER TABLE emoji_types
    ADD COLUMN IF NOT EXISTS organizationid UUID
    REFERENCES organizations (organizationid) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_emoji_types_organization
    ON emoji_types (organizationid) WHERE organizationid IS NOT NULL;

COMMIT;
