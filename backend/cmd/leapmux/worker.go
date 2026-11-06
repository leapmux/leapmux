package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/leapmux/leapmux/util/version"
	"github.com/leapmux/leapmux/worker"
)

func runWorker(args []string) error {
	// Pre-dispatch the subcommands of the worker so the runtime parser
	// does not see flags meant for them. `register` owns registration and
	// pending recovery; `deregister` completes the remote removal before
	// the local one; `cross-worker-pins` manages the worker-local TOFU
	// pin store.
	if len(args) > 0 {
		switch args[0] {
		case "register":
			return runWorkerRegister(args[1:])
		case "deregister":
			return runWorkerDeregister(args[1:])
		case "cross-worker-pins":
			return runWorkerCrossWorkerPins(args[1:])
		}
	}
	return runWorkerProcess(args)
}

// runWorkerProcess runs the worker runtime. It never registers: a
// complete saved registration is required before anything starts, and a
// registration key or a missing registration is a refusal that names
// the command that owns registration.
func runWorkerProcess(args []string) error {
	// A long-lived daemon: a lost stderr (a dead tee, a closed terminal) must
	// return EPIPE from its log writes, not kill the process before the
	// goodbye reaches the Hub. The provider helper dispatch stays before this
	// in main, so the helper keeps its stdio behaviour.
	procutil.KeepRunningOnBrokenPipe()
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

	// The worker's data lease: acquired before the credentials are read,
	// so the storage directory is owned and validated for this process.
	// worker.Run takes its ownership and releases it after the database
	// closes.
	canonical, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return err
	}
	directory, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, canonical), lifecycle.RoleWorker, canonical)
	if err != nil {
		return fmt.Errorf("acquire the worker data directory: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The saved registration is the only credential source. Nothing
	// here generates keys, burns a registration key, or redirects the
	// credentials to another Hub: the state's endpoint is the endpoint
	// this process dials.
	state, err := cfg.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state == nil || state.AuthToken == "" || state.WorkerID == "" {
		return unregisteredError(cfg)
	}
	if cfg.RegistrationKey != "" {
		// A key on a runtime command would burn against a machine
		// that is already configured, so refuse it and name the
		// command that owns keys.
		return alreadyRegisteredError(cfg.StatePath())
	}
	if state.HubURL == "" {
		return fmt.Errorf("the saved registration names no hub; register again with `leapmux worker register`")
	}
	compositeKey, err := state.CompositeKeypair()
	if err != nil {
		return reregistrationAdvice(fmt.Errorf("%w: %w", config.ErrUnusableRegisteredKeypair, err), cfg.StatePath())
	}

	// ONE endpoint for the whole process, from the registration: the
	// Connect stream and every hub call a spawned agent makes share one
	// connection pool per protocol and one h2c verdict.
	hubEndpoint, err := hubtransport.New(state.HubURL)
	if err != nil {
		return fmt.Errorf("saved hub URL %q: %w", state.HubURL, err)
	}

	slog.Info("starting worker",
		"worker_id", state.WorkerID,
		"hub", state.HubURL,
		"key_fingerprint", compositeKey.Fingerprint(),
		"encryption_mode", cfg.EncryptionMode,
	)

	// A signal only cancels ctx: worker.Run keeps the bidi stream alive until its
	// shutdown has run, so the disconnect notices still reach the watchers.
	// The registration is never stopped, so a further signal during the drain
	// is dropped instead of killing a process that is releasing its state.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, procutil.ShutdownSignals()...)
	go func() {
		select {
		case <-sigCh:
			slog.Info("shutdown signal received")
			cancel()
		case <-ctx.Done():
		}
	}()

	// RegisteredBy is deliberately not set: the Hub delivers the owner on connect
	// (see Client.OnWorkerIdentity, wired by bootstrap.Wire) and is the only
	// authority for it.
	return reregistrationAdvice(worker.Run(ctx, worker.RunConfig{
		HubEndpoint:             hubEndpoint,
		Directory:               directory,
		AuthToken:               state.AuthToken,
		CompositeKey:            compositeKey,
		WorkerID:                state.WorkerID,
		Name:                    cfg.Name,
		DBMaxConns:              cfg.DBMaxConns,
		DBCacheSize:             cfg.DBCacheSize,
		DBMmapSize:              cfg.DBMmapSize,
		MaxIncompleteChunked:    cfg.MaxIncompleteChunked,
		MaxMessageSize:          cfg.MaxMessageSize,
		AgentStartupTimeout:     cfg.AgentStartupTimeout,
		APITimeout:              cfg.APITimeout,
		AgentStartupConcurrency: cfg.AgentStartupConcurrency,
		EncryptionMode:          cfg.EncryptionModeProto(),
		UseLoginShell:           cfg.UseLoginShell,
		// This is the only cleanup for WORKER_DELETED, and it also runs for a
		// worker deleted with its user, which was never deregistered. A discarded
		// error would leave state.json on disk and repeat the same silent failure
		// at every start.
		OnDeregister: func() {
			slog.Info("the hub told this worker to stop; clearing state and shutting down")
			if err := clearDeletedWorkerState(cfg); err != nil {
				slog.Error("failed to clear the worker state", "error", err)
			}
		},
	}), cfg.StatePath())
}

// unregisteredError names the command that owns registration.
func unregisteredError(cfg *config.Config) error {
	return fmt.Errorf("worker is unregistered: run `leapmux worker register --hub <url> --registration-key <key>` first")
}

// reregistrationSteps states the one remedy for a Worker whose keys cannot be
// used any more.
func reregistrationSteps(statePath string) string {
	return fmt.Sprintf("deregister the worker with `leapmux worker deregister`, delete %s, and register it again", statePath)
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
// gone, with every interrupted copy beside it. An absent file is already the
// desired end state, which RemoveSensitiveRecord reports as success.
func clearDeletedWorkerState(cfg *config.Config) error {
	if err := cfg.ClearState(); err != nil {
		return fmt.Errorf("clear the worker state at %s: %w", cfg.StatePath(), err)
	}
	return nil
}
