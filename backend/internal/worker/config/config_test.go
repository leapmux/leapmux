package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/cli/clitest"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	t.Run("defaults only", func(t *testing.T) {
		cfg, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".config/leapmux/worker"), cfg.DataDir)
		assert.Equal(t, sqlitedb.DefaultMaxConns, cfg.DBMaxConns)
		assert.Equal(t, "info", cfg.LogLevel)
	})

	t.Run("config file overrides defaults", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "worker.yaml")
		yamlContent := `name: "from-file"
db_max_conns: 8
log_level: "warn"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, err := Load([]string{"--config", configPath})
		require.NoError(t, err)
		assert.Equal(t, "from-file", cfg.Name)
		assert.Equal(t, 8, cfg.DBMaxConns)
		assert.Equal(t, "warn", cfg.LogLevel)
		assert.Equal(t, tmpDir, cfg.DataDir)
	})

	t.Run("env vars override config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "worker.yaml")
		yamlContent := `name: "from-file"
log_level: "warn"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		t.Setenv("LEAPMUX_WORKER_NAME", "from-env")

		cfg, err := Load([]string{"--config", configPath})
		require.NoError(t, err)
		assert.Equal(t, "from-env", cfg.Name)
		assert.Equal(t, "warn", cfg.LogLevel) // from config file
	})

	t.Run("CLI flags override env vars", func(t *testing.T) {
		t.Setenv("LEAPMUX_WORKER_NAME", "from-env")

		cfg, err := Load([]string{"--name", "from-flag"})
		require.NoError(t, err)
		assert.Equal(t, "from-flag", cfg.Name)
	})

	// The Hub URL and the registration key belong to `leapmux worker register`,
	// which takes both as its own flags. The shared loader defines neither.
	for _, name := range []string{"hub", "registration-key"} {
		t.Run("rejects the registration flag "+name, func(t *testing.T) {
			_, err := Load([]string{"--" + name, "value"})
			require.ErrorContains(t, err, "unknown flag: --"+name)
		})
	}

	// The root command alone prints the version: a loader takes no --version
	// of its own.
	t.Run("version flag is not a flag of the worker", func(t *testing.T) {
		_, err := Load([]string{"--version"})
		require.ErrorContains(t, err, "unknown flag: --version")
	})

	t.Run("missing config file silently ignored", func(t *testing.T) {
		cfg, err := Load([]string{"--config", "/nonexistent/worker.yaml"})
		require.NoError(t, err)
		assert.Equal(t, "info", cfg.LogLevel)
	})

	t.Run("invalid YAML returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "worker.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte("invalid: [yaml: broken"), 0o644))

		_, err := Load([]string{"--config", configPath})
		assert.Error(t, err)
	})

	t.Run("custom config file with custom data dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		dataDir := filepath.Join(tmpDir, "mydata")
		configPath := filepath.Join(tmpDir, "worker.yaml")
		// Use YAML single quotes: they're literal and don't interpret
		// backslash escapes, which matters on Windows where dataDir is
		// something like `C:\Users\...` and `\U` triggers a YAML error in
		// double-quoted strings.
		yamlContent := "data_dir: '" + dataDir + "'\n"
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, err := Load([]string{"--config", configPath})
		require.NoError(t, err)
		assert.Equal(t, dataDir, cfg.DataDir)
	})

	t.Run("relative data dir resolved against config file dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "worker.yaml")
		yamlContent := `data_dir: "subdir"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, err := Load([]string{"--config", configPath})
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(tmpDir, "subdir"), cfg.DataDir)
	})

	t.Run("data dir from CLI flag", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg, err := Load([]string{"--data-dir", tmpDir})
		require.NoError(t, err)
		assert.Equal(t, tmpDir, cfg.DataDir)
	})
}

// The Worker's two timeouts take the same spellings as the Hub's, from every
// source. They are registered separately from the Hub's, so nothing but a test
// keeps the two flag sets speaking the same language.
func TestTimeoutDurations(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		cfg, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, DefaultAgentStartupTimeout, cfg.AgentStartupTimeout)
		assert.Equal(t, DefaultAPITimeout, cfg.APITimeout)
	})

	t.Run("an explicit zero means the default", func(t *testing.T) {
		cfg, err := Load([]string{"--agent-startup-timeout", "0", "--api-timeout", "0"})
		require.NoError(t, err)
		assert.Equal(t, DefaultAgentStartupTimeout, cfg.AgentStartupTimeout,
			"a zero timeout would make every agent handshake fail at once")
		assert.Equal(t, DefaultAPITimeout, cfg.APITimeout)
	})

	for _, c := range []struct {
		text string
		want time.Duration
	}{
		// A bare number is the seconds count this key took before it had units.
		{"600", 10 * time.Minute},
		{"90s", 90 * time.Second},
		{"10m", 10 * time.Minute},
		{"2h", 2 * time.Hour},
		{"1d", 24 * time.Hour},
	} {
		t.Run("flag "+c.text, func(t *testing.T) {
			cfg, err := Load([]string{"--agent-startup-timeout", c.text})
			require.NoError(t, err)
			assert.Equal(t, c.want, cfg.AgentStartupTimeout)
		})

		t.Run("config file "+c.text, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worker.yaml")
			require.NoError(t, os.WriteFile(path, []byte("agent_startup_timeout: "+c.text+"\n"), 0o644))
			cfg, err := Load([]string{"--config", path})
			require.NoError(t, err)
			assert.Equal(t, c.want, cfg.AgentStartupTimeout)
		})

		t.Run("env "+c.text, func(t *testing.T) {
			t.Setenv("LEAPMUX_WORKER_AGENT_STARTUP_TIMEOUT", c.text)
			cfg, err := Load(nil)
			require.NoError(t, err)
			assert.Equal(t, c.want, cfg.AgentStartupTimeout)
		})
	}

	t.Run("rejects a value that is not a duration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worker.yaml")
		require.NoError(t, os.WriteFile(path, []byte("api_timeout: 30x\n"), 0o644))
		_, err := Load([]string{"--config", path})
		require.Error(t, err, "a typed unit must fail at startup, not read as zero")
	})

	t.Run("rejects a value past the representable range", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worker.yaml")
		require.NoError(t, os.WriteFile(path, []byte("api_timeout: 18446744075\n"), 0o644))
		_, err := Load([]string{"--config", path})
		require.Error(t, err, "an out-of-range value must fail rather than wrap")
	})
}

func TestLoadRejectsPositionalArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"bare help word", []string{"help"}},
		{"trailing positional after flag", []string{"--name", "w1", "garbage"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unexpected argument")
		})
	}
}

func TestWorkerHelpGroupsOptions(t *testing.T) {
	output := flagsHelp(t, LoadOptions{})

	sections := []string{
		"\nCommon options:\n",
		"\nWorker options:\n",
		"\nTimeout and limit options:\n",
		"\nSQLite database options:\n",
	}
	for _, section := range sections {
		require.Contains(t, output, section)
	}
	for i := 1; i < len(sections); i++ {
		assert.Less(t, strings.Index(output, sections[i-1]), strings.Index(output, sections[i]))
	}
	assert.Contains(t, output, "\nCommon options:\n      --config string")
	// Pinning a specific lead flag would make adding a new option in front of
	// it a noisy churn. Assert that the section header is followed by *some*
	// flag instead.
	assert.Contains(t, output, "\nWorker options:\n      --")
	assert.Contains(t, output, "\nTimeout and limit options:\n      --")
	assert.Contains(t, output, "\nSQLite database options:\n      --")

	// Then pin each flag to its SECTION, which "the output contains this flag
	// somewhere" does not. A flag whose workerFlagGroups entry is dropped or
	// mistyped still prints -- the help lists a flag of no group first, under
	// no header -- so a bare Contains passes while `leapmux worker run --help`
	// groups the flag under the wrong header. Reading the section back is
	// lead-flag independent, so it survives a new option ahead of any of
	// these.
	for flagLine, section := range map[string]string{
		"--config string":                  "Common options",
		"--data-dir string":                "Worker options",
		"--name string":                    "Worker options",
		"--agent-startup-timeout duration": "Timeout and limit options",
		"--agent-startup-concurrency int":  "Timeout and limit options",
		"--api-timeout duration":           "Timeout and limit options",
		"--db-cache-size int":              "SQLite database options",
	} {
		assert.Equal(t, section, helpSectionOf(t, output, flagLine), "flag %q is in the wrong help section", flagLine)
	}
	assert.NotContains(t, output, "--hub ", "the Hub URL belongs to `leapmux worker register`")
	assert.NotContains(t, output, "--registration-key ", "the key belongs to `leapmux worker register`")
}

// A command that selects only the data directory shows and accepts only that
// flag, while the config file and the environment still reach every key.
func TestLoadWithCLIFlagSubset(t *testing.T) {
	opts := LoadOptions{CLIFlags: []string{"data-dir"}}

	t.Run("help shows only the selected flags", func(t *testing.T) {
		output := flagsHelp(t, opts)
		assert.Contains(t, output, "--config string")
		assert.Contains(t, output, "--data-dir string")
		for _, absent := range []string{"--name ", "--log-level ", "--db-max-conns ", "--agent-startup-timeout ", "--version"} {
			assert.NotContains(t, output, absent)
		}
		assert.NotContains(t, output, "Timeout and limit options", "an empty section prints no header")
	})

	t.Run("a flag outside the subset is rejected", func(t *testing.T) {
		_, err := LoadWithOptions([]string{"--log-level", "debug"}, opts)
		require.ErrorContains(t, err, "unknown flag: --log-level")
	})

	t.Run("the selected flag still sets its key", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg, err := LoadWithOptions([]string{"--data-dir", dataDir}, opts)
		require.NoError(t, err)
		assert.Equal(t, dataDir, cfg.DataDir)
	})

	t.Run("the config file and the environment still reach every key", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "worker.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte("log_level: warn\n"), 0o644))
		t.Setenv("LEAPMUX_WORKER_DB_MAX_CONNS", "7")
		cfg, err := LoadWithOptions([]string{"--config", configPath}, opts)
		require.NoError(t, err)
		assert.Equal(t, "warn", cfg.LogLevel)
		assert.Equal(t, 7, cfg.DBMaxConns)
	})

	t.Run("an empty subset keeps config", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "worker.yaml")
		_, err := LoadWithOptions([]string{"--config", configPath}, LoadOptions{CLIFlags: []string{}})
		require.NoError(t, err)
		_, err = LoadWithOptions([]string{"--data-dir", t.TempDir()}, LoadOptions{CLIFlags: []string{}})
		require.ErrorContains(t, err, "unknown flag: --data-dir")
	})

	t.Run("no subset selects every worker flag", func(t *testing.T) {
		output := flagsHelp(t, LoadOptions{})
		assert.Contains(t, output, "--log-level string")
	})
}

// helpSectionOf reports the section header a flag line appears under: the last
// header above it. A header is a line that starts at column 0 and ends in a
// colon, which in this output is only the section headings -- the usage line
// carries no trailing colon, the description ends in a period, and every flag
// and usage line is indented.
func helpSectionOf(t *testing.T, output, flagLine string) string {
	t.Helper()
	idx := strings.Index(output, flagLine)
	require.GreaterOrEqual(t, idx, 0, "flag %q is missing from the help output entirely", flagLine)
	section := ""
	for _, line := range strings.Split(output[:idx], "\n") {
		if line != "" && !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":") {
			section = strings.TrimSuffix(line, ":")
		}
	}
	return section
}

func TestValidate(t *testing.T) {
	t.Run("valid config creates data dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		dataDir := filepath.Join(tmpDir, "data")

		cfg := &Config{DataDir: dataDir}
		require.NoError(t, cfg.Validate())

		info, err := os.Stat(dataDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("an empty name defaults to the hostname", func(t *testing.T) {
		hostname, err := os.Hostname()
		require.NoError(t, err)
		cfg := &Config{DataDir: t.TempDir()}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, hostname, cfg.Name)
	})

	t.Run("max_message_size zero is allowed", func(t *testing.T) {
		cfg := &Config{DataDir: t.TempDir(), MaxMessageSize: 0}
		require.NoError(t, cfg.Validate())
	})

	t.Run("max_message_size below floor is rejected", func(t *testing.T) {
		cfg := &Config{DataDir: t.TempDir(), MaxMessageSize: 1}
		assert.Error(t, cfg.Validate())
	})

	t.Run("max_message_size above ceiling is rejected", func(t *testing.T) {
		cfg := &Config{DataDir: t.TempDir(), MaxMessageSize: contracts.MaxConfigurableMessageSize + 1}
		assert.Error(t, cfg.Validate())
	})
}

func TestPaths(t *testing.T) {
	cfg := &Config{DataDir: "/test/dir"}
	assert.Equal(t, filepath.Join("/test/dir", "worker.db"), cfg.DBPath())
	assert.Equal(t, filepath.Join("/test/dir", "state.json"), cfg.StatePath())
}

// TestAgentStartupConcurrency covers the knob that caps concurrent agent
// startups, from every source the loader supports.
//
// Zero is load-bearing and is asserted here rather than resolved: the default
// follows this machine's core count, so it is resolved by
// agent.ResolveStartupConcurrency at the point of use. A Load that "helpfully"
// substituted a number would freeze that decision into the config and break the
// entry points that pass the value straight through.
func TestAgentStartupConcurrency(t *testing.T) {
	t.Run("unset stays zero so the consumer resolves the default", func(t *testing.T) {
		cfg, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, 0, cfg.AgentStartupConcurrency)
	})

	t.Run("flag", func(t *testing.T) {
		cfg, err := Load([]string{"--agent-startup-concurrency", "7"})
		require.NoError(t, err)
		assert.Equal(t, 7, cfg.AgentStartupConcurrency)
	})

	t.Run("config file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worker.yaml")
		require.NoError(t, os.WriteFile(path, []byte("agent_startup_concurrency: 3\n"), 0o644))
		cfg, err := Load([]string{"--config", path})
		require.NoError(t, err)
		assert.Equal(t, 3, cfg.AgentStartupConcurrency)
	})

	t.Run("env", func(t *testing.T) {
		t.Setenv("LEAPMUX_WORKER_AGENT_STARTUP_CONCURRENCY", "5")
		cfg, err := Load(nil)
		require.NoError(t, err)
		assert.Equal(t, 5, cfg.AgentStartupConcurrency)
	})

	t.Run("flag beats config file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worker.yaml")
		require.NoError(t, os.WriteFile(path, []byte("agent_startup_concurrency: 3\n"), 0o644))
		cfg, err := Load([]string{"--config", path, "--agent-startup-concurrency", "9"})
		require.NoError(t, err)
		assert.Equal(t, 9, cfg.AgentStartupConcurrency)
	})

	t.Run("a negative value loads and is resolved downstream", func(t *testing.T) {
		cfg, err := Load([]string{"--agent-startup-concurrency", "-2"})
		require.NoError(t, err)
		assert.Equal(t, -2, cfg.AgentStartupConcurrency,
			"Load does not clamp; agent.ResolveStartupConcurrency turns any non-positive value into the default")
	})
}

// TestAgentStartupConcurrencyHelpDerivesTheDefault pins that the flag's help
// text reads the constant rather than restating it.
//
// The number appears in the help output a user reads, so a hand-written literal
// makes `leapmux worker --help` lie the day the default changes -- silently, and
// with nothing that fails.
func TestAgentStartupConcurrencyHelpDerivesTheDefault(t *testing.T) {
	output := flagsHelp(t, LoadOptions{})
	assert.Contains(t, output, "0 = "+strconv.Itoa(DefaultMaxStartupConcurrency),
		"the flag help must derive the default from DefaultMaxStartupConcurrency")
	assert.Contains(t, output, "per channel (0 = "+strconv.Itoa(contracts.DefaultMaxIncompleteChunked)+")",
		"the flag help must derive the chunk cap from contracts.DefaultMaxIncompleteChunked")
}

// TestResolveStartupConcurrency covers the rule the two help strings describe.
func TestResolveStartupConcurrency(t *testing.T) {
	t.Run("a positive value is honored", func(t *testing.T) {
		assert.Equal(t, 1, ResolveStartupConcurrency(1))
		assert.Equal(t, 3, ResolveStartupConcurrency(3))
		assert.Equal(t, 64, ResolveStartupConcurrency(64), "a value above the default cap is still honored")
	})

	t.Run("a non-positive value selects the default", func(t *testing.T) {
		want := min(runtime.GOMAXPROCS(0), DefaultMaxStartupConcurrency)
		assert.Equal(t, want, ResolveStartupConcurrency(0))
		assert.Equal(t, want, ResolveStartupConcurrency(-3))
		assert.GreaterOrEqual(t, want, 1, "the default must always admit at least one startup")
		assert.LessOrEqual(t, want, DefaultMaxStartupConcurrency, "the default never exceeds the cap")
	})

	t.Run("the default follows the CPU budget, not the core count", func(t *testing.T) {
		// Not parallel: GOMAXPROCS is process-global state. NumCPU reports the
		// affinity mask and a cgroup CPU quota does not change it, so a container
		// limited to half a core on a 32-core host still reports 32 -- and the
		// "a two-core container must not run four handshakes" promise would never
		// apply there. Both answers agree on a normal laptop, so only lowering
		// GOMAXPROCS tells them apart.
		prev := runtime.GOMAXPROCS(1)
		t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
		assert.Equal(t, 1, ResolveStartupConcurrency(0))
	})
}

// flagsHelp prints the help of a command whose flags NewFlags(opts)
// registers, as the help of a Worker command prints them.
func flagsHelp(t *testing.T, opts LoadOptions) string {
	t.Helper()
	return clitest.FlagsHelp(t, NewFlags(opts).Register)
}
