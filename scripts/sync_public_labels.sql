-- scripts/sync_public_labels.sql
--
-- Ensures the "Public" label in the labels table is consistent with each
-- photo's is_public flag.
--
-- Run with:
--   psql $DATABASE_URL -f scripts/sync_public_labels.sql
--
-- Safe to run multiple times (idempotent).

BEGIN;

-- 1. Fix existing "Public" labels whose value is out of sync.
UPDATE labels l
SET    value      = CASE WHEN p.is_public THEN 'True' ELSE 'False' END,
       updated_at = NOW()
FROM   photos p
WHERE  l.photoid    = p.photoid
  AND  l.name       = 'Public'
  AND  l.deleted_at IS NULL
  AND  p.deleted_at IS NULL
  AND  l.value     != CASE WHEN p.is_public THEN 'True' ELSE 'False' END;

-- 2. Insert a "Public" label for any photo that doesn't have one yet,
--    using the photo's owner as the label author.
INSERT INTO labels (photoid, added_by_userid, name, value)
SELECT p.photoid,
       p.owner_userid,
       'Public',
       CASE WHEN p.is_public THEN 'True' ELSE 'False' END
FROM   photos p
WHERE  p.deleted_at IS NULL
  AND  NOT EXISTS (
    SELECT 1
    FROM   labels l
    WHERE  l.photoid    = p.photoid
      AND  l.name       = 'Public'
      AND  l.deleted_at IS NULL
  );

COMMIT;
