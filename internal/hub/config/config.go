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
	SoloMode                     bool
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

// LoadOptions parameterizes differences between hub and solo/dev mode config loading.
type LoadOptions struct {
	DefaultAddr       string   // default listen address (hub: ":4327", solo: "127.0.0.1:4327")
	DefaultConfigDir  string   // for data_dir resolution (e.g. "~/.config/leapmux/solo")
	DefaultConfigFile string   // default config file path
	FlagSetName       string   // flag.NewFlagSet name ("hub" vs "leapmux")
	CLIFlags          []string // if non-nil, only register these flags (solo exposes a subset)
	SoloMode          bool     // set on resulting Config
}

// Load parses hub configuration from defaults, config file, env vars, and CLI flags.
// Returns the config, whether -version was requested, and any error.
func Load(args []string) (*Config, bool, error) {
	return LoadWithOptions(args, LoadOptions{})
}

// LoadWithOptions parses hub configuration with customizable options.
// Zero-value LoadOptions fields fall back to hub defaults.
func LoadWithOptions(args []string, opts LoadOptions) (*Config, bool, error) {
	addr := opts.DefaultAddr
	if addr == "" {
		addr = defaultAddr
	}
	configDir := opts.DefaultConfigDir
	if configDir == "" {
		configDir = defaultConfigDir
	}
	configFile := opts.DefaultConfigFile
	if configFile == "" {
		configFile = defaultConfigFile
	}
	fsName := opts.FlagSetName
	if fsName == "" {
		fsName = "hub"
	}

	// Pre-scan for -config flag.
	configPath := internalconfig.ExtractConfigFlag(args, configFile)

	// All available flags with their definitions.
	type flagDef struct {
		name     string
		koanfKey string
		usage    string
		// Exactly one of these is set.
		strDefault  *string
		intDefault  *int
		boolDefault *bool
	}

	strVal := func(s string) *string { return &s }
	intVal := func(i int) *int { return &i }
	boolVal := func(b bool) *bool { return &b }

	allFlags := []flagDef{
		{"addr", "addr", "listen address", strVal(addr), nil, nil},
		{"data-dir", "data_dir", "data directory", strVal("."), nil, nil},
		{"dev-frontend", "dev_frontend", "Vite dev server URL for reverse proxy (dev mode only)", strVal(""), nil, nil},
		{"db-max-conns", "db_max_conns", "maximum number of open database connections", nil, intVal(hubdb.DefaultMaxConns), nil},
		{"max-message-size", "max_message_size", "maximum reassembled channel message size in bytes (default 16 MiB)", nil, intVal(0), nil},
		{"max-incomplete-chunked", "max_incomplete_chunked", "maximum in-flight chunked sequences per channel (default 4)", nil, intVal(0), nil},
		{"log-level", "log_level", "log level (debug, info, warn, error)", strVal(defaultLogLevel), nil, nil},
		{"signup-enabled", "signup_enabled", "enable user sign-up", nil, nil, boolVal(false)},
		{"email-verification-required", "email_verification_required", "require email verification on sign-up", nil, nil, boolVal(false)},
		{"smtp-host", "smtp_host", "SMTP server host", strVal(""), nil, nil},
		{"smtp-port", "smtp_port", "SMTP server port", nil, intVal(587), nil},
		{"smtp-username", "smtp_username", "SMTP username", strVal(""), nil, nil},
		{"smtp-password", "smtp_password", "SMTP password", strVal(""), nil, nil},
		{"smtp-from-address", "smtp_from_address", "SMTP from address", strVal(""), nil, nil},
		{"smtp-use-tls", "smtp_use_tls", "use TLS for SMTP", nil, nil, boolVal(true)},
		{"api-timeout-seconds", "api_timeout_seconds", "general API timeout in seconds", nil, intVal(DefaultAPITimeoutSeconds), nil},
		{"agent-startup-timeout-seconds", "agent_startup_timeout_seconds", "agent startup timeout in seconds", nil, intVal(DefaultAgentStartupTimeoutSeconds), nil},
		{"worktree-create-timeout-seconds", "worktree_create_timeout_seconds", "worktree creation timeout in seconds", nil, intVal(DefaultWorktreeCreateTimeoutSeconds), nil},
	}

	// Build the set of allowed CLI flags.
	var allowedFlags map[string]bool
	if opts.CLIFlags != nil {
		allowedFlags = make(map[string]bool, len(opts.CLIFlags))
		for _, f := range opts.CLIFlags {
			allowedFlags[f] = true
		}
	}

	// Define CLI flags and build fieldMap/defaults from the canonical list.
	fs := flag.NewFlagSet(fsName, flag.ContinueOnError)
	fs.String("config", configFile, "path to config file")
	fieldMap := make(map[string]string, len(allFlags))
	defaults := make(map[string]interface{}, len(allFlags))

	for _, fd := range allFlags {
		// Always add to defaults.
		switch {
		case fd.strDefault != nil:
			defaults[fd.koanfKey] = *fd.strDefault
		case fd.intDefault != nil:
			defaults[fd.koanfKey] = *fd.intDefault
		case fd.boolDefault != nil:
			defaults[fd.koanfKey] = *fd.boolDefault
		}

		// Register CLI flag only if allowed.
		if allowedFlags != nil && !allowedFlags[fd.name] {
			continue
		}
		fieldMap[fd.name] = fd.koanfKey
		switch {
		case fd.strDefault != nil:
			fs.String(fd.name, *fd.strDefault, fd.usage)
		case fd.intDefault != nil:
			fs.Int(fd.name, *fd.intDefault, fd.usage)
		case fd.boolDefault != nil:
			fs.Bool(fd.name, *fd.boolDefault, fd.usage)
		}
	}
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}

	if *showVersion {
		return nil, true, nil
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
	cfg.DataDir = internalconfig.ResolveDataDir(cfg.DataDir, configPath, configDir)
	cfg.SoloMode = opts.SoloMode

	return &cfg, false, nil
}

// Validate checks the configuration values and ensures required directories exist.
func (c *Config) Validate() error {
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
