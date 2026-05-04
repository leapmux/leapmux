package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
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
		assert.Equal(t, 0, cfg.MaxMessageSize)
		assert.Equal(t, 0, cfg.MaxIncompleteChunked)
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
		yamlContent := `max_message_size: 1024
max_incomplete_chunked: 8
signup_enabled: true
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o644))

		cfg, _, err := LoadWithOptions([]string{"-config", configPath}, LoadOptions{
			CLIFlags: []string{"listen", "data-dir", "log-level"},
		})
		require.NoError(t, err)
		assert.Equal(t, 1024, cfg.MaxMessageSize)
		assert.Equal(t, 8, cfg.MaxIncompleteChunked)
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

	t.Run("version flag works with options", func(t *testing.T) {
		_, showVersion, err := LoadWithOptions([]string{"-version"}, LoadOptions{
			CLIFlags: []string{"listen"},
		})
		require.NoError(t, err)
		assert.True(t, showVersion)
	})
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
