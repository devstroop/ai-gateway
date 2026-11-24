package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type ServerConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

type UpstreamConfig struct {
	URL           string `toml:"url"`
	Authorization string `toml:"authorization"`
}

type ApiType string

const (
	ApiTypeChatCompletions ApiType = "chat-completions"
	ApiTypeMessages        ApiType = "messages"
	ApiTypeResponses       ApiType = "responses"
	ApiTypeSystemone       ApiType = "systemone"
	ApiTypeEmbeddings      ApiType = "embeddings"
	ApiTypeModerations     ApiType = "moderations"
)

type ProviderConfig struct {
	ID            string   `toml:"id"`
	Vendor        string   `toml:"vendor"`
	APIType       ApiType  `toml:"api_type"`
	Models        []string `toml:"models"`
	URL           string   `toml:"url"`
	Authorization string   `toml:"authorization"`
}

type KeyConfig struct {
	Name    string `toml:"name"`
	Key     string `toml:"key"`
	KeyHash string `toml:"key_hash"`
	Role    string `toml:"role"`
	Owner   string `toml:"owner"`
}

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Upstream UpstreamConfig `toml:"upstream"`
	LogLevel string         `toml:"log_level"`

	DefaultModel string `toml:"default_model"`
	TimeoutMs    int    `toml:"timeout_ms"`

	OwnedBy       string            `toml:"owned_by"`
	ModelPrefixes []string          `toml:"model_prefixes"`
	ModelMap      map[string]string `toml:"model_map"`
	Providers     []ProviderConfig  `toml:"providers"`

	APIKeys []string    `toml:"api_keys"`
	Keys    []KeyConfig `toml:"keys"`
}

func Default() Config {
	return Config{
		Server:        ServerConfig{Host: "0.0.0.0", Port: 8080},
		LogLevel:      "info",
		DefaultModel:  "default",
		TimeoutMs:     30000,
		OwnedBy:       "ai-gateway-go",
		ModelMap:      map[string]string{},
		ModelPrefixes: []string{},
	}
}

func (c Config) EffectiveProviders() []ProviderConfig {
	if len(c.Providers) > 0 {
		out := make([]ProviderConfig, len(c.Providers))
		copy(out, c.Providers)
		for i := range out {
			if out[i].APIType == "" {
				out[i].APIType = ApiTypeChatCompletions
			}
			if out[i].Vendor == "" {
				out[i].Vendor = "opencode"
			}
		}
		return out
	}
	if c.Upstream.URL != "" {
		return []ProviderConfig{{
			ID:            "default",
			Vendor:        "opencode",
			APIType:       ApiTypeChatCompletions,
			Models:        []string{"*"},
			URL:           c.Upstream.URL,
			Authorization: c.Upstream.Authorization,
		}}
	}
	return nil
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return Config{}, fmt.Errorf("config file not found: %s", path)
			}
			return Config{}, fmt.Errorf("stat config %s: %w", path, err)
		}
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if v := os.Getenv("HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return Config{}, fmt.Errorf("invalid PORT %q: %w", v, err)
		}
		cfg.Server.Port = p
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Server.Host == "" {
		return fmt.Errorf("server host must not be empty")
	}
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}
	switch strings.ToLower(c.LogLevel) {
	case "", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log_level: %s", c.LogLevel)
	}
	if c.TimeoutMs < 1000 || c.TimeoutMs > 300000 {
		return fmt.Errorf("invalid timeout_ms: %d", c.TimeoutMs)
	}
	for i, p := range c.ModelPrefixes {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("model_prefixes[%d] must not be empty", i)
		}
		if p != strings.TrimSpace(p) {
			return fmt.Errorf("model_prefixes[%d] must not have surrounding whitespace", i)
		}
	}
	if len(c.Providers) == 0 && c.Upstream.URL != "" {
		u, err := url.ParseRequestURI(c.Upstream.URL)
		if err != nil {
			return fmt.Errorf("invalid upstream url: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("invalid upstream url scheme: %s", u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("invalid upstream url: missing host")
		}
	}

	for k, v := range c.ModelMap {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("model_map key must not be empty")
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("model_map value for %q must not be empty", k)
		}
		if k != strings.TrimSpace(k) || v != strings.TrimSpace(v) {
			return fmt.Errorf("model_map key/value must not have surrounding whitespace")
		}
	}
	seen := map[string]bool{}
	for i, p := range c.Providers {
		if p.ID == "" {
			return fmt.Errorf("providers[%d]: id is required", i)
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate provider id: %s", p.ID)
		}
		seen[p.ID] = true
		if p.URL == "" {
			return fmt.Errorf("providers[%d] (%s): url is required", i, p.ID)
		}
		u, err := url.ParseRequestURI(p.URL)
		if err != nil {
			return fmt.Errorf("providers[%d] (%s): invalid url: %w", i, p.ID, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("providers[%d] (%s): invalid url scheme: %s", i, p.ID, u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("providers[%d] (%s): invalid url: missing host", i, p.ID)
		}
		if p.APIType != "" {
			switch p.APIType {
			case ApiTypeChatCompletions, ApiTypeMessages, ApiTypeResponses, ApiTypeSystemone, ApiTypeEmbeddings, ApiTypeModerations:
			default:
				return fmt.Errorf("providers[%d] (%s): unknown api_type: %s", i, p.ID, p.APIType)
			}
		}
		apiType := p.APIType
		if apiType == "" {
			apiType = ApiTypeChatCompletions
		}
		for j, m := range p.Models {
			if strings.TrimSpace(m) == "" {
				return fmt.Errorf("providers[%d] (%s): models[%d] must not be empty", i, p.ID, j)
			}
			if m != strings.TrimSpace(m) {
				return fmt.Errorf("providers[%d] (%s): models[%d] must not have surrounding whitespace", i, p.ID, j)
			}
		}
		if apiType == ApiTypeChatCompletions {
			if len(p.Models) == 0 {
				return fmt.Errorf("providers[%d] (%s): chat provider must declare at least one model or \"*\"", i, p.ID)
			}
		}
	}

	// Reject duplicate model claims among chat-completion providers.
	modelOwners := map[string]string{}
	catchAllCount := 0
	for _, p := range c.Providers {
		apiType := p.APIType
		if apiType == "" {
			apiType = ApiTypeChatCompletions
		}
		if apiType != ApiTypeChatCompletions {
			continue
		}
		for _, m := range p.Models {
			if m == "*" {
				catchAllCount++
				if catchAllCount > 1 {
					return fmt.Errorf("multiple catch-all (\"*\") chat providers: %s", p.ID)
				}
				continue
			}
			if prev, ok := modelOwners[m]; ok {
				return fmt.Errorf("model %q claimed by multiple providers: %s, %s", m, prev, p.ID)
			}
			modelOwners[m] = p.ID
		}
	}
	return nil
}

func (c Config) HasAuth() bool {
	for _, k := range c.APIKeys {
		if k != "" {
			return true
		}
	}
	for _, kc := range c.Keys {
		if kc.Key != "" || kc.KeyHash != "" {
			return true
		}
	}
	return false
}

func (c Config) Timeout() time.Duration {
	return time.Duration(c.TimeoutMs) * time.Millisecond
}
