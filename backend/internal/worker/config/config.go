package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
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

	// DefaultMaxStartupConcurrency caps the DEFAULT number of agent startups the
	// boot-time resume sweep runs at once. Agent startup is CPU-bound on this
	// side (a login shell, the CLI's own boot, JSON parsing), so four concurrent
	// handshakes already saturate a laptop while it serves the user's own tab. A
	// machine with more cores gets no more by default, because the extra
	// parallelism buys the sweep little; an operator who wants more asks for it
	// by name.
	DefaultMaxStartupConcurrency = 4
)

// ResolveStartupConcurrency answers how many agent processes the resume sweep
// may have inside their startup handshake at once. n <= 0 selects the default.
//
// The default is DefaultMaxStartupConcurrency, lowered to this machine's CPU
// budget when that is smaller: a two-core container must not run four
// handshakes at once.
//
// The budget comes from runtime.GOMAXPROCS(0), not runtime.NumCPU(). NumCPU
// reports the logical CPUs the process may run on -- the affinity mask -- and a
// cgroup CPU quota does not change it, so a container limited to half a core on
// a 32-core host still reports 32. GOMAXPROCS derives its default from the quota
// as well as the affinity mask, which is the number this cap wants.
//
// It lives in this package, and not beside the permit pool it sizes, so that the
// two flag-help strings that print the default can read it. agent imports config
// (agent/start_options.go, for the two timeouts above), so the reverse edge is a
// cycle and a constant in agent is unreachable from here or from solo.
func ResolveStartupConcurrency(n int) int {
	if n > 0 {
		return n
	}
	return min(runtime.GOMAXPROCS(0), DefaultMaxStartupConcurrency)
}

// Config holds the worker's runtime configuration.
type Config struct {
	HubURL string `koanf:"hub" json:"hub_url"`
	// RegistrationKey is the bearer credential the worker presents to
	// WorkerConnectorService.Register during explicit registration. The private
	// pending-registration.json record retains it until recovery and durable
	// credential publication complete. Runtime commands do not register.
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
	// follows this machine's CPU budget, which is a runtime property rather than
	// a loaded one, so ResolveStartupConcurrency resolves it at the point of use.
	// That is also why the flag's default below is the literal 0.
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

// ErrUnusableRegisteredKeypair reports a saved state that holds credentials but
// no usable keypair. Its message states only the fact: reregistrationAdvice is
// the one source of the remedy.
var ErrUnusableRegisteredKeypair = errors.New(
	"the worker state is registered, but its keypair is missing or unusable")

// KeyMaterial is the Worker's static key material as it is persisted: the three
// PRIVATE halves, base64-encoded.
//
// Each public half is derivable (see noise.RestoreCompositeKeypair), so storing
// it would be a second copy that can drift from the private half it belongs to
// -- and a drifted copy is permanent once the Hub pins it.
type KeyMaterial struct {
	// X25519PrivateKey is the raw 32-byte X25519 scalar.
	X25519PrivateKey string `json:"x25519_private_key,omitempty"`
	// MlkemPrivateKey is the 64-byte ML-KEM-1024 decapsulation seed.
	MlkemPrivateKey string `json:"mlkem_private_key,omitempty"`
	// SlhdsaPrivateKey is the SLH-DSA private key, which embeds its public key.
	SlhdsaPrivateKey string `json:"slhdsa_private_key,omitempty"`
}

// HasKeypair reports whether all three private halves are present.
func (k KeyMaterial) HasKeypair() bool {
	return k.X25519PrivateKey != "" && k.MlkemPrivateKey != "" && k.SlhdsaPrivateKey != ""
}

// GenerateKeypair generates a fresh composite keypair and stores its private
// halves. It returns the keypair, so a caller that just generated one can use
// it without a restore round trip.
func (k *KeyMaterial) GenerateKeypair() (*noiseutil.CompositeKeypair, error) {
	ck, err := noiseutil.GenerateCompositeKeypair()
	if err != nil {
		return nil, fmt.Errorf("generate composite keypair: %w", err)
	}
	slhdsaPriv, err := ck.SlhdsaPrivateKey.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal slhdsa private key: %w", err)
	}
	k.X25519PrivateKey = base64.StdEncoding.EncodeToString(ck.X25519Private)
	k.MlkemPrivateKey = base64.StdEncoding.EncodeToString(ck.MlkemDecapsulationKey.Bytes())
	k.SlhdsaPrivateKey = base64.StdEncoding.EncodeToString(slhdsaPriv)
	return ck, nil
}

// CompositeKeypair decodes the saved private halves and restores the keypair,
// public halves included.
func (k KeyMaterial) CompositeKeypair() (*noiseutil.CompositeKeypair, error) {
	x25519Priv, err := decode64("x25519_private_key", k.X25519PrivateKey)
	if err != nil {
		return nil, err
	}
	mlkemSeed, err := decode64("mlkem_private_key", k.MlkemPrivateKey)
	if err != nil {
		return nil, err
	}
	slhdsaPriv, err := decode64("slhdsa_private_key", k.SlhdsaPrivateKey)
	if err != nil {
		return nil, err
	}
	return noiseutil.RestoreCompositeKeypair(x25519Priv, mlkemSeed, slhdsaPriv)
}

// decode64 decodes one base64 field of the saved key material and states the
// field in its error.
func decode64(field, s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", field, err)
	}
	return b, nil
}

// State holds the worker's persistent credentials. The register command
// writes them; runtime execution reads them.
//
// hub_url is the Hub endpoint the registration succeeded against, and
// the only endpoint execution and deregistration use: a runtime command
// must never silently redirect saved credentials to another Hub.
//
// No PID and no registered_by: a process identifier in a credentials
// file is never evidence of a running worker, and the Hub delivers the
// worker's owner on every connect (leapmuxv1.WorkerIdentity), so a
// persisted copy would be a second source of truth for the fact every
// machine-scoped gate keys on.
type State struct {
	HubURL    string `json:"hub_url"`
	WorkerID  string `json:"worker_id"`
	AuthToken string `json:"auth_token"`
	KeyMaterial
}

// EnsureCompositeKeypair returns the state's keypair, generating one when it has
// none. The bool reports whether it generated one, so the caller knows to save.
//
// A REGISTERED state (a token is present) whose keypair is missing or fails to
// restore is an error: the Hub has already pinned keys this Worker cannot
// reproduce, and nothing here may silently replace them. An UNREGISTERED state
// with a broken or missing keypair regenerates, because nothing is pinned yet.
func (s *State) EnsureCompositeKeypair() (*noiseutil.CompositeKeypair, bool, error) {
	if s.HasKeypair() {
		ck, err := s.CompositeKeypair()
		if err == nil {
			return ck, false, nil
		}
		if s.AuthToken != "" {
			return nil, false, fmt.Errorf("%w: %w", ErrUnusableRegisteredKeypair, err)
		}
		// No token: nothing is pinned, so a broken keypair is replaced below.
	} else if s.AuthToken != "" {
		return nil, false, fmt.Errorf("%w: %w", ErrUnusableRegisteredKeypair,
			errors.New("the keypair is missing"))
	}

	ck, err := s.GenerateKeypair()
	if err != nil {
		return nil, false, err
	}
	return ck, true, nil
}

// LoadOptions supplies flags and configuration hooks for a command.
type LoadOptions struct {
	ConfigureFlags func(*flag.FlagSet)
	// AfterFlagsParsed runs after valid flags and metadata, before configuration I/O.
	AfterFlagsParsed func(*flag.FlagSet) error
}

// Load parses worker configuration from defaults, config file, env vars, and CLI flags.
// Returns the config, whether -version was requested, and any error.
func Load(args []string) (*Config, bool, error) {
	return LoadWithOptions(args, LoadOptions{})
}

// LoadWithOptions parses mode flags and caller-owned command flags together.
// Only mode flags enter the configuration values.
func LoadWithOptions(args []string, opts LoadOptions) (*Config, bool, error) {
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
		fmt.Sprintf("how many agents to bring back at once after a restart "+
			"(0 = %d, or the CPU count when that is lower); "+
			"it never delays an agent you start yourself", DefaultMaxStartupConcurrency))
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
	if opts.ConfigureFlags != nil {
		opts.ConfigureFlags(fs)
	}
	if err := internalconfig.ConfigureAndParse(fs, args, "Run a Worker connected to a Hub.", usageCategories, workerFlagCategoryOrder); err != nil {
		return nil, false, err
	}

	if *showVersion {
		return nil, true, nil
	}
	if opts.AfterFlagsParsed != nil {
		if err := opts.AfterFlagsParsed(fs); err != nil {
			return nil, false, err
		}
	}
	configPath := fs.Lookup("config").Value.String()

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

	// The worker has no list-typed environment variable: every key it reads
	// holds one value.
	if err := internalconfig.Load(k, defaults, configPath, "LEAPMUX_WORKER_", nil, fp); err != nil {
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

	// Ensure data dir exists, durably: the directory that will hold
	// registration credentials must be published before any write below
	// names a file inside it.
	if err := atomicfile.MkdirAllDurable(c.DataDir); err != nil {
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

// RemoveSensitiveRecord removes a credentials file and every temporary
// copy an interrupted write left beside it. It always attempts both
// removals -- even when one fails -- joins their failures, and succeeds
// for files that are already gone, so a retry finishes a partial
// cleanup without knowing which half failed.
//
// These operations remove accessible files; they do not promise physical
// secure erasure.
func RemoveSensitiveRecord(path string) error {
	return removeSensitiveRecord(path, os.Remove, atomicfile.RemoveTempFiles, atomicfile.SyncDirectory)
}

func removeSensitiveRecord(path string, remove func(string) error, removeCopies func(string) error, syncDirectory func(string) error) error {
	removeErr := remove(path)
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		removeErr = fmt.Errorf("remove %s: %w", path, removeErr)
	} else if removeErr != nil {
		removeErr = nil
	}
	copiesErr := removeCopies(path)
	syncErr := syncDirectory(filepath.Dir(path))
	if errors.Is(syncErr, os.ErrNotExist) {
		syncErr = nil
	}
	if syncErr != nil {
		syncErr = fmt.Errorf("confirm removal of %s: %w", path, syncErr)
	}
	return errors.Join(removeErr, copiesErr, syncErr)
}

// ClearState removes the persisted credentials and every interrupted
// copy beside them.
func (c *Config) ClearState() error {
	return RemoveSensitiveRecord(c.StatePath())
}

// SaveState durably persists the credentials: a reader never sees a
// half-written file, the mode 0600 survives the replacement, and the
// directory entry that names the file is published before success is
// reported.
func (c *Config) SaveState(s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFileDurable(c.StatePath(), data, 0o600)
}
