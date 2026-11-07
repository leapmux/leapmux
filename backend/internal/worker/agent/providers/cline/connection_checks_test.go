package cline

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clinesLocalHostNames are the host strings for which Cline 3.0.64 trusts a
// tokenless connection that states a local Origin (isLocalHubHostName in
// sdk/packages/core/src/hub/server/hub-websocket-server.ts). Cline compares the
// host string that the daemon was started with, so the daemon must be started
// with none of them.
var clinesLocalHostNames = []string{"localhost", "127.0.0.1", "::1", "[::1]"}

func TestDaemonListenStaysOutOfClinesLocalHostList(t *testing.T) {
	t.Parallel()
	for _, goos := range []string{"darwin", "linux", "windows", "freebsd"} {
		listen := hubListenFor(goos)
		host := strings.ToLower(strings.TrimSpace(listen.host))
		assert.NotContains(t, clinesLocalHostNames, host, "%s: Cline would trust a local Origin on this host", goos)
		ip := net.ParseIP(listen.address)
		require.NotNil(t, ip, "%s: the address is an IP address", goos)
		assert.True(t, ip.IsLoopback(), "%s: the daemon listens on loopback alone", goos)
	}
	// macOS has 127.0.0.1 alone on lo0, and Cline builds its URL from the host
	// string, so the host is the short form that the resolver reads as
	// 127.0.0.1. Linux and Windows route all of 127.0.0.0/8 to loopback.
	assert.Equal(t, hubListen{host: "127.1", address: "127.0.0.1"}, hubListenFor("darwin"))
	assert.Equal(t, hubListen{host: "127.0.0.2", address: "127.0.0.2"}, hubListenFor("linux"))
	assert.Equal(t, hubListen{host: "127.0.0.2", address: "127.0.0.2"}, hubListenFor("windows"))
}

// localOriginHub serves one upgrade path the way a Cline daemon does: the token
// subprotocol opens it, and trustsLocalOrigin also opens it for a tokenless
// request whose Origin is local.
func localOriginHub(t *testing.T, trustsLocalOrigin bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != hubPathname {
			http.NotFound(w, r)
			return
		}
		if !trustsLocalOrigin || !isLocalOrigin(r.Header.Get("Origin")) {
			http.Error(w, "token", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		_, _, _ = conn.Read(r.Context())
		_ = conn.CloseNow()
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRefuseTokenlessHubRefusesADaemonThatTrustsALocalOrigin(t *testing.T) {
	t.Parallel()
	server := localOriginHub(t, true)
	err := refuseTokenlessHub(context.Background(), fakeRecord(server.URL))
	require.ErrorIs(t, err, errHubTrustsLocalOrigin)
	assert.Contains(t, err.Error(), "without its token", "the reason states the risk")
}

func TestRefuseTokenlessHubAcceptsADaemonThatWantsItsToken(t *testing.T) {
	t.Parallel()
	server := localOriginHub(t, false)
	require.NoError(t, refuseTokenlessHub(context.Background(), fakeRecord(server.URL)))
}

func TestRefuseTokenlessHubAcceptsADaemonThatDropsTheConnection(t *testing.T) {
	t.Parallel()
	// Bun can reset the socket after Cline writes its 401, so the client sees
	// the connection end with no status at all.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buffer := make([]byte, 4096)
			_, _ = conn.Read(buffer)
			_ = conn.Close()
		}
	}()
	require.NoError(t, refuseTokenlessHub(context.Background(), fakeRecord("http://"+listener.Addr().String())))
}

func TestRefuseTokenlessHubFailsClosedWhenItCannotAsk(t *testing.T) {
	t.Parallel()
	// A daemon that never answers cannot show that it refuses, so the check
	// refuses the start rather than assume it. The context ends once the daemon
	// holds the connection, which is what ends the wait.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = conn.Close() })
		cancel()
	}()
	err = refuseTokenlessHub(ctx, fakeRecord("http://"+listener.Addr().String()))
	require.Error(t, err)
	assert.NotErrorIs(t, err, errHubTrustsLocalOrigin)
	assert.Contains(t, err.Error(), "check that the Cline hub requires its token")
}

func TestCheckProtocolDoesNotRequireReleaseMetadata(t *testing.T) {
	t.Parallel()
	require.NoError(t, checkProtocol(discoveryRecord{ProtocolVersion: "v1"}))
}

// The check refuses a record whose address is not a loopback address, as the
// connection does, before it dials anything.
func TestRefuseTokenlessHubRefusesAnAddressThatIsNotLoopback(t *testing.T) {
	t.Parallel()
	err := refuseTokenlessHub(context.Background(), fakeRecord("http://192.0.2.1:4242"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, errHubTrustsLocalOrigin)
	assert.Contains(t, err.Error(), "not a loopback address")
}
