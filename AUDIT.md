# Permission Audit

This document records findings from a review of all API handlers against the
permissions model defined in `internal/permissions/permissions.go` and
`PERMISSIONS.md`.

---

## Prerequisites

Several findings share a common prerequisite. Handlers that act on a photo need
the photo's `exhibitionid` to perform meaningful permission checks (grants are
scoped to exhibitions).

**Status: RESOLVED** — `internal/handlers/resolve.go` provides:

```go
// resolvePhotoExhibition returns the exhibitionid for the given photo,
// or "" and pgx.ErrNoRows if the photo does not exist.
// Results are cached in a process-wide sync.Map (photoid → exhibitionid)
// because the relationship is immutable — a photo never changes exhibitions.
// Deleted photos are not re-queried because the cached exhibitionid remains
// correct even after soft-deletion. The cache clears on server restart.
func resolvePhotoExhibition(ctx context.Context, pool *db.Pool, photoid string) (string, error)
```

The `PermissionsHandler`, `PhotoHandler`, `SearchHandler`, and `AdminHandler`
are already wired with `Checker *permissions.Checker` fields via the router.

---

## Finding 1 — `GET /api/v1/photo`: legacy `authorized_non_public` flag used for photo and annotation visibility

**Handler:** `PhotoHandler.ServeHTTP`

**Status: RESOLVED** — `canSeeNonPublic` replaced with
`checker.Check(ctx, userID, exhibitionID, "", "", "", PermPrivatePhotoView)`.
The `authorized_non_public` column is dropped in migration 013.

To grant a user access to private photos, add them to a team whose role
includes `PrivatePhotoView`. The seed script grants this permission to the
Admin role by default.

The inline label/emoji/comment blocks within the photo response are still
returned unconditionally once the photo is visible; they are governed by the
standalone List endpoints (Finding 2, now resolved) which enforce the view
permissions independently.

---

## Finding 2 — `GET /api/v1/labels`, `GET /api/v1/emojis`, `GET /api/v1/comments`: no permission check

**Handlers:** `LabelsHandler.List`, `EmojisHandler.List`, `CommentsHandler.List`

**Status: RESOLVED** — Each `List` handler now checks the appropriate
exhibition-level view permission at the top before querying:

```
GET /api/v1/labels   → PhotoLabelView
GET /api/v1/emojis   → PhotoEmojiView
GET /api/v1/comments → PhotoCommentView
```

`Checker` is wired into all three handlers via the router. Since `Public` holds
the `Viewer` role by default, behavior is unchanged for current deployments but
will correctly enforce any future role configuration.

---

## Finding 3 — `POST /api/v1/labels`: authenticated but not permission-checked

**Handler:** `LabelsHandler.Create`

**Status: RESOLVED** — `resolvePhotoExhibition` now replaces the bare
photo-exists check and provides the exhibition for permission evaluation.
`PhotoLabelCreate` is checked scoped to the specific photo before inserting:

```go
checker.Check(ctx, userID, exhibitionID, "", ResourcePhoto, photoid, PermPhotoLabelCreate)
```

A `TODO(phase-5b)` marks the location for the restricted-label check once the
`label_names` table is introduced in Phase 5.

---

## Finding 4 — `PATCH /api/v1/labels/:labelid` and `DELETE /api/v1/labels/:labelid`: ownership-only check, no permission check

**Handlers:** `LabelsHandler.Update`, `LabelsHandler.Delete`

**Status: RESOLVED** — Both queries now fetch `photoid` alongside `ownerID`.
When the caller is not the label owner, the handler resolves the exhibition and
falls through to a `PermLabelAdmin` check before returning 403:

```
if caller == owner → allow
else if checker.Check(..., PermLabelAdmin) → allow
else → 403
```

The restricted-label branch (`PermLabelAdmin` required even for the owner) is
deferred to Phase 5b along with Finding 3's restricted-label hook.

---

## Finding 5 — `POST /api/v1/emoji/react` and `DELETE /api/v1/emoji/react`: authenticated but not permission-checked

**Handlers:** `EmojisHandler.React`, `EmojisHandler.Unreact`

**Status: RESOLVED** — Both handlers resolve the photo's exhibition via
`resolvePhotoExhibition` (returning 404 if the photo is missing) then check the
appropriate permission scoped to the photo before any DB write:

```
POST   → PhotoEmojiCreate scoped to the photo
DELETE → PhotoEmojiDelete scoped to the photo
```

The existing `WHERE userid=$3` in `Unreact` still ensures a user can only
remove their own reaction; the permission check gates entry to the operation.

---

## Finding 6 — `POST /api/v1/emoji/types`: any authenticated user can upload a custom emoji type, which is immediately activated

**Handler:** `EmojisHandler.UploadType`

**Current behavior:** Any authenticated user can upload a new emoji type. The
upload comment says "any authenticated user may upload." The inserted row has
`is_active = TRUE`, making the emoji immediately usable by everyone.

**Problem:** There is no `EmojiAdmin` check. A non-admin user can introduce new
site-wide emoji types without any approval.

**Proposed solution:** Two valid policies — decide before implementing:

- **Admin-only upload:** Require `PermEmojiAdmin` to upload. Simplest to
  enforce.
- **User suggestion with admin activation:** Allow any `Contributor` to upload
  but insert with `is_active = FALSE`. Require `PermEmojiAdmin` to activate.
  This matches a moderation workflow.

Either way, the current `is_active = TRUE` on insert should change unless the
admin-only policy is chosen.

---

## Finding 7 — `GET /api/v1/emoji/users`: no permission check

**Handler:** `EmojisHandler.ListUsers`

**Current behavior:** Anyone can list the users who reacted to a photo with a
specific emoji.

**Problem:** `PhotoEmojiView` is never checked.

**Proposed solution:** Same pattern as Finding 2 — check `PhotoEmojiView` at
the top of the handler before querying.

---

## Finding 8 — `POST /api/v1/comments`, `PATCH /api/v1/comments/:commentid`, `DELETE /api/v1/comments/:commentid`: authenticated but not permission-checked

**Handlers:** `CommentsHandler.Create`, `CommentsHandler.Update`,
`CommentsHandler.Delete`

**Current behavior:** Any authenticated user can post a comment. Only the
comment's author can edit or delete it. No admin override path exists.

**Problems:**
- `PhotoCommentCreate`, `PhotoCommentModify`, and `PhotoCommentDelete` are never
  checked.
- An admin who needs to moderate a comment cannot without a direct DB update.

**Proposed solution:** Same pattern as labels (Finding 4):

```
POST   → checker.Check(..., PermPhotoCommentCreate)
PATCH  → if caller == author → allow; else check PermAdmin → allow; else 403
DELETE → if caller == author → allow; else check PermAdmin → allow; else 403
```

Resolve `exhibitionid` from the photo before checking. For `Update` and
`Delete`, the `photoid` is already fetched from the comments row — use that to
resolve the exhibition.

---

## Finding 9 — `GET /api/v1/search`: legacy `authorized_non_public` flag used for photo visibility

**Handler:** `SearchHandler.ServeHTTP` / `buildSearchSQL`

**Status: RESOLVED** — `canSeeNonPublic` replaced with
`checker.Check(ctx, userID, exhibitionID, "", "", "", PermPrivatePhotoView)`.
The boolean is passed into `buildSearchSQL` under the name `canSeePrivate`;
the SQL filter `(p.is_public OR $2)` is structurally unchanged.

---

## Finding 10 — `GET /api/v1/admin/*`: legacy `authorized_non_public` flag used instead of `PermAdmin`

**Handler:** `AdminHandler.ListExhibitions`, `AdminHandler.ListPhotos`,
`AdminHandler.SetPublic`

**Status: RESOLVED** — All three admin endpoints now call
`checker.Check(ctx, userID, exhibitionID, "", "", "", PermAdmin)`.
The `AuthorizedNonPublic()` middleware function and `authorized_non_public`
DB column have been removed (migration 013).

---

## Finding 11 — `PATCH /api/v1/photo`: ownership-only check, no admin override

**Handler:** `PatchPhotoHandler.ServeHTTP`

**Current behavior:** Only the photo owner or the user who last set the title
can change the title. Anyone else receives a 403 unconditionally.

**Problem:** An admin who needs to correct a title on any photo cannot do so
through the API.

**Proposed solution:** After the ownership check fails, fall through to an admin
check before returning 403:

```
if caller == owner || caller == titleUserID → allow
else if checker.Check(..., PermAdmin) → allow
else → 403
```

---

## Summary

| Endpoint | Status | Remaining work |
|---|---|---|
| `GET /api/v1/photo` | ✅ `PermPrivatePhotoView` for non-public | Inline label/emoji/comment blocks not individually gated (by design; governed by List endpoints) |
| `GET /api/v1/labels` | ✅ `PhotoLabelView` | — |
| `POST /api/v1/labels` | ✅ `PhotoLabelCreate` scoped to photo | Restricted-label → `PermLabelAdmin` deferred to Phase 5b |
| `PATCH /api/v1/labels/:id` | ✅ `PermLabelAdmin` override | Restricted-label branch deferred to Phase 5b |
| `DELETE /api/v1/labels/:id` | ✅ `PermLabelAdmin` override | Restricted-label branch deferred to Phase 5b |
| `GET /api/v1/emojis` | ✅ `PhotoEmojiView` | — |
| `GET /api/v1/emoji/users` | ⬜ open | Add `PhotoEmojiView` check (Finding 7) |
| `POST /api/v1/emoji/react` | ✅ `PhotoEmojiCreate` scoped to photo | — |
| `DELETE /api/v1/emoji/react` | ✅ `PhotoEmojiDelete` scoped to photo | — |
| `POST /api/v1/emoji/types` | ⬜ open | Policy decision needed (Finding 6) |
| `GET /api/v1/comments` | ✅ `PhotoCommentView` | — |
| `POST /api/v1/comments` | ⬜ open | `PhotoCommentCreate` (Finding 8) |
| `PATCH /api/v1/comments/:id` | ⬜ open | `PhotoCommentModify`; `PermAdmin` override (Finding 8) |
| `DELETE /api/v1/comments/:id` | ⬜ open | `PhotoCommentDelete`; `PermAdmin` override (Finding 8) |
| `GET /api/v1/search` | ✅ `PermPrivatePhotoView` | — |
| `PATCH /api/v1/photo` | ⬜ open | `PermAdmin` override (Finding 11) |
| `GET /api/v1/admin/exhibitions` | ✅ `PermAdmin` | — |
| `GET /api/v1/admin/photos` | ✅ `PermAdmin` | — |
| `PATCH /api/v1/admin/photo` | ✅ `PermAdmin` | — |
