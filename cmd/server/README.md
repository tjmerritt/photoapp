# PhotoApp — Go API Server

REST API server for the PhotoApp photo viewer, written in Go, backed by PostgreSQL.

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

## Project Layout

```
photoapp/
├── cmd/server/main.go              # Entrypoint — server setup & graceful shutdown
├── internal/
│   ├── config/config.go            # Environment-based configuration
│   ├── db/db.go                    # pgxpool wrapper + helpers
│   ├── middleware/middleware.go     # RequestID, Logger, CORS, Auth
│   ├── models/models.go            # Request/response structs
│   └── handlers/
│       ├── router.go               # Route wiring
│       ├── pagination.go           # parsePage / buildPages helpers
│       ├── fetch.go                # Shared DB fetch functions
│       ├── photo.go                # GET /api/v1/photo, GET /api/v1/user
│       ├── labels.go               # Labels CRUD
│       ├── emojis.go               # Emoji reactions + type upload
│       └── comments.go             # Comments CRUD
├── migrations/
│   ├── 001_initial.sql             # Full schema (idempotent)
│   └── 002_seed.sql                # Development seed data
├── .env.example                    # Environment variable template
├── Makefile
└── go.mod
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

Authentication is currently a placeholder using the `X-User-ID` header.
Pass the UUID of the acting user:

```bash
curl -X POST http://localhost:8080/api/v1/labels?photoid=aaaaaaaa-0000-0000-0000-000000000001 \
  -H "X-User-ID: 11111111-0000-0000-0000-000000000001" \
  -H "Content-Type: application/json" \
  -d '{"name":"Shutter","value":"1/250s"}'
```

When real login is added, replace the `Auth` middleware in
`internal/middleware/middleware.go` with JWT / session validation and remove
the `X-User-ID` override.

## Emoji Upload

Custom emoji images are uploaded as `multipart/form-data`:

```bash
curl -X POST http://localhost:8080/api/v1/emoji/types \
  -H "X-User-ID: 11111111-0000-0000-0000-000000000001" \
  -F "image=@/path/to/emoji.png" \
  -F "alttext=My Custom Emoji"
```

Files are stored in `UPLOAD_DIR` (default `./uploads`) and served at
`UPLOAD_URL_BASE` (default `/uploads`).

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
3. **No photo creation API** — photos are curated and inserted directly into the database by administrators.
4. **No search endpoint** — `/api/v1/search` is not yet implemented.
5. **No image resizing** — uploaded emoji images are stored as-is. Add a resizing step (e.g. using `vips` or `imaging`) for production.
6. **No rate limiting** — add per-IP or per-user rate limiting before public deployment.
