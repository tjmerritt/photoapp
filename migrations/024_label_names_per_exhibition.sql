-- migrations/024_label_names_per_exhibition.sql
-- label_names was a single site-wide catalog keyed by name alone
-- (migrations/016_label_names.sql) — the same label name's color override,
-- restricted flag, and enabled flag were shared across every exhibition on
-- the install, even though the permission checks gating who can change them
-- (PermLabelNameCreate/Modify, checked via internal/handlers/labels.go)
-- were already exhibition-scoped. This migration makes the data match the
-- permission model: label_names gains an exhibitionid column and its
-- primary key becomes (exhibitionid, name), so each exhibition has its own
-- independent catalog — the same name in two different exhibitions can now
-- have different color/restricted/enabled settings.
--
-- Backfill: one row per (exhibitionid, name) pair actually in use today,
-- derived from non-deleted labels joined to their (non-deleted) photo's
-- exhibitionid, carrying over that name's existing color_hex/restricted/
-- enabled from the old global row. A label_names row that was pre-configured
-- but has never actually been used on any photo (in any exhibition) has no
-- exhibition to attach it to and is intentionally dropped rather than
-- guessed at — per the user's own call: an unused row carries no live
-- behavior today, so nothing is lost by not carrying it forward. Likewise a
-- label whose only usage is on a photo with no resolved exhibitionid (e.g.
-- an upload from before an exhibition context existed) is dropped for the
-- same reason — there's no valid exhibition to scope it to.
--
-- This migration set has no schema_migrations tracking table (see
-- SUMMARIES2.md Session 20) — every migration file re-runs in full on every
-- `make migrate-up`. The rename/create/backfill/drop sequence below only
-- needs to happen once, so it's guarded by checking whether label_names
-- already has an exhibitionid column; everything inside that guard runs via
-- EXECUTE'd dynamic SQL rather than plain statements, because a plain
-- statement referencing label_names_legacy (which won't exist on the second
-- run — dropped by this same file's own first run) fails at PARSE time even
-- inside the guard's IF branch, the same issue
-- migrations/022_drop_is_public.sql ran into and documented for exactly
-- this reason.

BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE  table_name = 'label_names' AND column_name = 'exhibitionid'
    ) THEN
        EXECUTE $sql$
            ALTER TABLE label_names RENAME TO label_names_legacy
        $sql$;

        EXECUTE $sql$
            CREATE TABLE label_names (
                exhibitionid UUID        NOT NULL REFERENCES exhibitions (exhibitionid) ON DELETE CASCADE,
                name         TEXT        NOT NULL,
                -- NULL = no override; clients fall back to a deterministic
                -- hash-based color derived from the name itself (see
                -- labelColorFor() in app.js).
                color_hex    TEXT,
                -- TRUE = only a caller holding Admin or LabelAdmin (in this
                -- exhibition) may add, modify, or delete labels with this
                -- name in this exhibition.
                restricted   BOOLEAN     NOT NULL DEFAULT FALSE,
                -- FALSE = hidden from the add/autocomplete UI and blocked
                -- from new use in this exhibition, mirroring
                -- emoji_types.is_active.
                enabled      BOOLEAN     NOT NULL DEFAULT TRUE,
                created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                PRIMARY KEY (exhibitionid, name),
                CONSTRAINT chk_label_names_color_hex CHECK (color_hex IS NULL OR color_hex ~* '^#[0-9a-f]{6}$')
            )
        $sql$;

        -- One row per (exhibitionid, name) pair actually in use, carrying
        -- over the old global row's settings. DISTINCT ON collapses however
        -- many labels rows share a given (exhibitionid, name) pair down to
        -- one — old.color_hex/restricted/enabled is identical for every one
        -- of them (they all join the same single old-table row by name), so
        -- it doesn't matter which underlying labels row is picked.
        EXECUTE $sql$
            INSERT INTO label_names (exhibitionid, name, color_hex, restricted, enabled, created_at, updated_at)
            SELECT DISTINCT ON (p.exhibitionid, l.name)
                   p.exhibitionid, l.name, old.color_hex, old.restricted, old.enabled, old.created_at, old.updated_at
            FROM   labels l
            JOIN   photos p           ON p.photoid = l.photoid
            JOIN   label_names_legacy old ON old.name = l.name
            WHERE  l.deleted_at IS NULL
              AND  p.deleted_at IS NULL
              AND  p.exhibitionid IS NOT NULL
        $sql$;

        EXECUTE $sql$
            DROP TABLE label_names_legacy
        $sql$;

        EXECUTE $sql$
            CREATE TRIGGER set_updated_at_label_names
                BEFORE UPDATE ON label_names
                FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at()
        $sql$;
    END IF;
END $$;

COMMIT;
