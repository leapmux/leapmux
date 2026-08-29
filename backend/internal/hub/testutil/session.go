package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// ElevateSession stamps a live step-up window on one session row, so a test
// can reach a surface the elevation gate guards without running a ceremony.
//
// Through Sessions().Elevate, which is the statement production writes, so a
// test cannot elevate a session into a shape the hub would never produce. It
// asserts that exactly one row moved: a mistyped session id or the wrong
// owner otherwise leaves the session un-elevated and the test then measures
// the gate instead of the behavior behind it.
func ElevateSession(t *testing.T, st store.Store, sessionID, userID string) {
	t.Helper()

	owner, ok := userid.New(userID)
	require.True(t, ok, "a session owner id must be non-empty")

	now := time.Now().UTC()
	n, err := st.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID:          sessionID,
		UserID:             owner,
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the session must exist and belong to this user")
}

// ElevateAPIToken stamps a live step-up window on one command-line
// credential, so a test can reach a restricted surface without running the
// browser ceremony. The api_tokens twin of ElevateSession, and it holds the same two
// rules: it goes through the statement production writes, and it asserts that
// exactly one row moved.
//
// Elevate BEFORE the credential authenticates anything. The production leg
// evicts the cached UserInfo after it stamps the row (see
// OAuthServerHandler.elevateGrantedToken); this writes the row alone, so a
// credential that already made a request in the same process keeps serving
// the deadline it was cached with. A test that needs both paths mints two
// credentials rather than reusing one.
func ElevateAPIToken(t *testing.T, st store.Store, tokenID, userID string) {
	t.Helper()

	owner, ok := userid.New(userID)
	require.True(t, ok, "a credential owner id must be non-empty")

	now := time.Now().UTC()
	n, err := st.APITokens().Elevate(context.Background(), store.ElevateAPITokenParams{
		TokenID:            tokenID,
		UserID:             owner,
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the credential must exist and belong to this user")
}

// AssertSessionLifetime pins the contract that both writers of a session expiry
// keep -- CreateSession at the login and the interceptor at each slide.
//
// The lower limit is the promise the setting makes: the session lasts at least
// the configured lifetime past the request that wrote the expiry. The upper
// limit is what the slide throttle costs, and this helper states it as a
// fraction rather than as the throttle constant, which the auth package keeps
// to itself: a slide waits at most a tenth of the session, so the row can carry
// at most a tenth more than the configured value.
//
// A range rather than a formula, on purpose. A formula copied from
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

// ElevatedAdminContext returns a context that carries an ALREADY ELEVATED
// administrator, for a test that drives a restricted handler directly rather
// than through the interceptor.
//
// A test that goes over the wire elevates the real session row instead (see
// ElevateSession): that path exercises the interceptor, the cache and the
// store statement, which is what a test of the gate itself must do. This one
// is for a test whose subject is what the handler does AFTER the gate admits
// it -- a deregistration cascade, an effect fan-out -- where building a login
// and a session row is fixture setup that the assertions never read.
//
// The user row must exist, because the gate re-reads the acting account's
// credential epoch immediately before the write. The session id is
// deliberately synthetic: the gate TOLERATES a missing session row (an owner's
// own sign-out in another tab must not roll back a change they started), so the
// re-read passes and the elevation is what admits the call.
func ElevatedAdminContext(t *testing.T, ctx context.Context, userID string) context.Context {
	t.Helper()

	owner, ok := userid.New(userID)
	require.True(t, ok, "an actor id must be non-empty")

	now := time.Now().UTC()
	until := now.Add(auth.ElevationWindow)
	return auth.WithUser(ctx, &auth.UserInfo{
		ID:         owner,
		IsAdmin:    true,
		Credential: auth.SessionCredential("elevated-admin-context"),
		Elevation:  auth.NewElevation(&now, &until),
	})
}
