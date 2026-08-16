package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRefreshApplyTo_WritesSlidExpiry(t *testing.T) {
	expires := time.Now().Add(DefaultSessionDuration).UTC().Truncate(time.Second)
	h := http.Header{}

	sessionRefresh{sessionID: "sess-123", expiresAt: expires}.applyTo(h, false)

	values := h.Values("Set-Cookie")
	require.Len(t, values, 1)
	parsed, err := http.ParseSetCookie(values[0])
	require.NoError(t, err)
	assert.Equal(t, CookieName, parsed.Name)
	assert.Equal(t, "sess-123", parsed.Value)
	assert.True(t, parsed.HttpOnly)
	assert.Equal(t, expires, parsed.Expires.UTC())
	assert.Positive(t, parsed.MaxAge, "the browser must keep the cookie, not drop it at the end of the browser session")
}

func TestSessionRefreshApplyTo_UsesSecureCookieName(t *testing.T) {
	h := http.Header{}

	sessionRefresh{sessionID: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, true)

	parsed, err := http.ParseSetCookie(h.Get("Set-Cookie"))
	require.NoError(t, err)
	assert.Equal(t, SecureCookieName, parsed.Name)
	assert.True(t, parsed.Secure)
}

func TestSessionRefreshApplyTo_ZeroValueWritesNothing(t *testing.T) {
	h := http.Header{}

	sessionRefresh{}.applyTo(h, false)

	assert.Empty(t, h.Values("Set-Cookie"), "a request that slid nothing must not touch the browser's cookie")
}

// A handler that wrote its own session cookie is authoritative. Logout is the
// case that matters: a browser applies both Set-Cookie headers for one name in
// order and keeps the last, so appending a refresh after the clearing cookie
// would leave the user signed in.
func TestSessionRefreshApplyTo_KeepsHandlerCookie(t *testing.T) {
	h := http.Header{}
	h.Set("Set-Cookie", ClearSessionCookie(false).String())

	sessionRefresh{sessionID: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, false)

	values := h.Values("Set-Cookie")
	require.Len(t, values, 1, "the refresh must not append a second cookie for the same name")
	parsed, err := http.ParseSetCookie(values[0])
	require.NoError(t, err)
	assert.Empty(t, parsed.Value)
	assert.Negative(t, parsed.MaxAge, "the clearing cookie must survive the refresh")
}

// Only a cookie of the SAME name can overwrite the session cookie in a browser,
// so an unrelated cookie must not stop the refresh. A guard that stood back from
// every Set-Cookie would silently stop refreshing the session on each response
// that also carried, say, an OAuth state cookie.
func TestSessionRefreshApplyTo_WritesAlongsideAnUnrelatedCookie(t *testing.T) {
	h := http.Header{}
	h.Add("Set-Cookie", "oauth-state=abc; Path=/")

	sessionRefresh{sessionID: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, false)

	values := h.Values("Set-Cookie")
	require.Len(t, values, 2, "the unrelated cookie must not suppress the refresh")
	parsed, err := http.ParseSetCookie(values[1])
	require.NoError(t, err)
	assert.Equal(t, CookieName, parsed.Name)
	assert.Equal(t, "sess-123", parsed.Value)
}

// A cookie this code cannot read counts as a collision. Losing one refresh
// costs a throttle window; overwriting a cookie meant for the browser could
// sign a user in again after a logout.
func TestSessionRefreshApplyTo_StandsBackFromAnUnreadableCookie(t *testing.T) {
	h := http.Header{}
	h.Add("Set-Cookie", "this is not a cookie")

	sessionRefresh{sessionID: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, false)

	assert.Len(t, h.Values("Set-Cookie"), 1, "an unparseable cookie must fail closed")
}

// The secure name is a different name, so a handler's plain-name cookie must not
// stop a secure refresh, and the reverse.
func TestSessionRefreshApplyTo_MatchesTheNameInUse(t *testing.T) {
	h := http.Header{}
	h.Add("Set-Cookie", ClearSessionCookie(false).String())

	sessionRefresh{sessionID: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, true)

	values := h.Values("Set-Cookie")
	require.Len(t, values, 2, "the __Host- cookie is a separate name and must still be written")
	parsed, err := http.ParseSetCookie(values[1])
	require.NoError(t, err)
	assert.Equal(t, SecureCookieName, parsed.Name)
}

// A failed unary call still has to carry the cookie. The slide already
// committed to the row, and the throttle blocks every other request for a whole
// window, so a client whose calls keep failing would never receive one.
func TestSessionRefreshApplyToError_AttachesToConnectError(t *testing.T) {
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	refresh := sessionRefresh{sessionID: "sess-123", expiresAt: expires}

	t.Run("a connect error keeps its code and message", func(t *testing.T) {
		orig := connect.NewError(connect.CodePermissionDenied, errors.New("nope"))
		out := refresh.applyToError(orig, false)

		var connectErr *connect.Error
		require.ErrorAs(t, out, &connectErr)
		assert.Equal(t, connect.CodePermissionDenied, connectErr.Code())
		assert.Contains(t, connectErr.Message(), "nope")
		parsed, err := http.ParseSetCookie(connectErr.Meta().Get("Set-Cookie"))
		require.NoError(t, err)
		assert.Equal(t, "sess-123", parsed.Value)
	})

	// Wrapping a plain error to give it metadata would decide its status code
	// here, and connect.CodeOf answers CodeUnknown for a context.Canceled that
	// connect-go itself reports as CodeCanceled. Losing one refresh costs a
	// throttle window; changing the code a client sees for every cancelled
	// request costs more.
	t.Run("a plain error passes through untouched", func(t *testing.T) {
		sentinel := errors.New("boom")
		out := refresh.applyToError(sentinel, false)

		assert.Equal(t, sentinel, out, "the error must reach connect-go's own mapping unchanged")
		var connectErr *connect.Error
		assert.False(t, errors.As(out, &connectErr), "it must not become a connect.Error here")
	})

	t.Run("a context cancellation keeps the code connect-go gives it", func(t *testing.T) {
		out := refresh.applyToError(context.Canceled, false)

		assert.ErrorIs(t, out, context.Canceled)
		assert.Equal(t, context.Canceled, out,
			"wrapping would report CodeUnknown where connect-go reports CodeCanceled")
	})

	t.Run("a request that slid nothing adds no cookie", func(t *testing.T) {
		orig := connect.NewError(connect.CodeUnauthenticated, errors.New("no"))
		out := sessionRefresh{}.applyToError(orig, false)

		var connectErr *connect.Error
		require.ErrorAs(t, out, &connectErr)
		assert.Empty(t, connectErr.Meta().Get("Set-Cookie"))
	})

	t.Run("no error stays no error", func(t *testing.T) {
		assert.NoError(t, refresh.applyToError(nil, false))
	})
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
func TestSlideDurationCoversTouchThrottle(t *testing.T) {
	for _, configured := range []time.Duration{0, -time.Hour, MinSessionDuration, time.Hour, 30 * 24 * time.Hour} {
		a := &authInterceptor{policy: func() Policy { return Policy{SessionDuration: configured} }}
		assert.Equal(t, sessionLifetime(configured)+touchThreshold(configured), a.slideDuration(),
			"configured=%s", configured)
		assert.Greater(t, a.slideDuration(), sessionLifetime(configured), "configured=%s", configured)
	}
}

// The login path and the slide path must write the same expiry. They disagreed
// once, and the first window after a login was short by a whole throttle
// window -- at the floor, the entire session.
func TestSessionExpiryMatchesTheSlide(t *testing.T) {
	for _, configured := range []time.Duration{0, MinSessionDuration, DefaultSessionDuration} {
		now := time.Now()
		a := &authInterceptor{policy: func() Policy { return Policy{SessionDuration: configured} }}
		assert.Equal(t, now.Add(a.slideDuration()).UTC(), sessionExpiry(now, configured),
			"configured=%s", configured)
	}
}

// The floor exists to keep what the two overshoot sources cost together a small
// share of the session. The throttle scales with the session; the validation
// cache TTL does not, so it is the term that decides the floor.
func TestMinSessionDurationBoundsTheOvershoot(t *testing.T) {
	overshoot := touchThreshold(MinSessionDuration) + sessionCacheTTL
	assert.LessOrEqual(t, overshoot, MinSessionDuration/5,
		"at the floor a session must not outlive its configured duration by more than a fifth")

	// And the floor must sit above the window a request can be served from the
	// cache after the row expired, or "expired" describes nothing.
	assert.Greater(t, MinSessionDuration, sessionCacheTTL)
}

// The lastTouch map is the DB rate-limiter and nothing else reads it, so an
// entry older than one throttle window changes no decision. Sweeping it at the
// session's whole life instead retained one entry per session the process ever
// authenticated -- seven days of them by default, and however long an operator
// configured otherwise.
func TestSweepDropsLastTouchOnceTheThrottleWindowPassed(t *testing.T) {
	a := &authInterceptor{
		policy: func() Policy { return Policy{SessionDuration: DefaultSessionDuration} },
		state:  &authState{},
	}
	threshold := a.touchThreshold()

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

	require.Error(t, ValidateSessionDuration(MinSessionDuration-time.Second))
	require.Error(t, ValidateSessionDuration(time.Second))
	require.Error(t, ValidateSessionDuration(-time.Second))
}

// A non-positive configured duration must resolve to the default everywhere,
// including on a hand-built interceptor: a five-minute slide (the throttle
// alone) would sign every user out through the day.
func TestSlideDurationFallsBackToDefault(t *testing.T) {
	a := &authInterceptor{}
	assert.Equal(t, DefaultSessionDuration+sessionTouchThreshold, a.slideDuration())
}

// The configured duration, not the default, is what a slide writes.
func TestSlideDurationHonoursConfiguredValue(t *testing.T) {
	configured := 36 * time.Hour
	interceptor, registry := NewInterceptor(InterceptorOptions{
		Policy: func() Policy { return Policy{SessionDuration: configured} },
	})
	t.Cleanup(registry.Stop)

	assert.Equal(t, configured+sessionTouchThreshold, interceptor.(*authInterceptor).slideDuration())
}
