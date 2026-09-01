package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

const (
	defaultHubURL     = "http://127.0.0.1:4327"
	defaultConfigDir  = "~/.config/leapmux/worker"
	defaultConfigFile = "~/.config/leapmux/worker/worker.yaml"
	defaultLogLevel   = "info"

	// DefaultAgentStartupTimeout is the default timeout for the agent startup
	// handshake. Must match the hub's default. Sized for agents that initialize
	// slow MCP servers during the handshake (e.g. Claude Code loading a
	// workspace .mcp.json with multiple servers).
	DefaultAgentStartupTimeout = 5 * time.Minute

	// DefaultAPITimeout is the default timeout for JSON-RPC requests to agent
	// processes (e.g. turn/start, session/new).
	DefaultAPITimeout = 10 * time.Second
)

// Config holds the worker's runtime configuration.
type Config struct {
	HubURL string `koanf:"hub" json:"hub_url"`
	// RegistrationKey is the bearer credential the worker presents to
	// WorkerConnectorService.Register. Required on first run; ignored on
	// subsequent runs if the worker is already registered. Not persisted.
	RegistrationKey      string `koanf:"registration_key" json:"-"`
	Name                 string `koanf:"name" json:"name"`
	DataDir              string `koanf:"data_dir" json:"data_dir"`
	DBMaxConns           int    `koanf:"db_max_conns" json:"db_max_conns"`
	DBCacheSize          int    `koanf:"db_cache_size" json:"db_cache_size"`
	DBMmapSize           int    `koanf:"db_mmap_size" json:"db_mmap_size"`
	MaxIncompleteChunked int    `koanf:"max_incomplete_chunked" json:"max_incomplete_chunked"`
	MaxMessageSize       int    `koanf:"max_message_size" json:"max_message_size"`
	// Both durations accept a unit suffix and read a bare number as seconds --
	// see internalconfig.ParseDuration. Load resolves a zero to the matching
	// Default* constant.
	AgentStartupTimeout time.Duration `koanf:"agent_startup_timeout" json:"agent_startup_timeout"`
	APITimeout          time.Duration `koanf:"api_timeout" json:"api_timeout"`
	// AgentStartupConcurrency caps how many agent processes may be inside their
	// startup handshake at once. It does NOT cap how many agents this machine
	// runs -- a started agent holds nothing.
	//
	// Zero is kept as zero here, unlike the two timeouts above: the default
	// follows this machine's core count, so it is resolved by
	// agent.ResolveStartupConcurrency at the point of use rather than baked into
	// a loaded Config. This package cannot call that function (agent imports
	// config, so the reverse edge is a cycle), which is also why the flag's
	// default below is the literal 0.
	AgentStartupConcurrency int    `koanf:"agent_startup_concurrency" json:"agent_startup_concurrency"`
	LogLevel                string `koanf:"log_level" json:"log_level"`
	EncryptionMode          string `koanf:"encryption_mode" json:"encryption_mode"`
	UseLoginShell           bool   `koanf:"use_login_shell" json:"use_login_shell"`
}

// EncryptionModeProto returns the protobuf EncryptionMode value.
func (c *Config) EncryptionModeProto() leapmuxv1.EncryptionMode {
	return ParseEncryptionMode(c.EncryptionMode)
}

// ParseEncryptionMode parses a string encryption mode to its protobuf enum value.
func ParseEncryptionMode(s string) leapmuxv1.EncryptionMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "classic":
		return leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC
	case "post-quantum", "post_quantum", "pq", "":
		return leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
	default:
		return leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
	}
}

// applyDurationDefaults restores the default for each timeout that a source
// left at zero, so every reader of a loaded Config finds a usable value and
// none of them repeats the fallback.
func (c *Config) applyDurationDefaults() {
	if c.AgentStartupTimeout == 0 {
		c.AgentStartupTimeout = DefaultAgentStartupTimeout
	}
	if c.APITimeout == 0 {
		c.APITimeout = DefaultAPITimeout
	}
}

// State holds the worker's persistent state (saved to disk after registration).
type State struct {
	WorkerID  string `json:"worker_id"`
	AuthToken string `json:"auth_token"`
	// No registered_by: the Hub delivers the worker's owner on every connect
	// (leapmuxv1.WorkerIdentity), so persisting a copy would be a second source of
	// truth for the fact every machine-scoped gate keys on -- and a file that lost
	// it left the worker denying its own legitimate owner, permanently.
	PublicKey        string `json:"public_key,omitempty"`         // Base64-encoded X25519 public key
	PrivateKey       string `json:"private_key,omitempty"`        // Base64-encoded X25519 private key
	MlkemPublicKey   string `json:"mlkem_public_key,omitempty"`   // Base64-encoded ML-KEM-1024 decapsulation key
	SlhdsaPublicKey  string `json:"slhdsa_public_key,omitempty"`  // Base64-encoded SLH-DSA public key
	SlhdsaPrivateKey string `json:"slhdsa_private_key,omitempty"` // Base64-encoded SLH-DSA private key
	MlkemPrivateKey  string `json:"mlkem_private_key,omitempty"`  // Base64-encoded ML-KEM-1024 decapsulation key (serialized)
}

// EnsureCompositeKeypair generates a composite keypair if one doesn't exist.
// Returns true if a new keypair was generated (state needs saving).
func (s *State) EnsureCompositeKeypair() (bool, error) {
	if s.PublicKey != "" && s.PrivateKey != "" && s.MlkemPublicKey != "" && s.MlkemPrivateKey != "" && s.SlhdsaPublicKey != "" && s.SlhdsaPrivateKey != "" {
		return false, nil
	}

	ck, err := noiseutil.GenerateCompositeKeypair()
	if err != nil {
		return false, fmt.Errorf("generate composite keypair: %w", err)
	}

	slhdsaPub, err := ck.SlhdsaPublicKeyBytes()
	if err != nil {
		return false, fmt.Errorf("marshal slhdsa public key: %w", err)
	}
	slhdsaPriv, err := ck.SlhdsaPrivateKey.MarshalBinary()
	if err != nil {
		return false, fmt.Errorf("marshal slhdsa private key: %w", err)
	}

	s.PublicKey = base64.StdEncoding.EncodeToString(ck.X25519Public)
	s.PrivateKey = base64.StdEncoding.EncodeToString(ck.X25519Private)
	s.MlkemPublicKey = base64.StdEncoding.EncodeToString(ck.MlkemPublicKeyBytes())
	s.MlkemPrivateKey = base64.StdEncoding.EncodeToString(ck.MlkemDecapsulationKey.Bytes())
	s.SlhdsaPublicKey = base64.StdEncoding.EncodeToString(slhdsaPub)
	s.SlhdsaPrivateKey = base64.StdEncoding.EncodeToString(slhdsaPriv)
	return true, nil
}

// CompositeKeypair reconstructs the CompositeKeypair from persisted state.
func (s *State) CompositeKeypair() (*noiseutil.CompositeKeypair, error) {
	return noiseutil.RestoreCompositeKeypair(
		mustDecode(s.PublicKey),
		mustDecode(s.PrivateKey),
		mustDecode(s.MlkemPrivateKey),
		mustDecode(s.SlhdsaPublicKey),
		mustDecode(s.SlhdsaPrivateKey),
	)
}

func mustDecode(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}

// Load parses worker configuration from defaults, config file, env vars, and CLI flags.
// Returns the config, whether -version was requested, and any error.
func Load(args []string) (*Config, bool, error) {
	// Pre-scan for -config flag.
	configPath := internalconfig.ExtractConfigFlag(args, defaultConfigFile)

	// Define CLI flags.
	fs := flag.NewFlagSet("leapmux worker", flag.ContinueOnError)
	fs.String("config", defaultConfigFile, "path to config file")
	fs.String("hub", defaultHubURL, "Hub server URL (http[s]://..., unix:<socket-path>, or npipe:<pipe-name>)")
	fs.String("registration-key", "", "registration key from the hub UI (required on first run)")
	fs.String("name", "", "worker display name (default: hostname)")
	fs.String("data-dir", ".", "data directory")
	fs.Int("db-max-conns", sqlitedb.DefaultMaxConns, "maximum number of open database connections")
	fs.Int("db-cache-size", 0, "SQLite page cache size (positive = pages, negative = KiB, e.g. -65536 = 64 MiB; 0 = default)")
	fs.Int("db-mmap-size", 0, "SQLite memory-mapped I/O size in bytes (0 = disabled)")
	fs.Int("max-incomplete-chunked", 0, "maximum in-flight chunked sequences per channel (default 4)")
	fs.Int("max-message-size", 0, "maximum application payload size in bytes (0 = 16 MiB default); reassembled ceiling is this plus 64 KiB headroom")
	fs.Var(internalconfig.NewDurationFlag(new(time.Duration), DefaultAgentStartupTimeout),
		"agent-startup-timeout", "agent startup timeout. "+internalconfig.UnitSyntax)
	fs.Var(internalconfig.NewDurationFlag(new(time.Duration), DefaultAPITimeout),
		"api-timeout", "JSON-RPC request timeout. "+internalconfig.UnitSyntax)
	fs.Int("agent-startup-concurrency", 0,
		"maximum agent processes inside their startup handshake at once "+
			"(0 = 4, or the CPU core count when that is lower); "+
			"does not limit how many agents run")
	fs.String("log-level", defaultLogLevel, "log level (debug, info, warn, error)")
	fs.String("encryption-mode", "post-quantum", "encryption mode (classic, post-quantum)")
	fs.Bool("use-login-shell", true, "wrap claude invocation in user's login shell")
	showVersion := fs.Bool("version", false, "print version and exit")
	usageCategories := map[string]string{
		"config":                    "Common options",
		"version":                   "Common options",
		"hub":                       "Worker options",
		"registration-key":          "Worker options",
		"name":                      "Worker options",
		"data-dir":                  "Worker options",
		"log-level":                 "Worker options",
		"encryption-mode":           "Worker options",
		"use-login-shell":           "Worker options",
		"max-incomplete-chunked":    "Timeout and limit options",
		"max-message-size":          "Timeout and limit options",
		"agent-startup-timeout":     "Timeout and limit options",
		"api-timeout":               "Timeout and limit options",
		"agent-startup-concurrency": "Timeout and limit options",
		"db-max-conns":              "SQLite database options",
		"db-cache-size":             "SQLite database options",
		"db-mmap-size":              "SQLite database options",
	}
	if err := internalconfig.ConfigureAndParse(fs, args, "Run a Worker connected to a Hub.", usageCategories, workerFlagCategoryOrder); err != nil {
		return nil, false, err
	}

	if *showVersion {
		return nil, true, nil
	}

	// Flag name -> koanf key mapping.
	fieldMap := map[string]string{
		"hub":                       "hub",
		"registration-key":          "registration_key",
		"name":                      "name",
		"data-dir":                  "data_dir",
		"db-max-conns":              "db_max_conns",
		"db-cache-size":             "db_cache_size",
		"db-mmap-size":              "db_mmap_size",
		"max-incomplete-chunked":    "max_incomplete_chunked",
		"max-message-size":          "max_message_size",
		"agent-startup-timeout":     "agent_startup_timeout",
		"api-timeout":               "api_timeout",
		"agent-startup-concurrency": "agent_startup_concurrency",
		"log-level":                 "log_level",
		"encryption-mode":           "encryption_mode",
		"use-login-shell":           "use_login_shell",
	}

	defaults := map[string]interface{}{
		"hub":                    defaultHubURL,
		"registration_key":       "",
		"name":                   "",
		"data_dir":               ".",
		"db_max_conns":           sqlitedb.DefaultMaxConns,
		"db_cache_size":          0,
		"db_mmap_size":           0,
		"max_incomplete_chunked": 0,
		"max_message_size":       0,
		"agent_startup_timeout":  DefaultAgentStartupTimeout,
		"api_timeout":            DefaultAPITimeout,
		// 0 means "resolve from this machine's core count" -- see the field's
		// comment on Config.AgentStartupConcurrency.
		"agent_startup_concurrency": 0,
		"log_level":                 defaultLogLevel,
		"encryption_mode":           "post-quantum",
		"use_login_shell":           true,
	}

	k := koanf.New(internalconfig.Delim)
	fp := internalconfig.NewFlagProvider(fs, fieldMap)

	if err := internalconfig.Load(k, defaults, configPath, "LEAPMUX_WORKER_", fp); err != nil {
		return nil, false, fmt.Errorf("load config: %w", err)
	}

	var cfg Config
	if err := internalconfig.Unmarshal(k, &cfg); err != nil {
		return nil, false, fmt.Errorf("unmarshal config: %w", err)
	}

	// Resolve the timeouts before anything reads one.
	cfg.applyDurationDefaults()

	// Resolve relative data_dir against config file directory.
	cfg.DataDir = internalconfig.ResolveDataDir(cfg.DataDir, configPath, defaultConfigDir)

	return &cfg, false, nil
}

var workerFlagCategoryOrder = []string{
	"Common options",
	"Worker options",
	"Timeout and limit options",
	"SQLite database options",
}

// Validate checks the configuration and ensures required directories exist.
func (c *Config) Validate() error {
	if c.HubURL == "" {
		return fmt.Errorf("hub URL is required")
	}

	// Default name to hostname if not explicitly set.
	if c.Name == "" {
		hostname, _ := os.Hostname()
		c.Name = hostname
	}

	// Ensure data dir exists.
	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := channelwire.ValidateConfiguredMaxMessageSize(c.MaxMessageSize); err != nil {
		return err
	}

	return nil
}

// StatePath returns the path to the state file.
func (c *Config) StatePath() string {
	return filepath.Join(c.DataDir, "state.json")
}

// DBPath returns the path to the worker database file.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "worker.db")
}

// DBConfig returns the SQLite configuration for sqlitedb.Open.
func (c *Config) DBConfig() sqlitedb.Config {
	return sqlitedb.Config{
		MaxConns:  c.DBMaxConns,
		CacheSize: c.DBCacheSize,
		MmapSize:  c.DBMmapSize,
	}
}

// LoadState loads persisted state from disk. Returns nil if no state file exists.
func (c *Config) LoadState() (*State, error) {
	data, err := os.ReadFile(c.StatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ClearState removes the persisted state file.
func (c *Config) ClearState() error {
	return os.Remove(c.StatePath())
}

// SaveState persists state to disk.
func (c *Config) SaveState(s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.StatePath(), data, 0o600)
}
