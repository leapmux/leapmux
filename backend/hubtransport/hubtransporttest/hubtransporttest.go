// Package hubtransporttest starts test servers that speak the protocols a
// LeapMux Hub speaks, so a test exercises the transport a caller really gets.
//
// A bare httptest.NewServer speaks HTTP/1.1 alone, which a Hub never does
// (hub/server.go sets HTTP1 and UnencryptedHTTP2 on one listener). Against
// such a server the h2c probe's connection preface arrives as a malformed
// HTTP/1.1 request and REACHES THE HANDLER, so a test that counts handler
// calls counts one extra. Use NewServer for a faithful Hub, and NewHTTP1Server
// only when the point of the test is the fallback.
package hubtransporttest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// NewServer starts a cleartext server that speaks HTTP/1.1 and h2c, as the Hub
// and the worker control-IPC server both do. It is closed when the test ends.
func NewServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return start(t, handler, false, func(p *http.Protocols) {
		p.SetHTTP1(true)
		p.SetUnencryptedHTTP2(true)
	})
}

// NewHTTP1Server starts a cleartext server that speaks HTTP/1.1 ONLY. It
// stands for a reverse proxy with no h2c support, the deployment the fallback
// exists for.
func NewHTTP1Server(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return start(t, handler, false, func(p *http.Protocols) { p.SetHTTP1(true) })
}

// NewTLSServer starts a TLS server that offers both h2 and http/1.1 through
// ALPN. Its certificate is self-signed: a client reaches it only by trusting
// srv.Certificate(), which is what proves that verification stayed on.
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
	// EnableHTTP2 governs httptest's own TLS setup; Protocols governs the
	// server. Both have to agree, or StartTLS offers no h2 in its ALPN list.
	srv.EnableHTTP2 = useTLS && protocols.HTTP2()
	if useTLS {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}
