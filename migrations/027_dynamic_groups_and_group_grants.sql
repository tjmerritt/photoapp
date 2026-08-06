-- migrations/027_dynamic_groups_and_group_grants.sql
-- PLAN2.md Phase 3c ("Dynamic groups: membership derived from labels") and
-- 3d ("Grants on groups: resource groups usable as the resource in
-- permission grants"), both built on migrations/025_resource_labels.sql and
-- 026_resource_groups.sql from the same phase.
--
-- ── 3c: dynamic groups ───────────────────────────────────────────────────────
-- A group can now be "dynamic": instead of explicit resource_group_members
-- rows, its membership is computed live from a single label-name (and,
-- optionally, label-value) rule. `resource_group_label_rules` holds exactly
-- one rule per dynamic group (PRIMARY KEY (groupid) — this migration doesn't
-- support multiple AND/OR rules per group; that's a bigger feature left for
-- later if it's ever needed). `resource_group_effective_members` is a VIEW
-- with the same (groupid, resource_ref, added_at) shape
-- resource_group_members already has, so every existing consumer
-- (internal/handlers/groups.go's ListMembers, and the new Checker.Check
-- branches added below for 3d) can query "the group's real members" through
-- one place regardless of whether the group is static or dynamic:
--   - static groups: passes through resource_group_members unchanged
--   - dynamic Photo groups: photos (within the group's own exhibitionid)
--     carrying a `labels` row matching the rule
--   - dynamic Gallery/Display groups: the equivalent using `resource_labels`
--     (migrations/025_resource_labels.sql), scoped to the group's exhibitionid
--   - dynamic Exhibition groups: `resource_labels` rows of resource_type
--     'Exhibition', scoped to the group's organizationid
-- A NULL label_value on the rule means "any value" (match by name alone);
-- a non-NULL value requires an exact match, mirroring how a photo label or
-- resource label is itself just a (name, value) pair.
--
-- ── 3d: grants on groups ─────────────────────────────────────────────────────
-- 'Group' becomes a fourth valid entity_role_grants.resource_type (alongside
-- Gallery/Display/Photo — see migrations/018_grant_exhibitionid.sql's own
-- CHECK). A grant with resource_type = 'Group', resource_ref = <groupid>
-- reaches every one of that group's members (static or dynamic, via the
-- view above) as if each had been granted individually. internal/permissions
-- /permissions.go's Checker.Check gets two new OR branches for this in the
-- same migration-adjacent commit (Go changes, not SQL) — not repeated here.
--
-- Run with: psql $DATABASE_URL -f migrations/027_dynamic_groups_and_group_grants.sql

BEGIN;

-- ── 3c: is_dynamic + label rules ─────────────────────────────────────────────

ALTER TABLE resource_groups
    ADD COLUMN IF NOT EXISTS is_dynamic BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS resource_group_label_rules (
    groupid     UUID NOT NULL PRIMARY KEY REFERENCES resource_groups (groupid) ON DELETE CASCADE,
    label_name  TEXT NOT NULL,
    label_value TEXT
);

CREATE OR REPLACE VIEW resource_group_effective_members AS
    -- Static membership: unchanged, passed straight through.
    SELECT rgm.groupid, rgm.resource_ref, rgm.added_at
    FROM   resource_group_members rgm
    JOIN   resource_groups g ON g.groupid = rgm.groupid AND g.deleted_at IS NULL
    WHERE  g.is_dynamic = FALSE

    UNION

    -- Dynamic Photo groups: photos in the group's own exhibition carrying a
    -- matching (non-deleted) label.
    SELECT g.groupid, l.photoid::text, NULL::timestamptz
    FROM   resource_groups g
    JOIN   resource_group_label_rules rule ON rule.groupid = g.groupid
    JOIN   labels  l ON l.name = rule.label_name
                     AND (rule.label_value IS NULL OR l.value = rule.label_value)
                     AND l.deleted_at IS NULL
    JOIN   photos  p ON p.photoid = l.photoid AND p.deleted_at IS NULL AND p.exhibitionid = g.exhibitionid
    WHERE  g.is_dynamic = TRUE AND g.resource_type = 'Photo' AND g.deleted_at IS NULL

    UNION

    -- Dynamic Gallery groups: galleries in the group's own exhibition
    -- carrying a matching resource_label.
    SELECT g.groupid, gal.galleryid::text, NULL::timestamptz
    FROM   resource_groups g
    JOIN   resource_group_label_rules rule ON rule.groupid = g.groupid
    JOIN   resource_labels rl  ON rl.resource_type = 'Gallery'
                               AND rl.name = rule.label_name
                               AND (rule.label_value IS NULL OR rl.value = rule.label_value)
                               AND rl.deleted_at IS NULL
    JOIN   galleries gal ON gal.galleryid::text = rl.resource_ref
                         AND gal.deleted_at IS NULL AND gal.exhibitionid = g.exhibitionid
    WHERE  g.is_dynamic = TRUE AND g.resource_type = 'Gallery' AND g.deleted_at IS NULL

    UNION

    -- Dynamic Display groups: same shape, one join deeper to reach the
    -- owning gallery's exhibitionid.
    SELECT g.groupid, d.displayid::text, NULL::timestamptz
    FROM   resource_groups g
    JOIN   resource_group_label_rules rule ON rule.groupid = g.groupid
    JOIN   resource_labels rl  ON rl.resource_type = 'Display'
                               AND rl.name = rule.label_name
                               AND (rule.label_value IS NULL OR rl.value = rule.label_value)
                               AND rl.deleted_at IS NULL
    JOIN   displays  d  ON d.displayid::text = rl.resource_ref AND d.deleted_at IS NULL
    JOIN   galleries gg ON gg.galleryid = d.galleryid AND gg.deleted_at IS NULL AND gg.exhibitionid = g.exhibitionid
    WHERE  g.is_dynamic = TRUE AND g.resource_type = 'Display' AND g.deleted_at IS NULL

    UNION

    -- Dynamic Exhibition groups: exhibitions in the group's own organization
    -- carrying a matching resource_label of resource_type 'Exhibition'
    -- (resource_ref is the exhibition's own id — see resolveResourceLabelExhibition
    -- in internal/handlers/resource_labels.go).
    SELECT g.groupid, ex.exhibitionid::text, NULL::timestamptz
    FROM   resource_groups g
    JOIN   resource_group_label_rules rule ON rule.groupid = g.groupid
    JOIN   resource_labels rl ON rl.resource_type = 'Exhibition'
                              AND rl.name = rule.label_name
                              AND (rule.label_value IS NULL OR rl.value = rule.label_value)
                              AND rl.deleted_at IS NULL
    JOIN   exhibitions ex ON ex.exhibitionid::text = rl.resource_ref
                          AND ex.deleted_at IS NULL AND ex.organizationid = g.organizationid
    WHERE  g.is_dynamic = TRUE AND g.resource_type = 'Exhibition' AND g.deleted_at IS NULL;

-- ── 3d: 'Group' as an entity_role_grants resource_type ──────────────────────

ALTER TABLE entity_role_grants DROP CONSTRAINT IF EXISTS entity_role_grants_resource_type_check;
ALTER TABLE entity_role_grants
    ADD CONSTRAINT entity_role_grants_resource_type_check
    CHECK (resource_type IN ('Gallery', 'Display', 'Photo', 'Group'));

COMMIT;
