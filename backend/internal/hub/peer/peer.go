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
	"net/netip"
)

type localIPCKey struct{}

type transportAddrKey struct{}

type clientIPKey struct{}

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

// WithTransportAddr records the unchanged address of the accepted connection.
func WithTransportAddr(ctx context.Context, addr net.Addr) context.Context {
	if addr == nil {
		return ctx
	}
	return context.WithValue(ctx, transportAddrKey{}, addr)
}

// TransportAddr returns the unchanged connection address.
func TransportAddr(ctx context.Context) (net.Addr, bool) {
	addr, ok := ctx.Value(transportAddrKey{}).(net.Addr)
	return addr, ok
}

// WithClientIP records the verified client IP. Invalid values become an
// unknown client instead of entering request identity state.
func WithClientIP(ctx context.Context, value string) context.Context {
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" || addr.IsUnspecified() {
		return context.WithValue(ctx, clientIPKey{}, "")
	}
	return context.WithValue(ctx, clientIPKey{}, addr.Unmap().String())
}

// ClientIP returns the verified client IP, or an empty string when the request
// source could not identify one.
func ClientIP(ctx context.Context) string {
	value, _ := ctx.Value(clientIPKey{}).(string)
	return value
}
