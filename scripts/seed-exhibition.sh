#!/usr/bin/env bash
# seed-exhibition.sh — Bootstrap the standard Viewer/Contributor/Admin roles,
# grants, and an "Admins" team for an already-existing exhibition.
#
# Takes an exhibition-id and user-id, both of which must already exist
# (validated below) -- this script only *configures* an exhibition, it does
# not create organizations, exhibitions, or users itself. For that, see
# scripts/seed-organization.sh (creates/reuses an organization and an
# optional admin user) and either the app's own exhibition-creation flow
# (POST /api/v1/exhibitions, which self-bootstraps the same roles this
# script sets up) or direct SQL to create an exhibition row.
#
# Not tied to any one particular install: run this against any
# exhibition-id/user-id pair you like, as many times as you like -- e.g. to
# (re)configure a legacy/migration-seeded exhibition, to set up a second
# admin for an exhibition someone else created, or to reapply the standard
# role bundle after upgrading (new permissions in a later Phase just need
# adding to the lists below and re-running this script picks them up, same
# as it always has).
#
# Creates, all idempotent (ON CONFLICT DO NOTHING / NOT EXISTS guards
# throughout, so re-running for the same exhibition is always safe):
#   - The standard exhibition-level Viewer/Contributor/Admin roles
#     (Viewer -> Public, Contributor -> LoggedIn, Admin -> an "Admins" team
#     seeded with the given user).
#   - Every permission string known to
#     internal/permissions/permissions.go's PermissionCatalog(), kept in the
#     same grouping/order as that function so the two are easy to diff
#     against each other by eye -- see scripts/seed-organization.sh's
#     matching list. There is no DB table to query this from
#     (PermissionCatalog() is pure Go), so this list has to be maintained by
#     hand; if you add a permission constant there, add it here too (and to
#     seed-organization.sh).
#
# Usage:
#   DATABASE_URL=postgres://... ./scripts/seed-exhibition.sh <exhibition-id> <user-id>
#
# Arguments:
#   exhibition-id   UUID of the target exhibition (must already exist,
#                   non-deleted).
#   user-id         UUID of the user to make the initial admin (must already
#                   exist, non-deleted) -- added to a new/existing "Admins"
#                   team for this exhibition, which is granted the Admin
#                   role.

set -euo pipefail

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

echo "Seeding exhibition $EXHIBITION_ID with admin user $USER_ID..."

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<SQL
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

-- ── All permissions (used by the Admin role bundle below) ────────────────────
--
-- A TEMP TABLE (not a CTE) so it can be referenced from both of the
-- separate statements below within this one transaction/session.
CREATE TEMP TABLE _all_permissions (perm text) ON COMMIT DROP;
INSERT INTO _all_permissions (perm) VALUES
    ('GalleryView'),   ('GalleryCreate'),   ('GalleryModify'),   ('GalleryDelete'),
    ('DisplayView'),   ('DisplayCreate'),   ('DisplayModify'),   ('DisplayDelete'),
    ('PhotoView'),     ('PhotoCreate'),     ('PhotoDelete'),     ('PrivatePhotoView'), ('PhotoDescriptionModify'),
    ('PhotoLabelView'),   ('PhotoLabelCreate'),   ('PhotoLabelModify'),   ('PhotoLabelDelete'),
    ('PhotoEmojiView'),   ('PhotoEmojiCreate'),   ('PhotoEmojiDelete'),   ('PhotoEmojiReact'),
    ('PhotoCommentView'), ('PhotoCommentCreate'), ('PhotoCommentModify'), ('PhotoCommentDelete'), ('CommentEmojiReact'),
    ('EmojiUpload'),   ('EmojiCreate'),     ('EmojiModify'),     ('EmojiDelete'),
    ('LabelNameView'), ('LabelNameCreate'), ('LabelNameModify'), ('LabelNameDelete'),
    ('TeamView'),      ('TeamCreate'),      ('TeamModify'),      ('TeamDelete'),
    ('RoleView'),      ('RoleCreate'),      ('RoleModify'),      ('RoleDelete'),
    ('Admin'), ('LabelAdmin'), ('EmojiAdmin'), ('UserAdmin'), ('GalleryAdmin'),
    ('TeamAdmin'), ('PermissionsAdmin');

-- ── Roles ─────────────────────────────────────────────────────────────────────

INSERT INTO roles (exhibitionid, name, description)
VALUES
    ('$EXHIBITION_ID'::uuid, 'Viewer',      'Can view photos, labels, emojis, and comments.'),
    ('$EXHIBITION_ID'::uuid, 'Contributor', 'Can view and contribute labels, emoji reactions, and comments.'),
    ('$EXHIBITION_ID'::uuid, 'Admin',       'Full administrative access to the exhibition.')
ON CONFLICT (exhibitionid, name) DO NOTHING;

-- ── Role permissions ──────────────────────────────────────────────────────────

-- Viewer: read-only access to galleries, displays, photos, and their
-- annotations. PhotoView is required here (PLAN2.md Phase 2a enforcement,
-- SUMMARIES2.md Session 30) -- without it, granting Viewer to Public would
-- no longer make any photo visible at all, since PhotoView now gates photo
-- visibility itself, on top of the Public label / PrivatePhotoView check
-- it's paired with.
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES
           ('GalleryView'),
           ('DisplayView'),
           ('PhotoView'),
           ('PhotoLabelView'),
           ('PhotoEmojiView'),
           ('PhotoCommentView')
       ) AS p(perm)
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Viewer'
ON CONFLICT DO NOTHING;

-- Contributor: can browse galleries/displays, upload photos, and fully
-- manage labels, emoji reactions, and comments. PhotoDelete and
-- gallery/display creation/deletion are intentionally omitted -- only
-- Admins may do those. PhotoView included for the same reason as Viewer
-- above.
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles,
       (VALUES
           ('GalleryView'),
           ('DisplayView'),
           ('PhotoView'),
           ('PhotoCreate'),
           ('PhotoLabelView'),   ('PhotoLabelCreate'),   ('PhotoLabelModify'),   ('PhotoLabelDelete'),
           ('PhotoEmojiView'),   ('PhotoEmojiCreate'),   ('PhotoEmojiDelete'),
           ('PhotoCommentView'), ('PhotoCommentCreate'), ('PhotoCommentModify'), ('PhotoCommentDelete')
       ) AS p(perm)
WHERE  exhibitionid = '$EXHIBITION_ID'::uuid
  AND  name = 'Contributor'
ON CONFLICT DO NOTHING;

-- Admin: every known permission (via _all_permissions above), including
-- gallery/display management, photo deletion, and administrative controls.
INSERT INTO role_permissions (roleid, permission)
SELECT roleid, perm
FROM   roles, _all_permissions
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

-- Add the specified user to the Admins team.
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
echo "  Exhibition : $EXHIBITION_ID"
echo "  Admin user : $USER_ID"
echo "  Roles      : Viewer, Contributor, Admin"
echo "  Team       : Admins (member: $USER_ID)"
