package auth

import (
	"net/http"
	"testing"
	"time"

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

// The refresh must not resurrect a session cookie for an unrelated cookie the
// handler set (an OAuth state cookie, say). The skip is deliberately blind to
// the name: a handler that writes any Set-Cookie owns the response's cookies.
func TestSessionRefreshApplyTo_SkipsWhenAnyCookieWritten(t *testing.T) {
	h := http.Header{}
	h.Add("Set-Cookie", "oauth-state=abc; Path=/")

	sessionRefresh{sessionID: "sess-123", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, false)

	assert.Len(t, h.Values("Set-Cookie"), 1)
}

// The slide must exceed the configured session duration by at least the touch
// throttle. Otherwise a request inside the throttle window leaves the session
// ending before that duration passed since the request -- the guarantee the
// session duration states.
func TestSlideDurationCoversTouchThrottle(t *testing.T) {
	for _, configured := range []time.Duration{0, -time.Hour, time.Minute, 30 * 24 * time.Hour} {
		a := &authInterceptor{sessionDuration: configured}
		assert.GreaterOrEqual(t, a.slideDuration()-sessionLifetime(configured), sessionTouchThreshold,
			"configured=%s", configured)
	}
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
	interceptor, registry := NewInterceptor(InterceptorOptions{SessionDuration: configured})
	t.Cleanup(registry.Stop)

	assert.Equal(t, configured+sessionTouchThreshold, interceptor.(*authInterceptor).slideDuration())
}
