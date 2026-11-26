package main

import (
	"context"
	"fmt"
	"io"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/atomicfile"
)

// registerWorker registers the worker and exits. It never starts the
// worker and never configures a service.
//
// The durable order is the whole design:
//
//  0. The Hub URL parses, so a URL the transport cannot use writes nothing.
//  1. The data directory exists and is owned by this instance.
//  2. The operation lock serializes this command against every other
//     lifecycle command of the same directory.
//  3. A pending attempt record -- the identity, the endpoint, the key,
//     and the complete keypair -- is durably published BEFORE any
//     network request.
//  4. The registration protocol runs, recovering a committed attempt
//     whose response was lost.
//  5. The registered state is durably published, and only then is the
//     pending record removed. A failure in between leaves the pending
//     record as the recovery material of the NEXT run.
type workerRegisterResult struct {
	Status    string `json:"status"`
	WorkerID  string `json:"worker_id"`
	HubURL    string `json:"hub_url"`
	StatePath string `json:"state_path"`
}

func registerWorker(ctx context.Context, printer *cli.Printer, cfg *config.Config, commandOptions workerRegisterOptions, files workerRegistrationFiles) (*workerRegisterResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := lifecycleProseOut(printer)
	if printer == nil || printer.Format == cli.FormatText {
		printModeBanner("worker")
	}
	logging.Setup()

	// Parse the endpoint before anything is written. A pending record for a
	// URL that the transport cannot use would block the corrected command as
	// a conflicting attempt. Every later request goes to this endpoint: a
	// reused pending record must name the same URL (checked below).
	endpoint, err := hubtransport.New(commandOptions.hubURL)
	if err != nil {
		return nil, fmt.Errorf("hub URL %q: %w", commandOptions.hubURL, err)
	}
	defer endpoint.CloseIdleConnections()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	// The data lease and the operation lock together are what the
	// registration changes its credentials under: the lease keeps every
	// other mode and instance out of this directory, the lock keeps
	// every other lifecycle command out of this transaction.
	canonical, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return nil, err
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

	// A complete registration that survived a crash while its pending
	// record did not get cleaned up: the registered state wins, the
	// pending record must agree, and the cleanup finishes without a new
	// registration.
	state, stateErr := files.LoadState()
	if stateErr != nil {
		return nil, fmt.Errorf("load state: %w", stateErr)
	}
	if state != nil && !state.Registered() {
		return nil, fmt.Errorf("the saved registration is incomplete; preserve %s and recover its registration before registering again", cfg.StatePath())
	}
	if state != nil {
		pending, pendingErr := files.LoadPendingRegistration()
		if pendingErr != nil {
			return nil, pendingErr
		}
		if pending != nil {
			if err := pendingAgreesWithState(cfg, pending, state); err != nil {
				return nil, err
			}
		}
		copies, err := atomicfile.TempFiles(cfg.PendingPath())
		if err != nil {
			return nil, fmt.Errorf("inspect interrupted registration copies: %w", err)
		}
		interrupted := pending != nil || len(copies) != 0
		if interrupted {
			// A prior rename may have succeeded before its directory flush failed.
			// Reconfirm durable credentials before deleting recovery material.
			if err := files.SaveState(state); err != nil {
				return nil, fmt.Errorf("confirm durable saved credentials: %w", err)
			}
		}
		if err := files.RemovePendingRegistration(); err != nil {
			return nil, fmt.Errorf("finish the interrupted registration's cleanup: %w", err)
		}
		if interrupted {
			printWorkerRegistration(out, cfg)
			return &workerRegisterResult{
				Status:    "registered",
				WorkerID:  state.WorkerID,
				HubURL:    state.HubURL,
				StatePath: cfg.StatePath(),
			}, nil
		}
		return nil, alreadyRegisteredError(state)
	}

	// Load or durably publish the pending attempt record. A record from
	// a cancelled, timed-out, or crashed run is REUSED: its attempt
	// identity and keypair are what the hub may already have committed.
	pending, err := files.LoadPendingRegistration()
	if err != nil {
		return nil, err
	}
	if pending == nil {
		attemptID, err := lifecycle.NewAttemptID()
		if err != nil {
			return nil, err
		}
		pending = &config.PendingRegistration{
			AttemptID: attemptID,
			HubURL:    commandOptions.hubURL,
			RegKey:    commandOptions.registrationKey,
		}
		if _, err := pending.GenerateKeypair(); err != nil {
			return nil, fmt.Errorf("generate the worker keypair: %w", err)
		}
		// Durable publication before the first network request. A
		// failure here means zero requests left this machine.
		if err := files.SavePendingRegistration(pending); err != nil {
			return nil, fmt.Errorf("publish the pending registration: %w", err)
		}
	} else if pending.HubURL != commandOptions.hubURL || pending.RegKey != commandOptions.registrationKey {
		// Never silently replace a conflicting pending identity: the
		// operator must decide whether the old attempt is abandoned.
		return nil, fmt.Errorf(
			"a pending registration for hub %s already exists in %s; it belongs to an attempt that may already have succeeded -- rerun the same command to recover it, or delete the file to abandon it",
			pending.HubURL, cfg.PendingPath())
	}

	compositeKey, err := pending.CompositeKeypair()
	if err != nil {
		return nil, fmt.Errorf("restore the pending keypair: %w", err)
	}
	opts, err := hub.RegistrationOptionsFromKeypair(pending.AttemptID, pending.RegKey, compositeKey)
	if err != nil {
		return nil, err
	}

	opts.Deadline = commandOptions.timeout
	result, err := hub.Register(ctx, endpoint, opts)
	if err != nil {
		failure := cli.FromError(fmt.Errorf("registration: %w", err))
		if failure.Code == cli.CodeNotLoggedIn {
			// The registration key is the credential of this call.
			failure = failure.WithHint("generate a new registration key in the Register worker dialog of the Hub, and run the command with it")
		}
		return nil, failure
	}

	// Publish the complete registered state BEFORE removing the pending
	// record: a crash between the two leaves both, and the branch above
	// finishes the cleanup on the next run.
	state = &config.State{
		HubURL:      pending.HubURL,
		WorkerID:    result.WorkerID,
		AuthToken:   result.AuthToken,
		KeyMaterial: pending.KeyMaterial,
	}
	if err := files.SaveState(state); err != nil {
		// The Hub committed registration, but durable local publication is
		// unconfirmed. Keep the pending record so the next run can recover.
		return nil, fmt.Errorf("save state: %w", err)
	}
	if err := files.RemovePendingRegistration(); err != nil {
		return nil, fmt.Errorf(
			"the worker registered and its credentials were saved, but the pending record %s could not be removed: %w; rerun `leapmux daemon worker register` to finish the cleanup",
			cfg.PendingPath(), err)
	}

	printWorkerRegistration(out, cfg)
	printWorkerNextCommands(out, cfg, commandOptions.configFile, []string{"start", "status"}, nil)
	return &workerRegisterResult{
		Status:    "registered",
		WorkerID:  state.WorkerID,
		HubURL:    state.HubURL,
		StatePath: cfg.StatePath(),
	}, nil
}

func printWorkerRegistration(w io.Writer, cfg *config.Config) {
	_, _ = fmt.Fprintln(w, "Worker registered.")
	_, _ = fmt.Fprintf(w, "Credentials saved to %s\n", cfg.StatePath())
}

// printWorkerNextCommands prints the commands that act on this Worker next,
// selected by its configuration file and its data directory. A saved service
// adds serviceVerbs. A data directory that cannot be resolved prints nothing,
// because the credential command already succeeded.
func printWorkerNextCommands(w io.Writer, cfg *config.Config, configFile string, verbs, serviceVerbs []string) {
	dataDirectory, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return
	}
	printSelectedNextCommandsTo(w, "worker", configFile, dataDirectory, nativeController, verbs, serviceVerbs)
}

// pendingAgreesWithState checks that a surviving pending record and a
// complete registered state describe one registration: the same hub and
// the same pinned keys. A disagreement is never silently resolved.
func pendingAgreesWithState(cfg *config.Config, pending *config.PendingRegistration, state *config.State) error {
	if pending.HubURL != state.HubURL {
		return fmt.Errorf("the pending registration identifies hub %s but the saved registration identifies %s; delete %s only if you know the saved one is yours",
			pending.HubURL, state.HubURL, cfg.PendingPath())
	}
	pendingKey, err := pending.CompositeKeypair()
	if err != nil {
		return err
	}
	pendingPub, err := workerkeys.Of(pendingKey)
	if err != nil {
		return err
	}
	stateKey, err := state.CompositeKeypair()
	if err != nil {
		return err
	}
	statePub, err := workerkeys.Of(stateKey)
	if err != nil {
		return err
	}
	if !workerkeys.Equal(pendingPub, statePub) {
		return fmt.Errorf("the pending registration's keys differ from the saved registration's keys; delete %s only if you know the saved one is yours", cfg.PendingPath())
	}
	return nil
}
