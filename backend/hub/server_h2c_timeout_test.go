package hub

import (
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests verify the net/http contract that NewServer's ReadHeaderTimeout requires.
// The header deadline protects HTTP/1.1 header reads and ends before HTTP/2 frame reads.
// The Hub serves both protocols on one http.Server.
// Its Worker Connect stream uses cleartext HTTP/2 (h2c) and can remain open indefinitely.
// Solo and desktop use local inter-process communication (IPC). Remote Workers can use plain TCP.
//
// Go 1.25.13 retained the header deadline after the HTTP/2 handoff: golang/go#80876.
// Every Worker stream then ended at ReadHeaderTimeout after accept, including active streams.
// The Hub used a listener wrapper until Go 1.27 supplied the correction.
// Go 1.25.14 and 1.26.7 supplied the correction also.
// The go.mod directive requires a corrected toolchain. These tests detect a later regression.
//
// These tests isolate the standard-library mechanism, as TestBaseContextCancelsInFlightHandlerOnShutdown does.
// NewServer also requires these resources:
// - A live store.
// - Listeners.
// - A keystore.
// Both servers enable unencrypted HTTP/2 with a nonzero ReadHeaderTimeout and a zero ReadTimeout.

// h2cHeaderTimeout permits a local handshake and supplies the real deadline that these tests verify.
const h2cHeaderTimeout = time.Second

// newH2CTestServer starts a server with the Hub's HTTP/2 timeout configuration and returns its base URL.
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
	protocols := &http.Protocols{}
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

// TestH2CStreamSurvivesHeaderTimeout verifies golang/go#80876.
// The header deadline must not govern frame reads after the HTTP/2 handoff.
func TestH2CStreamSurvivesHeaderTimeout(t *testing.T) {
	const stall = 4 * h2cHeaderTimeout
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("HTTP/2 response headers did not flush: %v", err)
			return
		}
		<-release
		_, _ = io.WriteString(w, "still alive")
	})
	url := newH2CTestServer(t, handler)
	t.Cleanup(releaseHandler)

	resp, err := h2cClient(t).Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 2, resp.ProtoMajor)
	// net/http owns a real socket deadline, so a mock clock cannot advance it.
	// The completed handshake starts this wait after the original deadline began.
	// The test releases the handler only after that deadline must expire.
	timer := time.NewTimer(stall)
	defer timer.Stop()
	<-timer.C
	releaseHandler()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "still alive", string(body))
}

// TestHTTP1SlowHeadersStillTimeOut verifies that incomplete HTTP/1.1 headers still trigger ReadHeaderTimeout.
// The public TCP listener requires that protection against slowloris requests.
func TestHTTP1SlowHeadersStillTimeOut(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached")
	})
	url := newH2CTestServer(t, handler)

	conn, err := net.DialTimeout("tcp", url[len("http://"):], 30*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Send a valid request line and stop before its headers end.
	// The server must close the connection at ReadHeaderTimeout.
	// Without that deadline, this read ends only at its independent failure limit.
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: l"))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
	_, err = conn.Read(make([]byte, 1))
	require.Error(t, err, "the server must close the connection at the header deadline")
	var timeout net.Error
	if errors.As(err, &timeout) {
		assert.False(t, timeout.Timeout(), "the server close must precede the independent read deadline")
	}
}
