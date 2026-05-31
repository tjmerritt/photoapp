package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all application configuration sourced from environment variables.
type Config struct {
	// Server
	Port int
	Host string

	// Database
	DBURL string // full DSN, e.g. postgres://user:pass@host:5432/dbname

	// Static app directory served for all non-/api routes
	AppDir string

	// File storage for uploaded emoji images
	UploadDir     string // local filesystem path
	UploadURLBase string // public URL prefix served for uploaded files

	// Pagination defaults
	DefaultPageSize int
	MaxPageSize     int

	// Auth: X-User-ID header used for now (no real login yet).
	// When a proper auth system is added, replace header-based identity
	// with JWT / session middleware.
	AuthHeader string
}

func Load() (*Config, error) {
	port, err := envInt("PORT", 8080)
	if err != nil {
		return nil, err
	}
	defaultPage, err := envInt("DEFAULT_PAGE_SIZE", 10)
	if err != nil {
		return nil, err
	}
	maxPage, err := envInt("MAX_PAGE_SIZE", 100)
	if err != nil {
		return nil, err
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// Build from parts if DATABASE_URL not set
		host := envStr("DB_HOST", "localhost")
		dbport := envStr("DB_PORT", "5432")
		user := envStr("DB_USER", "photoapp")
		pass := envStr("DB_PASSWORD", "photoapp")
		name := envStr("DB_NAME", "photoapp")
		dbURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, dbport, name)
	}

	return &Config{
		Port:            port,
		Host:            envStr("HOST", ""),
		DBURL:           dbURL,
		AppDir:          envStr("APP_DIR", "app"),
		UploadDir:       envStr("UPLOAD_DIR", "./uploads"),
		UploadURLBase:   envStr("UPLOAD_URL_BASE", "/uploads"),
		DefaultPageSize: defaultPage,
		MaxPageSize:     maxPage,
		AuthHeader:      envStr("AUTH_HEADER", "X-User-ID"),
	}, nil
}

func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("env %s: %w", key, err)
	}
	return n, nil
}
