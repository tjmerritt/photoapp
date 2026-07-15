-- migrations/018_grant_exhibitionid.sql
-- Adds an explicit exhibitionid column to entity_role_grants so a grant can be
-- scoped to one specific exhibition without going through the Gallery/
-- Display/Photo resource machinery. Replaces the old resource_type =
-- 'Exhibition' convention.
--
-- New semantics for a grant row:
--   exhibitionid IS NULL, resource_type IS NULL  -> global (every exhibition)
--   exhibitionid IS NOT NULL, resource_type NULL -> scoped to that one exhibition
--   resource_type/resource_ref set (exhibitionid always NULL in this case)
--                                                 -> scoped to that one Gallery/Display/Photo
--
-- Run with: psql $DATABASE_URL -f migrations/018_grant_exhibitionid.sql

BEGIN;

ALTER TABLE entity_role_grants
    ADD COLUMN exhibitionid UUID REFERENCES exhibitions (exhibitionid) ON DELETE CASCADE;

-- ── Backfill ──────────────────────────────────────────────────────────────────
--
-- Historical bug: Checker.Check()'s old "global" branch (resource_type IS
-- NULL) never filtered on the granting role's home exhibition, so every grant
-- with no resource scope -- the Viewer/Contributor/Admin grants
-- scripts/seed-exhibition.sh creates for Public/LoggedIn/the Admins team, and
-- every per-user singleton grant from the "view private photos" toggle --
-- has actually been leaking across every exhibition in the deployment
-- (e.g. being on one exhibition's Admins team silently made you an Admin of
-- every other exhibition too). None of that was ever intentional: it was
-- only ever meant to be scoped to the grant's own exhibition. Scope it now.
--
-- This must run BEFORE the resource_type = 'Exhibition' backfill below, since
-- those rows still have resource_type = 'Exhibition' (not NULL) at this point
-- and are correctly left untouched by this step.
UPDATE entity_role_grants erg
SET    exhibitionid = r.exhibitionid
FROM   roles r
WHERE  erg.roleid = r.roleid
  AND  erg.resource_type IS NULL
  AND  erg.exhibitionid  IS NULL;

-- Existing Exhibition-scoped grants move their target exhibition from
-- resource_ref into the new exhibitionid column. (resource_ref names the
-- exhibition directly and is preserved exactly -- it need not equal the
-- granting role's own home exhibition, though in practice it always has.)
UPDATE entity_role_grants
SET    exhibitionid  = resource_ref::uuid,
       resource_type = NULL,
       resource_ref  = NULL
WHERE  resource_type = 'Exhibition';

-- ── Constraints ───────────────────────────────────────────────────────────────

-- 'Exhibition' is no longer a valid resource_type -- exhibition-level scope is
-- now expressed via the exhibitionid column instead.
ALTER TABLE entity_role_grants DROP CONSTRAINT IF EXISTS entity_role_grants_resource_type_check;
ALTER TABLE entity_role_grants
    ADD CONSTRAINT entity_role_grants_resource_type_check
    CHECK (resource_type IN ('Gallery', 'Display', 'Photo'));

-- A grant is scoped by at most one of exhibitionid or resource_type/
-- resource_ref -- never both on the same row.
ALTER TABLE entity_role_grants
    ADD CONSTRAINT chk_exhibitionid_resource_exclusive
    CHECK (exhibitionid IS NULL OR resource_type IS NULL);

CREATE INDEX IF NOT EXISTS idx_entity_role_grants_exhibition
    ON entity_role_grants (exhibitionid) WHERE exhibitionid IS NOT NULL;

COMMIT;
