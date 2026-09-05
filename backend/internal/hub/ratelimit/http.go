package ratelimit

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"

	"github.com/leapmux/leapmux/internal/hub/peer"
)

// The plain-HTTP entry point.
//
// This package is reached everywhere else through a Connect interceptor, keyed
// by procedure. The OAuth authorization server's ANONYMOUS endpoints -- every
// route
// mounted through anonymousLeg: device authorization, the token exchange,
// revocation, dynamic registration, step-up, and the app icons -- are mux
// routes rather than Connect procedures, so no interceptor sees them, and they
// were the only endpoints on the hub an unauthenticated caller could drive in
// a loop against the store.
//
// It lives beside the interceptor so BOTH entry points read one budget table:
// an operator sets `rate_limit.oauth_anonymous` once, and it governs whichever
// door the traffic came through.

// AllowHTTP reports whether a plain-HTTP request may proceed.
//
// It counts the request in the fixed window the Manager already keeps
// (allowWindowed), so the budget is genuinely windowed: 600 requests per ten
// minutes, not 600 per address per process lifetime.
//
// A nil manager or an unreachable configuration ADMITS. The former is
// deliberate (a test wires no manager). The latter differs from the
// interceptor's fail-closed choice on purpose: these endpoints are how a
// client authenticates at all, so a settings-store blip that locked every app
// out of every hub would be a worse outage than the unthrottled window it
// prevents. The failure is logged so it is not silent.
func AllowHTTP(ctx context.Context, m *Manager, op Operation, r *http.Request) bool {
	if m == nil {
		return true
	}
	allowed, _, err := m.allowWindowed(ctx, op, anonymousBudgetKey(r.Context()))
	if err != nil {
		slog.WarnContext(ctx, "rate limit unavailable for an anonymous OAuth endpoint; admitting",
			"operation", string(op), "err", err)
		return true
	}
	return allowed
}

// anonymousBudgetKey renders one anonymous caller's budget key.
//
// It answers from the VERIFIED client IP when the request-source middleware
// could name one, and from the unchanged TRANSPORT PEER when it could not.
// Both entry points -- the plain-HTTP door and the Connect interceptor -- call
// this one function, so a caller can never hold two budgets.
//
// The fallback is what keeps the shared bucket honest, and it is not a
// weakening: the transport peer is the address the kernel accepted the
// connection from, so no header can set it and no proxy can forge it. Without
// the fallback, EVERY request the middleware refuses to name lands in one
// bucket -- a malformed forwarding header from behind a trusted proxy, a
// chain whose addresses are all trusted, a `for=unknown` node, and an IPv6
// link-local peer, which needs no configuration at all. That bucket also
// holds the local IPC socket, so a remote caller could spend the desktop
// app's window: 600 requests in ten minutes exhausts OpOAuthAnonymous, and
// `leapmux control` is then refused on the machine's own socket.
//
// The client IP stays the PREFERRED key, so a real proxy deployment still
// budgets per client rather than per proxy.
//
// A caller with neither -- the local IPC socket, whose accepted connection
// carries no IP address, and a test transport -- shares one budget. That is
// the population the shared bucket was always for.
func anonymousBudgetKey(ctx context.Context) string {
	if clientIP := peer.ClientIP(ctx); clientIP != "" {
		return AddressBudgetKey(clientIP)
	}
	return AddressBudgetKey(transportHost(ctx))
}

// transportHost is the host of the accepted connection's address, or an empty
// string when it has none.
//
// A unix socket and a named pipe both reach this: the first carries no address
// at all, and the second carries a pipe path rather than a host. Neither can
// be reached from the network, which is why both belong in the shared bucket.
func transportHost(ctx context.Context) string {
	addr, ok := peer.TransportAddr(ctx)
	if !ok {
		return ""
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP == nil {
		return ""
	}
	host, err := netip.ParseAddr(tcp.IP.String())
	if err != nil {
		return ""
	}
	// The ZONE stays. On a link-local address it is part of the identity: two
	// peers on different interfaces can carry the same address, and merging
	// them would put them in one budget.
	if tcp.Zone != "" {
		host = host.WithZone(tcp.Zone)
	}
	return host.Unmap().String()
}

// AddressBudgetKey renders one anonymous caller's budget key from its host.
//
// It is exported because `AllowHTTP`'s callers outside this package name the
// unknown bucket in their own tests. Inside the package every budget goes
// through anonymousBudgetKey, which is the one place that decides what a host
// is.
func AddressBudgetKey(host string) string {
	if host == "" {
		// An unaddressed caller (a test transport, a unix socket, a Windows
		// named pipe) shares one budget under a name no address can collide
		// with, rather than each getting an unlimited one.
		return "anonymous:unknown"
	}
	return "anonymous:" + host
}
