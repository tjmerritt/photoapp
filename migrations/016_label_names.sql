-- migrations/016_label_names.sql
-- Phase 5a (label colors) & 5b (restricted labels): per-label-name
-- attributes, one row per distinct label *name* (not per label value/row in
-- the `labels` table). PLAN.md's Phase 5 draft separately proposed a
-- `label_name_colors` table (5a) and adding columns to a `label_names`
-- table (5b) — those describe the same underlying concept (attributes of a
-- label name), so this migration unifies them into a single table rather
-- than maintaining two overlapping ones. This also matches the table name
-- already anticipated by the `internal/handlers/labels.go` TODO left from
-- the Phase 1 session ("TODO(phase-5b): if label_names.restricted = TRUE
-- for req.Name, also require PermLabelAdmin").
--
-- Run with: psql $DATABASE_URL -f migrations/016_label_names.sql

BEGIN;

CREATE TABLE IF NOT EXISTS label_names (
    name       TEXT        PRIMARY KEY,
    -- NULL = no override; clients fall back to a deterministic hash-based
    -- color derived from the name itself (see labelColorFor() in app.js).
    color_hex  TEXT,
    -- TRUE = only a caller holding Admin or LabelAdmin may add, modify, or
    -- delete labels with this name (existing labels remain viewable by
    -- everyone who could already view labels).
    restricted BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_label_names_color_hex CHECK (color_hex IS NULL OR color_hex ~* '^#[0-9a-f]{6}$')
);

DROP TRIGGER IF EXISTS set_updated_at_label_names ON label_names;
CREATE TRIGGER set_updated_at_label_names
    BEFORE UPDATE ON label_names
    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

COMMIT;
