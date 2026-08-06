-- migrations/025_resource_labels.sql
-- PLAN2.md Phase 3b: labels on Displays, Galleries, and Exhibitions — Photos
-- already have their own `labels` table (migrations/001_initial.sql), which
-- this migration deliberately leaves untouched. Rather than generalizing
-- `labels` into a polymorphic table covering all four resource types (a
-- much larger, riskier change touching every piece of already-working code
-- that reads/writes photo labels — fetch.go, search.go, admin stats, the
-- import tooling, and all of their tests), this adds a SEPARATE table,
-- `resource_labels`, scoped to Gallery/Display/Exhibition only, following
-- the same (resource_type, resource_ref) polymorphic pattern
-- entity_role_grants already established (migrations/012_permissions.sql) —
-- resource_ref is TEXT holding the resource's UUID, not a real FK, since a
-- single column can't reference three different tables.
--
-- label_names (migrations/016_label_names.sql, made per-exhibition by
-- migrations/024_label_names_per_exhibition.sql) is reused as-is for
-- color/restricted/enabled — it's already keyed by (exhibitionid, name),
-- independent of what kind of resource carries the name, so a label named
-- "Featured" can have one consistent color/restricted setting whether it's
-- attached to a photo, a gallery, a display, or the exhibition itself. No
-- change needed there.
--
-- Run with: psql $DATABASE_URL -f migrations/025_resource_labels.sql

BEGIN;

CREATE TABLE IF NOT EXISTS resource_labels (
    labelid         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 'Photo' is deliberately not included — that's what the original
    -- `labels` table is for; see this file's header comment.
    resource_type   TEXT        NOT NULL CHECK (resource_type IN ('Gallery', 'Display', 'Exhibition')),
    resource_ref    TEXT        NOT NULL,
    added_by_userid UUID        NOT NULL REFERENCES users (userid) ON DELETE RESTRICT,
    name            TEXT        NOT NULL,
    value           TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_resource_labels_resource
    ON resource_labels (resource_type, resource_ref, created_at) WHERE deleted_at IS NULL;

DROP TRIGGER IF EXISTS set_updated_at_resource_labels ON resource_labels;
CREATE TRIGGER set_updated_at_resource_labels
    BEFORE UPDATE ON resource_labels
    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

COMMIT;
