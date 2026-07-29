.PHONY: build run tidy migrate-up migrate-down test test-go test-js test-db-create test-db-drop hooks-install lint

# ── Build & run ───────────────────────────────────────────────────────────────
build:	tailwind.css
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

# ── Tailwind ──────────────────────────────────────────────────────────────────
tailwind.css:
	sh scripts/build-tailwind.sh

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
	psql "$$DATABASE_URL" -f migrations/011_facebook_auth.sql
	psql "$$DATABASE_URL" -f migrations/012_permissions.sql
	psql "$$DATABASE_URL" -f migrations/013_remove_authorized_non_public.sql
	psql "$$DATABASE_URL" -f migrations/014_galleries_displays.sql
	psql "$$DATABASE_URL" -f migrations/015_microsoft_auth.sql
	psql "$$DATABASE_URL" -f migrations/016_label_names.sql
	psql "$$DATABASE_URL" -f migrations/017_admin_phase6.sql
	psql "$$DATABASE_URL" -f migrations/018_grant_exhibitionid.sql

#Commented out so that the database isn't destroyed accidentally
#migrate-down:
#	psql "$$DATABASE_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

seed:
	psql "$$DATABASE_URL" -f migrations/002_seed.sql

# ── Quality ───────────────────────────────────────────────────────────────────
# Phase 0 (PLAN2.md) test infrastructure.
#
# Go: DB-backed tests (internal/permissions, internal/handlers, ...) skip
# themselves automatically when TEST_DATABASE_URL is unset — see
# internal/testutil's package doc comment. Point it at a disposable database
# (its tables get truncated before every test):
#
#   make test-db-create
#   TEST_DATABASE_URL=postgres://photoapp:photoapp@localhost:5432/photoapp_test?sslmode=disable make test-go
#
# -p 1 is required, not optional, whenever TEST_DATABASE_URL is set: `go
# test ./...` otherwise runs different packages' tests concurrently, and
# every package shares the one database named by TEST_DATABASE_URL —
# concurrent packages will truncate tables out from under each other's
# in-flight tests (see internal/testutil.TruncateAll's doc comment) and
# produce flaky, misleading failures that vanish under -v -run on a single
# package. -count=1 additionally bypasses Go's test result cache, which
# otherwise doesn't know TEST_DATABASE_URL affects the outcome and can
# silently serve a stale result from a run before the database was set up.
#
# JS: `npm test` (Vitest) covers the pure, DOM-free helpers exposed by
# app/app.js (see app/__tests__/).
test: test-go test-js

test-go:
	go test -p 1 -count=1 ./...

test-js:
	npm test

# Convenience targets for a local throwaway test database. Requires
# createdb/dropdb (part of the same Postgres client tools as psql) and
# DATABASE_URL set to a superuser/owner connection for the CREATE/DROP.
test-db-create:
	createdb photoapp_test

test-db-drop:
	dropdb --if-exists photoapp_test

# Run the test suite before every commit — see scripts/githooks/pre-commit.
hooks-install:
	git config core.hooksPath scripts/githooks

lint:
	golangci-lint run ./...
