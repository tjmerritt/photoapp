-- migrations/011_facebook_auth.sql
-- Add Facebook Sign-In support to users table.

BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS facebook_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_facebook_id ON users (facebook_id) WHERE facebook_id IS NOT NULL;

COMMIT;
