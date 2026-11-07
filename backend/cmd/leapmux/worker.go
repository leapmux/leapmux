package main

import (
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
)

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

// clearDeletedWorkerState removes the registration of a Worker the Hub says
// is gone, including recovery material and every interrupted credential copy.
// An absent file is already the desired end state.
func clearDeletedWorkerState(cfg *config.Config) error {
	if err := config.ClearRegistration(cfg); err != nil {
		return fmt.Errorf("clear the worker registration at %s and %s: %w", cfg.StatePath(), cfg.PendingPath(), err)
	}
	return nil
}
