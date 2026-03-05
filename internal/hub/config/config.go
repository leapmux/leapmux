package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

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

// Config holds the hub's runtime configuration.
type Config struct {
	Addr                 string `koanf:"addr"`
	DataDir              string `koanf:"data_dir"`
	DevFrontend          string `koanf:"dev_frontend"`
	DBMaxConns           int    `koanf:"db_max_conns"`
	MaxMessageSize       int    `koanf:"max_message_size"`
	MaxIncompleteChunked int    `koanf:"max_incomplete_chunked"`
	LogLevel             string `koanf:"log_level"`
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
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}

	if *showVersion {
		return nil, true, nil
	}

	// Flag name -> koanf key mapping.
	fieldMap := map[string]string{
		"addr":                   "addr",
		"data-dir":               "data_dir",
		"dev-frontend":           "dev_frontend",
		"db-max-conns":           "db_max_conns",
		"max-message-size":       "max_message_size",
		"max-incomplete-chunked": "max_incomplete_chunked",
		"log-level":              "log_level",
	}

	defaults := map[string]interface{}{
		"addr":                   defaultAddr,
		"data_dir":               ".",
		"dev_frontend":           "",
		"db_max_conns":           hubdb.DefaultMaxConns,
		"max_message_size":       0,
		"max_incomplete_chunked": 0,
		"log_level":              defaultLogLevel,
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
