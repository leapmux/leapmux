// Package errwrap provides error wrapping and cancellation classification
// shared across the Hub, CLI, and desktop teardown paths.
package errwrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// IsCancellation reports an explicit cancellation with no independent cause.
// SQL cleanup can report an already-ended transaction after automatic rollback.
func IsCancellation(err error) bool {
	return IsCancellationWith(err, nil)
}

// IsCancellationWith also accepts protocol-specific cancellation verdicts.
// Every cause must be cancellation or an automatic-rollback artifact, and at
// least one cause must indicate cancellation. Wrappers cannot hide siblings.
func IsCancellationWith(err error, cancellationVerdict func(error) bool) bool {
	found, only := cancellationCauses(err, cancellationVerdict)
	return found && only
}

func cancellationCauses(err error, cancellationVerdict func(error) bool) (found, only bool) {
	if err == nil {
		return false, true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			childFound, childOnly := cancellationCauses(cause, cancellationVerdict)
			found = found || childFound
			if !childOnly {
				return found, false
			}
		}
		return found, true
	}
	if cancellationVerdict != nil && cancellationVerdict(err) {
		return true, true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if cause := wrapped.Unwrap(); cause != nil {
			return cancellationCauses(cause, cancellationVerdict)
		}
	}
	if errors.Is(err, context.Canceled) {
		return true, true
	}
	return false, errors.Is(err, sql.ErrTxDone)
}

// Wrap returns nil when err is nil; otherwise an error formatted as
// "<message>: <err>" that wraps err so errors.Is / errors.As traverse it.
//
// It is the single definition of the nil-safe "fmt.Errorf("%s: %w", …)" idiom
// that the hub server, revocation watcher, and desktop sidecar each previously
// inlined as wrapServerError / wrapError / wrapDesktopError. Centralizing it
// here means the "nil returns nil, else wrap once" contract cannot drift
// between those teardown paths.
func Wrap(err error, message string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}
