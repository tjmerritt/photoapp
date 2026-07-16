-- migrations/014_galleries_displays.sql
-- Phase 2: Gallery & Display resources.
-- Run with: psql $DATABASE_URL -f migrations/014_galleries_displays.sql

BEGIN;

-- =============================================================================
-- DISPLAY TEMPLATES (global — not exhibition-scoped)
-- A template defines the layout geometry and rendering rules for a display.
-- photo_count is the number of photo slots the template expects.
-- slot_positions is a JSONB array describing where each slot sits in the layout.
-- presentation is a JSONB object describing rendering / styling rules.
-- =============================================================================
CREATE TABLE IF NOT EXISTS display_templates (
    templateid     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT        NOT NULL,
    photo_count    INTEGER     NOT NULL DEFAULT 1 CHECK (photo_count > 0),
    slot_positions JSONB       NOT NULL DEFAULT '[]',
    presentation   JSONB       NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ,
    CONSTRAINT uq_template_name UNIQUE (name)
);

-- =============================================================================
-- GALLERIES
-- A gallery is an ordered collection of displays within an exhibition.
-- =============================================================================
CREATE TABLE IF NOT EXISTS galleries (
    galleryid    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    exhibitionid UUID        NOT NULL REFERENCES exhibitions (exhibitionid) ON DELETE CASCADE,
    title        TEXT        NOT NULL,
    sort_order   INTEGER     NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_galleries_exhibition
    ON galleries (exhibitionid, sort_order) WHERE deleted_at IS NULL;

-- =============================================================================
-- PLACARD DEFAULTS
-- Per-gallery defaults for placard rendering. One row per gallery (1:1).
-- The defaults JSONB object is merged with per-slot placard overrides at
-- render time. The frontend defines the exact schema.
-- =============================================================================
CREATE TABLE IF NOT EXISTS placard_defaults (
    galleryid  UUID        PRIMARY KEY REFERENCES galleries (galleryid) ON DELETE CASCADE,
    defaults   JSONB       NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- DISPLAYS
-- A display is one panel within a gallery. It references an optional template
-- that describes the layout geometry. A display with no template is freeform.
-- =============================================================================
CREATE TABLE IF NOT EXISTS displays (
    displayid  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    galleryid  UUID        NOT NULL REFERENCES galleries (galleryid) ON DELETE CASCADE,
    templateid UUID        REFERENCES display_templates (templateid) ON DELETE SET NULL,
    sort_order INTEGER     NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_displays_gallery
    ON displays (galleryid, sort_order) WHERE deleted_at IS NULL;

-- =============================================================================
-- DISPLAY SLOTS
-- Each slot is one position in a display. slot_index corresponds to a position
-- in the template's slot_positions array (0-based). photoid may be NULL while
-- the slot is unfilled. rich_text and placard are optional per-slot content.
-- =============================================================================
CREATE TABLE IF NOT EXISTS display_slots (
    slotid     UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    displayid  UUID    NOT NULL REFERENCES displays (displayid) ON DELETE CASCADE,
    slot_index INTEGER NOT NULL,
    photoid    UUID    REFERENCES photos (photoid) ON DELETE SET NULL,
    rich_text  TEXT,
    placard    JSONB,
    CONSTRAINT uq_display_slot UNIQUE (displayid, slot_index)
);

CREATE INDEX IF NOT EXISTS idx_display_slots_display
    ON display_slots (displayid, slot_index);

-- =============================================================================
-- updated_at triggers
-- =============================================================================
DROP TRIGGER IF EXISTS set_updated_at_display_templates ON display_templates;
DROP TRIGGER IF EXISTS set_updated_at_galleries         ON galleries;
DROP TRIGGER IF EXISTS set_updated_at_placard_defaults  ON placard_defaults;
DROP TRIGGER IF EXISTS set_updated_at_displays          ON displays;

CREATE TRIGGER set_updated_at_display_templates
    BEFORE UPDATE ON display_templates
    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

CREATE TRIGGER set_updated_at_galleries
    BEFORE UPDATE ON galleries
    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

CREATE TRIGGER set_updated_at_placard_defaults
    BEFORE UPDATE ON placard_defaults
    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

CREATE TRIGGER set_updated_at_displays
    BEFORE UPDATE ON displays
    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

COMMIT;
