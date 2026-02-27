package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFrom_FileNotExist(t *testing.T) {
	t.Parallel()
	cfg, err := LoadFrom("/nonexistent/path/.spotinfo.yaml")
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Nil(t, cfg.Defaults)
	assert.Nil(t, cfg.Profiles)
}

func TestLoadFrom_ValidConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".spotinfo.yaml")

	content := `defaults:
  region: us-west-2
  os: linux
  output: table
profiles:
  ml:
    region: us-east-1
    arch: arm64
  ci:
    output: json
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "us-west-2", cfg.Defaults["region"])
	assert.Equal(t, "linux", cfg.Defaults["os"])
	assert.Equal(t, "us-east-1", cfg.Profiles["ml"]["region"])
	assert.Equal(t, "arm64", cfg.Profiles["ml"]["arch"])
	assert.Equal(t, "json", cfg.Profiles["ci"]["output"])
}

func TestLoadFrom_InvalidYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".spotinfo.yaml")

	require.NoError(t, os.WriteFile(path, []byte(":::invalid"), 0o600))

	_, err := LoadFrom(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config")
}

func TestLoadFrom_EmptyPath(t *testing.T) {
	t.Parallel()
	// Empty path uses home dir — file likely doesn't exist, should return empty config
	cfg, err := LoadFrom("")
	require.NoError(t, err)
	assert.NotNil(t, cfg)
}

func TestResolve_DefaultsOnly(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Defaults: map[string]any{"region": "eu-west-1", "output": "json"},
	}

	vals, err := cfg.Resolve("")
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", vals["region"])
	assert.Equal(t, "json", vals["output"])
}

func TestResolve_ProfileOverridesDefaults(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Defaults: map[string]any{"region": "us-east-1", "output": "table"},
		Profiles: map[string]map[string]any{
			"ml": {"region": "eu-west-1", "arch": "arm64"},
		},
	}

	vals, err := cfg.Resolve("ml")
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", vals["region"])
	assert.Equal(t, "table", vals["output"])
	assert.Equal(t, "arm64", vals["arch"])
}

func TestResolve_UnknownProfile(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Profiles: map[string]map[string]any{
			"ml": {"region": "eu-west-1"},
		},
	}

	_, err := cfg.Resolve("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolve_EmptyConfig(t *testing.T) {
	t.Parallel()
	cfg := &Config{}

	vals, err := cfg.Resolve("")
	require.NoError(t, err)
	assert.Empty(t, vals)
}
