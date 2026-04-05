package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/config"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
	"github.com/leapmux/leapmux/util/version"
)

func runWorker(args []string) error {
	cfg, showVersion, err := config.Load(args)
	if err != nil {
		return err
	}

	if showVersion {
		fmt.Println(version.Value)
		return nil
	}

	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	logging.PrintBanner("worker", logging.VersionInfo{Version: version.Value, CommitHash: version.CommitHash, CommitTime: version.CommitTime, BuildTime: version.BuildTime})

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

	// Ensure the worker has a composite keypair for E2EE channels.
	// Generated before registration so the public keys can be sent to the Hub.
	generated, err := state.EnsureCompositeKeypair()
	if err != nil {
		return fmt.Errorf("ensure composite keypair: %w", err)
	}
	if generated {
		if err := cfg.SaveState(state); err != nil {
			return fmt.Errorf("save state with keypair: %w", err)
		}
	}

	if state.AuthToken == "" {
		// No saved credentials — need to register.
		compositeKey, ckErr := state.CompositeKeypair()
		if ckErr != nil {
			return fmt.Errorf("restore composite keypair for registration: %w", ckErr)
		}
		slhdsaPub, _ := compositeKey.SlhdsaPublicKeyBytes()
		result, regErr := hub.Register(ctx, cfg.HubURL, version.Value, compositeKey.X25519Public, compositeKey.MlkemPublicKeyBytes(), slhdsaPub)
		if regErr != nil {
			return fmt.Errorf("registration: %w", regErr)
		}

		state.WorkerID = result.WorkerID
		state.AuthToken = result.AuthToken
		state.RegisteredBy = result.RegisteredBy

		if err := cfg.SaveState(state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}

		slog.Info("credentials saved", "path", cfg.StatePath())
	}

	compositeKey, err := state.CompositeKeypair()
	if err != nil {
		return fmt.Errorf("restore composite keypair: %w", err)
	}

	slog.Info("starting worker",
		"worker_id", state.WorkerID,
		"hub", cfg.HubURL,
		"key_fingerprint", compositeKey.Fingerprint(),
		"encryption_mode", cfg.EncryptionMode,
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

	wakeLockTracker := wakelock.NewActivityTracker()
	defer wakeLockTracker.Close()

	// Set up E2EE channel manager with service handlers.
	encMode := cfg.EncryptionModeProto()

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
		compositeKey, encMode, client.Send,
		cfg.MaxMessageSize, cfg.MaxIncompleteChunked,
		func(channelID string) { svcCtx.Watchers.UnwatchAll(channelID) },
	)

	svcCtx.WorkerID = state.WorkerID
	svcCtx.RegisteredBy = state.RegisteredBy
	svcCtx.Name = cfg.Name
	svcCtx.AgentStartupTimeout = cfg.AgentStartupTimeout()
	svcCtx.APITimeout = cfg.APITimeout()
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
	client.EncryptionMode = encMode
	client.PublicKey = compositeKey.X25519Public
	if encMode != leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC {
		client.MlkemPublicKey = compositeKey.MlkemPublicKeyBytes()
		slhdsaPub, _ := compositeKey.SlhdsaPublicKeyBytes()
		client.SlhdsaPublicKey = slhdsaPub
	}

	client.OnDeregister = func() {
		slog.Info("worker deregistered by hub, clearing state and shutting down")
		_ = cfg.ClearState()
		stop()
	}

	client.ConnectWithReconnect(ctx, state.AuthToken)

	return nil
}
