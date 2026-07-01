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

**Status: RESOLVED** — A dedicated `PermEmojiUpload` permission now gates this
endpoint. The handler resolves `userID` and `exhibitionID`, then checks:

```go
if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermEmojiUpload); err != nil || !ok {
    middleware.WriteError(w, http.StatusForbidden, "forbidden")
    return
}
```

`EmojiUpload` is granted only to the Admin role in the seed script. Operators
can grant it to any team or user independently of `EmojiAdmin` — for example,
a designated "emoji curator" team could hold `EmojiUpload` without full admin
access.

---

## Finding 7 — `GET /api/v1/emoji/users`: no permission check

**Handler:** `EmojisHandler.ListUsers`

**Status: RESOLVED** — Same pattern as Finding 2. `ctx`, `userID`, and
`exhibitionID` are set at the top; `PhotoEmojiView` is checked before any DB
query:

```go
if ok, err := h.Checker.Check(ctx, userID, exhibitionID, "", "", "", permissions.PermPhotoEmojiView); err != nil || !ok {
    middleware.WriteError(w, http.StatusForbidden, "forbidden")
    return
}
```

---

## Finding 8 — `POST /api/v1/comments`, `PATCH /api/v1/comments/:commentid`, `DELETE /api/v1/comments/:commentid`: authenticated but not permission-checked

**Handlers:** `CommentsHandler.Create`, `CommentsHandler.Update`,
`CommentsHandler.Delete`

**Status: RESOLVED** — Same pattern as labels (Finding 4):

```
POST   → resolvePhotoExhibition (404 if missing) → PhotoCommentCreate scoped to photo
PATCH  → if caller == author → allow; else resolvePhotoExhibition → PermAdmin → allow; else 403
DELETE → if caller == author → allow; else resolvePhotoExhibition → PermAdmin → allow; else 403
```

`Create` replaces the bare `SELECT TRUE FROM photos` exists-check with
`resolvePhotoExhibition`, which also provides the exhibition for the
`PhotoCommentCreate` check. For `Update`, `photoid` was already fetched in the
existing row query — no query change needed. For `Delete`, `photoid::text` was
added to the SELECT to enable exhibition resolution for the admin override.

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

## Finding 11 — `PATCH /api/v1/photo`: ownership-only check, no permission override

**Handler:** `PatchPhotoHandler.ServeHTTP`

**Status: RESOLVED** — A dedicated `PermPhotoDescriptionModify` permission now
covers non-owner edits. After the ownership check fails the handler falls
through to a permission check before returning 403:

```
if caller == owner || caller == titleUserID → allow
else if checker.Check(..., PermPhotoDescriptionModify) → allow
else → 403
```

The exhibition is resolved via `resolvePhotoExhibition` (cached). The
permission is granted to the Admin role in the seed script. Operators can
grant it to any team or user independently.

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
| `GET /api/v1/emoji/users` | ✅ `PhotoEmojiView` | — |
| `POST /api/v1/emoji/react` | ✅ `PhotoEmojiCreate` scoped to photo | — |
| `DELETE /api/v1/emoji/react` | ✅ `PhotoEmojiDelete` scoped to photo | — |
| `POST /api/v1/emoji/types` | ✅ `EmojiUpload` | — |
| `GET /api/v1/comments` | ✅ `PhotoCommentView` | — |
| `POST /api/v1/comments` | ✅ `PhotoCommentCreate` scoped to photo | — |
| `PATCH /api/v1/comments/:id` | ✅ `PermAdmin` override | — |
| `DELETE /api/v1/comments/:id` | ✅ `PermAdmin` override | — |
| `GET /api/v1/search` | ✅ `PermPrivatePhotoView` | — |
| `PATCH /api/v1/photo` | ✅ `PhotoDescriptionModify` override | — |
| `GET /api/v1/admin/exhibitions` | ✅ `PermAdmin` | — |
| `GET /api/v1/admin/photos` | ✅ `PermAdmin` | — |
| `PATCH /api/v1/admin/photo` | ✅ `PermAdmin` | — |
