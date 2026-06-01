package testutil

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// StartEchoServer starts a TCP server that echoes back all data received.
// Returns the listener address (host:port). The server is stopped when
// the test completes.
func StartEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	return ln.Addr().String()
}

// StartWriteThenCloseServer starts a TCP server that writes data to each
// connection and then closes it. Returns the listener address (host:port).
func StartWriteThenCloseServer(t *testing.T, data []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = conn.Write(data)
			}()
		}
	}()

	return ln.Addr().String()
}

// ParseAddr splits an address string into host and port components.
func ParseAddr(addr string) (string, uint32) {
	host, port, _ := net.SplitHostPort(addr)
	var p uint32
	_, _ = fmt.Sscanf(port, "%d", &p)
	return host, p
}

// PortNumber extracts the bare numeric port from a testcontainers mapped-port
// string. Since testcontainers-go v0.42, the wait.ForSQL callback receives the
// mapped host port in moby's "<num>/<proto>" form (e.g. "32768/tcp") because it
// invokes the callback with network.Port.String(). The protocol suffix must be
// stripped before building a DSN; otherwise "/tcp" leaks into the connection
// string, the DB driver can never connect, and the readiness wait spins until
// it times out with a misleading "context deadline exceeded" against the Docker
// socket. A bare port (no slash) is returned unchanged.
func PortNumber(mappedPort string) string {
	num, _, _ := strings.Cut(mappedPort, "/")
	return num
}
