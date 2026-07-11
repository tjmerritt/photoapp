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
