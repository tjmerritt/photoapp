.PHONY: build run tidy migrate-up migrate-down test lint

# ── Build & run ───────────────────────────────────────────────────────────────
build:
	go build -o bin/photoapp ./cmd/server
	go build -o bin/import-photos ./cmd/import-photos
	go build -o bin/import-emojis ./cmd/import-emojis

import-photos:
	go run ./cmd/import-photos $(ARGS)

import-emojis:
	go run ./cmd/import-emojis $(ARGS)

run:
	go run ./cmd/server

tidy:
	go mod tidy

# ── Database ──────────────────────────────────────────────────────────────────
# Requires psql on PATH and DATABASE_URL set in environment.
migrate-up:
	psql "$$DATABASE_URL" -f migrations/001_initial.sql
	psql "$$DATABASE_URL" -f migrations/003_view_count.sql
	psql "$$DATABASE_URL" -f migrations/004_emoji_unique.sql
	psql "$$DATABASE_URL" -f migrations/005_emoji_skintone.sql
	psql "$$DATABASE_URL" -f migrations/006_auth_providers.sql
	psql "$$DATABASE_URL" -f migrations/007_multi_login.sql
	psql "$$DATABASE_URL" -f migrations/008_exhibitions.sql
	psql "$$DATABASE_URL" -f migrations/009_public_flag.sql
	psql "$$DATABASE_URL" -f migrations/010_profile_image_source.sql

migrate-down:
	psql "$$DATABASE_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

seed:
	psql "$$DATABASE_URL" -f migrations/002_seed.sql

# ── Quality ───────────────────────────────────────────────────────────────────
test:
	go test ./...

lint:
	golangci-lint run ./...
