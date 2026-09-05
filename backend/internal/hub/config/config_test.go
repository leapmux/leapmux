package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unsetAmbientEnvPrefix(t *testing.T, prefix string) {
	t.Helper()
	// testing.T.Setenv cannot represent an absent variable. An empty value still
	// overrides defaults and configuration files.
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(key, prefix) {
			continue
		}
		require.NoError(t, os.Unsetenv(key))
		t.Cleanup(func() {
			require.NoError(t, os.Setenv(key, value))
		})
	}
}

func TestLoad(t *testing.T) {
	unsetAmbientEnvPrefix(t, "LEAPMUX_HUB_")
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	t.Run("defaults only", func(t *testing.T) {
		cfg, showVersion, err := Load(nil)
		require.NoError(t, err)
		assert.False(t, showVersion)
		assert.Equal(t, ":4327", cfg.Listen)
		assert.Equal(t, filepath.Join(home, ".config/leapmux/hub"), cfg.DataDir)
		assert.Equal(t, "", cfg.DevFrontend)
		assert.Equal(t, sqlitedb.DefaultMaxConns, cfg.SQLiteDBConfig().MaxConns)
		assert.Equal(t, "info", cfg.LogLevel)
	})

	t.Run("config file overrides defaults", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "hub.yaml")
		yamlContent := `listen: ":9999"
storage:
  sqlite:
    max_conns: 16
log_level: "debug"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, _, err := Load([]string{"-config", configPath})
		require.NoError(t, err)
		assert.Equal(t, ":9999", cfg.Listen)
		assert.Equal(t, 16, cfg.SQLiteDBConfig().MaxConns)
		assert.Equal(t, "debug", cfg.LogLevel)
		// data_dir defaults to "." resolved against config file dir.
		assert.Equal(t, tmpDir, cfg.DataDir)
	})

	t.Run("env vars override config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "hub.yaml")
		yamlContent := `listen: ":9999"
log_level: "debug"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		t.Setenv("LEAPMUX_HUB_LISTEN", ":7777")

		cfg, _, err := Load([]string{"-config", configPath})
		require.NoError(t, err)
		assert.Equal(t, ":7777", cfg.Listen)
		assert.Equal(t, "debug", cfg.LogLevel) // from config file
	})

	t.Run("CLI flags override env vars", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB_LISTEN", ":7777")

		cfg, _, err := Load([]string{"-listen", ":5555"})
		require.NoError(t, err)
		assert.Equal(t, ":5555", cfg.Listen)
	})

	t.Run("version flag", func(t *testing.T) {
		_, showVersion, err := Load([]string{"-version"})
		require.NoError(t, err)
		assert.True(t, showVersion)
	})

	t.Run("missing config file silently ignored", func(t *testing.T) {
		cfg, _, err := Load([]string{"-config", "/nonexistent/hub.yaml"})
		require.NoError(t, err)
		assert.Equal(t, ":4327", cfg.Listen) // uses default
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
		// YAML single quotes are literal — no backslash escape processing,
		// which matters on Windows where dataDir looks like `C:\Users\...`.
		yamlContent := "data_dir: '" + dataDir + "'\n"
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

func TestLoadWithOptions(t *testing.T) {
	unsetAmbientEnvPrefix(t, "LEAPMUX_HUB_")
	t.Run("custom DefaultListen applied", func(t *testing.T) {
		cfg, _, err := LoadWithOptions(nil, LoadOptions{
			DefaultListen: "127.0.0.1:4327",
		})
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1:4327", cfg.Listen)
	})

	t.Run("SoloMode set on output", func(t *testing.T) {
		cfg, _, err := LoadWithOptions(nil, LoadOptions{
			SoloMode: true,
		})
		require.NoError(t, err)
		assert.True(t, cfg.SoloMode)
	})

	t.Run("SoloMode false by default", func(t *testing.T) {
		cfg, _, err := LoadWithOptions(nil, LoadOptions{})
		require.NoError(t, err)
		assert.False(t, cfg.SoloMode)
	})

	t.Run("CLIFlags restriction rejects unlisted flags", func(t *testing.T) {
		_, _, err := LoadWithOptions([]string{"-dev-frontend"}, LoadOptions{
			CLIFlags: []string{"listen", "data-dir", "log-level"},
		})
		assert.Error(t, err)
	})

	t.Run("CLIFlags restriction allows listed flags", func(t *testing.T) {
		cfg, _, err := LoadWithOptions([]string{"-listen", ":9999"}, LoadOptions{
			CLIFlags: []string{"listen", "data-dir", "log-level"},
		})
		require.NoError(t, err)
		assert.Equal(t, ":9999", cfg.Listen)
	})

	t.Run("config file values for all fields work with CLIFlags restriction", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "solo.yaml")
		yamlContent := `dev_frontend: "http://localhost:5173"
local_listen: "unix:/tmp/leapmux.sock"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, _, err := LoadWithOptions([]string{"-config", configPath}, LoadOptions{
			CLIFlags: []string{"listen", "data-dir", "log-level"},
		})
		require.NoError(t, err)
		// A config-file field that is NOT in CLIFlags is still loaded: the
		// restriction narrows which CLI flags are registered, not which config keys
		// are read.
		assert.Equal(t, "http://localhost:5173", cfg.DevFrontend)
		assert.Equal(t, "unix:/tmp/leapmux.sock", cfg.LocalListen)
	})

	t.Run("custom DefaultConfigDir used for data dir resolution", func(t *testing.T) {
		home, err := os.UserHomeDir()
		require.NoError(t, err)

		cfg, _, err := LoadWithOptions(nil, LoadOptions{
			DefaultConfigDir: "~/.config/leapmux/solo",
		})
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".config/leapmux/solo"), cfg.DataDir)
	})

	t.Run("custom FlagSetName", func(t *testing.T) {
		// Verify it doesn't error (flag set name is internal).
		_, _, err := LoadWithOptions(nil, LoadOptions{
			FlagSetName: "leapmux",
		})
		require.NoError(t, err)
	})

	t.Run("ExtraFlags register, default, and group under their Category", func(t *testing.T) {
		extras := []ExtraFlagDef{
			{Name: "max-incomplete-chunked", KoanfKey: "max_incomplete_chunked", StrDefault: "0", Category: "Timeout and limit options"},
			{Name: "encryption-mode", KoanfKey: "encryption_mode", StrDefault: "post-quantum"},
		}
		cfg, _, err := LoadWithOptions([]string{"-max-incomplete-chunked", "8"}, LoadOptions{
			CLIFlags:   []string{"listen"},
			ExtraFlags: extras,
		})
		require.NoError(t, err)
		assert.Equal(t, "8", cfg.Extras["max_incomplete_chunked"])
		// An unset extra still lands in Extras at its declared default.
		assert.Equal(t, "post-quantum", cfg.Extras["encryption_mode"])

		// CLIFlags lists only "listen", so parsing -max-incomplete-chunked at all
		// is the assertion that extras bypass the allowlist: that list names the
		// subset of the HUB's own flags a launcher exposes, and an extra is by
		// definition not in it.
		//
		// The help output is where the Category actually lands (usageCategories),
		// so the grouping this subtest is named for is asserted rather than
		// assumed -- including the empty-Category default, which is the arm a
		// caller gets by omitting the field.
		output := testutil.CaptureStdout(t, func() {
			_, _, err := LoadWithOptions([]string{"--help"}, LoadOptions{
				CLIFlags:   []string{"listen"},
				ExtraFlags: extras,
			})
			require.True(t, errors.Is(err, flag.ErrHelp))
		})
		server := strings.Index(output, "\nServer options:\n")
		timeouts := strings.Index(output, "\nTimeout and limit options:\n")
		require.Positive(t, server, "an extra's fallback category must open a section")
		require.Positive(t, timeouts, "an extra's declared category must open a section")
		require.Less(t, server, timeouts, "hubFlagCategoryOrder puts Server before Timeout")
		assert.Greater(t, strings.Index(output, "  -max-incomplete-chunked string"), timeouts,
			"an extra must be listed under the Category it declared")
		encryptionMode := strings.Index(output, "  -encryption-mode string")
		assert.Greater(t, encryptionMode, server)
		assert.Less(t, encryptionMode, timeouts,
			"an extra with no Category falls back to Server options")
	})

	t.Run("ExtraFlags read from the config file too", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "solo.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte("max_incomplete_chunked: 16\n"), 0o644))

		cfg, _, err := LoadWithOptions([]string{"-config", configPath}, LoadOptions{
			CLIFlags: []string{"listen"},
			ExtraFlags: []ExtraFlagDef{
				{Name: "max-incomplete-chunked", KoanfKey: "max_incomplete_chunked", StrDefault: "0", Category: "Timeout and limit options"},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "16", cfg.Extras["max_incomplete_chunked"],
			"solo.yaml is the only config file solo reads; the embedded worker's cap must be settable there")
	})

	t.Run("the hub does not define max-incomplete-chunked", func(t *testing.T) {
		// The Hub has no chunk-count cap to tune: channelmgr's interleaving guard
		// admits one in-flight chunked sequence per channel+direction, which is
		// strictly stronger than any count cap. The flag is solo-only (worker-scoped);
		// re-adding it to allFlags would resurrect a dead hub knob.
		_, _, err := LoadWithOptions([]string{"-max-incomplete-chunked", "8"}, LoadOptions{})
		assert.Error(t, err, "the hub must not accept a flag it cannot act on")
	})

	t.Run("version flag works with options", func(t *testing.T) {
		_, showVersion, err := LoadWithOptions([]string{"-version"}, LoadOptions{
			CLIFlags: []string{"listen"},
		})
		require.NoError(t, err)
		assert.True(t, showVersion)
	})
}

func TestLoadRejectsPositionalArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"bare help word", []string{"help"}},
		{"trailing positional after flag", []string{"-listen", ":4327", "garbage"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Load(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unexpected argument")
		})
	}
}

func TestHubHelpGroupsStorageOptions(t *testing.T) {
	output := testutil.CaptureStdout(t, func() {
		_, _, err := Load([]string{"--help"})
		require.True(t, errors.Is(err, flag.ErrHelp))
	})

	sections := []string{
		"\nCommon options:\n",
		"\nServer options:\n",
		"\nStorage common options:\n",
		"\nSQLite storage options:\n",
		"\nPostgreSQL storage options:\n",
		"\nCockroachDB storage options:\n",
		"\nYugabyteDB storage options:\n",
		"\nMySQL storage options:\n",
		"\nTiDB storage options:\n",
	}
	for _, section := range sections {
		require.Contains(t, output, section)
	}
	for i := 1; i < len(sections); i++ {
		assert.Less(t, strings.Index(output, sections[i-1]), strings.Index(output, sections[i]))
	}
	assert.Contains(t, output, "\nStorage common options:\n\n  -storage-type string")
	assert.Contains(t, output, "\nSQLite storage options:\n\n  -storage-sqlite-cache-size int")
}

func TestValidate(t *testing.T) {
	t.Run("empty data dir returns error", func(t *testing.T) {
		// MkdirAll("") fails with "no such file or directory"; documents the
		// requirement that DataDir is set before Validate is called.
		cfg := &Config{DataDir: ""}
		assert.Error(t, cfg.Validate())
	})

	t.Run("removed storage backends are unsupported", func(t *testing.T) {
		for _, storageType := range []StorageType{"mongodb", "dynamodb"} {
			cfg := &Config{
				Listen:  ":4327",
				DataDir: t.TempDir(),
				Storage: StorageConfig{Type: storageType},
			}
			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, "unsupported storage.type")
			assert.ErrorContains(t, err, validStorageTypes)
		}
	})

	t.Run("valid config creates data dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		dataDir := filepath.Join(tmpDir, "data")

		cfg := &Config{Listen: ":4327", DataDir: dataDir}
		require.NoError(t, cfg.Validate())

		info, err := os.Stat(dataDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})
}

func TestPaths(t *testing.T) {
	cfg := &Config{DataDir: "/test/dir"}
	assert.Equal(t, filepath.Join("/test/dir", "hub.db"), cfg.SQLiteDBPath(), "defaults to DataDir/hub.db")

	cfg.Storage.SQLite.Path = "/custom/path.db"
	assert.Equal(t, "/custom/path.db", cfg.SQLiteDBPath(), "uses explicit SQLite path")
}

// "Exactly one default, of a kind we can register" used to be a comment over a
// union of four pointers, enforced by nothing: a row that set none simply
// vanished from -help and from the defaults map with no compile or test signal.
// That is how prefixFlags came to drop int64Default silently.
func TestFlagDefRegistersEveryKindAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, fd := range []flagDef{
		strFlag("s", "s", "c", "u", "x"),
		intFlag("i", "i", "c", "u", 1),
		int64Flag("i64", "i64", "c", "u", 2),
		boolFlag("b", "b", "c", "u", true),
	} {
		require.NotPanics(t, func() { fd.register(fs) }, "flag %q", fd.name)
		require.NotNil(t, fs.Lookup(fd.name), "flag %q must be registered", fd.name)
	}

	// The arm that makes the invariant real: an unregisterable kind is loud at
	// startup rather than a flag that quietly does not exist.
	assert.Panics(t, func() {
		flagDef{name: "bad", value: 1.5}.register(fs)
	}, "a default of an unsupported kind must not be skipped silently")
	assert.Panics(t, func() {
		flagDef{name: "none"}.register(fs)
	}, "a row with no default at all must not be skipped silently")
}

// TestDurationOptionsShareTheParser pins the property that made this one
// change rather than four: every duration key takes the same spellings. A key
// that a later change registers with fs.Int again would read "5m" as a parse
// error and "3600" as 3600 nanoseconds. The storage pool durations are the
// duration keys the hub still owns; the timeout keys moved to hub_settings.
func TestDurationOptionsShareTheParser(t *testing.T) {
	cfg, _, err := Load([]string{
		"-storage-postgres-conn-max-lifetime", "1d",
		"-storage-mysql-conn-max-idle-time", "30s",
	})
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, cfg.Storage.Postgres.ConnMaxLifetime)
	assert.Equal(t, 30*time.Second, cfg.Storage.MySQL.ConnMaxIdleTime)
}

// A flag whose koanf key is nested must reach the config. Every storage flag is
// nested, and each one parsed, reported no error, and was then dropped: the
// provider handed koanf a flat "storage.postgres.dsn", which is a top-level key
// whose name contains dots, and Unmarshal walks the nested map instead.
func TestNestedFlagsReachTheConfig(t *testing.T) {
	cfg, _, err := Load([]string{
		"-storage-type", "postgres",
		"-storage-postgres-dsn", "postgres://example/db",
		"-storage-postgres-max-conns", "77",
		"-storage-sqlite-path", "/tmp/hub.db",
	})
	require.NoError(t, err)
	assert.Equal(t, StorageTypePostgres, cfg.Storage.Type)
	assert.Equal(t, "postgres://example/db", cfg.Storage.Postgres.DSN)
	assert.Equal(t, 77, cfg.Storage.Postgres.MaxConns)
	assert.Equal(t, "/tmp/hub.db", cfg.Storage.SQLite.Path)

	// A nested flag must still lose to nothing else: the default fills what the
	// command line left alone.
	assert.Equal(t, 5, cfg.Storage.Postgres.MinConns)
}

// The database pool durations keep their own meaning for zero -- leave the
// driver's own default alone -- so the timeout resolver must not touch them.
// The pool code tests for zero (`if cfg.ConnMaxLifetime > 0`), so filling one in
// here would silently start pinning a lifetime the operator never asked for.
func TestPoolDurationsKeepTheirOwnZero(t *testing.T) {
	cfg, _, err := Load([]string{
		"-storage-postgres-conn-max-lifetime", "0",
		"-storage-mysql-conn-max-idle-time", "0",
	})
	require.NoError(t, err)
	assert.Zero(t, cfg.Storage.Postgres.ConnMaxLifetime,
		"an explicit zero must reach the pool as zero, not as the default")
	assert.Zero(t, cfg.Storage.MySQL.ConnMaxIdleTime)

	// The flag default still applies when the operator sets nothing, so zero
	// only ever arrives because someone asked for it.
	unset, _, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, unset.Storage.Postgres.ConnMaxLifetime)
	assert.Equal(t, 5*time.Minute, unset.Storage.MySQL.ConnMaxIdleTime)
}
