package auth

import (
	"context"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRefreshApplyTo_WritesSlidExpiry(t *testing.T) {
	expires := time.Now().Add(DefaultSessionDuration).UTC().Truncate(time.Second)
	h := &connect.Header{}

	sessionRefresh{token: "sess-123", expiresAt: expires}.applyTo(h, false)

	values := h.Values("Set-Cookie")
	require.Len(t, values, 2)
	parsed, err := http.ParseSetCookie(values[0])
	require.NoError(t, err)
	assert.Equal(t, CookieName, parsed.Name)
	assert.Equal(t, "sess-123", parsed.Value)
	assert.True(t, parsed.HttpOnly)
	assert.Equal(t, expires, parsed.Expires.UTC())
	assert.Positive(t, parsed.MaxAge, "the browser must keep the cookie, not drop it at the end of the browser session")
}

func TestSessionRefreshApplyTo_UsesSecureCookieName(t *testing.T) {
	h := &connect.Header{}

	sessionRefresh{token: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, true)

	parsed, err := http.ParseSetCookie(h.Get("Set-Cookie"))
	require.NoError(t, err)
	assert.Equal(t, SecureCookieName, parsed.Name)
	assert.True(t, parsed.Secure)
}

func TestSessionRefreshApplyTo_ZeroValueWritesNothing(t *testing.T) {
	h := &connect.Header{}

	sessionRefresh{}.applyTo(h, false)

	assert.Empty(t, h.Values("Set-Cookie"), "a request that slid nothing must not touch the browser's cookie")
}

// A handler that wrote its own session cookie is authoritative. Logout is the
// case that matters: a browser applies both Set-Cookie headers for one name in
// order and keeps the last, so appending a refresh after the clearing cookie
// would leave the user signed in.
func TestSessionRefreshApplyTo_KeepsHandlerCookie(t *testing.T) {
	h := &connect.Header{}
	h.Set("Set-Cookie", ClearSessionCookie(false).String())

	sessionRefresh{token: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, false)

	values := h.Values("Set-Cookie")
	require.Len(t, values, 1, "the refresh must not append a second cookie for the same name")
	parsed, err := http.ParseSetCookie(values[0])
	require.NoError(t, err)
	assert.Empty(t, parsed.Value)
	assert.Negative(t, parsed.MaxAge, "the clearing cookie must survive the refresh")
}

// Either session spelling can carry a handler decision, but an unrelated cookie
// must not stop the refresh. A guard that stood back from
// every Set-Cookie would silently stop refreshing the session on each response
// that also carried, say, an OAuth state cookie.
func TestSessionRefreshApplyTo_WritesAlongsideAnUnrelatedCookie(t *testing.T) {
	h := &connect.Header{}
	h.Add("Set-Cookie", "oauth-state=abc; Path=/")

	sessionRefresh{token: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, false)

	values := h.Values("Set-Cookie")
	require.Len(t, values, 3, "the unrelated cookie must not suppress the refresh")
	parsed, err := http.ParseSetCookie(values[1])
	require.NoError(t, err)
	assert.Equal(t, CookieName, parsed.Name)
	assert.Equal(t, "sess-123", parsed.Value)
}

// A cookie this code cannot read counts as a collision. Losing one refresh
// costs a throttle window; overwriting a cookie meant for the browser could
// sign a user in again after a logout.
func TestSessionRefreshApplyTo_StandsBackFromAnUnreadableCookie(t *testing.T) {
	h := &connect.Header{}
	h.Add("Set-Cookie", "this is not a cookie")

	sessionRefresh{token: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, false)

	assert.Len(t, h.Values("Set-Cookie"), 1, "an unparseable cookie must fail closed")
}

// A handler's result is authoritative for both session spellings.
func TestSessionRefreshApplyTo_MatchesEitherSessionName(t *testing.T) {
	h := &connect.Header{}
	h.Add("Set-Cookie", ClearSessionCookie(false).String())
	sessionRefresh{token: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, true)
	require.Len(t, h.Values("Set-Cookie"), 1)
	parsed, err := http.ParseSetCookie(h.Get("Set-Cookie"))
	require.NoError(t, err)
	assert.Equal(t, CookieName, parsed.Name)
	assert.Negative(t, parsed.MaxAge)
}

// A session must be able to slide BEFORE it expires. This is the property a
// fixed five-minute throttle broke: a session shorter than the throttle expired
// with the user still active, because no request in its whole life was ever
// allowed to write the slide. The floor alone does not save it -- the throttle
// has to scale with the session.
func TestTouchThresholdAlwaysLeavesRoomToSlide(t *testing.T) {
	for _, configured := range []time.Duration{
		0, -time.Hour,
		MinSessionDuration,
		MinSessionDuration + time.Second,
		6 * time.Minute,
		time.Hour,
		DefaultSessionDuration,
		365 * 24 * time.Hour,
	} {
		lifetime := sessionLifetime(configured)
		threshold := touchThreshold(configured)

		assert.Positive(t, threshold, "configured=%s", configured)
		assert.Less(t, threshold, lifetime,
			"configured=%s: a session that expires before its first slide is an absolute timeout", configured)
		assert.LessOrEqual(t, threshold, sessionTouchThreshold,
			"configured=%s: the ceiling caps the DB write rate on a long session", configured)
	}
}

// The slide must exceed the configured session duration by exactly the throttle
// window. A request inside that window writes no row, so a shorter expiry would
// end the session before the configured duration passed since the last request.
func TestSlideExpiryCoversTouchThrottle(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, configured := range []time.Duration{0, -time.Hour, MinSessionDuration, time.Hour, 30 * 24 * time.Hour} {
		want := now.Add(sessionLifetime(configured)).Add(touchThreshold(configured))
		assert.Equal(t, want, sessionExpiry(now, configured), "configured=%s", configured)
		assert.True(t, sessionExpiry(now, configured).After(now.Add(sessionLifetime(configured))))
	}
}

// The login path and the slide path must write the same expiry. They disagreed
// once, and the first window after a login was short by a whole throttle
// window -- at the floor, the entire session.
func TestSessionExpiryMatchesTheSlide(t *testing.T) {
	for _, configured := range []time.Duration{0, MinSessionDuration, DefaultSessionDuration} {
		a := slidingInterceptor(t, configured)
		before := time.Now()
		written := a.touchSession(t.Context(), "session", nil, a.currentPolicy(t.Context()))
		after := time.Now()
		assert.False(t, written.Before(sessionExpiry(before, configured)))
		assert.False(t, written.After(sessionExpiry(after, configured)))
	}
}

func TestSessionExpiryPreservesMaximumDuration(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, duration := range []time.Duration{math.MaxInt64 - sessionTouchThreshold, math.MaxInt64 - sessionTouchThreshold + 1, math.MaxInt64 - sessionTouchThreshold + 2, math.MaxInt64} {
		assert.Equal(t, now.Add(duration).Add(sessionTouchThreshold), sessionExpiry(now, duration))
		a := slidingInterceptor(t, duration)
		before := time.Now()
		info := cookieCallInfo()
		require.NoError(t, callWrap(t, a.wrap, "/private", info, func(context.Context, connect.Spec, connect.ServerStream) error { return nil }))
		after := time.Now()
		written := a.store.Sessions().(*touchRecordingSessionStore).touched.ExpiresAt
		assert.False(t, written.Before(before.Add(duration).Add(sessionTouchThreshold)))
		assert.False(t, written.After(after.Add(duration).Add(sessionTouchThreshold)))
		cookie, err := http.ParseSetCookie(info.ResponseHeader().Get("Set-Cookie"))
		require.NoError(t, err)
		assert.True(t, cookie.Expires.After(after))
		assert.Positive(t, cookie.MaxAge)
	}
}

func TestSweepMaximumSessionDurationPreservesFreshRevocations(t *testing.T) {
	for _, duration := range []time.Duration{math.MaxInt64 - sessionTouchThreshold, math.MaxInt64 - sessionTouchThreshold + 1, math.MaxInt64 - sessionTouchThreshold + 2, math.MaxInt64} {
		a := &authInterceptor{sessionHasher: testSecretHasher(), state: &authState{}, policy: func(context.Context) Policy { return Policy{SessionDuration: duration} }}
		now := time.Now()
		old := now.Add(-duration).Add(-sessionTouchThreshold).Add(-time.Hour)
		maps := []*sync.Map{&a.state.sessionRevocations, &a.state.userRevocations, &a.state.userInvalidations, &a.state.bearerRevocations, &a.state.bearerInvalidations}
		for _, marks := range maps {
			marks.Store("fresh", revocationMark{generation: 1, recordedAt: now})
			marks.Store("old", revocationMark{generation: 1, recordedAt: old})
		}
		a.sweepCachesOnce()
		for _, marks := range maps {
			_, fresh := marks.Load("fresh")
			assert.True(t, fresh, "fresh security marks survive the configured lifetime")
			_, stale := marks.Load("old")
			assert.False(t, stale)
		}
	}
}

// A fresh cache entry must still stop authenticating at the credential deadline.
func TestSessionCacheChecksCredentialDeadline(t *testing.T) {
	now := time.Now()
	cached := cachedSession{cachedAt: now, user: &UserInfo{CredentialExpiresAt: DeadlineAt(now)}}
	assert.True(t, cacheEntryFresh(cached, now.Add(-time.Nanosecond)))
	assert.False(t, cacheEntryFresh(cached, now))
	assert.False(t, cacheEntryFresh(cached, now.Add(time.Nanosecond)))
}

// The lastTouch map is the DB rate-limiter and nothing else reads it, so an
// entry older than one throttle window changes no decision. Sweeping it at the
// session's whole life instead retained one entry per session the process ever
// authenticated -- seven days of them by default, and however long an operator
// configured otherwise.
func TestSweepDropsLastTouchOnceTheThrottleWindowPassed(t *testing.T) {
	a := &authInterceptor{sessionHasher: testSecretHasher(),
		policy: func(context.Context) Policy { return Policy{SessionDuration: DefaultSessionDuration} },
		state:  &authState{},
	}
	threshold := a.touchThreshold(a.currentPolicy(context.Background()))

	a.state.lastTouch.Store("fresh", time.Now())
	a.state.lastTouch.Store("stale", time.Now().Add(-threshold-time.Minute))

	a.sweepCachesOnce()

	_, freshKept := a.state.lastTouch.Load("fresh")
	assert.True(t, freshKept, "an entry inside the throttle window still gates a DB write")
	_, staleKept := a.state.lastTouch.Load("stale")
	assert.False(t, staleKept, "an entry past the throttle window gates nothing and must not be retained")
}

func TestValidateSessionDuration(t *testing.T) {
	assert.NoError(t, ValidateSessionDuration(MinSessionDuration), "the minimum itself must be allowed")
	assert.NoError(t, ValidateSessionDuration(DefaultSessionDuration))
	assert.NoError(t, ValidateSessionDuration(365*24*time.Hour), "a long session is a policy an operator may hold")

	require.EqualError(t, ValidateSessionDuration(MinSessionDuration-time.Second), "must be at least 5m0s, got 4m59s")
	require.Error(t, ValidateSessionDuration(time.Second))
	require.Error(t, ValidateSessionDuration(-time.Second))
}

// A non-positive configured duration must resolve to the default everywhere,
// including on a hand-built interceptor: a five-minute slide (the throttle
// alone) would sign every user out through the day.
func TestSessionExpiryFallsBackToDefault(t *testing.T) {
	now := time.Now()
	for _, configured := range []time.Duration{0, -time.Second} {
		assert.Equal(t, now.Add(DefaultSessionDuration).Add(sessionTouchThreshold).UTC(), sessionExpiry(now, configured))
	}
}

// The configured duration, not the default, is what a slide writes. NewInterceptor
// returns a bare ServerInterceptor func in v2, so the value is observed where
// the slide writes it: the expiry of the Touch the interceptor issued.
func TestSlideDurationHonoursConfiguredValue(t *testing.T) {
	configured := 36 * time.Hour
	sessions := &touchRecordingSessionStore{row: validSessionRow("user", "user")}
	interceptor, registry := NewInterceptor(InterceptorOptions{SessionHasher: testSecretHasher(),
		ServingCtx: context.Background(),
		Store:      sessionValidationOverrideStore{sessions: sessions},
		Policy:     func(context.Context) Policy { return Policy{SessionDuration: configured} },
	})
	t.Cleanup(registry.Stop)

	before := time.Now()
	info := cookieCallInfo()
	require.NoError(t, callWrap(t, interceptor, "/private", info, func(context.Context, connect.Spec, connect.ServerStream) error {
		return nil
	}))

	assert.WithinDuration(t, before.Add(configured+sessionTouchThreshold), sessions.touched.ExpiresAt, time.Second,
		"the slide must write the configured duration, not the default")
	parsed, err := http.ParseSetCookie(info.ResponseHeader().Get("Set-Cookie"))
	require.NoError(t, err)
	assert.WithinDuration(t, sessions.touched.ExpiresAt, parsed.Expires.UTC(), time.Second,
		"the re-issued cookie must carry the expiry the slide wrote")
}

// TestPolicyResolvedOncePerRequest pins the request-path cost contract:
// the wrapper resolves the (snapshot-backed, mutex-guarded) Policy once
// and passes it through the chain, instead of re-entering the settings
// snapshot at every consumer — the hot path must not multiply global
// lock acquisitions per request.
func TestPolicyResolvedOncePerRequest(t *testing.T) {
	var calls int
	inc := &authInterceptor{sessionHasher: testSecretHasher(),
		policy: func(context.Context) Policy {
			calls++
			return Policy{}
		},
		state: &authState{},
	}
	// An empty procedure is not public, so the wrapper's
	// authentication chain runs far enough to consume the policy (cookie
	// header parsing) before it rejects the request.
	_ = callWrap(t, inc.wrap, "", &connect.CallInfo{}, func(context.Context, connect.Spec, connect.ServerStream) error {
		return nil
	})
	assert.EqualValues(t, 1, calls, "one request resolves the policy exactly once")
}

func TestSessionRetentionCutoffPreservesMaximumDuration(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, duration := range []time.Duration{math.MaxInt64 - sessionTouchThreshold, math.MaxInt64 - sessionTouchThreshold + 1, math.MaxInt64 - sessionTouchThreshold + 2, math.MaxInt64} {
		cutoff := now.Add(-duration).Add(-sessionTouchThreshold)
		assert.Equal(t, cutoff, sessionRetentionCutoff(now, duration))
		var marks sync.Map
		marks.Store("before", revocationMark{recordedAt: cutoff.Add(-time.Nanosecond)})
		marks.Store("equal", revocationMark{recordedAt: cutoff})
		marks.Store("after", revocationMark{recordedAt: cutoff.Add(time.Nanosecond)})
		sweepRevocationMarks(&marks, cutoff)
		_, before := marks.Load("before")
		_, equal := marks.Load("equal")
		_, after := marks.Load("after")
		assert.False(t, before)
		assert.True(t, equal)
		assert.True(t, after)
	}
}
