package testutil

import (
	"io"
	"net"
	"strconv"
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

// ParseAddr splits an address string into host and port components. A
// bracketed IPv6 host comes back unbracketed ("[::1]:80" -> "::1", 80).
//
// Malformed input yields a zero PORT rather than an error: every caller passes
// an address it just took from a net.Listener, so a failure here means the test
// is wrong, not that the input needs handling. What ParseAddr must never do is
// GUESS, which is why the port is parsed strictly.
//
// A zero port is therefore the only reliable failure signal. The host is
// best-effort and comes back whenever the split succeeded, so "localhost:http"
// yields ("localhost", 0) -- and an empty host is a legitimate SUCCESS value
// anyway, since ":9999" splits to no host at all.
//
// fmt.Sscanf("%d") would not be strict: it stops at the first non-digit and
// reports success, so "80abc" and the "80/tcp" form a testcontainers mapped
// port carries both come back as a plausible-looking 80 -- a wrong port that
// dials something real instead of failing.
func ParseAddr(addr string) (string, uint32) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0
	}
	// ParseUint returns the clamped maximum (65535) *together with* an error
	// for an out-of-range port, so the error has to decide the result -- using
	// the value would silently turn port 70000 into 65535.
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return host, 0
	}
	return host, uint32(p)
}
