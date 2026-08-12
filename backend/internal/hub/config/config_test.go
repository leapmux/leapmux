package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/memlimit"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
		assert.Equal(t, ":4327", cfg.Listen)
		assert.Equal(t, "", cfg.PublicURL)
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

	// The reference docs state one rule -- every key's flag is the key with
	// underscores turned into hyphens, bar the two listed as flagless -- so a key
	// without a flag is not a smaller feature, it is that rule being false. These
	// three shipped YAML/env-only, and an operator following the documented
	// derivation got "flag provided but not defined".
	t.Run("queue memory budgets are settable from the command line", func(t *testing.T) {
		cfg, _, err := Load([]string{
			"-relay-queue-memory-budget", "134217728",
			"-worker-queue-memory-budget", "67108864",
			"-userevents-queue-memory-budget", "33554432",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(128<<20), cfg.RelayQueueMemoryBudget)
		assert.Equal(t, int64(64<<20), cfg.WorkerQueueMemoryBudget)
		assert.Equal(t, int64(32<<20), cfg.UserEventsQueueMemoryBudget)
	})

	// int64 end to end: an 8 GiB budget is inside the documented ceiling, and
	// nothing on the way from the flag to sendq.PoolConfig may narrow it.
	t.Run("a multi-gigabyte budget survives the flag and the struct", func(t *testing.T) {
		cfg, _, err := Load([]string{"-relay-queue-memory-budget", "8589934592"})
		require.NoError(t, err)
		assert.Equal(t, int64(8<<30), cfg.RelayQueueMemoryBudget)
		// An explicit budget wins outright, so any basis resolves to it.
		assert.Equal(t, int64(8<<30), cfg.ResolveRelayQueueMemoryBudget(
			memlimit.Basis{Bytes: 8 << 30, Source: memlimit.SourceGoMemLimit}).Capacity)
	})
}

func TestLoadWithOptions(t *testing.T) {
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
		_, _, err := LoadWithOptions([]string{"-signup-enabled"}, LoadOptions{
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
		yamlContent := `smtp_port: 2525
signup_enabled: true
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, _, err := LoadWithOptions([]string{"-config", configPath}, LoadOptions{
			CLIFlags: []string{"listen", "data-dir", "log-level"},
		})
		require.NoError(t, err)
		// A config-file field that is NOT in CLIFlags is still loaded: the
		// restriction narrows which CLI flags are registered, not which config keys
		// are read.
		assert.Equal(t, 2525, cfg.SmtpPort)
		assert.True(t, cfg.SignupEnabled)
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
		"\nTimeout and limit options:\n",
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
	assert.Contains(t, output, "\nTimeout and limit options:\n\n  -agent-startup-timeout-seconds int")
	assert.Contains(t, output, "\nStorage common options:\n\n  -storage-type string")
	assert.Contains(t, output, "\nSQLite storage options:\n\n  -storage-sqlite-cache-size int")
}

func TestLoadPublicURL(t *testing.T) {
	t.Run("CLI flag accepted and stored verbatim", func(t *testing.T) {
		cfg, _, err := Load([]string{"-public-url", "https://hub.example.com"})
		require.NoError(t, err)
		assert.Equal(t, "https://hub.example.com", cfg.PublicURL)
	})

	t.Run("trailing slash stripped", func(t *testing.T) {
		cfg, _, err := Load([]string{"-public-url", "https://hub.example.com/"})
		require.NoError(t, err)
		assert.Equal(t, "https://hub.example.com", cfg.PublicURL)
	})

	t.Run("env var", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB_PUBLIC_URL", "https://hub.example.com")
		cfg, _, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, "https://hub.example.com", cfg.PublicURL)
	})

	t.Run("YAML key", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "hub.yaml")
		yamlContent := `public_url: "https://hub.example.com"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))
		cfg, _, err := Load([]string{"-config", configPath})
		require.NoError(t, err)
		assert.Equal(t, "https://hub.example.com", cfg.PublicURL)
	})

	t.Run("invalid URLs are rejected", func(t *testing.T) {
		cases := []struct {
			name, value string
		}{
			{"not a URL", "not-a-url"},
			{"wrong scheme", "ftp://example.com"},
			{"empty hostname", "https://:443"},
			{"path", "https://example.com/leapmux"},
			{"query", "https://example.com?x=1"},
			{"bare query marker", "https://example.com?"},
			{"fragment", "https://example.com#frag"},
			{"userinfo", "https://user@example.com"},
			{"multiple trailing slashes", "https://example.com///"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, _, err := Load([]string{"-public-url", tc.value})
				require.Error(t, err)
				assert.Contains(t, err.Error(), "public_url")
			})
		}
	})

	t.Run("rejected in solo mode (env)", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB_PUBLIC_URL", "https://hub.example.com")
		_, _, err := LoadWithOptions(nil, LoadOptions{SoloMode: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "solo mode")
	})

	t.Run("rejected in solo mode (YAML)", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "solo.yaml")
		yamlContent := `public_url: "https://hub.example.com"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))
		_, _, err := LoadWithOptions([]string{"-config", configPath}, LoadOptions{SoloMode: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "solo mode")
	})

	t.Run("solo mode allows empty PublicURL", func(t *testing.T) {
		cfg, _, err := LoadWithOptions(nil, LoadOptions{SoloMode: true})
		require.NoError(t, err)
		assert.Empty(t, cfg.PublicURL)
	})
}

func TestBaseURL(t *testing.T) {
	t.Run("derived from listen + http when PublicURL empty", func(t *testing.T) {
		cfg := &Config{Listen: ":4327"}
		assert.Equal(t, "http://localhost:4327", cfg.BaseURL())
	})

	t.Run("derived from listen + https when SecureCookies set", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", SecureCookies: true}
		assert.Equal(t, "https://localhost:4327", cfg.BaseURL())
	})

	t.Run("PublicURL wins over derivation", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", PublicURL: "https://hub.example.com"}
		assert.Equal(t, "https://hub.example.com", cfg.BaseURL())
	})

	t.Run("PublicURL wins even with SecureCookies false", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", PublicURL: "https://hub.example.com", SecureCookies: false}
		assert.Equal(t, "https://hub.example.com", cfg.BaseURL())
	})
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

	t.Run("max_message_size zero is allowed", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxMessageSize: 0}
		require.NoError(t, cfg.Validate())
	})

	t.Run("max_message_size below floor is rejected", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxMessageSize: 1}
		assert.Error(t, cfg.Validate())
	})

	t.Run("max_message_size above ceiling is rejected", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxMessageSize: channelwire.MaxConfigurableMessageSize + 1}
		assert.Error(t, cfg.Validate())
	})

	t.Run("queue memory budgets zero mean auto-size", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir()}
		require.NoError(t, cfg.Validate())
	})

	t.Run("a queue memory budget too small for its class is rejected", func(t *testing.T) {
		// Two floors, and the larger binds. A budget under one guaranteed
		// per-connection floor cannot serve even one connection: the floor is
		// granted anyway and every enqueue counts as an overcommit, which is a
		// silently degraded Hub rather than a small one. A budget under one
		// LARGEST FRAME of its class is worse than degraded -- a frame bigger
		// than the whole budget can never be admitted at any occupancy, so that
		// class of connection simply never works on this deployment.
		//
		// Checked for EVERY pool: validating only the first would let the
		// others be misconfigured silently.
		for _, tc := range []struct {
			key   string
			class queueClass
			set   func(*Config, int64)
		}{
			{"relay_queue_memory_budget", (&Config{}).relayQueueClass(),
				func(c *Config, v int64) { c.RelayQueueMemoryBudget = v }},
			{"worker_queue_memory_budget", (&Config{}).workerQueueClass(),
				func(c *Config, v int64) { c.WorkerQueueMemoryBudget = v }},
			{"userevents_queue_memory_budget", (&Config{}).userEventsQueueClass(),
				func(c *Config, v int64) { c.UserEventsQueueMemoryBudget = v }},
		} {
			minimum := tc.class.minimum()

			cfg := &Config{Listen: ":4327", DataDir: t.TempDir()}
			tc.set(cfg, minimum-1)
			err := cfg.Validate()
			require.Error(t, err, tc.key)
			assert.Contains(t, err.Error(), tc.key)

			cfg2 := &Config{Listen: ":4327", DataDir: t.TempDir()}
			tc.set(cfg2, minimum)
			assert.NoError(t, cfg2.Validate(), "%s at exactly its minimum is allowed", tc.key)

		}
	})

	// A budget at or under ONE guaranteed floor pins the pool's threshold at
	// that floor for every occupancy: the dynamic branch never engages, and
	// total resident becomes members x floor with nothing bounding it -- the
	// exact shape the pools exist to remove. It used to pass validation.
	t.Run("a budget too small for the admission rule to engage is rejected", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(),
			RelayQueueMemoryBudget: sendq.DefaultMinFloor}
		err := cfg.Validate()
		require.Error(t, err, "one floor of budget disables the dynamic threshold entirely")
		assert.Contains(t, err.Error(), "relay_queue_memory_budget")

		// And the accepted minimum really does leave the rule room to work: at
		// MinQueueMembersAtFloor members the fair share is still a whole floor.
		ok := &Config{Listen: ":4327", DataDir: t.TempDir(),
			RelayQueueMemoryBudget: MinQueueMembersAtFloor * sendq.DefaultMinFloor}
		require.NoError(t, ok.Validate())
	})

	// The Hub is a RELAY on this path: it forwards ciphertext chunks read off a
	// socket capped at WSReadLimit and never holds a reassembled application
	// payload, so max_message_size -- which bounds what the two ENDPOINTS
	// rebuild -- must not move the worker budget's minimum. Deriving it from
	// that knob demanded a 64.25 MiB worker budget at max_message_size = 64 MiB
	// and refused a workable smaller one at startup, for a frame that cannot
	// exist.
	t.Run("max_message_size does not move the worker budget minimum", func(t *testing.T) {
		base := (&Config{}).workerQueueClass().maxFrame
		for _, size := range []int{0, 32 << 20, channelwire.MaxConfigurableMessageSize} {
			cfg := &Config{MaxMessageSize: size}
			assert.Equal(t, base, cfg.workerQueueClass().maxFrame,
				"the Hub never queues a whole application payload for a worker")
		}

		// Concretely: a budget that was refused before is accepted now.
		large := &Config{Listen: ":4327", DataDir: t.TempDir(),
			WorkerQueueMemoryBudget: 20 << 20, MaxMessageSize: channelwire.MaxConfigurableMessageSize}
		require.NoError(t, large.Validate())
	})

	// Every class's minimum has to cover the frame AS CHARGED, not as a payload:
	// the pool tests Config.Size plus Config.FrameOverhead against
	// `capacity - reserve`, so a bound that covered only the payload left the
	// largest frame unfittable at any occupancy -- worker fenced on every one.
	t.Run("a class minimum covers the frame as the pool charges it", func(t *testing.T) {
		relay := (&Config{}).relayQueueClass()
		assert.Greater(t, relay.maxFrame, int64(channelwire.WSReadLimit),
			"the payload alone is not what the pool admits")

		worker := (&Config{}).workerQueueClass()
		assert.Greater(t, worker.maxFrame, int64(channelwire.WSReadLimit)+sendq.DefaultControlReserve,
			"the control reserve is headroom the data path never sees, so it is on top")

		// Equality, not Greater: the ONLY thing that makes the user-events bound
		// right is the exact crdt.ResidentFactor the queue charges by. A
		// Greater bound is satisfied by any factor over 1.0 and would let the
		// two drift apart silently, which is the drift this class's own comment
		// says deriving from crdt's constant exists to prevent.
		userEvents := (&Config{}).userEventsQueueClass()
		assert.Equal(t,
			int64(channelwire.UserEventsReadLimit+queueFrameEnvelopeHeadroom)*crdt.ResidentFactor,
			userEvents.maxFrame,
			"the user-events bound must be exactly what subscriberQueue charges: wire size times ResidentFactor")
	})

	// minimum() is a max over two terms, and neither may be the one that always
	// wins -- a max whose other argument can never bind is not a max. Pinned
	// from the INPUT side, because asserting the output is >= each input only
	// restates max().
	t.Run("both terms of a class minimum can bind", func(t *testing.T) {
		floorTerm := int64(MinQueueMembersAtFloor) * sendq.DefaultMinFloor

		relay := (&Config{}).relayQueueClass()
		assert.Less(t, relay.maxFrame, floorTerm,
			"a relay frame is tiny, so the guaranteed working sets are what bound this class")
		assert.Equal(t, floorTerm, relay.minimum())

		userEvents := (&Config{}).userEventsQueueClass()
		assert.Greater(t, userEvents.maxFrame, floorTerm,
			"a bootstrap frame is a whole account snapshot, so the frame is what bounds this class")
		assert.Equal(t, userEvents.maxFrame, userEvents.minimum())
	})

	t.Run("invalid PublicURL caught at Validate", func(t *testing.T) {
		// Programmatic construction bypasses LoadWithOptions canonicalization.
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), PublicURL: "ftp://example.com"}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "public_url")
	})

	t.Run("PublicURL canonicalized at Validate", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), PublicURL: "https://hub.example.com/"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, "https://hub.example.com", cfg.PublicURL)
	})

	t.Run("PublicURL rejected when SoloMode at Validate", func(t *testing.T) {
		cfg := &Config{
			Listen:    ":4327",
			DataDir:   t.TempDir(),
			SoloMode:  true,
			PublicURL: "https://hub.example.com",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "solo mode")
	})

	t.Run("empty SmtpTLSMode is normalized to starttls", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir()}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, SmtpTLSModeSTARTTLS, cfg.SmtpTLSMode)
	})

	t.Run("invalid SmtpTLSMode is rejected even without SmtpHost", func(t *testing.T) {
		// Validating unconditionally — not gated on SmtpHost — surfaces typos
		// at startup instead of waiting until someone configures smtp_host.
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), SmtpTLSMode: "bogus"}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "smtp_tls_mode")
		assert.Contains(t, err.Error(), validSmtpTLSModes)
	})

	t.Run("SMTP block rejection cases", func(t *testing.T) {
		// Valid baseline; each subtest mutates a single field. Pulling
		// the boilerplate up here is the point of the table — it forces
		// each row to highlight only the field that triggers rejection.
		base := func() *Config {
			return &Config{
				Listen:          ":4327",
				DataDir:         t.TempDir(),
				SmtpHost:        "smtp.example.com",
				SmtpPort:        587,
				SmtpFromAddress: "hub@example.test",
				SmtpTLSMode:     SmtpTLSModeSTARTTLS,
			}
		}
		cases := []struct {
			name     string
			mutate   func(*Config)
			contains string
		}{
			{"missing from address", func(c *Config) { c.SmtpFromAddress = "" }, "smtp_from_address is required"},
			{"malformed from address", func(c *Config) { c.SmtpFromAddress = "not-an-email" }, "invalid smtp_from_address"},
			{"out-of-range port", func(c *Config) { c.SmtpPort = 0 }, "smtp_port"},
			{"verification required without host", func(c *Config) {
				*c = Config{Listen: ":4327", DataDir: t.TempDir(), EmailVerificationRequired: true}
			}, "smtp_host is required"},
			{"tls=none + auth on non-localhost", func(c *Config) {
				c.SmtpTLSMode = SmtpTLSModeNone
				c.SmtpPort = 25
				c.SmtpUsername = "user"
				c.SmtpPassword = "pw"
			}, "smtp_tls_mode=none"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cfg := base()
				tc.mutate(cfg)
				err := cfg.Validate()
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.contains)
			})
		}
	})

	t.Run("tls_mode=none + auth + localhost is accepted", func(t *testing.T) {
		// Trusted local relay: PlainAuth permits credentials on loopback even
		// without TLS, so the validation rule must not over-reach.
		// Includes 127.0.0.2 because Go treats the entire 127.0.0.0/8
		// range as loopback — verifying we use IsLoopback rather than
		// hard-coding the canonical addresses.
		for _, host := range []string{"localhost", "LOCALHOST", "127.0.0.1", "127.0.0.2", "::1", "[::1]"} {
			cfg := &Config{
				Listen:          ":4327",
				DataDir:         t.TempDir(),
				SmtpHost:        host,
				SmtpPort:        25,
				SmtpUsername:    "user",
				SmtpPassword:    "pw",
				SmtpFromAddress: "hub@example.test",
				SmtpTLSMode:     SmtpTLSModeNone,
			}
			require.NoError(t, cfg.Validate(), "host=%s", host)
		}
	})

	t.Run("tls_mode=none + no auth on non-localhost is accepted", func(t *testing.T) {
		// No credentials means PlainAuth's localhost-only restriction doesn't
		// apply; an unauthenticated relay over plaintext is admin's choice.
		cfg := &Config{
			Listen:          ":4327",
			DataDir:         t.TempDir(),
			SmtpHost:        "relay.example.com",
			SmtpPort:        25,
			SmtpFromAddress: "hub@example.test",
			SmtpTLSMode:     SmtpTLSModeNone,
		}
		require.NoError(t, cfg.Validate())
	})

	t.Run("valid SMTP block is accepted", func(t *testing.T) {
		cfg := &Config{
			Listen:                    ":4327",
			DataDir:                   t.TempDir(),
			SmtpHost:                  "smtp.example.com",
			SmtpPort:                  587,
			SmtpUsername:              "user",
			SmtpPassword:              "pw",
			SmtpFromAddress:           "hub@example.test",
			SmtpTLSMode:               SmtpTLSModeSTARTTLS,
			EmailVerificationRequired: true,
		}
		require.NoError(t, cfg.Validate())
	})
}

func TestPaths(t *testing.T) {
	cfg := &Config{DataDir: "/test/dir"}
	assert.Equal(t, filepath.Join("/test/dir", "hub.db"), cfg.SQLiteDBPath(), "defaults to DataDir/hub.db")

	cfg.Storage.SQLite.Path = "/custom/path.db"
	assert.Equal(t, "/custom/path.db", cfg.SQLiteDBPath(), "uses explicit SQLite path")
}

func TestResolveQueueMemoryBudgets(t *testing.T) {
	// The basis every subtest that reasons about a share states explicitly. On a
	// developer's 32 GiB laptop every share clears its minimum by two orders of
	// magnitude, so a probed basis would leave the clamps untested.
	//
	// A VALUE, not the process's memory limit: the resolvers take the basis as
	// an argument, so a test says which machine it means instead of installing
	// one globally and restoring it -- which is what used to keep this whole
	// package from running in parallel.
	basisOf := func(bytes int64) memlimit.Basis {
		return memlimit.Basis{Bytes: bytes, Source: memlimit.SourceGoMemLimit}
	}

	t.Run("an explicit value wins outright", func(t *testing.T) {
		// A basis nothing should reach for: every budget below is configured.
		basis := basisOf(8 << 30)
		cfg := &Config{
			RelayQueueMemoryBudget:      123 << 20,
			WorkerQueueMemoryBudget:     77 << 20,
			UserEventsQueueMemoryBudget: 55 << 20,
		}

		relay := cfg.ResolveRelayQueueMemoryBudget(basis)
		assert.Equal(t, int64(123<<20), relay.Capacity)
		assert.Contains(t, relay.Source, "source=config",
			"an operator reading the startup log must be able to tell a configured budget from a guessed one")

		assert.Equal(t, int64(77<<20), cfg.ResolveWorkerQueueMemoryBudget(basis).Capacity)
		assert.Equal(t, int64(55<<20), cfg.ResolveUserEventsQueueMemoryBudget(basis).Capacity)
	})

	// Only Capacity and Source may depend on whether a budget was configured.
	// Everything else is the CLASS's, and identical either way -- so this pins
	// the one thing the shape of resolve can get wrong: a field set on one arm
	// and left at its zero value on the other, which no capacity assertion sees
	// (a zero MaxFrame silently disables the unfittable-frame check, a zero Name
	// unlabels the pool's whole metric family).
	t.Run("a configured budget carries the same class fields as an auto-sized one", func(t *testing.T) {
		basis := basisOf(8 << 30)
		auto := &Config{}
		configured := &Config{
			RelayQueueMemoryBudget:      321 << 20,
			WorkerQueueMemoryBudget:     222 << 20,
			UserEventsQueueMemoryBudget: 111 << 20,
		}
		for _, tc := range []struct {
			name           string
			autoB, configB QueueMemoryBudget
		}{
			{"relay", auto.ResolveRelayQueueMemoryBudget(basis), configured.ResolveRelayQueueMemoryBudget(basis)},
			{"worker", auto.ResolveWorkerQueueMemoryBudget(basis), configured.ResolveWorkerQueueMemoryBudget(basis)},
			{"userevents", auto.ResolveUserEventsQueueMemoryBudget(basis), configured.ResolveUserEventsQueueMemoryBudget(basis)},
		} {
			assert.NotEmpty(t, tc.configB.Name, tc.name)
			assert.Positive(t, tc.configB.MaxFrame, tc.name)
			assert.Equal(t, tc.autoB.Name, tc.configB.Name, tc.name)
			assert.Equal(t, tc.autoB.Reclaim, tc.configB.Reclaim, tc.name)
			assert.Equal(t, tc.autoB.MaxFrame, tc.configB.MaxFrame, tc.name)
			assert.NotEqual(t, tc.autoB.Capacity, tc.configB.Capacity,
				"%s: the configured capacity must be the one that differs", tc.name)
		}
	})

	t.Run("each pool is resolved independently", func(t *testing.T) {
		// Setting one must not silently move the others -- the point of separate
		// budgets is that a deployment with many workers and few tabs can raise
		// the one that binds.
		//
		// Pinned, like every other subtest that reasons about a share, and then
		// compared against the same classes resolved from an UNCONFIGURED
		// Config. Asserting merely that they differ from the configured number
		// read the host: on a machine whose basis is exactly 8 GiB the
		// user-events share IS 512 MiB (8 GiB/32*2), and on a 4 GiB one so is
		// the relay share -- both everyday sizes, so the test failed on hardware
		// rather than on a defect. Comparing to the auto-sized value is also the
		// stronger claim: they must be UNCHANGED, not merely different.
		basis := basisOf(6 << 30)
		cfg := &Config{WorkerQueueMemoryBudget: 512 << 20}
		auto := &Config{}

		assert.Equal(t, int64(512<<20), cfg.ResolveWorkerQueueMemoryBudget(basis).Capacity)
		assert.Equal(t, auto.ResolveRelayQueueMemoryBudget(basis).Capacity,
			cfg.ResolveRelayQueueMemoryBudget(basis).Capacity,
			"the relay budget must still auto-size, exactly as if nothing were configured")
		assert.Equal(t, auto.ResolveUserEventsQueueMemoryBudget(basis).Capacity,
			cfg.ResolveUserEventsQueueMemoryBudget(basis).Capacity,
			"and so must the user-events budget")
	})

	t.Run("auto-sizing stays inside its clamps and names its share", func(t *testing.T) {
		basis := basisOf(8 << 30)
		cfg := &Config{}
		for _, tc := range []struct {
			name    string
			class   queueClass
			resolve func(memlimit.Basis) QueueMemoryBudget
		}{
			{"relay", cfg.relayQueueClass(), cfg.ResolveRelayQueueMemoryBudget},
			{"worker", cfg.workerQueueClass(), cfg.ResolveWorkerQueueMemoryBudget},
			{"userevents", cfg.userEventsQueueClass(), cfg.ResolveUserEventsQueueMemoryBudget},
		} {
			b := tc.resolve(basis)
			assert.GreaterOrEqual(t, b.Capacity, tc.class.minimum(), tc.name)
			assert.LessOrEqual(t, b.Capacity, int64(MaxQueueMemoryBudget), tc.name)
			assert.Contains(t, b.Source, "source=auto", tc.name)
			assert.Contains(t, b.Source, fmt.Sprintf("/%d of ", QueueMemoryShareDenominator),
				"the log must name the share exactly, not as a rounded reciprocal")
		}
	})

	// One number, three shares -- now by construction rather than by memo: the
	// resolvers cannot observe a basis of their own, so there is no second
	// reading for them to disagree with. The arithmetic is checked against the
	// same value the startup log states once beside the three budgets.
	t.Run("the three shares divide ONE number", func(t *testing.T) {
		basis := basisOf(8 << 30)
		cfg := &Config{}
		relay := cfg.ResolveRelayQueueMemoryBudget(basis)
		worker := cfg.ResolveWorkerQueueMemoryBudget(basis)
		userEvents := cfg.ResolveUserEventsQueueMemoryBudget(basis)

		assert.Equal(t, basis.Bytes/QueueMemoryShareDenominator*RelayQueueMemoryShare, relay.Capacity)
		assert.Equal(t, basis.Bytes/QueueMemoryShareDenominator*WorkerQueueMemoryShare, worker.Capacity)
		assert.Equal(t, basis.Bytes/QueueMemoryShareDenominator*UserEventsQueueMemoryShare, userEvents.Capacity)
		for _, b := range []QueueMemoryBudget{relay, worker, userEvents} {
			assert.Contains(t, b.Source, "of basis",
				"every budget must refer to the one basis the startup log states beside them")
		}
	})

	// The basis is the PROCESS's, probed once, so a budget that renders it makes
	// the startup log repeat it once per budget. On a host whose cgroup probe
	// failed that repetition was the whole diagnosis, printed three times inside
	// one log line -- see hub.logQueueMemoryBudgets, which states the basis once
	// and warns about the failure on a record of its own.
	t.Run("a budget names the basis without rendering it", func(t *testing.T) {
		basis := memlimit.Basis{
			Bytes:     8 << 30,
			Source:    memlimit.SourcePhysical,
			CgroupErr: errors.New("open /custom/inner/memory.max: permission denied"),
		}
		require.Contains(t, basis.String(), "permission denied",
			"the basis itself must still be able to render its failure standalone")

		cfg := &Config{}
		for _, tc := range []struct {
			name  string
			class queueClass
		}{
			{"relay", cfg.relayQueueClass()},
			{"worker", cfg.workerQueueClass()},
			{"userevents", cfg.userEventsQueueClass()},
		} {
			source := tc.class.resolve(basis).Source
			assert.NotContains(t, source, "permission denied",
				"%s: a probe failure logged once per budget is the same diagnosis three times", tc.name)
			assert.NotContains(t, source, basis.Figure(),
				"%s: the basis is stated once beside the budgets, not inside each of them", tc.name)
			assert.Contains(t, source, "of basis",
				"%s: but the budget must still say what it took a share OF", tc.name)
		}
	})

	// "Why is this budget this number?" has to be answerable from the log line
	// whichever way the number was set, so both arms open with the same token.
	t.Run("every budget says whether it was configured or auto-sized", func(t *testing.T) {
		basis := memlimit.Basis{Bytes: 8 << 30, Source: memlimit.SourceGoMemLimit}
		auto := &Config{}
		configured := &Config{
			RelayQueueMemoryBudget:      321 << 20,
			WorkerQueueMemoryBudget:     222 << 20,
			UserEventsQueueMemoryBudget: 111 << 20,
		}
		for _, tc := range []struct {
			name        string
			autoClass   queueClass
			configClass queueClass
			share       int
		}{
			{"relay", auto.relayQueueClass(), configured.relayQueueClass(), RelayQueueMemoryShare},
			{"worker", auto.workerQueueClass(), configured.workerQueueClass(), WorkerQueueMemoryShare},
			{"userevents", auto.userEventsQueueClass(), configured.userEventsQueueClass(), UserEventsQueueMemoryShare},
		} {
			autoSource := tc.autoClass.resolve(basis).Source
			assert.Contains(t, autoSource, "source=auto", tc.name)
			assert.Contains(t, autoSource,
				fmt.Sprintf("%d/%d of basis", tc.share, QueueMemoryShareDenominator),
				"%s: an operator must be able to derive the figure from the basis beside it", tc.name)
			assert.Contains(t, autoSource, memlimit.HumanBytes(
				basis.Bytes/QueueMemoryShareDenominator*int64(tc.share)),
				"%s: and the figure itself has to be in the line", tc.name)

			configSource := tc.configClass.resolve(basis).Source
			assert.Contains(t, configSource, "source=config", tc.name)
			assert.NotContains(t, configSource, "of basis",
				"%s: a configured budget took no share of anything", tc.name)
		}
	})

	t.Run("relay leads, because it carries the bulk direction", func(t *testing.T) {
		// Terminal output is one frame per PTY read with no coalescing, where a
		// worker stream carries keystrokes, RPCs and control. An inversion here
		// would starve the side that actually queues.
		basis := basisOf(8 << 30)
		cfg := &Config{}
		assert.Greater(t, RelayQueueMemoryShare, WorkerQueueMemoryShare)
		assert.Greater(t, cfg.ResolveRelayQueueMemoryBudget(basis).Capacity,
			cfg.ResolveWorkerQueueMemoryBudget(basis).Capacity)
	})

	// Throughput is NOT what ranks user events. Its steady-state frames are the
	// smallest in the Hub and shared besides, but its worst case is the largest
	// single frame any pool carries -- and unlike a broadcast, a bootstrap is
	// unique per subscriber, so a reconnect storm is N of them at once with no
	// eviction in that pool to clear it.
	t.Run("user events is sized by its largest frame, not its traffic", func(t *testing.T) {
		basis := basisOf(8 << 30)
		cfg := &Config{}
		userEvents := cfg.ResolveUserEventsQueueMemoryBudget(basis)

		assert.Equal(t, WorkerQueueMemoryShare, UserEventsQueueMemoryShare,
			"the class carrying the least traffic still needs room for the biggest frame")
		assert.Greater(t, userEvents.MaxFrame, cfg.relayQueueClass().maxFrame)
		assert.Greater(t, userEvents.MaxFrame, cfg.workerQueueClass().maxFrame)

		// The number that actually matters: how many worst-case opening frames
		// can be in flight before the budget refuses one. A share that left this
		// in single digits made a Hub restart converge through retry backoff.
		assert.Greater(t, userEvents.Capacity/userEvents.MaxFrame, int64(9),
			"a reconnect storm of large accounts must not be shed at single-digit concurrency")
	})

	t.Run("the pools still come to a quarter of the basis between them", func(t *testing.T) {
		// The quarter is the budget for outbound queues as a whole. Adding a
		// pool has to re-slice the quarter, never grow it.
		assert.Equal(t, QueueMemoryShareDenominator/4,
			RelayQueueMemoryShare+WorkerQueueMemoryShare+UserEventsQueueMemoryShare)
		// Every slice user events has gained came out of relay's, leaving the
		// worker pool at the 1/16 it has always had -- so a fleet whose workers
		// were sized correctly never has to be re-tuned because a different
		// class was.
		assert.Equal(t, 16, QueueMemoryShareDenominator/WorkerQueueMemoryShare)
	})

	// The container memlimit exists for. A flat byte floor used to raise two of
	// the three pools here and claim 208 MiB -- 40% of the limit -- on the one
	// deployment where "a generous queue budget IS the OOM".
	t.Run("a small container gets the shares, not a flat floor", func(t *testing.T) {
		// Synthetic basis rather than debug.SetMemoryLimit: resolve takes the
		// basis as a parameter, so the arithmetic can be stated at any machine
		// size without asking the Go runtime to actually live at it.
		basis := memlimit.Basis{Bytes: 512 << 20, Source: memlimit.SourceCgroup}
		cfg := &Config{}
		relay := cfg.relayQueueClass().resolve(basis)
		worker := cfg.workerQueueClass().resolve(basis)
		userEvents := cfg.userEventsQueueClass().resolve(basis)

		total := relay.Capacity + worker.Capacity + userEvents.Capacity
		assert.LessOrEqual(t, total, basis.Bytes/4+userEvents.MaxFrame,
			"the three pools together must not exceed their quarter by more than the one clamp that can bind")
		// The flat 64 MiB floor this replaced took 208 MiB here -- 40% of the
		// container memlimit exists to protect.
		assert.Less(t, total, int64(208<<20))

		// Each is still usable: never below one largest frame of its class.
		for _, b := range []QueueMemoryBudget{relay, worker, userEvents} {
			assert.GreaterOrEqual(t, b.Capacity, b.MaxFrame)
		}
	})

	t.Run("auto-sizing never lands below one largest frame of the class", func(t *testing.T) {
		// A basis small enough that every share is under its class minimum.
		basis := memlimit.Basis{Bytes: 16 << 20, Source: memlimit.SourcePhysical}
		cfg := &Config{}
		for _, tc := range []struct {
			name  string
			class queueClass
		}{
			{"relay", cfg.relayQueueClass()},
			{"worker", cfg.workerQueueClass()},
			{"userevents", cfg.userEventsQueueClass()},
		} {
			b := tc.class.resolve(basis)
			assert.Equal(t, tc.class.minimum(), b.Capacity, tc.name)
			assert.GreaterOrEqual(t, b.Capacity, b.MaxFrame,
				"%s: an auto-sized pool must hold one largest frame of its class", tc.name)
			assert.Contains(t, b.Source, "clamped up to the minimum",
				"%s: and must say so, since the share is no longer what set the number", tc.name)
		}
	})

	t.Run("auto-sizing is capped on a very large host", func(t *testing.T) {
		basis := memlimit.Basis{Bytes: 1 << 40, Source: memlimit.SourcePhysical}
		b := (&Config{}).relayQueueClass().resolve(basis)
		assert.Equal(t, int64(MaxQueueMemoryBudget), b.Capacity)
		assert.Contains(t, b.Source, "clamped down to the maximum")
	})

	// The class's frame size has to REACH sendq, or the pool is built with
	// generic defaults: a merely-full user-events pool refused every 16 MiB
	// bootstrap against a 4 MiB floor, and only large accounts were locked out.
	t.Run("the pool config carries one largest frame as the guaranteed floor", func(t *testing.T) {
		basis := basisOf(8 << 30)
		cfg := &Config{}
		for _, tc := range []struct {
			name    string
			resolve func(memlimit.Basis) QueueMemoryBudget
		}{
			{"relay", cfg.ResolveRelayQueueMemoryBudget},
			{"worker", cfg.ResolveWorkerQueueMemoryBudget},
			{"userevents", cfg.ResolveUserEventsQueueMemoryBudget},
		} {
			b := tc.resolve(basis)
			pc := b.PoolConfig()
			assert.Equal(t, b.Capacity, pc.Capacity, tc.name)
			assert.GreaterOrEqual(t, pc.MaxFloor, b.MaxFrame,
				"%s: an idle member must be able to place one largest frame", tc.name)
			assert.GreaterOrEqual(t, pc.MaxFloor, sendq.DefaultMaxFloor,
				"%s: and never less than sendq's own default", tc.name)
			assert.Equal(t, sendq.DefaultMinFloor, pc.MinFloor,
				"%s: MinFloor is STATED at sendq's default -- raising it to the class's "+
					"frame would multiply that frame by an unbounded connection count, and "+
					"leaving it implied would decouple it from queueClass.minimum, which "+
					"sizes every budget to MinQueueMembersAtFloor of this same number", tc.name)
		}
	})

	t.Run("a negative value is treated as auto rather than trusted", func(t *testing.T) {
		basis := basisOf(8 << 30)
		cfg := &Config{RelayQueueMemoryBudget: -1, WorkerQueueMemoryBudget: -1, UserEventsQueueMemoryBudget: -1}
		for _, b := range []QueueMemoryBudget{
			cfg.ResolveRelayQueueMemoryBudget(basis),
			cfg.ResolveWorkerQueueMemoryBudget(basis),
			cfg.ResolveUserEventsQueueMemoryBudget(basis),
		} {
			assert.Positive(t, b.Capacity,
				"a nonsense budget must not reach sendq.NewPool, which panics on one")
			assert.GreaterOrEqual(t, b.Capacity, b.MaxFrame)
		}
	})
}

// The per-user connection cap: one number, three ways to set it, and a
// validation floor that keeps it a cap rather than an outage.
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

// The worker pool's count term. Workers take no lease, so the connection cap
// cannot see them; this is what keeps that pool's guaranteed floors from summing
// without limit.
func TestMaxWorkersPerUser(t *testing.T) {
	t.Run("defaults to the built-in cap", func(t *testing.T) {
		cfg, _, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, int64(DefaultMaxWorkersPerUser), cfg.MaxWorkersPerUser,
			"an operator who sets nothing must still get the guard")
	})

	// Three independent wiring mistakes, one subtest each: a missing fieldMap
	// entry, a koanf tag that does not match the key, and an env-name mismatch
	// all present as "the value I set was ignored".
	t.Run("is settable from the command line", func(t *testing.T) {
		cfg, _, err := Load([]string{"-max-workers-per-user", "3"})
		require.NoError(t, err)
		assert.Equal(t, int64(3), cfg.MaxWorkersPerUser)
	})

	t.Run("is settable from the config file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "hub.yaml")
		require.NoError(t, os.WriteFile(path, []byte("max_workers_per_user: 3\n"), 0o644))
		cfg, _, err := Load([]string{"-config", path})
		require.NoError(t, err)
		assert.Equal(t, int64(3), cfg.MaxWorkersPerUser)
	})

	t.Run("is settable from the environment", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB_MAX_WORKERS_PER_USER", "3")
		cfg, _, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, int64(3), cfg.MaxWorkersPerUser)
	})

	t.Run("zero is unlimited and allowed", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxWorkersPerUser: 0}
		assert.NoError(t, cfg.Validate())
	})

	// One Worker is a working deployment, unlike a browser tab which needs two
	// connections before it works at all -- so there is no lower bound above
	// one, only a rejection of a value nobody meant.
	t.Run("a negative is refused, naming the key", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxWorkersPerUser: -1}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max_workers_per_user")
	})
}

func TestMaxConnectionsPerUser(t *testing.T) {
	t.Run("defaults to the built-in cap", func(t *testing.T) {
		cfg, _, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, int64(DefaultMaxConnectionsPerUser), cfg.MaxConnectionsPerUser,
			"an operator who sets nothing must still get the guard")
	})

	// Three independent wiring mistakes, one subtest each: a missing fieldMap
	// entry, a koanf tag that does not match the key, and an env-name mismatch
	// all present as "the value I set was ignored".
	t.Run("is settable from the command line", func(t *testing.T) {
		cfg, _, err := Load([]string{"-max-connections-per-user", "8"})
		require.NoError(t, err)
		assert.Equal(t, int64(8), cfg.MaxConnectionsPerUser)
	})

	t.Run("is settable from the config file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hub.yaml")
		require.NoError(t, os.WriteFile(path, []byte("max_connections_per_user: 8\n"), 0o644))
		cfg, _, err := Load([]string{"-config", path})
		require.NoError(t, err)
		assert.Equal(t, int64(8), cfg.MaxConnectionsPerUser)
	})

	t.Run("is settable from the environment", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB_MAX_CONNECTIONS_PER_USER", "8")
		cfg, _, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, int64(8), cfg.MaxConnectionsPerUser)
	})

	t.Run("zero is unlimited and allowed", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxConnectionsPerUser: 0}
		assert.NoError(t, cfg.Validate(),
			"an operator must be able to turn the cap off outright")
	})

	// A browser tab holds TWO connections -- /ws/userevents always, /ws/channel
	// once it opens a channel -- so a cap of 1 refuses a user's first tab and a
	// cap of 2 serves that tab and nothing else: no second tab, no desktop
	// sidecar, no CLI remote session. That is an outage wearing a cap's
	// clothes, and it fails at startup naming the key rather than as a mystery
	// at runtime.
	t.Run("rejects a cap too small to serve one tab", func(t *testing.T) {
		for _, tooSmall := range []int64{1, 2, MinConnectionsPerUser - 1} {
			cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxConnectionsPerUser: tooSmall}
			err := cfg.Validate()
			require.Error(t, err, "cap %d must be rejected", tooSmall)
			assert.Contains(t, err.Error(), "max_connections_per_user")
		}

		ok := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxConnectionsPerUser: MinConnectionsPerUser}
		assert.NoError(t, ok.Validate(), "the minimum itself must be allowed")
	})

	t.Run("rejects a negative rather than reading it as unlimited", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), MaxConnectionsPerUser: -1}
		err := cfg.Validate()
		require.Error(t, err, "a sign typo must not silently disable the cap")
		assert.Contains(t, err.Error(), "max_connections_per_user")
	})
}

func TestSessionDuration(t *testing.T) {
	t.Run("defaults to the built-in duration", func(t *testing.T) {
		cfg, _, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, auth.DefaultSessionDuration, cfg.SessionDuration(),
			"an operator who sets nothing must get the documented lifetime")
	})

	// The same three wiring mistakes each other settable key is pinned against:
	// a missing fieldMap entry, a koanf tag that does not match the key, and an
	// env-name mismatch all present as "the value I set was ignored".
	t.Run("is settable from the command line", func(t *testing.T) {
		cfg, _, err := Load([]string{"-session-duration-seconds", "3600"})
		require.NoError(t, err)
		assert.Equal(t, time.Hour, cfg.SessionDuration())
	})

	t.Run("is settable from the config file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hub.yaml")
		require.NoError(t, os.WriteFile(path, []byte("session_duration_seconds: 3600\n"), 0o644))
		cfg, _, err := Load([]string{"-config", path})
		require.NoError(t, err)
		assert.Equal(t, time.Hour, cfg.SessionDuration())
	})

	t.Run("is settable from the environment", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB_SESSION_DURATION_SECONDS", "3600")
		cfg, _, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, time.Hour, cfg.SessionDuration())
	})

	t.Run("zero reads as the default", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), SessionDurationSeconds: 0}
		assert.NoError(t, cfg.Validate())
		assert.Equal(t, auth.DefaultSessionDuration, cfg.SessionDuration())
	})

	// Below the floor the Hub cannot honestly enforce the value it was given:
	// a validated session stays cached in memory for 30 seconds, so a session
	// of a few seconds is served well past its own expiry. That fails at
	// startup naming the key rather than as a mystery at runtime.
	t.Run("rejects a duration under the floor", func(t *testing.T) {
		for _, tooShort := range []int{1, 30, MinSessionDurationSeconds - 1} {
			cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), SessionDurationSeconds: tooShort}
			err := cfg.Validate()
			require.Error(t, err, "duration %d must be rejected", tooShort)
			assert.Contains(t, err.Error(), "session_duration_seconds")
		}

		ok := &Config{Listen: ":4327", DataDir: t.TempDir(), SessionDurationSeconds: MinSessionDurationSeconds}
		assert.NoError(t, ok.Validate(), "the minimum itself must be allowed")
	})

	t.Run("rejects a negative rather than reading it as the default", func(t *testing.T) {
		cfg := &Config{Listen: ":4327", DataDir: t.TempDir(), SessionDurationSeconds: -1}
		err := cfg.Validate()
		require.Error(t, err, "a sign typo must not silently restore the default")
		assert.Contains(t, err.Error(), "session_duration_seconds")
	})
}
