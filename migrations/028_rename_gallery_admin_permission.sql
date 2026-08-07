-- migrations/028_rename_gallery_admin_permission.sql
-- Renames the "GalleryAdmin" permission string to "GalleryManager", matching
-- the Go constant rename PermGalleryAdmin -> PermGalleryManager
-- (internal/permissions/permissions.go) done when the Gallery Admin page
-- itself was renamed Gallery Manager (app/gallery-manager.html).
--
-- No schema_migrations tracking table in this project (see other migrations'
-- own comments) — every migration re-runs in full on every `make migrate-up`,
-- so this has to be idempotent on its own. INSERT ... ON CONFLICT DO NOTHING
-- followed by DELETE (rather than a plain UPDATE) handles the edge case
-- where a role somehow already has both "GalleryAdmin" and "GalleryManager"
-- bundled — role_permissions' primary key is (roleid, permission), so a
-- plain UPDATE could violate that constraint in that case; this can't.
--
-- Run with: psql $DATABASE_URL -f migrations/028_rename_gallery_admin_permission.sql

BEGIN;

INSERT INTO role_permissions (roleid, permission)
SELECT roleid, 'GalleryManager'
FROM role_permissions
WHERE permission = 'GalleryAdmin'
ON CONFLICT DO NOTHING;

DELETE FROM role_permissions WHERE permission = 'GalleryAdmin';

COMMIT;
