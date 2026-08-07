#!/usr/bin/env bash
# seed-organization.sh — Create (or reuse) an organization, and optionally
# create/reuse a local admin user granted organization-level Admin over it.
#
# This is the organization half of what a single seed-exhibition.sh used to
# do before Phase 1d hardcoded it into a fixed "Initial Org"/"Initial
# Exhibition" bootstrap (see SUMMARIES2.md Session 21/40). Splitting
# organization seeding from exhibition seeding, and taking the org name/admin
# identity as arguments instead of fixed UUIDs, means this script isn't tied
# to any one particular install -- run it as many times, for as many
# organizations, as you need.
#
# Idempotent by natural key, not by a hardcoded id:
#   - Organization is looked up by exact NAME (non-deleted) before creating
#     one -- organizations has no unique constraint on name, so this script
#     is what makes re-running it for the same name safe rather than a
#     database constraint.
#   - Admin user (if requested) is looked up by USERNAME or EMAIL
#     (non-deleted) before creating one -- both columns are genuinely
#     UNIQUE (migrations/001_initial.sql), so this mirrors a real conflict
#     the database would otherwise raise.
#   - The organization-level Admin role and its permission bundle
#     (migrations/012_permissions.sql + 021_org_admin.sql) are created via
#     ON CONFLICT DO NOTHING against uq_role_name_org; the grant itself has
#     no natural unique constraint, so it's guarded with NOT EXISTS instead,
#     the same convention seed-exhibition.sh uses.
#
# Usage:
#   DATABASE_URL=postgres://... ./scripts/seed-organization.sh <org-name>
#   DATABASE_URL=postgres://... ./scripts/seed-organization.sh <org-name> <admin-username> <admin-email> [admin-password]
#
# Arguments:
#   org-name         Name of the organization to create, or reuse if a
#                     non-deleted organization already exists with this
#                     exact name.
#   admin-username   Optional. If given (admin-email is then required too),
#                     creates -- or reuses an existing non-deleted user
#                     matching this username or email -- a local-provider
#                     user, and grants them an organization-level Admin role
#                     covering every exhibition under this org, present and
#                     future (PLAN2.md Phase 1c/1d; see
#                     internal/handlers/exhibitions.go's grantOrgAdmin,
#                     which this mirrors by hand for a pre-existing org the
#                     same way seed-exhibition.sh mirrors
#                     bootstrapExhibitionRoles for an exhibition).
#   admin-email      Required if admin-username is given.
#   admin-password   Optional, only used when actually creating a new user
#                     (ignored if an existing user is reused). Defaults to a
#                     random 20-character password, printed once at the end
#                     -- note it down or change it after first login.
#
# Once you have an org (and, if you asked for one, an admin user), that
# admin can log in and create exhibitions through the app itself
# (POST /api/v1/exhibitions bootstraps an exhibition's Viewer/Contributor/
# Admin roles automatically), or you can point scripts/seed-exhibition.sh at
# any already-existing exhibition-id/user-id pair -- including one under
# this org -- to (re)configure its standard roles/grants directly.
#
# Requires the pgcrypto extension for bcrypt-compatible password hashing via
# crypt()/gen_salt('bf') -- see seed-exhibition.sh's header comment for why.
# Created automatically below if missing.

set -euo pipefail

usage() {
    cat >&2 <<'USAGE'
Usage: DATABASE_URL=<dsn> scripts/seed-organization.sh <org-name>
       DATABASE_URL=<dsn> scripts/seed-organization.sh <org-name> <admin-username> <admin-email> [admin-password]
USAGE
    exit 1
}

if [[ $# -lt 1 ]]; then
    usage
fi

ORG_NAME="$1"
shift

ADMIN_USERNAME=""
ADMIN_EMAIL=""
ADMIN_PASSWORD=""
GENERATED_PASSWORD=0

if [[ $# -ge 1 ]]; then
    if [[ $# -lt 2 ]]; then
        echo "Error: admin-email is required when admin-username is given." >&2
        usage
    fi
    ADMIN_USERNAME="$1"
    ADMIN_EMAIL="$2"
    ADMIN_PASSWORD="${3:-}"
    if [[ -z "$ADMIN_PASSWORD" ]]; then
        ADMIN_PASSWORD="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 20)"
        GENERATED_PASSWORD=1
    fi
fi

if [[ -z "${DATABASE_URL:-}" ]]; then
    echo "Error: DATABASE_URL environment variable is not set." >&2
    exit 1
fi

# Single-quote escaping for values interpolated directly into the SQL
# heredoc below (doubles any embedded single quote, the standard SQL
# literal-escaping rule).
sql_escape() {
    printf '%s' "$1" | sed "s/'/''/g"
}

ORG_NAME_SQL="$(sql_escape "$ORG_NAME")"
ADMIN_USERNAME_SQL="$(sql_escape "$ADMIN_USERNAME")"
ADMIN_EMAIL_SQL="$(sql_escape "$ADMIN_EMAIL")"
ADMIN_PASSWORD_SQL="$(sql_escape "$ADMIN_PASSWORD")"

if [[ -n "$ADMIN_USERNAME" ]]; then
    echo "Seeding organization \"$ORG_NAME\" with admin $ADMIN_USERNAME <$ADMIN_EMAIL>..."
else
    echo "Seeding organization \"$ORG_NAME\" (no admin user requested)..."
fi

RESULT="$(psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -t -A -F'|' <<SQL
BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TEMP TABLE _seed_result (key TEXT PRIMARY KEY, value TEXT);

DO \$do\$
DECLARE
    v_org_id  uuid;
    v_user_id uuid;
    v_role_id uuid;
BEGIN
    -- ── Organization: find by name, else create ────────────────────────────
    SELECT organizationid INTO v_org_id
    FROM   organizations
    WHERE  name = '$ORG_NAME_SQL' AND deleted_at IS NULL
    LIMIT  1;

    IF v_org_id IS NULL THEN
        INSERT INTO organizations (name) VALUES ('$ORG_NAME_SQL')
        RETURNING organizationid INTO v_org_id;
    END IF;

    INSERT INTO _seed_result (key, value) VALUES ('organizationid', v_org_id::text);

    IF '$ADMIN_USERNAME_SQL' <> '' THEN
        -- ── Admin user: find by username or email, else create ────────────
        --
        -- provider='local' + explicit password hash so this user can log in
        -- via POST /auth/login (which only matches provider='local' rows) --
        -- same reasoning as seed-exhibition.sh's own user insert.
        SELECT userid INTO v_user_id
        FROM   users
        WHERE  (username = '$ADMIN_USERNAME_SQL' OR email = lower('$ADMIN_EMAIL_SQL'))
          AND  deleted_at IS NULL
        LIMIT  1;

        IF v_user_id IS NULL THEN
            INSERT INTO users (username, email, password_hash, provider)
            VALUES (
                '$ADMIN_USERNAME_SQL',
                lower('$ADMIN_EMAIL_SQL'),
                crypt('$ADMIN_PASSWORD_SQL', gen_salt('bf', 10)),
                'local'
            )
            RETURNING userid INTO v_user_id;

            INSERT INTO _seed_result (key, value) VALUES ('user_created', 'true');
        ELSE
            INSERT INTO _seed_result (key, value) VALUES ('user_created', 'false');
        END IF;

        INSERT INTO _seed_result (key, value) VALUES ('userid', v_user_id::text);

        -- ── Organization-level Admin role (PLAN2.md Phase 1c) ──────────────
        --
        -- Every permission string known to
        -- internal/permissions/permissions.go's PermissionCatalog(), kept in
        -- the same grouping/order so the two are easy to diff against each
        -- other by eye -- see seed-exhibition.sh's matching comment. There
        -- is no DB table to query this from (PermissionCatalog() is pure
        -- Go), so this list has to be maintained by hand in both scripts;
        -- if you add a permission constant there, add it here too.
        INSERT INTO roles (organizationid, name, description)
        VALUES (
            v_org_id,
            'Admin',
            'Full administrative access to the organization and all its exhibitions.'
        )
        ON CONFLICT (organizationid, name) WHERE organizationid IS NOT NULL DO NOTHING;

        SELECT roleid INTO v_role_id
        FROM   roles
        WHERE  organizationid = v_org_id AND name = 'Admin';

        INSERT INTO role_permissions (roleid, permission)
        SELECT v_role_id, perm
        FROM   (VALUES
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
            ('Admin'), ('LabelAdmin'), ('EmojiAdmin'), ('UserAdmin'), ('GalleryManager'),
            ('TeamAdmin'), ('PermissionsAdmin')
        ) AS p(perm)
        ON CONFLICT DO NOTHING;

        -- entity_role_grants has no unique constraint to ON CONFLICT
        -- against besides its own auto-generated id -- guard with NOT
        -- EXISTS instead, same convention as seed-exhibition.sh.
        IF NOT EXISTS (
            SELECT 1 FROM entity_role_grants erg
            WHERE  erg.roleid = v_role_id
              AND  erg.entity_type = 'User'
              AND  erg.entity_ref  = v_user_id::text
              AND  erg.organizationid = v_org_id
        ) THEN
            INSERT INTO entity_role_grants (roleid, entity_type, entity_ref, organizationid)
            VALUES (v_role_id, 'User', v_user_id::text, v_org_id);
        END IF;
    END IF;
END
\$do\$;

COMMIT;

SELECT key, value FROM _seed_result;
SQL
)"

echo ""
echo "Done."
echo ""

ORG_ID=""
USER_ID=""
USER_CREATED=""
while IFS='|' read -r key value; do
    case "$key" in
        organizationid) ORG_ID="$value" ;;
        userid) USER_ID="$value" ;;
        user_created) USER_CREATED="$value" ;;
    esac
done <<<"$RESULT"

echo "  Organization : $ORG_NAME  ($ORG_ID)"
if [[ -n "$ADMIN_USERNAME" ]]; then
    echo "  Admin user   : $ADMIN_USERNAME / $ADMIN_EMAIL  ($USER_ID)"
    if [[ "$USER_CREATED" == "true" ]]; then
        if [[ "$GENERATED_PASSWORD" -eq 1 ]]; then
            echo "  Password     : $ADMIN_PASSWORD  (generated -- note it down, or change it after first login)"
        else
            echo "  Password     : (as given)"
        fi
    else
        echo "  (Reused an existing user -- any password argument given was ignored.)"
    fi
    echo "  Org grant    : $ADMIN_USERNAME is an organization-level Admin of \"$ORG_NAME\""
    echo "                 (covers every exhibition under it, present and future)"
else
    echo "  (No admin user requested -- pass admin-username/admin-email to create/grant one.)"
fi
