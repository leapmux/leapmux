package main

import (
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
)

// reregistrationSteps states the one remedy for a Worker whose registration
// must be replaced. `leapmux worker deregister` removes the saved credentials
// once the Hub confirms the deletion, so the remedy deletes no file by hand.
const reregistrationSteps = "run `leapmux worker deregister`, then `leapmux worker register` with a new registration key"

// reregistrationAdvice adds the re-registration steps to an error that no
// reconnect can fix: the Hub refused this worker's keys, or its saved keypair
// cannot be used. Every other error is returned unchanged.
func reregistrationAdvice(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, hub.ErrKeysRefused) || errors.Is(err, config.ErrUnusableRegisteredKeypair) {
		return fmt.Errorf("%w; to register this worker again, %s", err, reregistrationSteps)
	}
	return err
}

// alreadyRegisteredError refuses `leapmux worker register` on a worker that
// already has credentials. Wiping the state alone would leave the old row
// ACTIVE: it keeps counting against max_workers_per_user, stays in the owner's
// list as an offline Worker, and keeps its token valid for any copy of the old
// state.json.
func alreadyRegisteredError(state *config.State) error {
	return fmt.Errorf("this worker is already registered with %s; start it with `leapmux worker start` or `leapmux worker run`, or, to register it again, %s",
		state.HubURL, reregistrationSteps)
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
