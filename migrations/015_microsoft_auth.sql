-- migrations/015_microsoft_auth.sql
-- Add Microsoft Sign-In support to users table.

BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS microsoft_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_microsoft_id ON users (microsoft_id) WHERE microsoft_id IS NOT NULL;

COMMIT;
