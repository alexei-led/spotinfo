package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const configFileName = ".spotinfo.yaml"

// Config represents the spotinfo configuration file.
type Config struct {
	Defaults map[string]any            `yaml:"defaults"`
	Profiles map[string]map[string]any `yaml:"profiles"`
}

// Load reads the config from ~/.spotinfo.yaml.
// Returns an empty config if the file doesn't exist.
func Load() (*Config, error) {
	return LoadFrom("")
}

// LoadFrom reads the config from the given path.
// If path is empty, uses ~/.spotinfo.yaml.
func LoadFrom(path string) (*Config, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return &Config{}, fmt.Errorf("home dir: %w", err) //nolint:nilerr // empty config is valid fallback
		}
		path = filepath.Join(home, configFileName)
	}

	path = filepath.Clean(path)

	data, err := os.ReadFile(path) //nolint:gosec // path is from user home dir or explicit caller input
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return &cfg, nil
}

// Resolve returns merged values: defaults overridden by profile.
// Returns empty map if neither defaults nor profile exist.
func (c *Config) Resolve(profile string) (map[string]any, error) {
	merged := make(map[string]any)

	for k, v := range c.Defaults {
		merged[k] = v
	}

	if profile != "" {
		p, ok := c.Profiles[profile]
		if !ok {
			return nil, fmt.Errorf("profile %q not found in config", profile)
		}
		for k, v := range p {
			merged[k] = v
		}
	}

	return merged, nil
}
