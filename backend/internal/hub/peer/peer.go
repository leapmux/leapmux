// Package peer records which connection carried a request, so a handler can
// answer two questions the request body cannot: did this arrive on the hub's
// local IPC listener, and what address is it from?
//
// The package holds FOUR marks, and they have two writers. The http.Server
// places the local-IPC mark and the transport address, one per listener and
// one per connection. The request-source middleware places the verified client
// IP and the verified protocol, per request, because both answers need the
// forwarding headers and the trusted-proxy setting, which the server knows
// nothing about.
//
// Every consumer reads them from the context. That is what lets the ConnectRPC
// interceptor and the WebSocket authenticator answer alike: a Connect handler
// holds a context and a WebSocket handler holds an *http.Request whose context
// descends from the same connection.
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

type httpsKey struct{}

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
//
// A ZONE is kept, and only the transport peer can supply one. Every
// header-derived address is refused a zone by the parser that reads it, so a
// zoned value here came from the accepted connection, where the zone is part
// of the identity: two link-local peers on different interfaces carry the same
// address, and dropping the zone would merge them. Refusing the whole address
// instead put every link-local client into the shared unknown budget and wrote
// an empty address on their session rows.
func WithClientIP(ctx context.Context, value string) context.Context {
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.IsUnspecified() {
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

// WithHTTPS records that the request reached the hub over TLS.
//
// The hub terminates no TLS itself, so the only way this is true is a trusted
// reverse proxy that terminated it and said so. The request-source middleware
// is the one writer, and it sets this only after the transport peer passed the
// trust test -- a caller-controlled header can never reach it.
//
// The POLARITY matches the rest of this package: an unmarked context reports
// plain HTTP. A missed mark then costs a cookie its Secure attribute, which is
// the direction that fails safe against a mark the wiring forgot.
func WithHTTPS(ctx context.Context, https bool) context.Context {
	return context.WithValue(ctx, httpsKey{}, https)
}

// IsHTTPS reports whether a trusted proxy verified that this request reached
// the hub over TLS.
func IsHTTPS(ctx context.Context) bool {
	value, _ := ctx.Value(httpsKey{}).(bool)
	return value
}
