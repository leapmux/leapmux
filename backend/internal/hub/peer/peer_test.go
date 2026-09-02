package peer_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/peer"
)

// The absent case is the security-relevant one: a context nothing marked must
// grant nothing, because every mark comes from wiring a test can omit.
func TestUnmarkedContextGrantsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	assert.False(t, peer.IsLocalIPC(ctx), "an unmarked context must not read as local IPC")
	_, ok := peer.RemoteAddr(ctx)
	assert.False(t, ok)
	assert.Equal(t, "", peer.RemoteHost(ctx))
}

func TestIsLocalIPC(t *testing.T) {
	t.Parallel()

	assert.True(t, peer.IsLocalIPC(peer.WithLocalIPC(context.Background())))

	// A TCP connection is never local IPC, whatever its address says. This is
	// the rule change the feature turns on: loopback no longer exempts a
	// caller from signing in.
	for _, addr := range []net.Addr{
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4327},
		&net.TCPAddr{IP: net.ParseIP("::1"), Port: 4327},
		&net.TCPAddr{IP: net.ParseIP("192.168.1.24"), Port: 4327},
	} {
		ctx := peer.WithRemoteAddr(context.Background(), addr)
		assert.Falsef(t, peer.IsLocalIPC(ctx), "%s must not read as local IPC", addr)
	}
}

// The two marks are independent: a local IPC connection also records its peer
// address, and recording an address never implies the transport.
func TestMarksCompose(t *testing.T) {
	t.Parallel()
	ctx := peer.WithRemoteAddr(peer.WithLocalIPC(context.Background()),
		&net.UnixAddr{Name: "/tmp/hub.sock", Net: "unix"})

	assert.True(t, peer.IsLocalIPC(ctx))
	addr, ok := peer.RemoteAddr(ctx)
	assert.True(t, ok)
	assert.Equal(t, "/tmp/hub.sock", addr.String())
}

func TestWithRemoteAddr_IgnoresNil(t *testing.T) {
	t.Parallel()
	ctx := peer.WithRemoteAddr(context.Background(), nil)
	_, ok := peer.RemoteAddr(ctx)
	assert.False(t, ok, "a nil address must record nothing rather than a typed nil")
}

func TestRemoteHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		addr net.Addr
		want string
	}{
		{"IPv4 drops the port", &net.TCPAddr{IP: net.ParseIP("192.168.1.24"), Port: 51234}, "192.168.1.24"},
		{"IPv6 drops the port and the brackets", &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 51234}, "2001:db8::1"},
		{"loopback keeps its address", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 51234}, "127.0.0.1"},
		// A unix address carries no port, so the split fails and the whole
		// name is the host. Every desktop connection then shares one budget,
		// which is right: they are all the same person.
		{"a unix socket path is the host", &net.UnixAddr{Name: "/tmp/hub.sock", Net: "unix"}, "/tmp/hub.sock"},
		{"an empty unix address is empty", &net.UnixAddr{Net: "unix"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := peer.WithRemoteAddr(context.Background(), tc.addr)
			assert.Equal(t, tc.want, peer.RemoteHost(ctx))
		})
	}
}

// The two ports of one client must share a budget, or a rate limit counts
// nothing: a caller opens a fresh connection per attempt.
func TestRemoteHost_IsStableAcrossPorts(t *testing.T) {
	t.Parallel()
	first := peer.RemoteHost(peer.WithRemoteAddr(context.Background(),
		&net.TCPAddr{IP: net.ParseIP("192.168.1.24"), Port: 51234}))
	second := peer.RemoteHost(peer.WithRemoteAddr(context.Background(),
		&net.TCPAddr{IP: net.ParseIP("192.168.1.24"), Port: 51235}))

	assert.Equal(t, first, second)
}

// The two entry points must reduce an address the same way, or one caller
// holds two budgets and the sign-in limit counts neither of them properly.
//
// ratelimit.clientAddressKey reads r.RemoteAddr and RemoteHost reads the peer
// the http.Server stamped; both go through HostOf, and this pins that the one
// reduction handles every shape either of them can see.
func TestHostOf(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"192.168.1.24:51234", "192.168.1.24"},
		{"[2001:db8::1]:51234", "2001:db8::1"},
		{"[fe80::1%en0]:51234", "fe80::1%en0"},
		// No port at all: a unix socket path, or an address a transport
		// rendered without one.
		{"/tmp/hub.sock", "/tmp/hub.sock"},
		{"192.168.1.24", "192.168.1.24"},
		{"", ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, peer.HostOf(tc.in))
		})
	}
}
