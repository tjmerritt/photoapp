-- migrations/017_admin_phase6.sql
-- Phase 6 (Admin Pages) support columns.
--
-- 6b (user admin) needs four per-user toggles: account enabled/disabled,
-- access to private photos, and "manage my own labels/emoji/comments".
-- These are NOT implemented uniformly:
--
--   - Private-photo access needs no new column at all. Ordinary users don't
--     hold PermPrivatePhotoView by default (only the seeded Admin role
--     does), so granting it per-user is a pure *addition* on top of a
--     user's existing role grants — which the existing entity_role_grants
--     table already does natively. See permissions.Checker.GrantUserPermission
--     / RevokeUserPermission, which grant/revoke this via a small
--     auto-created "singleton" role rather than a new table.
--
--   - The other three (manage own labels/emoji/comments) are different: the
--     seed-exhibition.sh "Contributor" role already grants PhotoLabelCreate,
--     PhotoEmojiCreate/Delete, and PhotoCommentCreate to *every* logged-in
--     user via a single LoggedIn-entity grant. Permission checks in this
--     codebase are purely additive (Checker.Check ORs together every
--     matching grant) — there is no way to carve a single user back out of
--     a broad LoggedIn grant using that mechanism. Disabling one troublesome
--     user's ability to add labels/react/comment therefore needs a real,
--     per-user override that the handlers can AND against the permission
--     check, hence real columns here.
--
--   - Account enabled/disabled is likewise not a permission at all (it's an
--     authentication-level gate, checked in AuthHandler.LookupSession) so it
--     gets a column too.
--
-- 6e (label admin) adds `enabled` to label_names, parallel to
-- emoji_types.is_active (migration 001) — a disabled label name is hidden
-- from the add/autocomplete UI and blocked from new use, the same way an
-- inactive emoji type is, while labels already using that name remain
-- visible. This is intentionally a separate concept from `restricted`
-- (migration 016): restricted means "view-only for non-admins", enabled
-- means "hidden/blocked entirely for non-admins".
--
-- Run with: psql $DATABASE_URL -f migrations/017_admin_phase6.sql

BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS account_enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS can_manage_own_labels   BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS can_manage_own_emoji    BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS can_manage_own_comments BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE label_names
    ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE;

COMMIT;
