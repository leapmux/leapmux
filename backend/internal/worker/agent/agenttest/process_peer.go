package agenttest

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/require"
)

// ListenForProcessPeer opens a loopback listener for a test subprocess.
func ListenForProcessPeer(t *testing.T, timeout time.Duration) *net.TCPListener {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	require.NoError(t, listener.SetDeadline(time.Now().Add(timeout)))
	return listener
}

// AcceptProcessPeer reads the peer's PID and records its current identity.
// Keep the returned connection open while the test inspects the live peer.
func AcceptProcessPeer(t *testing.T, listener *net.TCPListener, timeout time.Duration) (procutil.ProcessIdentity, *net.TCPConn) {
	t.Helper()
	conn, err := listener.AcceptTCP()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err)
	identity, present := procutil.IdentifyProcess(pid)
	require.True(t, present)
	return identity, conn
}

// RequireProcessPeerClosed checks that the peer released its socket while
// the test still holds its own end open.
func RequireProcessPeerClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
	var unexpected [1]byte
	size, err := conn.Read(unexpected[:])
	require.Zero(t, size, "the peer must close without sending another record")
	require.ErrorIs(t, err, io.EOF, "the peer must close its socket")
}
