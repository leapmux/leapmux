package auth

import (
	"context"
	"net"
	"net/http"
)

// IsUnixSocket reports whether the current request arrived via a Unix
// domain socket. It inspects http.LocalAddrContextKey, which the
// standard library's http.Server sets for every accepted connection.
func IsUnixSocket(ctx context.Context) bool {
	addr, _ := ctx.Value(http.LocalAddrContextKey).(net.Addr)
	if addr == nil {
		return false
	}
	return addr.Network() == "unix"
}
