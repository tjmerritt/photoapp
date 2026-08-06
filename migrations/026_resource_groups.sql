-- migrations/026_resource_groups.sql
-- PLAN2.md Phase 3a: a group model for Photos, Displays, Galleries, and
-- Exhibitions. This migration covers STATIC membership only — a group's
-- members are whatever's explicitly added to resource_group_members below.
-- "3c. Dynamic groups: membership derived from labels" is deliberately a
-- separate, later migration (adding an is_dynamic flag and a label-rule
-- table), not bundled in here — keeping each phase's schema change small
-- and independently reviewable, the same granularity every migration in
-- this set has used so far.
--
-- Scope: a group needs a CONTAINER to be listed/searched within, and that
-- container differs by resource_type. A Photo/Gallery/Display group's
-- members all live inside one exhibition, so those groups are
-- exhibition-scoped (resource_groups.exhibitionid). An Exhibition group's
-- MEMBERS ARE EXHIBITIONS THEMSELVES — an exhibition can't be its own
-- container — so those groups are scoped to the organization the member
-- exhibitions belong to instead (resource_groups.organizationid), letting
-- an org admin curate sets of exhibitions (e.g. for grants that should
-- reach a curated subset of an organization's exhibitions once "3d. Grants
-- on groups" lands). This mirrors the exhibitionid/organizationid mutual
-- exclusivity already established for roles (migrations/021_org_admin.sql's
-- chk_roles_scope_exclusive) and entity_role_grants
-- (chk_grant_scope_exclusive) — same pattern, applied here to groups.
--
-- resource_group_members.resource_ref is TEXT (the member's UUID as text),
-- not a real FK, for the same reason entity_role_grants.resource_ref is
-- TEXT (migrations/012_permissions.sql): a single column can't reference
-- photos/galleries/displays/exhibitions depending on the group's
-- resource_type. Application code (internal/handlers/groups.go) is
-- responsible for validating a member actually exists and belongs to the
-- group's scope (exhibitionid or organizationid) before inserting it.
--
-- Run with: psql $DATABASE_URL -f migrations/026_resource_groups.sql

BEGIN;

CREATE TABLE IF NOT EXISTS resource_groups (
    groupid        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_type  TEXT        NOT NULL CHECK (resource_type IN ('Photo', 'Gallery', 'Display', 'Exhibition')),
    exhibitionid   UUID        REFERENCES exhibitions   (exhibitionid)   ON DELETE CASCADE,
    organizationid UUID        REFERENCES organizations (organizationid) ON DELETE CASCADE,
    name           TEXT        NOT NULL,
    description    TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ,
    -- Exhibition-type groups must be organization-scoped (their members are
    -- exhibitions, so an exhibitionid container makes no sense); every
    -- other resource_type must be exhibition-scoped. This single CHECK
    -- covers both "exactly one of exhibitionid/organizationid is set" (the
    -- same invariant roles.chk_roles_scope_exclusive enforces one tier up)
    -- AND which one, in one constraint — a separate num_nonnulls(...) = 1
    -- CHECK would just be a redundant weaker restatement of this one.
    CONSTRAINT chk_resource_groups_exhibition_type CHECK (
        (resource_type = 'Exhibition' AND organizationid IS NOT NULL AND exhibitionid IS NULL)
        OR (resource_type <> 'Exhibition' AND exhibitionid IS NOT NULL AND organizationid IS NULL)
    )
);

-- Partial unique indexes rather than a single UNIQUE constraint: a group
-- name only needs to be unique within its own container, and exactly one of
-- exhibitionid/organizationid is ever set on a given row (enforced above),
-- so two separate partial indexes cover both cases without a NULL-handling
-- workaround (Postgres treats every NULL as distinct in a plain UNIQUE
-- constraint, so a naive UNIQUE (exhibitionid, organizationid, resource_type, name)
-- would silently fail to enforce uniqueness on whichever column is NULL —
-- same reasoning as uq_role_name_org in migrations/021_org_admin.sql).
CREATE UNIQUE INDEX IF NOT EXISTS uq_resource_groups_name_exhibition
    ON resource_groups (exhibitionid, resource_type, name)
    WHERE deleted_at IS NULL AND exhibitionid IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_resource_groups_name_organization
    ON resource_groups (organizationid, resource_type, name)
    WHERE deleted_at IS NULL AND organizationid IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_resource_groups_exhibition
    ON resource_groups (exhibitionid, resource_type) WHERE deleted_at IS NULL AND exhibitionid IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_resource_groups_organization
    ON resource_groups (organizationid, resource_type) WHERE deleted_at IS NULL AND organizationid IS NOT NULL;

DROP TRIGGER IF EXISTS set_updated_at_resource_groups ON resource_groups;
CREATE TRIGGER set_updated_at_resource_groups
    BEFORE UPDATE ON resource_groups
    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

CREATE TABLE IF NOT EXISTS resource_group_members (
    groupid         UUID        NOT NULL REFERENCES resource_groups (groupid) ON DELETE CASCADE,
    resource_ref    TEXT        NOT NULL,
    added_by_userid UUID        REFERENCES users (userid) ON DELETE SET NULL,
    added_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (groupid, resource_ref)
);

CREATE INDEX IF NOT EXISTS idx_resource_group_members_groupid ON resource_group_members (groupid);

COMMIT;
