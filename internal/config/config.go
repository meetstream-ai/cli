// Package config reads and writes ~/.meetstream/config.json, the file the JS
// CLI already uses. Fields are only ever added, never renamed, so both CLIs can
// share it while users switch. MEETSTREAM_API_KEY always wins over the file.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is the on-disk shape. Unknown fields are kept on save.
type Config struct {
	APIKey     string `json:"api_key,omitempty"`
	APIKeyName string `json:"api_key_name,omitempty"`
	extra      map[string]json.RawMessage
}

const DefaultAPIURL = "https://api.meetstream.ai/api/v1"

// Dir is ~/.meetstream, or $MEETSTREAM_CONFIG_DIR when set (tests use it).
func Dir() string {
	if d := os.Getenv("MEETSTREAM_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".meetstream"
	}
	return filepath.Join(home, ".meetstream")
}

func Path() string { return filepath.Join(Dir(), "config.json") }

// APIURL is $MEETSTREAM_API_URL or the production API.
func APIURL() string {
	if u := os.Getenv("MEETSTREAM_API_URL"); u != "" {
		return u
	}
	return DefaultAPIURL
}

func Load() (*Config, error) {
	c := &Config{extra: map[string]json.RawMessage{}}
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c.extra); err != nil {
		return c, err
	}
	_ = json.Unmarshal(b, c)
	return c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	out := map[string]any{}
	for k, v := range c.extra {
		out[k] = v
	}
	set := func(k, v string) {
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	set("api_key", c.APIKey)
	set("api_key_name", c.APIKeyName)
	b, _ := json.MarshalIndent(out, "", "  ")
	return os.WriteFile(Path(), append(b, '\n'), 0o600)
}

// Source says where the key came from: "env", "file" or "" when there is none.
func ResolveKey() (key, source string) {
	if k := os.Getenv("MEETSTREAM_API_KEY"); k != "" {
		return k, "env"
	}
	if c, err := Load(); err == nil && c.APIKey != "" {
		return c.APIKey, "file"
	}
	return "", ""
}

// Mask shows only the last four characters of a key, never the key itself.
func Mask(k string) string {
	if len(k) <= 8 {
		return "••••"
	}
	return k[:3] + "…" + k[len(k)-4:]
}
