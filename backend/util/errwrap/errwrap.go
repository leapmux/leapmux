// Package errwrap provides a nil-safe error-wrapping helper shared across the
// hub, revocation watcher, and desktop sidecar teardown paths.
package errwrap

import "fmt"

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
