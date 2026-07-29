# Implementation Plan 2

Organizes TODO2.md into phased work, ordered by dependency. Follows completion of PLAN.md (Plan 1). Non-engineering items (ticketing/AI support, marketing) are parked at the end as a separate track.

**Testing rule:** Phase 0 sets up test infrastructure. Every subsequent phase ships with unit tests for its new code.

---

## Phase 0 — Test Infrastructure

- Go: `go test` conventions, testable DB layer (test database or fixtures), `make test` target
- JS: lightweight runner (e.g., Vitest) for `app/` modules
- Backfill tests for critical existing paths: permission checks, photo/label/emoji/comment handlers, wall layout algorithm
- Hook into build so tests run before commits

---

## Phase 1 — Organizations (Foundation)

The Organization is the billing entity and owns exhibitions. Many later items depend on this model.

### 1a. Data model
- `organizations` table; `exhibitions.organization_id`
- Auto-create an organization for a user when they create their first exhibition

### 1b. Emoji ownership
- Uploaded emojis are owned by an organization
- Emojis not owned by any organization are global — usable in all organizations; only added via `import-emojis`

### 1c. Org admin
- Permissions scoped to the Organization but spanning all its exhibitions

### 1d. Header
- Show current organization in header
- Popup for global admins to select an alternate org
- **Open question:** Exhibition selection — popup or keep as dropdown?

---

## Phase 2 — Permissions Expansion
**Depends on Phase 1 (org scoping, global admin).**

### 2a. New permission types
- LabelName: Create, Modify, Delete
- Team: View, Create, Modify, Delete
- Role: View, Create, Modify, Delete
- Photo: PhotoView (view photo), PhotoEmojiReact (add/remove own emoji reactions on a photo)
- Comment: CommentEmojiReact (add/remove own emoji reactions on a comment/reply)
- Emoji: Create (upload), Modify (rename), Delete
- Display: View (grants PhotoView for all photos in that display), Create, Modify, Delete
- Gallery: View (grants PhotoView for all photos in displays within that gallery), Create, Modify, Delete

### 2b. Public flag migration
- Remove `is_public`; replace with a `Public` label

### 2c. Label-based grants
- Label-based permission grants for photos

### 2d. Photo access resolution
- Direct grants + indirect grants via display/gallery View permissions

### 2e. Global permission administration
- Admin tooling for grants across the whole database (global admins) and per-org (org admins)

---

## Phase 3 — Resource Groups
**Depends on Phase 2.**

### 3a. Group model
- Groups for Photos, Displays, Galleries, Exhibitions

### 3b. Labels on Displays, Galleries, Exhibitions
- Prerequisite for dynamic groups of those types (photos already have labels)

### 3c. Dynamic groups
- Membership derived from labels

### 3d. Grants on groups
- Resource groups usable as the resource in permission grants

---

## Phase 4 — Shared Headers & Admin UI
**Depends on Phases 1–2 (org display, permission-aware admin).**

### 4a. Header templating
- Enable Go `net/html` templating solely for a common header include
- Two header styles: regular and admin
- Regular header supports disabling nearly every element — used in displays to reduce distraction from photos

### 4b. Rename
- "Gallery Admin" → "Gallery Manager" in menu items (not on the admin page)

### 4c. Gallery Manager
- Placard Settings behind a gear icon
- **Open question:** TODO2 has a truncated line here ("Change ...") — intent unknown

### 4d. Display Manager
- Gear icon for settings: overrides of Template and Gallery settings; change template
- Thumb-drag position to change slot ordering

### 4e. Overview page
- Cover: Organizations, Exhibitions, Teams, Users, Galleries, Displays, Templates, Photos, Roles, Permissions, Labels, Comments, Emojis
- "New" buttons jumping to the relevant pages
- Counts, including OpenEmoji vs. custom emoji counts

### 4f. Global admin affordances
- "Global" option in the Exhibition selector for global admins
- Heading title: "Admin" for org-level or lower; "Global Admin" for full-database admin
- Selectors for a specific organization and a specific exhibition

---

## Phase 5 — Frame Settings
**Independent; touches template/gallery/display editors from Plan 1 Phase 2.**

- Frame **color**: default in template → gallery override → display override → slot override
- Frame **size**: default in template → display override → slot override
- Document the override-resolution order; share the pattern with Display settings overrides (4d)

---

## Phase 6 — Inline Editing & Rich Text
**Needs Phase 2 permission API for error handling.**

- Click on text to edit; Escape cancels, Enter commits
- Toast on permission errors (e.g., no permission to edit)
- Rich text improvements — **Open question:** enumerate the specific improvements before starting

---

## Phase 7 — Media Formats & Watermarks
**Independent.**

- TIFF support (import + web-displayable conversion)
- HEIC support
- Photo watermarks

---

## Phase 8 — Presentation Features
**Independent.**

- Vertical waterfall (masonry) photo walls
- Gallery maps: visual layout map of the displays within a gallery, for navigation

---

## Phase 9 — Onboarding & Invitations
**Depends on Phases 1–3 (orgs, grants, groups).**

- Tools for creating exhibitions on cicada.photos (auto-creates the user's org, per 1a)
- Invitations: send and request flows
- Tools for inviting users to exhibitions and galleries (issues role/permission grants)

---

## Parked — Separate Track (not scheduled here)

- Ticketing system; feedback form that populates support tickets; AI-generated replies; AI-created feature-request tickets for unsupported capabilities
- Marketing: UI; channels (individuals, photographers, museums, photo archives); channel partners (scanning services, photo labs); marketing plan

---

## Open Questions

1. Truncated TODO2 line under Gallery admin ("Change ...") — what was intended?
2. Exhibition selector in header: popup or dropdown?
3. "Rich text improvements" — which specific improvements?

---

## Dependency Summary

```
Phase 0 (Tests)            — first; testing continues through all phases
Phase 1 (Organizations)
    └── Phase 2 (Permissions Expansion)
            └── Phase 3 (Resource Groups)
            └── Phase 6 (Inline Editing) — permission toasts
    └── Phase 4 (Headers & Admin UI) — also needs Phase 2
    └── Phase 9 (Onboarding & Invitations) — also needs Phases 2–3

Phase 5 (Frame Settings)   — independent, can start anytime
Phase 7 (Media/Watermarks) — independent, can start anytime
Phase 8 (Presentation)     — independent, can start anytime
```

Recommended order: **0 → 1 → 2 → 3 → 4 → 6 → 9**, interleaving **5, 7, 8** whenever a lighter or independent work item is useful (e.g., while design questions on the core track are being resolved).
