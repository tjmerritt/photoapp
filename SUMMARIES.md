# Conversation Summaries

---

## Session 1 — Phase 1: Permissions System & Audit Fixes

### What was done

**Permissions model** (`internal/permissions/permissions.go`):
- Defined all permission constants: Gallery, Display, Photo, Label, Emoji, Comment, and Admin groups
- Added `PermPrivatePhotoView` replacing `authorized_non_public`
- Added `PermEmojiUpload` for emoji type upload gating
- Added `PermPhotoDescriptionModify` for non-owner photo title/description edits
- `Checker.Check` evaluates entity × resource × permission via a single SQL query traversing the ownership chain (Global → Exhibition → Gallery → Display, and Global → Exhibition → Photo)

**DB migrations**:
- `012_permissions.sql`: `teams`, `team_members`, `roles`, `role_permissions`, `entity_role_grants` tables
- `013_remove_authorized_non_public.sql`: drops `authorized_non_public` column from `users`

**Seed script** (`scripts/seed-exhibition.sh`):
- Creates three default roles: Viewer (read-only), Contributor (all write actions), Admin (full access)
- Grants Viewer to Public, Contributor to LoggedIn, Admin to an "Admins" team seeded with the initial user
- Admin role includes: `PrivatePhotoView`, `EmojiUpload`, `PhotoDescriptionModify`, `LabelAdmin`, `EmojiAdmin`, `GalleryAdmin`, `UserAdmin`, `Admin`

**authorized_non_public removal** — replaced in:
- `middleware.go`: removed ctx key, `UserFlags.AuthorizedNonPublic`, and `AuthorizedNonPublic()` accessor
- `auth.go` (`LookupUserFlags`): query now selects only `username`
- `photo.go`, `search.go`, `admin.go`: all guards replaced with `checker.Check(..., PermPrivatePhotoView)` or `PermAdmin`

**`internal/handlers/resolve.go`** (new file):
- `syncMap[K, V]` — generic goroutine-safe map using `sync.RWMutex` (mirrors proposed `sync/v2` API without the unstable dependency)
- `resolvePhotoExhibition` — caches photoid → exhibitionid permanently (immutable relationship); doubles as a photo-exists check (returns `pgx.ErrNoRows` if missing)
- `resolveDisplayGallery` — caches displayid → galleryid (same pattern)

**AUDIT.md findings resolved**:

| Finding | Endpoint | Fix |
|---|---|---|
| F1 | `GET /photo` | `PermPrivatePhotoView` replaces `authorized_non_public` |
| F2 | `GET /labels`, `GET /emojis`, `GET /comments` | Added `PhotoLabelView` / `PhotoEmojiView` / `PhotoCommentView` checks |
| F3 | `POST /labels` | `resolvePhotoExhibition` + `PhotoLabelCreate` scoped to photo |
| F4 | `PATCH/DELETE /labels/:id` | `PermLabelAdmin` override after ownership check |
| F5 | `POST/DELETE /emoji/react` | `PhotoEmojiCreate` / `PhotoEmojiDelete` scoped to photo |
| F6 | `POST /emoji/types` | `PermEmojiUpload` check |
| F7 | `GET /emoji/users` | `PhotoEmojiView` check |
| F8 | `POST/PATCH/DELETE /comments` | `PhotoCommentCreate` on create; `PermAdmin` override on edit/delete |
| F9 | `GET /search` | `PermPrivatePhotoView` replaces `authorized_non_public` |
| F10 | `GET/PATCH /admin/*` | `PermAdmin` replaces `authorized_non_public` |
| F11 | `PATCH /photo` | `PermPhotoDescriptionModify` override after ownership check |

All handlers (`PhotoHandler`, `PatchPhotoHandler`, `SearchHandler`, `AdminHandler`, `LabelsHandler`, `EmojisHandler`, `CommentsHandler`) have `Checker *permissions.Checker` wired in via `router.go`.

---

## Session 2 — Phase 2 (2a–2d): Gallery & Display Backend

### What was done

**Migration `014_galleries_displays.sql`**:
- `display_templates` — global (not exhibition-scoped), `name UNIQUE`, `photo_count`, `slot_positions JSONB`, `presentation JSONB`, soft-delete
- `galleries` — `(galleryid, exhibitionid, title, sort_order)`, indexed on `(exhibitionid, sort_order)`, soft-delete
- `placard_defaults` — 1:1 with gallery (`galleryid PRIMARY KEY`), `defaults JSONB DEFAULT '{}'`
- `displays` — `(displayid, galleryid, templateid nullable FK, sort_order)`, soft-delete; photo FK on slots uses `ON DELETE SET NULL`
- `display_slots` — `(slotid, displayid, slot_index, photoid nullable, rich_text, placard JSONB)`, UNIQUE on `(displayid, slot_index)`
- `updated_at` triggers on all four mutable tables reuse `trg_set_updated_at()` from migration 001

**`internal/models/models.go`** — new types:
- Response: `GallerySummary`, `GalleryDetail`, `GalleriesResponse`, `TemplateSummary`, `DisplaySummary`, `DisplayDetail`, `DisplaySlot`, `SlotPhoto`, `DisplayTemplate`, `TemplatesResponse`
- Request: `CreateGalleryRequest`, `UpdateGalleryRequest` (with `DisplayOrder []string` for reordering), `CreateDisplayRequest`, `UpdateDisplayRequest`, `SlotUpdate`, `CreateTemplateRequest`, `UpdateTemplateRequest`
- JSONB fields use `json.RawMessage`; nil = absent/no-change; `[]byte("null")` = explicit null treated as no-change

**`internal/handlers/galleries.go`** (`GalleriesHandler`):
- `List`: paginated, counts live displays via LEFT JOIN; checks `GalleryView` at exhibition level
- `Create`: `GalleryCreate`; returns `GallerySummary`
- `Get`: full detail with placard defaults (LEFT JOIN `placard_defaults`) and display summaries (slot count + filled count); checks `GalleryView` scoped to gallery
- `Update`: `GalleryModify`; handles title, sort_order, placard_defaults upsert, and display reordering; delegates response to `Get`
- `Delete`: `GalleryDelete`; soft-delete with exhibition cross-check

**`internal/handlers/displays.go`** (`DisplaysHandler`):
- `Get`: full detail with slots + joined photo data; `resolveDisplayGallery` for gallery scope in `DisplayView` check
- `Create` (`POST /api/v1/galleries/:galleryid/displays`): `DisplayCreate` at gallery scope; auto-populates empty slots from template's `photo_count` (`ON CONFLICT DO NOTHING`)
- `Update`: `DisplayModify`; handles templateid change (pre-populates new slots), sort_order, and slot upserts via `INSERT … ON CONFLICT (displayid, slot_index) DO UPDATE`; `SlotUpdate.PhotoID ""` maps to NULL via `NULLIF($n,'')::uuid`
- `Delete`: `DisplayDelete`; cross-joins galleries to verify exhibition ownership before soft-delete

**`internal/handlers/templates.go`** (`TemplatesHandler`):
- `List`: no auth, no permission check — templates are public metadata
- `Create`, `Update`, `Delete`: all require `PermAdmin` on the current exhibition; `Update` fetches current values first and applies partial changes

**`internal/handlers/resolve.go`** — added:
- `displayGalleryCache syncMap[string, string]`
- `resolveDisplayGallery(ctx, pool, displayid) (galleryid, error)` — same caching pattern as `resolvePhotoExhibition`

**`internal/handlers/router.go`** — 12 new routes:

| Method | Path | Handler | Auth |
|---|---|---|---|
| GET | `/api/v1/galleries` | `galleries.List` | — |
| POST | `/api/v1/galleries` | `galleries.Create` | ✓ |
| GET | `/api/v1/galleries/:galleryid` | `galleries.Get` | — |
| PATCH | `/api/v1/galleries/:galleryid` | `galleries.Update` | ✓ |
| DELETE | `/api/v1/galleries/:galleryid` | `galleries.Delete` | ✓ |
| GET | `/api/v1/displays/:displayid` | `displays.Get` | — |
| POST | `/api/v1/galleries/:galleryid/displays` | `displays.Create` | ✓ |
| PATCH | `/api/v1/displays/:displayid` | `displays.Update` | ✓ |
| DELETE | `/api/v1/displays/:displayid` | `displays.Delete` | ✓ |
| GET | `/api/v1/display-templates` | `templates.List` | — |
| POST | `/api/v1/display-templates` | `templates.Create` | ✓ |
| PATCH | `/api/v1/display-templates/:templateid` | `templates.Update` | ✓ |
| DELETE | `/api/v1/display-templates/:templateid` | `templates.Delete` | ✓ |

### Open items
- Phase 2e–2g: ✅ complete (see Session 5)
- Phase 3: photo wall ✅ (see Session 3)
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 3 — Phase 3: Photo Wall

### What was done

**`GET /api/v1/photos` endpoint** (new):
- `ListPhotosHandler{DB, Checker}` in `internal/handlers/photo.go`
- Accepts `?limit=N&offset=N` (limit clamped 1–100, default 40)
- Uses `COUNT(*) OVER()` window function for total in a single query
- Respects `PermPrivatePhotoView` and exhibition scope
- Returns `PhotoListResponse{Total, Offset, Limit, Photos: []PhotoListItem{PhotoID, ImageURL, Width, Height}}`
- Route: `GET /api/v1/photos` added to `router.go`

**`internal/models/models.go`**: new types `PhotoListItem`, `PhotoListResponse`

**`app/photo.html`** (new):
- Renamed copy of the old `app/index.html` photo viewer
- Only change: hamburger "Photos" link `href="#"` → `href="/"`

**`app/app.js`** (extended):
- `doSearch` `clickurl` updated to `/photo.html?photoid=...`
- Added `packRows(photos, containerWidth)` — justified row-layout algorithm:
  - n ∈ {2, 3, 4} per row; no consecutive rows share the same n
  - n=4 only when ≥2 portrait photos or max/min aspect ratio < 1.5
  - Photos in a row scaled to equal height, widths summed → rowScale fills container
  - Last partial row: capped at TARGET_ROW_H=240px (not stretched)
  - All rows capped at MAX_ROW_H=400px; 4px gap between photos
  - Returns `[{startIndex, photos:[{...displayWidth, displayHeight}]}]`
- Added `wallApp()` Alpine component:
  - Auth/navbar state mirrors `photoApp`
  - `init()`: awaits `$nextTick` to read `wall.clientWidth`, sets up `ResizeObserver` + `IntersectionObserver` (rootMargin 600px)
  - `loadMore()`: fetches `/api/v1/photos`, appends batch, re-packs rows

**`app/index.html`** (new wall page):
- Root: `wallApp` Alpine component
- Navbar: hamburger, logo, user switcher, settings, auth panel (search/random removed)
- `x-ref="wall"` on always-visible wrapper so `clientWidth` is always valid
- Photos link to `/photo.html?photoid=<id>`
- Infinite-scroll sentinel + spinner states + "All photos loaded" message

### Open items
- Phase 2e–2g: frontend gallery pages, display editor, gallery permissions UI
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 4 — Photo Wall Bug Fixes

### What was done

**Fix: row overflow (horizontal scroll)**

`packRows` was rewritten to output `flexGrow` + row `height` instead of explicit `displayWidth`/`displayHeight` per photo:
- Each row div gets `height:${row.height}px` via inline style
- Each photo gets `flex:${p.flexGrow} 1 0px` — CSS distributes widths proportionally, always summing to container width regardless of measurement timing
- `.wall-row` gains `width:100%; overflow:hidden`; `.wall-photo` loses `flex-shrink:0`, gains `min-width:0`
- `containerWidth` now only affects row height, not horizontal correctness
- Added `MIN_ROW_H=80px` floor, `|| 1` guards for zero-dimension photos, and `TARGET_ROW_H` fallback when `containerWidth=0`

**Fix: last-row reflow on infinite scroll**

When `hasMore` is true, the last (partial) row is hidden so it doesn't jump as the next batch fills it out:
- `x-for="(row, idx) in rows"` with `x-show="!hasMore || idx < rows.length - 1"` on the row div
- `x-show` sets `display:none` → zero height → no gap; on next batch the row becomes interior and appears instantly

**Fix: photo click navigation**

Several Alpine CSP build constraints were discovered and worked around:

| Approach | Result |
|---|---|
| `:href="\`/photo.html?photoid=${p.photoid}\`"` | No `href` set — CSP build blocks template literals in `:href` |
| `:href="photoUrl(p.photoid)"` (component method) | No `href` set — CSP build blocks method calls in `:href` |
| `:href="p.url"` (pre-computed string on photo object) | No `href` set — CSP build blocks all dynamic `:href` in nested `x-for` |
| `x-init="$el.href = ..."` | CSP evaluator doesn't support assignment expressions |
| `@click="window.location.href = p.url"` | CSP evaluator blocks `window` access in event expressions |
| `@click="navigate(p.url)"` (component method calling `window.location.href`) | Works but user found a better fix |
| `:href="'/photo.html?photoid=' + p.photoid"` + `target="_blank"` | **Works** — string concatenation IS supported in `:href`; `target="_blank"` opens in new tab |

**Final solution** in `app/index.html`:
```html
<a :href="'/photo.html?photoid=' + p.photoid"
   target="_blank"
   class="wall-photo"
   :style="`flex:${p.flexGrow} 1 0px`">
```

**Alpine CSP build rules learned:**
- Template literals in `:href` → blocked
- Method calls in `:href` → blocked
- String concatenation in `:href` → works
- `window.*` access in `@click` expressions → blocked (must wrap in a component method)
- `x-init` expressions → read-only; assignment statements silently fail
- `target="_blank"` must be a static attribute (not bound)

### Open items
- Phase 2e–2g: frontend gallery pages, display editor, gallery permissions UI
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 5 — Phase 2e–2g: Gallery Frontend

### What was done

**`app/app.js`** — four new Alpine components added and registered:

- **`galleriesNav()`** — nested component for the hamburger menu. Loads `GET /api/v1/galleries?limit=100` on init; sets `galleriesHref` as a plain data property (not getter, so Alpine tracks it). 0 galleries → grayed-out; 1 gallery → `galleries.html?galleryid=xxx`; N → `galleries.html` with sub-items per gallery.
- **`galleriesApp()`** — root for `galleries.html`. Detects `?galleryid=` in URL and redirects to first display of that gallery. Single-gallery auto-redirect. Otherwise renders gallery card grid.
- **`displayApp()`** — root for `display.html`. Fetches display → galleryid, then gallery → ordered display list. `prevDisplayId`/`nextDisplayId` are plain data properties updated after load. `gridStyle` precomputed from slot count (1→1col, 2→2col, 3→3col, 4→2col, 5-6→3col, 7+→4col). Navigation via `goToPrev()`/`goToNext()` methods setting `window.location.href` (safe from CSP inside methods).
- **`galleryAdminApp()`** — root for `gallery-admin.html`. CRUD: create gallery, rename (in-place edit), delete, expand displays panel per gallery, add/remove/reorder displays. `display_order` array sent in PATCH body. Immediate local swap for reorder with server-side reload on failure.

**Navbar updates** (`app/index.html`, `app/photo.html`):
- Dropdown width `w-44` → `w-52`
- Nested `x-data="galleriesNav"` block: loading state, grayed state, active link, per-gallery sub-items
- Gallery Admin link added
- All `:href` use string concatenation — no template literals, no method calls

**`app/galleries.html`** (new):
- Responsive card grid (1/2/3 cols)
- `@click="navigateToGallery(g.galleryid)"` fetches first display and navigates
- Handles `?galleryid=` redirect mode for nav sub-items
- Empty state with link to Gallery Admin

**`app/display.html`** (new) — museum exhibit board:
- Board: `background:#f0ead6`, drop shadow, border-radius
- Slots in CSS grid with `gridStyle` `:style` binding
- Each slot: `photo-frame` (4:3 aspect-ratio box, `object-fit:contain`) + `slot-placard` (cream card, serif font for captions)
- `x-if` (not `x-show`) guards `slot.photo` to avoid null dereference on image bindings
- Breadcrumb: Galleries > Gallery Name > Display N of M
- Prev/Next buttons (disabled at ends) at top and bottom
- Empty display message when no slots exist

**`app/gallery-admin.html`** (new):
- Create gallery form; toasts for all async feedback
- Expand/collapse per-gallery display list
- In-place rename (input + Save/Cancel)
- Up/Down arrows for reorder; delete with confirm()
- Add Display creates blank display; View button links to display.html?displayid=xxx

### Alpine CSP constraints respected
- `:href` uses only string concatenation (`'/path/' + property`)
- No template literals in any binding attribute
- `window.location.href` only accessed inside component methods
- `x-if` used for conditional rendering that dereferences optional properties

### Open items
- Display slot editing (assign photos to slots, add placard text) — Phase 2 followup
- Display template selection in admin UI
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads
