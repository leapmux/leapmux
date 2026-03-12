package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractConfigFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		def      string
		expected string
	}{
		{
			name:     "no flag uses default",
			args:     []string{"-addr", ":9999"},
			def:      "/default/path.yaml",
			expected: "/default/path.yaml",
		},
		{
			name:     "-config with space",
			args:     []string{"-config", "/custom/path.yaml", "-addr", ":9999"},
			def:      "/default/path.yaml",
			expected: "/custom/path.yaml",
		},
		{
			name:     "--config with space",
			args:     []string{"--config", "/custom/path.yaml"},
			def:      "/default/path.yaml",
			expected: "/custom/path.yaml",
		},
		{
			name:     "-config=value",
			args:     []string{"-config=/custom/path.yaml"},
			def:      "/default/path.yaml",
			expected: "/custom/path.yaml",
		},
		{
			name:     "--config=value",
			args:     []string{"--config=/custom/path.yaml"},
			def:      "/default/path.yaml",
			expected: "/custom/path.yaml",
		},
		{
			name:     "-config at end without value uses default",
			args:     []string{"-addr", ":9999", "-config"},
			def:      "/default/path.yaml",
			expected: "/default/path.yaml",
		},
		{
			name:     "empty args uses default",
			args:     nil,
			def:      "/default/path.yaml",
			expected: "/default/path.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractConfigFlag(tt.args, tt.def)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFlagProvider(t *testing.T) {
	t.Run("only loads explicitly set flags", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("addr", ":4327", "listen address")
		fs.String("data-dir", "/default", "data directory")
		fs.Int("db-max-conns", 4, "max conns")

		// Only set addr explicitly.
		require.NoError(t, fs.Parse([]string{"-addr", ":9999"}))

		fieldMap := map[string]string{
			"addr":         "addr",
			"data-dir":     "data_dir",
			"db-max-conns": "db_max_conns",
		}

		fp := NewFlagProvider(fs, fieldMap)
		m, err := fp.Read()
		require.NoError(t, err)

		// Only addr should be present.
		assert.Equal(t, map[string]interface{}{
			"addr": ":9999",
		}, m)
	})

	t.Run("maps hyphens to underscores", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("data-dir", "/default", "data directory")

		require.NoError(t, fs.Parse([]string{"-data-dir", "/custom"}))

		fieldMap := map[string]string{
			"data-dir": "data_dir",
		}

		fp := NewFlagProvider(fs, fieldMap)
		m, err := fp.Read()
		require.NoError(t, err)

		assert.Equal(t, map[string]interface{}{
			"data_dir": "/custom",
		}, m)
	})

	t.Run("ignores flags not in field map", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("addr", ":4327", "listen address")
		fs.Bool("version", false, "show version")

		require.NoError(t, fs.Parse([]string{"-addr", ":9999", "-version"}))

		fieldMap := map[string]string{
			"addr": "addr",
			// version not in map
		}

		fp := NewFlagProvider(fs, fieldMap)
		m, err := fp.Read()
		require.NoError(t, err)

		assert.Equal(t, map[string]interface{}{
			"addr": ":9999",
		}, m)
	})
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "tilde only",
			input:    "~",
			expected: home,
		},
		{
			name:     "tilde with path",
			input:    "~/some/path",
			expected: filepath.Join(home, "some/path"),
		},
		{
			name:     "absolute path unchanged",
			input:    "/absolute/path",
			expected: "/absolute/path",
		},
		{
			name:     "relative path unchanged",
			input:    "relative/path",
			expected: "relative/path",
		},
		{
			name:     "empty string unchanged",
			input:    "",
			expected: "",
		},
		{
			name:     "tilde in middle unchanged",
			input:    "/some/~/path",
			expected: "/some/~/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandHome(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveDataDir(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	t.Run("absolute data dir used as-is", func(t *testing.T) {
		result := ResolveDataDir("/absolute/data", "/some/config.yaml", "/default/config/dir")
		assert.Equal(t, "/absolute/data", result)
	})

	t.Run("relative data dir resolved against config file directory", func(t *testing.T) {
		// Create a temp config file so os.Stat succeeds.
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))

		result := ResolveDataDir(".", configPath, "/default/dir")
		assert.Equal(t, tmpDir, result)
	})

	t.Run("relative data dir with subpath resolved against config file directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))

		result := ResolveDataDir("data/sub", configPath, "/default/dir")
		assert.Equal(t, filepath.Join(tmpDir, "data/sub"), result)
	})

	t.Run("relative data dir falls back to default config dir when config file missing", func(t *testing.T) {
		result := ResolveDataDir(".", "/nonexistent/config.yaml", "~/.config/leapmux/hub")
		assert.Equal(t, filepath.Join(home, ".config/leapmux/hub"), result)
	})

	t.Run("relative data dir falls back to default config dir when config path empty", func(t *testing.T) {
		result := ResolveDataDir(".", "", "~/.config/leapmux/hub")
		assert.Equal(t, filepath.Join(home, ".config/leapmux/hub"), result)
	})

	t.Run("tilde in data dir expanded", func(t *testing.T) {
		result := ResolveDataDir("~/custom/data", "/some/config.yaml", "/default/dir")
		assert.Equal(t, filepath.Join(home, "custom/data"), result)
	})
}
