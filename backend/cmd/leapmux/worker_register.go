package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/logging"
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
	cfg, _, err := config.Load(args)
	if err != nil {
		return err
	}
	logging.Setup()

	// --hub defaults to the loopback hub in the configuration layer, so
	// its PRESENCE is what the register command requires: registering
	// against a default the operator never named would burn the key on
	// the wrong hub.
	if !flagPresent(args, "hub") {
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
	if state, stateErr := cfg.LoadState(); stateErr == nil && state != nil && state.Registered() {
		pending, pendingErr := cfg.LoadPendingRegistration()
		if pendingErr == nil && pending != nil {
			if err := pendingAgreesWithState(cfg, pending, state); err != nil {
				return err
			}
			if err := cfg.RemovePendingRegistration(); err != nil {
				return fmt.Errorf("finish the interrupted registration's cleanup: %w", err)
			}
		}
		return alreadyRegisteredError(cfg.StatePath())
	} else if stateErr != nil {
		return fmt.Errorf("load state: %w", stateErr)
	}

	// Load or durably publish the pending attempt record. A record from
	// a cancelled, timed-out, or crashed run is REUSED: its attempt
	// identity and keypair are what the hub may already have committed.
	pending, err := cfg.LoadPendingRegistration()
	if err != nil {
		return err
	}
	if pending == nil {
		attemptID, err := newAttemptID()
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
		if err := cfg.SavePendingRegistration(pending); err != nil {
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
	result, err := hub.Register(context.Background(), endpoint, opts)
	if err != nil {
		return fmt.Errorf("registration: %w", err)
	}

	// Publish the complete registered state BEFORE removing the pending
	// record: a crash between the two leaves both, and the branch above
	// finishes the cleanup on the next run.
	state := &config.State{
		HubURL:      pending.HubURL,
		WorkerID:    result.WorkerID,
		AuthToken:   result.AuthToken,
		KeyMaterial: pending.KeyMaterial,
	}
	if err := cfg.SaveState(state); err != nil {
		// The credentials exist on the hub but not here; the pending
		// record stays as the recovery material of the next run.
		return fmt.Errorf("save state: %w", err)
	}
	if err := cfg.RemovePendingRegistration(); err != nil {
		return fmt.Errorf(
			"the worker registered and its credentials were saved, but the pending record %s could not be removed: %w; rerun `leapmux worker register` to finish the cleanup",
			cfg.PendingPath(), err)
	}

	fmt.Println("Worker registered.")
	fmt.Printf("Credentials saved to %s\n", cfg.StatePath())
	return nil
}

// pendingAgreesWithState checks that a surviving pending record and a
// complete registered state describe one registration: the same hub and
// the same pinned keys. A disagreement is never silently resolved.
func pendingAgreesWithState(cfg *config.Config, pending *config.PendingRegistration, state *config.State) error {
	if pending.HubURL != state.HubURL {
		return fmt.Errorf("the pending registration names hub %s but the saved registration names %s; delete %s only if you know the saved one is yours",
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

// newAttemptID mints the durable attempt identity: 32 lowercase
// hexadecimal characters.
func newAttemptID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("mint an attempt identity: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// flagPresent reports whether args name the flag explicitly, as
// --name, --name=value, or -name.
func flagPresent(args []string, name string) bool {
	for _, arg := range args {
		trimmed := strings.TrimLeft(arg, "-")
		if trimmed == name || strings.HasPrefix(trimmed, name+"=") {
			return true
		}
	}
	return false
}
