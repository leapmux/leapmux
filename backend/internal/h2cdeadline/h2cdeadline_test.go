package h2cdeadline

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

// headerTimeout is sized so a healthy local handshake finishes far inside it
// but the handler's deliberate stall lands well outside it: without the
// workaround the connection dies at the deadline mid-stream; with it, the
// deadline never applies to the h2c connection at all.
const headerTimeout = 150 * time.Millisecond

func newTestServer(t *testing.T, handler http.Handler) (url string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: headerTimeout,
		Protocols:         protocols,
	}
	go func() { _ = srv.Serve(Wrap(ln)) }()
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
// because the header deadline a connection armed before the HTTP/2 handoff
// must not govern frame reads for the rest of its life.
func TestH2CStreamSurvivesHeaderTimeout(t *testing.T) {
	const stall = 4 * headerTimeout
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(stall)
		_, _ = io.WriteString(w, "still alive")
	})
	url := newTestServer(t, handler)

	resp, err := h2cClient(t).Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "still alive", string(body))
}

// TestHTTP1SlowHeadersStillTimeOut pins the other half of the contract: the
// wrapper disarms the deadline ONLY for h2c connections. An HTTP/1.1 client
// dribbling its request headers must still be cut off by ReadHeaderTimeout.
func TestHTTP1SlowHeadersStillTimeOut(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached")
	})
	url := newTestServer(t, handler)

	conn, err := net.DialTimeout("tcp", url[len("http://"):], time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Send a valid request line, then go quiet mid-headers. A server that
	// honors ReadHeaderTimeout closes the connection at the deadline; a
	// server that lost the deadline would leave this read parked until the
	// test's own cleanup tears everything down.
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: l"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(5 * headerTimeout))
	_, err = conn.Read(make([]byte, 1))
	assert.Error(t, err, "server should have closed the connection at the header deadline")
}

// recordingConn scripts the reads a fresh connection would deliver and
// records every SetReadDeadline call, which is the wrapper's only output
// besides passing the bytes through.
type recordingConn struct {
	net.Conn
	mu        sync.Mutex
	reads     [][]byte
	deadlines []time.Time
}

func (c *recordingConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reads) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.reads[0])
	if n == len(c.reads[0]) {
		c.reads = c.reads[1:]
	} else {
		c.reads[0] = c.reads[0][n:]
	}
	return n, nil
}

func (c *recordingConn) SetReadDeadline(d time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadlines = append(c.deadlines, d)
	return nil
}

func (c *recordingConn) recordedDeadlines() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.deadlines...)
}

// TestConnClearsDeadlineOnFragmentedPreface drives the preface ONE BYTE PER
// READ, the worst fragmentation TCP can produce: the wrapper must accumulate
// the match across reads, clear the deadline exactly once at the final byte,
// and never distort the byte stream doing it. The integration test above
// only sees the preface arrive in a single read, which hides both halves.
func TestConnClearsDeadlineOnFragmentedPreface(t *testing.T) {
	fc := &recordingConn{}
	for _, b := range clientPreface {
		fc.reads = append(fc.reads, []byte{b})
	}
	fc.reads = append(fc.reads, []byte("SETTINGS framing follows"))
	c := &conn{Conn: fc}

	var got bytes.Buffer
	p := make([]byte, 8)
	for {
		n, err := c.Read(p)
		got.Write(p[:n])
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}

	assert.Equal(t, append(clientPreface, []byte("SETTINGS framing follows")...),
		got.Bytes(), "the wrapper must only observe reads, never alter them")
	assert.Equal(t, []time.Time{{}}, fc.recordedDeadlines(),
		"exactly one deadline clear, and only after the FULL preface")
}

// TestConnKeepsDeadlineWhenPrefacePrefixDiverges pins the misclassification
// guard: bytes that START like the preface but diverge partway — an HTTP/1.x
// request line is the real-world shape — must never clear the deadline, at
// any fragmentation.
func TestConnKeepsDeadlineWhenPrefacePrefixDiverges(t *testing.T) {
	nearMiss := []byte("PRI * HTTP/1.1\r\nHost: x\r\n\r\n")
	fc := &recordingConn{
		reads: [][]byte{nearMiss[:13], nearMiss[13:], []byte("GET body")},
	}
	c := &conn{Conn: fc}

	var got bytes.Buffer
	p := make([]byte, 32)
	for {
		n, err := c.Read(p)
		got.Write(p[:n])
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}

	assert.Equal(t, nearMiss, got.Bytes()[:len(nearMiss)])
	assert.Empty(t, fc.recordedDeadlines(),
		"a connection that stops matching the preface keeps the header deadline")
}
