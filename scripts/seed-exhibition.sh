#!/usr/bin/env bash
# seed-exhibition.sh — Bootstrap permissions for an exhibition.
#
# Creates three standard roles (Viewer, Contributor, Admin), grants Viewer to
# all public visitors and Contributor to all logged-in users, then creates an
# "Admins" team seeded with the specified user and grants that team the Admin
# role.
#
# Usage:
#   DATABASE_URL=postgres://... ./scripts/seed-exhibition.sh <exhibition-id> <user-id>
#
# Arguments:
#   exhibition-id   UUID of the target exhibition (must already exist)
#   user-id         UUID of the user to make the initial admin
#
# The script is idempotent: re-running it for the same exhibition will not
# create duplicate roles, teams, or grants (ON CONFLICT DO NOTHING throughout).

set -euo pipefail

# ── Argument validation ───────────────────────────────────────────────────────

usage() {
    echo "Usage: DATABASE_URL=<dsn> $0 <exhibition-id> <user-id>" >&2
    exit 1
}

if [[ $# -ne 2 ]]; then
    usage
fi

EXHIBITION_ID="$1"
USER_ID="$2"

UUID_RE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'

if [[ ! "$EXHIBITION_ID" =~ $UUID_RE ]]; then
    echo "Error: exhibition-id is not a valid UUID: $EXHIBITION_ID" >&2
    exit 1
fi

if [[ ! "$USER_ID" =~ $UUID_RE ]]; then
    echo "Error: user-id is not a valid UUID: $USER_ID" >&2
    exit 1
fi

if [[ -z "${DATABASE_URL:-}" ]]; then
    echo "Error: DATABASE_URL environment variable is not set." >&2
    exit 1
fi

# ── Run ───────────────────────────────────────────────────────────────────────

echo "Seeding exhibition $EXHIBITION_ID with admin user $USER_ID..."

psql "$DATABASE_URL" <<SQL
BEGIN;

-- ── Verify exhibition and user exist ─────────────────────────────────────────

DO \$\$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM exhibitions
        WHERE exhibitionid = '$EXHIBITION_ID'::uuid
          AND deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'Exhibition not found: $EXHIBITION_ID';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM users
        WHERE userid = '$USER_ID'::uuid
          AND deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'User not found: $USER_ID';
    END IF;
END;
\$\$;

-- ── Roles ─────────────────────────────────────────────────────────────────────

INSERT INTO roles (exhibitionid, name, description)
VALUES
    ('$EXHIBITION_ID'::uuid, 'Viewer',      'Can view photos, labels, emojis, and comments.'),
    ('$EXHIBITION_ID'::uuid, 'Contributor', 'Can view and contribute labels, emoji reactions, and comments.'),
    ('$EXHIBITION_ID'::uuid, 'Admin',       'Full administrative access to the exhibition.')
ON CONFLICT (exhibitionid, name) DO NOTHING;

-- ── Role permissions ──────────────────────────────────────────────────────────

-- Viewer: read-only
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES ('View')) AS p(perm)
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Viewer'
ON CONFLICT DO NOTHING;

-- Contributor: standard CRUD
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES ('View'), ('Create'), ('Modify'), ('Delete')) AS p(perm)
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Contributor'
ON CONFLICT DO NOTHING;

-- Admin: full access
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES
           ('View'), ('Create'), ('Modify'), ('Delete'),
           ('Admin'), ('LabelAdmin'), ('EmojiAdmin'), ('UserAdmin'), ('GalleryAdmin')
       ) AS p(perm)
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Admin'
ON CONFLICT DO NOTHING;

-- ── Global grants ─────────────────────────────────────────────────────────────

-- Grant Viewer to Public (unauthenticated visitors can read)
INSERT INTO entity_role_grants (roleid, entity_type)
SELECT roleid, 'Public'
FROM   roles
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Viewer'
  AND  NOT EXISTS (
           SELECT 1 FROM entity_role_grants erg
           WHERE  erg.roleid = roles.roleid
             AND  erg.entity_type = 'Public'
             AND  erg.entity_ref  IS NULL
             AND  erg.resource_type IS NULL
       );

-- Grant Contributor to LoggedIn (all authenticated users can contribute)
INSERT INTO entity_role_grants (roleid, entity_type)
SELECT roleid, 'LoggedIn'
FROM   roles
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Contributor'
  AND  NOT EXISTS (
           SELECT 1 FROM entity_role_grants erg
           WHERE  erg.roleid = roles.roleid
             AND  erg.entity_type = 'LoggedIn'
             AND  erg.entity_ref  IS NULL
             AND  erg.resource_type IS NULL
       );

-- ── Admins team ───────────────────────────────────────────────────────────────

INSERT INTO teams (exhibitionid, name, description)
VALUES (
    '$EXHIBITION_ID'::uuid,
    'Admins',
    'Exhibition administrators.'
)
ON CONFLICT (exhibitionid, name) DO NOTHING;

-- Add the specified user to the Admins team
INSERT INTO team_members (teamid, userid)
SELECT t.teamid, '$USER_ID'::uuid
FROM   teams t
WHERE  t.exhibitionid = '$EXHIBITION_ID'::uuid
  AND  t.name = 'Admins'
  AND  t.deleted_at IS NULL
ON CONFLICT DO NOTHING;

-- Grant Admin role to the Admins team
INSERT INTO entity_role_grants (roleid, entity_type, entity_ref)
SELECT r.roleid, 'Team', t.teamid::text
FROM   roles r
JOIN   teams t ON t.exhibitionid = r.exhibitionid
WHERE  r.exhibitionid = '$EXHIBITION_ID'::uuid
  AND  r.name = 'Admin'
  AND  t.name = 'Admins'
  AND  t.deleted_at IS NULL
  AND  NOT EXISTS (
           SELECT 1 FROM entity_role_grants erg
           WHERE  erg.roleid      = r.roleid
             AND  erg.entity_type = 'Team'
             AND  erg.entity_ref  = t.teamid::text
             AND  erg.resource_type IS NULL
       );

COMMIT;
SQL

echo "Done."
echo ""
echo "  Exhibition : $EXHIBITION_ID"
echo "  Admin user : $USER_ID"
echo "  Roles      : Viewer, Contributor, Admin"
echo "  Team       : Admins (member: $USER_ID)"
