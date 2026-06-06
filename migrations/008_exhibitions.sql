-- migrations/008_exhibitions.sql
-- Adds exhibitions — a way to group photos by the hostname that serves them.

BEGIN;

-- ── Core exhibition table ─────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS exhibitions (
    exhibitionid  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);

-- ── Hostname → exhibition mapping (many hostnames per exhibition) ─────────────
CREATE TABLE IF NOT EXISTS exhibition_hostnames (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    exhibitionid  UUID        NOT NULL REFERENCES exhibitions (exhibitionid) ON DELETE CASCADE,
    hostname      TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_exhibition_hostname UNIQUE (hostname)
);

CREATE INDEX IF NOT EXISTS idx_exhibition_hostnames ON exhibition_hostnames (hostname);

-- ── User ↔ exhibition membership ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS user_exhibitions (
    userid        UUID        NOT NULL REFERENCES users       (userid)       ON DELETE CASCADE,
    exhibitionid  UUID        NOT NULL REFERENCES exhibitions (exhibitionid) ON DELETE CASCADE,
    joined_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (userid, exhibitionid)
);

-- ── Photos get an exhibition ──────────────────────────────────────────────────
ALTER TABLE photos ADD COLUMN IF NOT EXISTS exhibitionid UUID
    REFERENCES exhibitions (exhibitionid) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_photos_exhibition
    ON photos (exhibitionid) WHERE deleted_at IS NULL;

-- ── Seed: default exhibition for local development ────────────────────────────
INSERT INTO exhibitions (exhibitionid, name) VALUES
    ('ffffffff-0000-0000-0000-000000000001', 'Default Exhibition')
ON CONFLICT (exhibitionid) DO NOTHING;

INSERT INTO exhibition_hostnames (exhibitionid, hostname) VALUES
    ('ffffffff-0000-0000-0000-000000000001', 'localhost'),
    ('ffffffff-0000-0000-0000-000000000001', 'localhost:8080')
ON CONFLICT (hostname) DO NOTHING;

-- Migrate existing photos to the default exhibition.
UPDATE photos
SET    exhibitionid = 'ffffffff-0000-0000-0000-000000000001'
WHERE  exhibitionid IS NULL;

COMMIT;
