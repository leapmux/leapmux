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
	return errors.Is(err, context.Canceled) && cancellationOnly(err)
}

func cancellationOnly(err error) bool {
	if err == nil {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if !cancellationOnly(cause) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if cause := wrapped.Unwrap(); cause != nil {
			return cancellationOnly(cause)
		}
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrTxDone)
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
