package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/knadh/koanf/v2"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	hubdb "github.com/leapmux/leapmux/internal/hub/db"
)

const (
	defaultAddr       = ":4327"
	defaultConfigDir  = "~/.config/leapmux/hub"
	defaultConfigFile = "~/.config/leapmux/hub/hub.yaml"
	defaultLogLevel   = "info"
)

// Default timeout values (in seconds).
const (
	DefaultAPITimeoutSeconds            = 10
	DefaultAgentStartupTimeoutSeconds   = 30
	DefaultWorktreeCreateTimeoutSeconds = 60
)

// Config holds the hub's runtime configuration.
type Config struct {
	Addr                         string `koanf:"addr"`
	DataDir                      string `koanf:"data_dir"`
	DevFrontend                  string `koanf:"dev_frontend"`
	DBMaxConns                   int    `koanf:"db_max_conns"`
	MaxMessageSize               int    `koanf:"max_message_size"`
	MaxIncompleteChunked         int    `koanf:"max_incomplete_chunked"`
	LogLevel                     string `koanf:"log_level"`
	SignupEnabled                bool   `koanf:"signup_enabled"`
	EmailVerificationRequired    bool   `koanf:"email_verification_required"`
	SmtpHost                     string `koanf:"smtp_host"`
	SmtpPort                     int    `koanf:"smtp_port"`
	SmtpUsername                 string `koanf:"smtp_username"`
	SmtpPassword                 string `koanf:"smtp_password"`
	SmtpFromAddress              string `koanf:"smtp_from_address"`
	SmtpUseTLS                   bool   `koanf:"smtp_use_tls"`
	APITimeoutSeconds            int    `koanf:"api_timeout_seconds"`
	AgentStartupTimeoutSeconds   int    `koanf:"agent_startup_timeout_seconds"`
	WorktreeCreateTimeoutSeconds int    `koanf:"worktree_create_timeout_seconds"`
}

// APITimeout returns the general API timeout as a duration.
func (c *Config) APITimeout() time.Duration {
	v := c.APITimeoutSeconds
	if v <= 0 {
		v = DefaultAPITimeoutSeconds
	}
	return time.Duration(v) * time.Second
}

// AgentStartupTimeout returns the agent startup/resume timeout as a duration.
func (c *Config) AgentStartupTimeout() time.Duration {
	v := c.AgentStartupTimeoutSeconds
	if v <= 0 {
		v = DefaultAgentStartupTimeoutSeconds
	}
	return time.Duration(v) * time.Second
}

// WorktreeCreateTimeout returns the worktree creation timeout as a duration.
func (c *Config) WorktreeCreateTimeout() time.Duration {
	v := c.WorktreeCreateTimeoutSeconds
	if v <= 0 {
		v = DefaultWorktreeCreateTimeoutSeconds
	}
	return time.Duration(v) * time.Second
}

// Load parses hub configuration from defaults, config file, env vars, and CLI flags.
// Returns the config, whether -version was requested, and any error.
func Load(args []string) (*Config, bool, error) {
	// Pre-scan for -config flag.
	configPath := internalconfig.ExtractConfigFlag(args, defaultConfigFile)

	// Define CLI flags.
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	fs.String("config", defaultConfigFile, "path to config file")
	fs.String("addr", defaultAddr, "listen address")
	fs.String("data-dir", ".", "data directory")
	fs.String("dev-frontend", "", "Vite dev server URL for reverse proxy (dev mode only)")
	fs.Int("db-max-conns", hubdb.DefaultMaxConns, "maximum number of open database connections")
	fs.Int("max-message-size", 0, "maximum reassembled channel message size in bytes (default 16 MiB)")
	fs.Int("max-incomplete-chunked", 0, "maximum in-flight chunked sequences per channel (default 4)")
	fs.String("log-level", defaultLogLevel, "log level (debug, info, warn, error)")
	fs.Bool("signup-enabled", false, "enable user sign-up")
	fs.Bool("email-verification-required", false, "require email verification on sign-up")
	fs.String("smtp-host", "", "SMTP server host")
	fs.Int("smtp-port", 587, "SMTP server port")
	fs.String("smtp-username", "", "SMTP username")
	fs.String("smtp-password", "", "SMTP password")
	fs.String("smtp-from-address", "", "SMTP from address")
	fs.Bool("smtp-use-tls", true, "use TLS for SMTP")
	fs.Int("api-timeout-seconds", DefaultAPITimeoutSeconds, "general API timeout in seconds")
	fs.Int("agent-startup-timeout-seconds", DefaultAgentStartupTimeoutSeconds, "agent startup timeout in seconds")
	fs.Int("worktree-create-timeout-seconds", DefaultWorktreeCreateTimeoutSeconds, "worktree creation timeout in seconds")
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}

	if *showVersion {
		return nil, true, nil
	}

	// Flag name -> koanf key mapping.
	fieldMap := map[string]string{
		"addr":                            "addr",
		"data-dir":                        "data_dir",
		"dev-frontend":                    "dev_frontend",
		"db-max-conns":                    "db_max_conns",
		"max-message-size":                "max_message_size",
		"max-incomplete-chunked":          "max_incomplete_chunked",
		"log-level":                       "log_level",
		"signup-enabled":                  "signup_enabled",
		"email-verification-required":     "email_verification_required",
		"smtp-host":                       "smtp_host",
		"smtp-port":                       "smtp_port",
		"smtp-username":                   "smtp_username",
		"smtp-password":                   "smtp_password",
		"smtp-from-address":               "smtp_from_address",
		"smtp-use-tls":                    "smtp_use_tls",
		"api-timeout-seconds":             "api_timeout_seconds",
		"agent-startup-timeout-seconds":   "agent_startup_timeout_seconds",
		"worktree-create-timeout-seconds": "worktree_create_timeout_seconds",
	}

	defaults := map[string]interface{}{
		"addr":                            defaultAddr,
		"data_dir":                        ".",
		"dev_frontend":                    "",
		"db_max_conns":                    hubdb.DefaultMaxConns,
		"max_message_size":                0,
		"max_incomplete_chunked":          0,
		"log_level":                       defaultLogLevel,
		"signup_enabled":                  false,
		"email_verification_required":     false,
		"smtp_host":                       "",
		"smtp_port":                       587,
		"smtp_username":                   "",
		"smtp_password":                   "",
		"smtp_from_address":               "",
		"smtp_use_tls":                    true,
		"api_timeout_seconds":             DefaultAPITimeoutSeconds,
		"agent_startup_timeout_seconds":   DefaultAgentStartupTimeoutSeconds,
		"worktree_create_timeout_seconds": DefaultWorktreeCreateTimeoutSeconds,
	}

	k := koanf.New(".")
	fp := internalconfig.NewFlagProvider(fs, fieldMap)

	if err := internalconfig.Load(k, defaults, configPath, "LEAPMUX_HUB_", fp); err != nil {
		return nil, false, fmt.Errorf("load config: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, false, fmt.Errorf("unmarshal config: %w", err)
	}

	// Resolve relative data_dir against config file directory.
	cfg.DataDir = internalconfig.ResolveDataDir(cfg.DataDir, configPath, defaultConfigDir)

	return &cfg, false, nil
}

// Validate checks the configuration values and ensures required directories exist.
func (c *Config) Validate() error {
	if c.Addr == "" {
		return fmt.Errorf("addr is required")
	}

	// Ensure data dir exists.
	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	return nil
}

// DBPath returns the path to the SQLite database file.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "hub.db")
}

// SocketPath returns the path to the Unix domain socket.
func (c *Config) SocketPath() string {
	return filepath.Join(c.DataDir, "hub.sock")
}
