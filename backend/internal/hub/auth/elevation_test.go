package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func ptrTime(t time.Time) *time.Time { return &t }

// TestNewElevation_AnAbsentDeadlineIsNeverElevated pins the one thing the
// constructor still decides.
//
// It used to take elevation_proven_at as a second argument and refuse half a
// pair. That guard moved into the schema -- user_sessions carries
// CHECK ((elevation_proven_at IS NULL) = (elevation_expires_at IS NULL)) in
// all three dialects -- so half a pair is now unwritable rather than tolerated
// at every read. storetest covers the constraint; this covers what is left.
func TestNewElevation_AnAbsentDeadlineIsNeverElevated(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	assert.True(t, auth.NewElevation(ptrTime(now), nil).IsZero())
	assert.False(t, auth.NewElevation(ptrTime(now), ptrTime(now.Add(time.Hour))).IsZero())

	// BOTH halves, or nothing. The schema's CHECK is the write-side home for
	// this, but a CHECK is not something the admission read can stand on:
	// TiDB parses and ignores one unless tidb_enable_check_constraint is ON,
	// and the hub's attempt to set that variable discards its error. A lone
	// deadline would otherwise admit a sensitive action on a row whose factor
	// was never proven.
	assert.True(t, auth.NewElevation(nil, ptrTime(now.Add(time.Hour))).IsZero(),
		"a deadline with no anchor is not an elevation")
	assert.True(t, auth.NewElevation(nil, nil).IsZero())
	assert.False(t, auth.NewElevation(nil, ptrTime(now.Add(time.Hour))).IsCurrent(now),
		"and it admits nothing")
}

// TestElevation_IsCurrentUsesAnExclusiveUpperLimit pins the boundary against
// auth.IsExpired, which the rest of the hub uses: a credential is invalid AT
// the recorded instant, not one clock tick afterward. Two predicates that
// disagree at exactly the deadline is the kind of difference nothing catches
// until it matters.
func TestElevation_IsCurrentUsesAnExclusiveUpperLimit(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e := auth.NewElevation(ptrTime(now), ptrTime(now))

	assert.True(t, e.IsCurrent(now.Add(-time.Nanosecond)))
	assert.False(t, e.IsCurrent(now), "the deadline instant itself is not current")
	assert.False(t, e.IsCurrent(now.Add(time.Nanosecond)))
	assert.False(t, auth.Elevation{}.IsCurrent(now), "the zero value is never current")
	assert.Equal(t, auth.IsExpired(now, now), !e.IsCurrent(now),
		"the two predicates must agree at the exact deadline")
}

// TestElevation_DeadlineHidesALapsedWindow pins that a client is never handed
// a deadline in the past: it would render as a live elevation the hub has
// already stopped honouring.
func TestElevation_DeadlineHidesALapsedWindow(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	live := auth.NewElevation(ptrTime(now), ptrTime(now.Add(time.Hour)))
	until, ok := live.Deadline(now)
	assert.True(t, ok)
	assert.Equal(t, now.Add(time.Hour), until)

	lapsed := auth.NewElevation(ptrTime(now), ptrTime(now.Add(-time.Hour)))
	until, ok = lapsed.Deadline(now)
	assert.False(t, ok)
	assert.True(t, until.IsZero())
}

// TestUserInfo_ElevatedRequiresAnElevatableCredential is the structural rule:
// a credential can be elevated only when it has a row to stamp and a person
// who can be prompted. Two kinds do -- a session cookie, and a command-line
// credential, which proves its factor in a browser through
// /oauth/step-up.
func TestUserInfo_ElevatedRequiresAnElevatableCredential(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	elevation := auth.NewElevation(ptrTime(now), ptrTime(now.Add(time.Hour)))

	var nilInfo *auth.UserInfo
	assert.False(t, nilInfo.Elevated(now), "an unauthenticated request is never elevated")

	session := &auth.UserInfo{
		ID:         userid.MustNew("usr_1"),
		Credential: auth.SessionCredential("s-1"),
		Elevation:  elevation,
	}
	assert.True(t, session.Elevated(now))

	cli := &auth.UserInfo{
		ID:         userid.MustNew("usr_1"),
		Credential: auth.APICredential("tok-1"),
		Elevation:  elevation,
	}
	assert.True(t, cli.Elevated(now), "a command-line credential carries a window of its own")

	// A DELEGATION bearer never can: a worker mints it for an agent that
	// reads untrusted input, and there is nobody present to re-authenticate
	// it. It is refused even carrying the field.
	delegated := &auth.UserInfo{
		ID:         userid.MustNew("usr_1"),
		Credential: auth.DelegationCredential("del-1", "worker-1"),
		Elevation:  elevation,
	}
	assert.False(t, delegated.Elevated(now), "a delegation bearer must still be refused")

	// Solo mode carries the zero CredentialIdentity, so it fails on the same
	// branch without a special case.
	solo := &auth.UserInfo{ID: userid.MustNew("usr_1"), Elevation: elevation}
	assert.False(t, solo.Elevated(now))
}

// TestElevationConstantsLimitOneAnother states the relation the design rests
// on rather than the two literals: a cap at or below the window would make
// the slide pointless, and the whole point of two columns is that the cap
// outlives one window.
func TestElevationConstantsLimitOneAnother(t *testing.T) {
	t.Parallel()

	assert.Greater(t, auth.ElevationMaxTotal, auth.ElevationWindow,
		"the absolute cap must outlast one window, or a slide could never extend anything")
	assert.Positive(t, auth.ElevationWindow)
}

// TestRefreshWindowFor pins the credential-lifetime clamp: a rotation moves
// the refresh window forward, but never past created_at + AbsoluteTokenLifetime.
// Without the clamp, a CLI that refreshes weekly keeps ONE browser consent
// alive for ever.
func TestRefreshWindowFor(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// A fresh credential: the ordinary window applies.
	assert.Equal(t, auth.RefreshTokenTTL, auth.RefreshWindowFor(now, now))

	// Young enough that the ceiling is still far away.
	young := now.Add(-24 * time.Hour)
	assert.Equal(t, auth.RefreshTokenTTL, auth.RefreshWindowFor(young, now))

	// Inside the last RefreshTokenTTL of its life: the ceiling applies, and
	// the window is exactly what remains.
	old := now.Add(-auth.AbsoluteTokenLifetime).Add(24 * time.Hour)
	assert.Equal(t, 24*time.Hour, auth.RefreshWindowFor(old, now))

	// Past the ceiling: non-positive, which the caller reads as "this
	// credential must sign in again".
	expired := now.Add(-auth.AbsoluteTokenLifetime).Add(-time.Hour)
	assert.LessOrEqual(t, auth.RefreshWindowFor(expired, now), time.Duration(0))

	// A zero created_at (a caller that did not load the row) yields the
	// ordinary window rather than an instantly-expired token: failing closed
	// here would revoke every live credential on a mapping slip.
	assert.Equal(t, auth.RefreshTokenTTL, auth.RefreshWindowFor(time.Time{}, now))

	assert.Greater(t, auth.AbsoluteTokenLifetime, auth.RefreshTokenTTL,
		"the ceiling must outlast the ordinary window, or an idle credential would die on the wrong rule")
}
