// Package project owns project settings. The bootstrap loads only user UI
// preferences; discovery, project layers, and trust are implemented separately.
package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Paths are resolved without creating directories. Use XDG conventions on macOS
// too, rather than os.UserConfigDir's Application Support default.
type Paths struct {
	ConfigFile string `json:"configFile"`
	StateDir   string `json:"stateDir"`
	CacheDir   string `json:"cacheDir"`
}

func UserPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	resolve := func(name, fallback string) (string, error) {
		value := os.Getenv(name)
		if value == "" {
			value = filepath.Join(home, fallback)
		}
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("%s must be absolute", name)
		}
		return filepath.Join(value, "backlog"), nil
	}
	config, err := resolve("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return Paths{}, err
	}
	state, err := resolve("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return Paths{}, err
	}
	cache, err := resolve("XDG_CACHE_HOME", ".cache")
	if err != nil {
		return Paths{}, err
	}
	return Paths{ConfigFile: filepath.Join(config, "config.yaml"), StateDir: state, CacheDir: cache}, nil
}

type UIConfig struct {
	HighContrast bool `yaml:"highContrast" json:"highContrast"`
}

type Config struct {
	UI    UIConfig `yaml:"ui" json:"ui"`
	Paths Paths    `yaml:"-" json:"paths"`
}

// Load applies built-ins, the optional user file, and a flag/environment
// override. Nil means absent; false is an explicit override. It never writes,
// executes configuration, or reads committed project configuration.
func Load(paths Paths, highContrast *bool) (Config, error) {
	cfg := Config{Paths: paths}
	file, err := os.Open(paths.ConfigFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return Config{}, fmt.Errorf("%s: %w", paths.ConfigFile, err)
	default:
		defer file.Close()
		const maxSize = 1 << 20
		data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", paths.ConfigFile, err)
		}
		if len(data) > maxSize {
			return Config{}, fmt.Errorf("%s: config exceeds 1 MiB", paths.ConfigFile)
		}
		if err := decode(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", paths.ConfigFile, err)
		}
	}
	if highContrast != nil {
		cfg.UI.HighContrast = *highContrast
	}
	return cfg, nil
}

func decode(data []byte, cfg *Config) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("expected a single YAML document")
	}
	return nil
}
