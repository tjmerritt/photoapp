.PHONY: build run tidy migrate-up migrate-down test lint

# ── Build & run ───────────────────────────────────────────────────────────────
build:
	go build -o bin/photoapp ./cmd/server

run:
	go run ./cmd/server

tidy:
	go mod tidy

# ── Database ──────────────────────────────────────────────────────────────────
# Requires psql on PATH and DATABASE_URL set in environment.
migrate-up:
	psql "$$DATABASE_URL" -f migrations/001_initial.sql

migrate-down:
	psql "$$DATABASE_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

seed:
	psql "$$DATABASE_URL" -f migrations/002_seed.sql

# ── Quality ───────────────────────────────────────────────────────────────────
test:
	go test ./...

lint:
	golangci-lint run ./...
