package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/lifecycle"
)

// runWorkerDeregister removes this worker's registration. The remote
// deletion completes BEFORE the local credentials go, and the worker
// must be stopped: a running process would keep dialing a hub that no
// longer knows it, and the local teardown it performs mid-run is not
// this command's to race.
//
// The worker database is preserved: deregistration removes the
// registration, not the machine's sessions and local state.
func runWorkerDeregister(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return runWorkerDeregisterWithFiles(ctx, args, configuredWorkerRegistrationFiles)
}

func runWorkerDeregisterWithFiles(ctx context.Context, args []string, filesFor func(*config.Config) workerRegistrationFiles) error {
	cfg, options, err := loadWorkerCredentialCommand(args, defaultStopTimeout)
	if handleVersionQuery(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	logging.Setup()
	files := filesFor(cfg)
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	canonical, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return err
	}

	// A stopped worker only: the runtime lock proves no process runs.
	paths := lifecycle.PathsOf(canonical)
	held, probeErr := lifecycle.ProbeRuntimeLock(paths)
	if probeErr != nil {
		return probeErr
	}
	if held {
		return fmt.Errorf("the worker is running; stop it with `leapmux worker stop` before deregistering")
	}

	lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, canonical), lifecycle.RoleWorker, canonical)
	if err != nil {
		return fmt.Errorf("acquire the data directory: %w", err)
	}
	release, err := lifecycle.OperationLock(canonical)
	if err != nil {
		_ = lease.Close()
		return err
	}
	defer release()
	defer func() { _ = lease.Close() }()

	state, err := files.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state != nil && !state.Registered() {
		return fmt.Errorf("the saved registration is incomplete; preserve %s and recover its registration before deregistering", cfg.StatePath())
	}
	if state == nil {
		pending, pendingErr := files.LoadPendingRegistration()
		if pendingErr != nil {
			return pendingErr
		}
		if pending != nil {
			return fmt.Errorf("the pending registration in %s may already be committed; complete `leapmux worker register` with the same Hub and key before deregistering", cfg.PendingPath())
		}
		// A previous confirmed deletion may have removed state.json before
		// temporary-copy cleanup failed. Finish both cleanup operations.
		if err := errors.Join(files.ClearState(), files.RemovePendingRegistration()); err != nil {
			return err
		}
		fmt.Println("Worker is not registered; nothing to remove.")
		return nil
	}

	endpoint, err := hubtransport.New(state.HubURL)
	if err != nil {
		// The credentials stay: an unusable endpoint is not proof the
		// deletion happened.
		return fmt.Errorf("saved hub URL %q: %w", state.HubURL, err)
	}
	// An error here keeps the credentials. The hub's retained deleted
	// row accepts a repeat call, so a retry completes rather than wedges.
	defer endpoint.CloseIdleConnections()
	if err := hub.Deregister(ctx, endpoint, state.AuthToken); err != nil {
		return err
	}

	// The Hub confirmed deletion. Remove every local credential copy and
	// preserve the worker database.
	if err := files.RemovePendingRegistration(); err != nil {
		return fmt.Errorf("the hub removed this worker, but the pending record %s could not be cleared: %w; rerun `leapmux worker deregister` to finish",
			cfg.PendingPath(), err)
	}
	// Retain the token until recovery-material cleanup succeeds, so a retry
	// can authenticate against the Hub's retained deletion record.
	if err := files.ClearState(); err != nil {
		return fmt.Errorf("the hub removed this worker, but its local credentials at %s could not be cleared: %w; rerun `leapmux worker deregister` to finish",
			cfg.StatePath(), err)
	}

	fmt.Println("Worker deregistered.")
	fmt.Printf("Credentials removed from %s; the worker database is preserved\n", cfg.StatePath())
	return nil
}
