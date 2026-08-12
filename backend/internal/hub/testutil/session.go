package testutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// AssertSessionLifetime pins the contract that both writers of a session expiry
// keep -- CreateSession at the login and the interceptor at each slide.
//
// The lower bound is the promise the setting makes: the session lasts at least
// the configured lifetime past the request that wrote the expiry. The upper
// bound is what the slide throttle costs, and it is stated as a fraction rather
// than as the throttle constant, which the auth package keeps to itself: a
// slide waits at most a tenth of the session, so the row can carry at most a
// tenth more than the configured value.
//
// Stated as a range rather than as a formula, on purpose. A formula copied from
// the code under test passes whatever that code does, including passing the
// wrong lifetime through; this range fails the moment a mint site drops its
// argument and falls back to the default.
func AssertSessionLifetime(t *testing.T, before time.Time, lifetime time.Duration, expiresAt time.Time) {
	t.Helper()

	earliest := before.Add(lifetime)
	assert.False(t, expiresAt.Before(earliest),
		"a session must last at least its configured lifetime (%s) past the request that wrote it: want no earlier than %s, got %s",
		lifetime, earliest, expiresAt)

	// One minute of slack for the test's own clock, which reads `before` before
	// the call and cannot see the instant the row was written.
	latest := before.Add(lifetime + lifetime/10 + time.Minute)
	assert.True(t, expiresAt.Before(latest),
		"a session must not outlive its configured lifetime (%s) by more than the slide throttle: want before %s, got %s",
		lifetime, latest, expiresAt)
}
