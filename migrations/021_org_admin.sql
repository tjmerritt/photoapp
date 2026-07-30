-- migrations/021_org_admin.sql
-- PLAN2.md Phase 1c: org admin — permissions scoped to the Organization but
-- spanning all its exhibitions.
--
-- Extends the existing role/grant machinery (migrations/012_permissions.sql,
-- 018_grant_exhibitionid.sql) with a third, broader scope tier:
--
--   entity_role_grants.organizationid IS NOT NULL
--     -> the grant applies to every exhibition under that organization,
--        present and future, exactly the way an exhibitionid grant applies
--        to every gallery/display/photo within that one exhibition.
--
-- A grant row is still scoped by AT MOST ONE of exhibitionid,
-- resource_type/resource_ref, or organizationid — num_nonnulls() gives a
-- clean 3-way version of the exclusivity check the old 2-way
-- chk_exhibitionid_resource_exclusive constraint enforced.
--
-- Roles themselves also need to be able to belong to an organization instead
-- of an exhibition, so an org admin's grant has a role to point at (the same
-- role/role_permissions bundling used everywhere else, just one scope level
-- up) — so roles.exhibitionid becomes nullable and roles.organizationid is
-- added, with a CHECK that a role belongs to exactly one scope. No existing
-- row is affected: every role created so far has exhibitionid set and
-- organizationid NULL, satisfying the new constraint without a backfill.
--
-- Run with: psql $DATABASE_URL -f migrations/021_org_admin.sql

BEGIN;

-- ── entity_role_grants: organization scope ──────────────────────────────────

ALTER TABLE entity_role_grants
    ADD COLUMN organizationid UUID REFERENCES organizations (organizationid) ON DELETE CASCADE;

ALTER TABLE entity_role_grants DROP CONSTRAINT IF EXISTS chk_exhibitionid_resource_exclusive;
ALTER TABLE entity_role_grants
    ADD CONSTRAINT chk_grant_scope_exclusive
    CHECK (num_nonnulls(exhibitionid, resource_type, organizationid) <= 1);

CREATE INDEX IF NOT EXISTS idx_entity_role_grants_organization
    ON entity_role_grants (organizationid) WHERE organizationid IS NOT NULL;

-- ── roles: organization scope ────────────────────────────────────────────────

ALTER TABLE roles ALTER COLUMN exhibitionid DROP NOT NULL;
ALTER TABLE roles
    ADD COLUMN organizationid UUID REFERENCES organizations (organizationid) ON DELETE CASCADE;
ALTER TABLE roles
    ADD CONSTRAINT chk_roles_scope_exclusive
    CHECK (num_nonnulls(exhibitionid, organizationid) = 1);

-- uq_role_name (exhibitionid, name) still enforces per-exhibition uniqueness
-- fine as-is (exhibitionid is never NULL for an exhibition-scoped role). It
-- does NOT enforce anything for org-scoped roles (exhibitionid is NULL on
-- all of them, and Postgres treats every NULL as distinct in a UNIQUE
-- constraint), so give organization-scoped roles their own partial index.
CREATE UNIQUE INDEX IF NOT EXISTS uq_role_name_org
    ON roles (organizationid, name) WHERE organizationid IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_roles_organization
    ON roles (organizationid) WHERE deleted_at IS NULL AND organizationid IS NOT NULL;

COMMIT;
