-- migrations/001_initial.sql
-- Run with: psql $DATABASE_URL -f migrations/001_initial.sql

BEGIN;

-- =============================================================================
-- USERS
-- =============================================================================
CREATE TABLE IF NOT EXISTS users (
    userid          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    username        TEXT        NOT NULL UNIQUE,
    password_hash   TEXT        NOT NULL DEFAULT '',
    email           TEXT        NOT NULL UNIQUE DEFAULT '',
    fullname        TEXT,
    profile_image   TEXT,
    profile_link    TEXT,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users (username)  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_users_email    ON users (email)     WHERE deleted_at IS NULL;

-- =============================================================================
-- PHOTOS
-- =============================================================================
CREATE TABLE IF NOT EXISTS photos (
    photoid         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_userid    UUID        NOT NULL REFERENCES users (userid) ON DELETE RESTRICT,
    image_url       TEXT        NOT NULL,
    image_width     INTEGER     NOT NULL CHECK (image_width > 0),
    image_height    INTEGER     NOT NULL CHECK (image_height > 0),
    title_text      TEXT,
    title_userid    UUID        REFERENCES users (userid) ON DELETE SET NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_photos_owner   ON photos (owner_userid) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_photos_created ON photos (created_at DESC) WHERE deleted_at IS NULL;

-- =============================================================================
-- RELATED PHOTOS
-- =============================================================================
CREATE TABLE IF NOT EXISTS related_photos (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    photoid          UUID        NOT NULL REFERENCES photos (photoid) ON DELETE CASCADE,
    related_photoid  UUID        NOT NULL REFERENCES photos (photoid) ON DELETE CASCADE,
    scaled_image_url TEXT,
    click_url        TEXT,
    sort_order       INTEGER     NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_related_photos UNIQUE (photoid, related_photoid),
    CONSTRAINT no_self_relation  CHECK  (photoid <> related_photoid)
);

CREATE INDEX IF NOT EXISTS idx_related_photoid ON related_photos (photoid, sort_order);

-- =============================================================================
-- LABELS
-- =============================================================================
CREATE TABLE IF NOT EXISTS labels (
    labelid         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    photoid         UUID        NOT NULL REFERENCES photos (photoid) ON DELETE CASCADE,
    added_by_userid UUID        NOT NULL REFERENCES users (userid) ON DELETE RESTRICT,
    name            TEXT        NOT NULL,
    value           TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_labels_photoid ON labels (photoid, created_at) WHERE deleted_at IS NULL;

-- =============================================================================
-- EMOJI TYPES
-- =============================================================================
CREATE TABLE IF NOT EXISTS emoji_types (
    emojiid         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    emoji_char      TEXT,
    image_url       TEXT,
    alt_text        TEXT        NOT NULL,
    sort_order      INTEGER     NOT NULL DEFAULT 0,
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT emoji_has_representation CHECK (
        (emoji_char IS NOT NULL) OR (image_url IS NOT NULL)
    )
);

-- =============================================================================
-- EMOJI REACTIONS
-- =============================================================================
CREATE TABLE IF NOT EXISTS emoji_reactions (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    photoid     UUID        NOT NULL REFERENCES photos      (photoid) ON DELETE CASCADE,
    emojiid     UUID        NOT NULL REFERENCES emoji_types (emojiid) ON DELETE CASCADE,
    userid      UUID        NOT NULL REFERENCES users       (userid)  ON DELETE CASCADE,
    reacted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_emoji_reaction UNIQUE (photoid, emojiid, userid)
);

CREATE INDEX IF NOT EXISTS idx_emoji_reactions_photo ON emoji_reactions (photoid, emojiid);
CREATE INDEX IF NOT EXISTS idx_emoji_reactions_user  ON emoji_reactions (photoid, userid);

-- Materialised view (requires CONCURRENTLY index for non-locking refresh)
CREATE MATERIALIZED VIEW IF NOT EXISTS emoji_counts AS
    SELECT photoid, emojiid, COUNT(*) AS reaction_count
    FROM   emoji_reactions
    GROUP  BY photoid, emojiid
WITH DATA;

CREATE UNIQUE INDEX IF NOT EXISTS idx_emoji_counts_pk ON emoji_counts (photoid, emojiid);

-- =============================================================================
-- COMMENTS
-- =============================================================================
CREATE TABLE IF NOT EXISTS comments (
    commentid           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    photoid             UUID        NOT NULL REFERENCES photos   (photoid)   ON DELETE CASCADE,
    parent_commentid    UUID        REFERENCES comments (commentid) ON DELETE SET NULL,
    author_userid       UUID        NOT NULL REFERENCES users    (userid)    ON DELETE RESTRICT,
    comment_text        TEXT        NOT NULL CHECK (char_length(comment_text) > 0),
    reply_count         INTEGER     NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_comments_photo_top ON comments (photoid, created_at)
    WHERE parent_commentid IS NULL AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_comments_replies ON comments (parent_commentid, created_at)
    WHERE parent_commentid IS NOT NULL AND deleted_at IS NULL;

-- reply_count trigger
CREATE OR REPLACE FUNCTION trg_update_reply_count()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' AND NEW.parent_commentid IS NOT NULL THEN
        UPDATE comments SET reply_count = reply_count + 1, updated_at = NOW()
        WHERE  commentid = NEW.parent_commentid;
    ELSIF TG_OP = 'UPDATE'
        AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
        AND NEW.parent_commentid IS NOT NULL
    THEN
        UPDATE comments SET reply_count = GREATEST(reply_count - 1, 0), updated_at = NOW()
        WHERE  commentid = NEW.parent_commentid;
    ELSIF TG_OP = 'DELETE' AND OLD.parent_commentid IS NOT NULL THEN
        UPDATE comments SET reply_count = GREATEST(reply_count - 1, 0), updated_at = NOW()
        WHERE  commentid = OLD.parent_commentid;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_comments_reply_count ON comments;
CREATE TRIGGER trg_comments_reply_count
AFTER INSERT OR UPDATE OR DELETE ON comments
FOR EACH ROW EXECUTE FUNCTION trg_update_reply_count();

-- =============================================================================
-- SESSIONS
-- =============================================================================
CREATE TABLE IF NOT EXISTS sessions (
    session_id   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    userid       UUID        NOT NULL REFERENCES users (userid) ON DELETE CASCADE,
    token_hash   TEXT        NOT NULL UNIQUE,
    user_agent   TEXT,
    ip_address   INET,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '30 days',
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sessions_userid     ON sessions (userid)     WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions (token_hash);

-- =============================================================================
-- updated_at trigger
-- =============================================================================
CREATE OR REPLACE FUNCTION trg_set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN NEW.updated_at = NOW(); RETURN NEW; END;
$$;

DROP TRIGGER IF EXISTS set_updated_at_users    ON users;
DROP TRIGGER IF EXISTS set_updated_at_photos   ON photos;
DROP TRIGGER IF EXISTS set_updated_at_labels   ON labels;
DROP TRIGGER IF EXISTS set_updated_at_comments ON comments;

CREATE TRIGGER set_updated_at_users    BEFORE UPDATE ON users    FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();
CREATE TRIGGER set_updated_at_photos   BEFORE UPDATE ON photos   FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();
CREATE TRIGGER set_updated_at_labels   BEFORE UPDATE ON labels   FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();
CREATE TRIGGER set_updated_at_comments BEFORE UPDATE ON comments FOR EACH ROW EXECUTE FUNCTION trg_set_updated_at();

COMMIT;
