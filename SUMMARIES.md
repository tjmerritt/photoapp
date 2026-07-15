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

---

## Session 6 — Template Editor, Photo Picker, Display View/Edit Split, Matte/Frame/Placard Styling

### What was done

**Template Admin page** (`app/template-admin.html`, new):
- Simple CRUD editor for `display_templates`: create (name + photo_count), list, expand-to-edit, delete
- Edit form: name, photo_count, "Regenerate layout" button (even grid via `defaultSlotPositions(n)`), raw JSON textareas for `slot_positions` and `presentation`, live grid preview
- `templateAdminApp()` Alpine component added to `app/app.js`; nav link added across all pages

**Display template selection in Gallery Admin** (`app/gallery-admin.html`, `app/app.js`):
- `galleryAdminApp()` extended with `templates`/`loadTemplates()`, a template picker on "Add Display", and a per-row `<select>` (`setDisplayTemplate`) to change an existing display's template
- Bug fix: the per-row `<select>` used `:value` bound to `x-for`-generated `<option>`s, which doesn't reliably re-sync in Alpine's CSP build after a selection; switched to `x-model="d.selectedTemplateId"` on a plain reactive field, with revert-on-PATCH-failure

**Photo picker for display slots** (`app/app.js`, `app/display-edit.html`):
- `SearchResult.Title` / `search.go` updated to return `p.title_text` so search results show photo titles
- `displayApp()`'s picker logic (`pickerOpen`, `openPicker`, `onPickerInput`, `runPickerSearch`, `selectPickerPhoto`, `clearPickerPhoto`, `saveSlotPhoto`) added; modal shows a search box + thumbnail results
- Backend gotcha documented: the slot upsert in `displays.go` (`PATCH /displays/:id`) does `ON CONFLICT DO UPDATE SET photoid=EXCLUDED.photoid, rich_text=EXCLUDED.rich_text, placard=EXCLUDED.placard` — omitting `rich_text`/`placard` from a slot PATCH wipes them to NULL, so every slot save must resend current values

**View/edit page split**:
- `app/display.html` rewritten as a minimal public viewer: navbar only, no breadcrumb/controls, just the photo grid + edge-hover prev/next arrows (tall narrow arrow zones on the far left/right that fade in on hover, `edge-nav`/`edge-arrow` CSS)
- `app/display-edit.html` (new): full editing experience — breadcrumb, "Display X of Y", top/bottom Prev/Next, "View live" link, photo picker modal, "Add/Change photo" overlay button per slot
- `app/gallery-admin.html` display rows now link to both (eye icon → view, pencil icon → edit)
- `app/app.js` split into `displayApp()` (view-only) and `displayEditApp()` (editing), sharing helper methods

**Matte / frame / placard presentation system** (`display_templates.presentation` JSONB, frontend-owned schema):
- Removed the old fixed photo border; added configurable matte (`color`, `width`, per-side widths, `enabled`) and frame (`color`, `width`, `enabled`) around each photo, driven by shared helpers in `app.js` (`frameOuterStyle`, `matteInnerStyle`, `normalizeSideWidths`)
- Removed the yellow `.board` background/border/shadow wrapper entirely — framing now comes only from each photo's own matte/frame
- Made placard **position** configurable (`presentation.placard.position`: top/bottom/left/right) via `slotCardDirectionStyle()`, applied to `.slot-card`
- Made placard **content** configurable: `presentation.placard.fields` is a list of `{source: "title"|"rich_text", typography: {...}}`; added `photo.title` to the API response (`SlotPhoto.Title`, `displays.go` slot query) so the `title` source works; `normalizePlacardConfig`, `typographyStyle`, `placardFieldSourceValue`, `placardFieldsFor`, `placardBoxStyle` helpers added; empty fields are skipped rather than shown blank
- Templates with no `presentation` set (or an empty one) fall back to the original single-caption look — fully backward compatible
- Template Admin's `defaultPresentation()` and its documentation text updated to reflect the fuller schema; new templates now start pre-filled with a title+caption example
- User explicitly deferred building a non-JSON config UI for `presentation` ("annoying, but we can work on that later") — raw JSON textarea remains the only editor for now

**Template ordering**:
- `GET /api/v1/display-templates` (`templates.go`) now returns `ORDER BY photo_count, name` instead of just `name`, so every template picker (Template Admin list, Gallery Admin's "Add Display" and per-row selectors) is ordered smallest-photo-count-first, then alphabetically
- Added `sortTemplates()` helper in `app.js`; applied after create/save in `templateAdminApp()` so the in-memory list stays correctly ordered without a full reload

### Testing notes
- No Go toolchain, Postgres, or sudo in the sandbox — Go changes (`models.go`, `search.go`, `displays.go`, `templates.go`) verified only by manual diff review, not compiled or run
- Frontend verified via a JSDOM-based headless-DOM harness serving the real files over a local HTTP server with `window.fetch` mocked, proving actual Alpine reactivity against the literal files on disk

### Deployment note (standing instruction from user)
- Static assets (`app/*.html`, `app/app.js`) take effect immediately — the Go server reads them fresh from disk per request
- Go source changes (`internal/**/*.go`) require `make build` + a server restart before they take effect; each Go-touching change in this session was flagged as such

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard) in Template Admin — explicitly deferred by user
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 7 — Display View Wasn't Following Template Layout

### What was done

**Bug**: the Template Admin preview pane correctly rendered a template's `slot_positions` (per-slot `x/y/w/h` percentages), but the actual view/edit pages (`display.html`, `display-edit.html`) ignored it — they computed a generic N-column CSS grid purely from slot count, so any custom layout designed in Template Admin never showed up on the real display.

**Root cause**: `TemplateSummary` (the compact template shape embedded in `DisplayDetail.Template`) never included `slot_positions` — only `presentation` was exposed on `GET /api/v1/displays/:id`. `displayApp()`/`displayEditApp()` had no way to know the template's geometry even if they'd tried to use it.

**Backend** (`internal/models/models.go`, `internal/handlers/displays.go`):
- Added `SlotPositions json.RawMessage` to `TemplateSummary`
- `DisplaysHandler.Get()`: query now selects `t.slot_positions::text` and populates `d.Template.SlotPositions`
- `DisplaysHandler.Create()`: template-summary fetch for a freshly-created display now also includes `slot_positions`/`presentation` for consistency (previously only `templateid/name/photo_count`)

**Frontend** (`app/app.js`):
- Extracted `defaultSlotPositions(n)` (even-grid generator) out of `templateAdminApp()` into a shared top-level function, so the same fallback layout is used both by new templates and by displays with no usable geometry; `templateAdminApp()`'s method now just delegates to it
- Added `normalizeSlotPositions(raw, count)` — uses the template's own `slot_positions` if it's a valid array matching the slot count, else falls back to `defaultSlotPositions(count)` (guards against a template edited after a display was built with a different slot count)
- Added `slotBoxStyle(positions, index)` — returns `position:absolute; left:%; top:%; width:%; height:%;` for one slot, identical geometry model to the Template Admin preview pane
- `displayApp()`/`displayEditApp()`: replaced the old count-based `gridStyle` with a `slotPositions` array computed in `init()` from `display.template.slot_positions`, plus a `slotBoxStyle(i)` delegating method

**Frontend** (`app/display.html`, `app/display-edit.html`):
- `.display-grid` changed from a `display:grid` column layout to `position:relative; aspect-ratio:16/9` — a fixed-shape canvas matching the Template Admin preview exactly
- `.slot-card` is now absolutely positioned per-slot via inline `slotBoxStyle(i) + slotCardDirectionStyle()`
- `.photo-frame`/`.photo-matte` changed from natural-image-size sizing to `flex:1 1 auto` filling whatever space is left after the placard, with the `<img>` switched from `width:100%; height:auto` to `max-width/max-height:100%; object-fit:contain` so photos scale to fit their slot's fixed box instead of dictating it
- `.slot-placard` given `flex:0 0 auto` so it keeps its natural size within the flex column/row
- Removed the empty-slot placeholder's fixed `aspect-ratio:4/3` (it now just fills the flex space like a real photo would)

### Testing notes
- Verified via the same JSDOM headless-DOM harness as prior sessions: custom asymmetric `slot_positions` render with exact matching inline styles; a template with no `slot_positions` falls back to the even grid; a `slot_positions` array whose length doesn't match the display's slot count also falls back (rather than rendering a broken partial layout); `display-edit.html` renders identical geometry with the edit button intact; Template Admin's mini/full preview panes and template creation (which now share `defaultSlotPositions` with the display pages) still work correctly

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard) in Template Admin — explicitly deferred by user
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 8 — Framed Photo Must Keep Its Own Aspect Ratio Within the Slot

### What was done

**Bug**: Session 7 made `.photo-frame` a `flex:1 1 auto` child that stretched to fill its slot's box completely — which honored the slot's *position*, but distorted the frame/matte's own shape to match whatever aspect ratio the slot happened to be. The photo inside used `object-fit:contain`, so the image itself wasn't stretched, but the frame/matte around it was, which looked wrong.

**Fix** (`app/app.js`, `app/display.html`, `app/display-edit.html`): the framed photo (frame + matte + image) now always keeps its own aspect ratio — derived from the photo's raw pixel width/height — and is fit ("contain"-style) within the slot's available space rather than stretched to fill it. Any leftover space in the slot renders as the plain page background, never a fill of its own.

- Added `presentation.align: { horizontal: "left"|"center"|"right", vertical: "top"|"center"|"bottom" }` (default center/center) — controls which side the leftover space collects on. New helpers in `app.js`: `normalizeAlign()`, `photoAreaStyle()` (maps align to `justify-content`/`align-items` on a new wrapper), `photoFrameSizeStyle(slot)` (sets `aspect-ratio: photoW / photoH; max-width:100%; max-height:100%` on the frame, falling back to 4:3 for empty slots)
- Markup: added a `.photo-area` wrapper around `.photo-frame` in both display pages — `.photo-area` is the flexible region (`flex:1 1 auto`, background `var(--surface)`) that gives the framed photo somewhere to be aligned within; `.photo-frame` no longer stretches (dropped `flex:1 1 auto`), instead sized via the new aspect-ratio + max-width/height "contain" technique
- `.slot-card` and `.photo-area` both explicitly set `background: var(--surface)` so unfilled space is guaranteed to be the page background regardless of what's stacked underneath
- Template Admin: `defaultPresentation()` now seeds `align: { horizontal: 'center', vertical: 'center' }`; documentation paragraph updated to explain the aspect-ratio-preserving behavior and the new `align` field
- Matte/frame padding (a few px) is ignored when computing the frame's aspect ratio — negligible at real photo sizes; `object-fit:contain` on the `<img>` absorbs any tiny remaining mismatch

### Testing notes
- Verified via the JSDOM harness: a portrait photo (1:2) centers within a wide slot without being stretched; `align: {left, top}` on a wide photo (4:1) correctly maps to `justify-content:flex-start; align-items:flex-start`; an empty slot with no photo falls back to a 4:3 frame aspect ratio; `slot-card` background resolves to `var(--surface)`; `display-edit.html` renders identical geometry with the edit button still anchored correctly to the photo's own corner

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard/align) in Template Admin — explicitly deferred by user
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 9 — Matte Width Wasn't Actually Uniform on All Sides

### What was done

**Bug**: with matte width set to the same px value on every side (e.g. 20), the rendered gap around the photo was visibly wider left/right than top/bottom (or vice versa, depending on the photo's shape).

**Root cause**: Session 8's `.photo-frame` was sized via `aspect-ratio: photoW / photoH` (the *raw* photo's pixel ratio), then the matte's fixed-px padding was subtracted from that box by ordinary CSS box-model layout. Since the padding is a fixed pixel amount and the frame's ratio didn't account for it, the *content area left for the image* (frame box minus padding) ended up with a slightly different ratio than the photo itself. `object-fit:contain` then letterboxed the image inside that content area on whichever axis had the mismatch — so the CSS padding was technically uniform (always 20px), but the *visible* gap (padding + that extra letterbox sliver) wasn't.

**Fix** (`app/app.js`, `app/display.html`, `app/display-edit.html`): replaced the CSS-only aspect-ratio approximation with an exact, JS-measured pixel size for `.photo-frame`, following the same measure-then-layout pattern `wallApp` already uses (`ResizeObserver` + explicit pixel styles) rather than trying to force this through CSS alone.

- New `computeFrameBoxSize(availW, availH, slot, presentation)` in `app.js`: given the actual measured size of a slot's `.photo-area`, subtracts the matte/frame's exact pixel overhead *first*, fits the photo's true aspect ratio into what's left, then adds the overhead back — so the resulting frame box, once padded/bordered normally, has the image filling its content area exactly with zero internal letterboxing, and the matte is the same width on every side by construction (asymmetric per-side matte widths are still fully respected — verified with a `{top:5,right:20,bottom:5,left:20}` case)
- `photoFrameFallbackStyle(slot)` keeps the old CSS aspect-ratio approximation as a first-paint fallback (used only before the JS layout pass has measured anything, so there's no flash of a collapsed box)
- `displayApp()`/`displayEditApp()`: added `frameSizes` (array of measured `{w,h}` per slot) and `layoutFrames()`, which queries all `.photo-area` elements under a new `x-ref="grid"` on `.display-grid`, measures each with `clientWidth`/`clientHeight`, and calls `computeFrameBoxSize()`. Wired up in `init()` (measure once after the grid renders, then a `ResizeObserver` keeps it correct across window resizes and placard-height reflows) and again after `saveSlotPhoto()` (a newly-assigned photo can have a different aspect ratio even when the slot's own box size on screen doesn't change)
- `photoFrameSizeStyle(i, slot)` now takes the slot index and returns the exact `width:/height:` px from `frameSizes[i]` when available, else the fallback

### Testing notes
- Since JSDOM has no real layout engine (`clientWidth`/`clientHeight` are always 0), added a standalone unit test extracting just `computeFrameBoxSize`/`normalizeSideWidths` and running the actual overhead math against hand-checked numbers (uniform 20px matte, matte+frame combined, asymmetric matte, empty-slot fallback, unmeasured-yet null case) — all passed, including confirming the content box's aspect ratio matches the photo's exactly (no drift) so the matte gap is provably identical on all sides
- Also verified the full pipeline end-to-end by monkey-patching a `.photo-area` element's `clientWidth`/`clientHeight` in the JSDOM harness and calling `layoutFrames()` directly, confirming the applied `.photo-frame` style matches the unit-test numbers exactly
- Re-ran all prior display/template regression suites (matte/frame/placard config, slot_positions layout, alignment, Template Admin preview) — no regressions; JSDOM's lack of real layout means these still exercise the CSS fallback path, which is unchanged from Session 8

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard/align) in Template Admin — explicitly deferred by user
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 10 — Gallery Admin Template Dropdown Defaulted to "No template"

### What was done

**Bug**: expanding a gallery in Gallery Admin, the per-display template `<select>` always showed "No template" even when the display had a template assigned — despite `d.template`/`d.selectedTemplateId` being correctly populated by the backend and by `toggleDisplays()`.

**Root cause**: another manifestation of the Alpine `<select>` + `x-for`-generated `<option>`s fragility documented earlier this project (Session 5's "selector doesn't reflect selection immediately" bug). This time it's the *initial* render, not a later update: Alpine processes a `<select>` element's own directives (`x-model`) before walking into its children, so when a display row is freshly created, `x-model`'s initial effect sets `select.value = d.selectedTemplateId` *before* the nested `<template x-for="t in templates">` has created the matching `<option>`. The browser silently falls back to the first option ("No template") for a value it doesn't yet recognize, and nothing ever re-triggers the `x-model` effect afterward to fix it.

**Fix** (`app/gallery-admin.html`, `app/app.js`): added `x-init="syncTemplateSelect($el, d)"` on the select. `syncTemplateSelect(el, d)` (new `galleryAdminApp()` method) does `this.$nextTick(() => { el.value = d.selectedTemplateId; })`, re-applying the value one tick later once the options actually exist. Had to be a real component method rather than an inline `x-init="$nextTick(() => ...)"` — the CSP build's expression parser rejects arrow-function bodies inside directive attributes (confirmed via a `CSP Parser Error: Unexpected token` when tried inline), consistent with the CSP constraints already documented in this project (Session 4).

### Testing notes
- Reproduced first via the JSDOM harness: `toggleDisplays('g1')` on a display with `template.templateid: 't2'` showed `expandedDisplays[0].selectedTemplateId === 't2'` (Alpine data correct) but the actual `<select>` DOM `.value` was `""` (bug confirmed) — then confirmed the fix makes `.value` read `"t2"`
- Verified the no-template case still shows `""`/"No template" correctly, and that changing the selection with a failing PATCH still reverts `selectedTemplateId` to the prior value (Session 5's original fix), so this change doesn't regress the earlier bug fix

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard/align) in Template Admin — explicitly deferred by user
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 11 — Photos in Short Slots Rendered Much Smaller Than the Slot Itself

### What was done

**Bug report**: a template with three short, staggered slots (`h: 30, w: 22`, staggered `x`/`y`) looked correctly sized in the Template Admin preview, but the actual photos on the display view/edit pages rendered much smaller than their slot boxes.

**Root cause**: the slot's own position/size was correct (identical percentage math to the preview pane) — this wasn't a positioning bug. The actual cause was `.slot-placard`'s forced `min-height: 2.5rem` (~56px including padding). For a slot only ~184px tall (30% of a typical 16:9 canvas height), the placard's fixed floor consumes over 30% of that height regardless of how little text it holds, leaving a `.photo-area` whose aspect ratio is badly skewed (measured ~2.0:1 — very wide and short) compared to a typical photo's shape (~1.33:1). Since Session 8/9 correctly preserve the photo's true aspect ratio rather than distorting it to fit, `computeFrameBoxSize()`'s contain-fit then shrinks the photo dramatically to fit that skewed area — down to roughly 40% of the slot's total area in the reported case, which reads as "very small" even though the slot box itself is exactly where and how big it was configured.

**Fix** (`app/display.html`, `app/display-edit.html`): removed the `min-height: 2.5rem` floor from `.slot-placard`. The placard is always non-empty (it falls back to a "No caption" label when there's no real caption), so it can safely size purely from its own content instead of enforcing an arbitrary minimum — giving short slots meaningfully more room for the photo. Verified via hand-calculation with the reported template's exact numbers that this improves the rendered photo from ~40% to ~57% of the slot's area for a typical 4:3 photo.

### Testing notes
- Quantified the bug numerically before fixing: extracted `computeFrameBoxSize`/`normalizeSideWidths` and ran them against a realistic canvas size (1088×612, matching the display pages' `max-w-6xl` content width) with the reported template's exact `slot_positions`, confirming the photo-area's skewed 2.0:1 aspect ratio and the resulting ~40%-of-slot-area frame size before the fix, and the improved ~57% after
- Re-ran the Session 8/9 regression suites (matte/frame/placard config, alignment, backward-compatible default rendering) — no regressions, since removing a CSS minimum doesn't touch any of the inline-style-generating logic those tests check
- Noted but did not change: a *side* placard (`placard.position: "left"/"right"`) has a fixed `width: 200px` that could similarly dominate a narrow slot — not part of this report (which uses the default bottom position) but worth knowing if the same symptom shows up with a side-positioned placard on a narrow slot

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard/align) in Template Admin — explicitly deferred by user
- Side placard's fixed 200px width could dominate narrow slots the same way the bottom placard's old min-height did — not yet reported as an issue, flagged for awareness
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 12 — Layout Guide Overlay on the Display Edit Page

### What was done

**Request**: a visual reference on the display edit page showing the 16:9 canvas boundary and each slot's exact bounds (from `slot_positions`), matching what the public view page assumes — useful after Session 11's investigation made clear that a slot's *box* and the *photo rendered inside it* can differ significantly once matte/frame/aspect-ratio containment is applied.

**Implementation** (`app/display-edit.html`, `app/app.js`), edit page only — `display.html` (the public view) is untouched:
- New `showGuides` state on `displayEditApp()`, defaulting to `true`, toggled via a small "Guides" checkbox added next to "View live" in the header
- `.display-grid` gets a dashed `outline` (not `border`, so it never affects box sizing/layout) when `guides-on` is toggled, marking the 16:9 canvas boundary
- Each `.slot-card` gets the same treatment — since `.slot-card`'s own box is already positioned/sized exactly per `slot_positions` (via `slotBoxStyle()`), outlining it directly shows the slot's true bounds independent of whatever's actually rendered inside (photo, matte, frame, empty placeholder)
- Each slot also gets a small floating label (top-left corner) showing its raw `x`/`y`/`w`/`h` from `slot_positions` (new `slotGuideLabel(i)` method), so it's unambiguous this is the template's own geometry, not something derived from the photo

### Testing notes
- Verified via the JSDOM harness with the exact staggered `slot_positions` from Session 11's bug report: `guides-on` class present on the grid and all three slot-cards by default, labels showing `x:10 y:60  22×30` etc., and toggling `showGuides` to `false` correctly removes the classes and hides the labels (`display:none`) and unchecks the checkbox
- Confirmed zero references to the new guide classes/state in `display.html`, so the public view page is unaffected

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard/align) in Template Admin — explicitly deferred by user
- Side placard's fixed 200px width could dominate narrow slots the same way the bottom placard's old min-height did — not yet reported as an issue, flagged for awareness
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 13 — Percentage-Based Matte/Frame Widths

### What was done

**Request**: matte/frame widths were px-only; add the option to specify them as a percentage of the photo's width instead, so they scale proportionally rather than staying a fixed pixel amount.

**Design**: a `width` value (matte or frame, and per-side for matte) can now be either a plain number (px, unchanged) or a string like `"5%"` — a percentage of the photo's own *rendered* width (`contentW`, the fitted image width Session 9 already computes), not its raw pixel dimensions or the slot's size. Percentages are always relative to width specifically (per the request), even for a matte's top/bottom sides. px and percent can be freely mixed per side.

**The hard part**: this makes the overhead in `computeFrameBoxSize()` (Session 9) depend on the very thing it's solving for — a photo that's bigger has a bigger percentage-based matte, which shrinks the space left for the photo, etc. Fixed with real math rather than iteration: since percentage overhead is *linear* in the photo's fitted width (`overhead = fixed + coef × contentW`), both the width-fit and height-fit constraints reduce to simple linear inequalities in `contentW`, solved directly:
- `contentW ≤ (availW − overheadWFixed) / (1 + overheadWCoef)`
- `contentW ≤ (availH − overheadHFixed) / (1/ratio + overheadHCoef)`
- `contentW = max(0, min(of the above))`

When nothing is percentage-based (`coef = 0` everywhere), this reduces to exactly the old fixed-overhead arithmetic — verified bit-for-bit against Session 9's numbers.

**Frontend** (`app/app.js`):
- `computeFrameBoxSize()` rewritten around the linear solver above; now also returns `contentW` on its result (the photo's resolved fitted width) so percentage sides can be turned into exact final px values elsewhere
- New `splitWidthValue(v)` (splits a width entry into `{fixed, coef}`) and `resolveWidthPx(v, contentW)` (resolves one entry to a final px number)
- `frameOuterStyle(presentation, contentW)` and `matteInnerStyle(presentation, contentW)` now take an optional `contentW` and use it to resolve any percentage sides; pure-px configs ignore it entirely (zero behavior change)
- `displayApp()`/`displayEditApp()`: `frameOuterStyle(i)`/`matteInnerStyle(i)` now take the slot index and pull `contentW` from `this.frameSizes[i]` (computed by `layoutFrames()`, same JS-measurement pass as Session 9). Before the first layout pass measures anything, percentage sides resolve to `0` (matching Session 9's existing brief fallback window for frame sizing) rather than showing a wrong/stale value
- Template Admin's presentation-JSON documentation updated to describe percentage widths and mixed px/percent per-side mattes

### Testing notes
- Extracted the pure functions and hand-verified: backward compatibility (px-only configs produce numerically identical results to Session 9, including the exact `600×460`/`600×465` cases from that session's own tests); a `"5%"` matte's resolved px scales up correctly as the available space grows (verified 4× larger area → 4× larger matte px); mixed px+percent per side computes correctly; `frameOuterStyle`/`matteInnerStyle` resolve percentages exactly against the solver's own `contentW` output; pure-px configs are provably unaffected by whatever `contentW` is passed (including `undefined`)
- Verified the full pipeline end-to-end through real DOM rendering (JSDOM, measured `.photo-area` monkey-patched to 600×600): a template with `matte.width: "5%"` and `frame.width: "2%"` renders `padding: 26.32px` / `border: 10.53px` after `layoutFrames()` runs, matching the solver's `contentW` (526.32px) times 5%/2% exactly; before measurement, percentages correctly show as `0px` rather than a wrong guess
- Re-ran all prior display/template regression suites (matte/frame/placard, slot_positions layout, alignment, matte uniformity, guide overlay) — no regressions

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard/align) in Template Admin — explicitly deferred by user
- Side placard's fixed 200px width could dominate narrow slots the same way the bottom placard's old min-height did — not yet reported as an issue, flagged for awareness
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 14 — Clickable Photos on the Display View Page

### What was done

**Request**: clicking a photo on the public display view page (`display.html`) should navigate to that photo's own page.

**Implementation** (`app/display.html` only — `display-edit.html` deliberately untouched, since its photo area already has its own click target, the "Add/Change photo" overlay button):
- Each filled slot's `<img>` is now wrapped in `<a :href="'/photo.html?photoid=' + slot.photo.photoid" target="_blank" class="photo-link">`, following the exact `:href` pattern already established in `index.html`'s photo wall (string concatenation — the Alpine CSP build this app uses rejects template literals and method calls inside `:href` bindings)
- New `.photo-link` CSS makes the anchor fill the matte's content box exactly (`display:flex; width:100%; height:100%`), so the `<img>`'s existing `max-width/max-height:100%` sizing still resolves against the same area it did before the link was added — no visual/sizing change, purely adds a click target
- Empty slots (no photo) get no link, matching the wall's own photo-only-links behavior

### Testing notes
- Verified via the JSDOM harness: a display with one filled and one empty slot renders exactly one `.photo-link`, with the correct `href`/`target="_blank"`, wrapping the `<img>`; the empty slot has no link
- Re-ran the matte/frame/alignment/percentage-width regression suites — no changes to any rendered sizing, since `.photo-link` is purely a same-size pass-through wrapper

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard/align) in Template Admin — explicitly deferred by user
- Side placard's fixed 200px width could dominate narrow slots the same way the bottom placard's old min-height did — not yet reported as an issue, flagged for awareness
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

---

## Session 15 — Create Template Moved Into a Popup

### What was done

**Request**: replace the inline "Create New Template" box on Template Admin with a "Create Template" button in the upper right corner, opening a popup with the fields and a button to create.

**Implementation** (`app/template-admin.html`, `app/app.js`):
- Page header changed to a flex row: title/subtitle on the left, a `+ Create Template` button (upper right) that calls `openCreateModal()`
- Removed the old inline create-form box entirely
- New modal (`createModalOpen`), styled to match the photo-picker modal already used on `display-edit.html` (`.backdrop` blur, centered card, `animate-scalein`), closable via the `×` button, Cancel, clicking the backdrop, or Escape — same interaction pattern as that existing modal
- Modal contains the same fields the inline form had (name, photo count, the "starts with an evenly-spaced grid" helper text, error message) plus a **New** button that calls the existing `createTemplate()`
- `openCreateModal()` resets `newName`/`newPhotoCount`/`createError` before showing the modal, so reopening it after a previous session doesn't show stale input; `createTemplate()` now also closes the modal on success (stays open with the error shown on failure, so the user can retry without re-entering everything)

### Testing notes
- Verified via the JSDOM harness: the header button and modal both exist, the old inline form text is gone, the modal is hidden by default; opening the modal resets the fields; a successful create closes the modal and adds the template to the list; a failed create leaves the modal open with the error message; Cancel closes the modal without creating anything
- Re-ran the existing Template Admin regression suite (mini/full preview rendering, `defaultSlotPositions` sharing with the display pages) — no regressions

### Open items
- Non-JSON config UI for `presentation` (matte/frame/placard/align) in Template Admin — explicitly deferred by user
- Side placard's fixed 200px width could dominate narrow slots the same way the bottom placard's old min-height did — not yet reported as an issue, flagged for awareness
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

## Session 16 — Gallery-Level Placard System (Content, Positioning, Per-Slot Overrides)

### What was done

**Request**: track a placard *position* per slot (in addition to the photo's own slot position); configure placard *size*, *background*, and *content* once at the gallery level for consistency across every display in it; support placard items like "Photographer: {Photographer}" that substitute a photo's labels, with a per-item control to hide the item when the referenced label is missing; build a real UI for setting this up (not raw JSON); and let the display editor override an item's rendered text for one slot without touching the photo's labels.

Clarified three design decisions with the user up front: (1) the new gallery-level placard system **replaces** the old template-level `presentation.placard` (position/fields) system entirely, rather than keeping both; (2) per-slot overrides in the display editor replace an item's *text value* only (not full custom items per slot); (3) item positioning within the placard uses a **visual drag-to-position** canvas rather than plain x/y number fields.

**Data model**:
- Gallery-level `gallery.placard_defaults` (existing but previously-unused `placard_defaults` table/API, now wired up) — `{ width, height }` (percent of the 16:9 canvas, same coordinate space as `slot_positions`), `background`, `borderColor`, and `items[]`. Each item: `{ id, text, x, y, fontFamily, fontSize, fontWeight, fontStyle, color, hideIfMissing }` — `x`/`y` are percent position *within* the placard box; `text` may contain `{LabelName}` tokens.
- Template `slot_positions[i]` gained a `placard: { x, y }` — the placard box's own independent position on the 16:9 canvas (its *size* comes from the gallery, not the template). `defaultSlotPositions()`/`normalizeSlotPositions()` generate/preserve this alongside the existing `x/y/w/h`.
- Per-slot override — the existing but previously-inert `display_slots.placard` column now stores `{ overrides: { <itemId>: "replacement text" } }`. An override replaces an item's fully-resolved text verbatim (bypassing substitution and `hideIfMissing`); a blank override field means "use the auto-resolved value."

**Backend** (`internal/models/models.go`, `internal/handlers/displays.go`):
- `SlotPhoto` gained `Labels []Label` so the frontend can resolve `{LabelName}` substitutions without a per-photo fetch.
- `GET /api/v1/displays/:id` now also queries `labels` (joined to `users` for username, matching the existing pattern in `fetch.go`) for every photo across the display's slots in one batched query (`WHERE photoid = ANY($1::uuid[])`), grouped in Go and attached to each slot's `Photo.Labels`.
- No other backend changes needed — `placard_defaults` (gallery) and `display_slots.placard` (per-slot) read/write were already fully wired, just never used by any frontend page until now.

**Frontend** (`app/app.js`):
- Removed the old template-level placard system entirely: `normalizePlacardConfig`, `placardFieldSourceValue`, `placardFieldsFor`, `slotCardDirectionStyle`, and the old zero-arg `placardBoxStyle`.
- Added: `normalizeGalleryPlacard(raw)` (defaults + item normalization), `resolvePlacardItemText(template, labels)` (token substitution + missing-token detection), `placardBoxStyle(gallery, slotPos)` (position from the template, size/background from the gallery), `placardItemStyle(item)`, and `placardItemsFor(gallery, slot)` (merges gallery items + label substitution + per-slot override + `hideIfMissing`, in that precedence order). `typographyStyle()` is reused unchanged.
- `displayApp`/`displayEditApp`: replaced the old placard methods with `placardBoxStyle(i)`/`placardItemsFor(slot)` delegating to the new helpers using `this.gallery` (already fetched for prev/next nav) and `this.slotPositions[i]`.
- `displayEditApp` gained a caption-override editor: `captionModalOpen`/`captionDraft`/`openCaptionEditor()`/`resolvedCaptionPlaceholder()`/`saveCaptionOverrides()` — PATCHes `display_slots.placard.overrides`, resending `photoid`/`rich_text` as-is per the existing "don't wipe unrelated fields" pattern.
- `galleryAdminApp` gained the Placard Settings editor: `expandedGalleryPlacard` (loaded alongside displays when a gallery row expands), `placardModalOpen`/`placardDraft` (a deep-cloned, safely-editable copy), `addPlacardItem()`/`removePlacardItem()`, `startItemDrag(item, event)` (a vanilla mousedown/touchstart → window-level mousemove/touchmove → mouseup/touchend drag handler that converts pointer position to a clamped 0–100 percent position on the preview canvas), and `savePlacardSettings()` (PATCHes `placard_defaults`).
- `templateAdminApp.defaultPresentation()` no longer includes the old `placard` key (moved to the gallery level).

**Frontend markup**:
- `display.html`/`display-edit.html`: placards are now rendered as a **separate absolutely-positioned loop** in `.display-grid` (sibling to `.slot-card`, not nested inside it) since a placard's position/size is independent of its slot's own bounds. `.slot-card` simplified back to a plain flex wrapper around `.photo-area` (no more `flex-direction` juggling for placard stacking). New `.placard-box`/`.placard-item`/`.placard-empty` CSS.
- `display-edit.html`: added an "Edit captions" button (`.slot-caption-btn`, left corner, alongside the existing "Add/Change photo" button in the right corner) opening a caption-override modal listing the gallery's configured items with text inputs, each pre-filled as empty with the auto-resolved value shown as the placeholder.
- `gallery-admin.html`: added a "Placard Settings" button in each expanded gallery's panel, opening a modal with width/height/background/border fields, a live drag-to-position preview canvas (aspect-ratio computed from width%×16 / height%×9 to match the real render), and a per-item editor (text, font family/size/weight/style, color, hide-if-missing checkbox, remove button).
- `template-admin.html`: the JSON docs paragraph now documents `slot_positions[i].placard` and clarifies that size/content live at the gallery level; the live preview pane gets a dashed `.preview-placard` marker per slot (fixed reference size, since actual size is gallery-controlled) — hidden in the small row-header preview via CSS to avoid clutter.

### Testing notes
Verified via the JSDOM harness (6 new scripts, 62 checks total, 0 failures):
- Pure-function tests for `defaultSlotPositions`/`normalizeSlotPositions` (placard field present, explicit values preserved, missing values fall back, y is clamped ≤96 so it doesn't get pushed off-canvas), `normalizeGalleryPlacard`, `resolvePlacardItemText` (substitution, missing-token detection, multiple tokens), and `placardItemsFor` (label resolution, `hideIfMissing` skip behavior, override precedence over both substitution and `hideIfMissing`).
- `display.html` end-to-end render: two independently-positioned placard boxes (confirmed *not* nested inside `.slot-card`), correct item text/visibility per slot, an override slot showing the override text verbatim while still hiding its other missing-label item, and the box's own position/size styles.
- `display-edit.html`: "Edit captions" button present, modal opens with the gallery's items, placeholder shows the auto-resolved value (or a "missing" hint), saving PATCHes `overrides` keyed by item id while resending `photoid` so it isn't wiped, and the modal closes on success.
- `gallery-admin.html`: Placard Settings button and modal, draft is a true deep clone (editing it doesn't mutate the live gallery state until Save), add/remove item, a simulated `startItemDrag` → `mousemove` → position update (stubbing `getBoundingClientRect` since JSDOM doesn't lay out real geometry), and a successful save PATCHing `placard_defaults` and refreshing the cached gallery placard.
- `template-admin.html`: `defaultPresentation()` no longer has a `placard` key, a newly-created template's `slot_positions` include a `placard` per slot, and the preview renders one `.preview-placard` marker per slot.
- Regression check: matte/frame/align/clickable-photo-link on `display.html` and the guides overlay/photo-picker button on `display-edit.html` all still work after removing the old placard system — nothing else was affected by pulling placard rendering out of `.slot-card`.
- No Go toolchain available in this environment; the backend changes (`models.go`, `displays.go`) were verified by careful manual review only — not compiled or run against Postgres.

### Open items
- Placard item font family is a free-text field rather than a curated font picker — acceptable for now but could use a dropdown of the site's actual loaded fonts later.
- No drag support for repositioning a slot's placard *box* itself (only its position value in the template's raw `slot_positions` JSON) — the drag-to-position UI only covers *items within* the placard box in Gallery Admin, per the user's explicit scoping of the "visual UI" request to placard content/appearance.
- Backend changes untested against a live Postgres instance (no DB access in this environment) — needs `make build` + restart, and a smoke test of `GET /api/v1/displays/:id` label attachment, before relying on it in production.
- Phase 4: Microsoft sign-in
- Phase 5: label colors, restricted labels, emoji improvements, rich-text comments
- Phase 6: admin pages
- Phase 7: photo uploads

## Session 17 — Fixed: New Placard Items Beyond the 4th Weren't Visible in the Preview

### What was done
**Bug report**: adding a 6th placard item in Gallery Admin's Placard Settings didn't show up in the preview, even after resizing the placard box.

**Root cause**: `addPlacardItem()`'s default position was `y: 8 + n * 22` (n = item count), which grows unbounded — the 6th item (n=5) landed at `y: 118%`, far below the visible 0–100% box. Since item x/y is percent *of the placard box itself*, resizing the box couldn't help; the position was simply out of range. `normalizeGalleryPlacard()`'s fallback for items loaded without an explicit x/y had the same unbounded formula.

**Fix** (`app/app.js`): added `defaultPlacardItemPosition(n)`, a shared helper that wraps into a new column every 4 rows (`row = n % 4`, `col = Math.floor(n / 4) % 3`), so any number of sequentially-added items always lands within 0–100% on both axes — items beyond ~12 will start overlapping earlier ones, but remain visible and draggable rather than disappearing off-canvas. Both `addPlacardItem()` and `normalizeGalleryPlacard()`'s fallback now use this helper.

### Testing notes
- New pure-function test (`run36.js`): 10 sequentially-added default positions all stay within bounds; the specific reported case (6th item) verified in-bounds and distinct from earlier items; `normalizeGalleryPlacard()`'s fallback verified in-bounds for 8 items with no explicit position.
- Re-ran the full placard test suite (`run30`–`run35`, 47 checks) — no regressions.

### Open items
- Same as Session 16.

## Session 18 — Fixed: Placard Item Jumped on First Drag Move

### What was done
**Bug report**: selecting a placard item to reposition it worked, but the item jumped as soon as dragging started, requiring the user to drag it back to where they wanted — making fine positioning difficult.

**Root cause**: `startItemDrag()`'s `move` handler set the item's `x`/`y` (its top-left corner) directly to the cursor's position on the canvas. Since a user grabs an item wherever they click on it — usually the middle of its text, not its exact top-left corner — the very first `mousemove` snapped the item's corner to the cursor, producing a visible jump equal to the distance between the grab point and the corner.

**Fix** (`app/app.js`, `startItemDrag()`): capture the pixel offset between the pointer and the item's top-left corner once at drag start (`offsetX`/`offsetY`), then subtract that same offset on every subsequent move. The item now moves by the same delta the pointer moves, staying wherever it was grabbed relative to the cursor, instead of snapping its corner to the raw cursor position.

### Testing notes
- Updated `run33.js`'s drag test to grab an item a few pixels off its corner (simulating a realistic click) and assert: (1) a `mousemove` at the exact grab point produces zero movement (the specific bug), and (2) a subsequent move by a known pixel delta moves the item by the equivalent percent delta, not to an absolute cursor-derived position.
- Re-ran the full placard suite (`run30`–`run36`, 65 checks) — no regressions.

### Open items
- Same as Session 16.

## Session 19 — Placard Sizing in Inches/Centimeters

### What was done
**Request**: "The sizing of the placard isn't particularly intuitive. Would like to specify it in inches or cm."

Since the display canvas is rendered responsively (no fixed physical size on its own), specifying a real-world unit needs a physical reference. Clarified with the user: add a **"board width"** setting per gallery — the physical width the whole 16:9 canvas is meant to represent (e.g. "this gallery's display is a 48in wide board") — with placard width/height then entered in inches or cm relative to that. Default unit: inches.

**Data model** (`gallery.placard_defaults` — no backend schema change, still the same `json.RawMessage` column):
- `width`/`height` remain stored and rendered as percent-of-canvas — unchanged, this is still what actually drives the CSS. Nothing about rendering changed.
- Added `boardWidthIn` (physical width, in inches, that the 16:9 canvas represents — canonical unit is always inches) and `unit` (`'in'` | `'cm'`, remembers which unit the editor was last shown in). Both are purely editor-conversion metadata; they have no effect outside Gallery Admin's Placard Settings.
- Canvas height in inches is always `boardWidthIn * 9/16`. Defaults: 48in board, 4in × 1.5in placard (previously an oddly-large ~9.6in × 2.16in implied by the old flat 20%/8% default).

**Frontend** (`app/app.js`):
- `normalizeGalleryPlacard()` now also normalizes/defaults `boardWidthIn`/`unit`.
- Added `inToCm()`/`cmToIn()`/`round2()` conversion helpers.
- `galleryAdminApp.openPlacardSettings()` now builds `placardDraft` around **physical** values (`boardWidthIn`, `widthIn`, `heightIn`, all canonically inches) converted from the stored percent, plus `*Display` string fields (`boardWidthDisplay`/`widthDisplay`/`heightDisplay`) rendered in whichever unit is selected — added `refreshPlacardDisplayFields()` (recomputes the Display strings from canonical inches), `onPlacardUnitChange()` (called when the unit toggle changes), and `placardUnitInput(field, rawValue)` (converts a typed Display value back to the canonical inches value; keeps the raw typed text in the Display field itself so it doesn't get reformatted mid-keystroke, e.g. while typing "4.5").
- `savePlacardSettings()` converts the draft's physical inches back to percent (`width = widthIn/boardWidthIn*100`, `height = heightIn/(boardWidthIn*9/16)*100`) before PATCHing, and persists `boardWidthIn`/`unit` alongside so reopening later shows the same physical size in the same unit rather than a re-derived, potentially-rounded value.

**Frontend markup** (`app/gallery-admin.html`):
- Placard Settings modal's width%/height% number inputs replaced with: a Unit selector (inches/centimeters), a "Board width" field (with explanatory text: it's a physical reference only, the display itself still renders responsively), and "Placard width"/"Placard height" fields in the selected unit. These use `:value` + `@input` (not `x-model`) so the conversion logic fully controls what's stored, avoiding any double-fire ordering issues with a plain two-way binding.
- The live preview's `aspect-ratio` CSS now uses the placard's physical `widthIn`/`heightIn` directly (their ratio is identical to the percent ratio, so this needs no board-width math at all) — simpler than before and always accurate regardless of which unit is currently displayed.
- Item drag-to-position, add/remove, and per-item typography controls are unchanged (item x/y stay percent-within-the-box, since dragging is already an intuitive/visual control, not a typed number).

### Testing notes
- Extended the pure-function suite (`run30.js`, now 29 checks): `inToCm`/`cmToIn` round-trip correctly; `normalizeGalleryPlacard(null)` defaults `boardWidthIn` to 48 and `unit` to `'in'`, with the default percent correctly corresponding to a 4in × 1.5in placard on a 48in board; explicit `boardWidthIn`/`unit` are preserved; an invalid unit string falls back to `'in'`.
- Rewrote the Gallery Admin modal test (`run33.js`, now 28 checks) to verify the full physical-unit flow: opening the modal converts stored 18%/7% to ~8.64in/~1.89in on the default 48in board; typing "5" sets `widthIn` to exactly 5; switching the unit to cm re-renders the display field as ~12.7cm *without* changing the canonical inches value; typing "10" while in cm mode converts back to ~3.94in; the board-width field updates `boardWidthIn`; the preview's `aspect-ratio` matches `widthIn`/`heightIn` exactly; saving converts back to ~18%/~7% and persists `boardWidthIn`/`unit`. The existing item add/remove/drag-to-position checks from Session 18 still pass unchanged.
- Re-ran the full placard suite (`run30`–`run36`, 83 checks total) — no regressions from Sessions 16–18's rendering, override, or drag behavior.

### Open items
- Same as Session 16, plus: item font sizes are still in px (not inches/points) — not reported as an issue, but flagged for consistency awareness if raised later.

## Session 20 — Raw-JSON Editor for Placard Settings

### What was done
**Request**: add a way to edit the placard config as JSON, for making precise changes after the general layout has been adjusted with the visual editor.

**Implementation** (`app/app.js`, `app/gallery-admin.html`): added an "Edit as JSON" checkbox to the Placard Settings modal. It shows/hides a textarea containing the exact `placard_defaults` JSON that gets saved (same shape `normalizeGalleryPlacard()`/`placardItemsFor()` consume elsewhere) — not a separate format, so what you see is exactly what's persisted.

- `galleryAdminApp.placardDraftToStored()` — derives the stored (percent-based) shape from the draft's physical (inches) fields; used both to populate the JSON textarea and as the actual save payload, so the two paths can't drift apart.
- `applyPlacardJSON(raw)` — applies a parsed object back onto the draft by running it through `normalizeGalleryPlacard()` (so malformed/partial JSON still produces a sane draft) and converting its percent width/height back to the draft's canonical inches fields.
- `applyPlacardJsonText()` — parses `placardJsonText`, applying it via `applyPlacardJSON()` on success or setting `placardJsonError` and returning `false` on invalid JSON.
- `togglePlacardJsonMode()` — entering JSON mode snapshots the current draft as formatted JSON; **leaving** JSON mode applies the typed JSON first and refuses to leave (leaving `placardJsonMode` `true`, with the error shown) if it doesn't parse — so a syntax error can't silently discard the edit or corrupt the draft.
- `savePlacardSettings()` now also auto-applies pending JSON text if the editor is still open when Save is clicked, so `Save` doesn't need an explicit "apply" step first.
- The checkbox binds via `:checked` (not `x-model`) specifically so the veto behavior works — a plain two-way binding would flip the underlying state before the handler could reject an invalid parse.
- The visual controls (unit/board-width/size fields, drag preview, item list) are wrapped in `x-if="!placardJsonMode"` and swapped for the textarea + a short inline schema reference + an "Apply & back to visual editor" button when JSON mode is on.

### Testing notes
- New `run37.js` (19 checks): toggling into JSON mode snapshots the exact draft state; editing the JSON (adding an item, changing width/background) and toggling back applies it to the visual draft, correctly re-deriving `widthIn` from the edited percent; invalid JSON keeps the editor in JSON mode, sets the error message, and leaves the existing draft untouched; clicking Save while JSON mode is still open auto-applies the (now valid) JSON and PATCHes exactly those values, including a completely different `boardWidthIn`/`unit` (60cm) and item list; reopening the modal after a save resets back to visual mode.
- Re-ran the full placard suite (`run30`–`run37`, 111 checks total) — no regressions from Sessions 16–19.

### Open items
- Same as Session 16, plus: the JSON textarea has no live syntax highlighting or inline validation-as-you-type (only on toggle/save) — acceptable for the "precise tweak after the fact" use case this was built for, but worth revisiting if it becomes a primary editing path.

## Session 21 — Placard Item Positions Are Now Absolute Inches, Not Percent-of-Box

### What was done
- Changed placard item positioning from percent-of-placard-box (`x`/`y`, 0-100) to a fixed physical offset from the placard's top-left corner (`xIn`/`yIn`, in inches). Percent is now only computed at render time, from the item's fixed inches and the placard's *current* physical size.
- As explicitly requested: resizing the placard no longer moves items. If a placard is shrunk below an item's stored offset, that item can render outside the box and become invisible (clipped by `overflow:hidden`) until the placard is enlarged again or the item is dragged back in.
- `app/app.js`:
  - New `placardPhysicalSize(g)` helper — derives `{widthIn, heightIn}` from a normalized gallery's percent `width`/`height` and `boardWidthIn`.
  - `defaultPlacardItemPosition(n, placardWidthIn, placardHeightIn)` — now takes the placard's actual current size and returns inches (falls back to the module defaults if size is unknown), wrapping new items into a bounded grid instead of the old unbounded percent formula.
  - `placardItemStyle(item, placardWidthIn, placardHeightIn)` and `placardItemsFor` — convert the item's fixed inches to render-time percent against the placard's current size.
  - `normalizeGalleryPlacard` — computes physical size first, then fills in any item missing `xIn`/`yIn` via the new default-position logic.
  - Gallery Admin (`galleryAdminApp`): `addPlacardItem` stores new items with `xIn`/`yIn`; new `previewItemStyle(item)` (modal preview chip positioning) and `itemPositionLabel(item)` (human-readable "X, Y from top-left" label in the current unit) methods; `startItemDrag` rewritten to convert drag deltas to inches instead of percent, preserving the existing grab-offset fix from Session 18.
- `app/gallery-admin.html`: preview item styling now calls `previewItemStyle(item)`; added explanatory copy under the preview noting that resizing won't move items and can make them fall outside the box; added a position label under each item row; updated the JSON-mode docs paragraph to describe `xIn`/`yIn` instead of `x`/`y`.

### Testing notes
- JSDOM harness, `run30`–`run37` (121 checks total across pure-function and DOM-level tests), all passing after the rewrite.
- `run30.js`/`run36.js` directly verify: default positions stay within actual current bounds at multiple placard sizes, `xIn`/`yIn` are never mutated by the render-time style function, and shrinking a placard below an item's offset correctly pushes its rendered percent past 100% (the intended "gets clipped" behavior).
- `run33.js` verifies drag math now tracks inches (not percent) and that resizing changes an item's rendered percent while leaving its stored `xIn`/`yIn` untouched. One test-fixture bug (a leftover placard size from the drag sub-test wasn't restored before the final save/PATCH assertion) was found and fixed in the test itself — the app's round-trip math was already correct.
- `run31.js`/`run37.js` (display render + JSON-mode editor) updated with `xIn`/`yIn` fixtures, all passing.

### Open items
- None known. All four bug reports/feature requests from this session (unbounded item growth, drag-jump, physical units, JSON editor) plus this absolute-positioning change are implemented and tested.

## Session 22 — Placard Width/Height Now Match Between Visual Editor and JSON View

### What was done
- Fixed a reported mismatch: the visual editor's "Placard width"/"Placard height" fields showed physical inches/cm, but the JSON view (added in Session 20) showed `width`/`height` as percent-of-canvas — different units for the same value, so the numbers never matched at a glance.
- `widthIn`/`heightIn` (physical inches) are now the canonical stored/JSON field for the placard box's own size, mirroring the `xIn`/`yIn` convention already used for items. Percent-of-canvas is still computed, but only internally at normalize time, purely for CSS rendering — it's no longer part of the saved/JSON shape.
- `app/app.js`:
  - `normalizeGalleryPlacard` now reads `widthIn`/`heightIn` directly (falling back to converting legacy percent `width`/`height` for galleries saved before this change), and returns both the canonical inches and the derived render-time percent.
  - `placardPhysicalSize(g)` simplified to just read `g.widthIn`/`g.heightIn` (no longer recomputes from percent).
  - `placardDraftToStored()` now outputs `widthIn`/`heightIn` as-is instead of converting to percent — this is what populates both the JSON textarea and the real PATCH payload, so the two can't drift.
  - `applyPlacardJSON()` and `openPlacardSettings()` simplified to copy `widthIn`/`heightIn` straight across instead of round-tripping through percent math.
- `app/gallery-admin.html`: updated the JSON-mode docs paragraph to describe `widthIn`/`heightIn` as the same numbers shown in the visual editor's size fields.
- Backend is unaffected — `placard_defaults` is stored as opaque `json.RawMessage`, no schema change needed there.

### Testing notes
- Updated `run33.js`/`run37.js` assertions that checked the PATCH/JSON payload's `width`/`height` percent to check `widthIn`/`heightIn` instead.
- Full placard-related suite (`run30`–`run37`, 130 checks) passes, including the legacy-fallback path (`run30`/`run36` still normalize old percent-only `width`/`height` fixtures correctly) and the full open → edit → JSON round-trip → save cycle.

### Open items
- None known.

## Session 23 — Fixed: Placard Item Font Sizes Didn't Scale With the Displayed Canvas Size

### What was done
- Fixed a reported bug: on the display view/edit pages, the placard box itself rendered at the right size, but item text (font sizes, stored as raw px) stayed a fixed pixel size regardless of how big the 16:9 canvas actually rendered on screen — so a placard set up for a small preview looked right there but too big/small once shown on an actual display (whose canvas can render at any pixel width depending on viewport).
- Font sizes are now scaled by the ratio of the canvas's *actual* on-screen pixel width to its *design* pixel width — `boardWidthIn * 96` (CSS's own 96px = 1in reference). At scale 1 (canvas shown at its true physical size) fonts render exactly as specified; if the canvas is shown smaller/larger, fonts shrink/grow proportionally, so caption text stays true to the placard's physical size on any screen.
- `app/app.js`:
  - New `CSS_PX_PER_IN = 96` constant and `placardFontScale(boardWidthIn, canvasPxWidth)` helper.
  - `typographyStyle(typo, scale)` now multiplies `fontSize`/`letterSpacing` (px properties) by `scale` (default 1); unitless `lineHeight` and non-numeric properties are untouched.
  - `placardItemStyle(item, placardWidthIn, placardHeightIn, fontScale)` and `placardItemsFor(gallery, slot, canvasPxWidth)` thread the scale factor through to rendering.
  - `displayApp`/`displayEditApp`: added a `canvasWidthPx` field, updated in `layoutFrames()` (the same ResizeObserver-driven measurement already used for exact frame sizing) from `grid.clientWidth`; `placardItemsFor(slot)` now passes `this.canvasWidthPx` through.
- Scoped to the real display pages (`display.html`, `display-edit.html`) since that's where the bug was reported. The Gallery Admin Placard Settings mini preview is left unscaled — it's a rough layout tool capped at a small fixed width, not a true render of on-screen text size.

### Testing notes
- `run30.js`: added pure-function tests for `placardFontScale` (scale 1 at design width, 0.5/2 at half/double, defaults to 1 with no/invalid measurement), scale-aware `typographyStyle`/`placardItemStyle` (px properties scale, unitless/non-numeric properties don't), and `placardItemsFor`'s new `canvasPxWidth` argument end-to-end.
- `run31.js`: added a DOM-level test that stubs `.display-grid`'s `clientWidth` (JSDOM has no real layout engine) to a known pixel value, calls `layoutFrames()` directly (exactly what the ResizeObserver does on a real resize), and confirms the re-rendered `.placard-item` style attribute reflects the scaled font size — first at the design width (scale 1, unchanged), then at half that width (font halved). This is the true end-to-end confirmation that the fix works through the real reactive rendering path, not just the underlying helper functions.
- Full placard suite (`run30`–`run37`, 139 checks) passes with no regressions.

### Open items
- Gallery Admin's mini placard preview doesn't apply this scaling (documented above as an intentional scope decision) — if that becomes confusing in practice, it could be added later using the same `placardFontScale()` helper against the preview canvas's measured width.

## Session 24 — Hover-to-Expand Placards for Full-Size Readable Text

### What was done
- Added a hover interaction to placard boxes on both display pages: hovering over a placard, after a short pause, expands the whole box (and everything inside it, including text) up to its full, un-shrunk design size — undoing the Session 23 font-size shrink so caption text is comfortably readable, then collapses back immediately when the mouse moves away.
- Pure CSS, no JS event handling needed — implemented via a `:hover` transition-delay trick: the base `.placard-box` rule transitions `transform` quickly (0.12s, no delay) so it snaps back instantly on mouse-out; the `.placard-box:hover` rule transitions to `transform: scale(var(--placard-hover-scale))` with a 0.35s delay, so a passing cursor doesn't trigger it — only a deliberate pause does. Also adds `z-index: 40` and a drop shadow on hover so the enlarged box visibly lifts above neighboring slots/placards.
- `--placard-hover-scale` is the exact inverse of the font shrink factor from Session 23 (`1 / placardFontScale(...)`), computed in `placardBoxStyle()` and set as an inline CSS custom property. Scaling the *whole box* by this factor (rather than recomputing per-item font sizes) is mathematically equivalent to rendering the placard as if the canvas were shown at its true physical size — it also scales the box's own dimensions and any drawn border/background proportionally, which is what "read it at full size" implies.
- `app/app.js`: `placardBoxStyle(gallery, slotPos, canvasPxWidth)` now takes the same `canvasPxWidth` measurement `placardItemsFor` already uses, and appends `--placard-hover-scale: <n>;` to its returned style string. Both `displayApp` and `displayEditApp`'s `placardBoxStyle(i)` wrapper methods now pass `this.canvasWidthPx` through.
- `app/display.html` and `app/display-edit.html`: added the `:hover` CSS rules above to `.placard-box`. Applied to both pages (view and edit) for consistency — an editor also benefits from being able to read placard text clearly while working.
- Uses `transform-origin: center center`, and since the box is `position: absolute`, the transform doesn't reflow or shift any sibling slots/placards — it only changes how the hovered box itself is painted, so it can safely grow past its normal footprint without disturbing the rest of the layout.

### Testing notes
- `run30.js`: added pure-function tests for `placardBoxStyle`'s new `canvasPxWidth` arg and `--placard-hover-scale` output — no scale at the canvas's design width (hover would be a no-op, correctly), scale 2 when the canvas is compressed to half its design width, and confirmed the position/size percent styling is untouched by the addition.
- `run31.js`: extended the existing DOM-level canvas-width-stubbing test to also check the rendered `.placard-box` style's `--placard-hover-scale` value at both the design width (1) and half the design width (2), alongside the existing font-size checks — confirming the box-level and item-level scale factors stay in lockstep through the real reactive rendering path.
- Full placard suite (`run30`–`run37`, 145 checks) passes with no regressions. Not able to visually verify the actual hover animation/timing in this sandboxed environment (no real browser) — the CSS was written and manually reasoned through, but worth a quick look in a real browser to confirm the delay/feel is right.

### Open items
- The 0.35s hover delay and un-hover snap-back speed (0.12s) are reasonable defaults, not something the user specified an exact value for — easy to tune if they feel off in practice.
- No cap on the expansion magnitude — a placard on a heavily-compressed canvas (e.g. a wide board viewed on a narrow phone) could expand quite dramatically on hover. This matches the literal request ("full size for the font") but is worth watching for if it ever looks excessive.

## Session 25 — Fixed: Hover-Expanded Placards Could Grow Off-Screen Near an Edge

### What was done
- Fixed a nit on the Session 24 hover-to-expand feature: a placard positioned near the edge of the window would expand (via `transform: scale()`, anchored at its own center) partly or entirely off-screen, since pure CSS has no way to know how close an element actually sits to the *viewport's* edge and adjust for it.
- Added a small JS-computed clamp that runs once on `mouseenter` (before the CSS hover-delay transition becomes visible, so there's no visible jump): it measures the box's real screen position, computes how far the *expanded* box would extend past each edge, and — if any edge would be crossed — writes a pixel offset into two new CSS custom properties (`--placard-hover-shift-x/-y`). The `:hover` transform now composes `translate(shift) scale(hoverScale)`, so the box shifts back into view by exactly enough to stay fully on-screen (plus a small margin, and extra clearance at the top to also stay clear of the sticky navbar) while still expanding by the same amount.
- `app/app.js`:
  - New `clampPlacardHoverShift(rect, scale, viewportW, viewportH, margin, topMargin)` — pure math, given the box's un-scaled `getBoundingClientRect()` and its hover scale, returns the `{shiftX, shiftY}` needed to keep the *expanded* box within `[margin, viewport - margin]` (or `[topMargin, ...]` vertically). Returns `{0, 0}` whenever `scale <= 1` (hover wouldn't actually grow the box, so nothing needs correcting, even if the box already happens to sit near the edge on its own).
  - New `applyPlacardHoverShift(el)` — the DOM-facing wrapper: reads the element's live `--placard-hover-scale` (via `getComputedStyle`) and its `getBoundingClientRect()`/the window's `innerWidth`/`innerHeight`, calls `clampPlacardHoverShift()`, and writes the result back as `el.style.setProperty('--placard-hover-shift-x'/'-y', ...)`.
  - `displayApp`/`displayEditApp`: added `handlePlacardHover(event) { applyPlacardHoverShift(event.currentTarget); }`.
- `app/display.html` / `app/display-edit.html`: added `@mouseenter="handlePlacardHover($event)"` to `.placard-box`; the `:hover` rule's `transform` now reads `translate(var(--placard-hover-shift-x, 0px), var(--placard-hover-shift-y, 0px)) scale(var(--placard-hover-scale, 1))` (translate composed *outside* scale in the CSS transform list, so the shift is applied in real screen pixels, not itself scaled up).

### Testing notes
- `run30.js`: added pure-function tests for `clampPlacardHoverShift` (no shift needed when there's room to grow; shifts right/left near the left/right edge; shifts down past the taller top margin near the top; `topMargin` falling back to `margin` when omitted; `scale <= 1` always yielding no shift even right at an edge) and for `applyPlacardHoverShift` (wired up against a faked element — plain objects shaped like the DOM APIs it touches, no real DOM needed — confirming it reads the scale, writes both shift properties in px, and is a safe no-op for a missing/invalid element).
- `run31.js`: extended the existing DOM-level test — with the canvas already compressed to half its design width (hover-scale 2 from Session 24's test), stubbed the placard box's `getBoundingClientRect()` near the left edge of the JSDOM viewport, dispatched a real `mouseenter` event, and confirmed `handlePlacardHover()` actually ran through Alpine's event binding and wrote non-zero `--placard-hover-shift-x/-y` onto the box's live inline style — full wiring confirmed, not just the underlying helper functions.
- Full placard suite (`run30`–`run37`, 157 checks) passes with no regressions.

### Open items
- None known.

## Session 26 — Placard Position Now Attaches to a Side of the Photo Frame

### What was done
- Replaced the old free-form `slot_positions[i].placard = { x, y }` (an absolute percent position anywhere on the 16:9 canvas, unrelated to where the photo actually ended up) with `placard = { side, align, gapIn }` — the placard now attaches to one edge of the *photo frame itself*:
  - `side`: `"top"` | `"bottom"` | `"left"` | `"right"` — which edge of the frame the placard sits against.
  - `align`: a continuous 0–100 value — position along that edge, from the frame's own top/left (0) to its bottom/right (100); 50 centers it. For a top/bottom placard this slides it left↔right; for left/right it slides top↔bottom.
  - `gapIn`: physical distance in inches between the placard and the frame (same board-relative unit as the gallery's placard width/height/boardWidthIn).
  - Per the user's decision, this fully replaces the old x/y positioning — there's no separate "custom position" mode.
- This is a bigger computation than before because the photo frame can be smaller than (and offset within) its slot, depending on the photo's aspect ratio and the template's `presentation.align` setting — so `placardBoxStyle()` now has to work out the frame's *actual* on-screen box before it can attach anything to it:
  1. The slot's own box, converted from percent to px using the canvas's actual measured width (`canvasWidthPx`, from Session 23/24's `layoutFrames()`).
  2. The frame's px size within that slot — `frameSizes[i]` (`computeFrameBoxSize()`'s `{w, h}`, already measured for matte/frame rendering).
  3. Where that frame box sits *within* the slot — from `presentation.align`, the same flexbox alignment the real `.photo-frame` element uses (`normalizeAlign()`/`photoAreaStyle()`).
  4. From the frame's resolved px box, `side`/`align`/`gapIn` place the placard directly against an edge (gap converted to px via the canvas's actual current px-per-inch, `canvasWidthPx / boardWidthIn` — the real on-screen scale, not the fixed 96dpi reference `placardFontScale()` uses for font sizing).
  5. Converted back to percent-of-canvas so it still renders through the existing `position: absolute; left/top: %` mechanism.
  - Before the first `layoutFrames()` measurement pass (or if `frameSize` isn't available), falls back to roughly "below the slot" (the old default's formula) rather than flashing at a nonsensical 0%,0%.
- `app/app.js`:
  - `defaultSlotPositions()`/`normalizeSlotPositions()` — new `placard` shape, defaults `{ side: 'bottom', align: 50, gapIn: 0.15 }`; `align` is clamped 0–100, an invalid `side` falls back to `'bottom'`.
  - `placardBoxStyle(gallery, slotPos, canvasPxWidth, frameSize, presentation)` — two new params, full position rewrite as described above (still also sets `--placard-hover-scale` exactly as before).
  - `displayApp`/`displayEditApp`'s `placardBoxStyle(i)` wrapper methods now pass `this.frameSizes[i]` and the template's `presentation` through.
- `app/template-admin.html`: updated the slot-positions JSON docs paragraph to describe the new schema; the preview's placard marker now calls a new `previewPlacardStyle(slot)` method (in `templateAdminApp`) instead of an inline `slot.placard.x/.y` expression — since this schematic preview has no real photos/frames to measure, it approximates by treating the slot's own box as a stand-in for the frame, documented clearly as an approximation (the real display resolves it against the actual measured frame).

### Testing notes
- `run30.js`: rewrote the `defaultSlotPositions`/`normalizeSlotPositions` tests for the new schema (including align-clamping and invalid-side fallback), and added a new hand-verified test block for `placardBoxStyle`'s position math — a canvas/slot/frame/gallery setup with clean round numbers (480×270px canvas, 100×80px frame, 10px/in) checked against every side (top/bottom/left/right) and multiple align values, plus the no-canvas-measurement fallback, the no-frameSize fallback, and a case showing `presentation.align` shifting the frame (and therefore the placard) as expected.
- `run31.js`: updated the display.html end-to-end fixture to the new schema and added position assertions both before measurement (fallback path) and after (real geometry, hand-verified against the same math as run30 for two side-by-side slots).
- `run34.js`/`run32.js`/`run35.js`: updated fixtures/assertions for the new schema.
- Full placard suite (`run30`–`run37`, 180 checks) passes with no regressions.

### Open items
- None known.

## Session 27 — Drag-to-Position/Resize in Template Admin's Preview

### What was done
- Template Admin's slot geometry was previously JSON-only (hand-editing the `slot_positions` textarea, with a read-only preview marker). Added three drag interactions directly on the live preview:
  - **Move a slot**: drag anywhere on a `.preview-slot` box to reposition it. Clamped to stay fully within the canvas.
  - **Resize a slot**: drag one of 4 small corner handles (nw/ne/sw/se) that appear on each slot box; the opposite corner stays fixed while the dragged one follows the cursor, with a minimum size so it can't be dragged to nothing or flipped inside-out.
  - **Re-attach the placard**: drag the dashed placard marker anywhere in the preview; it picks whichever side of the slot (used as a stand-in for the frame, same approximation `previewPlacardStyle()` already made) the drop point is nearest to, and derives `align`/`gapIn` from the drop position — same underlying `{ side, align, gapIn }` schema from Session 26, just settable by drag instead of only by typing JSON.
- All three write straight back into `editSlotPositions` (the JSON string) after every move, so the hand-editable textarea and the live preview always agree — dragging is just a faster way to produce the same JSON, not a separate system. The raw-JSON textarea is still there for precise numeric tweaks afterward.
- `app/app.js`:
  - New pure functions: `resizeSlotFromCorner(corner, orig, cursorX, cursorY, minSize)` (corner-resize geometry) and `placardDragToConfig(slot, px, py)` (nearest-side detection + align/gapIn derivation — gapIn is necessarily approximate here, converted using `DEFAULT_BOARD_WIDTH_IN` as a reference since Template Admin has no gallery context to know the real `boardWidthIn`; the actual display resolves it precisely against whichever gallery it's shown in).
  - `templateAdminApp`: `startSlotDrag(i, event)`, `startSlotResize(i, corner, event)`, `startPlacardDrag(i, event)` — mousedown/touchstart handlers following the same pattern as `galleryAdminApp`'s existing `startItemDrag()` (window-level mousemove/mouseup listeners, touch support). Slot drag preserves the grab-point offset (same "no jump on first move" fix from Session 18); the placard drag tracks the cursor directly since it's a snap-to-edge interaction, not a fixed-point translation.
- `app/template-admin.html`: added `x-ref="templatePreview"` to the editor's live preview, `@mousedown`/`@touchstart` handlers on `.preview-slot` and `.preview-placard`, 4 `.resize-handle` corner divs per slot (with `.stop` modifiers so grabbing a handle doesn't also trigger the parent slot's move), and matching CSS (cursor hints, handle appearance). The read-only mini thumbnail in each template's collapsed row header (`.template-preview.mini`) intentionally has none of this — only the expanded editor's preview is interactive.

### Testing notes
- `run30.js`: pure-function tests for `resizeSlotFromCorner` (all 4 corners keep the correct opposite corner fixed, minimum-size clamping, cursor-overshoot clamping) and `placardDragToConfig` (nearest-side detection from all 4 directions, align derivation, gapIn from drop distance, gapIn clamped to 0 for a drop inside the slot, align always clamped 0-100).
- `run34.js`: extended the existing template-admin.html DOM test with real drag simulations (stubbed `getBoundingClientRect()` on the preview canvas, dispatched `mousedown`/`mousemove`/`mouseup`) for all three interactions, confirming `editSlotPositions` updates live and correctly. Caught and fixed two real test-authoring issues: (1) `.preview-slot` matches both the interactive editor preview *and* the read-only mini thumbnail — every query now scopes to the non-mini preview specifically; (2) the original 2-slot test fixture was full-height (h=100%), leaving no vertical room to actually exercise y-axis drag/resize — switched to a 4-slot (2×2) fixture.
- Full placard/template suite (`run30`–`run37`, 214 checks) passes with no regressions.

### Open items
- Placard drag's `gapIn` is an approximation (assumes a default board width, since a template isn't tied to any one gallery) — same caveat already noted for the marker's rendered position in Session 26, now also applying to what dragging produces.

## Session 28 — Fixed: Placard Marker Ignored gapIn in Template Admin Preview

### What was done
- Bug report: dragging the placard marker in Template Admin's preview correctly updated the stored `gapIn` value (confirmed working in Session 27), but the marker itself never visually moved to reflect it — it always rendered at a fixed, hardcoded gap regardless of what `gapIn` actually was, whether set by dragging or typed directly into the JSON textarea.
- Root cause: `templateAdminApp.previewPlacardStyle(slot)` had `const pw = 16, ph = 6, gap = 1;` — `gap` was a leftover placeholder constant from before `gapIn` existed as a real per-slot value, and was never wired up when the `{side, align, gapIn}` schema was introduced (Session 26) or when drag-to-reposition was added (Session 27). Both of those sessions correctly wrote `gapIn` into the data; nothing read it back for rendering.
- `app/app.js`: `previewPlacardStyle()` now computes `gap` dynamically from `cfg.gapIn` (falling back to `DEFAULT_PLACARD_GAP_IN`, clamped non-negative), converted from inches to percent using the same `DEFAULT_BOARD_WIDTH_IN` / `boardHeightIn` reference `placardDragToConfig()` already used for the reverse conversion — the two are now exact inverses, so drag → store gapIn → re-render round-trips correctly.
- `app/template-admin.html`: updated the stale HTML comment above the placard-marker template loop, which used to say the marker used "a fixed reference size/gap" — only the *size* is still a fixed reference; gapIn is now honored.

### Testing notes
- Added a new targeted regression test to `run34.js` calling `previewPlacardStyle()` directly with two different `gapIn` values (0.15in and 2.7in) on an identical slot/side/align, hand-verifying the exact expected `top%` for each (50.56% and 60% respectively) — this is the precise scenario the bug involved, and no prior test would have caught it (existing tests only checked the stored `gapIn` value coming out of `placardDragToConfig`, never whether the marker's rendered position actually used it).
- Full placard/template suite (`run30`–`run37`, 217 checks) passes with no regressions.

### Open items
- None. Both files are static frontend assets — sync-only, no backend rebuild needed.

## Session 29 — Fixed: Placard Drag Could Corrupt gapIn to null / Marker Disappearing

### What was done
- Bug report: dragging the placard marker in Template Admin's preview sometimes produced `{ "side": "right", "align": 100, "gapIn": null }` and the marker visually disappeared.
- Root cause: `startSlotDrag()`, `startSlotResize()`, and `startPlacardDrag()` each call `canvas.getBoundingClientRect()` exactly once at mousedown and reuse that same `rect` for every subsequent `mousemove` in the drag. If that rect is ever captured as zero-size (width and/or height 0 — e.g. a layout race right as the accordion editor expands, or any other moment the preview happens to measure as 0x0), every `xPct`/`yPct` computed from it divides by zero, producing `Infinity`. That `Infinity` flows straight through `placardDragToConfig()`'s math into `gapIn`, and `JSON.stringify(Infinity)` silently writes the literal `null` — which is exactly the corrupted value reported. (Worked out and confirmed by hand: `Infinity` for the x-axis alone naturally produces `side: "right"` since `Math.abs(nx) >= Math.abs(ny)` is always true against a finite `ny`, while `align` stays a normal finite number since it only depends on the y-axis — matching the exact `side`/`align`/`gapIn` combination in the bug report.)
- `app/app.js`:
  - `startSlotDrag()`, `startSlotResize()`, `startPlacardDrag()`: added a guard right after measuring `rect` — `if (!(rect.width > 0) || !(rect.height > 0)) return;` — so a drag simply never starts against a degenerate rect, instead of running for its whole duration on broken math.
  - `resizeSlotFromCorner()` and `placardDragToConfig()` (the pure geometry functions): added `isFinite()` guards on their cursor/drop-point inputs as defense-in-depth, falling back to a sane value (the box's own existing edge, or its center) so a non-finite input from any future caller can never propagate into a non-finite (and therefore `null`-after-JSON) output field.

### Testing notes
- `run30.js`: new pure-function regression checks — `placardDragToConfig()` called with `px: Infinity` and with `px: py: NaN`, confirming `gapIn`/`align`/`side` all stay finite/valid; `resizeSlotFromCorner()` called with `cursorX: Infinity, cursorY: NaN`, confirming `x/y/w/h` all stay finite.
- `run34.js`: new DOM-level regression test — stubs `getBoundingClientRect()` to return a zero-size rect, simulates a full placard-marker drag (mousedown/mousemove/mouseup), and confirms `editSlotPositions` is completely unchanged afterward (the drag no-ops rather than writing corrupted JSON).
- Full placard/template suite (`run30`–`run37`, 224 checks) passes with no regressions.

### Open items
- None. Both files are static frontend assets — sync-only, no backend rebuild needed.

## Session 30 — Fixed: Slot Drag/Resize Broken by Alpine x-ref Collision Across Templates

### What was done
- Follow-up bug report right after Session 29's fix: "adjusting the placard in template admin is working, but adjusting the slot is now broken — corners don't move, box doesn't move."
- Root cause, found by reading the vendored `alpinejs.min.js` source directly: Alpine's `x-ref` directive registers into a single flat map on the component root, keyed only by the ref name — `n._x_refs[e] = t`, with no per-`x-for`-iteration scoping, and the entry is only ever cleaned up when its element is truly removed from the DOM (never on an `x-show` hide, which just toggles CSS `display`). Template Admin's preview `<div class="template-preview" x-ref="templatePreview">` lives inside the `x-for="templates"` loop — one per template row, and since `x-show` keeps every row's markup in the DOM (just hidden), **all** of them register the same `x-ref="templatePreview"` name. `this.$refs.templatePreview` therefore always resolved to whichever row happened to register *last*, regardless of which template the user actually expanded. If that "winning" row wasn't the one currently expanded, its preview is `display:none` — a genuinely zero-size box — which Session 29's new zero-rect guard then (correctly, but unhelpfully) used to block the drag entirely. This bug has existed since Session 27 whenever a template-admin user had 2+ templates; it just hadn't been reported yet, and the test suite never caught it because `run34.js`'s fixture only ever created a single template, so there was never a "wrong row" for `$refs` to collide with.
- `app/app.js`: `startSlotDrag()`, `startSlotResize()`, `startPlacardDrag()` no longer read `this.$refs.templatePreview`. Each now resolves its preview container via `event.currentTarget.closest('.template-preview')` — unambiguous no matter how many templates exist, since it's derived from the actual element that was clicked/touched rather than a name-keyed lookup.
- `app/template-admin.html`: removed the now-unused `x-ref="templatePreview"` attribute (left a comment explaining why, so nobody re-adds it and reintroduces the same collision).

### Testing notes
- `run34.js`: added a new regression block that seeds **two** templates ("Alpha", "Beta" — same photo count, so alphabetically Alpha is first/non-last in the sorted list), expands the non-last one (Alpha), stubs *only* Alpha's own preview to a valid non-zero rect (Beta's is left at JSDOM's default 0x0, exactly simulating a real collapsed row), and confirms dragging Alpha's slot and resizing it by a corner both actually work.
- Verified this test is a real regression guard, not a tautology: temporarily reverted the fix back to `this.$refs.templatePreview`, re-ran the suite, and confirmed 8 checks failed (including, notably, some in the original single-template placard-drag flow too — `$refs` was fragile there in a subtler way this fixture hadn't previously exercised). Restored the fix and confirmed all checks pass again.
- Full placard/template suite (`run30`–`run37`, 230 checks) passes with no regressions.

### Open items
- None. Both files are static frontend assets — sync-only, no backend rebuild needed.

## Session 31 — PLAN.md Completion Audit & Remaining Work Sequence

### What was done
Reviewed the repo against every phase in `PLAN.md` (read-only audit, no code changes) to determine what's actually built versus still pending, and recommended an order for the rest.

**Phase 1 — Permissions: done.** `migrations/012_permissions.sql` (teams/roles/role_permissions/entity_role_grants) and `013_remove_authorized_non_public.sql` are in place; `internal/permissions/permissions.go` implements `Checker.Check`/`UserPermissions`/`HasAny`; every handler (`admin.go`, `comments.go`, `displays.go`, `emojis.go`, `galleries.go`, `labels.go`, `photo.go`, `search.go`, `templates.go`) calls the checker; `GET /api/v1/permissions` is wired in `router.go`.

**Phase 2 — Gallery & Display: done except 2g.** `migrations/014_galleries_displays.sql` covers `display_templates`, `galleries`, `placard_defaults`, `displays`, `display_slots`. Full CRUD for galleries/displays/templates exists in `galleries.go`/`displays.go`/`templates.go` and is routed. Frontend has `galleries.html`, `gallery-admin.html`, `display.html`, `display-edit.html`, `template-admin.html`, plus hamburger-menu integration (`galleriesNav()` in `app.js`) and an extensive drag/resize/placard editor (Sessions 18–30). **Missing: 2g, a per-gallery permissions UI** — grepped for any grant/revoke page and found none.

**Phase 3 — Photo Wall: done.** `app/index.html` is now the wall (`wallApp`), the old viewer moved to `app/photo.html`, and the row-layout algorithm (`packRows`, `rowScale`, `nominalWidth`, `photoScale`) is implemented in `app.js`.

**Phase 4 — Microsoft Sign-in: not started.** No `Microsoft*` config vars in `internal/config/config.go`, no `MicrosoftLogin`/`MicrosoftCallback` in `auth.go`, no `/auth/microsoft*` routes. Only Google/Apple/Facebook exist.

**Phase 5 — Attributes: partially done.**
- 5a (label colors): only a client-side hash-based fallback (`labelColorFor` in `app.js`); no `label_name_colors` table and no color-picker override UI.
- 5b (restricted labels): not implemented — no `restricted` column, no CLI flag, no 403 enforcement.
- 5c (emoji improvements): search/pagination/upload UI exist (`ListTypes` supports `search`/`group`/paging), but sorting by `usage_count DESC` isn't implemented (no such column anywhere) and the reaction tooltip only shows "Click to react/remove," not the emoji name.
- 5d (rich text comments): not implemented — no Markdown library in `package.json` or frontend, no sanitization in `comments.go`.

**Phase 6 — Admin Pages: not started (beyond pre-existing baseline).** Only `app/admin.html` and its three endpoints (`ListExhibitions`, `ListPhotos`, `SetPublic`) exist, which predate this plan. No `admin-master.html`, `admin-users.html`, `admin-emojis.html`, or `admin-labels.html`; no `user_flags` table; no admin endpoints for users/emoji/label-name toggles; 6c's planned search/filter enhancement to the existing admin page also hasn't been added.

**Phase 7 — Photo Uploads: not started.** No upload icon/popup, no drag-and-drop queue, no batch-label editor, and no `POST /api/v1/photos/upload` endpoint. The only "upload" code in the frontend/backend is the unrelated profile-avatar upload.

### Recommended sequence for remaining work
`Phase 4 → Phase 7 → Phase 5 (5a/5b/5c/5d) → Phase 6 → Phase 2g`

Reasoning: Phase 4 (Microsoft sign-in) is small, independent, and mirrors the existing Facebook pattern — a quick win. Phase 7 (photo uploads) is independent and high-value, and doesn't strictly need 5a first. Phase 5's remainder should land next, with 5b (restricted labels) completed before Phase 6 since the planned label-admin page (6e) exposes the restrict/unrestrict toggle. Phase 6 (admin pages) depends on Phase 1 (done) and benefits from 5b/5c/5d existing so there's something for the toggles to control. Phase 2g (gallery permissions UI) is left for last since it's small and self-contained, and can reuse UI patterns established while building Phase 6's admin pages.

### Open items
- None — this was an audit only; no code was written.

## Session 32 — Phase 4: Microsoft Sign-In

### What was done
Implemented PLAN.md Phase 4, following the existing Facebook Login pattern (Session 1 baseline / `auth.go`) as the template.

- **`migrations/015_microsoft_auth.sql`** (new): adds `microsoft_id TEXT` to `users` plus a partial unique index, mirroring `011_facebook_auth.sql`. The codebase identifies OAuth accounts via one dedicated column per provider rather than a generic `auth_providers` table, so PLAN.md's assumption that migration 006 already covered this wasn't quite right — a real migration was needed, same as Facebook got in 011.
- **`internal/config/config.go`**: added `MicrosoftClientID`, `MicrosoftClientSecret`, `MicrosoftRedirectURL`, and `MicrosoftTenantID` (env vars `MICROSOFT_CLIENT_ID`/`_SECRET`/`_REDIRECT_URL`/`_TENANT_ID`, the last defaulting to `"common"` so both personal and work/school Microsoft accounts can sign in unless the operator pins it to one tenant).
- **`internal/handlers/auth.go`**:
  - `findOrCreateOAuthUser`: added a `"microsoft"` case mapping to the new `microsoft_id` column.
  - New `microsoftEndpoint(tenant)` — `golang.org/x/oauth2` has no premade Microsoft/Azure endpoint (unlike its `google`/`facebook` sub-packages), so the v2.0 authorize/token URLs are built directly from the tenant.
  - New `microsoftConfig()`, `MicrosoftLogin` (`GET /auth/microsoft`), `MicrosoftCallback` (`GET /auth/microsoft/callback`) — same state-cookie CSRF check, token exchange, and `finishLogin` flow as Google/Facebook.
  - Callback calls Microsoft Graph's `/v1.0/me` for `id`/`displayName`/`mail`, falling back to `userPrincipalName` when `mail` is null (common for personal Microsoft accounts). No profile picture is passed to `findOrCreateOAuthUser` — Graph only exposes the photo via a separate authenticated binary endpoint, not a plain URL like Google/Facebook provide, so Microsoft sign-ins get the same generated-avatar fallback as local accounts. This is called out as a known, intentional limitation, not a bug.
  - `Config()` (`GET /auth/config`): added `"microsoftEnabled": h.Cfg.MicrosoftClientID != ""`.
- **`internal/handlers/router.go`**: registered `GET /auth/microsoft` and `GET /auth/microsoft/callback`.
- **Frontend** (`app/index.html`, `app/photo.html`, `app/app.js`): added a "Sign in with Microsoft" button (four-square Microsoft logo) next to the existing Google/Apple buttons, gated on `authConfig.microsoftEnabled`; extended the OAuth-divider's `x-show` condition to include it; added `microsoftEnabled: false` to all six `authConfig` default objects in `app.js`. `GET /auth/config`'s response is assigned to `authConfig` wholesale in `init()`, so no other frontend wiring was needed. Note: Facebook itself has no frontend button despite a working backend (a pre-existing gap from before this plan, left untouched — out of scope for this session).
- **`README.md`**: added a full "Microsoft Sign-In Setup" section (Azure AD app registration, client secret, `User.Read` Graph permission, credential table) mirroring the Facebook section, updated the "four login methods" intro to five, and added the four `MICROSOFT_*` variables to the "Full auth environment variables" table.

### Testing notes
- No Go toolchain is available in this sandbox (no `go` binary, and no network egress to `go.dev` to install one), so `go build ./...` / `make build` could not be run to confirm compilation.
- Verified by manual read-through instead: all braces/blocks in the new `auth.go` code close correctly; every identifier used (`oauth2`, `fmt`, `json`, `io`, `slog`, `uuid`, `http`, `httprouter`) is already imported at the top of the file — no new imports were required since Microsoft's endpoint is built from `oauth2.Endpoint{AuthURL, TokenURL}` directly rather than a provider sub-package.
- **Recommended follow-up**: run `make build` (or `go build ./...`) locally before deploying, since this couldn't be verified in-session.

### Open items
- Facebook Login has a working backend but no frontend button — noticed while wiring up Microsoft's button, but it predates this plan and wasn't part of the Phase 4 scope.
- Build not verified locally (see Testing notes) — please run `make build` before deploying.

## Session 33 — Facebook Sign-In Frontend Button

### What was done
Follow-up to Session 32: confirmed (by reading the "Added Facebook signin." commit's diff directly) that the Facebook backend work touched only `README.md`, `config.go`, `auth.go`, `router.go`, and the migration — no frontend file was part of that commit, so this was a full omission of the UI piece rather than a one-line miss. Closed that gap now, mirroring the Google/Apple/Microsoft buttons already in place:

- **`app/index.html`** and **`app/photo.html`**: added a "Sign in with Facebook" button, gated on `authConfig.facebookEnabled`, positioned between Apple and Microsoft (matching the provider order used in `README.md`'s "five login methods" list). Styled as a solid Facebook-blue (`#1877F2`) button with a white "f" glyph, matching the pattern of Apple's solid-black button rather than Google/Microsoft's bordered-white style. Extended both files' OAuth-divider `x-show` condition to include `authConfig.facebookEnabled`.
- **`app/app.js`**: added `facebookEnabled: false` to all six `authConfig` default objects (same six spots updated for Microsoft in Session 32). No other JS changes needed — `GET /auth/config`'s response (which already includes `facebookEnabled` server-side, added back when Facebook's backend was built) is assigned to `authConfig` wholesale in each page's `init()`.

### Testing notes
- Verified via `grep`/Python HTML scan that both `index.html` and `photo.html` now reference `authConfig.facebookEnabled` exactly twice each (button `x-show` + divider `x-show`), with no stray/unclosed tags introduced.
- Same caveat as Session 32: no Go changes were made here (frontend-only), so no build verification was needed.

### Open items
- None.

## Session 34 — Phase 7: Photo Uploads

### What was done
Implemented PLAN.md Phase 7 (title bar upload icon, upload popup, backend upload endpoint) end to end.

**New shared package `internal/photoimport/exif.go`** — pulls the EXIF-field list, `ExtractEXIF`, `ImageDimensions`, and `MergeLabels` out of `cmd/import-photos/main.go` into their own package (exported `Label{Name, Value}` type) so the CLI importer and the new browser upload endpoint use the exact same metadata logic instead of two copies drifting apart. Deliberately excludes `cmd/import-photos`'s face-detection code (`detectIsPublic`) — that depends on `gocv` (OpenCV cgo bindings), a heavy native dependency intentionally not linked into the main server binary. This is documented in the package doc comment.

**`cmd/import-photos/main.go`** — refactored to delegate to the shared package: `imageDimensions`, `extractEXIF`, and `mergeLabels` are now thin wrappers converting between the CLI's local lowercase-field `label` type and `photoimport.Label`. Removed the now-redundant `exifFields` var and the blank `image/gif`, `image/jpeg`, `image/png`, and `bytes`/`exif` imports (decoding now happens inside `internal/photoimport`). Behavior is unchanged — this was a pure refactor, verified by reading through the diff line by line since there's no Go toolchain available to run the CLI's own tests here.

**`internal/handlers/upload.go`** (new) — `UploadPhotosHandler` serving `POST /api/v1/photos/upload`:
- Requires auth + `PermPhotoCreate` (already granted to the `Contributor` role → `LoggedIn` by `scripts/seed-exhibition.sh`, so this works for any logged-in user out of the box, no new seed/migration needed).
- Parses a multipart form: one or more files under a `files` field, plus an optional `labels` field (JSON array of `{"name","value"}`, reusing `models.AddLabelRequest`) applied to every photo in the request.
- Per file: sniffs content type (JPEG/PNG/GIF only — deliberately narrower than the emoji/avatar upload endpoints, since photos need real pixel dimensions and Go's standard library has no WebP decoder, and `photos.image_width/image_height` are `NOT NULL`), decodes dimensions and EXIF via `internal/photoimport`, saves the file under `Cfg.UploadDir` with a UUID filename (same convention as avatar/emoji uploads), then inserts the `photos` row plus all labels (EXIF-derived + a computed `Resolution` label + batch labels) inside one DB transaction so a photo is never left committed without its labels or vice versa. New photos are always `is_public=false` — no face-detection reuse (see above), matching the safe default `cmd/import-photos` itself falls back to without `--cascade`.
- Every file is independent: one file's failure (bad format, decode error, DB error) doesn't abort the rest of the batch. Response is `{"results": [{"filename","photoid","status","error"}, ...]}` per PLAN.md's spec.
- Failed inserts clean up their saved file so errors don't leave orphaned uploads on disk.
- New models in `internal/models/models.go`: `UploadResult`, `UploadPhotosResponse`.
- Registered in `internal/handlers/router.go` as `POST /api/v1/photos/upload` behind the existing `auth()` wrapper.

**Frontend** (`app/app.js`, `app/index.html`, `app/photo.html`):
- New `Alpine.store('upload', uploadStore())` in `app.js` — a global store (not a per-component `Alpine.data`) because the queue needs to be reachable both from the nav icon's drag-and-drop handler and the popup itself, and should keep tracking uploads if the popup is closed and reopened. Holds `open`, `queue` (`{id, file, filename, status, progress, error, photoid}`), and batch `labels`, plus `addFiles`, `addLabel`, `removeLabel`, `retry`, `removeItem`, `clearFinished`.
- Uploads go out as one `XMLHttpRequest` per file (not one multipart POST for the whole batch) specifically so each queue row gets a real per-file progress percentage from `xhr.upload.onprogress`, per PLAN.md 7b's requirement — `fetch()` has no upload-progress event. Up to 3 files upload concurrently (`maxConcurrent`); the rest wait as `pending` until a slot frees.
- Batch labels are read fresh at the moment each file's XHR actually fires, not when the file was queued — so adding/editing/removing a label while other files are mid-upload correctly still applies to anything not yet sent, per spec. Auth headers (`getAuthHeaders()`, the existing test-user-switcher `X-User-ID` mechanism) are attached to each XHR the same way regular `fetch()` calls already do.
- Nav icon (both `index.html` and `photo.html`, same upload-arrow glyph already used for avatar uploads): click opens the popup; dragging files directly onto the icon also opens it and immediately starts uploading, per PLAN.md 7a.
- Popup (added to both pages, following the exact `<template x-if="$store.ui.labelModal">` backdrop-modal pattern already used elsewhere): drop zone + click-to-browse file input, a batch label editor (add/remove chips), and the queue list with a progress bar per file, status text (waiting/uploading %/done/error), and a retry button on failures.

### Scope decisions (called out explicitly, not hidden)
- **No face-detection/is_public automation** for uploads — new photos default to private (`is_public=false`); the operator can flip individual photos public via the existing admin panel. Reusing `cmd/import-photos`'s OpenCV-based detection would mean linking `gocv` into the main server binary, a real native-dependency/build-size tradeoff PLAN.md's Phase 7 text didn't ask for (it only specified reusing EXIF extraction).
- **No WebP uploads** — Go's standard library can't decode WebP dimensions, and `photos.image_width/height` are `NOT NULL`. JPEG/PNG/GIF only, matching what `cmd/import-photos` has always supported.
- **Label-on-upload colors**: PLAN.md notes this feature "benefits from Phase 5a (label colors) but is not blocked by it" — 5a (DB-backed label color overrides) still isn't built (see Session 31's audit), so batch labels render with the existing client-side hash-based color fallback, same as labels added anywhere else in the app today. Nothing extra needed here; will automatically pick up real colors once 5a lands.

### Testing notes
- No Go toolchain is available in this sandbox (confirmed again: no `go` binary, no network egress to install one), so `go build ./...` could not be run. Reviewed every changed/new Go file by hand instead: `internal/photoimport/exif.go`, the `cmd/import-photos/main.go` diff, `internal/handlers/upload.go`, `internal/models/models.go`, and the `router.go`/handler-wiring changes — checked import lists against actual usage, confirmed the transaction/permission-check pattern matches `galleries.Create` exactly, and confirmed no naming collisions with existing package-level identifiers in `internal/handlers`.
- Frontend changes were checked programmatically: `node --check app/app.js` passes (valid JS syntax), and a tag-balance scan confirmed `<div>`/`</div>` and `<template>`/`</template>` counts match in both `index.html` (63/63, 17/17) and `photo.html` (176/176, 96/96) after the edits.
- **Recommended follow-up**: run `make build` (or `go build ./...`) locally, then a real end-to-end upload test (drag a JPEG with EXIF data onto the nav icon, confirm the photo, its EXIF labels, and the Resolution label all show up correctly) before relying on this in production.

### Open items
- Build not verified locally — please run `make build` before deploying (same caveat as Sessions 32/33).
- Face-detection-based is_public and WebP support are known, intentional gaps — flagged above, not oversights.

## Session 35 — Fixed: Batch Labels Didn't Reach Photos Already Uploaded

### What was done
Bug report following Session 34: the upload popup's helper text said "Applied to every photo not yet uploaded," which was accurate but not what was wanted — labels added (or removed) after some photos in the batch had already finished uploading never reached those already-done photos at all, since the original design only attached the batch label snapshot to a photo's own upload request at the moment it was sent.

Reworked `uploadStore` in `app/app.js` so batch labels are no longer sent as part of the initial `POST /api/v1/photos/upload` request at all. Instead:
- The moment a file's upload finishes, `_syncItemLabels(item)` reconciles that photo's labels against whatever the batch label list currently shows — POSTing anything missing, via the same `/api/v1/labels` endpoint the regular per-photo label editor uses.
- `addLabel()` and `removeLabel()` now call `_syncAllDone()`, which re-runs that same reconciliation against every photo already marked `done` in the queue — so editing the batch label list after photos have uploaded pushes the change out to them immediately, not just to whatever's still pending.
- Each queue item tracks its own `appliedLabels` map (`"name value" -> labelid`) — the record of which labels this code has actually applied to that specific photo. This is what makes label *removal* possible after the fact: since we're the ones who POSTed each label (not the upload endpoint's own atomic insert), we always know the exact `labelid` to `DELETE` when a batch label is taken back out, without needing to guess or re-fetch and match by name/value.
- Label sync is best-effort and fire-and-forget: a failed add/remove simply isn't recorded in `appliedLabels`, so the very next label edit retries it automatically. It doesn't block the upload from being marked `done`, and it keeps running in the background even if the popup is closed (the store persists independent of the popup's visibility), so it isn't strictly limited to "while the popup is open."
- Updated the helper text under the label editor in both `app/index.html` and `app/photo.html` from "Applied to every photo not yet uploaded" to "Applied to every photo in this batch, including ones already uploaded."

No backend changes were needed — this reuses the existing `POST /api/v1/labels?photoid=` and `DELETE /api/v1/labels/:labelid` endpoints exactly as the rest of the app already does. Permission-wise this works with no extra grants: the same logged-in user who uploaded the photo is also the one whose session POSTs each label, so `added_by_userid` matches the caller and they can delete their own labels later without needing `PermLabelAdmin` (see `labels.go`'s existing ownership check).

### Testing notes
- `node --check app/app.js` passes.
- Re-ran the `<div>`/`<template>` tag-balance scan on both HTML files — unchanged counts (only the helper-text copy changed, no markup structure).
- Not able to run an end-to-end upload test in this sandbox (no Go toolchain, no running Postgres instance) — same caveat as Session 34. Recommend manually verifying: upload a photo, wait for it to show "Done," then add a label and confirm it appears on that photo (via the normal photo page) without needing to re-upload; then remove the label from the batch editor and confirm it disappears from that same already-uploaded photo.

### Open items
- Label sync failures are silent (logged nowhere, just retried on the next edit) — acceptable for a best-effort background sync, but if labels seem to lag behind what's shown, that's the mechanism to check first.
- Still pending the same `make build` verification called out in Session 34.

## Session 36 — Add Filename Label to Uploaded Photos

### What was done
Small follow-up: `internal/handlers/upload.go`'s `uploadOne()` now adds a `Filename` computed label (alongside the existing `Resolution` one) holding the original uploaded file's name (`fh.Filename`, the browser-supplied filename from the multipart part) — merged in via the same `photoimport.MergeLabels` call already used for `Resolution`, so it takes precedence over any same-named EXIF tag exactly the way `Resolution` already does (moot in practice — EXIF has no standard "Filename" field — but keeps the precedence consistent).

Scoped to the browser upload endpoint only, not `cmd/import-photos`: the CLI imports from URLs, not local files, so it has no equivalent "filename" (it derives a *title* from the URL basename, which is a different, pre-existing thing). If a similar label is wanted there too, that's a separate, explicit change.

### Testing notes
- Read through the edit in place; it's a two-line addition to an existing, already-reviewed slice literal — no new imports, no signature changes, no other call site depends on the exact label set (confirmed via grep for any other reference to the `"Resolution"` label name).
- Same build-verification caveat as Sessions 34/35 — no Go toolchain available in this sandbox.

### Open items
- None.

## Session 37 — Fixed: Filename Label Missing on Photos With Rich EXIF

### What was done
Bug report: the `Filename` label (added in Session 36) showed up on uploads with no EXIF data, but was missing on uploads that did have EXIF data — even though the upload itself succeeded and other labels were present.

Root cause: it wasn't a `MergeLabels` logic bug (that function was working exactly as designed) — it was a display-pagination issue. The photo detail page shows only the *first page* of labels (`internal/handlers/photo.go`'s `labelLimit = 10`), ordered by `created_at` (`fetchLabels` in `internal/handlers/fetch.go`). Labels are inserted in `uploadOne()` in `allLabels`' slice order, and `photoimport.MergeLabels(exifLabels, computed)` — called with EXIF as the base and `{Resolution, Filename}` as the extra — appends non-colliding extra entries *after* all base entries. So `Resolution` and `Filename` were always the last labels inserted, and therefore had the latest `created_at` of the whole set. A camera photo can easily produce 10+ EXIF labels (Camera Make, Camera Model, Lens Make, Lens Model, Shutter Speed, Aperture, ISO, Focal Length ×2, Flash, White Balance, Exposure Mode, Exposure Program, Artist, Copyright, Software, Description, Date Taken, GPS — up to 19 possible), which pushed `Filename` (and `Resolution`, same latent bug, just not yet reported) past the first page's `LIMIT 10` and out of view unless you paginate. A photo with no EXIF has only 2 labels total, so both trivially fit on page 1 — matching exactly the reported "present without EXIF, missing with EXIF" symptom.

Fix, in `internal/handlers/upload.go`: added `prioritizeLabels(labels, priorityNames...)`, which reorders a label slice so labels matching the given names (in that order) come first, with everything else keeping its existing relative order afterward. `uploadOne()` now calls `allLabels = prioritizeLabels(allLabels, "Filename", "Resolution")` right before the insert loop, so both computed labels are always inserted — and therefore always land on the label list's first page — first, regardless of how many EXIF tags a given photo has. Applied the same fix to `Resolution` too since it's the identical bug, just not yet noticed; no reason to leave it half-fixed.

This is a targeted fix, not a rework of label pagination generally — a photo with enough *batch* labels (user-typed, unbounded count) could still theoretically push some of those past page 1, but that's a pre-existing, separate characteristic of the label list's pagination design, not something this bug report was about.

### Testing notes
- Traced the exact insertion order by hand: confirmed `exifFields` in `internal/photoimport/exif.go` has no entry named "Resolution" or "Filename" (so there's never a same-name collision to reason about), confirmed `MergeLabels`'s append-order behavior, and confirmed `fetchLabels`'s `ORDER BY l.created_at LIMIT $2 OFFSET $3` is what the photo page uses by default (`labelLimit = 10` in `photo.go`) — this chain is what reproduces the reported symptom exactly based on EXIF tag count.
- Re-read the edited file in full; `prioritizeLabels` is a pure, self-contained function with no new imports needed (`strings` was already imported).
- Same caveat as prior Phase 7 sessions: no Go toolchain in this sandbox, so `go build` still hasn't been run — recommend verifying with a real photo that has a large EXIF profile (10+ tags) once built.

### Open items
- Still pending the `make build` verification called out in Sessions 34–36.

## Session 38 — Infinite-Scroll Label Loading on the Photo Page

### What was done
Follow-up to Session 37's investigation: the photo detail page (`GET /api/v1/photo`) has always capped its embedded label list at the first page (`labelLimit = 10` in `internal/handlers/photo.go`), and set `photo.labelsurl` (a working, paginated `/api/v1/labels?...` URL) whenever there were more — but the frontend never consumed `labelsurl` at all, so any labels past the first 10 were silently unreachable from the photo page. Session 37 fixed *which* labels land in that first page; this session fixes the underlying "extra labels vanish" problem generally, per the request: show as many labels as fit, and load the rest on scroll instead of capping them.

Implemented in `app/app.js` (`photoApp`) and `app/photo.html`, reusing the exact infinite-scroll pattern the photo wall (`wallApp`) already uses:
- Added an invisible sentinel `<div x-ref="labelsSentinel">` right after the label chips (and the "add label" button) in the Labels card in `photo.html`.
- `photoApp.loadPhoto()` now calls a new `_observeLabelsSentinel()` after every photo load (via `$nextTick`, since the sentinel lives inside the page's `x-if="photo && !loading"` block and is therefore destroyed and rebuilt on every photo navigation — reusing a stale `IntersectionObserver` target from a previous photo would silently stop working, so `_observeLabelsSentinel()` always disconnects any previous observer first and attaches a fresh one to the current sentinel node).
- New `loadMoreLabels()` fetches `photo.labelsurl`, appends the returned labels to `photo.labels`, and updates `photo.labelsurl` to the response's `pages.next` (or `null` once exhausted) — so scrolling keeps loading additional pages until all of a photo's labels are loaded, then stops.
- No backend changes were needed: `GET /api/v1/labels?photoid=...` (`LabelsHandler.List`) already supported offset/limit pagination and already returns a `pages.next` URL; the photo detail response already exposed the first `labelsurl` link. This was purely a "the frontend never wired up pagination that already existed" gap.

Left `labelLimit = 10` (the initial page size) unchanged — the fix that matters is that nothing is silently lost anymore, not the exact size of the first page. Also left the near-identical `photo.emojisurl` pagination unwired, since the request was specifically about labels; that's a separate, pre-existing gap of the same shape, not something to fix as a drive-by.

### Testing notes
- `node --check app/app.js` passes.
- Tag-balance scan: `photo.html`'s `<div>`/`</div>` count moved from 176/176 to 177/177 (exactly the one new sentinel `<div>`), confirming no other markup was accidentally left unbalanced.
- Traced the reconnect-on-navigation logic by hand since it's the trickiest part: `x-if="photo && !loading"` unmounts the whole content block (including the sentinel) every time `loading` flips to `true` during a photo-to-photo navigation, so a one-time observer setup in `init()` alone would only work for the very first photo loaded, not subsequent ones reached via search or the related-photos sidebar — hence re-running `_observeLabelsSentinel()` at the end of every `loadPhoto()` call, not just once at startup.
- Not able to run this in a browser in this sandbox (no running server/Postgres). Recommend manually verifying: open a photo with 15+ labels (or add enough via the label editor), confirm only the first ~10 render initially, then scroll down and confirm the rest load in automatically without needing a manual "load more" click.

### Open items
- `photo.emojisurl` has the identical un-wired-pagination shape; flagged for awareness, not fixed here (out of scope for this request).
- Still pending the `make build` / end-to-end verification called out in prior Phase 7 sessions.

## Session 39 — Phase 5a & 5b: Label Colors and Restricted Labels

### What was done
Implemented both remaining Phase 5 sub-phases together, since PLAN.md's two drafts (a `label_name_colors` table for 5a, columns added to a `label_names` table for 5b) describe attributes of the same underlying concept — a label *name*, not an individual label row — and unifying them avoided two overlapping tables.

**Migration** (`migrations/016_label_names.sql`): new `label_names` table, one row per distinct label name — `color_hex` (nullable, `CHECK`-constrained to `#rrggbb`), `restricted` (`BOOLEAN NOT NULL DEFAULT FALSE`), plus the standard `created_at`/`updated_at` + `trg_set_updated_at` trigger. A name only gets a row once something explicitly sets one of these (EXIF import, `--restrict-labels`, or the new admin PATCH endpoint) — no row means "no color override, not restricted," which is exactly what the zero values already mean.

**Backend:**
- `internal/models/models.go`: `Label` gained `ColorHex *string` (json `color`, omitempty) and `Restricted bool`; new `LabelNameInfo{Name, ColorHex, Restricted}` type.
- `internal/handlers/fetch.go`: `fetchLabels` now `LEFT JOIN`s `label_names` so every label a photo page loads carries its name's color/restricted state.
- `internal/handlers/labels.go`: added `fetchLabelNameInfo`/`isLabelNameRestricted` helpers. `Create`, `Update`, and `Delete` all now check the relevant label name(s) against `label_names.restricted` and reject with 403 unless the caller holds `Admin` or `LabelAdmin` (`Update` checks both the existing name and, on rename, the new name). `Names()` (`GET /api/v1/label-names`) now returns `{name, color, restricted}` objects instead of bare strings. New `UpdateName` handler (`PATCH /api/v1/label-names?name=`, Admin/LabelAdmin only) upserts a name's `color_hex` and/or `restricted` — an empty `color_hex` clears the override back to the frontend's deterministic hash color.
- `internal/handlers/router.go`: registered the new `PATCH /api/v1/label-names` route.
- `internal/photoimport/exif.go`: new `Names(labels)` helper and `MarkNamesRestricted(ctx, db, names)`, which upserts each name into `label_names` with `restricted = TRUE` (never un-restricts). Takes a small `dbExecer` interface so it works against both a bare pool and a transaction.
- `internal/handlers/upload.go`: `uploadOne()` now calls `MarkNamesRestricted` with the photo's EXIF-derived label names, inside the same transaction as the photo/label inserts — so a photo and its restricted-name bookkeeping commit or roll back together. Implements "EXIF import always marks its labels restricted."
- `cmd/import-photos/main.go`: added `--restrict-labels` flag. EXIF-derived names are now *always* marked restricted after each successful (non-dry-run) fetch, regardless of the flag; when `--restrict-labels` is also set, `--label`-supplied names are marked restricted too (computed `Resolution`/`Public` names are never restricted). Wired into both the main per-URL insert path and the `--refresh-exif`-off fast path that patches labels onto an already-existing photoid without re-downloading.

**Frontend** (`app/app.js`, `app/photo.html`):
- New `window._isLabelAdmin` global (mirrors the existing `window._currentUser` pattern) plus `getIsLabelAdmin()` helper, kept in sync by `photoApp.refreshPermissions()` — fetches `GET /api/v1/permissions` and checks its `summary` for `Admin`/`LabelAdmin`. Called on init, login, logout, and test-user switch (every point `currentUser` changes).
- Label chips: background color now prefers `label.color` (the admin-set override) over `labelColorFor(name)`'s hash-based fallback; a lock icon renders on restricted labels; the edit/delete popup only opens for non-restricted labels or for Admin/LabelAdmin.
- Admin/LabelAdmin users additionally see an inline `<input type="color">` swatch in the chip popup, wired to a new `setLabelColor(label, colorHex)` method (`PATCH /api/v1/label-names`) that applies the new color to every currently-loaded label sharing that name.
- The add/edit label modal's name dropdown (`labelEditor` in `app.js`) now consumes the `{name,color,restricted}` object shape from `GET /api/v1/label-names`; restricted names show a lock glyph and are `disabled` in the `<select>` for non-admins (defense in depth — the backend is the actual enforcement point).

### Testing notes
- `node --check app/app.js` passes.
- `photo.html` tag-balance scan (`div`/`template`/`span`) all balanced (177/177, 98/98, 74/74).
- Manually traced every new/edited Go code path line by line (handlers, models, router, photoimport, cmd/import-photos) for type correctness and call-signature matches — no Go toolchain available in this sandbox (no `go` binary, no network egress to install one), so this is review, not compilation.
- Confirmed `github.com/jackc/pgx/v5/pgconn` (used by `MarkNamesRestricted`'s `dbExecer` interface) needs no new `go.mod`/`go.sum` entry — it's a sub-package of the already-required `pgx/v5` module.
- Confirmed `permissions.Checker.HasAny` (used throughout the new restricted-label checks) already existed prior to this session with the exact signature called.

### Open items
- `make build` / `go vet` still needs to be run in an environment with the Go toolchain before deploying — this is the accumulated caveat across every Go-touching session so far.
- No UI was added for manually toggling a label name's `restricted` flag on/off (only for setting color) — 5b's requirements describe restriction as applied automatically (EXIF import, `--restrict-labels`), not as something admins toggle by hand, so this was left out as out of scope. `PATCH /api/v1/label-names` supports a `restricted` field already if that's wanted later — it would just need a small UI affordance.
- End-to-end verification (upload a photo with EXIF, confirm its labels show as restricted and locked to non-admins; run `import-photos --restrict-labels`, confirm `--label`-supplied names get restricted too; confirm the color picker round-trips) still needs a real Postgres + running server, unavailable in this sandbox.

## Session 40 — Fixed: Label Edit/Delete Buttons Did Nothing

### What was done
Bug report: clicking the Edit or Delete icons in a label chip's popup had no visible effect at all — not even closing the popup.

Root cause: `app/alpinejs.min.js` is Alpine's CSP-restricted build (confirmed by decompiling its bundled expression evaluator — a hand-written tokenizer/parser/interpreter with no `new Function` anywhere, matching the existing comment in `app.js` about "the Alpine CSP evaluator"). That evaluator's `Parser.parse()` parses exactly *one* expression, optionally swallows a single trailing `;`, and then throws `Unexpected token: ...` if anything else follows. It has no support for a semicolon-separated sequence of statements (no `Program`/`ExpressionStatement`/`SequenceExpression` node types exist in the evaluator at all). Both label buttons were written as two statements joined by `;`:
```
@click.stop="open = false; openLabelModal(label)"
@click.stop="open = false; deleteLabel(label)"
```
Since the whole expression fails to *parse* (not just to run), Alpine's expression-builder throws before either statement executes — so neither `open = false` nor the actual edit/delete call ever ran. This is a pre-existing bug, not something introduced by the Phase 5a/5b work (confirmed via `git diff` against the pre-Phase-5 commit — the two lines were byte-for-byte identical before and after that change) — it simply had never been exercised end to end before, since every session so far lacked a live Postgres/browser to test against.

Grepped the entire `app/*.html` set for the same shape (`@directive="...;..."`) and found two more, identical-cause instances: the hidden file `<input type="file" @change="...">` used to feed dropped/selected files into the upload queue, in both `app/index.html` and `app/photo.html`:
```
@change="$store.upload.addFiles($event.target.files); $event.target.value = ''"
```
This means **file-picker-based photo uploads have likely never worked either** — `addFiles()` (which actually queues the files) never ran, only ever failing to parse silently. (Drag-and-drop uploads, if wired to a separate `@drop` handler rather than this `<input>`'s `@change`, would not be affected — worth confirming separately.)

Fixed all four using the exact idiom already established elsewhere in this same codebase for exactly this constraint (e.g. `authModal`'s `(userModal = false) || showToast(...)` and `labelEditor`'s `(nameIsOther = false) || (selectedName = '')`): replace the `;`-joined statements with a single expression using `||`, relying on the first statement's result being reliably falsy (an assignment evaluates to the assigned value; `false` is falsy, and `addFiles()` has no explicit `return` so it's always `undefined`, also falsy) so the right-hand side is guaranteed to run:
```
@click.stop="(open = false) || openLabelModal(label)"
@click.stop="(open = false) || deleteLabel(label)"
@change="$store.upload.addFiles($event.target.files) || ($event.target.value = '')"
```
Verified via the decompiled parser that parenthesized assignment expressions are valid grouped primaries in this evaluator, so `(open = false)` parses correctly as the left operand of `||`.

### Testing notes
- Decompiled and read the relevant sections of the minified `alpinejs.min.js` bundle (Tokenizer, Parser, and evaluator classes) directly to confirm the exact failure mode rather than guessing — confirmed `parse()`'s single-trailing-semicolon-then-EOF-or-throw behavior and confirmed the evaluator's `AssignmentExpression`/`BinaryExpression` (`&&`/`||`) node handling, which is what makes the `(x = v) || sideEffect()` idiom work.
- Grepped all of `app/*.html` for the pattern `@directive="...;..."` both before and after the fix — found exactly these 4 instances before, 0 after.
- `node --check app/app.js` passes (unaffected — no `.js` file changes this time, only the two `.html` files).
- Tag-balance scan on both edited HTML files (`div`/`template`/`span`) unchanged and balanced (`photo.html` 177/177, 98/98, 74/74; `index.html` 63/63, 17/17, 22/22) — these were pure attribute-value edits, no markup structure changed.
- Still unable to click-test in an actual browser in this sandbox (no running server/Postgres) — recommend the user retest Edit/Delete on the photo page, and also retest that clicking to browse/select files for upload (not just drag-and-drop, if that's wired separately) now actually queues them.

### Open items
- Confirm whether drag-and-drop upload (if it has its own separate `@drop` handler elsewhere) was affected by the same bug or is unrelated — not checked this session, only the `<input type="file">` `@change` path.
- Given this exact class of bug (silently-failing multi-statement CSP expressions) has now been found three additional times beyond the two pre-existing correct examples, it may be worth a one-time full audit of every `@`-prefixed directive in `app/*.html` for stray semicolons whenever there's next a lull — this session's grep only searched for the literal `;` character inside quoted directive values, which should catch all remaining cases, but wasn't cross-checked against every possible directive prefix (e.g. `x-on:`-spelled-out equivalents, of which there don't currently appear to be any in this codebase).
- Still pending the accumulated `make build` / end-to-end verification caveat from every prior Go-touching session.

## Session 41 — Upload Permission: Error Instead of False "Done", Gate the Upload Icon

### What was done
Bug report: a user without permission to upload photos saw a green "Done" progress bar instead of an error.

Reviewed the existing upload path in detail (`uploadStore._upload()` in `app.js`) before changing anything: the per-file XHR's `onload` handler already only marks an item `'done'` when `xhr.status` is in the 2xx range *and* the server's per-file `results[0].status === 'ok'` — any other case (including a top-level 403, which is exactly what `POST /api/v1/photos/upload` returns when the caller lacks `PhotoCreate`, before it ever builds a per-file results array) already falls through to `item.status = 'error'` with a message pulled from the response body. That logic was already correct on inspection, so the reported "Done" a user saw was most likely from a test account that *does* hold `PhotoCreate` — worth double-checking, since the seed script (`scripts/seed-exhibition.sh`) grants the Contributor role (which includes `PhotoCreate`) to *every* logged-in user via a blanket `entity_type = 'LoggedIn'` grant, not just to specifically-assigned Contributors. A logged-in user who should be upload-restricted needs a role setup that doesn't include that blanket grant.

Regardless of that root cause, implemented both of the requested changes directly, since they're worth having independent of exactly how the report reproduced:

**Backend** (`internal/handlers/upload.go`): changed the 403 body from the bare word `"forbidden"` to `"you do not have permission to upload photos"` — surfaces directly in the queue item's error line via the existing `item.error = ... || data.error || ...` fallback chain, no frontend change needed for this part.

**Frontend — preventive gating**, mirroring the `isLabelAdmin`/restricted-label pattern from Session 39:
- New shared helper `fetchPermissionSummary(authHeadersFn)` in `app.js` (factored out since both `photoApp` and `wallApp` need it identically).
- `photoApp.refreshPermissions()` now also sets `canUploadPhotos = summary.includes('PhotoCreate')` (exactly mirroring the backend's own check — deliberately *not* OR'd with `'Admin'`, since Admin only gets upload rights because the seed script separately grants it `PhotoCreate` explicitly; frontend and backend should agree on the exact same permission string).
- `wallApp` (the photo wall / `index.html`) didn't have any permission-refresh machinery before this — added its own `canUploadPhotos` state and a trimmed-down `refreshPermissions()` (it doesn't need `isLabelAdmin`, so didn't duplicate that half), called from `init()`, `selectTestUser()`, `logout()`, and the `photoapp:auth-success` listener — the same four points `photoApp` already refreshes permissions from.
- `app/photo.html` and `app/index.html`: the upload icon button, its `:class` (greyed out vs. clickable), and its drag-and-drop handler are now all gated on `loggedInUser && canUploadPhotos` instead of just `loggedInUser`; a logged-in-but-unauthorized user now sees a permanently greyed-out icon (matching the "not logged in" visual) and its `:title` explains why ("You do not have permission to upload photos"), and the popup itself (`$store.upload.open`) can no longer be reached by clicking or dropping files on the icon. The upload popup's own internal drop-zone (once already open) wasn't re-gated — it has no other way to become reachable than through the now-gated icon, and the backend's 403 is still there as a backstop regardless.

### Testing notes
- `node --check app/app.js` passes.
- Tag-balance scans (`div`/`template`/`span`/`button`) on both edited HTML files unchanged and balanced.
- Re-ran the Session 40 stray-semicolon grep across all of `app/*.html` after these edits — still zero matches, confirmed no new instances of that bug class were introduced.
- Manually re-traced `uploadStore._upload()`'s `xhr.onload` branch against a simulated 403 response shape (`{"error": "you do not have permission to upload photos"}`, no `results` array) to confirm it still correctly falls through to the error branch — unchanged code, but worth re-verifying given it's central to this report.
- No live server/Postgres available in this sandbox, so the actual end-to-end scenario (log in as a role without `PhotoCreate`, confirm the icon is greyed out, and — if somehow still reachable — confirm a rejected upload shows red "Error" with the new message) still needs to be tried by hand.

### Open items
- If the user still sees "Done" for a genuinely non-permitted account after this, the next thing to check live (browser dev tools Network tab) is the actual HTTP status/response body of that account's `POST /api/v1/photos/upload` call — if the server itself is returning 2xx with `results[0].status: "ok"`, the bug is a permissions-grant/seed-data issue (that account unexpectedly holds `PhotoCreate`), not a frontend display bug.
- Still pending the accumulated `make build` / end-to-end verification caveat from every prior Go-touching session.

## Session 42 — Root Cause: Uploaded Photo Invisible Even When "Done" Was Correct

### What was done
Follow-up to Session 41: the user clarified the actual symptom more precisely — regardless of whether the uploader held `PhotoCreate` or not, the queue showed "Done" but the photo genuinely never showed up anywhere afterward. That ruled out Session 41's theory (a false-positive "Done" from a misread server response) — the upload really was succeeding server-side, so the question became "why is a real, successfully-created photo invisible?"

Traced it to `internal/handlers/upload.go`'s `uploadOne()`: every browser-uploaded photo is inserted with `is_public = FALSE` unconditionally (a deliberate choice — the browser endpoint has no face-detection screening, unlike `cmd/import-photos --cascade`, so it defaults to the same safe "not public" fallback that CLI imports use without a cascade file). Every read path that lists or fetches photos filters private ones out with `(is_public OR $canSeePrivate)`, where `canSeePrivate` comes from the `PrivatePhotoView` permission — and per `scripts/seed-exhibition.sh`, only the `Admin` role has `PrivatePhotoView`; the `Contributor` role (auto-granted to every logged-in user via a blanket `entity_type = 'LoggedIn'` grant) does not. So: any ordinary logged-in user who uploads a photo gets it inserted successfully ("Done" was always correct), but then can't see it in the wall, can't fetch it directly by the `photoid` the upload response gave them, can't find it via search, and it won't show up as a "related" suggestion elsewhere — because they lack `PrivatePhotoView` and the photo is private. From the user's point of view this looks exactly like "the photo wasn't added," even though it fully exists in the database.

Fixed by adding an **owner exception** to every read path that filters on `is_public`: a photo is visible if it's public, if the caller holds `PrivatePhotoView`, *or* if the caller is the photo's own `owner_userid` — regardless of the `is_public` flag. This preserves the existing "uploads need admin review before going public" workflow (nothing about default visibility or the admin approval flow changed) while guaranteeing an uploader never loses track of their own photo. Applied to:
- `internal/handlers/photo.go`: `PhotoHandler.ServeHTTP` (both the `random=true` and `photoid=` fetch queries) and `ListPhotosHandler.ServeHTTP` (the wall listing).
- `internal/handlers/search.go`: `buildSearchSQL` gained a third fixed parameter (`$3 = currentUserID`, shifting the dynamically-numbered qualifier parameters to start at `$4` instead of `$3` — done by bumping the initial `argN`, so no hardcoded placeholder numbers elsewhere in the function needed to change).
- `internal/handlers/fetch.go`: `fetchRelated` and `fetchRelatedByLabel` (the photo detail page's "related photos" sidebar) both gained a `currentUserID` parameter with the same owner-exception clause.
- Deliberately left `internal/handlers/admin.go`'s photo list alone — it already returns every photo regardless of `is_public` (it's the admin moderation view), so it was never affected by this bug.

### Testing notes
- Manually re-verified every changed query's placeholder numbering by hand, since three of the five queries build SQL with positional parameters where getting the count/order wrong compiles fine in Go but fails or misbehaves at query time: `photo.go`'s two `PhotoHandler` queries (4 params each, `currentUser` last), `photo.go`'s `ListPhotosHandler` query (5 params), `fetch.go`'s two queries (4 and 5 params respectively), and `search.go`'s dynamically-built query (confirmed via `grep '\$[0-9]'` that only `$1`/`$2` were ever hardcoded elsewhere in the file, so bumping the starting `argN` from 2 to 3 for the new `$3` cleanly shifts every dynamically-generated placeholder without any leftover hardcoded reference colliding).
- Confirmed via `grep` that `fetchRelated`/`fetchRelatedByLabel`/`buildSearchSQL` each have exactly one call site (`photo.go` and `search.go` respectively), and updated both to pass the new parameter.
- No Go toolchain in this sandbox, so this is careful manual review, not a compile — `make build` is still the outstanding verification step, as in every Go-touching session so far.
- Still recommend an end-to-end check once buildable: log in as a plain Contributor (no `PrivatePhotoView`), upload a photo, and confirm it now appears on the wall and is directly viewable by its returned `photoid`, without needing an admin to first flip it public.

### Open items
- This does *not* change the default-private-until-reviewed workflow itself — an uploader can now always see their *own* pending photo, but it's still invisible to everyone else until an admin marks it public via the admin panel. That remains intentional, just now with less confusing UX for the uploader.
- Still pending the accumulated `make build` / end-to-end verification caveat from every prior Go-touching session.

## Session 43 — Fixed: Photo Wall Distorting Aspect Ratio for Mismatched Photo Sizes

### What was done
Bug report: on the photo wall, when photos in a row have very different native sizes (even at the same aspect ratio), the smaller ones render much smaller than intended, and in one case a landscape photo ended up squeezed into a portrait-shaped box. The user's reminder of the original algorithm was the key diagnostic: scale every photo in a row to the tallest photo's height, then scale the *whole row* to fit the display width — since one uniform factor scales both width and height together, aspect ratio can never change.

Traced `packRows()` in `app.js` and found it matched that description exactly, *except* for one addition: after computing the width-filling `rowHeight`, it independently clamped that height into `[MIN_ROW_H=80, MAX_ROW_H=400]` — but the CSS layer (`.wall-photo` is a flex item with `flex:${flexGrow} 1 0px` inside a `display:flex` row) always stretches every photo's box width to fill the *entire* row width regardless of that clamp, using flex-grow values computed from the *unclamped* height. So whenever the clamp actually changed the row's height from its natural value, every photo in that row got the same uniform "extra" stretch/squash factor between width and height — breaking exactly the one-scale-factor invariant the original design relied on. Confirmed with a small standalone Node simulation of the math (`box aspect ratio` no longer equaled `true aspect ratio` whenever the natural height fell outside 80–400px) — and a large size disparity between two same-aspect-ratio photos in a row is precisely what tends to push the natural row height to an extreme, since the nominal-width sum used to compute it isn't affected by resolution alone but the resulting height very much is once combined with a fixed container width. The trailing partial (last, incomplete) row had a related but separate issue: it forced the *same* full-width flex-fill as any other row even though there aren't enough photos to justify stretching it, causing the identical style of distortion there too.

Fixed by removing the independent height clamp entirely for full rows — `rowHeight` is now always exactly `maxNatH * rowScale`, so width and height are always scaled by the same factor, full stop. For the trailing partial row, rather than clamping+flex-filling (the buggy pattern), it's no longer stretched at all: each photo gets a fixed pixel width (`flex: 0 0 <px>`) computed from the same aspect-preserving math at a fixed target height, left-aligned with natural whitespace after it — the standard look for an under-full last row in any justified gallery. `packRows()` now returns a `stretch` boolean per row so `index.html`'s template can pick the right CSS (`flex:${flexGrow} 1 0px` when stretching to fill the container, `flex:0 0 ${widthPx}px` when not).

### Testing notes
- `node --check app/app.js` passes.
- `index.html` tag-balance scan (`div`/`template`/`span`/`a`) unchanged and balanced.
- Wrote and ran a standalone Node script re-implementing just the row-height/width math to numerically verify the fix: tested a row with a huge (4000×2000) and tiny (100×50) photo at the same 2:1 aspect ratio (both now render at identical computed aspect ratio, confirming the size-invariance the algorithm always intended), and a row mixing an extreme landscape (4000×2000, aspect 2.0) with an extreme portrait (60×600, aspect 0.1) — both came out matching their true aspect ratio to within rounding error, where previously a clamp-triggering case like this would have distorted both uniformly.
- No live browser available in this sandbox to visually confirm on an actual photo wall — recommend the user re-check a wall containing a wide mix of photo resolutions/aspect ratios, especially the previously-reported landscape-in-a-portrait-box case, and also glance at the trailing partial row (last, incomplete row of the wall) to confirm it now looks left-aligned/natural-width rather than stretched.

### Open items
- Removing the height clamp means a row's height is now purely a function of its photos' aspect ratios and the container width — a pathological input (e.g. several extreme-aspect-ratio portrait photos landing in the same row on a narrow viewport) could in theory produce an unusually tall row. This is an accepted tradeoff of restoring the original algorithm's exact aspect-preservation guarantee, per the user's explicit request, rather than a new bug — but worth knowing if an oddly tall row ever gets reported.
- Still pending the accumulated `make build` / end-to-end verification caveat from every prior Go-touching session (this particular fix is pure frontend, so it's actually testable without Go — just no browser available here).

## Session 44 — Confirmed Session 43's Fix Is Correct; Added Cache-Busting for app.js

### What was done
The user retested with four specific photos (1948×2310, 3688×2452, 1948×2310, 344×418) and reported sizes (145×183, 275×183, 145×183, 26×183) that still looked wrong, with expected sizes (124×147, 221×147, 124×147, 121×147).

Rather than re-guessing, extracted the *actual* `packRows` function straight out of the current `app/app.js` (via a small Node script that `eval`s the live source, not a hand reimplementation) and ran it against exactly those four photo dimensions at the container width implied by the user's own numbers (591px usable + 3×4px gaps = 603px). Result: `height: 147`, `displayWidth: [124, 221, 124, 121]` — an exact match for what the user says the sizes *should* be, not what they reported seeing. Separately, back-computing what algorithm *would* produce the reported (wrong) numbers showed they match dropping the aspect-ratio height-normalization step entirely — i.e. treating `flexGrow` as each photo's raw pixel width with no `maxNatH/height` correction at all, which is exactly the shape of bug Session 43 already fixed.

Conclusion: the fix in the repository is correct and already produces the right output for this exact case — the numbers the user saw match the *pre-fix* behavior byte-for-byte, which is a strong signal their browser was still running a cached copy of `app.js` from before Session 43 landed, not the current file.

To stop this class of confusion from recurring across future rounds of frontend changes (this is now the second report that turned out to be a stale-cache artifact), added a cache-busting version query string to every page's `app.js` script tag: `<script src="/app.js?v=44">` across all seven HTML files that load it (`index.html`, `photo.html`, `galleries.html`, `gallery-admin.html`, `template-admin.html`, `display.html`, `display-edit.html`). The Go server serves these as plain static files (`http.FileServer`) with no explicit `Cache-Control` header, so browsers can and do cache them somewhat aggressively across page navigations within a session without necessarily revalidating — a version-query bump forces a fresh fetch. This number should be bumped again (e.g. to match the next session number) any time `app.js` changes going forward.

### Testing notes
- Confirmed via direct execution (not just static reading) that current `packRows`, fed the user's exact four photo dimensions, produces their exact expected output.
- `grep`-verified all seven `app.js` script tags now carry `?v=44`, and that no other file references were accidentally touched (the two doc-comment mentions of "app.js" in prose were correctly left alone).
- No live browser available in this sandbox — recommend the user do one hard refresh (or fully close and reopen the tab) to make sure the cache-busted URL is picked up, then re-verify the same four-photo row.

### Open items
- If the wall still shows the wrong sizes after a hard refresh with the new `?v=44` URL, that would mean this isn't a caching issue after all and the bug is somewhere not yet found (e.g. in how `wallApp` measures `containerWidth`, or a difference between what's actually being fed into `packRows` versus these four dimensions) — worth a fresh look with actual browser dev tools network/console output at that point, since static analysis has now twice confirmed the algorithm itself is correct.
- The version-bump-on-every-change convention is manual/easy to forget; if this keeps being a problem, worth considering a small build step or server-side cache-control header instead.

## Session 45 — Real Root Cause Found: CSP Build Doesn't Support Template Literals At All

### What was done
The user ruled out caching definitively (confirmed the served `app.js` byte-for-byte identical to the source) and then pasted the actual browser console output, which contained the real answer:

```
alpinejs.min.js:1 Alpine Expression Error: CSP Parser Error: Unexpected token: OPERATOR "`"
Expression: "`height:${row.height}px`"
alpinejs.min.js:1 Alpine Expression Error: CSP Parser Error: Unexpected token: OPERATOR "`"
Expression: "row.stretch ? `flex:${p.flexGrow} 1 0px` : `flex:0 0 ${p.widthPx}px`"
```

Decompiling `alpinejs.min.js`'s tokenizer in Session 40 already established this is Alpine's CSP-restricted build with a hand-written expression parser, and that session's fix was for a *different* limitation of that same parser (no multi-statement/semicolon support). This is a second, entirely separate limitation of the same parser: its tokenizer's `readString()` only recognizes `"` and `'` as string delimiters — backticks aren't handled as a string type at all, so any expression using a template literal (`` `...${...}...` ``) fails to *parse*, not just to evaluate, and the whole `:style`/`:src`/etc. binding is abandoned (logged to console, silently never applied — this is why nothing about Session 43's `packRows` fix ever showed up on screen: the computed values were correct, but the `:style` attribute that was supposed to carry them to the DOM never got set at all, on any of these three sessions' testing rounds).

Crucially, `:style="`flex:${p.flexGrow} 1 0px`"` (using a template literal) was **already broken this way before Session 43** — it's not something introduced by this conversation's changes; it's been silently non-functional since whenever the wall layout feature was first built. Session 43's `stretch`/`widthPx` addition just added a second broken template-literal expression alongside the pre-existing one, in the same spot.

Grepped every `.html` file in `app/` for backticks inside directive-like attributes (`@`, `:`, `x-`-prefixed) and found 7 total, all previously undiscovered:
- `app/index.html`: the upload-progress-bar width (`:style`), the wall row height (`:style`), and the wall photo flex (`:style`).
- `app/photo.html`: the same upload-progress-bar width (a separate copy of the upload popup lives on this page too), a comment author's pravatar fallback avatar URL (`:src`), a comment body's `-webkit-line-clamp` truncation style (`:style`), and an emoji reaction's skin-tone swatch background color (`:style`).

Fixed all 7 by rewriting them as plain string concatenation with `+` instead of template literals — e.g. `` `height:${row.height}px` `` → `'height:' + row.height + 'px'`. The CSP evaluator's `BinaryExpression` case for `+` just uses native JS `+`, so numeric/string coercion still works exactly the same as it would in a template literal; only the *syntax* needed to change, not the runtime behavior. Re-grepped afterward and confirmed zero backticks remain in any `.html` file's attributes.

This also means two more real, previously-unknown bugs are now fixed as a side effect, not just the wall sizing: comment authors with no profile image (`c.author.tn` falsy) were rendering with a completely unset `<img src>` instead of the intended pravatar fallback, and long comments' "show more" line-clamp truncation was never actually applying its computed line count.

Separately, the user's pasted console log also showed `GET /api/v1/permissions 500 (Internal Server Error)` — the very endpoint Sessions 39/41 added new frontend callers for (`canUploadPhotos`/`isLabelAdmin`). This fails safe (defaults to `false`, so it doesn't grant anything it shouldn't), but it's a real bug worth chasing. `PermissionsHandler.ServeHTTP` was swallowing the underlying error without logging it, so there was no way to tell *why* it 500'd from server logs alone. Added `slog.Error` logging there (with userid/exhibitionid context) so the next occurrence will actually show the root cause in the server's log output.

Bumped the `app.js` cache-bust query string again, from `?v=44` to `?v=45`, since these were `.html` file changes (the query string is on the `<script>` tag itself, so it needed bumping even though `app.js` itself wasn't touched this session).

### Testing notes
- `node --check app/app.js` passes (no `.js` file changes this session — only `.html` attribute rewrites and the one Go handler).
- Re-ran the backtick grep across all of `app/*.html` after the fixes: zero remaining.
- Tag-balance scans (`div`/`template`/`span`) on both edited HTML files unchanged and balanced.
- Could not verify the Go change compiles via a real build (no Go toolchain in this sandbox, as in every prior Go-touching session) — it's a two-line, low-risk addition (one import, one `slog.Error` call) matching the exact pattern used in every other handler in this codebase.
- Cannot run a real browser here to confirm the wall now renders correctly — this is the first time in this whole multi-session investigation that a plausible, *provable* root cause (a parser error visible directly in the console) has been found, rather than a plausible-sounding theory. Strongly recommend the user retest the same four-photo row now.

### Open items
- Need the user to reproduce the `/api/v1/permissions` 500 again after rebuilding (`make build`) and restarting the server, then share the new server-side log line (now includes the actual DB error, userid, and exhibitionid) so the real cause can be found — right now there's no way to know if it's a schema/migration gap, a bad UUID cast, or something else.
- Given this parser silently drops *any* unparseable expression (not just template literals — Session 40 found the same silent-failure behavior for semicolon-separated statements), it's worth budgeting time for one more full pass across `app/*.html` looking for other CSP-incompatible syntax this parser doesn't support (arrow functions, are already avoided per an existing code comment; regex literals, `new`, spread/rest, destructuring, and array/object literals are all candidates worth spot-checking, since the evaluator's switch statement only explicitly implements a specific list of node types).
- Still pending the accumulated `make build` / end-to-end verification caveat from every prior Go-touching session.

## Session 46 — Fixed the /api/v1/permissions 500: SELECT DISTINCT / ORDER BY Mismatch

### What was done
The logging added in Session 45 immediately paid off — the user's server log showed the exact cause:

```
"error":"ERROR: for SELECT DISTINCT, ORDER BY expressions must appear in select list (SQLSTATE 42P10)"
```

`Checker.UserPermissions`'s query used `SELECT DISTINCT rp.permission, COALESCE(erg.resource_type, ''), COALESCE(erg.resource_ref, '')` but `ORDER BY rp.permission, erg.resource_type, erg.resource_ref` — ordering by the *raw* columns rather than the exact `COALESCE(...)` expressions that appear in the select list. Postgres requires `ORDER BY` expressions in a `SELECT DISTINCT` query to match the select list exactly (not just be derived from the same columns), so this is a hard query-planning error, not something dependent on the specific user or data — meaning this query has failed on **every single call since it was written**, regardless of who called it or what permissions existed. It just so happened that nothing in the app ever actually called `GET /api/v1/permissions` until Sessions 39/41 added `photoApp`/`wallApp`'s `refreshPermissions()` — so a latent, 100%-reproducible bug sat completely undetected until this conversation started actually exercising that endpoint.

Fixed by changing the `ORDER BY` to use the same `COALESCE(...)` expressions as the `SELECT DISTINCT` list. Also swept every other `SELECT DISTINCT` query in the codebase (`internal/handlers/labels.go`, `fetch.go`, `search.go`) for the same mismatch pattern — none of the others had it; they all order by columns that appear in their select list verbatim (or don't have a same-statement `ORDER BY` at all).

### Testing notes
- Manually re-verified the fixed query's `ORDER BY` expressions now textually match the `SELECT DISTINCT` list exactly, which is what Postgres's `42P10` check requires.
- Grepped every other `SELECT DISTINCT` in `internal/` for the same shape of bug; none found.
- No Go toolchain in this sandbox, so — as with every Go change this conversation — this is careful manual review, not a compile or live query test. This one is a single-clause SQL text change with no Go-level type/signature implications, about as low-risk as a fix gets, but the user's rebuild-and-retest is what will actually confirm it.

### Open items
- Once rebuilt, worth confirming `/api/v1/permissions` returns 200 for both anonymous and logged-in requests, since this is now the second time in this codebase a "hasn't been exercised yet" endpoint turned out to have a bug that pure code reading missed and only a live request surfaced.
- Still pending the accumulated `make build` / end-to-end verification caveat from every prior Go-touching session.

## Session 47 — Fixed: My Own Session 45 Edit Broke the Wall's `<img>` Tag

### What was done
Follow-up to Session 46: the user reported the wall now showed *no* images at all — every photo tile rendered blank, but was still clickable and correctly positioned/sized (navigating through to the right photo), which is an important clue: the anchor boxes were fine, so this wasn't the aspect-ratio/sizing bug again — something about the `<img>` itself had gone missing.

Asked for the actual rendered outer HTML of one tile from DevTools, and it revealed the exact problem immediately: `<a ... :style="..." <img="" :src="..." ... width="528" height="356" ...></a>` — a single element with the `<img>`'s attributes merged directly onto the `<a>` tag, and a garbage `img=""` attribute. That shape only happens when an opening tag never closes with `>` before the next `<` appears — the browser then parses everything up to the *next* real `>` as one giant tag's attribute list, so `<img` gets swallowed as an attribute name (`img=""`) rather than starting a new element, and no `<img>` node is ever created. No image, but the anchor itself (still a single valid, correctly-styled `<a>` element) renders and links just fine — exactly matching what was reported.

Traced this back to my own Session 45 edit: when rewriting the wall photo's `:style` attribute from a template literal to string concatenation, I dropped the trailing `>` that used to close the `<a ...>` opening tag right after that attribute, in `app/index.html` only (the equivalent line in `photo.html` doesn't exist — that file has no wall). Notably, my own verification *did* flag this at the time — a quick tag-balance script I ran after the Session 45 edit reported `app/index.html a opens=13 closes=13+selfclosed=1 CHECK` (an actual imbalance once self-closing tags are correctly excluded from the "closes" side), and I wrote it off as a script artifact instead of investigating. That was a mistake worth noting: a "CHECK"/ambiguous result from a hand-rolled verification script should be treated as a real signal to dig into, not dismissed because a *different*, simpler balance check happened to pass.

Fixed by restoring the missing `>`. Re-ran the plain open/close `<a>` tag count (the same check style used throughout this whole conversation) on both `index.html` and `photo.html`: both now balanced (13/13 and 14/14). Also went back and manually re-inspected all 6 *other* backtick-to-concatenation fixes from Session 45 for the same kind of slip — none of them had it; this was an isolated one-line mistake.

### Testing notes
- `node --check app/app.js` passes (no `.js` changes this session).
- `<a>` (along with `div`/`template`/`span`) tag-balance counts now clean on both `index.html` and `photo.html`.
- Manually re-read the other 6 Session 45 edits line-by-line to confirm each element still closes properly — confirmed clean.
- Bumped the `app.js` cache-bust query string to `?v=46` since `index.html` changed again.
- Still no live browser available in this sandbox to visually confirm — but this fix is about as mechanically verifiable as an HTML fix gets (a literal missing character, now restored, confirmed via tag-count parity), so confidence here is high. Recommend the user reload and confirm both that images now render *and* that the aspect-ratio sizing from Session 43 finally looks right, since this bug had been masking any visual confirmation of that fix too.

### Open items
- This is a good argument for treating any "ambiguous"/non-green result from my own verification scripts as worth a follow-up look rather than a dismissible artifact, especially right before telling the user a fix is ready to test.
- Still pending the accumulated `make build` / end-to-end verification caveat from every prior Go-touching session (unaffected by this particular fix, which is pure HTML markup).

## Session 48 — Photo Wall Confirmed Fixed; Restricted-Label Testing Begins

### What was done
The user confirmed the photo wall now renders correctly — closing out the whole Session 43/45/47 investigation (aspect-ratio math → CSP template-literal parsing → a missing `>` typo). Restricted-label testing (Phase 5b, from Session 39) then surfaced its first bug: the Add Label dropdown's lock indicator for restricted names showed the literal text `u{1F512}` instead of a 🔒 glyph.

Root cause is the *same family* of CSP-evaluator limitation found in Sessions 40 and 45, just in the string-escape handling rather than syntax parsing. Decompiling the tokenizer's `readString()` (done back in Session 40) shows it only special-cases four escapes — `\n`, `\t`, `\r`, `\\` — plus whatever the current quote character is; any other escape sequence hits its `default` branch, which keeps the character *after* the backslash but drops the backslash itself. So the source's `'flex:' ... ' \u{1F512}'` (from Session 39) tokenized as the literal seven characters `u{1F512}`, not the Unicode code point escape it was written as.

Fixed in `app/photo.html`'s label-name `<option>` template by replacing the escape sequence with the actual 🔒 character typed directly into the string literal — since the file is UTF-8 and the CSP tokenizer just copies non-backslash characters through verbatim, an unescaped literal emoji works fine; only escape *sequences* are the problem. Grepped all of `app/*.html` for any other `\u{` or `\uXXXX` escape inside directive attributes — this was the only one.

### Testing notes
- Grep confirmed no remaining `\u` escapes in any `.html` directive attribute.
- Tag-balance re-check on `photo.html` (`div`/`template`/`span`/`option`/`select`) unchanged and balanced.
- Bumped `app.js` cache-bust query string to `?v=47`.
- No live browser here to confirm the glyph renders — but this fix has the same "about as verifiable as it gets" character as the missing-`>` fix: the literal character is now directly in the source rather than routed through an escape the evaluator can't handle.

### Open items
- Worth a quick visual sweep of any other emoji/symbol usage added across Sessions 39–41 (color swatches, restricted-label lock icon on the chip itself) — the chip's lock icon is an inline `<svg>`, not a text escape, so it's unaffected, but flagging as a reminder that *any* future non-ASCII character in a directive expression should be typed literally, never as a `\u` escape, given this parser's limitation.
- Still pending the accumulated `make build` / end-to-end verification caveat from every prior Go-touching session (unaffected by this fix, which is pure HTML markup) — restricted-label testing is now underway per the user's message, so more findings may follow in this same thread.

## Session 49 — Hide Restricted Label Names from Non-Admins + Live Custom-Name Validation

### What was done
Continued restricted-label testing feedback: the user didn't want restricted label names visible in the Add Label dropdown at all unless they have LabelAdmin (previously they showed up disabled with a 🔒 suffix, per Session 39/48), and wanted an immediate error if they typed a restricted name into the custom "Other…" text field, rather than only finding out after clicking Add Label and getting a 403 back.

In `app/app.js`'s `labelEditor()`: split the single `knownNames` array into two. `allNameInfos` now holds the full, unfiltered `{name, color, restricted}` list straight from `GET /api/v1/label-names`, kept around purely for validation. `knownNames` — the array the dropdown's `x-for` actually iterates — is now a filtered view: LabelAdmins see everything, everyone else gets `allNameInfos.filter(n => !n.restricted)`, so restricted names never appear as options at all for non-admins (the old per-option `:disabled`/lock-emoji rendering in `photo.html` still exists and still works correctly for LabelAdmins, who do see restricted entries; it's simply unreachable for non-admins now since they never receive a restricted `n` to iterate over).

Added a new computed getter, `customNameRestricted`, that trims `customName`, looks it up (exact match, mirroring the backend's `WHERE name = $1`) in the unfiltered `allNameInfos`, and returns true only when that name is restricted and the user isn't a LabelAdmin. Wired this into `photo.html`'s custom-name `<input>`: it now gets a red border/ring and an inline `"<name>" is a restricted label name.` message the moment the typed text matches, live as they type (no need to press Add Label first). Also added `customNameRestricted` to the Add Label button's `:disabled` condition so submission is blocked outright while the error is showing, not just visually flagged.

### Testing notes
- `node --check app/app.js` passes.
- Grepped `app.js` and `photo.html` for the CSP-parser hazards this codebase has repeatedly hit (backticks and semicolons inside directive attributes, `\u` escapes) — none introduced by this change.
- Re-ran the tag-balance script (`div`/`template`/`span`/`a`/`option`/`select`/`p`/`button`/`input`) across `photo.html` and `index.html` — all balanced.
- Bumped `app.js` cache-bust query string to `?v=48` across all 7 HTML files (both `app.js` and `photo.html` changed this session).
- No live browser in this sandbox — as with every frontend change this conversation, recommend the user reload and confirm: (1) a non-LabelAdmin's Add Label dropdown no longer lists any restricted names, (2) a LabelAdmin's dropdown still shows them (with the 🔒 suffix, selectable), and (3) typing a restricted name into the "Other…" field as a non-admin shows the red inline error immediately and keeps the Add Label button disabled.

### Open items
- None outstanding from this specific request. All bugs reported so far this conversation (label edit/delete buttons, upload permission error/gating, uploaded-photo visibility, wall aspect ratio, CSP template literals, `/api/v1/permissions` 500, missing `>` typo, lock-icon escape, and now restricted-name dropdown filtering/live validation) have been fixed; still pending is the standing `make build` / end-to-end verification caveat, since no Go toolchain or live browser is available in this sandbox.

## Session 50 — Removed `app.js?v=N` Cache-Busting

### What was done
The user pointed out that the `?v=N` cache-busting query string added across Sessions 37/44–48 wasn't actually needed — their browser's clear-cache button works fine — and that bumping it on every change was just gratuitous diff noise. Removed the query string from all 7 `<script src="/app.js...">` tags (`index.html`, `photo.html`, `galleries.html`, `gallery-admin.html`, `template-admin.html`, `display.html`, `display-edit.html`), reverting to a plain `/app.js`.

### Testing notes
- Grepped all `.html` files to confirm no `app.js?v=` references remain.
- No other content changed; this is a pure revert of the cache-bust query string.

### Open items
- None. Going forward, `app.js` changes don't need any accompanying script-tag edit.

## Session 51 — Phase 5c: Emoji Improvements

### What was done
Moved on to PLAN.md's Phase 5c (Emoji improvements). Backend investigation first: `GET /api/v1/emoji/types` already supported search (`?search=` against `alt_text`/`tags` via `ILIKE`) and pagination (`offset`/`limit` with a `pages` metadata block), and the frontend's `emojiPicker` already fetches one page at a time (64 emojis) rather than loading everything upfront — so two of the five 5c sub-items (search, lazy paged download) were already done and needed no changes. The remaining three were implemented:

**Popular-first ordering.** `EmojisHandler.ListTypes` (`internal/handlers/emojis.go`) now `LEFT JOIN`s a per-emoji reaction count (`SELECT emojiid, COUNT(*) FROM emoji_reactions GROUP BY emojiid`) and orders by `COALESCE(usage_count,0) DESC, alt_text ASC, sort_order, created_at`. Since the vast majority of emoji types have zero reactions, this single stable ORDER BY naturally produces "frequently-used emojis first, everything else alphabetical" without needing to special-case page 1 vs. later pages (which would risk an inconsistent, non-paginate-safe ordering). Added a `UsageCount int` field to `models.EmojiTypeResponse` (`json:"usage_count,omitempty"`) so the count travels with each result, and threaded it through the existing `rows.Scan(...)` call.

**Reaction tooltip shows the name.** `emojiHover.reactionTitle(em)` (`app/app.js`) now prefixes the native browser tooltip with `em.alttext` — e.g. "thumbs up — Click to react" instead of just "Click to react". The custom hover popup on the photo page (`app/photo.html`, the "Reacted with 👍" box) now also renders `em.alttext` as text next to the glyph, matching the plan's `"Reacted with :thumbsup:"` framing using the actual stored name rather than an emoji-shortcode convention (this app doesn't store colon-names, only `alt_text`).

**Upload-your-own UI.** The backend's `POST /api/v1/emoji/types` (multipart image + alttext, gated on the `EmojiUpload` permission) has existed since before this session but had *no* frontend entry point at all. Added one inside the existing Add Reaction picker modal in `app/photo.html`: a collapsed "+ Upload your own emoji" link that expands into a file picker + name field + Upload button, gated behind a new `canUploadEmoji` getter on `emojiPicker` (backed by `window._canUploadEmoji`, mirroring the existing `window._isLabelAdmin` pattern). `photoApp.refreshPermissions()` now also computes `canUploadEmoji = summary.includes('EmojiUpload')` — deliberately *not* falling back to `Admin` the way the label-restriction checks do, because the backend's `UploadType` handler checks `EmojiUpload` alone with no `HasAny(Admin, ...)` fallback, and the frontend gate needs to match the backend's actual rule exactly (checked `scripts/seed-exhibition.sh`: the `Admin` role is granted the literal `EmojiUpload` permission row, so real admins still see the control — just via an explicit grant, not an implicit superset). On successful upload, the new emoji is unshifted onto the visible list immediately (it has no reactions yet, so it wouldn't otherwise resurface until pagination caught up with the alphabetical tail) and the form resets. Also fixed `UploadType`'s response to run its `imageURL` through `proxyImageURLPtr` before returning, matching `ListTypes`'s behavior — without this, a freshly uploaded emoji's image would try to load directly from the external upload host instead of through the app's image proxy, and might not render immediately after upload even though the list endpoint would proxy it correctly on next fetch.

### Testing notes
- `node --check app/app.js` passes.
- Grepped `app.js`/`photo.html` for this codebase's recurring CSP-parser hazards (semicolons or backticks inside directive attributes, `\u` escapes) — none introduced.
- Caught and fixed one real bug during review: the first draft of the upload form's placeholder text used backslash-escaped quotes (`placeholder="...e.g. \"party parrot\""`), which isn't valid HTML attribute syntax (HTML has no backslash-escaping) and would have broken the attribute/tag boundary the same way the Session 47 missing-`>` typo did. Reworded the placeholder to avoid embedded quotes entirely rather than trying to escape them.
- Re-ran the tag-balance script (`div`/`template`/`span`/`a`/`option`/`select`/`p`/`button`/`input`/`svg`/`path`) on `photo.html` — all balanced after the new upload-form markup.
- No Go toolchain or live browser in this sandbox — the SQL join/order change and the model field addition were reviewed by hand (parameter placeholders `$n`/`$n+1` for `LIMIT`/`OFFSET` are untouched by the join, so no positional-arg mismatch), but this is exactly the kind of change (Postgres SQL text plus a Scan column list) that's only fully confirmed by an actual query against real data.

### Open items
- No index exists on `emoji_reactions(emojiid)` alone (only composite `(photoid, emojiid)` and `(photoid, userid)`); the new popularity subquery's `GROUP BY emojiid` will do more work than strictly necessary at scale. Not addressed since photo-app emoji-type counts are expected to stay small, but worth a migration if this table ever grows large.
- The plan text describes the search query param as `?q=`; the existing (pre-this-session) implementation uses `?search=` end-to-end (frontend and backend already agree on this name and it fully covers the described behavior — alt_text/tags substring match), so it was left as-is rather than renamed for no functional gain.
- Standing `make build` / end-to-end verification caveat still applies, as with every backend change this conversation.

## Session 52 — Removed app.js Cache-Busting; Phase 5d: Rich-Text Comments

### What was done
Two pieces of work this session.

**Cache-busting removal.** The user pointed out the `app.js?v=N` query string added in Sessions 37/44–48 was gratuitous — the browser's clear-cache button works fine — and just added diff noise every time `app.js` changed. Reverted all 7 `<script src="/app.js...">` tags back to plain `/app.js`.

**Phase 5d (PLAN.md): rich-text comments.** This was the largest single feature so far. Scope decisions and implementation:

*Storage/validation (backend, `internal/handlers/comments.go`).* Comments already stored as `TEXT` (no schema change needed) and already rejected empty bodies; added `strings.TrimSpace()` before the empty check on both Create and Update so a whitespace-only body can't sneak past a direct API call.

*Rendering pipeline.* Comments are Markdown source, rendered to HTML entirely client-side — there's no backend Markdown renderer. Vendored `marked` (18.0.6) and `DOMPurify` (3.4.12) as local files (`app/vendor/marked.min.js`, `app/vendor/purify.min.js`) rather than pulling from a CDN, matching this app's existing self-hosted-assets convention (alpinejs.min.js, tailwind.css, fonts are all vendored the same way — no CDN fetches happen anywhere else in the app). Getting these files required going through npm (`npm install marked dompurify`, then `terser` to minify marked's UMD build) since this sandbox's direct `curl`/web-fetch to the actual CDN URLs is blocked; confirmed both work correctly and produce the expected output via a battery of Node.js tests (not just `node --check`) before wiring them into the app — see Testing notes.

`renderMarkdown(text)` in `app.js` runs `marked.parse(text, { breaks: true })` then `DOMPurify.sanitize()` with an explicit allow-list (`p, br, strong, em, del, a, ul, ol, li, blockquote, code, pre, span, img` / `href, src, alt, title, style, class, target, rel`) — this allow-list is deliberately generous enough to permit the `<span style="...">` the Font toolbar produces, but excludes anything script-capable. This is the actual point where PLAN.md's "sanitize HTML on output" happens, since in this architecture "output" (HTML generation) only happens in the browser, not on the backend — confirmed via a raw `<script>` injection test (see Testing notes) that it's stripped while the Font `<span>` survives.

*Emoji shorthand.* `:name:` patterns are resolved via a small client-side cache (`_emojiShortcodeCache`, keyed by a normalized — lowercased, spaces→underscores — shortcode) backed by the existing `GET /api/v1/emoji/types?search=` endpoint (no backend changes needed here either). Because Alpine template bindings need synchronous values, `renderMarkdown()` only substitutes shortcodes already in the cache; a separate `resolveShortcodes(text, onDone)` scans for anything uncached, fetches it, and calls `onDone()` exactly once if (and only if) something new was resolved — verified this doesn't loop (a fully-cached or fully-unresolvable text short-circuits before calling `onDone` again).

*Toolbar/composer (`markdownComposerMixin(field, refName)` in app.js).* Bold/Italic/Strikethrough (wrap selection in `**`/`*`/`~~`), a Font popover (family/weight/size selects that wrap the selection in `<span style="...">`, satisfying PLAN.md's "stored as Markdown with HTML spans"), a `:emoji:` insertion button backed by a new small `emojiShortcodePicker()` component (search/browse, no reactions/skintone complexity, dispatches `insert-emoji-shortcode` which bubbles to the composer's own listener), and a Preview toggle. This is a *mixin factory*, not a nested reusable sub-component, because this app's Alpine build is the CSP-restricted evaluator established over many prior sessions (it can't parse arrow-function expressions in directive attributes) — there's no way to hand a nested component getter/setter closures from `x-data="..."`. So every composer (new comment, edit, reply) gets its own statically-named copy of the fields/methods (e.g. `wrapNewText`, `wrapEditText`, `wrapReplyText`) via `Object.assign`/spread in plain JS (which has none of the CSP evaluator's restrictions — that restriction only applies to expressions *inside* Alpine directive attribute strings in the HTML, not to app.js's own function bodies).

*Scope decision on where the toolbar appears.* The comment thread's reply nesting (depth 0–5, six levels deep) is hand-unrolled markup in `photo.html` from an earlier session (not something introduced here) — each depth level has its own literal copy of the reply/edit boxes rather than true recursion. Adding the full toolbar at all ~19 composer locations across all 6 levels would have meant a huge amount of repetitive markup for an interaction pattern (editing/replying at reply-depth 2+) that's rare in practice. Scoped the full toolbar (Bold/Italic/Strike/Font/Emoji/Preview) to the three composers that matter most: the top-level new-comment box, the depth-0 comment's edit box, and the depth-0 reply box (both of its two mutually-exclusive render locations, since collapsed vs. expanded replies show the same reply composer in different spots). Markdown+emoji *rendering* (view-only, not authoring) was applied everywhere — all 6 nesting levels' comment-body displays were converted from `x-text="commentBody"` to `x-html="renderedComment"` — so replies at any depth display correctly even though only the shallow levels get toolbar buttons; a user can still type raw Markdown syntax by hand at any depth and have it render.

*CSS.* Added a `.rich-content` class (applied to every rendered-comment/preview container) with rules for lists, links, code/pre, blockquote, and inline-emoji image sizing — Tailwind's preflight reset strips all default browser styling from these tags, so without this the rendered Markdown would have looked like unstyled run-on text.

### Testing notes
- `node --check app/app.js` passes.
- Full tag-balance sweep (`div/template/span/button/select/option/input/a/p/svg/path/textarea/style`) on `photo.html` — all balanced after ~250 new lines of toolbar/popover markup across 4 edit locations.
- CSP-hazard grep (backticks, semicolons, `\u` escapes inside directive attributes) — clean, none introduced, despite this being by far the largest single markup addition this conversation.
- Went well beyond `node --check` for the new logic given its complexity: wrote a battery of actual Node.js execution tests (loading the vendored `marked`/`DOMPurify` plus a `jsdom` window, extracting the real functions from `app.js` via `vm.runInContext`) covering: `renderMarkdown()` correctly bolds/italicizes/strikes and — critically — strips a raw `<script>` tag while preserving an allowed `<span style="...">`; `resolveShortcodes()` fetches an uncached shortcode exactly once and doesn't refetch or re-invoke its callback on subsequent calls (including when the shortcode isn't found, cached as `null`); the full shortcode → cache → substitute → marked → DOMPurify pipeline for both emoji-char and emoji-image shortcodes; and `markdownComposerMixin`'s `wrap*`, `insertShortcode*`, and `applyFont*` methods against a mocked `<textarea>` (selectionStart/selectionEnd), confirming correct text splicing in all three cases.
- Verified `marked.parse(text, { breaks: true })` — the option this code relies on — actually converts single newlines to `<br>` in the vendored 18.0.6 build (checked directly, since the API for this can vary by version).
- No live browser in this sandbox, so the toolbar buttons' actual click behavior, the emoji popover's visual positioning, and the `.rich-content` CSS's visual appearance are unverified beyond the logic tests above — this is the highest-risk unverified surface of any change this conversation given its size, and a manual click-through (typing bold/italic/emoji shortcodes, toggling preview, posting, editing, replying at both depth-0 locations) is strongly recommended before considering this fully done.

### Open items
- Toolbar (Bold/Italic/Strike/Font/Emoji/Preview) is only wired up at 3 of the ~19 composer locations in the hand-unrolled 6-level reply tree (new comment, depth-0 edit, depth-0 reply). Deeper levels still render Markdown correctly but have plain textareas with no buttons. Extending the toolbar deeper is mechanical (copy the same block, rename fields) if this turns out to matter in practice.
- `app/vendor/marked.min.js` and `app/vendor/purify.min.js` are unpinned local copies (no lockfile/version-check mechanism) — worth noting their versions (marked 18.0.6, DOMPurify 3.4.12) somewhere if this project ever wants a process for bumping them.
- The Font toolbar's family/weight/size lists are a small fixed set (Georgia/Courier New/Arial/Serif Display; three weights; three sizes) rather than an open-ended picker — matches PLAN.md's ask ("font name, weight, size selection") without over-building a full CSS font picker.
- Standing `make build` / end-to-end verification caveat applies to the Go changes (comments.go trimming) as with every backend change this conversation.

## Session 53 — Fixed: x-html Is Hard-Disabled in the Alpine CSP Build

### What was done
The user reported "the draft comment disappears when you click Preview" on the new-comment box added in Session 52. Rather than guess, built a proper jsdom test harness that loads the actual `alpinejs.min.js` shipped in this app (not a generic Alpine build) and exercises real markup against it — the same rigor as the Node.js logic tests from Session 52, but this time driving the real Alpine runtime instead of just the plain-JS helper functions.

That harness immediately surfaced the real bug, with an unambiguous, deliberately-worded error: **`Alpine Expression Error: Using the x-html directive is prohibited in the CSP build`**. This app's Alpine CSP build doesn't just restrict *expression syntax* inside directive attributes (semicolons, template literals, certain escapes — all found in earlier sessions) — it outright disables the `x-html` directive itself as a hard-coded, unconditional guard, for exactly the reason you'd expect from a CSP-hardened build (raw-HTML injection is treated as a blanket risk regardless of who authored the binding). A follow-up test confirmed a second, equally hard guard: any directive *expression* that assigns to a DOM property (e.g. `x-effect="$el.innerHTML = x"`) is also blocked ("Property assignments on DOM objects are prohibited in the CSP build"). Both fire unconditionally — they're not scope/parsing quirks like the earlier semicolon/backtick/escape issues, they're deliberate categorical blocks in the build itself.

Practical impact: **every use of `x-html` introduced in Session 52 was silently broken** — not just the new-comment Preview toggle the user happened to notice, but the rendering of every already-posted comment at all 6 nesting depths (`x-html="renderedComment"`) and the edit/reply preview panes too. The user's report was the first symptom found, not the full extent of the breakage.

Fix: registered a custom Alpine directive, `x-rich-html`, via `Alpine.directive('rich-html', ...)` in `app.js`'s existing `alpine:init` block. The key insight (confirmed by testing): the CSP build's restrictions apply to the *expression parser* — i.e., what you're allowed to write inside an `x-foo="..."` attribute string — not to a registered directive's own handler function, which is real, unrestricted JS running outside that parser entirely. So `Alpine.directive('rich-html', (el, { expression }, { effect, evaluateLater }) => { const getValue = evaluateLater(expression); effect(() => getValue(v => { el.innerHTML = v || ''; })); })` does the `.innerHTML` assignment in plain JS (allowed), while the bound expression itself (`"renderedComment"`, `"newTextPreviewHtml"`, etc.) is just a plain identifier lookup through the CSP evaluator (also allowed — it's not an assignment or a `x-html`-named directive). Replaced all 10 occurrences of `x-html="..."` in `photo.html` with `x-rich-html="..."` via `sed`.

While building the jsdom harness, also found and fixed a second, separate gap: the new `emojiShortcodePicker` component (added in Session 52 for the comment toolbar's `:emoji:` insertion popover) was never registered via `Alpine.data(...)`, unlike literally every other `x-data`-referenced component in this app (`photoApp`, `commentItem`, `emojiPicker`, etc. are all registered even though they're invoked with arguments like `x-data="commentItem(c, photoid, 0)"`). Added `Alpine.data('emojiShortcodePicker', emojiShortcodePicker);` to match the established pattern — this was very likely also silently broken (the emoji-insert button in the comment toolbar probably did nothing), just not yet reported.

### Testing notes
- Built a real jsdom + actual-`alpinejs.min.js` test harness (not a generic/CDN Alpine build) and used it to: (1) reproduce the exact `x-html`-prohibited error against markup matching this app's structure; (2) reproduce the DOM-property-assignment prohibition as a second, independent confirmation this is a hard build restriction, not a one-off; (3) validate the `x-rich-html` custom-directive fix both for initial render and for *reactive updates* (changed a bound property after mount, confirmed `.innerHTML` updated automatically) — this specifically validates the pattern `resolveShortcodes()`'s async-resolution callback relies on (updating `renderedComment` after the initial render and expecting the DOM to pick it up).
- Noted and worked around a jsdom-specific limitation unrelated to the actual bug: Alpine's CSP evaluator couldn't resolve global functions defined via `window.eval(...)` or `<script>` textContent injection in jsdom specifically (a "Undefined variable" error, distinct in wording and cause from the two deliberate CSP-build prohibitions above) — worked around this by testing the directive against inline `x-data="{...}"` object literals instead of named global functions, which sidesteps the jsdom quirk while still exercising the real evaluator and the real custom-directive code path.
- `node --check app/app.js` passes.
- Re-ran the full tag-balance sweep and CSP-hazard grep (backticks/semicolons/`\u` escapes in directive attributes) across `photo.html` after the `x-html` → `x-rich-html` rename — clean.
- Grepped the rest of the app (`index.html`, `galleries.html`, etc.) for any other `x-html` usage — `photo.html` was the only file affected, since it's the only page with rich-text comments.

### Open items
- This is the second time in this conversation that a change looked correct under static review and `node --check`/tag-balance but was actually broken by an Alpine-CSP-build-specific runtime restriction (the first being the template-literal/semicolon/escape findings in Sessions 40/45/48) — worth treating *any* new Alpine directive usage in this app as something to check against the real `alpinejs.min.js` (via a jsdom harness like the one built this session, now easy to reuse) rather than trusting it by analogy to how "normal" Alpine behaves.
- Recommend the user do a fresh manual pass now that this is fixed: confirm existing posted comments at all depths still render (they were silently blank/broken since Session 52, this fixes that), confirm the new-comment Preview toggle now shows the typed draft's rendered Markdown instead of appearing empty, and confirm the emoji-shortcode insert button in the toolbar now actually opens/works.
- No live browser in this sandbox — the jsdom harness is a strong proxy for Alpine-level behavior (it's driving the real shipped Alpine build) but isn't a full substitute for an actual browser click-through.

## Session 54 — Actually Fixed It: Vendor Scripts Were 404ing, Not Just x-html

### What was done
The user reported Session 53's fix "didn't fix it" — Preview still made the draft vanish with nothing shown. Rather than keep reasoning from the jsdom harness alone, asked the user to check the browser console, which immediately showed the real, complete picture:

```
GET http://.../vendor/marked.min.js net::ERR_ABORTED 404 (Not Found)
Refused to execute script from '.../vendor/marked.min.js' because its MIME type ('text/plain') is not executable
GET http://.../vendor/purify.min.js net::ERR_ABORTED 404 (Not Found)
Refused to execute script from '.../vendor/purify.min.js' ...
```

Both vendored library files were **never reaching the browser at all** — the `<script src="/vendor/marked.min.js">` tags added in Session 52 404'd every time. Since `marked`/`DOMPurify` were consequently always `undefined`, every single call to `renderMarkdown()` had been throwing a `ReferenceError` since Session 52 — a strictly bigger and more fundamental problem than the `x-html`-prohibited finding from Session 53 (that fix was real and necessary, just not sufficient, since the expression it was evaluating never had valid content to render in the first place).

Investigated why `/vendor/*.min.js` 404s when `/photo.html`, `/app.js`, `/alpinejs.min.js`, and `/tailwind.css` all serve fine from the exact same `AppDir` via the exact same `http.FileServer(http.Dir(cfg.AppDir))` mechanism (confirmed in `router.go` — no per-path special-casing that would explain a subdirectory being treated differently). Confirmed the files do exist on disk in this project's `app/vendor/` directory and are syntactically valid. The most likely explanation is some difference between how this workspace's file writes reach the actual machine serving requests at `192.168.64.2:8080` (a VM-like address) versus how already-tracked files get there — `git status` shows `app/vendor/` as untracked, so if there's any git-mediated step between editing here and the server actually seeing the files, a brand-new untracked directory could plausibly be the one thing that doesn't make the trip, unlike edits to already-existing tracked files. This wasn't fully confirmed (no visibility into that machine from here), and going forward isn't worth chasing further given the fix below sidesteps it entirely.

**Fix:** stopped depending on a separate static-file request for the vendor libraries altogether. Read both `app/vendor/marked.min.js` (42,616 bytes) and `app/vendor/purify.min.js` (28,979 bytes) and inlined their full contents directly as `<script>...</script>` blocks in `photo.html`'s `<head>`, replacing the two `<script src="/vendor/...">` tags. This has no dependency on any additional HTTP request, route, or directory — it's part of `photo.html` itself, which is unambiguously already serving correctly. Also stripped a `//# sourceMappingURL=purify.min.js.map` trailer comment left over from the vendored file (would have caused one more harmless-but-noisy 404 for a dev-tools-only source map).

The `x-rich-html` custom directive and `emojiShortcodePicker` registration from Session 53 are both still correct and still needed — those fixed real, separate bugs. This session's fix addresses the actual root cause underneath them.

### Testing notes
- Verified the two library files extracted back out of the inlined `<script>` blocks are byte-for-byte identical to the originals (`node --check` passes on both the file-based originals and the values extracted from the rewritten `photo.html`).
- Ran a jsdom test loading the *inlined* content exactly as a browser would (via `createElement('script'); .textContent = ...; appendChild`, no `require`/CommonJS involved) and confirmed `window.marked`/`window.DOMPurify` both initialize correctly and `marked.parse()` / `DOMPurify.sanitize()` both work — this specifically rules out the "UMD wrapper behaves differently when there's no separate module system" class of concern.
- Re-ran the tag-balance sweep across all of `photo.html`, this time explicitly excluding the two now-giant inlined-script lines from the regex scan (since 40KB+29KB of arbitrary minified JS could otherwise produce false `<...>`-shaped matches) — everything else in the file still balances correctly, and script-tag count (4 opens / 4 closes) is correct once a false-positive match against my own explanatory code comment's prose (which literally contains the substring "`<script src>`") is accounted for.
- `node --check app/app.js` still passes (unchanged this session).
- Grepped for any remaining reference to `/vendor/` in `photo.html` — none; the two script tags are gone, replaced by inline content.

### Open items
- `app/vendor/marked.min.js` and `app/vendor/purify.min.js` are now unused/orphaned (nothing references them anymore) — left in place rather than deleted, since files in the user's connected folder require explicit permission to remove; happy to delete them on request.
- Still don't have a confirmed root cause for *why* the static file route 404'd — only a plausible git/untracked-file theory. If another brand-new file/directory 404s the same way in the future, that's worth investigating properly (e.g., checking whatever process syncs this folder to the actual serving host) rather than working around it again.
- This is now the second consecutive session where the fix required actual browser console output to find the real bug (Session 45 also cracked a stuck investigation this same way) — reinforcing that asking for live evidence early, rather than iterating on static analysis, is the fastest path when something "should" work but doesn't.

## Session 55 — Corrected Course: Vendor Files Stay as Files, Not Inlined

### What was done
The user corrected Session 54's diagnosis: the vendor files 404'd because **they were genuinely never created** on the machine that matters, not because of some deploy/sync quirk between a tracked-vs-untracked git state. Direction given: keep `marked.min.js`/`purify.min.js` as real separate vendor files (don't inline them into `photo.html`), and add a script that (re)generates them, to be run before release testing.

Reverted Session 54's inlining: removed the two ~40KB/~29KB inline `<script>` blocks from `photo.html`'s `<head>` and restored the plain `<script src="/vendor/marked.min.js"></script>` / `<script src="/vendor/purify.min.js"></script>` tags (now with a comment pointing at the new script). Along the way, cleaned up a duplicated leftover HTML comment and a stray `</script>` tag that Session 54's text-replacement had left behind from the original (pre-inlining) comment block.

Added `scripts/update-vendor-js.sh`, matching the existing `scripts/build-tailwind.sh` pattern in this repo (installs into the project's own `node_modules`, not a temp directory; same `set -euo pipefail` / progress-echo style): it runs `npm install marked dompurify terser`, minifies marked's UMD build with `terser` (marked's npm package doesn't ship a pre-minified dist), copies DOMPurify's pre-minified `purify.min.js` directly, and writes both into `app/vendor/`. Ran it end-to-end in this sandbox to confirm it actually works — it reproduced byte-identical files to what was already in `app/vendor/` (42,616 and 29,209 bytes), so the script is a validated, repeatable substitute for however these two files need to get onto the machine that serves the app before release testing.

### Testing notes
- Ran `bash scripts/update-vendor-js.sh` for real in this sandbox — completed successfully (one harmless `npm` cleanup warning about an optional macOS-only native dependency, irrelevant on Linux and not a real error) and regenerated both files at their expected sizes.
- `node --check` on `app/app.js`, `app/vendor/marked.min.js`, and `app/vendor/purify.min.js` all pass.
- Re-ran the tag-balance sweep on `photo.html` post-revert — all tags balanced, including a clean 4-open/4-close `<script>` count (2 vendor + app.js + alpinejs.min.js).
- Re-confirmed the actual Phase 5d bug fixes from Sessions 52/53 are untouched by this revert: all 10 `x-rich-html="..."` bindings, the `Alpine.directive('rich-html', ...)` registration, and the `Alpine.data('emojiShortcodePicker', ...)` registration are all still exactly in place — only the `<head>`'s two script tags changed.
- CSP-hazard grep (backticks/`\u` escapes in directive attributes) on `photo.html` — clean.

### Open items
- The user still needs to actually run `scripts/update-vendor-js.sh` (or otherwise get `app/vendor/marked.min.js`/`purify.min.js` onto the real serving host) and confirm both URLs return 200 before this feature can be considered working end-to-end — this session fixes the *mechanism* for getting the files there, not the fact of them being there on their machine right now.
- Worth deciding whether `scripts/update-vendor-js.sh` should be wired into `make build` as a hard dependency (like `tailwind.css` is) or stay a manual pre-release step as asked for here — left as manual per the user's explicit request ("prior to release testing"), but flagging in case that decision should be revisited later.

## Session 56 — Added a Favicon

### What was done
Browser console showed `GET /favicon.ico 404`. Generated a simple on-brand icon programmatically (Pillow): a rounded navy square (`--brand: #1a1a2e`) with a white camera body and an accent-colored (`--accent: #e94560`) lens, rendered at 256×256 and downsampled into a multi-resolution `app/favicon.ico` (16/32/48/64px) plus a 256×256 `app/apple-touch-icon.png` for iOS/home-screen use. Added `<link rel="icon" href="/favicon.ico">` and `<link rel="apple-touch-icon" href="/apple-touch-icon.png">` right after the `<title>` tag in all 9 HTML pages (`index`, `photo`, `admin`, `galleries`, `gallery-admin`, `display`, `display-edit`, `template-admin`, `newdomain`) — explicit link tags rather than relying solely on the browser's default `/favicon.ico` guess, so it's unambiguous even if a page is embedded or the default lookup behaves inconsistently across browsers.

Given last session's lesson that newly-created files don't reliably reach wherever `192.168.64.2:8080` actually serves from, explicitly confirmed both new binary files are visible through the same file-access path the user's folder view uses (not just the bash sandbox) before calling this done.

### Testing notes
- Regenerated the icon at each target size directly (not just resized from one bitmap) so the small 16×16/32×32 renders stay legible rather than a downscaled blur.
- Visually inspected the 256×256 source render before finalizing — clean rounded-square camera glyph, matches the app's existing brand/accent colors.
- Confirmed via the file-sharing path (not bash) that `app/favicon.ico` and `app/apple-touch-icon.png` are visible in the user's actual connected folder.
- Re-ran the `<head>/<html>/<body>` tag-balance check across all 9 modified HTML files — all balanced.

### Open items
- Same as Session 55: still recommend the user confirm `/favicon.ico` actually returns 200 (not 404) after their next deploy/restart, given the standing question about whether newly-added files make it to the serving host automatically.

## Session 57 — Phase 6: Admin Pages

### What was done
Implemented all five sub-phases of PLAN.md's Phase 6.

**Migration** (`migrations/017_admin_phase6.sql`): added `account_enabled`, `can_manage_own_labels`, `can_manage_own_emoji`, `can_manage_own_comments` to `users`, and `enabled` to `label_names`.

**Design call worth flagging**: 6b asks for per-user toggles including things like "label adding/editing own" — but the seeded `Contributor` role already grants `PhotoLabelCreate`/`PhotoEmojiCreate`/`PhotoEmojiDelete`/`PhotoCommentCreate` to *every* logged-in user via a single `LoggedIn`-entity grant, and the permission checker only ever ORs grants together (no negative/deny grants exist). That means an admin can't revoke one troublemaking user's access to something everyone else has using the existing role-grant model alone — so those three toggles are real boolean columns, checked as a hard AND on top of the normal permission check inside `labels.Create`, `emojis.React`/`Unreact`, and `comments.Create`. The fourth toggle (private-photo access) is the opposite case — nobody gets it by default, so it only ever needs to be *added* for one user — and reuses the existing `entity_role_grants` machinery via a new auto-created "singleton role" per permission (`permissions.Checker.GrantUserPermission`/`RevokeUserPermission`/`HasDirectUserGrant`), rather than a fifth column. `account_enabled` is enforced at the session layer: `AuthHandler.LookupSession` now joins `users.account_enabled`, so flipping the toggle invalidates a disabled user's existing sessions on their very next request without needing to hunt down and revoke tokens; `Login` also checks it up front for a clearer error message.

**6a** — `GET /api/v1/admin/stats` (user/photo count for the selected exhibition; label/emoji "active" counts are site-wide since those tables have no `exhibitionid`). New `app/admin-master.html`: quick-stats tiles + link cards to every other admin page.

**6b** — `GET /api/v1/admin/users` (single query, no N+1, including an `EXISTS` subquery against the singleton-role mechanism for `can_view_private`) and `PATCH /api/v1/admin/users/:userid`. New `app/admin-users.html`: per-user checkbox row for all five toggles, with a self-lockout guard (can't disable your own account).

**6c** — added a `search` param to the existing `GET /api/v1/admin/photos` (matches title or photoid), plus a search box in `admin.html`/`admin.js`.

**6d** — `GET /api/v1/admin/emoji-types` (include_disabled + search + pagination) and `PATCH /api/v1/admin/emoji-types/:emojiid` (`is_active` toggle). New `app/admin-emojis.html`.

**6e** — extended the existing `PATCH /api/v1/label-names` with an `enabled` field (parallel to the Phase 5b `restricted` field), and added `GET /api/v1/admin/label-names` (paginated, with usage counts). Disabled is a stronger version of restricted: it hides the name from the add-label suggestion dropdown *and* blocks create/update/delete for non-admins, same enforcement shape as restricted but with its own error message; both checks now run together in `labels.go`'s `Create`/`Update`/`Delete`. New `app/admin-labels.html`: color swatch (native `<input type="color">`), restricted/enabled toggles, usage count, "show disabled" filter.

All four new admin pages share `app/admin.js` (not `app/app.js`) — matching the pre-existing precedent that `admin.html` is a standalone bundle — and a consistent header/cross-nav bar linking Overview/Photos/Users/Emojis/Labels/Galleries/Templates. Added an "Admin" hamburger-menu link (pointing at `/admin-master.html`) to all 7 pages that already had "Gallery Admin"/"Template Admin" links (`index.html`, `photo.html`, `galleries.html`, `gallery-admin.html`, `template-admin.html`, `display.html`, `display-edit.html`), matching the existing unconditional-link/backend-enforced convention rather than introducing new frontend permission-gating for nav visibility.

### Testing notes
No Go toolchain or browser available in this sandbox (same constraint as every prior session). Verified via: brace/paren-balance counts on every modified/new Go file; `node --check` on `app.js` and `admin.js`; HTML open/close tag-balance counts on all 12 touched/new HTML files; a grep sweep for CSP-incompatible patterns (semicolon-separated multi-statement directive expressions, template literals) across all admin pages — caught and fixed two semicolon-chained `@click` handlers in `admin-users.html`'s pagination buttons (`offset = ...; loadUsers()`), replaced with proper `prevPage()`/`nextPage()` methods matching the pattern already used in `admin-emojis.html`/`admin-labels.html`. Manually re-read every modified handler function end-to-end for parameter-count/placeholder-numbering correctness on the hand-built `fmt.Sprintf` SQL (several endpoints use a variable number of optional WHERE clauses).

### Open items
- Still no way to actually run this against a live Postgres/Go build in this environment — the user should run `migrations/017_admin_phase6.sql`, rebuild, and click through all four new admin pages (especially the per-user toggles and the emoji/label enable-disable flows) before trusting this in production.
- 6a's "active label/emoji" counts are intentionally site-wide, not per-exhibition — flagging in case that's surprising when multiple exhibitions are in play.
- No UI yet for granting `UserAdmin`/`EmojiAdmin`/`LabelAdmin`/`GalleryAdmin` themselves (i.e. an admin-of-admins page) — Phase 6 as specified only covers using those permissions, not managing who holds them; that would need to go through Phase 2g (gallery permissions UI) or a future extension of this work.

## Session 58 — Emoji admin: prefer OpenMoji graphics over the browser font

### What was done
User reported the emoji admin grid (`app/admin-emojis.html`) was rendering built-in emoji using the browser's native emoji font (`em.emoji`), which only covers a subset of the OpenMoji set that `cmd/import-emojis` actually imports (it populates `image_url` for every emoji from `https://openmoji.org/data/color/svg/<hexcode>.svg`). `photo.html` and `app.js`'s `emojiShortcodeHtml` already prefer the image over the native character; the admin page had the precedence backwards. Swapped it: the `<img>` (OpenMoji graphic) now renders whenever `image_url` is set, with the native character only as a fallback for the handful of seed reactions (`migrations/002_seed.sql`) that predate running `import-emojis` and have no `image_url` yet.

### Testing notes
Re-ran the HTML tag-balance check on `admin-emojis.html` — OK. No other page needed the same fix (all others already preferred the image).

### Open items
None — this was a small, isolated precedence fix.

## Session 59 — Emoji admin: source/status filters + used-only toggle

### What was done
Extended `GET /api/v1/admin/emoji-types` with three new filters, replacing the old boolean `include_disabled` param entirely:
- `source=all|openmoji|custom` — distinguishes OpenMoji-imported emoji (non-null `hexcode`, set by `cmd/import-emojis`) from user-uploaded custom emoji (`hexcode` is never set by `POST /api/v1/emoji/types`'s `UploadType` handler).
- `status=all|enabled|disabled` — replaces the old `include_disabled` boolean with a proper tri-state.
- `used_only=true` — filters to emoji with at least one reaction anywhere, via `EXISTS (... FROM emoji_reactions er WHERE er.emojiid = et.emojiid)` rather than the pre-aggregated `usage_count` column, so it's correct regardless of the LEFT JOIN used for display. Had to alias the COUNT query's `FROM emoji_types` as `et` (it wasn't aliased before) so this EXISTS clause — which needs to correlate against the outer row — works identically in both the count and page queries.

`app/admin-emojis.html`/`app/admin.js`'s `adminEmojis` component: swapped the single "Show disabled" checkbox for two `<select>` dropdowns (source, status) plus a "Used only" checkbox; all three call the existing `doSearch()` (resets to page 1, reloads).

### Testing notes
Re-checked brace/paren balance on `emojis.go`, `node --check` on `admin.js`, HTML tag balance and the CSP-incompatible-pattern grep on `admin-emojis.html` — all clean.

### Open items
None.

## Session 60 — Emoji admin: infinite scroll instead of prev/next

### What was done
Replaced `admin-emojis.html`'s prev/next pagination buttons with infinite scroll, matching the exact pattern `admin.html`'s photo grid (`adminApp`) already uses: a `window.addEventListener('scroll', ...)` listener that fires `loadMore()` when within 400px of the bottom, `loadMore()` appends to (rather than replaces) the list and advances `offset` by however many rows actually came back, and a bottom spinner shows while fetching more once the first page is already on screen. `doSearch()` (called by the search box and all three filters) now clears `emojis`/`offset`/`total` and calls `loadMore()` fresh, same reset-then-load shape as `adminApp.doSearch()`. Removed the now-unused `prevPage()`/`nextPage()` methods from `adminEmojis` (adminLabels still has its own prev/next buttons — this change was scoped to the emoji admin page only, as asked).

### Testing notes
`node --check` on `admin.js`, HTML tag-balance and CSP-incompatible-pattern grep on `admin-emojis.html` — all clean. Confirmed no leftover references to `prevPage`/`nextPage`/pagination markup in the HTML.

### Open items
None.

## Session 61 — Teams admin page + global/exhibition permission-grants viewer

### What was done
Added two new admin pages: full team management, and a viewer/revoker for permission grants, split exactly along an existing SQL-level distinction discovered in `permissions.Checker.Check()`: grants with `entity_role_grants.resource_type IS NULL` ("global") aren't filtered by `roles.exhibitionid` at all and apply across every exhibition, while `resource_type = 'Exhibition'/'Gallery'/'Display'/'Photo'` grants are tied to one exhibition. This maps directly onto the user's request for a page of "global permission grants that are independent of exhibition" plus a separate page of "exhibition specific permission grants."

**Permissions** (`internal/permissions/permissions.go`): added `PermTeamAdmin` and `PermPermissionsAdmin` constants, alongside the existing `Admin`/`LabelAdmin`/`EmojiAdmin`/`UserAdmin`/`GalleryAdmin` set. Added both to the Admin role's bundle in `scripts/seed-exhibition.sh` — existing full Admins (who hold bare `PermAdmin`) get access immediately since every new endpoint checks `HasAny(PermAdmin, PermXAdmin)`.

**Teams backend** (`internal/handlers/teams.go`, new): `List`/`Create`/`Update`/`Delete` on `/api/v1/admin/teams` (+`:teamid`), and `ListMembers`/`AddMember`/`RemoveMember` on `/api/v1/admin/teams/:teamid/members` (+`:userid`). `Delete` is transactional: soft-deletes the team, then strips its `entity_role_grants` and `team_members` rows in the same transaction — necessary because `Checker.Check()`'s Team-entity branch resolves membership via `team_members` alone and never checks `teams.deleted_at`, so a merely-soft-deleted team would otherwise keep conferring permissions to its former members.

**Permission-grants backend** (`internal/handlers/admin_grants.go`, new): `ListGlobal` (`GET /api/v1/admin/grants/global`) and `ListForExhibition` (`GET /api/v1/admin/grants/exhibition`) return entity name (user/team/public/logged-in), role name, the role's permissions (via `array_agg` over `role_permissions`), and either the home exhibition (global) or a resolved resource name (Gallery/Display/Photo/Exhibition title, exhibition-specific). `Revoke` (`DELETE /api/v1/admin/grants/:grantid`) deletes one `entity_role_grants` row. Scope was deliberately limited to view + revoke, not creating brand-new arbitrary grants — the user asked for "viewing," and revoke is a natural low-risk complement (an admin who spots something wrong needs some lever) but full grant-authoring was judged a separate, larger feature.

**Router** (`internal/handlers/router.go`): wired both new handlers and all new routes under the existing "Admin endpoints" section; the teams route nesting (`/teams` → `/teams/:teamid` → `/teams/:teamid/members` → `/teams/:teamid/members/:userid`) mirrors the pre-existing galleries/displays nesting already working in this router.

**Frontend**: `app/admin-teams.html` (new) — exhibition selector, add-team form, search, per-team edit-in-place, expandable member panel with search-and-add/remove. `app/admin-permissions.html` (new) — two sections (Global grants, Exhibition-specific grants), each a table with entity/role/permission-badges/home-or-resource/granted-date/revoke columns. Both share `app/admin.js`'s existing `loadAdminExhibitions`/`crossHostRedirect`/`thumbUrl` helpers and the established admin-page header/nav conventions. Added `adminTeams()` and `adminPermissions()` Alpine components to `admin.js` and registered them in the `alpine:init` block.

**Nav-bar cross-linking**: inserted "Teams" (after Users) and "Permissions" (after Labels) links into the shared nav bar of all 5 pre-existing admin pages (`admin.html`, `admin-master.html`, `admin-users.html`, `admin-emojis.html`, `admin-labels.html`), giving every admin page the same 9-item nav: Overview/Photos/Users/Teams/Emojis/Labels/Permissions/Galleries/Templates. Also added "Teams" and "Permissions" cards to `admin-master.html`'s card grid (previously only Photos/Users/Emojis/Labels).

### Testing notes
No Go toolchain or browser available in this sandbox (same standing constraint as every prior session). Verified via: brace/paren-balance counts on `teams.go`, `admin_grants.go`, `emojis.go`, `router.go`, `permissions.go` (all balanced); `node --check` on `admin.js` (passed); HTML tag-balance counts plus a CSP-incompatible-pattern grep (semicolon-chained directive expressions, template literals) across all 7 touched/new admin HTML pages (all clean). Manually re-verified SQL placeholder numbering across the dynamically-built WHERE clauses, the count-vs-page-query alias consistency, and that `ORDER BY` on a computed `CASE ... END AS resource_name` alias is valid Postgres.

### Open items
- Still no way to run this against a live Postgres/Go build in this sandbox — recommend rebuilding and clicking through both new pages (team CRUD + membership changes, and revoking a grant of each kind) before trusting this in production.
- Permissions viewer has no UI for authoring brand-new grants (assigning a role to an entity for the first time) — intentionally out of scope this round; would need its own follow-up if wanted.
- Global grants list currently has no pagination (flat fetch, `limit=200`) — a deliberate simplification given typical small grant counts; revisit if a deployment ends up with more than that.

## Session 62 — Fixed: emoji admin showed no emoji after the infinite-scroll change

### What was done
User reported `admin-emojis.html` stopped showing any emoji after Session 60's infinite-scroll change. Root cause: `adminEmojis`'s data still initialized `loading: true` (leftover from before infinite scroll, when `loading` only drove the skeleton display), but `loadMore()` now guards its own entry with `if (this.loading) return;` so a second concurrent call doesn't double-fetch. Since `init()` calls `loadMore()` while `loading` was still `true` from initialization, that very first call hit the guard and returned immediately — no fetch ever happened, and the skeleton just stayed empty forever. `adminApp` (the photo grid, which originated this same scroll-loading pattern) correctly initializes `loading: false`; `adminEmojis` had been left on the old default. Fixed by changing `adminEmojis`'s initial `loading` to `false`, matching `adminApp`.

### Testing notes
`node --check` on `admin.js` — OK. Grepped every `loading:` initializer in `admin.js` to confirm no other component has the same guard-vs-initial-state mismatch (only `adminApp`/`adminEmojis` use the scroll-guard `loadMore()` pattern; both now initialize `false`).

### Open items
None.

## Session 63 — Exhibition-scoped grants via a new `exhibitionid` column (+ fixed a real cross-exhibition permission leak)

### What was done
User asked to make it easier to scope a permission grant to one specific exhibition: add an `exhibitionid` column to `entity_role_grants` directly, rather than the old convention of `resource_type = 'Exhibition'` + `resource_ref = <exhibitionid as text>`. Per the user's framing, `exhibitionid` and `resource_ref` are now mutually exclusive on a row, and `exhibitionid IS NULL` means the grant applies to every exhibition.

**Bug found and flagged before implementing** (confirmed with the user via a clarifying question before touching data): `Checker.Check()`'s "global" branch (`resource_type IS NULL`) never filtered on the granting role's home exhibition, so every grant with no resource scope — the Viewer→Public, Contributor→LoggedIn, and Admin→Admins-team grants `scripts/seed-exhibition.sh` creates for *every* exhibition, plus every per-user "view private photos" singleton grant — has actually been leaking across every exhibition in the deployment the whole time (e.g. being on one exhibition's Admins team silently made you an Admin of every other exhibition too). User confirmed: scope all existing grants to their exhibition during the migration, closing the leak immediately rather than preserving it.

**Migration** (`migrations/018_grant_exhibitionid.sql`): adds `exhibitionid UUID REFERENCES exhibitions ON DELETE CASCADE` to `entity_role_grants`. Backfill runs in two ordered steps to avoid data-loss on an edge case: (1) every row with `resource_type IS NULL` gets `exhibitionid = ` its role's home exhibition — this is the leak fix, and must run first since it only touches rows that aren't `resource_type = 'Exhibition'` yet; (2) every remaining `resource_type = 'Exhibition'` row moves its target exhibition from `resource_ref` into the new `exhibitionid` column (preserving the exact value, in case it ever differed from the role's own home exhibition) and clears `resource_type`/`resource_ref`. Then the `resource_type` CHECK constraint is narrowed to `('Gallery', 'Display', 'Photo')` (Exhibition is no longer a valid value), a new `chk_exhibitionid_resource_exclusive` CHECK enforces the mutual-exclusivity rule, and a partial index is added on `exhibitionid`.

**`internal/permissions/permissions.go`**: `Check()` and `UserPermissions()` rewritten — the "global" branch is now `(erg.exhibitionid IS NULL AND erg.resource_type IS NULL)`, and the "exhibition-level" branch is `(erg.exhibitionid = $N::uuid AND erg.resource_type IS NULL)`, replacing the old `resource_type = 'Exhibition' AND resource_ref = $N`. Removed the now-invalid `ResourceExhibition` constant. Module doc comment rewritten to describe the new mutually-exclusive scoping model. Also fixed the singleton per-user-grant mechanism (`GrantUserPermission`/`RevokeUserPermission`/`HasDirectUserGrant`, Session 57's "view private photos" toggle) to set/match `exhibitionid` instead of relying on bare `resource_type IS NULL` — this was exactly the leak described above for that one mechanism.

**`internal/handlers/admin.go`**: `ListUsers`' `can_view_private` `EXISTS` subquery now also filters `erg.exhibitionid = $N::uuid`, matching the fixed singleton-grant scoping.

**`internal/handlers/admin_grants.go`** (the Phase 6g permissions viewer from Session 61): `ListGlobal`'s WHERE is now `erg.exhibitionid IS NULL AND erg.resource_type IS NULL`; `ListForExhibition`'s WHERE is now `(erg.exhibitionid = $1 AND erg.resource_type IS NULL) OR (erg.resource_type IN (...) AND r.exhibitionid = $1)`. The `resource_name` CASE dropped its `'Exhibition'` branch and is now wrapped in `COALESCE(..., '')` so an exhibition-level row (resource_type NULL) scans cleanly into the non-nullable Go string field instead of erroring on a NULL scan.

**`scripts/seed-exhibition.sh`**: the three grant INSERTs (Viewer→Public, Contributor→LoggedIn, Admin→Admins team) now set `exhibitionid = $EXHIBITION_ID` explicitly (and their idempotency `NOT EXISTS` guards check it too) — otherwise every exhibition seeded *after* this migration would immediately recreate the same leak for itself.

**`app/admin-permissions.html`**: the exhibition-specific grants table's "Resource" column now shows "(entire exhibition)" with an "Exhibition" type label when `resource_type` is empty (the new exhibition-level case), instead of rendering blank.

### Testing notes
No Go toolchain or live Postgres in this sandbox (same standing constraint as ever). Verified via: brace/paren-balance counts on `permissions.go`, `admin_grants.go`, `admin.go` (all balanced); `bash -n` on `seed-exhibition.sh` (clean); HTML tag-balance + CSP-pattern grep on `admin-permissions.html` (clean); a full-codebase grep for `resource_type = 'Exhibition'` / `ResourceExhibition` confirming no leftover references outside the migration's own backfill SQL and doc comments. Manually re-traced the migration's two-step backfill ordering against the one edge case that could otherwise corrupt data (an `Exhibition`-type grant whose `resource_ref` names a different exhibition than its role's own home one) and confirmed the ordering leaves that value untouched rather than silently overwriting it.

### Open items
- Still needs a real Postgres + rebuild to actually run `migrations/018_grant_exhibitionid.sql` and confirm the backfill produces the expected row counts before trusting this in production — recommend spot-checking a few exhibitions' grants afterward, especially any multi-exhibition deployment that predates this session.
- No UI yet for *creating* a new exhibition-scoped (or Gallery/Display/Photo-scoped) grant from scratch — Session 61's permissions viewer was intentionally scoped to view + revoke only, and this session's exhibitionid work was purely the underlying schema/enforcement change the user asked for. A "create a new grant" UI would be a natural next step if wanted.
- The private-photo-view toggle's behavior technically changes for any exhibition where it was already set: before this fix it silently applied everywhere; after, it applies only within the exhibition it was granted in. This is the intended fix, but flagging in case anyone was unknowingly relying on the old cross-exhibition leak.

## Session 64 — Add-grant popup: create new permission grants from the admin UI

### What was done
Session 61 built the permissions viewer as view-and-revoke only, deliberately leaving grant creation for later. This session added it: an "+ Add grant" button (top right of `admin-permissions.html`'s header) opens a popup that creates a new `entity_role_grants` row through the exhibitionid-based scoping model from Session 63.

**Backend** (`internal/handlers/admin_grants.go`): two additions.
- `ListRoles` (`GET /api/v1/admin/roles?exhibitionid=`) — lists an exhibition's non-deleted roles with their bundled permissions, to populate the popup's Role dropdown. There still isn't a role browser/editor anywhere in the app (roles are only ever created by `scripts/seed-exhibition.sh` or the auto-managed singleton-permission roles) — this endpoint only reads.
- `Create` (`POST /api/v1/admin/grants`) — validates the request body against the same rules the database itself enforces (entity_type/entity_ref pairing, exhibitionid vs. resource_type/resource_ref mutual exclusivity, resource_type/resource_ref pairing) before inserting, so a malformed request gets a clear 400 instead of a raw constraint-violation error. Sets `granted_by` to the acting admin. Both endpoints gate on `HasAny(PermAdmin, PermPermissionsAdmin)`, matching every other Phase 6g/6h endpoint.

**Router**: wired `GET /api/v1/admin/roles` and `POST /api/v1/admin/grants`.

**Frontend** (`app/admin-permissions.html`, `app/admin.js`): the popup has a Role dropdown; an entity-type dropdown (Public/LoggedIn/Team/User) that reveals a Team dropdown or a User search-and-select (reusing the existing `/api/v1/admin/users` search, same pattern as `admin-teams.html`'s member-add flow) as appropriate; a "Global grant" checkbox (exhibitionid IS NULL); and — only when not global — a resource-type dropdown (entire exhibition / Gallery / Display / Photo) that reveals a Gallery dropdown, a Gallery-then-Display cascade (picking a gallery loads its displays via `GET /api/v1/galleries/:galleryid`), or a Photo search-and-select (reusing the existing admin photo search). Grant/Cancel buttons at the bottom. On success, both the Global and Exhibition-specific lists reload so the new grant appears immediately.

**Bug caught and fixed before it shipped**: the first draft of `submitGrant()` sent `exhibitionid = selectedExhibition` whenever the Global checkbox was unchecked — including when a specific Gallery/Display/Photo was also chosen, which the server correctly rejects (exhibitionid and resource_type are mutually exclusive per `migrations/018_grant_exhibitionid.sql`). Fixed so `exhibitionid` is only sent for the "entire exhibition, no specific resource" case; a chosen resource carries its own exhibition implicitly and is sent alone.

### Testing notes
No Go toolchain or live Postgres in this sandbox (unchanged standing constraint). Verified via: brace/paren-balance counts on `admin_grants.go`, `router.go`, `permissions.go`, `admin.go`, `teams.go` (all balanced, `admin_grants.go` unaffected by other files but re-checked for safety); `node --check` on `admin.js`; HTML tag-balance and the CSP-incompatible-pattern grep (semicolon-chained directives, template literals) across all 7 admin pages (all clean — the popup uses only ternaries and string concatenation in bindings, no template literals). Manually re-traced all three grant shapes (global / whole-exhibition / specific-resource) the popup can produce against the `Create` handler's validation branches to confirm each one passes and produces the intended row.

### Open items
- The Role dropdown only lists roles belonging to the currently-selected exhibition — a role can technically still be granted globally or against a different exhibition's resource, but the popup doesn't offer cross-exhibition role browsing. Fine for the common case; would need a "browse all roles" endpoint if that's ever wanted.
- No role browser/editor exists yet — `ListRoles` only reads what `seed-exhibition.sh` (or the singleton-permission mechanism) already created. Creating brand-new roles or editing their permission bundles from the UI would be a separate feature.
- The Team/Gallery/Photo pickers reuse existing endpoints (`/api/v1/admin/teams`, `/api/v1/galleries`, `/api/v1/admin/photos`) that are gated on slightly different permission sets than the grants viewer itself (`PermTeamAdmin`/`GalleryView`/strict `PermAdmin` respectively) — an admin who holds only `PermPermissionsAdmin` (without also `PermAdmin`) could see some of those pickers come back empty/403. Not a new problem this session introduced, just worth knowing if that combination comes up.
- Still needs a real Postgres + rebuild to click through the popup end-to-end (all four entity types, all four resource scopes, plus the validation error paths) before trusting this in production.

## Session 65 — Add-grant button: repositioned and restyled

### What was done
User feedback on Session 64's "+ Add grant" button: it was in the header (styled `bg-accent`, a reddish/pink fill) and should instead sit below the admin-page nav bar, styled grey like the rest of the page's pill buttons. Moved the button out of `admin-permissions.html`'s `<header>` into its own right-aligned row at the top of the main content area (below the nav bar, above the "Global grants" section), and swapped its class from `bg-accent text-white hover:opacity-90` to `bg-gray-200 hover:bg-accent hover:text-white` — the same grey-pill style already used by every "Search" button across the admin.js page family (`admin-teams.html`, `admin-emojis.html`, etc.), so it now matches the established convention rather than standing out in red.

### Testing notes
HTML tag-balance and CSP-pattern grep on `admin-permissions.html` — clean.

### Open items
None.

## Session 66 — Add-grant popup: reordered fields (entity → resource → role)

### What was done
User feedback: the popup's field order was confusing (Role first, then Entity, then Global/Resource). Reordered `admin-permissions.html`'s form so it reads in the natural order of "who gets it, what it applies to, what it grants": entity type + entity selector (Team/User) first, then the Global checkbox and resource-scope fields (resource type + Gallery/Display/Photo selector), then the Role dropdown last, immediately above the Grant/Cancel buttons. Pure markup reordering — no changes to `admin.js`'s logic, since all the fields were already independent `<div>` blocks with no ordering dependencies between them.

### Testing notes
HTML tag-balance and CSP-pattern grep on `admin-permissions.html` — clean. Confirmed via grep that the comment markers now appear in the order Entity type → Global checkbox → Resource type → Role.

### Open items
None.
