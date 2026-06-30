-- migrations/012_permissions.sql
-- Adds the permissions system: teams, roles, and entity-scoped role grants.
-- Run with: psql $DATABASE_URL -f migrations/012_permissions.sql
--
-- To seed an exhibition with default roles and an initial admin user, run:
--   scripts/seed-exhibition.sh <exhibition-id> <user-id>

BEGIN;

-- ── Teams ─────────────────────────────────────────────────────────────────────
-- A team is a named group of users, scoped to an exhibition.

CREATE TABLE IF NOT EXISTS teams (
    teamid       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    exhibitionid UUID        NOT NULL REFERENCES exhibitions (exhibitionid) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    description  TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ,
    CONSTRAINT uq_team_name UNIQUE (exhibitionid, name)
);

CREATE INDEX IF NOT EXISTS idx_teams_exhibition
    ON teams (exhibitionid) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS team_members (
    teamid    UUID        NOT NULL REFERENCES teams (teamid) ON DELETE CASCADE,
    userid    UUID        NOT NULL REFERENCES users (userid) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (teamid, userid)
);

CREATE INDEX IF NOT EXISTS idx_team_members_userid ON team_members (userid);

-- ── Roles ─────────────────────────────────────────────────────────────────────
-- A role is a named collection of permissions, scoped to an exhibition.

CREATE TABLE IF NOT EXISTS roles (
    roleid       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    exhibitionid UUID        NOT NULL REFERENCES exhibitions (exhibitionid) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    description  TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ,
    CONSTRAINT uq_role_name UNIQUE (exhibitionid, name)
);

CREATE INDEX IF NOT EXISTS idx_roles_exhibition
    ON roles (exhibitionid) WHERE deleted_at IS NULL;

-- Permissions bundled in a role. Each row is one permission string.
CREATE TABLE IF NOT EXISTS role_permissions (
    roleid     UUID NOT NULL REFERENCES roles (roleid) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    PRIMARY KEY (roleid, permission)
);

-- ── Entity role grants ────────────────────────────────────────────────────────
-- Grants a role to an entity (Public, LoggedIn, Team, User), optionally
-- restricted to a specific resource. NULL resource_type means the grant applies
-- globally within the exhibition.
--
-- entity_type: 'Public' | 'LoggedIn' | 'Team' | 'User'
-- entity_ref:  teamid or userid (as text); NULL for Public and LoggedIn
-- resource_type: 'Exhibition' | 'Gallery' | 'Display' | 'Photo' | NULL (global)
-- resource_ref: the resource UUID as text; NULL when resource_type is NULL

CREATE TABLE IF NOT EXISTS entity_role_grants (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    roleid        UUID        NOT NULL REFERENCES roles (roleid) ON DELETE CASCADE,
    entity_type   TEXT        NOT NULL CHECK (entity_type IN ('Public', 'LoggedIn', 'Team', 'User')),
    entity_ref    TEXT,           -- NULL for Public / LoggedIn
    resource_type TEXT        CHECK (resource_type IN ('Exhibition', 'Gallery', 'Display', 'Photo')),
    resource_ref  TEXT,           -- NULL when resource_type is NULL (global grant)
    granted_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    granted_by    UUID        REFERENCES users (userid) ON DELETE SET NULL,
    CONSTRAINT chk_entity_ref CHECK (
        (entity_type IN ('Public', 'LoggedIn') AND entity_ref IS NULL)
        OR (entity_type IN ('Team', 'User') AND entity_ref IS NOT NULL)
    ),
    CONSTRAINT chk_resource_ref CHECK (
        (resource_type IS NULL AND resource_ref IS NULL)
        OR (resource_type IS NOT NULL AND resource_ref IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_entity_role_grants_lookup
    ON entity_role_grants (entity_type, entity_ref, resource_type, resource_ref);

CREATE INDEX IF NOT EXISTS idx_entity_role_grants_role
    ON entity_role_grants (roleid);

COMMIT;
