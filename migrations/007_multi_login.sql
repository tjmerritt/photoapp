-- migrations/007_multi_login.sql
-- Allow designated users (admins/devs) to switch between user contexts for testing.

BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS allow_multi_login BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
