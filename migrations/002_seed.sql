-- migrations/002_seed.sql
-- Development seed data. Run with: make seed

BEGIN;

-- Users
INSERT INTO users (userid, username, email, fullname, profile_image, joined_at) VALUES
  ('11111111-0000-0000-0000-000000000001', 'ansel_b',  'ansel@example.com',  'Ansel Blackwood', 'https://i.pravatar.cc/64?img=1',  '2021-03-15T09:00:00Z'),
  ('11111111-0000-0000-0000-000000000002', 'lena_ray', 'lena@example.com',   'Lena Raynor',     'https://i.pravatar.cc/64?img=5',  '2020-07-22T14:30:00Z'),
  ('11111111-0000-0000-0000-000000000003', 'marco_v',  'marco@example.com',  'Marco Vidal',     'https://i.pravatar.cc/64?img=8',  '2022-01-10T11:00:00Z'),
  ('11111111-0000-0000-0000-000000000004', 'suki_tm',  'suki@example.com',   'Suki Tamamoto',   'https://i.pravatar.cc/64?img=9',  '2019-11-01T08:00:00Z'),
  ('11111111-0000-0000-0000-000000000005', 'dev_null', 'dev@example.com',    'Dev Null',        'https://i.pravatar.cc/64?img=12', '2023-05-05T00:00:00Z')
ON CONFLICT (userid) DO NOTHING;

-- Photo
INSERT INTO photos (photoid, owner_userid, image_url, image_width, image_height, title_text, title_userid, description) VALUES
  (
    'aaaaaaaa-0000-0000-0000-000000000001',
    '11111111-0000-0000-0000-000000000001',
    'https://images.unsplash.com/photo-1506905925346-21bda4d32df4?w=1200&q=80',
    1200, 800,
    'Summit at Golden Hour',
    '11111111-0000-0000-0000-000000000001',
    'Captured just as the last light kissed the ridgeline, this shot from the summit of Mt. Rainier looks east across the Cascade Range.'
  )
ON CONFLICT (photoid) DO NOTHING;

-- Related photos (point at the same photo for demo purposes)
INSERT INTO related_photos (photoid, related_photoid, scaled_image_url, sort_order) VALUES
  ('aaaaaaaa-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000001',
   'https://images.unsplash.com/photo-1464822759023-fed622ff2c3b?w=300&q=70', 1)
ON CONFLICT DO NOTHING;

-- Labels
INSERT INTO labels (photoid, added_by_userid, name, value) VALUES
  ('aaaaaaaa-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000001', 'Location', 'Mt. Rainier, WA'),
  ('aaaaaaaa-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000001', 'Camera',   'Sony A7R V'),
  ('aaaaaaaa-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000001', 'Lens',     '24-70mm f/2.8'),
  ('aaaaaaaa-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000001', 'ISO',      '400'),
  ('aaaaaaaa-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000001', 'Aperture', 'f/8'),
  ('aaaaaaaa-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000002', 'Season',   'Late Summer')
ON CONFLICT DO NOTHING;

-- Emoji types
INSERT INTO emoji_types (emojiid, emoji_char, alt_text, sort_order) VALUES
  ('eeeeeeee-0000-0000-0000-000000000001', '❤️',  'Heart',    1),
  ('eeeeeeee-0000-0000-0000-000000000002', '🔥',  'Fire',     2),
  ('eeeeeeee-0000-0000-0000-000000000003', '😍',  'Love',     3),
  ('eeeeeeee-0000-0000-0000-000000000004', '🏔️', 'Mountain', 4),
  ('eeeeeeee-0000-0000-0000-000000000005', '✨',  'Sparkle',  5),
  ('eeeeeeee-0000-0000-0000-000000000006', '👏',  'Clap',     6),
  ('eeeeeeee-0000-0000-0000-000000000007', '😮',  'Wow',      7),
  ('eeeeeeee-0000-0000-0000-000000000008', '💯',  'Perfect',  8)
ON CONFLICT (emojiid) DO NOTHING;

-- Emoji reactions
INSERT INTO emoji_reactions (photoid, emojiid, userid) VALUES
  ('aaaaaaaa-0000-0000-0000-000000000001', 'eeeeeeee-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000002'),
  ('aaaaaaaa-0000-0000-0000-000000000001', 'eeeeeeee-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000003'),
  ('aaaaaaaa-0000-0000-0000-000000000001', 'eeeeeeee-0000-0000-0000-000000000001', '11111111-0000-0000-0000-000000000004'),
  ('aaaaaaaa-0000-0000-0000-000000000001', 'eeeeeeee-0000-0000-0000-000000000002', '11111111-0000-0000-0000-000000000003'),
  ('aaaaaaaa-0000-0000-0000-000000000001', 'eeeeeeee-0000-0000-0000-000000000002', '11111111-0000-0000-0000-000000000005'),
  ('aaaaaaaa-0000-0000-0000-000000000001', 'eeeeeeee-0000-0000-0000-000000000003', '11111111-0000-0000-0000-000000000004'),
  ('aaaaaaaa-0000-0000-0000-000000000001', 'eeeeeeee-0000-0000-0000-000000000004', '11111111-0000-0000-0000-000000000001')
ON CONFLICT DO NOTHING;

-- Refresh the materialised view now that we have data
REFRESH MATERIALIZED VIEW emoji_counts;

-- Comments
WITH c1 AS (
  INSERT INTO comments (commentid, photoid, author_userid, comment_text, created_at)
  VALUES (
    'cccccccc-0000-0000-0000-000000000001',
    'aaaaaaaa-0000-0000-0000-000000000001',
    '11111111-0000-0000-0000-000000000002',
    'This is absolutely stunning, Ansel! I was at Rainier last fall and the light was nothing like this. How long did you hike to get to this vantage point?',
    '2024-08-15T18:32:00Z'
  ) ON CONFLICT (commentid) DO NOTHING
  RETURNING commentid
)
INSERT INTO comments (photoid, parent_commentid, author_userid, comment_text, created_at)
SELECT
  'aaaaaaaa-0000-0000-0000-000000000001', c1.commentid,
  '11111111-0000-0000-0000-000000000001',
  'Thanks Lena! About a 6-hour round trip from the Paradise trailhead. Started at 4am.',
  '2024-08-15T19:00:00Z'
FROM c1
ON CONFLICT DO NOTHING;

INSERT INTO comments (commentid, photoid, author_userid, comment_text, created_at) VALUES
  (
    'cccccccc-0000-0000-0000-000000000002',
    'aaaaaaaa-0000-0000-0000-000000000001',
    '11111111-0000-0000-0000-000000000003',
    'The color grading here is immaculate. Did you do much post-processing or is this mostly SOOC?',
    '2024-08-15T20:14:00Z'
  ),
  (
    'cccccccc-0000-0000-0000-000000000003',
    'aaaaaaaa-0000-0000-0000-000000000001',
    '11111111-0000-0000-0000-000000000004',
    'Added to my inspiration board immediately. Goals.',
    '2024-08-16T07:05:00Z'
  )
ON CONFLICT (commentid) DO NOTHING;

COMMIT;
