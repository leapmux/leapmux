package main

import (
	"context"
	"fmt"

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
	cfg, _, err := config.Load(args)
	if err != nil {
		return err
	}
	logging.Setup()

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	canonical, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return err
	}

	// A stopped worker only: the runtime lock proves no process runs.
	paths := lifecycle.PathsOf(canonical)
	if held, probeErr := lifecycle.ProbeRuntimeLock(paths); probeErr == nil && held {
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

	state, err := cfg.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state == nil || !state.Registered() {
		// Nothing is registered here. Still sweep the leftovers, so the
		// command leaves the directory clean of registration material.
		if err := cfg.RemovePendingRegistration(); err != nil {
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
	if err := hub.Deregister(context.Background(), endpoint, state.AuthToken); err != nil {
		return err
	}

	// The hub no longer knows this worker; remove every local copy of
	// the credentials. The worker database stays.
	if err := cfg.ClearState(); err != nil {
		return fmt.Errorf("the hub removed this worker, but its local credentials at %s could not be cleared: %w; rerun `leapmux worker deregister` to finish",
			cfg.StatePath(), err)
	}
	if err := cfg.RemovePendingRegistration(); err != nil {
		return fmt.Errorf("the hub removed this worker, but the pending record %s could not be cleared: %w; rerun `leapmux worker deregister` to finish",
			cfg.PendingPath(), err)
	}

	fmt.Println("Worker deregistered.")
	fmt.Printf("Credentials removed from %s; the worker database is preserved\n", cfg.StatePath())
	return nil
}
