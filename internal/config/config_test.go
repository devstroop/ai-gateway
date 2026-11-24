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

func TestEffectiveProvidersFromConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	content := `
[[providers]]
id = "p1"
api_type = "chat-completions"
models = ["m1"]
url = "http://example.com/v1"

[[providers]]
id = "p2"
api_type = "chat-completions"
models = ["m2"]
url = "http://example2.com/v1"
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	ps := cfg.EffectiveProviders()
	if len(ps) != 2 {
		t.Fatalf("providers = %d", len(ps))
	}
	if ps[0].ID != "p1" || ps[1].ID != "p2" {
		t.Fatalf("ids = %s %s", ps[0].ID, ps[1].ID)
	}
}

func TestValidateDuplicateProvider(t *testing.T) {
	cfg := Default()
	cfg.Providers = []ProviderConfig{
		{ID: "a", URL: "http://x/v1", Models: []string{"m"}},
		{ID: "a", URL: "http://y/v1", Models: []string{"m2"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate error")
	}
}
