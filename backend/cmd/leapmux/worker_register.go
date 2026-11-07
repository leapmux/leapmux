package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/lifecycle"
)

// runWorkerRegister registers the worker and exits. It never starts the
// worker and never configures a service.
//
// The durable order is the whole design:
//
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
func runWorkerRegister(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return runWorkerRegisterWithFiles(ctx, args, configuredWorkerRegistrationFiles)
}

func runWorkerRegisterWithFiles(ctx context.Context, args []string, filesFor func(*config.Config) workerRegistrationFiles) error {
	cfg, commandOptions, err := loadWorkerCredentialCommand(args, hub.DefaultRegistrationDeadline)
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

	// --hub defaults to the loopback hub in the configuration layer, so
	// its PRESENCE is what the register command requires: registering
	// against a default the operator never specified would burn the key on
	// the wrong hub.
	if !commandOptions.explicitHub {
		return fmt.Errorf("--hub is required: the hub this worker registers with")
	}
	if cfg.RegistrationKey == "" {
		return fmt.Errorf("--registration-key is required: mint one in the hub UI")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	// The data lease and the operation lock together are what the
	// registration changes its credentials under: the lease keeps every
	// other mode and instance out of this directory, the lock keeps
	// every other lifecycle command out of this transaction.
	canonical, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return err
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

	// A complete registration that survived a crash while its pending
	// record did not get cleaned up: the registered state wins, the
	// pending record must agree, and the cleanup finishes without a new
	// registration.
	state, stateErr := files.LoadState()
	if stateErr != nil {
		return fmt.Errorf("load state: %w", stateErr)
	}
	if state != nil && !state.Registered() {
		return fmt.Errorf("the saved registration is incomplete; preserve %s and recover its registration before registering again", cfg.StatePath())
	}
	if state != nil {
		pending, pendingErr := files.LoadPendingRegistration()
		if pendingErr != nil {
			return pendingErr
		}
		if pending != nil {
			if err := pendingAgreesWithState(cfg, pending, state); err != nil {
				return err
			}
		}
		copies, err := atomicfile.TempFiles(cfg.PendingPath())
		if err != nil {
			return fmt.Errorf("inspect interrupted registration copies: %w", err)
		}
		interrupted := pending != nil || len(copies) != 0
		if interrupted {
			// A prior rename may have succeeded before its directory flush failed.
			// Reconfirm durable credentials before deleting recovery material.
			if err := files.SaveState(state); err != nil {
				return fmt.Errorf("confirm durable saved credentials: %w", err)
			}
		}
		if err := files.RemovePendingRegistration(); err != nil {
			return fmt.Errorf("finish the interrupted registration's cleanup: %w", err)
		}
		if interrupted {
			printWorkerRegistration(cfg)
			return nil
		}
		return alreadyRegisteredError(cfg.StatePath())
	}

	// Load or durably publish the pending attempt record. A record from
	// a cancelled, timed-out, or crashed run is REUSED: its attempt
	// identity and keypair are what the hub may already have committed.
	pending, err := files.LoadPendingRegistration()
	if err != nil {
		return err
	}
	if pending == nil {
		attemptID, err := lifecycle.NewAttemptID()
		if err != nil {
			return err
		}
		pending = &config.PendingRegistration{
			AttemptID: attemptID,
			HubURL:    cfg.HubURL,
			RegKey:    cfg.RegistrationKey,
		}
		if _, err := pending.GenerateKeypair(); err != nil {
			return fmt.Errorf("generate the worker keypair: %w", err)
		}
		// Durable publication before the first network request. A
		// failure here means zero requests left this machine.
		if err := files.SavePendingRegistration(pending); err != nil {
			return fmt.Errorf("publish the pending registration: %w", err)
		}
	} else if pending.HubURL != cfg.HubURL || pending.RegKey != cfg.RegistrationKey {
		// Never silently replace a conflicting pending identity: the
		// operator must decide whether the old attempt is abandoned.
		return fmt.Errorf(
			"a pending registration for hub %s already exists in %s; it belongs to an attempt that may already have succeeded -- rerun the same command to recover it, or delete the file to abandon it",
			pending.HubURL, cfg.PendingPath())
	}

	compositeKey, err := pending.CompositeKeypair()
	if err != nil {
		return fmt.Errorf("restore the pending keypair: %w", err)
	}
	opts, err := hub.RegistrationOptionsFromKeypair(pending.AttemptID, pending.RegKey, compositeKey)
	if err != nil {
		return err
	}

	endpoint, err := hubtransport.New(pending.HubURL)
	if err != nil {
		return fmt.Errorf("hub URL %q: %w", pending.HubURL, err)
	}
	defer endpoint.CloseIdleConnections()
	opts.Deadline = commandOptions.timeout
	result, err := hub.Register(ctx, endpoint, opts)
	if err != nil {
		return fmt.Errorf("registration: %w", err)
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
		return fmt.Errorf("save state: %w", err)
	}
	if err := files.RemovePendingRegistration(); err != nil {
		return fmt.Errorf(
			"the worker registered and its credentials were saved, but the pending record %s could not be removed: %w; rerun `leapmux worker register` to finish the cleanup",
			cfg.PendingPath(), err)
	}

	printWorkerRegistration(cfg)
	return nil
}

func printWorkerRegistration(cfg *config.Config) {
	fmt.Println("Worker registered.")
	fmt.Printf("Credentials saved to %s\n", cfg.StatePath())
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
