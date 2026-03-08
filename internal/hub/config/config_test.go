package config

import (
	"os"
	"path/filepath"
	"testing"

	hubdb "github.com/leapmux/leapmux/internal/hub/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	t.Run("defaults only", func(t *testing.T) {
		cfg, showVersion, err := Load(nil)
		require.NoError(t, err)
		assert.False(t, showVersion)
		assert.Equal(t, ":4327", cfg.Addr)
		assert.Equal(t, filepath.Join(home, ".config/leapmux/hub"), cfg.DataDir)
		assert.Equal(t, "", cfg.DevFrontend)
		assert.Equal(t, hubdb.DefaultMaxConns, cfg.DBMaxConns)
		assert.Equal(t, 0, cfg.MaxMessageSize)
		assert.Equal(t, 0, cfg.MaxIncompleteChunked)
		assert.Equal(t, "info", cfg.LogLevel)
	})

	t.Run("config file overrides defaults", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "hub.yaml")
		yamlContent := `addr: ":9999"
db_max_conns: 16
log_level: "debug"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, _, err := Load([]string{"-config", configPath})
		require.NoError(t, err)
		assert.Equal(t, ":9999", cfg.Addr)
		assert.Equal(t, 16, cfg.DBMaxConns)
		assert.Equal(t, "debug", cfg.LogLevel)
		// data_dir defaults to "." resolved against config file dir.
		assert.Equal(t, tmpDir, cfg.DataDir)
	})

	t.Run("env vars override config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "hub.yaml")
		yamlContent := `addr: ":9999"
log_level: "debug"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		t.Setenv("LEAPMUX_HUB_ADDR", ":7777")

		cfg, _, err := Load([]string{"-config", configPath})
		require.NoError(t, err)
		assert.Equal(t, ":7777", cfg.Addr)
		assert.Equal(t, "debug", cfg.LogLevel) // from config file
	})

	t.Run("CLI flags override env vars", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB_ADDR", ":7777")

		cfg, _, err := Load([]string{"-addr", ":5555"})
		require.NoError(t, err)
		assert.Equal(t, ":5555", cfg.Addr)
	})

	t.Run("version flag", func(t *testing.T) {
		_, showVersion, err := Load([]string{"-version"})
		require.NoError(t, err)
		assert.True(t, showVersion)
	})

	t.Run("missing config file silently ignored", func(t *testing.T) {
		cfg, _, err := Load([]string{"-config", "/nonexistent/hub.yaml"})
		require.NoError(t, err)
		assert.Equal(t, ":4327", cfg.Addr) // uses default
	})

	t.Run("invalid YAML returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "hub.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte("invalid: [yaml: broken"), 0o644))

		_, _, err := Load([]string{"-config", configPath})
		assert.Error(t, err)
	})

	t.Run("custom config file with custom data dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		dataDir := filepath.Join(tmpDir, "mydata")
		configPath := filepath.Join(tmpDir, "hub.yaml")
		yamlContent := `data_dir: "` + dataDir + `"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, _, err := Load([]string{"-config", configPath})
		require.NoError(t, err)
		assert.Equal(t, dataDir, cfg.DataDir)
	})

	t.Run("relative data dir resolved against config file dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "hub.yaml")
		yamlContent := `data_dir: "subdir"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, _, err := Load([]string{"-config", configPath})
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(tmpDir, "subdir"), cfg.DataDir)
	})

	t.Run("data dir from CLI flag", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg, _, err := Load([]string{"-data-dir", tmpDir})
		require.NoError(t, err)
		assert.Equal(t, tmpDir, cfg.DataDir)
	})
}

func TestValidate(t *testing.T) {
	t.Run("empty addr returns error", func(t *testing.T) {
		cfg := &Config{Addr: ""}
		assert.Error(t, cfg.Validate())
	})

	t.Run("valid config creates data dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		dataDir := filepath.Join(tmpDir, "data")

		cfg := &Config{Addr: ":4327", DataDir: dataDir}
		require.NoError(t, cfg.Validate())

		info, err := os.Stat(dataDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})
}

func TestPaths(t *testing.T) {
	cfg := &Config{DataDir: "/test/dir"}
	assert.Equal(t, "/test/dir/hub.db", cfg.DBPath())
	assert.Equal(t, "/test/dir/hub.sock", cfg.SocketPath())
}
