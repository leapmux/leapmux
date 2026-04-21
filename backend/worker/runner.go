// Package worker provides an exported entry point for running the
// LeapMux worker as a library (e.g. from the solo/dev binary).
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/channel"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
)

// RunConfig holds configuration for running the worker as a library.
type RunConfig struct {
	HubURL string // Hub server URL: http[s]://host:port, unix:<socket-path>, or npipe:<name>

	DataDir              string                      // Directory for persistent state
	AuthToken            string                      // Pre-provisioned auth token (skip registration)
	CompositeKey         *noiseutil.CompositeKeypair // Worker's composite keypair for E2EE channels
	WorkerID             string                      // Worker ID (from registration)
	Name                 string                      // Worker display name (from LEAPMUX_WORKER_NAME, defaults to hostname)
	DBMaxConns           int                         // Maximum number of open database connections (0 = default)
	DBCacheSize          int                         // SQLite page cache size (negative = KiB; 0 = default)
	DBMmapSize           int                         // SQLite memory-mapped I/O size in bytes (0 = disabled)
	MaxMessageSize       int                         // Maximum reassembled channel message size in bytes (0 = 16 MiB default)
	MaxIncompleteChunked int                         // Maximum in-flight chunked sequences per channel (0 = 4 default)
	AgentStartupTimeout  time.Duration               // Timeout for agent startup handshake (0 = 5m default)
	APITimeout           time.Duration               // Timeout for JSON-RPC requests (0 = 10s default)
	EncryptionMode       leapmuxv1.EncryptionMode    // Encryption mode (classic, post-quantum)
	UseLoginShell        bool                        // Wrap claude invocation in user's login shell
	RegisteredBy         string                      // User ID who registered this worker (for tunnel authorization)
}

// Run starts the worker and blocks until ctx is cancelled.
// If AuthToken is set, registration is skipped and the worker connects directly.
func Run(ctx context.Context, cfg RunConfig) error {
	// Open the Worker-local database for persistent state.
	dbPath := filepath.Join(cfg.DataDir, "worker.db")
	sqlDB, err := workerdb.Open(dbPath, sqlitedb.Config{
		MaxConns:  cfg.DBMaxConns,
		CacheSize: cfg.DBCacheSize,
		MmapSize:  cfg.DBMmapSize,
	})
	if err != nil {
		return fmt.Errorf("open worker db: %w", err)
	}
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			slog.Error("failed to close worker db", "error", closeErr)
		}
	}()

	if err := workerdb.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate worker db: %w", err)
	}

	client := hub.New(cfg.HubURL)
	defer client.Stop()

	// Set up E2EE channel manager if composite key is provided.
	if cfg.CompositeKey != nil {
		homeDir, _ := os.UserHomeDir()

		wakeLockTracker := wakelock.NewActivityTracker()
		defer wakeLockTracker.Close()

		// Create the service context first so the close callback can reference it.
		svcCtx := service.NewContext(
			sqlDB,
			client.AgentManager(),
			client.TerminalManager(),
			homeDir,
			cfg.DataDir,
			wakeLockTracker,
		)

		channelMgr := channel.NewManager(
			cfg.CompositeKey, cfg.EncryptionMode, client.Send,
			cfg.MaxMessageSize, cfg.MaxIncompleteChunked,
			func(channelID string) { svcCtx.Watchers.UnwatchAll(channelID) },
		)

		svcCtx.WorkerID = cfg.WorkerID
		svcCtx.RegisteredBy = cfg.RegisteredBy
		switch {
		case cfg.Name != "":
			svcCtx.Name = cfg.Name
		case os.Getenv("LEAPMUX_WORKER_NAME") != "":
			svcCtx.Name = os.Getenv("LEAPMUX_WORKER_NAME")
		default:
			hostname, _ := os.Hostname()
			svcCtx.Name = hostname
		}
		svcCtx.AgentStartupTimeout = cfg.AgentStartupTimeout
		svcCtx.APITimeout = cfg.APITimeout
		svcCtx.UseLoginShell = cfg.UseLoginShell
		svcCtx.Send = client.Send
		svcCtx.Channels = channelMgr
		svcCtx.Init()
		// Shutdown must run before client.Stop() so terminal screen snapshots
		// are persisted while in-memory state is still available.
		defer svcCtx.Shutdown()

		dispatcher := channel.NewDispatcher()
		service.RegisterAll(dispatcher, svcCtx)
		channelMgr.SetDispatcher(dispatcher)

		client.SetChannelMgr(channelMgr)
		client.EncryptionMode = cfg.EncryptionMode
		client.PublicKey = cfg.CompositeKey.X25519Public
		client.MlkemPublicKey = cfg.CompositeKey.MlkemPublicKeyBytes()
		slhdsaPub, _ := cfg.CompositeKey.SlhdsaPublicKeyBytes()
		client.SlhdsaPublicKey = slhdsaPub

		// Provide workspace tab sync data on connect.
		queries := db.New(sqlDB)
		client.TabSyncProvider = func() *leapmuxv1.WorkspaceTabsSync {
			return buildTabSync(queries)
		}
	}

	// Start the periodic cleanup loop to hard-delete agents and terminals
	// that have been closed for longer than the retention period.
	service.StartCleanupLoop(ctx, db.New(sqlDB))

	client.ConnectWithReconnect(ctx, cfg.AuthToken)
	return nil
}

// buildTabSync constructs a WorkspaceTabsSync message from the worker's
// database: all agents and all terminals.
func buildTabSync(queries *db.Queries) *leapmuxv1.WorkspaceTabsSync {
	ctx := context.Background()
	var tabs []*leapmuxv1.WorkspaceTabEntry

	// Add agent tabs from DB (includes both active and closed agents).
	agents, err := queries.ListAllAgentIDsAndWorkspaces(ctx)
	if err == nil {
		for _, agent := range agents {
			tabs = append(tabs, &leapmuxv1.WorkspaceTabEntry{
				WorkspaceId: agent.WorkspaceID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:       agent.ID,
			})
		}
	}

	// Add terminal tabs from DB.
	terminals, err := queries.ListAllTerminals(ctx)
	if err == nil {
		for _, t := range terminals {
			tabs = append(tabs, &leapmuxv1.WorkspaceTabEntry{
				WorkspaceId: t.WorkspaceID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
				TabId:       t.ID,
			})
		}
	}

	return &leapmuxv1.WorkspaceTabsSync{Tabs: tabs}
}
