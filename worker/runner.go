// Package worker provides an exported entry point for running the
// LeapMux worker as a library (e.g. from the standalone binary).
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// RunConfig holds configuration for running the worker as a library.
type RunConfig struct {
	HubURL     string       // Hub server URL (e.g. "http://localhost:4327") or "unix:<socket-path>"
	DataDir    string       // Directory for persistent state
	AuthToken  string       // Pre-provisioned auth token (skip registration)
	HTTPClient *http.Client // Custom HTTP client (e.g. for Unix socket transport)
	PrivateKey []byte       // Worker's X25519 private key for E2EE channels
	PublicKey  []byte       // Worker's X25519 public key for E2EE channels
	WorkerID   string       // Worker ID (from registration)
	Name       string       // Worker display name (from LEAPMUX_WORKER_NAME, defaults to hostname)
	DBMaxConns int          // Maximum number of open database connections (0 = default)
}

// Run starts the worker and blocks until ctx is cancelled.
// If AuthToken is set, registration is skipped and the worker connects directly.
// If HTTPClient is set, it is used for ConnectRPC communication instead of the default.
func Run(ctx context.Context, cfg RunConfig) error {
	// Open the Worker-local database for persistent state.
	dbPath := filepath.Join(cfg.DataDir, "worker.db")
	sqlDB, err := workerdb.Open(dbPath, cfg.DBMaxConns)
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

	var client *hub.Client
	if cfg.HTTPClient != nil {
		client = hub.NewWithHTTPClient(cfg.HTTPClient, cfg.HubURL)
	} else {
		client = hub.New(cfg.HubURL)
	}
	defer client.Stop()

	// Set up E2EE channel manager if keys are provided.
	if len(cfg.PrivateKey) > 0 && len(cfg.PublicKey) > 0 {
		channelMgr := channel.NewManager(cfg.PrivateKey, cfg.PublicKey, client.Send)

		homeDir, _ := os.UserHomeDir()

		// Create the service context and register all inner RPC handlers.
		svcCtx := service.NewContext(
			sqlDB,
			client.AgentManager(),
			client.TerminalManager(),
			homeDir,
			cfg.DataDir,
		)
		svcCtx.WorkerID = cfg.WorkerID
		switch {
		case cfg.Name != "":
			svcCtx.Name = cfg.Name
		case os.Getenv("LEAPMUX_WORKER_NAME") != "":
			svcCtx.Name = os.Getenv("LEAPMUX_WORKER_NAME")
		default:
			hostname, _ := os.Hostname()
			svcCtx.Name = hostname
		}
		svcCtx.Send = client.Send
		svcCtx.Channels = channelMgr
		svcCtx.Init()
		// Shutdown must run before client.Stop() so terminal screen snapshots
		// are persisted while in-memory state is still available.
		defer svcCtx.Shutdown()

		dispatcher := channel.NewDispatcher()
		service.RegisterAll(dispatcher, svcCtx)
		channelMgr.SetDispatcher(dispatcher)

		// Clean up watchers when a channel closes.
		channelMgr.SetCloseCallback(func(channelID string) {
			svcCtx.Watchers.UnwatchAll(channelID)
		})

		client.SetChannelMgr(channelMgr)
		client.PublicKey = cfg.PublicKey

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
