package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/lifecycle"
)

// deregisterWorker removes this worker's registration. The remote
// deletion completes BEFORE the local credentials go, and the worker
// must be stopped: a running process would keep dialing a hub that no
// longer knows it, and the local teardown it performs mid-run is not
// this command's to race.
//
// The worker database is preserved: deregistration removes the
// registration, not the machine's sessions and local state.
type workerDeregisterResult struct {
	Status    string `json:"status"`
	WorkerID  string `json:"worker_id,omitempty"`
	HubURL    string `json:"hub_url,omitempty"`
	StatePath string `json:"state_path"`
}

func deregisterWorker(ctx context.Context, printer *cli.Printer, cfg *config.Config, commandOptions workerCredentialOptions, files workerRegistrationFiles) (*workerDeregisterResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := lifecycleProseOut(printer)
	if printer == nil || printer.Format == cli.FormatText {
		printModeBanner("worker")
	}
	logging.Setup()
	ctx, cancel := context.WithTimeout(ctx, commandOptions.timeout)
	defer cancel()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	canonical, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	// A stopped worker only: the runtime lock proves no process runs.
	paths := lifecycle.PathsOf(canonical)
	held, probeErr := lifecycle.ProbeRuntimeLock(paths)
	if probeErr != nil {
		return nil, probeErr
	}
	if held {
		return nil, fmt.Errorf("the worker is running; stop it with `leapmux daemon worker stop` before deregistering")
	}

	lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, canonical), lifecycle.RoleWorker, canonical)
	if err != nil {
		return nil, fmt.Errorf("acquire the data directory: %w", err)
	}
	release, err := lifecycle.OperationLock(canonical)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	defer release()
	defer func() { _ = lease.Close() }()

	state, err := files.LoadState()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	if state != nil && !state.Registered() {
		return nil, fmt.Errorf("the saved registration is incomplete; preserve %s and recover its registration before deregistering", cfg.StatePath())
	}
	if state == nil {
		pending, pendingErr := files.LoadPendingRegistration()
		if pendingErr != nil {
			return nil, pendingErr
		}
		if pending != nil {
			return nil, fmt.Errorf("the pending registration in %s may already be committed; complete `leapmux daemon worker register` with the same Hub and key before deregistering", cfg.PendingPath())
		}
		// A previous confirmed deletion may have removed state.json before
		// temporary-copy cleanup failed. Finish both cleanup operations.
		if err := errors.Join(files.ClearState(), files.RemovePendingRegistration()); err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintln(out, "Worker is not registered; nothing to remove.")
		return &workerDeregisterResult{
			Status:    "not_registered",
			StatePath: cfg.StatePath(),
		}, nil
	}

	endpoint, err := hubtransport.New(state.HubURL)
	if err != nil {
		// The credentials stay: an unusable endpoint is not proof the
		// deletion happened.
		return nil, fmt.Errorf("saved hub URL %q: %w", state.HubURL, err)
	}
	// An error here keeps the credentials. The hub's retained deleted
	// row accepts a repeat call, so a retry completes rather than wedges.
	defer endpoint.CloseIdleConnections()
	if err := hub.Deregister(ctx, endpoint, state.AuthToken); err != nil {
		failure := cli.FromError(err)
		if failure.Code == cli.CodeNotLoggedIn {
			// The Hub refused the saved token, so it cannot delete this
			// Worker on the Worker's own request.
			failure = failure.WithHint("remove this Worker in the Worker list of the Hub, then delete %s", cfg.StatePath())
		}
		return nil, failure
	}

	// The Hub confirmed deletion. Remove every local credential copy and
	// preserve the worker database.
	if err := config.ClearRegistration(files); err != nil {
		return nil, fmt.Errorf("the hub removed this worker, but its registration at %s and %s could not be cleared: %w; rerun `leapmux daemon worker deregister` to finish",
			cfg.StatePath(), cfg.PendingPath(), err)
	}

	_, _ = fmt.Fprintln(out, "Worker deregistered.")
	_, _ = fmt.Fprintf(out, "Credentials removed from %s; the worker database is preserved\n", cfg.StatePath())
	_, _ = fmt.Fprintln(out, "To register this worker again, run `leapmux daemon worker register` with a new registration key.")
	printWorkerNextCommands(out, cfg, commandOptions.configFile, nil, []string{"disable"})
	return &workerDeregisterResult{
		Status:    "deregistered",
		WorkerID:  state.WorkerID,
		HubURL:    state.HubURL,
		StatePath: cfg.StatePath(),
	}, nil
}
