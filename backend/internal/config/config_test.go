package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlagProvider(t *testing.T) {
	t.Run("only loads explicitly set flags", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("listen", ":4327", "listen address")
		fs.String("data-dir", "/default", "data directory")
		fs.Int("db-max-conns", 4, "max conns")

		// Only set listen explicitly.
		require.NoError(t, fs.Parse([]string{"-listen", ":9999"}))

		fieldMap := map[string]string{
			"listen":       "listen",
			"data-dir":     "data_dir",
			"db-max-conns": "db_max_conns",
		}

		fp := NewFlagProvider(fs, fieldMap)
		m, err := fp.Read()
		require.NoError(t, err)

		// Only listen should be present.
		assert.Equal(t, map[string]interface{}{
			"listen": ":9999",
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
		fs.String("listen", ":4327", "listen address")
		fs.Bool("version", false, "show version")

		require.NoError(t, fs.Parse([]string{"-listen", ":9999", "-version"}))

		fieldMap := map[string]string{
			"listen": "listen",
			// version not in map
		}

		fp := NewFlagProvider(fs, fieldMap)
		m, err := fp.Read()
		require.NoError(t, err)

		assert.Equal(t, map[string]interface{}{
			"listen": ":9999",
		}, m)
	})

	// A nested key must arrive nested. koanf.Load does not split a key on the
	// delimiter, so a flat "storage.postgres.dsn" lands as a top-level key whose
	// name merely contains dots: Get finds it, because the lookup index is flat
	// either way, and Unmarshal does not, because it walks the nested map. Every
	// storage flag parsed, reported no error, and was then ignored.
	t.Run("nests a dotted key", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("storage-type", "", "storage type")
		fs.String("storage-postgres-dsn", "", "postgres dsn")
		fs.Int("storage-postgres-max-conns", 25, "postgres max conns")

		require.NoError(t, fs.Parse([]string{
			"-storage-type", "postgres",
			"-storage-postgres-dsn", "postgres://example/db",
			"-storage-postgres-max-conns", "77",
		}))

		fp := NewFlagProvider(fs, map[string]string{
			"storage-type":               "storage.type",
			"storage-postgres-dsn":       "storage.postgres.dsn",
			"storage-postgres-max-conns": "storage.postgres.max_conns",
		})
		m, err := fp.Read()
		require.NoError(t, err)

		assert.Equal(t, map[string]interface{}{
			"storage": map[string]interface{}{
				"type": "postgres",
				"postgres": map[string]interface{}{
					"dsn":       "postgres://example/db",
					"max_conns": "77",
				},
			},
		}, m)
	})

	// A key with no delimiter must stay exactly where it was, so the nesting
	// above cannot change how a flat key reaches the config.
	t.Run("leaves a flat key at the top level", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("log-level", "info", "log level")

		require.NoError(t, fs.Parse([]string{"-log-level", "debug"}))

		m, err := NewFlagProvider(fs, map[string]string{"log-level": "log_level"}).Read()
		require.NoError(t, err)

		assert.Equal(t, map[string]interface{}{"log_level": "debug"}, m)
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
		// Pick a path that's absolute on whatever OS we're running on so the
		// "return as-is" branch of ResolveDataDir is actually exercised.
		absDir := t.TempDir() // always absolute
		result := ResolveDataDir(absDir, "/some/config.yaml", "/default/config/dir")
		assert.Equal(t, absDir, result)
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

func TestHiddenFunc(t *testing.T) {
	newFlags := func(received *[]string) *flag.FlagSet {
		fs := flag.NewFlagSet("leapmux probe", flag.ContinueOnError)
		fs.String("visible", "shown", "a visible flag")
		fs.Int("count", 3, "a visible count")
		HiddenFunc(fs, "secret", "a hidden flag", func(value string) error {
			*received = append(*received, value)
			return nil
		})
		return fs
	}

	t.Run("the flag parses and reaches its function", func(t *testing.T) {
		var received []string
		fs := newFlags(&received)
		require.NoError(t, fs.Parse([]string{"--secret", "one", "-secret=two"}))
		assert.Equal(t, []string{"one", "two"}, received)
	})

	t.Run("an error from the function fails the parse", func(t *testing.T) {
		fs := flag.NewFlagSet("leapmux probe", flag.ContinueOnError)
		fs.SetOutput(&strings.Builder{})
		HiddenFunc(fs, "secret", "", func(string) error { return errors.New("refused") })
		require.ErrorContains(t, fs.Parse([]string{"--secret", "x"}), "refused")
	})

	for _, tc := range []struct {
		name       string
		categories map[string]string
	}{
		{"uncategorized help", nil},
		{"categorized help", map[string]string{"visible": "Probe options", "count": "Probe options", "secret": "Probe options"}},
	} {
		t.Run(tc.name+" omits the flag", func(t *testing.T) {
			var received []string
			fs := newFlags(&received)
			var output strings.Builder
			fs.SetOutput(&output)
			// Change a value first: help prints the default from definition
			// time, not the parsed value.
			require.NoError(t, fs.Parse([]string{"--count", "9"}))
			PrintFlagUsage(fs, "Probe.", tc.categories, []string{"Probe options"})
			text := output.String()
			assert.Contains(t, text, "Usage: leapmux probe [flags]")
			assert.Contains(t, text, "-visible")
			assert.Contains(t, text, `(default "shown")`)
			assert.Contains(t, text, "(default 3)")
			assert.NotContains(t, text, "(default 9)")
			assert.NotContains(t, text, "secret")
			assert.NotContains(t, text, "a hidden flag")
		})
	}

	t.Run("a category that holds only hidden flags prints no header", func(t *testing.T) {
		var received []string
		fs := newFlags(&received)
		var output strings.Builder
		fs.SetOutput(&output)
		PrintFlagUsage(fs, "", map[string]string{"visible": "Probe options", "count": "Probe options", "secret": "Internal options"}, nil)
		assert.Contains(t, output.String(), "Probe options:")
		assert.NotContains(t, output.String(), "Internal options")
	})
}
