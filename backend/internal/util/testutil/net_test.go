package testutil_test

import (
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestParseAddr(t *testing.T) {
	t.Run("splits host and port", func(t *testing.T) {
		host, port := testutil.ParseAddr("127.0.0.1:8080")
		assert.Equal(t, "127.0.0.1", host)
		assert.Equal(t, uint32(8080), port)
	})

	t.Run("keeps a bracketed IPv6 host unbracketed", func(t *testing.T) {
		host, port := testutil.ParseAddr("[::1]:4328")
		assert.Equal(t, "::1", host)
		assert.Equal(t, uint32(4328), port)
	})

	t.Run("handles the wildcard host a :0 listener reports", func(t *testing.T) {
		host, port := testutil.ParseAddr(":9999")
		assert.Empty(t, host)
		assert.Equal(t, uint32(9999), port)
	})

	// Both errors are deliberately swallowed by ParseAddr, so the contract on
	// malformed input is "zero values, no panic" -- callers are tests that pass
	// an address they just got from a listener.
	t.Run("returns zero values for input with no port", func(t *testing.T) {
		host, port := testutil.ParseAddr("localhost")
		assert.Empty(t, host)
		assert.Equal(t, uint32(0), port)
	})

	t.Run("returns a zero port for a non-numeric port", func(t *testing.T) {
		host, port := testutil.ParseAddr("localhost:http")
		assert.Equal(t, "localhost", host)
		assert.Equal(t, uint32(0), port)
	})

	// The cases a lenient scanner gets WRONG rather than merely failing on: it
	// stops at the first non-digit and reports success, so each of these would
	// come back as a plausible port that dials the wrong thing.
	t.Run("rejects a port with a trailing suffix instead of parsing its prefix", func(t *testing.T) {
		host, port := testutil.ParseAddr("localhost:80abc")
		assert.Equal(t, "localhost", host)
		assert.Equal(t, uint32(0), port, "a partial parse must not pass for a port")
	})

	t.Run("rejects the protocol-suffixed form a container mapped port carries", func(t *testing.T) {
		// testcontainers' network.Port stringifies as "<num>/<proto>". Reading
		// that as 5432 would hide the very mistake the store tests avoid by
		// calling port.Port().
		host, port := testutil.ParseAddr("localhost:5432/tcp")
		assert.Equal(t, "localhost", host)
		assert.Equal(t, uint32(0), port)
	})

	t.Run("rejects a port above the 16-bit range", func(t *testing.T) {
		// strconv reports 65535 alongside its range error; returning that
		// instead of zero would silently rewrite the caller's port.
		host, port := testutil.ParseAddr("localhost:65536")
		assert.Equal(t, "localhost", host)
		assert.Equal(t, uint32(0), port)
	})

	t.Run("accepts the largest valid port", func(t *testing.T) {
		host, port := testutil.ParseAddr("localhost:65535")
		assert.Equal(t, "localhost", host)
		assert.Equal(t, uint32(65535), port)
	})

	t.Run("rejects a negative port", func(t *testing.T) {
		host, port := testutil.ParseAddr("localhost:-1")
		assert.Equal(t, "localhost", host)
		assert.Equal(t, uint32(0), port)
	})

	t.Run("returns zero values for the empty string", func(t *testing.T) {
		host, port := testutil.ParseAddr("")
		assert.Empty(t, host)
		assert.Equal(t, uint32(0), port)
	})

	t.Run("round-trips the address of a real listener", func(t *testing.T) {
		addr := testutil.StartEchoServer(t)
		host, port := testutil.ParseAddr(addr)
		assert.Equal(t, "127.0.0.1", host)
		assert.NotZero(t, port, "an ephemeral listener must report a bound port")

		// Dial the PARSED pair, not a re-slice of the original string: that is
		// what makes this a round-trip. A ParseAddr returning a wrong non-zero
		// port fails here instead of passing on NotZero alone.
		conn, err := net.Dial("tcp", net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10)))
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	})
}
