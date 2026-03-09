package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/leapmux/leapmux/internal/logging"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/config"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/service"
)

func runWorker(args []string) error {
	cfg, showVersion, err := config.Load(args)
	if err != nil {
		return err
	}

	if showVersion {
		fmt.Println(version)
		return nil
	}

	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	logging.PrintBanner("worker", version, cfg.HubURL)

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Check if we already have credentials from a previous registration.
	state, err := cfg.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if state == nil {
		state = &config.State{}
	}

	// Ensure the worker has an X25519 keypair for E2EE channels.
	// Generated before registration so the public key can be sent to the Hub.
	generated, err := state.EnsureKeypair()
	if err != nil {
		return fmt.Errorf("ensure keypair: %w", err)
	}
	if generated {
		if err := cfg.SaveState(state); err != nil {
			return fmt.Errorf("save state with keypair: %w", err)
		}
	}

	if state.AuthToken == "" {
		// No saved credentials — need to register.
		publicKey, pkErr := state.PublicKeyBytes()
		if pkErr != nil {
			return fmt.Errorf("decode public key for registration: %w", pkErr)
		}
		result, regErr := hub.Register(ctx, cfg.HubURL, version, publicKey)
		if regErr != nil {
			return fmt.Errorf("registration: %w", regErr)
		}

		state.WorkerID = result.WorkerID
		state.AuthToken = result.AuthToken
		state.OrgID = result.OrgID

		if err := cfg.SaveState(state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}

		slog.Info("credentials saved", "path", cfg.StatePath())
	}

	privateKey, err := state.PrivateKeyBytes()
	if err != nil {
		return fmt.Errorf("decode private key: %w", err)
	}
	publicKey, err := state.PublicKeyBytes()
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}

	slog.Info("starting worker",
		"worker_id", state.WorkerID,
		"hub", cfg.HubURL,
		"key_fingerprint", noiseutil.KeyFingerprint(publicKey),
	)

	// Open the Worker-local database for persistent state.
	sqlDB, err := workerdb.Open(cfg.DBPath(), cfg.DBMaxConns)
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

	homeDir, _ := os.UserHomeDir()

	// Set up E2EE channel manager with service handlers.
	channelMgr := channel.NewManager(privateKey, publicKey, client.Send)
	if cfg.MaxMessageSize > 0 {
		channelMgr.SetMaxMessageSize(cfg.MaxMessageSize)
	}

	svcCtx := service.NewContext(
		sqlDB,
		client.AgentManager(),
		client.TerminalManager(),
		homeDir,
		cfg.DataDir,
	)
	svcCtx.WorkerID = state.WorkerID
	svcCtx.Name = cfg.Name
	svcCtx.Version = version
	svcCtx.AgentStartupTimeout = cfg.AgentStartupTimeout()
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
	client.PublicKey = publicKey

	client.OnDeregister = func() {
		slog.Info("worker deregistered by hub, clearing state and shutting down")
		_ = cfg.ClearState()
		stop()
	}

	client.ConnectWithReconnect(ctx, state.AuthToken)

	return nil
}
