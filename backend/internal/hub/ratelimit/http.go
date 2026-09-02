package ratelimit

import (
	"context"
	"log/slog"
	"net/http"

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
	allowed, _, err := m.allowWindowed(ctx, op, clientAddressKey(r))
	if err != nil {
		slog.WarnContext(ctx, "rate limit unavailable for an anonymous OAuth endpoint; admitting",
			"operation", string(op), "err", err)
		return true
	}
	return allowed
}

// clientAddressKey is the budget key for an anonymous caller: its IP address.
//
// The REMOTE ADDRESS of the connection, never a forwarded header. A header is
// caller-controlled, so keying on one would let an attacker mint a fresh budget
// per request by varying it -- which is worse than no limit, because it also
// lets them exhaust a victim's budget by claiming the victim's address.
//
// A hub behind a reverse proxy therefore sees the proxy's address and shares
// one budget across every client behind it. That is the honest reading of what
// the hub can actually verify, and the budget is sized for it: these endpoints
// are polled, not held open, so the default admits a large multiple of what one
// real client needs.
//
// The port is stripped, because a client picks a fresh one per connection and
// keying on it would give every request its own budget.
func clientAddressKey(r *http.Request) string {
	addr := r.RemoteAddr
	return AddressBudgetKey(peer.HostOf(addr))
}

// AddressBudgetKey renders one anonymous caller's budget key from its host.
//
// It is exported because the two entry points reach the host differently: the
// plain-HTTP door reads r.RemoteAddr, and the Connect interceptor reads the
// peer the http.Server stamped on the context. Both must produce the SAME key,
// or one caller would hold two budgets and neither would limit it. peer.HostOf
// is the shared reduction that makes the two agree.
func AddressBudgetKey(host string) string {
	if host == "" {
		// An unaddressed caller (a test transport, a unix socket) shares one
		// budget under a name no address can collide with, rather than each
		// getting an unlimited one.
		return "anonymous:unknown"
	}
	return "anonymous:" + host
}
