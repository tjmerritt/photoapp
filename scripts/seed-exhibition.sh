#!/usr/bin/env bash
# seed-exhibition.sh — Bootstrap an initial organization, exhibition, admin
# user, and permissions for local development / a fresh install.
#
# Creates, all idempotent (fixed IDs below, so re-running is always safe --
# every INSERT either ON CONFLICT DO NOTHINGs against those fixed IDs or, for
# entity_role_grants which has no natural unique constraint to conflict on,
# guards itself with a NOT EXISTS check, matching the rest of this script):
#
#   - Organization "Initial Org"
#   - Exhibition   "Initial Exhibition" (owned by Initial Org)
#   - User         inituser / inituser@example.com / password "noyet"
#   - An organization-level Admin role + grant for inituser (PLAN2.md Phase
#     1c) -- covers every exhibition under Initial Org, present and future,
#     not just the one created here.
#   - The standard exhibition-level Viewer/Contributor/Admin roles for
#     Initial Exhibition (Viewer -> Public, Contributor -> LoggedIn,
#     Admin -> an "Admins" team seeded with inituser) -- the same setup this
#     script has always bootstrapped for an exhibition, now run against the
#     exhibition it creates itself instead of one passed in as an argument.
#
# Usage:
#   DATABASE_URL=postgres://... ./scripts/seed-exhibition.sh
#
# No arguments: this always seeds the one fixed "Initial ..." installation
# described above, rather than accepting an arbitrary already-existing
# exhibition-id/user-id the way earlier versions of this script did.
#
# Requires the pgcrypto extension, for bcrypt-compatible password hashing via
# crypt()/gen_salt('bf') -- the same algorithm internal/handlers/auth.go uses
# via golang.org/x/crypto/bcrypt for password-based accounts, just generated
# from SQL instead of Go so this script has no dependency beyond psql.
# Created automatically below (CREATE EXTENSION IF NOT EXISTS) if missing --
# needs either superuser or a Postgres version/hosting provider that trusts
# pgcrypto for non-superuser CREATE EXTENSION (true of most local installs
# and managed providers; pgcrypto has been a "trusted" extension since PG13).

set -euo pipefail

if [[ -z "${DATABASE_URL:-}" ]]; then
    echo "Error: DATABASE_URL environment variable is not set." >&2
    exit 1
fi

# Fixed UUIDs, following this codebase's existing convention for singleton
# seed rows (migrations/008_exhibitions.sql's Default Exhibition
# 'ffffffff-...-000000000001', migrations/019_organizations.sql's Legacy
# Organization 'eeeeeeee-...-000000000001') -- lets this script, and anything
# else, refer to these rows by a known constant and makes every insert here
# idempotent via ON CONFLICT DO NOTHING instead of needing to look the ids
# back up after the fact.
ORG_ID="dddddddd-0000-0000-0000-000000000001"
EXHIBITION_ID="bbbbbbbb-0000-0000-0000-000000000001"
USER_ID="99999999-0000-0000-0000-000000000001"

echo "Seeding initial organization, exhibition, and admin user..."

psql "$DATABASE_URL" <<SQL
BEGIN;

-- pgcrypto: only used for crypt()/gen_salt('bf') below (bcrypt-compatible
-- password hashing). Not the same thing as gen_random_uuid(), which is a
-- core Postgres function (PG13+) and needs no extension.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ── Organization ──────────────────────────────────────────────────────────────

INSERT INTO organizations (organizationid, name)
VALUES ('$ORG_ID'::uuid, 'Initial Org')
ON CONFLICT (organizationid) DO NOTHING;

-- ── Exhibition ────────────────────────────────────────────────────────────────

INSERT INTO exhibitions (exhibitionid, name, organizationid)
VALUES ('$EXHIBITION_ID'::uuid, 'Initial Exhibition', '$ORG_ID'::uuid)
ON CONFLICT (exhibitionid) DO NOTHING;

-- ── User ──────────────────────────────────────────────────────────────────────
--
-- provider = 'local' matches column default (migrations/006_auth_providers.sql)
-- but is set explicitly here to match internal/handlers/auth.go's own
-- Register insert -- and, more importantly, because /auth/login only ever
-- matches rows WHERE provider = 'local', so this user couldn't log in with
-- a password otherwise. account_enabled defaults to TRUE
-- (migrations/017_admin_phase6.sql), which is what's wanted here.
INSERT INTO users (userid, username, email, password_hash, provider)
VALUES (
    '$USER_ID'::uuid,
    'inituser',
    'inituser@example.com',
    crypt('noyet', gen_salt('bf', 10)),
    'local'
)
ON CONFLICT (userid) DO NOTHING;

-- Exhibition membership (Phase 6b's admin listing, and the header's
-- Organization/Exhibition picker, both key off this table).
INSERT INTO user_exhibitions (userid, exhibitionid)
VALUES ('$USER_ID'::uuid, '$EXHIBITION_ID'::uuid)
ON CONFLICT (userid, exhibitionid) DO NOTHING;

-- ── Organization-level Admin (PLAN2.md Phase 1c) ─────────────────────────────
--
-- Grants inituser PermAdmin (and everything else the Admin bundle below
-- includes) across every exhibition under Initial Org, present and future --
-- see migrations/021_org_admin.sql and internal/handlers/exhibitions.go's
-- grantOrgAdmin, which this mirrors by hand the same way this script has
-- always mirrored bootstrapExhibitionRoles for the exhibition-level roles
-- below. uq_role_name_org is a partial unique index
-- (WHERE organizationid IS NOT NULL) -- Postgres only infers it as an
-- ON CONFLICT arbiter if that same predicate is repeated here.
INSERT INTO roles (organizationid, name, description)
VALUES (
    '$ORG_ID'::uuid,
    'Admin',
    'Full administrative access to the organization and all its exhibitions.'
)
ON CONFLICT (organizationid, name) WHERE organizationid IS NOT NULL DO NOTHING;

INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES
           ('GalleryView'),   ('GalleryCreate'),   ('GalleryModify'),   ('GalleryDelete'),
           ('DisplayView'),   ('DisplayCreate'),   ('DisplayModify'),   ('DisplayDelete'),
           ('PhotoCreate'),   ('PhotoDelete'),     ('PrivatePhotoView'),     ('PhotoDescriptionModify'),
           ('PhotoLabelView'),   ('PhotoLabelCreate'),   ('PhotoLabelModify'),   ('PhotoLabelDelete'),
           ('PhotoEmojiView'),   ('PhotoEmojiCreate'),   ('PhotoEmojiDelete'),
           ('PhotoCommentView'), ('PhotoCommentCreate'), ('PhotoCommentModify'), ('PhotoCommentDelete'),
           ('EmojiUpload'),
           ('Admin'), ('LabelAdmin'), ('EmojiAdmin'), ('UserAdmin'), ('GalleryAdmin'),
           ('TeamAdmin'), ('PermissionsAdmin')
       ) AS p(perm)
WHERE  organizationid = '$ORG_ID'::uuid
  AND  name = 'Admin'
ON CONFLICT DO NOTHING;

-- entity_role_grants has no unique constraint to ON CONFLICT against besides
-- its own auto-generated id -- guard with NOT EXISTS instead, same as every
-- other grant insert in this script.
INSERT INTO entity_role_grants (roleid, entity_type, entity_ref, organizationid)
SELECT r.roleid, 'User', '$USER_ID'::text, '$ORG_ID'::uuid
FROM   roles r
WHERE  r.organizationid = '$ORG_ID'::uuid
  AND  r.name = 'Admin'
  AND  NOT EXISTS (
           SELECT 1 FROM entity_role_grants erg
           WHERE  erg.roleid = r.roleid
             AND  erg.entity_type = 'User'
             AND  erg.entity_ref  = '$USER_ID'::text
             AND  erg.organizationid = '$ORG_ID'::uuid
       );

-- ── Exhibition-level roles ───────────────────────────────────────────────────

INSERT INTO roles (exhibitionid, name, description)
VALUES
    ('$EXHIBITION_ID'::uuid, 'Viewer',      'Can view photos, labels, emojis, and comments.'),
    ('$EXHIBITION_ID'::uuid, 'Contributor', 'Can view and contribute labels, emoji reactions, and comments.'),
    ('$EXHIBITION_ID'::uuid, 'Admin',       'Full administrative access to the exhibition.')
ON CONFLICT (exhibitionid, name) DO NOTHING;

-- ── Role permissions ──────────────────────────────────────────────────────────

-- Viewer: read-only access to galleries, displays, photos, and their annotations.
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES
           ('GalleryView'),
           ('DisplayView'),
           ('PhotoLabelView'),
           ('PhotoEmojiView'),
           ('PhotoCommentView')
       ) AS p(perm)
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Viewer'
ON CONFLICT DO NOTHING;

-- Contributor: can browse galleries/displays, upload photos, and fully manage
-- labels, emoji reactions, and comments. PhotoDelete and gallery/display
-- creation/deletion are intentionally omitted -- only Admins may do those.
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES
           ('GalleryView'),
           ('DisplayView'),
           ('PhotoCreate'),
           ('PhotoLabelView'),   ('PhotoLabelCreate'),   ('PhotoLabelModify'),   ('PhotoLabelDelete'),
           ('PhotoEmojiView'),   ('PhotoEmojiCreate'),   ('PhotoEmojiDelete'),
           ('PhotoCommentView'), ('PhotoCommentCreate'), ('PhotoCommentModify'), ('PhotoCommentDelete')
       ) AS p(perm)
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Contributor'
ON CONFLICT DO NOTHING;

-- Admin: all permissions including gallery/display management, photo deletion,
-- and administrative controls.
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES
           ('GalleryView'),   ('GalleryCreate'),   ('GalleryModify'),   ('GalleryDelete'),
           ('DisplayView'),   ('DisplayCreate'),   ('DisplayModify'),   ('DisplayDelete'),
           ('PhotoCreate'),   ('PhotoDelete'),     ('PrivatePhotoView'),     ('PhotoDescriptionModify'),
           ('PhotoLabelView'),   ('PhotoLabelCreate'),   ('PhotoLabelModify'),   ('PhotoLabelDelete'),
           ('PhotoEmojiView'),   ('PhotoEmojiCreate'),   ('PhotoEmojiDelete'),
           ('PhotoCommentView'), ('PhotoCommentCreate'), ('PhotoCommentModify'), ('PhotoCommentDelete'),
           ('EmojiUpload'),
           ('Admin'), ('LabelAdmin'), ('EmojiAdmin'), ('UserAdmin'), ('GalleryAdmin'),
           ('TeamAdmin'), ('PermissionsAdmin')
       ) AS p(perm)
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Admin'
ON CONFLICT DO NOTHING;

-- ── Global grants ─────────────────────────────────────────────────────────────

-- Grant Viewer to Public (unauthenticated visitors can read), scoped to this
-- exhibition only via the exhibitionid column (NOT a global grant -- see
-- migrations/018_grant_exhibitionid.sql).
INSERT INTO entity_role_grants (roleid, entity_type, exhibitionid)
SELECT roleid, 'Public', '$EXHIBITION_ID'::uuid
FROM   roles
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Viewer'
  AND  NOT EXISTS (
           SELECT 1 FROM entity_role_grants erg
           WHERE  erg.roleid = roles.roleid
             AND  erg.entity_type = 'Public'
             AND  erg.entity_ref  IS NULL
             AND  erg.exhibitionid = '$EXHIBITION_ID'::uuid
             AND  erg.resource_type IS NULL
       );

-- Grant Contributor to LoggedIn (all authenticated users can contribute),
-- scoped to this exhibition only.
INSERT INTO entity_role_grants (roleid, entity_type, exhibitionid)
SELECT roleid, 'LoggedIn', '$EXHIBITION_ID'::uuid
FROM   roles
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Contributor'
  AND  NOT EXISTS (
           SELECT 1 FROM entity_role_grants erg
           WHERE  erg.roleid = roles.roleid
             AND  erg.entity_type = 'LoggedIn'
             AND  erg.entity_ref  IS NULL
             AND  erg.exhibitionid = '$EXHIBITION_ID'::uuid
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

-- Add inituser to the Admins team.
INSERT INTO team_members (teamid, userid)
SELECT t.teamid, '$USER_ID'::uuid
FROM   teams t
WHERE  t.exhibitionid = '$EXHIBITION_ID'::uuid
  AND  t.name = 'Admins'
  AND  t.deleted_at IS NULL
ON CONFLICT DO NOTHING;

-- Grant Admin role to the Admins team, scoped to this exhibition only.
INSERT INTO entity_role_grants (roleid, entity_type, entity_ref, exhibitionid)
SELECT r.roleid, 'Team', t.teamid::text, '$EXHIBITION_ID'::uuid
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
             AND  erg.exhibitionid = '$EXHIBITION_ID'::uuid
             AND  erg.resource_type IS NULL
       );

COMMIT;
SQL

echo "Done."
echo ""
echo "  Organization : Initial Org         ($ORG_ID)"
echo "  Exhibition   : Initial Exhibition  ($EXHIBITION_ID)"
echo "  Admin user   : inituser / inituser@example.com / noyet ($USER_ID)"
echo "  Org grant    : inituser is an organization-level Admin of Initial Org"
echo "                 (covers every exhibition under it, not just this one)"
echo "  Ex. roles    : Viewer, Contributor, Admin"
echo "  Team         : Admins (member: inituser)"
