// Package config manages the CLI's local connection settings.
//
// The file lives at ~/.keypoint/config.json and holds exactly what a client
// needs to reach a server as one identity. It is deliberately small and
// hand-editable: when something is wrong, `cat ~/.keypoint/config.json` should
// explain it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultServer is where `kp serve` listens by default.
const DefaultServer = "http://127.0.0.1:8787"

// Config is the on-disk client configuration.
type Config struct {
	Server     string `json:"server"`
	APIKey     string `json:"api_key"`
	Identity   string `json:"identity,omitempty"`
	ActiveRole string `json:"active_role,omitempty"`
	// Path is not serialised; it records where this config was loaded from.
	Path string `json:"-"`
}

// Dir returns the config directory, honouring KEYPOINT_HOME for tests and for
// running several identities side by side.
func Dir() string {
	if v := os.Getenv("KEYPOINT_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".keypoint"
	}
	return filepath.Join(home, ".keypoint")
}

// Path is the full path of the config file.
func Path() string { return filepath.Join(Dir(), "config.json") }

// Load reads the config, layering environment variables on top so that a single
// command can target another server or act as another identity without editing
// the file:
//
//	KEYPOINT_SERVER   overrides server
//	KEYPOINT_API_KEY  overrides api_key
//	KEYPOINT_ROLE     overrides active_role
func Load() (*Config, error) {
	c := &Config{Server: DefaultServer, Path: Path()}
	data, err := os.ReadFile(Path())
	switch {
	case err == nil:
		if err := json.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("配置文件损坏 (%s): %w\n修掉它，或删掉重跑 `kp init`", Path(), err)
		}
	case errors.Is(err, os.ErrNotExist):
		// No file yet: environment variables alone are enough to run.
	default:
		return nil, err
	}
	if c.Server == "" {
		c.Server = DefaultServer
	}
	if v := os.Getenv("KEYPOINT_SERVER"); v != "" {
		c.Server = v
	}
	if v := os.Getenv("KEYPOINT_API_KEY"); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv("KEYPOINT_ROLE"); v != "" {
		c.ActiveRole = v
	}
	c.Server = strings.TrimRight(c.Server, "/")
	return c, nil
}

// Save writes the config with owner-only permissions: it contains an API key.
func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// Exists reports whether a config file is present.
func Exists() bool {
	_, err := os.Stat(Path())
	return err == nil
}

// Require returns the loaded config, or an error that tells the user to init.
func Require() (*Config, error) {
	c, err := Load()
	if err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, fmt.Errorf("还没有配置 API key（%s 不存在或为空）\n\n先初始化：\n  kp init --server %s\n\n或临时用环境变量：\n  export KEYPOINT_SERVER=%s KEYPOINT_API_KEY=kp_...",
			Path(), c.Server, c.Server)
	}
	return c, nil
}
