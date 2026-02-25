package service

import "github.com/cenkalti/backoff/v5"

// OverrideAutoContinueBackoff replaces the backoff factory for testing.
// Returns a restore function that reinstates the original factory.
func OverrideAutoContinueBackoff(fn func() *backoff.ExponentialBackOff) func() {
	old := newAutoContinueBackoff
	newAutoContinueBackoff = fn
	return func() { newAutoContinueBackoff = old }
}

// IsSyntheticAPIError exposes isSyntheticAPIError for external tests.
func IsSyntheticAPIError(content []byte) bool {
	return isSyntheticAPIError(content)
}
