-- Migration 013: Remove the authorized_non_public column from users.
--
-- This column was a legacy side-channel for granting access to non-public
-- photos. It is replaced by the PrivatePhotoView permission in the role-based
-- permissions system introduced in migration 012.
--
-- To grant a user access to private photos, add them to a team that holds a
-- role with the PrivatePhotoView permission.

ALTER TABLE users DROP COLUMN IF EXISTS authorized_non_public;
