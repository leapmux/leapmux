package peer_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/peer"
)

// The absent case is the security-relevant one: a context nothing marked must
// grant nothing, because every mark comes from wiring a test can omit.
func TestUnmarkedContextGrantsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	assert.False(t, peer.IsLocalIPC(ctx), "an unmarked context must not read as local IPC")
	_, ok := peer.TransportAddr(ctx)
	assert.False(t, ok)
	assert.Equal(t, "", peer.ClientIP(ctx))
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
		ctx := peer.WithTransportAddr(context.Background(), addr)
		assert.Falsef(t, peer.IsLocalIPC(ctx), "%s must not read as local IPC", addr)
	}
}

// The two marks are independent: a local IPC connection also records its peer
// address, and recording an address never implies the transport.
func TestMarksCompose(t *testing.T) {
	t.Parallel()
	ctx := peer.WithTransportAddr(peer.WithLocalIPC(context.Background()),
		&net.UnixAddr{Name: "/tmp/hub.sock", Net: "unix"})

	assert.True(t, peer.IsLocalIPC(ctx))
	addr, ok := peer.TransportAddr(ctx)
	assert.True(t, ok)
	assert.Equal(t, "/tmp/hub.sock", addr.String())
}

func TestWithTransportAddr_IgnoresNil(t *testing.T) {
	t.Parallel()
	ctx := peer.WithTransportAddr(context.Background(), nil)
	_, ok := peer.TransportAddr(ctx)
	assert.False(t, ok, "a nil address must record nothing rather than a typed nil")
}

func TestClientIP_IsSeparateAndCanonical(t *testing.T) {
	t.Parallel()
	ctx := peer.WithTransportAddr(context.Background(),
		&net.TCPAddr{IP: net.ParseIP("10.0.0.4"), Port: 4327})
	ctx = peer.WithClientIP(ctx, "2001:0db8::7")

	transport, ok := peer.TransportAddr(ctx)
	require.True(t, ok)
	assert.Equal(t, "10.0.0.4:4327", transport.String())
	assert.Equal(t, "2001:db8::7", peer.ClientIP(ctx))
}

func TestClientIP_RejectsInvalidValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "unknown", "192.0.2.1:443", "0.0.0.0", "::"} {
		assert.Empty(t, peer.ClientIP(peer.WithClientIP(context.Background(), value)), value)
	}
}

// A ZONE is kept, and only the transport peer can supply one: every
// header-derived address is refused a zone by the parser that reads it. On a
// link-local address the zone is part of the identity, so dropping it would
// merge two peers on different interfaces into one budget -- and refusing the
// address outright, which this used to do, put every link-local client into
// the shared unknown budget and wrote an empty address on their session rows.
func TestClientIP_KeepsAZone(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "fe80::1%en0",
		peer.ClientIP(peer.WithClientIP(context.Background(), "fe80::1%en0")))
	assert.NotEqual(t,
		peer.ClientIP(peer.WithClientIP(context.Background(), "fe80::1%en0")),
		peer.ClientIP(peer.WithClientIP(context.Background(), "fe80::1%en1")),
		"two interfaces must not share one budget")
}

// The POLARITY, which is the whole reason this mark is safe to derive from a
// header. An unmarked context reports plain HTTP, so wiring that forgets the
// middleware costs a cookie its Secure attribute rather than granting one.
func TestIsHTTPS_DefaultsToPlain(t *testing.T) {
	t.Parallel()
	assert.False(t, peer.IsHTTPS(context.Background()), "an unmarked context is not HTTPS")
	assert.False(t, peer.IsHTTPS(peer.WithHTTPS(context.Background(), false)))
	assert.True(t, peer.IsHTTPS(peer.WithHTTPS(context.Background(), true)))
}
