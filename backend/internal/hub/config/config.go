package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/v2"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
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
	Addr                         string        `koanf:"addr"`
	DataDir                      string        `koanf:"data_dir"`
	DevFrontend                  string        `koanf:"dev_frontend"`
	MaxMessageSize               int           `koanf:"max_message_size"`
	MaxIncompleteChunked         int           `koanf:"max_incomplete_chunked"`
	LogLevel                     string        `koanf:"log_level"`
	SignupEnabled                bool          `koanf:"signup_enabled"`
	EmailVerificationRequired    bool          `koanf:"email_verification_required"`
	SmtpHost                     string        `koanf:"smtp_host"`
	SmtpPort                     int           `koanf:"smtp_port"`
	SmtpUsername                 string        `koanf:"smtp_username"`
	SmtpPassword                 string        `koanf:"smtp_password"`
	SmtpFromAddress              string        `koanf:"smtp_from_address"`
	SmtpUseTLS                   bool          `koanf:"smtp_use_tls"`
	APITimeoutSeconds            int           `koanf:"api_timeout_seconds"`
	AgentStartupTimeoutSeconds   int           `koanf:"agent_startup_timeout_seconds"`
	WorktreeCreateTimeoutSeconds int           `koanf:"worktree_create_timeout_seconds"`
	SecureCookies                bool          `koanf:"secure_cookies"`
	EncryptionKeyPath            string        `koanf:"encryption_key_path"`
	Storage                      StorageConfig `koanf:"storage"`
	SoloMode                     bool
	DevMode                      bool              // Dev mode: non-solo but with auto-bootstrapped admin
	Extras                       map[string]string // Extra flag values not in the hub Config struct
}

// StorageType identifies a storage backend.
type StorageType string

// Storage type constants for StorageConfig.Type.
const (
	StorageTypeSQLite      StorageType = "sqlite"
	StorageTypePostgres    StorageType = "postgres"
	StorageTypeMySQL       StorageType = "mysql"
	StorageTypeMongoDB     StorageType = "mongodb"
	StorageTypeDynamoDB    StorageType = "dynamodb"
	StorageTypeCockroachDB StorageType = "cockroachdb"
	StorageTypeYugabyteDB  StorageType = "yugabytedb"
	StorageTypeTiDB        StorageType = "tidb"
)

// validStorageTypes is the display string for valid storage.type values.
const validStorageTypes = "sqlite, postgres, mysql, mongodb, dynamodb, cockroachdb, yugabytedb, tidb"

// StorageConfig holds the storage backend configuration.
type StorageConfig struct {
	Type        StorageType    `koanf:"type"` // See StorageType* constants for valid values.
	SQLite      SQLiteConfig   `koanf:"sqlite"`
	Postgres    PostgresConfig `koanf:"postgres"`
	MySQL       MySQLConfig    `koanf:"mysql"`
	MongoDB     MongoDBConfig  `koanf:"mongodb"`
	DynamoDB    DynamoDBConfig `koanf:"dynamodb"`
	CockroachDB PostgresConfig `koanf:"cockroachdb"` // CockroachDB reuses the PostgreSQL provider.
	YugabyteDB  PostgresConfig `koanf:"yugabytedb"`  // YugabyteDB reuses the PostgreSQL provider.
	TiDB        MySQLConfig    `koanf:"tidb"`        // TiDB reuses the MySQL provider.
}

// SQLiteConfig holds SQLite-specific storage configuration.
type SQLiteConfig struct {
	Path      string `koanf:"path"`       // Database file path. Default: "{data_dir}/hub.db".
	MaxConns  int    `koanf:"max_conns"`  // Maximum open connections. Default: 4.
	CacheSize int    `koanf:"cache_size"` // Page cache size (negative = KiB, positive = pages). Default: SQLite default (-2000 = 2 MiB).
	MmapSize  int    `koanf:"mmap_size"`  // Memory-mapped I/O size in bytes. Default: 0 (disabled).
}

// PostgresConfig holds PostgreSQL-specific storage configuration.
// Also used by CockroachDB and YugabyteDB (wire-compatible).
type PostgresConfig struct {
	DSN                      string `koanf:"dsn"`                         // Connection string (required).
	MaxConns                 int    `koanf:"max_conns"`                   // Maximum open connections. Default: 25.
	MinConns                 int    `koanf:"min_conns"`                   // Minimum pool connections kept alive. Default: 5.
	ConnMaxLifetimeSeconds   int    `koanf:"conn_max_lifetime_seconds"`   // Maximum connection lifetime. Default: 3600.
	MaxConnIdleTimeSeconds   int    `koanf:"max_conn_idle_time_seconds"`  // Maximum idle time per connection. Default: 300.
	HealthCheckPeriodSeconds int    `koanf:"health_check_period_seconds"` // Pool health check period. Default: 30.
}

// MySQLConfig holds MySQL-specific storage configuration.
// Also used by TiDB (wire-compatible).
type MySQLConfig struct {
	DSN                    string `koanf:"dsn"`                        // Connection string (required).
	MaxConns               int    `koanf:"max_conns"`                  // Maximum open connections. Default: 25.
	MaxIdleConns           int    `koanf:"max_idle_conns"`             // Maximum idle connections. Default: 5.
	ConnMaxLifetimeSeconds int    `koanf:"conn_max_lifetime_seconds"`  // Maximum connection lifetime. Default: 3600.
	ConnMaxIdleTimeSeconds int    `koanf:"conn_max_idle_time_seconds"` // Maximum idle time per connection. Default: 300.
}

// MongoDBConfig holds MongoDB-specific storage configuration.
type MongoDBConfig struct {
	URI                           string `koanf:"uri"`                              // Connection URI (required when type=mongodb).
	Database                      string `koanf:"database"`                         // Database name (required when type=mongodb).
	MaxPoolSize                   int    `koanf:"max_pool_size"`                    // Maximum connection pool size. Default: 100.
	MinPoolSize                   int    `koanf:"min_pool_size"`                    // Minimum connection pool size. Default: 0.
	MaxConnIdleTimeSeconds        int    `koanf:"max_conn_idle_time_seconds"`       // Maximum idle time per connection. Default: 300.
	ServerSelectionTimeoutSeconds int    `koanf:"server_selection_timeout_seconds"` // Server selection timeout. Default: 30.
	TimeoutSeconds                int    `koanf:"timeout_seconds"`                  // Operation timeout. Default: 30.
	ReadConcern                   string `koanf:"read_concern"`                     // Read concern level (e.g. "local", "majority"). Default: driver default.
	WriteConcern                  string `koanf:"write_concern"`                    // Write concern (e.g. "majority", "1"). Default: driver default.
	RetryWrites                   *bool  `koanf:"retry_writes"`                     // Retry supported writes on transient errors. Default: driver default (true).
}

// DynamoDBConfig holds DynamoDB-specific storage configuration.
type DynamoDBConfig struct {
	Region              string `koanf:"region"`                 // AWS region (required when type=dynamodb).
	Endpoint            string `koanf:"endpoint"`               // Endpoint override (for local development).
	TablePrefix         string `koanf:"table_prefix"`           // Prefix for all table names. Default: "leapmux_".
	CreateTables        bool   `koanf:"create_tables"`          // Auto-create tables on Open(). Default: true.
	PointInTimeRecovery bool   `koanf:"point_in_time_recovery"` // Enable PITR on created tables. Default: false.
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

// ExtraFlagDef defines a string CLI flag that is not part of the hub's own
// config but should be parsed alongside it (e.g. worker-specific flags in
// solo mode).
type ExtraFlagDef struct {
	Name       string
	KoanfKey   string
	Usage      string
	StrDefault string // used when the flag is a string
}

// LoadOptions parameterizes differences between hub and solo/dev mode config loading.
type LoadOptions struct {
	DefaultAddr       string         // default listen address (hub: ":4327", solo: "127.0.0.1:4327")
	DefaultConfigDir  string         // for data_dir resolution (e.g. "~/.config/leapmux/solo")
	DefaultConfigFile string         // default config file path
	FlagSetName       string         // flag.NewFlagSet name ("hub" vs "leapmux")
	CLIFlags          []string       // if non-nil, only register these flags (solo exposes a subset)
	ExtraFlags        []ExtraFlagDef // additional flags not in the hub's allFlags list
	SoloMode          bool           // set on resulting Config
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

	// prefixFlags creates flagDefs by prepending a CLI and koanf prefix to
	// a set of base definitions, replacing "{name}" in usage strings.
	prefixFlags := func(cliPrefix, koanfPrefix, displayName string, base []flagDef) []flagDef {
		out := make([]flagDef, len(base))
		for i, f := range base {
			out[i] = flagDef{
				name:        cliPrefix + "-" + f.name,
				koanfKey:    koanfPrefix + "." + f.koanfKey,
				usage:       strings.Replace(f.usage, "{name}", displayName, 1),
				strDefault:  f.strDefault,
				intDefault:  f.intDefault,
				boolDefault: f.boolDefault,
			}
		}
		return out
	}

	// Base flag templates for PostgreSQL-compatible and MySQL-compatible backends.
	postgresBaseFlags := []flagDef{
		{"dsn", "dsn", "{name} connection string", ptrconv.Ptr(""), nil, nil},
		{"max-conns", "max_conns", "{name} maximum open connections", nil, ptrconv.Ptr(25), nil},
		{"min-conns", "min_conns", "{name} minimum pool connections kept alive", nil, ptrconv.Ptr(5), nil},
		{"conn-max-lifetime-seconds", "conn_max_lifetime_seconds", "{name} connection max lifetime in seconds", nil, ptrconv.Ptr(3600), nil},
		{"max-conn-idle-time-seconds", "max_conn_idle_time_seconds", "{name} max idle time per connection in seconds", nil, ptrconv.Ptr(300), nil},
		{"health-check-period-seconds", "health_check_period_seconds", "{name} pool health check period in seconds", nil, ptrconv.Ptr(30), nil},
	}
	mysqlBaseFlags := []flagDef{
		{"dsn", "dsn", "{name} connection string", ptrconv.Ptr(""), nil, nil},
		{"max-conns", "max_conns", "{name} maximum open connections", nil, ptrconv.Ptr(25), nil},
		{"max-idle-conns", "max_idle_conns", "{name} maximum idle connections", nil, ptrconv.Ptr(5), nil},
		{"conn-max-lifetime-seconds", "conn_max_lifetime_seconds", "{name} connection max lifetime in seconds", nil, ptrconv.Ptr(3600), nil},
		{"conn-max-idle-time-seconds", "conn_max_idle_time_seconds", "{name} max idle time per connection in seconds", nil, ptrconv.Ptr(300), nil},
	}

	allFlags := []flagDef{
		{"addr", "addr", "listen address", ptrconv.Ptr(addr), nil, nil},
		{"data-dir", "data_dir", "data directory", ptrconv.Ptr("."), nil, nil},
		{"dev-frontend", "dev_frontend", "Vite dev server URL for reverse proxy (dev mode only)", ptrconv.Ptr(""), nil, nil},
		{"max-message-size", "max_message_size", "maximum reassembled channel message size in bytes (default 16 MiB)", nil, ptrconv.Ptr(0), nil},
		{"max-incomplete-chunked", "max_incomplete_chunked", "maximum in-flight chunked sequences per channel (default 4)", nil, ptrconv.Ptr(0), nil},
		{"log-level", "log_level", "log level (debug, info, warn, error)", ptrconv.Ptr(defaultLogLevel), nil, nil},
		{"signup-enabled", "signup_enabled", "enable user sign-up", nil, nil, ptrconv.Ptr(false)},
		{"email-verification-required", "email_verification_required", "require email verification on sign-up", nil, nil, ptrconv.Ptr(false)},
		{"smtp-host", "smtp_host", "SMTP server host", ptrconv.Ptr(""), nil, nil},
		{"smtp-port", "smtp_port", "SMTP server port", nil, ptrconv.Ptr(587), nil},
		{"smtp-username", "smtp_username", "SMTP username", ptrconv.Ptr(""), nil, nil},
		{"smtp-password", "smtp_password", "SMTP password", ptrconv.Ptr(""), nil, nil},
		{"smtp-from-address", "smtp_from_address", "SMTP from address", ptrconv.Ptr(""), nil, nil},
		{"smtp-use-tls", "smtp_use_tls", "use TLS for SMTP", nil, nil, ptrconv.Ptr(true)},
		{"api-timeout-seconds", "api_timeout_seconds", "general API timeout in seconds", nil, ptrconv.Ptr(DefaultAPITimeoutSeconds), nil},
		{"agent-startup-timeout-seconds", "agent_startup_timeout_seconds", "agent startup timeout in seconds", nil, ptrconv.Ptr(DefaultAgentStartupTimeoutSeconds), nil},
		{"worktree-create-timeout-seconds", "worktree_create_timeout_seconds", "worktree creation timeout in seconds", nil, ptrconv.Ptr(DefaultWorktreeCreateTimeoutSeconds), nil},
		// Storage configuration
		{"storage-type", "storage.type", "storage backend type (" + validStorageTypes + ")", ptrconv.Ptr(""), nil, nil},
		// SQLite (default)
		{"storage-sqlite-path", "storage.sqlite.path", "SQLite database file path (default: {data_dir}/hub.db)", ptrconv.Ptr(""), nil, nil},
		{"storage-sqlite-max-conns", "storage.sqlite.max_conns", "SQLite maximum open connections", nil, ptrconv.Ptr(sqlitedb.DefaultMaxConns), nil},
		{"storage-sqlite-cache-size", "storage.sqlite.cache_size", "SQLite page cache size (negative = KiB, e.g. -64000 = 64 MiB)", nil, ptrconv.Ptr(0), nil},
		{"storage-sqlite-mmap-size", "storage.sqlite.mmap_size", "SQLite memory-mapped I/O size in bytes (0 = disabled)", nil, ptrconv.Ptr(0), nil},
		// MongoDB
		{"storage-mongodb-uri", "storage.mongodb.uri", "MongoDB connection URI", ptrconv.Ptr(""), nil, nil},
		{"storage-mongodb-database", "storage.mongodb.database", "MongoDB database name", ptrconv.Ptr(""), nil, nil},
		{"storage-mongodb-max-pool-size", "storage.mongodb.max_pool_size", "MongoDB maximum connection pool size", nil, ptrconv.Ptr(100), nil},
		{"storage-mongodb-min-pool-size", "storage.mongodb.min_pool_size", "MongoDB minimum connection pool size", nil, ptrconv.Ptr(0), nil},
		{"storage-mongodb-max-conn-idle-time-seconds", "storage.mongodb.max_conn_idle_time_seconds", "MongoDB max idle time per connection in seconds", nil, ptrconv.Ptr(300), nil},
		{"storage-mongodb-server-selection-timeout-seconds", "storage.mongodb.server_selection_timeout_seconds", "MongoDB server selection timeout in seconds", nil, ptrconv.Ptr(30), nil},
		{"storage-mongodb-timeout-seconds", "storage.mongodb.timeout_seconds", "MongoDB operation timeout in seconds", nil, ptrconv.Ptr(30), nil},
		{"storage-mongodb-read-concern", "storage.mongodb.read_concern", "MongoDB read concern level (e.g. local, majority)", ptrconv.Ptr(""), nil, nil},
		{"storage-mongodb-write-concern", "storage.mongodb.write_concern", "MongoDB write concern (e.g. majority, 1)", ptrconv.Ptr(""), nil, nil},
		{"storage-mongodb-retry-writes", "storage.mongodb.retry_writes", "retry supported MongoDB writes on transient errors", nil, nil, ptrconv.Ptr(true)},
		// DynamoDB
		{"storage-dynamodb-region", "storage.dynamodb.region", "DynamoDB AWS region", ptrconv.Ptr(""), nil, nil},
		{"storage-dynamodb-endpoint", "storage.dynamodb.endpoint", "DynamoDB endpoint override (for local dev)", ptrconv.Ptr(""), nil, nil},
		{"storage-dynamodb-table-prefix", "storage.dynamodb.table_prefix", "DynamoDB table name prefix", ptrconv.Ptr("leapmux_"), nil, nil},
		{"storage-dynamodb-create-tables", "storage.dynamodb.create_tables", "auto-create DynamoDB tables on startup", nil, nil, ptrconv.Ptr(true)},
		{"storage-dynamodb-point-in-time-recovery", "storage.dynamodb.point_in_time_recovery", "enable point-in-time recovery on created tables", nil, nil, ptrconv.Ptr(false)},
	}
	// PostgreSQL and PostgreSQL-compatible backends.
	allFlags = append(allFlags, prefixFlags("storage-postgres", "storage.postgres", "PostgreSQL", postgresBaseFlags)...)
	allFlags = append(allFlags, prefixFlags("storage-cockroachdb", "storage.cockroachdb", "CockroachDB", postgresBaseFlags)...)
	allFlags = append(allFlags, prefixFlags("storage-yugabytedb", "storage.yugabytedb", "YugabyteDB", postgresBaseFlags)...)
	// MySQL and MySQL-compatible backends.
	allFlags = append(allFlags, prefixFlags("storage-mysql", "storage.mysql", "MySQL", mysqlBaseFlags)...)
	allFlags = append(allFlags, prefixFlags("storage-tidb", "storage.tidb", "TiDB", mysqlBaseFlags)...)

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
	// Register extra flags (e.g. worker-specific flags in solo mode).
	for _, ef := range opts.ExtraFlags {
		fieldMap[ef.Name] = ef.KoanfKey
		defaults[ef.KoanfKey] = ef.StrDefault
		fs.String(ef.Name, ef.StrDefault, ef.Usage)
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

	// Populate extra flag values.
	if len(opts.ExtraFlags) > 0 {
		cfg.Extras = make(map[string]string, len(opts.ExtraFlags))
		for _, ef := range opts.ExtraFlags {
			cfg.Extras[ef.KoanfKey] = k.String(ef.KoanfKey)
		}
	}

	return &cfg, false, nil
}

// Validate checks the configuration values and ensures required directories exist.
func (c *Config) Validate() error {
	// Ensure data dir exists.
	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Validate storage configuration.
	requireField := func(value, field string) error {
		if value == "" {
			return fmt.Errorf("%s is required when storage.type is %s", field, c.Storage.Type)
		}
		return nil
	}
	switch c.Storage.Type {
	case "", StorageTypeSQLite:
		// No additional validation needed.
	case StorageTypePostgres:
		if err := requireField(c.Storage.Postgres.DSN, "storage.postgres.dsn"); err != nil {
			return err
		}
	case StorageTypeMySQL:
		if err := requireField(c.Storage.MySQL.DSN, "storage.mysql.dsn"); err != nil {
			return err
		}
	case StorageTypeMongoDB:
		if err := requireField(c.Storage.MongoDB.URI, "storage.mongodb.uri"); err != nil {
			return err
		}
		if err := requireField(c.Storage.MongoDB.Database, "storage.mongodb.database"); err != nil {
			return err
		}
	case StorageTypeDynamoDB:
		if err := requireField(c.Storage.DynamoDB.Region, "storage.dynamodb.region"); err != nil {
			return err
		}
	case StorageTypeCockroachDB:
		if err := requireField(c.Storage.CockroachDB.DSN, "storage.cockroachdb.dsn"); err != nil {
			return err
		}
	case StorageTypeYugabyteDB:
		if err := requireField(c.Storage.YugabyteDB.DSN, "storage.yugabytedb.dsn"); err != nil {
			return err
		}
	case StorageTypeTiDB:
		if err := requireField(c.Storage.TiDB.DSN, "storage.tidb.dsn"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported storage.type: %q (valid: %s)", c.Storage.Type, validStorageTypes)
	}

	return nil
}

// DefaultHubDataDir returns the default hub data directory with ~ expanded.
func DefaultHubDataDir() string {
	return internalconfig.ExpandHome(defaultConfigDir)
}

// SQLiteDBPath returns the path to the SQLite database file.
// Uses Storage.SQLite.Path if set, otherwise defaults to {DataDir}/hub.db.
func (c *Config) SQLiteDBPath() string {
	if c.Storage.SQLite.Path != "" {
		return c.Storage.SQLite.Path
	}
	return filepath.Join(c.DataDir, "hub.db")
}

// SQLiteDBConfig returns the SQLite configuration for sqlitedb.Open.
func (c *Config) SQLiteDBConfig() sqlitedb.Config {
	return sqlitedb.Config{
		MaxConns:  c.Storage.SQLite.MaxConns,
		CacheSize: c.Storage.SQLite.CacheSize,
		MmapSize:  c.Storage.SQLite.MmapSize,
	}
}

// EncryptionKeyFilePath returns the path to the encryption key ring file.
func (c *Config) EncryptionKeyFilePath() string {
	if c.EncryptionKeyPath != "" {
		return c.EncryptionKeyPath
	}
	return filepath.Join(c.DataDir, "encryption.key")
}

// BaseURL returns the scheme+host base URL derived from Addr and SecureCookies.
// A bare ":port" address is resolved to "localhost:port".
func (c *Config) BaseURL() string {
	scheme := "http"
	if c.SecureCookies {
		scheme = "https"
	}
	host := c.Addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	return scheme + "://" + host
}

// SocketPath returns the path to the Unix domain socket.
func (c *Config) SocketPath() string {
	return filepath.Join(c.DataDir, "hub.sock")
}
