package config

import "testing"

// clearEnv sets key to "" for the duration of the test (auto-restored by
// t.Setenv's cleanup). envStr/envInt/Load all treat an empty string the same
// as "unset" (they only ever check `!= ""`), so this is equivalent to
// os.Unsetenv but — unlike a raw Unsetenv call — safely restores whatever
// value the host environment had afterward, so tests can't leak env changes
// into each other or the rest of the test binary.
func clearEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
}

func TestEnvStr_ReturnsSetValue(t *testing.T) {
	t.Setenv("PHOTOAPP_TEST_ENVSTR", "hello")
	if got := envStr("PHOTOAPP_TEST_ENVSTR", "default"); got != "hello" {
		t.Errorf("envStr = %q, want %q", got, "hello")
	}
}

func TestEnvStr_FallsBackToDefaultWhenUnset(t *testing.T) {
	clearEnv(t, "PHOTOAPP_TEST_ENVSTR_UNSET")
	if got := envStr("PHOTOAPP_TEST_ENVSTR_UNSET", "default"); got != "default" {
		t.Errorf("envStr = %q, want %q", got, "default")
	}
}

func TestEnvInt_ParsesSetValue(t *testing.T) {
	t.Setenv("PHOTOAPP_TEST_ENVINT", "42")
	got, err := envInt("PHOTOAPP_TEST_ENVINT", 7)
	if err != nil {
		t.Fatalf("envInt: %v", err)
	}
	if got != 42 {
		t.Errorf("envInt = %d, want 42", got)
	}
}

func TestEnvInt_FallsBackToDefaultWhenUnset(t *testing.T) {
	clearEnv(t, "PHOTOAPP_TEST_ENVINT_UNSET")
	got, err := envInt("PHOTOAPP_TEST_ENVINT_UNSET", 7)
	if err != nil {
		t.Fatalf("envInt: %v", err)
	}
	if got != 7 {
		t.Errorf("envInt = %d, want 7", got)
	}
}

func TestEnvInt_ErrorsOnNonNumericValue(t *testing.T) {
	t.Setenv("PHOTOAPP_TEST_ENVINT_BAD", "not-a-number")
	if _, err := envInt("PHOTOAPP_TEST_ENVINT_BAD", 7); err == nil {
		t.Error("envInt did not error on a non-numeric value")
	}
}

func TestAddr_CombinesHostAndPort(t *testing.T) {
	c := &Config{Host: "0.0.0.0", Port: 8080}
	if got := c.Addr(); got != "0.0.0.0:8080" {
		t.Errorf("Addr() = %q, want %q", got, "0.0.0.0:8080")
	}
}

func TestLoad_DefaultsAndOverrides(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DATABASE_URL", "postgres://u:p@host/db")
	clearEnv(t, "HOST")
	clearEnv(t, "APP_DIR")
	clearEnv(t, "DEFAULT_PAGE_SIZE")
	clearEnv(t, "MAX_PAGE_SIZE")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090 (from PORT env var)", cfg.Port)
	}
	if cfg.DBURL != "postgres://u:p@host/db" {
		t.Errorf("DBURL = %q, want the DATABASE_URL value", cfg.DBURL)
	}
	if cfg.AppDir != "app" {
		t.Errorf("AppDir = %q, want default %q", cfg.AppDir, "app")
	}
	if cfg.DefaultPageSize != 10 || cfg.MaxPageSize != 100 {
		t.Errorf("pagination defaults = (%d, %d), want (10, 100)", cfg.DefaultPageSize, cfg.MaxPageSize)
	}
}

func TestLoad_BuildsDSNFromPartsWhenDatabaseURLUnset(t *testing.T) {
	clearEnv(t, "DATABASE_URL")
	t.Setenv("DB_HOST", "dbhost")
	t.Setenv("DB_PORT", "5555")
	t.Setenv("DB_USER", "u")
	t.Setenv("DB_PASSWORD", "p")
	t.Setenv("DB_NAME", "n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := "postgres://u:p@dbhost:5555/n?sslmode=disable"
	if cfg.DBURL != want {
		t.Errorf("DBURL = %q, want %q", cfg.DBURL, want)
	}
}

func TestLoad_InvalidPortReturnsError(t *testing.T) {
	t.Setenv("PORT", "not-a-number")
	if _, err := Load(); err == nil {
		t.Error("Load did not error on a non-numeric PORT")
	}
}
