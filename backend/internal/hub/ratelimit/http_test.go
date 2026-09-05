package ratelimit

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/peer"
)

func requestWithClientIP(method, path, clientIP string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	return req.WithContext(peer.WithClientIP(req.Context(), clientIP))
}

// TestAllowHTTPCountsEveryRequestAgainstOneAddress pins the difference from the
// interceptor: this budget caps the RATE at which one address may drive an
// anonymous endpoint, so a request that FINISHED still counts. Releasing it
// would let an attacker with fast responses run without limit.
func TestAllowHTTPCountsEveryRequestAgainstOneAddress(t *testing.T) {
	m := newTestManager(t)
	upsertLimit(t, m, OpOAuthAnonymous, true, 2, 600)

	req := requestWithClientIP(http.MethodPost, "/oauth/token", "203.0.113.7")
	req.RemoteAddr = "203.0.113.7:51234"

	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req))
	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req))
	assert.False(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req),
		"a finished request still counts; nothing releases this reservation")

	// A DIFFERENT address has its own budget, so one noisy caller cannot lock
	// everybody else out.
	other := requestWithClientIP(http.MethodPost, "/oauth/token", "203.0.113.8")
	other.RemoteAddr = "203.0.113.8:51234"
	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, other))
}

// The limiter reads only the verified client IP from context. Raw forwarding
// headers cannot change the budget after request-source verification.
func TestAllowHTTPUsesVerifiedClientIPAndNotRawHeaders(t *testing.T) {
	m := newTestManager(t)
	upsertLimit(t, m, OpOAuthAnonymous, true, 1, 600)

	first := requestWithClientIP(http.MethodPost, "/oauth/token", "203.0.113.7")
	first.RemoteAddr = "203.0.113.7:1"
	first.Header.Set("X-Forwarded-For", "10.0.0.1")
	require.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, first))

	second := requestWithClientIP(http.MethodPost, "/oauth/token", "203.0.113.7")
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
	req := requestWithClientIP(http.MethodPost, "/oauth/token", "203.0.113.7")
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

	req := requestWithClientIP(http.MethodPost, "/oauth/token", "203.0.113.7")
	req.RemoteAddr = "203.0.113.7:51234"

	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req))
	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req))
	assert.False(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req),
		"the window's budget is spent")

	m.now = func() time.Time { return fakeNow.Add(601 * time.Second) }
	assert.True(t, AllowHTTP(context.Background(), m, OpOAuthAnonymous, req),
		"one window later the budget starts over")
}

// The verified value is already a canonical IP without a port, and it is the
// PREFERRED key: a request that carries one never falls back.
func TestAnonymousBudgetKeyUsesVerifiedClientIP(t *testing.T) {
	for clientIP, want := range map[string]string{
		"203.0.113.7": "anonymous:203.0.113.7",
		"2001:db8::1": "anonymous:2001:db8::1",
	} {
		req := requestWithClientIP(http.MethodPost, "/oauth/token", clientIP)
		assert.Equalf(t, want, anonymousBudgetKey(req.Context()), "client IP %q", clientIP)
	}
}

// The FALLBACK, and the property that makes the shared bucket safe: when the
// middleware could name no client, the budget keys on the address the kernel
// accepted the connection from. Without it every request the middleware
// refuses to name shares ONE bucket with the local IPC socket, so a remote
// caller could spend the desktop app's window.
func TestAnonymousBudgetKeyFallsBackToTheTransportPeer(t *testing.T) {
	for _, test := range []struct {
		name string
		addr net.Addr
		want string
	}{
		{"an IPv4 peer", &net.TCPAddr{IP: net.ParseIP("198.51.100.9"), Port: 51234}, "anonymous:198.51.100.9"},
		{"an IPv6 peer", &net.TCPAddr{IP: net.ParseIP("2001:db8::9"), Port: 51234}, "anonymous:2001:db8::9"},
		// The zone stays: two link-local peers on different interfaces are
		// different callers and must not share one budget.
		{"a link-local peer", &net.TCPAddr{IP: net.ParseIP("fe80::1"), Zone: "en0", Port: 51234}, "anonymous:fe80::1%en0"},
		// The local IPC socket carries no address, so it stays in the shared
		// bucket, which is the population that bucket was always for.
		{"a unix socket", &net.UnixAddr{Net: "unix"}, "anonymous:unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := peer.WithClientIP(peer.WithTransportAddr(context.Background(), test.addr), "")
			assert.Equal(t, test.want, anonymousBudgetKey(ctx))
		})
	}
}

// A request with NEITHER mark shares the unknown bucket. A test transport is
// the reachable case.
func TestAnonymousBudgetKeyWithNoMarksIsUnknown(t *testing.T) {
	assert.Equal(t, "anonymous:unknown", anonymousBudgetKey(context.Background()))
}

// The fallback must not be reachable from a header. A caller that could move
// itself between budgets would defeat both of them.
func TestAnonymousBudgetKeyIgnoresHeaders(t *testing.T) {
	addr := &net.TCPAddr{IP: net.ParseIP("198.51.100.9"), Port: 51234}
	ctx := peer.WithClientIP(peer.WithTransportAddr(context.Background(), addr), "")
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil).WithContext(ctx)
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("Forwarded", "for=10.0.0.2")
	assert.Equal(t, "anonymous:198.51.100.9", anonymousBudgetKey(req.Context()),
		"a forwarding header must not reach the fallback key")
}
