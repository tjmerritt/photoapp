# PhotoApp — Go API Server

REST API server for the PhotoApp photo viewer, written in Go, backed by PostgreSQL. The frontend is a single-page app built with plain HTML, Alpine.js, and Tailwind CSS.

## Prerequisites

- Go 1.22+
- PostgreSQL 13+
- `psql` on your PATH (for migrations)

## Quick Start

```bash
# 1. Create the database
createdb photoapp

# 2. Configure environment
cp .env.example .env
# edit .env with your DB credentials if needed

# 3. Source the env and run migrations
source .env   # or: export $(cat .env | xargs)
make migrate-up

# 4. Seed with development data (optional)
make seed

# 5. Fetch dependencies
make tidy

# 6. Run the server
make run
# → listening on http://localhost:8080
```

## Installing PostgreSQL on FreeBSD

```sh
pkg install postgresql16-server postgresql16-client

sysrc postgresql_enable="YES"
service postgresql initdb
service postgresql start
```

Create the database user and database:

```sh
su -l postgres
psql
```

```sql
CREATE USER photoapp WITH PASSWORD 'photoapp';
CREATE DATABASE photoapp OWNER photoapp;
\q
```

FreeBSD defaults to `peer` auth for local connections. Change it to `md5` in `/var/db/postgres/data16/pg_hba.conf`:

```
# change this line:
local   all   all   peer
# to:
local   all   all   md5
```

Then reload: `service postgresql reload`

## Project Layout

```
photoapp/
├── cmd/
│   ├── server/main.go              # Entrypoint — server setup & graceful shutdown
│   └── import-photos/main.go      # CLI tool for bulk photo import
├── internal/
│   ├── config/config.go            # Environment-based configuration
│   ├── db/db.go                    # pgxpool wrapper + helpers
│   ├── middleware/middleware.go     # RequestID, Logger, CORS, Auth
│   ├── models/models.go            # Request/response structs
│   └── handlers/
│       ├── router.go               # Route wiring + static file serving
│       ├── pagination.go           # parsePage / buildPages helpers
│       ├── fetch.go                # Shared DB fetch functions
│       ├── photo.go                # GET /api/v1/photo, GET /api/v1/user
│       ├── labels.go               # Labels CRUD
│       ├── emojis.go               # Emoji reactions + type upload
│       └── comments.go             # Comments CRUD
├── migrations/
│   ├── 001_initial.sql             # Full schema (idempotent)
│   └── 002_seed.sql                # Development seed data
├── app/
│   └── index.html                  # Frontend SPA (Alpine.js + Tailwind)
├── .env.example                    # Environment variable template
├── Makefile
└── go.mod
```

## Frontend

Static files are served from the `app/` directory (configurable) for all routes that do not begin with `/api`. The directory is resolved in this priority order:

1. `--app-dir` command-line flag
2. `APP_DIR` environment variable
3. Default: `app`

```sh
# Use a custom frontend directory
go run ./cmd/server --app-dir ./my-frontend

# Or via environment
APP_DIR=./my-frontend make run
```

Open `http://localhost:8080/` to view the photo viewer. A specific photo can be loaded via the `photoid` query parameter:

```
http://localhost:8080/?photoid=aaaaaaaa-0000-0000-0000-000000000001
```

## API Reference

### Read (no auth required)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/photo?photoid=` | Full photo with labels, emojis, comments |
| GET | `/api/v1/user?userid=` | User profile |
| GET | `/api/v1/labels?photoid=&offset=&limit=` | Paginated labels |
| GET | `/api/v1/emojis?photoid=&offset=&limit=` | Paginated emojis with counts |
| GET | `/api/v1/emoji/users?emoji=&photoid=&offset=&limit=` | Users who used an emoji |
| GET | `/api/v1/emoji/types` | All active emoji types (picker palette) |
| GET | `/api/v1/comments?photoid=&parentid=&offset=&limit=` | Comments or replies |

### Write (auth required — pass `X-User-ID: <uuid>` header)

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/labels?photoid=` | Add a label `{name, value}` |
| PATCH | `/api/v1/labels/:labelid` | Edit your own label `{name?, value?}` |
| DELETE | `/api/v1/labels/:labelid` | Delete your own label |
| POST | `/api/v1/emoji/react?photoid=&emojiid=` | Add your emoji reaction |
| DELETE | `/api/v1/emoji/react?photoid=&emojiid=` | Remove your emoji reaction |
| POST | `/api/v1/emoji/types` | Upload a new emoji image (multipart: `image`, `alttext`) |
| POST | `/api/v1/comments?photoid=&parentid=` | Post a comment or reply `{comment}` |
| PATCH | `/api/v1/comments/:commentid` | Edit your own comment `{comment}` |
| DELETE | `/api/v1/comments/:commentid` | Soft-delete your own comment |

### Other

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Health check |
| GET | `/uploads/*` | Serve uploaded emoji images |

## Authentication

Authentication is currently a placeholder using the `X-User-ID` header. Pass the UUID of the acting user:

```bash
curl -X POST http://localhost:8080/api/v1/labels?photoid=aaaaaaaa-0000-0000-0000-000000000001 \
  -H "X-User-ID: 11111111-0000-0000-0000-000000000001" \
  -H "Content-Type: application/json" \
  -d '{"name":"Shutter","value":"1/250s"}'
```

When real login is added, replace the `Auth` middleware in `internal/middleware/middleware.go` with JWT / session validation and remove the `X-User-ID` override.

## Emoji Upload

Custom emoji images are uploaded as `multipart/form-data`:

```bash
curl -X POST http://localhost:8080/api/v1/emoji/types \
  -H "X-User-ID: 11111111-0000-0000-0000-000000000001" \
  -F "image=@/path/to/emoji.png" \
  -F "alttext=My Custom Emoji"
```

Files are stored in `UPLOAD_DIR` (default `./uploads`) and served at `UPLOAD_URL_BASE` (default `/uploads`).

## Importing Photos

The `import-photos` command inserts photos from a list of URLs. It fetches each image to read its dimensions from the header and derives a title from the URL basename.

```bash
# From a file
go run ./cmd/import-photos --owner <user-uuid> urls.txt

# From stdin
cat urls.txt | go run ./cmd/import-photos --owner <user-uuid>

# Dry run (no DB writes)
go run ./cmd/import-photos --owner <user-uuid> --dry-run urls.txt

# Via make
ARGS="--owner <user-uuid> urls.txt" make import-photos
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--db` | `$DATABASE_URL` | PostgreSQL DSN |
| `--owner` | *(required)* | UUID of the owning user |
| `--title` | | Fixed title for every photo |
| `--title-from-url` | `true` | Derive title from URL basename |
| `--dry-run` | `false` | Print rows without writing to DB |

Lines starting with `#` and blank lines in the URL file are ignored. Failed URLs are logged and skipped without aborting the run. Supported image formats for dimension detection: JPEG, PNG, GIF.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Listen port |
| `HOST` | `` | Listen host (empty = all interfaces) |
| `DATABASE_URL` | — | Full PostgreSQL DSN |
| `DB_HOST` | `localhost` | Used if `DATABASE_URL` not set |
| `DB_PORT` | `5432` | Used if `DATABASE_URL` not set |
| `DB_USER` | `photoapp` | Used if `DATABASE_URL` not set |
| `DB_PASSWORD` | `photoapp` | Used if `DATABASE_URL` not set |
| `DB_NAME` | `photoapp` | Used if `DATABASE_URL` not set |
| `APP_DIR` | `app` | Directory served for non-/api routes |
| `UPLOAD_DIR` | `./uploads` | Directory for uploaded emoji images |
| `UPLOAD_URL_BASE` | `/uploads` | Public URL prefix for uploaded images |
| `DEFAULT_PAGE_SIZE` | `10` | Default items per page |
| `MAX_PAGE_SIZE` | `100` | Maximum items per page |
| `AUTH_HEADER` | `X-User-ID` | Header name used for placeholder auth |

## Dependencies

- [`jackc/pgx/v5`](https://github.com/jackc/pgx) — PostgreSQL driver with connection pooling
- [`julienschmidt/httprouter`](https://github.com/julienschmidt/httprouter) — Fast HTTP router
- [`google/uuid`](https://github.com/google/uuid) — UUID generation

No ORM is used. All queries are plain SQL.

## Known Limitations / Future Work

1. **No real auth** — replace `X-User-ID` header with JWT or session tokens when login is implemented.
2. **Emoji counts materialised view** is refreshed synchronously after each reaction. Under high write load, switch to a background refresh job or use a plain `COUNT(*)` with a covering index.
3. **No photo creation API** — photos are inserted via the `import-photos` tool or directly into the database by administrators.
4. **No search endpoint** — `/api/v1/search` is not yet implemented. The search box in the UI shows a placeholder toast.
5. **No image resizing** — uploaded emoji images are stored as-is. Add a resizing step (e.g. using `vips` or `imaging`) for production.
6. **No rate limiting** — add per-IP or per-user rate limiting before public deployment.
7. **Pagination UI** — the API returns `pages` objects with `next`/`prev` URLs, but there is no "Load more" button or infinite scroll in the frontend yet.
8. **`canedit` flag** — the title object exposes `canedit` but no editing UI is implemented. An edit control should appear when `canedit: true`.
9. **Comment author thumbnails** — the comment author object includes `tn` (thumbnail). If absent, the UI falls back to a Pravatar placeholder derived from the user ID.
10. **Image format support in import-photos** — dimension detection supports JPEG, PNG, and GIF only; other formats will be skipped with a warning.
