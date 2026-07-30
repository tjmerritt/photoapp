-- migrations/019_organizations.sql
-- PLAN2.md Phase 1a: Organizations (Foundation).
--
-- An Organization is the billing entity and owns exhibitions. This migration
-- adds the table and links exhibitions to it. The application-level
-- "auto-create an organization for a user when they create their first
-- exhibition" behavior (also part of 1a) is implemented in
-- internal/handlers/exhibitions.go's ExhibitionsHandler.Create /
-- resolveOrganizationID, added in the same round of work as this migration --
-- see that file's doc comments for how "first exhibition" is determined
-- (there's no real org-membership concept yet, so it's inferred from
-- existing Admin grants until Phase 1c adds one). Exhibitions created before
-- either of these existed (the seeded default exhibition, anything from
-- scripts/seed-exhibition.sh, Phase 0's test suite) are handled by the
-- Legacy Organization backfill below instead.
--
-- Column naming note: PLAN2.md's own text says "exhibitions.organization_id",
-- but every identifier column elsewhere in this schema is unbroken
-- (exhibitionid, teamid, roleid, galleryid, displayid, emojiid, labelid,
-- userid, ...) -- that's read as PLAN2.md's planning-doc shorthand, not a
-- deliberate naming decision, so this migration uses organizationid to match
-- everything else instead of introducing the schema's only underscored FK.
--
-- Run with: psql $DATABASE_URL -f migrations/019_organizations.sql

BEGIN;

CREATE TABLE IF NOT EXISTS organizations (
    organizationid UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ
);

-- ── Legacy organization ──────────────────────────────────────────────────────
--
-- Every exhibition that already exists (the seeded default exhibition, plus
-- anything created before this migration -- including everything Phase 0's
-- test suite has created in the shared test database) needs an
-- organizationid the moment the column goes NOT NULL below. Per the user's
-- decision, they all get assigned to one shared "Legacy Organization" rather
-- than being left NULL or split apart by inferred ownership -- simplest to
-- reason about, and splittable by hand later if it ever matters.
--
-- Fixed UUID (not gen_random_uuid()) so this insert is idempotent and so any
-- future migration/script can refer to "the legacy org" by a known constant,
-- matching migrations/008_exhibitions.sql's 'ffffffff-...-000000000001'
-- convention for the seeded default exhibition.
INSERT INTO organizations (organizationid, name) VALUES
    ('eeeeeeee-0000-0000-0000-000000000001', 'Legacy Organization')
ON CONFLICT (organizationid) DO NOTHING;

-- ── exhibitions.organizationid ───────────────────────────────────────────────
--
-- DEFAULT (not just a backfill UPDATE) is deliberate: until the "auto-create
-- an org for a user's first exhibition" application behavior above actually
-- exists, anything that inserts an exhibition without specifying
-- organizationid explicitly -- testutil.CreateExhibition, any future manual
-- SQL insert, scripts/seed-exhibition.sh's assumption that the exhibition row
-- already exists -- keeps working unchanged and lands in the Legacy
-- Organization, the same place every pre-existing exhibition just got
-- assigned. Once real org-creation logic exists, it sets organizationid
-- explicitly at insert time and this default simply stops being used.
ALTER TABLE exhibitions
    ADD COLUMN IF NOT EXISTS organizationid UUID
    NOT NULL DEFAULT 'eeeeeeee-0000-0000-0000-000000000001'
    REFERENCES organizations (organizationid) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_exhibitions_organization
    ON exhibitions (organizationid) WHERE deleted_at IS NULL;

COMMIT;
