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
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
)

const (
	defaultHubURL     = "http://localhost:4327"
	defaultConfigDir  = "~/.config/leapmux/worker"
	defaultConfigFile = "~/.config/leapmux/worker/worker.yaml"
	defaultLogLevel   = "info"

	// DefaultAgentStartupTimeoutSeconds is the default timeout (in seconds) for
	// agent startup handshake. Must match the hub's default.
	DefaultAgentStartupTimeoutSeconds = 30
)

// Config holds the worker's runtime configuration.
type Config struct {
	HubURL                     string `koanf:"hub" json:"hub_url"`
	Name                       string `koanf:"name" json:"name"`
	DataDir                    string `koanf:"data_dir" json:"data_dir"`
	DBMaxConns                 int    `koanf:"db_max_conns" json:"db_max_conns"`
	MaxMessageSize             int    `koanf:"max_message_size" json:"max_message_size"`
	AgentStartupTimeoutSeconds int    `koanf:"agent_startup_timeout_seconds" json:"agent_startup_timeout_seconds"`
	LogLevel                   string `koanf:"log_level" json:"log_level"`
	EncryptionMode             string `koanf:"encryption_mode" json:"encryption_mode"`
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

// AgentStartupTimeout returns the agent startup timeout as a duration.
func (c *Config) AgentStartupTimeout() time.Duration {
	v := c.AgentStartupTimeoutSeconds
	if v <= 0 {
		v = DefaultAgentStartupTimeoutSeconds
	}
	return time.Duration(v) * time.Second
}

// State holds the worker's persistent state (saved to disk after registration).
type State struct {
	WorkerID         string `json:"worker_id"`
	AuthToken        string `json:"auth_token"`
	OrgID            string `json:"org_id"`
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
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.String("config", defaultConfigFile, "path to config file")
	fs.String("hub", defaultHubURL, "Hub server URL or unix:<socket-path>")
	fs.String("name", "", "worker display name (default: hostname)")
	fs.String("data-dir", ".", "data directory")
	fs.Int("db-max-conns", workerdb.DefaultMaxConns, "maximum number of open database connections")
	fs.Int("max-message-size", 0, "maximum reassembled channel message size in bytes (default 16 MiB)")
	fs.Int("agent-startup-timeout-seconds", DefaultAgentStartupTimeoutSeconds, "agent startup timeout in seconds")
	fs.String("log-level", defaultLogLevel, "log level (debug, info, warn, error)")
	fs.String("encryption-mode", "post-quantum", "encryption mode (classic, post-quantum)")
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}

	if *showVersion {
		return nil, true, nil
	}

	// Flag name -> koanf key mapping.
	fieldMap := map[string]string{
		"hub":                           "hub",
		"name":                          "name",
		"data-dir":                      "data_dir",
		"db-max-conns":                  "db_max_conns",
		"max-message-size":              "max_message_size",
		"agent-startup-timeout-seconds": "agent_startup_timeout_seconds",
		"log-level":                     "log_level",
		"encryption-mode":               "encryption_mode",
	}

	defaults := map[string]interface{}{
		"hub":                           defaultHubURL,
		"name":                          "",
		"data_dir":                      ".",
		"db_max_conns":                  workerdb.DefaultMaxConns,
		"max_message_size":              0,
		"agent_startup_timeout_seconds": DefaultAgentStartupTimeoutSeconds,
		"log_level":                     defaultLogLevel,
		"encryption_mode":               "post-quantum",
	}

	k := koanf.New(".")
	fp := internalconfig.NewFlagProvider(fs, fieldMap)

	if err := internalconfig.Load(k, defaults, configPath, "LEAPMUX_WORKER_", fp); err != nil {
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
