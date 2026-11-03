package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/worker/bootstrap"
	"github.com/leapmux/leapmux/internal/worker/config"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/util/clockjump"
	"github.com/leapmux/leapmux/util/version"
)

func runWorker(args []string) error {
	// Pre-dispatch the subcommands of the worker so the daemon parser
	// does not see flags meant for them. Today the only such subcommand is
	// `cross-worker-pins`, which manages the worker-local TOFU pin store
	// without spinning up a worker process.
	if len(args) > 0 && args[0] == "cross-worker-pins" {
		return runWorkerCrossWorkerPins(args[1:])
	}
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
	// so SIGTERM/SIGINT can run wiring.Shutdown() *before* the bidi stream
	// is torn down. Otherwise the disconnect-notice broadcasts emitted by
	// Shutdown race a closed connection and never reach watchers.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Report the periods in which this process did not run, so a suspended machine
	// explains the ended streams and refused credentials that follow it. This
	// entry point needs its own call: worker.Run has one, but only solo goes
	// through worker.Run -- `leapmux worker` builds its client here instead.
	clockjump.StartLoop(ctx)

	// Check if we already have credentials from a previous registration.
	state, err := cfg.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if state == nil {
		state = &config.State{}
	}

	// Ensure the worker has a composite keypair for E2EE channels. It is
	// generated before registration so its public keys are what the Hub pins.
	compositeKey, generated, err := state.EnsureCompositeKeypair()
	if err != nil {
		return reregistrationAdvice(err, cfg.StatePath())
	}
	if generated {
		if err := cfg.SaveState(state); err != nil {
			return fmt.Errorf("save state with keypair: %w", err)
		}
	}
	// The public keys the Hub pins at registration and compares on every
	// connect.
	keys, err := workerkeys.Of(compositeKey)
	if err != nil {
		return fmt.Errorf("derive the worker's public keys: %w", err)
	}

	// ONE endpoint for the whole process: registration, the worker's own
	// Connect stream and every hub call a spawned agent makes share one
	// connection pool per protocol and one h2c verdict.
	hubEndpoint, err := hubtransport.New(cfg.HubURL)
	if err != nil {
		return fmt.Errorf("hub URL %q: %w", cfg.HubURL, err)
	}

	if state.AuthToken == "" {
		// Registration requires a key minted in advance by an authenticated
		// user via the hub UI; bare workers cannot self-register.
		if cfg.RegistrationKey == "" {
			return fmt.Errorf("worker is unregistered: pass --registration-key <key> from the hub UI")
		}
		result, regErr := hub.Register(ctx, hubEndpoint, cfg.RegistrationKey, keys)
		if regErr != nil {
			return fmt.Errorf("registration: %w", regErr)
		}

		state.WorkerID = result.WorkerID
		state.AuthToken = result.AuthToken

		if err := cfg.SaveState(state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		slog.Info("credentials saved", "path", cfg.StatePath())
	} else if cfg.RegistrationKey != "" {
		// The worker is already registered. Refusing to consume the key
		// here protects the user from accidentally burning it on a
		// machine that's already configured (a fresh `leapmux worker`
		// command in the wrong terminal, etc.).
		return alreadyRegisteredError(cfg.StatePath())
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

	if err := workerdb.Migrate(ctx, sqlDB); err != nil {
		return fmt.Errorf("migrate worker db: %w", err)
	}

	client := hub.New(hubEndpoint)
	defer client.Stop()

	homeDir, _ := os.UserHomeDir()

	wakeLockTracker := wakelock.NewActivityTracker()
	defer wakeLockTracker.Close()

	// Set up E2EE channel manager with service handlers.
	encMode := cfg.EncryptionModeProto()

	// SeedRegisteredBy is deliberately not set: the Hub delivers the owner
	// on connect (see Client.OnWorkerIdentity, wired by Wire) and is the
	// only authority for it. Everything else this entry point needs --
	// remote IPC, the orphan reconciler, the retention loops -- comes from
	// the shared sequence rather than being re-listed here, which is how
	// this entry point silently lost svc.ControlIPC before.
	wiring := bootstrap.Wire(bootstrap.Params{
		Ctx:                     ctx,
		Client:                  client,
		DB:                      sqlDB,
		CompositeKey:            compositeKey,
		EncryptionMode:          encMode,
		MaxIncompleteChunked:    cfg.MaxIncompleteChunked,
		MaxMessageSize:          cfg.MaxMessageSize,
		WorkerID:                state.WorkerID,
		Name:                    cfg.Name,
		HomeDir:                 homeDir,
		DataDir:                 cfg.DataDir,
		AuthToken:               state.AuthToken,
		AgentStartupTimeout:     cfg.AgentStartupTimeout,
		APITimeout:              cfg.APITimeout,
		AgentStartupConcurrency: cfg.AgentStartupConcurrency,
		UseLoginShell:           cfg.UseLoginShell,
		WakeLock:                wakeLockTracker,
	})
	// wiring.Shutdown persists terminal screen snapshots, broadcasts the
	// "[Worker disconnected - Press Enter to restart]" notice to live watchers,
	// and flushes the outbound queue those broadcasts land in. Wrap it in
	// sync.Once so all exit paths (signal, OnDeregister, defer fallback)
	// converge on a single invocation that runs *before* the bidi stream is
	// torn down — otherwise the broadcast races a closed connection.
	var shutdownOnce sync.Once
	runShutdown := func() {
		shutdownOnce.Do(func() {
			slog.Info("draining worker state for graceful shutdown")
			wiring.Shutdown()
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

	// This is the only cleanup for WORKER_DELETED, and it also runs for a worker
	// deleted with its user, which was never deregistered. A discarded error
	// would leave state.json on disk and repeat the same silent failure at every
	// start.
	client.OnDeregister = func() {
		slog.Info("the hub told this worker to stop; clearing state and shutting down")
		if err := clearDeletedWorkerState(cfg); err != nil {
			slog.Error("failed to clear the worker state", "error", err)
		}
		runShutdown()
		cancel()
	}

	if err := client.ConnectWithReconnect(ctx, state.AuthToken); err != nil {
		return reregistrationAdvice(err, cfg.StatePath())
	}

	return nil
}

// reregistrationSteps states the one remedy for a Worker whose keys cannot be
// used any more.
func reregistrationSteps(statePath string) string {
	return fmt.Sprintf("deregister the worker, delete %s, and register it again", statePath)
}

// reregistrationAdvice adds the re-registration steps to an error that no
// reconnect can fix: the Hub refused this worker's keys, or its saved keypair
// cannot be used. Every other error is returned unchanged.
func reregistrationAdvice(err error, statePath string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, hub.ErrKeysRefused) || errors.Is(err, config.ErrUnusableRegisteredKeypair) {
		return fmt.Errorf("%w; to re-register it, %s", err, reregistrationSteps(statePath))
	}
	return err
}

// alreadyRegisteredError refuses a --registration-key on a worker that already
// has credentials. Wiping the state alone would leave the old row ACTIVE: it
// keeps counting against max_workers_per_user, stays in the owner's list as an
// offline Worker, and keeps its token valid for any copy of the old state.json.
func alreadyRegisteredError(statePath string) error {
	return fmt.Errorf("worker is already registered; remove --registration-key, or %s", reregistrationSteps(statePath))
}

// clearDeletedWorkerState removes the state file of a Worker the Hub says is
// gone. An absent file is already the desired end state.
func clearDeletedWorkerState(cfg *config.Config) error {
	if err := cfg.ClearState(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear the worker state at %s: %w", cfg.StatePath(), err)
	}
	return nil
}
