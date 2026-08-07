package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/leapmux/leapmux/solo"
	"github.com/leapmux/leapmux/util/version"
)

func runSolo(args []string, soloMode bool) error {
	for _, a := range args {
		if a == "-version" || a == "--version" {
			fmt.Println(version.Format())
			return nil
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Everything but Args and DevMode is left unset, so solo.Start derives it from
	// DevMode: the listen address, the config dir and file, the CLI flag allowlist
	// and the worker-scoped extra flags. This file used to spell all of it out, and
	// the flag lists DRIFTED -- defaultCLIFlags gained max-connections-per-user,
	// max-workers-per-user and the two sqlite sizing knobs, the copy here did not,
	// and `leapmux solo --max-connections-per-user=64` answered "flag provided but
	// not defined" while solo's own test asserted the flag was exposed. One source
	// of truth, in the package whose comments explain why each default is what it is.
	inst, err := solo.Start(ctx, solo.Config{
		Args:    args,
		DevMode: !soloMode,
	})
	if err != nil {
		return err
	}
	return waitSolo(ctx, inst)
}

// soloInstance is the slice of *solo.Instance waitSolo needs, named so the wait
// logic is testable without standing up a Hub.
type soloInstance interface {
	// Wait blocks until the Hub's serve loop ends and returns its terminal error.
	Wait() error
	// Stop cancels the instance, drains it, and returns the same terminal error
	// Wait reports.
	Stop() error
}

// waitSolo blocks until the user signals or the Hub's serve loop ends, then shuts
// the instance down and reports the Hub's terminal error, if any.
//
// The deferred Stop is the SINGLE reporter of that error. Instance.Stop ends in
// Wait(), and hubErr is assigned exactly once, so Stop hands back the identical
// value the hubExited arm already observed -- returning it from the arm as well
// would join the error with itself and print the message twice to the user (main's
// handleRunError does a plain Fprintln of the joined error). Hence both select arms
// return nil: they choose WHEN to stop, never WHAT to report.
func waitSolo(ctx context.Context, inst soloInstance) (retErr error) {
	defer func() {
		stopErr := inst.Stop()
		// Belt-and-braces: a clean shutdown's http.ErrServerClosed cannot actually
		// reach here (Serve's recordListenerResult drops it before it is recorded as
		// hubErr), but filtering it in the one place that surfaces hubErr costs
		// nothing and keeps a normal Ctrl-C from ever being reported as a failure.
		if errors.Is(stopErr, http.ErrServerClosed) {
			stopErr = nil
		}
		retErr = errors.Join(retErr, stopErr)
	}()

	// Exit when the user signals or the Hub errors out.
	hubExited := make(chan error, 1)
	go func() { hubExited <- inst.Wait() }()
	select {
	case <-ctx.Done():
		return nil
	case <-hubExited:
		// The error is deliberately dropped here: the deferred Stop re-reads the very
		// same hubErr and reports it.
		return nil
	}
}
