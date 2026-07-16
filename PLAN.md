# Implementation Plan

This document organizes all TODO.md items into a phased implementation plan, ordered by dependency. Later phases depend on earlier ones being in place.

---

## Phase 1 — Permissions System
**Blocks everything else that involves access control.**

The rough concept is in PERMISSIONS.md. This phase makes it real.

### 1a. Define the model in detail
- Document the full matrix: Entity × Resource × Permission
- Entities: `Public`, `LoggedIn`, `Team:<name>`, `User:<username>`
- Resources: `Global`, `Exhibition`, `Gallery`, `Display`, `Photo`, `PublicPhoto`, and their group variants
- Permissions per resource type (View, Create, Modify, Delete; plus sub-permissions for Labels/Emoji/Comments on Photos)
- Define "role" as a named collection of permissions
- Roles are granted to entities — all permissions in the role become available to every user within that entity
- Role grants can be **resource-scoped**: granting a role to an entity for a specific resource (e.g., a gallery) limits those permissions to that resource only

### 1b. DB migration
- `permissions` table: `(id, entity_type, entity_ref, resource_type, resource_ref, permission, granted_at)`
- `roles` table: `(roleid, name, exhibitionid)`
- `role_permissions` table: `(roleid, permission)` — the permissions bundled in a role
- `entity_role_grants` table: `(id, roleid, entity_type, entity_ref, resource_type, resource_ref)` — grants a role to an entity, optionally scoped to a resource; `resource_type`/`resource_ref` NULL means global within the exhibition
- Index on `(entity_type, entity_ref, resource_type, resource_ref)`

### 1c. Backend enforcement
- `CheckPermission(ctx, entity, resource, permission) bool` function in a new `internal/permissions` package
- Middleware or helper that resolves the calling user → entities (their user ID + their teams + `LoggedIn` or `Public`)
- Wire into existing handlers: photos, labels, emojis, comments
- Admin endpoints: require a specific `Admin` permission rather than the current `authorized_non_public` flag

### 1d. API surface
- `GET /api/v1/permissions` — what the current user can do (used by frontend to hide/show controls)

---

## Phase 2 — Gallery & Display Resources
**Depends on Phase 1 for access control.**

### 2a. DB migrations
- `galleries` table: `(galleryid, exhibitionid, title, sort_order, created_at, updated_at, deleted_at)`
- `displays` table: `(displayid, galleryid, sort_order, template_id, created_at, updated_at, deleted_at)`
- `display_templates` table: `(templateid, name, photo_count, slot_positions JSONB, presentation JSONB)`
- `display_slots` table: `(slotid, displayid, slot_index, photoid, rich_text, placard JSONB)`
- `placard_defaults` table: per-gallery defaults for placard rendering

### 2b. Backend — Gallery CRUD
- `GET /api/v1/galleries` — list galleries for current exhibition
- `POST /api/v1/galleries` — create gallery (auth + Create permission)
- `GET /api/v1/galleries/:galleryid` — get gallery with its display list
- `PATCH /api/v1/galleries/:galleryid` — update title, reorder displays, set placard defaults
- `DELETE /api/v1/galleries/:galleryid` — soft delete

### 2c. Backend — Display CRUD
- `GET /api/v1/displays/:displayid` — full display with slots and placard data
- `POST /api/v1/galleries/:galleryid/displays` — add display to gallery
- `PATCH /api/v1/displays/:displayid` — edit display (select template, add/remove/reorder photos, edit placard per slot)
- `DELETE /api/v1/displays/:displayid` — remove display

### 2d. Backend — Display Templates
- `GET /api/v1/display-templates` — list available templates
- `POST /api/v1/display-templates` — create template (admin only)
- `PATCH /api/v1/display-templates/:templateid` — edit template (name, photo count, slot positions, presentation rules)
- `DELETE /api/v1/display-templates/:templateid`

### 2e. Frontend — Gallery pages
- Gallery list view
- Create/Edit Gallery page: title, reorder displays (drag-and-drop), placard defaults, add/remove displays
- Review Displays view within gallery
- **Hamburger menu integration**: add a "Galleries" item to the nav menu; hovering it reveals a sub-menu listing all available galleries for the current exhibition with direct links

### 2f. Frontend — Display editor
- Select display type (template picker)
- Slot editor: add photo (search/browse), remove photo, edit placard per slot
- Preview of the rendered display

### 2g. Gallery Permissions UI
- Per-gallery permission management page (grant/revoke entity access)

---

## Phase 3 — Photo Wall
**Independent of Phases 1–2 but benefits from the photo data model already in place.**

The current photo viewer lives in `app/index.html`. The goal is a "wall" view as the new index page.

### 3a. Move current viewer
- Rename/copy `app/index.html` photo viewer to `app/photo.html`
- Update any internal links

### 3b. Row-layout algorithm
Implement as a JS module (`app/wall.js` or inline in the new `index.html`):

1. For each candidate row, pick `n` photos where `n ∈ {2, 3, 4}` (4 only when photos are similarly shaped or two+ are portrait)
2. Compute `maxHeight` = max of the natural heights in the row
3. Per photo: `photoScale = maxHeight / photoHeight`
4. `nominalWidth` = Σ `(photoWidth × photoScale)`
5. `rowScale = containerWidth / nominalWidth`
6. Final size: `scaledWidth = photoWidth × photoScale × rowScale`, `scaledHeight = maxHeight × rowScale`
7. Constraint: consecutive rows should not have the same `n`

### 3c. Wall page
- New `app/index.html`: fetches paginated photo list, renders rows using the layout algorithm
- Lazy-loads more rows on scroll
- Clicking a photo navigates to `photo.html?id=<photoid>`

---

## Phase 4 — Microsoft Sign-in
**Independent. Facebook sign-in is already complete (migration 011, auth.go).**

- Register an app in Azure AD (document the required config vars)
- Add `MicrosoftClientID`, `MicrosoftClientSecret`, `MicrosoftRedirectURL` to `internal/config/config.go`
- Implement `MicrosoftLogin` and `MicrosoftCallback` in `internal/handlers/auth.go` following the existing Facebook/Google pattern
- Add `GET /auth/microsoft` and `GET /auth/microsoft/callback` routes in `router.go`
- DB migration if needed (the `auth_providers` table from migration 006 should already support it — just add `microsoft` as a provider value)
- Wire `microsoftEnabled` into `GET /auth/config`
- Add the Microsoft sign-in button to the frontend login UI

---

## Phase 5 — Attributes

### 5a. Label colors
- **Color fixed to label name**: store `(labelname, color_hex)` in a `label_name_colors` table; derive color deterministically (hash → palette) as fallback
- **Color picker**: UI to assign/override a color for a label name (admin or label-name-owner)

### 5b. Restricted labels
- Add `restricted BOOLEAN DEFAULT FALSE` to `label_names` table (migration)
- Restricted labels may be **viewed** but not added, deleted, or modified by regular users
- Restriction is applied automatically: EXIF import always marks its labels restricted; the `import-photos` CLI gains a `--restrict-labels` flag to restrict any labels from that run; if a label already exists, the restriction flag is added to it
- Backend: `POST /api/v1/labels`, `PATCH /api/v1/labels/:id`, and `DELETE /api/v1/labels/:id` check the label name's `restricted` flag and reject (403) unless the caller has an `Admin` or `LabelAdmin` permission

### 5c. Emoji improvements
- **Reaction tooltip**: show emoji name alongside the emoji character (already visible) when hovering the reaction button ("Reacted with :thumbsup:" not just 👍)
- **User-added emojis**: already has `POST /api/v1/emoji/types`; expose the upload UI more prominently
- **Popular emojis first page**: sort `GET /api/v1/emoji/types` by `usage_count DESC` for the first page, then alphabetical
- **Paged emoji download**: the existing `emoji/types` endpoint has pagination; ensure the frontend uses it lazily (don't fetch all pages upfront)
- **Search**: `GET /api/v1/emoji/types?q=<term>` — search by alttext/name, return matching emoji with name + image

### 5d. Rich text comments
- Switch comment storage from plain text to Markdown (DB column type stays `TEXT`, just change validation)
- **Editor**: integrate a lightweight rich-text editor (e.g., [Tiptap](https://tiptap.dev/) or a Markdown textarea with preview)
  - Emoji insertion via `:emoji:` shorthand, with a picker popup
  - Bold, italic, strikethrough toolbar buttons
  - Font name, weight, size selection (stored as Markdown with HTML spans or a custom syntax)
- **Rendering**: render stored Markdown to HTML in the frontend (use a client-side Markdown library, e.g., `marked`)
- Backend: validate that comment body is not empty; sanitize HTML on output

---

## Phase 6 — Admin Pages
**Depends on Phase 1 (permissions) for the authorization checks.**

### 6a. Master admin page (`app/admin-master.html`)
- Links to each specialized admin page
- Quick-stats panel (user count, photo count, active labels/emojis)

### 6b. User admin (`app/admin-users.html`)
- List users with their flags
- Toggle per-user:
  - Account enabled/disabled
  - Access to private photos
  - Label adding/editing own
  - Emoji adding/removing own
  - Comment add/edit/delete own
- Backend: `GET /api/v1/admin/users`, `PATCH /api/v1/admin/users/:userid`
- DB: `user_flags` table (or columns on `users`) for each toggle

### 6c. Public access admin (enhance existing `app/admin.html`)
- Add photo search/filter to limit the visible set
- Existing enable/disable public per photo stays as-is
- Wire to existing `GET /api/v1/admin/photos` + `PATCH /api/v1/admin/photo`

### 6d. Emoji admin (`app/admin-emojis.html`)
- List all emoji types with enable/disable toggle
- Toggle "show disabled emojis" in the listing
- Search by name/alttext
- Backend: `PATCH /api/v1/admin/emoji/:emojiid` with `{ "enabled": bool }`

### 6e. Label admin (`app/admin-labels.html`)
- List all label names with enable/disable and restrict/unrestrict toggles
- Toggle "show disabled label names" in the listing
- Backend: `PATCH /api/v1/admin/label-name/:labelnameid` with `{ "enabled": bool, "restricted": bool }`

---

## Phase 7 — Photo Uploads
**Independent. Only requires existing auth. Label-on-upload feature benefits from Phase 5a (label colors) but is not blocked by it.**

### 7a. Title bar upload icon
- Add an upload icon button to the global nav/title bar
- Clicking it opens the upload popup (modal or dedicated page)
- **Drag-and-drop to the icon**: dragging files onto the title bar icon opens the upload popup and immediately begins uploading the dropped files

### 7b. Upload popup
- **Drop zone**: large drag-and-drop target; also supports a file picker (click to browse)
- **Upload queue**: shows each file being uploaded with its filename and status (pending / uploading / done / error)
- **Progress bar**: per-file progress bar driven by XHR upload progress events
- **Batch labels**: a label editor below the queue lets the user add labels that will be applied to every photo in the current batch
  - Labels can be added, edited, or removed while uploads are already in progress; newly added labels apply to any photo not yet submitted
- Backend: the existing `import-photos` binary handles server-side import; the upload endpoint needs to accept multipart POST from the browser, save the photo, run EXIF extraction, and apply any batch labels atomically

### 7c. Backend upload endpoint
- `POST /api/v1/photos/upload` (auth required): accepts multipart form with one or more image files + optional JSON label array
- Reuses EXIF extraction logic from `cmd/import-photos`
- Returns a stream or array of per-file results (photoid, status, errors)

---

## Dependency Summary

```
Phase 1 (Permissions)
    └── Phase 2 (Gallery/Display) — needs permission checks
    └── Phase 6 (Admin pages)    — needs permission enforcement

Phase 3 (Photo Wall)       — independent, can start anytime
Phase 4 (Microsoft Auth)   — independent, can start anytime
Phase 5 (Attributes)       — mostly independent; label restrictions need Phase 1
Phase 7 (Photo Uploads)    — independent; label-on-upload benefits from Phase 5a
```

Recommended order: **1 → 3 → 4 → 7 → 5 → 2 → 6**
(Start with the independent items while the permissions model is being finalized, then build Gallery/Display and Admin once permissions are solid.)
