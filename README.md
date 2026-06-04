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

# 5. Import the OpenMoji emoji set
go run ./cmd/import-emojis

# 6. Fetch dependencies
make tidy

# 7. Run the server
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
│   ├── import-photos/main.go       # CLI tool for bulk photo import
│   └── import-emojis/main.go       # CLI tool for importing OpenMoji emoji data
├── internal/
│   ├── config/config.go            # Environment-based configuration
│   ├── db/db.go                    # pgxpool wrapper + helpers
│   ├── middleware/middleware.go     # RequestID, Logger, CORS, Auth
│   ├── models/models.go            # Request/response structs
│   └── handlers/
│       ├── router.go               # Route wiring + static file serving
│       ├── pagination.go           # parsePage / buildPages helpers
│       ├── fetch.go                # Shared DB fetch functions
│       ├── photo.go                # GET/PATCH /api/v1/photo, GET /api/v1/user
│       ├── labels.go               # Labels CRUD + name/value suggestion endpoints
│       ├── emojis.go               # Emoji reactions, types, upload
│       └── comments.go             # Comments CRUD
├── migrations/
│   ├── 001_initial.sql             # Full schema (idempotent)
│   ├── 002_seed.sql                # Development seed data
│   ├── 003_view_count.sql          # Adds view_count to photos
│   └── 004_emoji_unique.sql        # Unique index on emoji_char + group/tags columns
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

Open `http://localhost:8080/` to view the photo viewer. With no `photoid` in the URL a random photo is loaded. A specific photo can be linked directly:

```
http://localhost:8080/?photoid=aaaaaaaa-0000-0000-0000-000000000001
```

Clicking a label value adds `?label=<labelid>` to the URL and populates the Related sidebar with up to 8 photos that share the same label name and value (top 7 by view count + 1 random). Clicking the same label again clears the filter.

## API Reference

### Read (no auth required)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/photo?photoid=&label=` | Full photo; `label` filters related photos by label |
| GET | `/api/v1/photo?random=true` | Load a random photo |
| GET | `/api/v1/user?userid=` | User profile |
| GET | `/api/v1/labels?photoid=&offset=&limit=` | Paginated labels |
| GET | `/api/v1/label-names` | Distinct label names across all photos |
| GET | `/api/v1/label-values?name=` | Distinct values for a given label name |
| GET | `/api/v1/emojis?photoid=&offset=&limit=` | Paginated emoji reactions with counts |
| GET | `/api/v1/emoji/users?emoji=&photoid=&offset=&limit=` | Users who used an emoji |
| GET | `/api/v1/emoji/types?search=&group=&offset=&limit=` | Paginated, searchable emoji type catalogue |
| GET | `/api/v1/comments?photoid=&parentid=&offset=&limit=` | Comments or replies |

### Write (auth required — pass `X-User-ID: <uuid>` header)

| Method | Path | Description |
|--------|------|-------------|
| PATCH | `/api/v1/photo?photoid=` | Update photo title `{title}` |
| POST | `/api/v1/labels?photoid=` | Add a label `{name, value}` |
| PATCH | `/api/v1/labels/:labelid` | Edit your own label `{value}` |
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

The frontend includes a test-user dropdown in the navbar for switching between the five seeded users while real login is not yet implemented. When real login is added, replace the `Auth` middleware in `internal/middleware/middleware.go` with JWT / session validation.

## Emoji Setup

Emojis are stored in the `emoji_types` table and served from `GET /api/v1/emoji/types`. The full OpenMoji set (~3700 standard emoji, skintone variants excluded) is imported with the `import-emojis` command.

### Initial import

```sh
go run ./cmd/import-emojis
```

### Incremental updates

Re-running the command is safe — unchanged rows are skipped, modified annotations are updated, and new emoji are inserted:

```sh
# Update from latest OpenMoji master
go run ./cmd/import-emojis

# Also mark any emoji removed from the feed as inactive
go run ./cmd/import-emojis --deactivate-removed

# Preview without writing
go run ./cmd/import-emojis --dry-run

# Use a local or pinned JSON file
go run ./cmd/import-emojis --url file:///path/to/openmoji.json
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--db` | `$DATABASE_URL` | PostgreSQL DSN |
| `--url` | GitHub master `openmoji.json` | OpenMoji JSON feed URL |
| `--deactivate-removed` | `false` | Set `is_active=false` for emoji absent from feed |
| `--dry-run` | `false` | Print what would change without writing to DB |

### Custom emoji images

Custom emoji images can be uploaded via the API:

```bash
curl -X POST http://localhost:8080/api/v1/emoji/types \
  -H "X-User-ID: 11111111-0000-0000-0000-000000000001" \
  -F "image=@/path/to/emoji.png" \
  -F "alttext=My Custom Emoji"
```

Files are stored in `UPLOAD_DIR` (default `./uploads`) and served at `UPLOAD_URL_BASE` (default `/uploads`).

## Importing Photos

The `import-photos` command bulk-inserts photos from a list of URLs. Each image is downloaded once: dimensions are decoded from the image header and EXIF metadata is extracted and stored as labels. The title is derived from the URL basename by default.

### Input format

The input file is a CSV (or plain text). Each row can have one or two fields:

```
# Plain URL — always inserts a new photo
https://example.com/photo.jpg

# URL with photoid — re-uses an existing photo if the URL matches
https://example.com/photo.jpg,aaaaaaaa-0000-0000-0000-000000000001
```

Lines starting with `#` are treated as comments. The header row `url,photoid,action` (produced by `--output`) is skipped automatically.

### Basic usage

```bash
# From a file
go run ./cmd/import-photos --owner <user-uuid> urls.txt

# From stdin
cat urls.txt | go run ./cmd/import-photos --owner <user-uuid>

# Dry run — prints what would be inserted/updated without touching the DB
go run ./cmd/import-photos --owner <user-uuid> --dry-run urls.txt

# Via make
ARGS="--owner <user-uuid> urls.txt" make import-photos
```

### Adding labels

EXIF metadata is automatically extracted as labels (camera make/model, lens, shutter speed, aperture, ISO, focal length, GPS, date taken, and more). Additional labels can be added or overridden with `--label`:

```bash
go run ./cmd/import-photos \
  --owner <user-uuid> \
  --label "Season=Summer" \
  --label "Location=Mt. Rainier, WA" \
  urls.txt
```

`--label` values override any EXIF label with the same name. Supported image formats for EXIF and dimension detection: JPEG, PNG, GIF.

### Output file and idempotent re-runs

Use `--output` to write a `url,photoid,action` CSV after each run. Feed that file back in on subsequent runs to update existing photos without duplicating them:

```bash
# First run — inserts photos and records their IDs
go run ./cmd/import-photos --owner <user-uuid> --output results.csv urls.txt

# Second run — skips photos whose URL already matches; only inserts new ones
go run ./cmd/import-photos --owner <user-uuid> --output results.csv results.csv

# Force label refresh — re-downloads every image and replaces all labels
go run ./cmd/import-photos --owner <user-uuid> --refresh-exif --output results.csv results.csv
```

When a photo is unchanged (URL matches, `--refresh-exif` not set), any `--label` flags are still applied to that photo without re-downloading the image.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--db` | `$DATABASE_URL` | PostgreSQL DSN |
| `--owner` | *(required)* | UUID of the owning user |
| `--title` | | Fixed title for every photo (overrides `--title-from-url`) |
| `--title-from-url` | `true` | Derive title from URL path basename |
| `--label` | | Extra label as `Name=Value`; may be repeated |
| `--output` | | Write `url,photoid,action` results to this CSV file |
| `--refresh-exif` | `false` | Re-download images and replace labels even for existing photos |
| `--dry-run` | `false` | Print what would happen without writing to the DB |

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
- [`rwcarlsen/goexif`](https://github.com/rwcarlsen/goexif) — EXIF metadata extraction (used by `import-photos`)

No ORM is used. All queries are plain SQL.

## Known Limitations / Future Work

1. **No real auth** — replace `X-User-ID` header with JWT or session tokens when login is implemented.
2. **Emoji counts materialised view** is refreshed synchronously after each reaction. Under high write load, switch to a background refresh job or use a plain `COUNT(*)` with a covering index.
3. **No photo creation API** — photos are inserted via the `import-photos` tool or directly into the database by administrators.
4. **No search endpoint** — `/api/v1/search` is not yet implemented. The search box in the UI shows a placeholder toast.
5. **No image resizing** — uploaded emoji images are stored as-is. Add a resizing step (e.g. using `vips` or `imaging`) for production.
6. **No rate limiting** — add per-IP or per-user rate limiting before public deployment.
7. **Pagination UI** — labels and comments use server-side pagination but the frontend does not yet show "Load more" controls for these sections.
8. **Comment author thumbnails** — the comment author object includes `tn` (thumbnail). If absent, the UI falls back to a Pravatar placeholder derived from the user ID.
9. **Image format support in import-photos** — dimension detection and EXIF extraction support JPEG, PNG, and GIF only; other formats are skipped with a warning.
10. **Skintone emoji variants** — the `import-emojis` tool skips skintone variants by default. To include them, remove the skintone filter in `cmd/import-emojis/main.go`.
