package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
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
		fmt.Println(version.Format())
		return nil
	}

	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	logging.PrintBanner("worker")

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	// Use a manually-cancelled context (rather than signal.NotifyContext)
	// so SIGTERM/SIGINT can run svcCtx.Shutdown() *before* the bidi stream
	// is torn down. Otherwise the disconnect-notice broadcasts emitted by
	// Shutdown race a closed connection and never reach watchers.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
		// Registration requires a key minted in advance by an authenticated
		// user via the hub UI; bare workers cannot self-register.
		if cfg.RegistrationKey == "" {
			return fmt.Errorf("worker is unregistered: pass --registration-key <key> from the hub UI")
		}
		compositeKey, ckErr := state.CompositeKeypair()
		if ckErr != nil {
			return fmt.Errorf("restore composite keypair for registration: %w", ckErr)
		}
		slhdsaPub, _ := compositeKey.SlhdsaPublicKeyBytes()
		result, regErr := hub.Register(ctx, cfg.HubURL, cfg.RegistrationKey, version.Value, compositeKey.X25519Public, compositeKey.MlkemPublicKeyBytes(), slhdsaPub)
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
	} else if cfg.RegistrationKey != "" {
		// The worker is already registered. Refusing to consume the key
		// here protects the user from accidentally burning it on a
		// machine that's already configured (a fresh `leapmux worker`
		// command in the wrong terminal, etc.).
		return fmt.Errorf("worker is already registered; remove --registration-key or wipe local state to re-register")
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
	sqlDB, err := workerdb.Open(cfg.DBPath(), cfg.DBConfig())
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
	// svcCtx.Shutdown persists terminal screen snapshots and broadcasts the
	// "Connection to the terminal was lost" notice to live watchers. Wrap it
	// in sync.Once so all exit paths (signal, OnDeregister, defer fallback)
	// converge on a single invocation that runs *before* the bidi stream is
	// torn down — otherwise the broadcast races a closed connection.
	var shutdownOnce sync.Once
	runShutdown := func() {
		shutdownOnce.Do(func() {
			slog.Info("draining worker state for graceful shutdown")
			svcCtx.Shutdown()
		})
	}
	defer runShutdown()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			slog.Info("shutdown signal received")
			runShutdown()
			cancel()
		case <-ctx.Done():
		}
	}()

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
		runShutdown()
		cancel()
	}

	client.ConnectWithReconnect(ctx, state.AuthToken)

	return nil
}
