# PhotoApp

A photo viewer web application built with plain HTML, Alpine.js, and Tailwind CSS, backed by a mock Node.js API server.

## Quick Start

```bash
cd /Users/tjmerritt/src/github.com/tjmerritt/photoapp
node server.js
# Open http://localhost:3000/?photoid=photo001
```

No npm install required — the server uses only Node built-ins (`http`, `fs`, `path`, `url`).

## File Structure

```
photoapp/
├── index.html      # Main photo viewer page
├── server.js       # Mock API server (Node, no dependencies)
├── package.json
└── README.md
```

## Tech Stack

- **Layout**: Plain HTML5 semantic markup
- **Interactivity**: Alpine.js 3.x (CDN)
- **Styling**: Tailwind CSS 3.x (CDN)
- **Server**: Node.js built-in `http` module (no Express needed)

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/photo?photoid=` | Full photo object |
| GET | `/api/v1/labels?photoid=&offset=&limit=` | Paginated labels |
| GET | `/api/v1/emojis?photoid=&offset=&limit=` | Paginated emojis |
| GET | `/api/v1/emoji/users?emoji=&offset=&limit=` | Users for an emoji |
| GET | `/api/v1/comments?photoid=&offset=&limit=` | Top-level comments |
| GET | `/api/v1/comments?photoid=&parentid=&offset=&limit=` | Replies to a comment |
| GET | `/api/v1/user?userid=` | User profile |

Static files (index.html) are served from the project root.

---

## Notes on Missing / Ambiguous API Details

The following items were noticed during implementation and are worth resolving when write APIs are added:

### Structural Issues in the Spec

1. **`emojis` field is an object `{}` not an array `[]`** in the photo response spec, but the contents imply it should be an array. The mock server treats it as an array.

2. **`emojis` pagination response** has a malformed array — the array items start with `"emojiid":` as if the `{` opening the object was omitted. Assumed each item is a full object `{ emojiid, imageurl, count, users, usersurl }`.

3. **`comments` pagination response** is missing `"current"` in the `pages` sub-object (present in the `/api/v1/labels` response but absent from the `/api/v1/comments` response).

4. **`date` field** in comment objects is missing its closing `"` in the spec: `"date": "YYYY-MM-DDTHH:NN:SS.mmm"` — the milliseconds separator is `.` not `:`, which is non-standard. ISO 8601 uses `.` for sub-seconds which is fine, but `NN` for minutes is unconventional (usually `MM` — ambiguous with months). Clarify intended format.

### Missing Data

5. **Author thumbnail (`tn`) in comments**: The comment `author` object only has `userid` and `username`. There is no `tn` (thumbnail) field. The UI needs an avatar; either add `tn` to the author object in the comment, or the client must call `/api/v1/user?userid=` for every comment author — expensive. **Recommend adding `tn` to the comment author object.**

6. **Emoji unicode/character field**: The `emojis` array has `imageurl` but no unicode character field. If using standard emoji (❤️, 🔥, etc.), an `emoji` character field (or `codepoint`) is needed so the browser can render them without loading images. If all emojis are custom images, `imageurl` alone is fine but an `alttext` field would help accessibility.

7. **`labelsurl` in photo response**: Described as the URL for additional labels "can be omitted, ommitted [sic] = no more than in labels." It's unclear whether "omitted" means there are no more labels, or just that the field itself is optional. Suggest: omit the field (or set to `null`) when all labels are already included in the `labels` array.

### Missing Write APIs (noted for future addition)

8. **Add label**: `POST /api/v1/labels?photoid=` — with `{ name, value }` body.
9. **Add emoji reaction**: `POST /api/v1/emoji/react?photoid=&emoji=` — or `POST /api/v1/emojis?photoid=`.
10. **Remove emoji reaction**: `DELETE /api/v1/emoji/react?photoid=&emojiid=`.
11. **Post comment**: `POST /api/v1/comments?photoid=` — with `{ comment }` body.
12. **Post reply**: `POST /api/v1/comments?photoid=&parentid=` — with `{ comment }` body.
13. **Edit title/description**: `PATCH /api/v1/photo?photoid=` — only when `canedit: true`.
14. **Authentication**: `/api/v1/auth/login` (POST), `/api/v1/auth/logout` (POST), session/token management.
15. **User registration**: Not specified.

### UX / Behavioral Gaps

16. **Search API**: No `/api/v1/search` endpoint described at all. The search box is present in the UI.
17. **Pagination UI**: The API provides `pages` objects with `next`/`prev` URLs, but there is no "Load more" button or infinite scroll mechanism specified for labels, emojis, or comments on the page. The mock data is small enough to fit in the initial response, but this will matter at scale.
18. **`canedit` flag**: The title object has `canedit: false` but no editing UI is specified. Presumably an edit pencil should appear when `canedit: true`.
19. **Photo ownership / permissions**: No field indicating whether the current user owns the photo, which would control showing "delete", "edit" controls.
