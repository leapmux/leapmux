package hub

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

// These two tests pin the net/http contract that NewServer's ReadHeaderTimeout
// relies on: the header deadline guards HTTP/1.1 header reads and does NOT
// survive the handoff to the HTTP/2 server. The Hub hosts long-lived h2c bidi
// streams (the worker's Connect RPC, over the local-IPC listener in solo and
// desktop, and over plain TCP for remote workers) on the same http.Server as
// ordinary HTTP/1.1 traffic, so both halves must hold at once.
//
// Go broke exactly this in 1.25.13 (golang/go#80876): the deadline net/http
// armed before probing for the HTTP/2 preface stayed armed after the handoff,
// and every worker stream died ReadHeaderTimeout after ACCEPT no matter how
// active it was. The Hub carried a listener wrapper for it until the fix
// landed in Go 1.25.14, 1.26.7 and 1.27. The `go` directive in go.mod is the
// mechanical floor -- no toolchain that can build this module lacks the fix --
// and these tests are what fails loudly if a future release regresses again.
//
// Like TestBaseContextCancelsInFlightHandlerOnShutdown, this exercises the
// stdlib mechanism in isolation rather than the production server literal:
// NewServer needs a live store, listeners and keystore, so it is too heavy to
// stand up here. What the two share is the h2c-relevant configuration --
// Protocols with unencrypted HTTP/2 plus a non-zero ReadHeaderTimeout and a
// zero ReadTimeout -- which is the shape the bug needed.

// h2cHeaderTimeout is sized so a healthy local handshake finishes far inside it
// but the handler's deliberate stall lands well outside it.
const h2cHeaderTimeout = 150 * time.Millisecond

// newH2CTestServer starts a server with the Hub's h2c-relevant configuration
// and returns its base URL.
func newH2CTestServer(t *testing.T, handler http.Handler) (url string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: h2cHeaderTimeout,
		Protocols:         protocols,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

func h2cClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

// TestH2CStreamSurvivesHeaderTimeout is the regression test for
// golang/go#80876: a long-lived h2c stream must outlive ReadHeaderTimeout,
// because the header deadline a connection armed before the HTTP/2 handoff must
// not govern frame reads for the rest of its life.
func TestH2CStreamSurvivesHeaderTimeout(t *testing.T) {
	const stall = 4 * h2cHeaderTimeout
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(stall)
		_, _ = io.WriteString(w, "still alive")
	})
	url := newH2CTestServer(t, handler)

	resp, err := h2cClient(t).Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "still alive", string(body))
}

// TestHTTP1SlowHeadersStillTimeOut pins the other half of the contract: the
// deadline is disarmed ONLY for h2c connections. An HTTP/1.1 client dribbling
// its request headers must still be cut off by ReadHeaderTimeout, which is the
// slowloris guard the public TCP listener depends on.
func TestHTTP1SlowHeadersStillTimeOut(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached")
	})
	url := newH2CTestServer(t, handler)

	conn, err := net.DialTimeout("tcp", url[len("http://"):], time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Send a valid request line, then go quiet mid-headers. A server that
	// honors ReadHeaderTimeout closes the connection at the deadline; a server
	// that lost the deadline would leave this read parked until the test's own
	// cleanup tears everything down.
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: l"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(5 * h2cHeaderTimeout))
	_, err = conn.Read(make([]byte, 1))
	assert.Error(t, err, "server should have closed the connection at the header deadline")
}
