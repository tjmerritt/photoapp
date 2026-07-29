import { describe, it, expect } from 'vitest';
import { packRows } from '../app.js';

// packRows is the justified row-layout algorithm for the photo wall
// (app/app.js, "packRows — justified row-layout algorithm"). These tests
// pin down its documented contract: row-count selection from {2,3,4},
// portrait/aspect-ratio driven choice of 4-per-row, and the width/height
// math used to render each row.

function photo(id, width, height) {
  return { photoid: id, imageurl: `https://example.invalid/${id}.jpg`, width, height };
}

describe('packRows', () => {
  it('returns an empty array for no photos', () => {
    expect(packRows([], 1000)).toEqual([]);
  });

  it('never repeats the row size used by the previous row', () => {
    // 12 same-aspect-ratio (square) photos: "similar" is always true, so a
    // window of 4 is always eligible when it isn't excluded by prevN.
    const photos = Array.from({ length: 12 }, (_, i) => photo(i, 100, 100));
    const rows = packRows(photos, 1000);

    const sizes = rows.map(r => r.photos.length);
    expect(sizes).toEqual([4, 3, 4, 1]);
    expect(sizes.reduce((a, b) => a + b, 0)).toBe(12);

    for (let i = 1; i < sizes.length; i++) {
      expect(sizes[i]).not.toBe(sizes[i - 1]);
    }
  });

  it('picks n=4 when at least 2 of the candidate window are portrait', () => {
    // 4 photos, 2 portrait (height > width) — should trigger n=4 over n=3.
    const photos = [
      photo('a', 100, 200), // portrait
      photo('b', 100, 200), // portrait
      photo('c', 200, 100),
      photo('d', 200, 100),
    ];
    const rows = packRows(photos, 1000);
    expect(rows).toHaveLength(1);
    expect(rows[0].photos).toHaveLength(4);
  });

  it('falls back to n=3 when the 4-window is neither similar nor 2+ portrait', () => {
    // Wildly different aspect ratios, 0 portraits among the first 4 of 7 —
    // n=4 is disqualified, so it should fall back to 3.
    const photos = [
      photo('a', 1000, 100), // very wide
      photo('b', 100, 100),
      photo('c', 100, 100),
      photo('d', 100, 100),
      photo('e', 100, 100),
      photo('f', 100, 100),
      photo('g', 100, 100),
    ];
    const rows = packRows(photos, 1000);
    expect(rows[0].photos).toHaveLength(3);
  });

  it('computes flexGrow, widthPx, and height for a full stretched row', () => {
    const photos = [photo('a', 1000, 500), photo('b', 500, 500)];
    const rows = packRows(photos, 1000);

    expect(rows).toHaveLength(1);
    const row = rows[0];
    expect(row.stretch).toBe(true);
    expect(row.height).toBe(332); // round(500 * (1000-4)/1500)

    expect(row.photos[0].flexGrow).toBe(1000);
    expect(row.photos[1].flexGrow).toBe(500);
    expect(row.photos[0].widthPx).toBe(664);
    expect(row.photos[1].widthPx).toBe(332);
    expect(row.photos[0].displayWidth).toBe(664);
    expect(row.photos[1].displayWidth).toBe(332);
  });

  it('does not stretch rows when containerWidth is 0 (not yet measured)', () => {
    const photos = [photo('a', 1000, 500), photo('b', 500, 500)];
    const rows = packRows(photos, 0);

    expect(rows[0].stretch).toBe(false);
    expect(rows[0].height).toBe(240); // TARGET_ROW_H
  });

  it('treats every row length chosen from {2,3,4} (or a forced remainder) as full, never "partial"', () => {
    // Documents current behavior: since n is always chosen to be <= the
    // number of photos remaining (see the `x <= remaining` filter and the
    // `n = remaining` fallback), `rowPhotos.length < n` can never be true —
    // packRows never actually produces a `stretch: false` row via the
    // "trailing partial row" path described in its own doc comment; the
    // only way to get stretch: false today is containerWidth <= 0 (see the
    // test above). If that's surprising, it's because it's surprising —
    // this test exists to catch it changing silently either way.
    const photos = [photo('a', 100, 100)];
    const rows = packRows(photos, 1000);
    expect(rows[0].stretch).toBe(true);
  });
});
