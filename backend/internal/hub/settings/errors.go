package settings

import "fmt"

// The write path's two error classes, typed so an API boundary can map
// them without matching on message text. A substring match reclassifies
// itself the moment someone rewords an error: the failure is silent, the
// compiler says nothing, and an actionable InvalidArgument degrades into
// a generic 500.
//
// BOTH settings scopes use these. The account scope (internal/hub/
// usersettings) carried a hand-mirrored copy of InvalidError, so a handler
// that learned about one twin classified the other as a 500. One type
// means one errors.As at every boundary.

// InvalidError marks a value-level rejection — an unregistered key, a
// partial document that will not merge, a value the key's own rules or a
// cross-key rule refuse, or a write that would leave an empty row. The
// caller supplied it, so the caller can fix it.
type InvalidError struct {
	err error
}

func (e *InvalidError) Error() string { return e.err.Error() }

// Unwrap exposes the underlying decode, merge, or validate failure so
// callers can errors.Is/As into it.
func (e *InvalidError) Unwrap() error { return e.err }

// Invalidf builds an InvalidError from a format string. It is the ONE
// constructor: `%w` carries the underlying decode, merge, or validate
// failure through, so wrapping needs no second entry point.
//
// Exported because the account scope shares this class from its own
// package (internal/hub/usersettings).
func Invalidf(format string, args ...any) error {
	return &InvalidError{err: fmt.Errorf(format, args...)}
}

// SecretUndecryptableError marks a stored secret the active keystore
// cannot decrypt. The write must STOP rather than destroy the only copy
// of an operator-entered secret, and the message carries the recovery
// instructions, so this is its own class and not an InvalidError.
type SecretUndecryptableError struct {
	err error
}

func (e *SecretUndecryptableError) Error() string { return e.err.Error() }
func (e *SecretUndecryptableError) Unwrap() error { return e.err }

func secretUndecryptablef(format string, args ...any) error {
	return &SecretUndecryptableError{err: fmt.Errorf(format, args...)}
}
