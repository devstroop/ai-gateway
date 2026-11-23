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

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Upstream UpstreamConfig `toml:"upstream"`
	LogLevel string         `toml:"log_level"`

	DefaultModel string `toml:"default_model"`
	TimeoutMs    int    `toml:"timeout_ms"`
}

func Default() Config {
	return Config{
		Server:       ServerConfig{Host: "0.0.0.0", Port: 8080},
		LogLevel:     "info",
		DefaultModel: "default",
		TimeoutMs:    30000,
	}
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
	if c.Upstream.URL != "" {
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
	return nil
}

func (c Config) Timeout() time.Duration {
	return time.Duration(c.TimeoutMs) * time.Millisecond
}
