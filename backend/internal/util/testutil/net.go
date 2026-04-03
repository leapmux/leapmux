package testutil

import (
	"fmt"
	"io"
	"net"
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
