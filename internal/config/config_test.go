package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 8080 {
		t.Fatalf("port = %d", cfg.Server.Port)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(`server = { host = "127.0.0.1", port = 9090 }
log_level = "debug"
timeout_ms = 5000`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Host != "127.0.0.1" || cfg.Server.Port != 9090 {
		t.Fatalf("server = %+v", cfg.Server)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("log_level = %s", cfg.LogLevel)
	}
}

func TestLoadMissingExplicitFile(t *testing.T) {
	if _, err := Load("/no/such/config.toml"); err == nil {
		t.Fatal("expected error for missing explicit config file")
	}
}

func TestValidateBadPort(t *testing.T) {
	cfg := Default()
	cfg.Server.Port = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error")
	}
}
