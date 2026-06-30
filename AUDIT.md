# Permission Audit

This document records findings from a review of all API handlers against the
permissions model defined in `internal/permissions/permissions.go` and
`PERMISSIONS.md`. No changes have been made yet; this is a pre-implementation
record of gaps and proposed remedies.

---

## Prerequisites

Several findings share a common prerequisite. Handlers that act on a photo need
the photo's `exhibitionid` to perform meaningful permission checks (grants are
scoped to exhibitions). Currently no handler fetches this. A shared helper
should be introduced before wiring permissions into the photo-related handlers:

```go
// resolvePhotoExhibition returns the exhibitionid for the given photo,
// or "" and pgx.ErrNoRows if the photo does not exist.
func resolvePhotoExhibition(ctx context.Context, db *db.Pool, photoid string) (string, error)
```

Additionally, the `PermissionsHandler` and `Checker` are already wired into the
router, but no existing handler holds a reference to `*permissions.Checker`. Each
handler struct that needs permission checks must gain a `Checker *permissions.Checker`
field, or a shared helper must be passed through the router.

---

## Finding 1 — `GET /api/v1/photo`: legacy `authorized_non_public` flag used for photo and annotation visibility

**Handler:** `PhotoHandler.ServeHTTP`

**Status: RESOLVED** — `canSeeNonPublic` replaced with
`checker.Check(ctx, userID, exhibitionID, "", "", "", PermPrivatePhotoView)`.
The `authorized_non_public` column is dropped in migration 013.

To grant a user access to private photos, add them to a team whose role
includes `PrivatePhotoView`. The seed script grants this permission to the
Admin role by default.

Remaining gap: Labels, emojis, and comments are returned unconditionally for
any photo the caller can see — there is no check against `PhotoLabelView`,
`PhotoEmojiView`, or `PhotoCommentView` (see Finding 2 and Finding 8).

---

## Finding 2 — `GET /api/v1/labels`, `GET /api/v1/emojis`, `GET /api/v1/comments`: no permission check

**Handlers:** `LabelsHandler.List`, `EmojisHandler.List`, `CommentsHandler.List`

**Current behavior:** These list endpoints are fully open — any caller,
including unauthenticated requests, can fetch labels, emojis, and comments for
any photo by ID.

**Problem:** `PhotoLabelView`, `PhotoEmojiView`, and `PhotoCommentView` are
never enforced. The `Viewer` role is granted to `Public` globally, so under the
intended model these endpoints would pass — but it is entirely unenforced. A
future configuration change (e.g. restricting Viewer to `LoggedIn` only) would
have no effect.

**Proposed solution:** At the top of each handler, call the appropriate View
permission check:

```
GET /api/v1/labels   → checker.Check(..., "", "", PermPhotoLabelView)
GET /api/v1/emojis   → checker.Check(..., "", "", PermPhotoEmojiView)
GET /api/v1/comments → checker.Check(..., "", "", PermPhotoCommentView)
```

Return 403 if the check fails. Since `Public` holds `Viewer` by default, this
is a no-op for current behavior.

---

## Finding 3 — `POST /api/v1/labels`: authenticated but not permission-checked

**Handler:** `LabelsHandler.Create`

**Current behavior:** Any authenticated user can add a label to any photo. The
only check is that the user is logged in (`MustUserID`). The photo's exhibition
is not resolved, so exhibition-scoped grants cannot be evaluated.

**Problems:**
- `PhotoLabelCreate` is never checked.
- Restricted labels (Phase 5b) have no enforcement hook yet.
- Exhibition context is not available to evaluate scoped grants.

**Proposed solution:**
1. Resolve the photo's `exhibitionid` (see Prerequisites).
2. Call `checker.Check(ctx, userID, exhibitionID, "", ResourcePhoto, photoid, PermPhotoLabelCreate)`. Return 403 if false.
3. After the permission check passes, query whether the label name has
   `restricted = TRUE` in the `label_names` table. If so, additionally require
   `PermLabelAdmin`. Return 403 if that check also fails.

---

## Finding 4 — `PATCH /api/v1/labels/:labelid` and `DELETE /api/v1/labels/:labelid`: ownership-only check, no permission check

**Handlers:** `LabelsHandler.Update`, `LabelsHandler.Delete`

**Current behavior:** Only the label's creator can modify or delete it. Any
caller who is not the original creator receives a 403. There is no admin
override path.

**Problems:**
- `PhotoLabelModify` and `PhotoLabelDelete` are never checked.
- An admin who needs to moderate a label cannot — the handler unconditionally
  rejects anyone who is not the creator.
- Restricted-label enforcement is absent.

**Proposed solution:** After the ownership check fails (caller is not the
label's creator), fall through to a `PermLabelAdmin` check before returning 403.
If the label's name is restricted, require `PermLabelAdmin` even for the owner.
The full logic:

```
if caller == owner → allow (unless restricted, then also require PermLabelAdmin)
else if checker.Check(..., PermLabelAdmin) → allow
else → 403
```

---

## Finding 5 — `POST /api/v1/emoji/react` and `DELETE /api/v1/emoji/react`: authenticated but not permission-checked

**Handlers:** `EmojisHandler.React`, `EmojisHandler.Unreact`

**Current behavior:** Any authenticated user can add or remove a reaction on any
photo.

**Problem:** `PhotoEmojiCreate` and `PhotoEmojiDelete` are never checked.

**Proposed solution:** Resolve the photo's `exhibitionid`, then:

```
POST   /api/v1/emoji/react  → checker.Check(..., PermPhotoEmojiCreate)
DELETE /api/v1/emoji/react  → checker.Check(..., PermPhotoEmojiDelete)
```

Return 403 if the check fails. The existing `WHERE userid=$3` clause in
`Unreact` already ensures a user can only remove their own reaction; the
permission check gates entry to the operation.

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

| Endpoint | Current gate | Missing check | Notes |
|---|---|---|---|
| `GET /api/v1/photo` | `is_public` flag | ~~`PermPrivatePhotoView` for non-public~~ ✓ done; `PhotoLabel/Emoji/CommentView` per block still pending | |
| `GET /api/v1/labels` | none | `PhotoLabelView` | |
| `POST /api/v1/labels` | auth only | `PhotoLabelCreate`; restricted-label → `PermLabelAdmin` | Needs exhibition resolution |
| `PATCH /api/v1/labels/:id` | auth + ownership | `PhotoLabelModify`; `PermLabelAdmin` override | |
| `DELETE /api/v1/labels/:id` | auth + ownership | `PhotoLabelDelete`; `PermLabelAdmin` override | |
| `GET /api/v1/emojis` | none | `PhotoEmojiView` | |
| `GET /api/v1/emoji/users` | none | `PhotoEmojiView` | |
| `POST /api/v1/emoji/react` | auth only | `PhotoEmojiCreate` | Needs exhibition resolution |
| `DELETE /api/v1/emoji/react` | auth only | `PhotoEmojiDelete` | Needs exhibition resolution |
| `POST /api/v1/emoji/types` | auth only | `PermEmojiAdmin` (or policy decision on suggestion workflow) | Currently inserts `is_active=TRUE` |
| `GET /api/v1/comments` | none | `PhotoCommentView` | |
| `POST /api/v1/comments` | auth only | `PhotoCommentCreate` | Needs exhibition resolution |
| `PATCH /api/v1/comments/:id` | auth + ownership | `PhotoCommentModify`; `PermAdmin` override | |
| `DELETE /api/v1/comments/:id` | auth + ownership | `PhotoCommentDelete`; `PermAdmin` override | |
| `GET /api/v1/search` | `is_public` flag | ~~`PermPrivatePhotoView`~~ ✓ done | |
| `PATCH /api/v1/photo` | auth + ownership | `PermAdmin` override | |
| `GET /api/v1/admin/exhibitions` | `authorized_non_public` flag | ~~`PermAdmin`~~ ✓ done | |
| `GET /api/v1/admin/photos` | `authorized_non_public` flag | ~~`PermAdmin`~~ ✓ done | |
| `PATCH /api/v1/admin/photo` | `authorized_non_public` flag | ~~`PermAdmin`~~ ✓ done | |
