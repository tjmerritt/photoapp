-- migrations/006_auth_providers.sql
-- Add OAuth provider support to users table.

BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS google_id TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS apple_id  TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS provider  TEXT NOT NULL DEFAULT 'local';

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_google_id ON users (google_id) WHERE google_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_users_apple_id  ON users (apple_id)  WHERE apple_id  IS NOT NULL;

COMMIT;
