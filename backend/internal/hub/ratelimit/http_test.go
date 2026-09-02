package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAllowHTTPCountsEveryRequestAgainstOneAddress pins the difference from the
// interceptor: this budget caps the RATE at which one address may drive an
// anonymous endpoint, so a request that FINISHED still counts. Releasing it
// would let an attacker with fast responses run without limit.
func TestAllowHTTPCountsEveryRequestAgainstOneAddress(t *testing.T) {
	m := newTestManager(t)
	upsertLimit(t, m, OpOAuthAnonymous, true, 2, 600)

	req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	req.RemoteAddr = "203.0.113.7:51234"

	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req))
	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req))
	assert.False(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req),
		"a finished request still counts; nothing releases this reservation")

	// A DIFFERENT address has its own budget, so one noisy caller cannot lock
	// everybody else out.
	other := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	other.RemoteAddr = "203.0.113.8:51234"
	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, other))
}

// TestAllowHTTPKeysOnTheConnectionAddressAndNotAHeader is the security
// property: keying on a forwarded header would let an attacker mint a fresh
// budget per request by varying it -- worse than no limit, because it also lets
// them exhaust a victim's budget by claiming the victim's address.
func TestAllowHTTPKeysOnTheConnectionAddressAndNotAHeader(t *testing.T) {
	m := newTestManager(t)
	upsertLimit(t, m, OpOAuthAnonymous, true, 1, 600)

	first := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	first.RemoteAddr = "203.0.113.7:1"
	first.Header.Set("X-Forwarded-For", "10.0.0.1")
	require.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, first))

	second := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	second.RemoteAddr = "203.0.113.7:2"
	second.Header.Set("X-Forwarded-For", "10.0.0.2")
	assert.False(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, second),
		"a varying forwarded header must not buy a fresh budget")
}

// TestAllowHTTPAdmitsWhenUnconfigured pins the fail-OPEN choice, which differs
// from the interceptor's fail-closed one on purpose: these endpoints are how a
// client authenticates at all, so a settings blip that locked every app out of
// every hub would be a worse outage than the unthrottled window it prevents.
func TestAllowHTTPAdmitsWhenUnconfigured(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	req.RemoteAddr = "203.0.113.7:1"
	assert.True(t, AllowHTTP(context.Background(), nil, OpOAuthAnonymous, req),
		"a nil manager admits: a test wires none, and a hub always has one")

	solo := newTestManager(t)
	upsertLimit(t, solo, OpOAuthAnonymous, true, 1, 600)
	assert.True(t, AllowHTTP(context.Background(), solo, OpOAuthAnonymous, req),
		"the first request of the window admits")
	assert.False(t, AllowHTTP(context.Background(), solo, OpOAuthAnonymous, req),
		"solo mode enforces the one ADDRESS-keyed budget: a solo hub that listens beyond the loopback serves anonymous addresses like any other")
}

// TestAllowHTTPWindowActuallyResets pins the fix for the never-released
// reservation: the budget is a fixed window, so requests from one address are
// admitted again once the window turns over. The old implementation counted
// admissions into the IN-FLIGHT map that nothing on this path decremented,
// which turned "2 per 10 minutes" into "2 per address per process lifetime".
func TestAllowHTTPWindowActuallyResets(t *testing.T) {
	m := newTestManager(t)
	upsertLimit(t, m, OpOAuthAnonymous, true, 2, 600)

	req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	req.RemoteAddr = "203.0.113.7:51234"

	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req))
	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req))
	assert.False(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req),
		"the window's budget is spent")

	m.now = func() time.Time { return fakeNow.Add(601 * time.Second) }
	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req),
		"one window later the budget starts over")
}

// TestClientAddressKeyStripsThePortAndBracket pins the key shape. A client
// picks a fresh port per connection, so keying on it would give every request
// its own budget -- exactly the hole the header rule above closes.
func TestClientAddressKeyStripsThePortAndBracket(t *testing.T) {
	for addr, want := range map[string]string{
		"203.0.113.7:51234":  "anonymous:203.0.113.7",
		"[2001:db8::1]:4327": "anonymous:2001:db8::1",
		"203.0.113.7":        "anonymous:203.0.113.7",
		"":                   "anonymous:unknown",
	} {
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
		req.RemoteAddr = addr
		assert.Equalf(t, want, clientAddressKey(req), "address %q", addr)
	}
}
