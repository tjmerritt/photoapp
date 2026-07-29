import { describe, it, expect, beforeEach } from 'vitest';
import { thumbUrl, sortTemplates, avatarSrc, labelColorFor } from '../app.js';

describe('thumbUrl', () => {
  beforeEach(() => {
    // thumbUrl reads window.devicePixelRatio; app.js is normally loaded in a
    // browser where `window` always exists, so it's stubbed here for the
    // Node test environment.
    globalThis.window = { devicePixelRatio: 2 };
  });

  it('returns non-imgproxy URLs unchanged', () => {
    expect(thumbUrl('/uploads/foo.png', 200)).toBe('/uploads/foo.png');
    expect(thumbUrl('', 200)).toBe('');
    expect(thumbUrl(null, 200)).toBe(null);
  });

  it('appends a pixel-width hint scaled by devicePixelRatio for imgproxy URLs', () => {
    expect(thumbUrl('/api/v1/imgproxy?src=x', 300)).toBe('/api/v1/imgproxy?src=x&w=600');
  });

  it('rounds the computed width', () => {
    globalThis.window = { devicePixelRatio: 1.5 };
    expect(thumbUrl('/api/v1/imgproxy?src=x', 101)).toBe('/api/v1/imgproxy?src=x&w=152'); // round(151.5) = 152
  });

  it('defaults devicePixelRatio to 1 when unset', () => {
    globalThis.window = {};
    expect(thumbUrl('/api/v1/imgproxy?src=x', 300)).toBe('/api/v1/imgproxy?src=x&w=300');
  });
});

describe('sortTemplates', () => {
  it('sorts by photo_count ascending, then name alphabetically', () => {
    const input = [
      { name: 'Triptych', photo_count: 3 },
      { name: 'Single', photo_count: 1 },
      { name: 'Pair B', photo_count: 2 },
      { name: 'Pair A', photo_count: 2 },
    ];
    expect(sortTemplates(input).map(t => t.name)).toEqual(['Single', 'Pair A', 'Pair B', 'Triptych']);
  });

  it('does not mutate the input array', () => {
    const input = [{ name: 'B', photo_count: 2 }, { name: 'A', photo_count: 1 }];
    const before = [...input];
    sortTemplates(input);
    expect(input).toEqual(before);
  });

  it('treats a missing or null list as empty', () => {
    expect(sortTemplates(null)).toEqual([]);
    expect(sortTemplates(undefined)).toEqual([]);
  });
});

describe('avatarSrc', () => {
  it('returns the profileImage when present', () => {
    expect(avatarSrc({ profileImage: 'https://example.invalid/a.png' })).toBe('https://example.invalid/a.png');
  });

  it('returns an empty string for a null user or a user with no profileImage', () => {
    expect(avatarSrc(null)).toBe('');
    expect(avatarSrc({})).toBe('');
  });
});

describe('labelColorFor', () => {
  it('is deterministic for the same name', () => {
    expect(labelColorFor('Location')).toBe(labelColorFor('Location'));
  });

  it('returns a non-empty color string for any name', () => {
    expect(labelColorFor('Anything')).toMatch(/^hsl\(/);
  });

  it('can return different colors for different names', () => {
    expect(labelColorFor('Location')).not.toBe(labelColorFor('Photographer'));
  });
});
