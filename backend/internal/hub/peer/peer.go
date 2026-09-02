// Package peer records which connection carried a request, so a handler can
// answer two questions the request body cannot: did this arrive on the hub's
// local IPC listener, and what address is it from?
//
// Both marks are placed once, by the http.Server, and every consumer reads
// them from the context. That is what lets the ConnectRPC interceptor and the
// WebSocket authenticator answer alike: a Connect handler holds a context and
// a WebSocket handler holds an *http.Request whose context descends from the
// same connection.
//
// THE POLARITY IS DELIBERATE. An unmarked context reports "not local IPC" and
// an empty address. Every mark is added by production wiring that a test can
// omit, so the absent case has to be the one that grants nothing: a missed
// mark then costs a sign-in prompt, where the opposite would hand an
// unauthenticated caller the solo administrator.
package peer

import (
	"context"
	"net"
	"strings"
)

type localIPCKey struct{}

type remoteAddrKey struct{}

// WithLocalIPC marks the context as belonging to the local IPC listener --
// the unix domain socket on Unix, the named pipe on Windows.
//
// It is applied per LISTENER rather than per connection, from the
// http.Server's BaseContext, because the listener is what the hub knows the
// identity of. Asking the accepted connection instead would mean matching its
// address against a network name that a third-party pipe implementation owns,
// and a rename there would silently turn every desktop request into a remote
// one.
func WithLocalIPC(ctx context.Context) context.Context {
	return context.WithValue(ctx, localIPCKey{}, true)
}

// IsLocalIPC reports whether the request arrived on the hub's local IPC
// listener. It is false for every TCP connection, whatever its address, and
// false for an unmarked context.
func IsLocalIPC(ctx context.Context) bool {
	v, _ := ctx.Value(localIPCKey{}).(bool)
	return v
}

// WithRemoteAddr records one connection's peer address, from the
// http.Server's ConnContext.
func WithRemoteAddr(ctx context.Context, addr net.Addr) context.Context {
	if addr == nil {
		return ctx
	}
	return context.WithValue(ctx, remoteAddrKey{}, addr)
}

// RemoteAddr returns the recorded peer address, and whether one was recorded.
func RemoteAddr(ctx context.Context) (net.Addr, bool) {
	addr, ok := ctx.Value(remoteAddrKey{}).(net.Addr)
	return addr, ok
}

// RemoteHost returns the peer's host with the port removed, or "" when no
// address was recorded.
func RemoteHost(ctx context.Context) string {
	addr, ok := RemoteAddr(ctx)
	if !ok {
		return ""
	}
	return HostOf(addr.String())
}

// HostOf reduces one address to the host a budget is keyed on.
//
// The port goes because a client picks a fresh one for every connection, so a
// budget keyed on it would give each request a budget of its own. The brackets
// go so one IPv6 peer reads as one host whichever way its address was
// rendered.
//
// It is exported because TWO entry points reach an address differently -- the
// plain-HTTP door reads r.RemoteAddr, and the Connect interceptor reads the
// peer the http.Server stamped on the context -- and they must reduce it the
// same way or one caller holds two budgets. That invariant was stated in a
// comment beside two identical copies of these three lines; it is one function
// now.
func HostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	return strings.Trim(addr, "[]")
}
