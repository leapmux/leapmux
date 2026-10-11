// Package hubtransporttest starts test servers with the protocols that a LeapMux Hub supplies.
//
// A bare httptest.NewServer supplies HTTP/1.1 alone.
// The Hub enables HTTP1 and UnencryptedHTTP2 on one listener in hub/server.go.
// An HTTP/1.1 server accepts the cleartext HTTP/2 (h2c) preface as a PRI request and calls its handler.
// A test that counts handler calls then counts one extra.
// Use NewServer for a faithful Hub. Use NewHTTP1Server only to test fallback.
package hubtransporttest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// NewServer starts a cleartext server with HTTP/1.1 and h2c, as the Hub and Worker control server do.
// Test cleanup closes the server.
func NewServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return start(t, handler, false, func(p *http.Protocols) {
		p.SetHTTP1(true)
		p.SetUnencryptedHTTP2(true)
	})
}

// NewHTTP1Server starts a cleartext server with HTTP/1.1 alone.
// It models a reverse proxy without h2c support, which requires the HTTP/1.1 fallback.
func NewHTTP1Server(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return start(t, handler, false, func(p *http.Protocols) { p.SetHTTP1(true) })
}

// NewTLSServer starts a Transport Layer Security (TLS) server with h2 and http/1.1.
// It offers these protocols through Application-Layer Protocol Negotiation (ALPN).
// A client must trust srv.Certificate() to accept the server's self-signed certificate.
// That requirement proves that the client keeps certificate verification enabled.
func NewTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return start(t, handler, true, func(p *http.Protocols) {
		p.SetHTTP1(true)
		p.SetHTTP2(true)
	})
}

// NewHTTP1TLSServer starts a TLS server that offers http/1.1 alone.
func NewHTTP1TLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return start(t, handler, true, func(p *http.Protocols) { p.SetHTTP1(true) })
}

func start(t *testing.T, handler http.Handler, useTLS bool, setProtocols func(*http.Protocols)) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	protocols := &http.Protocols{}
	setProtocols(protocols)
	srv.Config.Protocols = protocols
	// EnableHTTP2 controls httptest's TLS setup. Protocols controls the server.
	// Both must agree, or StartTLS offers no h2 through ALPN.
	srv.EnableHTTP2 = useTLS && protocols.HTTP2()
	if useTLS {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// NewSocketServer starts a server on a unix: socket or a Windows named pipe.
// It supplies HTTP/1.1 and h2c, as the Hub's local listener and Worker's control listener do.
// It returns the listen URL. Test cleanup closes the server.
//
// name must be unique within the process only.
// UniqueListenURL keeps the socket path within AF_UNIX's 104-byte sun_path limit.
// A path from t.TempDir() exceeds that limit on macOS runners.
//
// This helper applies NewServer's protocol policy to a local socket.
// It keeps the shared setup in one place:
// - Create the listener.
// - Enable both protocols.
// - Set the header timeout on the http.Server.
// - Start the Serve goroutine.
// - Wait for the listener to become ready.
func NewSocketServer(t *testing.T, name string, handler http.Handler) string {
	t.Helper()
	socketURL := locallistentest.UniqueListenURL(t, name)
	ln, err := locallisten.Listen(socketURL)
	if err != nil {
		t.Fatalf("hubtransporttest: listen %s: %v", socketURL, err)
	}
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, Protocols: protocols}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
	if err := locallisten.WaitReady(context.Background(), socketURL); err != nil {
		t.Fatalf("hubtransporttest: %s never became ready: %v", socketURL, err)
	}
	return socketURL
}
