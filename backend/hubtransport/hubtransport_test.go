package hubtransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// recorder captures what each request actually arrived on, which is the only
// way to assert a protocol choice: the client API reports no protocol, and a
// transport that silently picked the wrong one still answers 204.
type recorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	proto  string
	method string
	path   string
	tls    bool
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.requests = append(r.requests, recordedRequest{
		proto:  req.Proto,
		method: req.Method,
		path:   req.URL.Path,
		tls:    req.TLS != nil,
	})
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *recorder) all() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.requests...)
}

// protos returns the protocol of each APPLICATION request.
//
// It drops the `PRI * HTTP/2.0` line, because an HTTP/1.1-only Go server hands
// the h2c probe's connection preface to the handler as a request of its own
// (net/http's http1ServerSupportsRequest lets `PRI` through). That artifact is
// the probe, not a request this package sent, and asserting on it here would
// make every fallback test read as though the client sent two.
func (r *recorder) protos() []string {
	out := []string{}
	for _, req := range r.all() {
		if req.method == "PRI" {
			continue
		}
		out = append(out, req.proto)
	}
	return out
}

func get(t *testing.T, client *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	return resp, err
}

func mustNew(t *testing.T, url string) *Endpoint {
	t.Helper()
	endpoint, err := New(url)
	require.NoError(t, err, "New(%q)", url)
	t.Cleanup(endpoint.CloseIdleConnections)
	return endpoint
}

// trusting builds an Endpoint that trusts srv's self-signed certificate, the
// way an operator trusts a private CA through the system store.
func trusting(t *testing.T, srv *httptest.Server) *Endpoint {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	endpoint, err := newEndpoint(srv.URL, roots)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	return endpoint
}

// --- cleartext: h2c preferred, HTTP/1.1 fallback ------------------------

// TestUnaryClientUsesH2CAgainstAnH2CHub is the unit-level regression for the
// reported bug: every hub call from a worker-spawned CLI failed with
// "http2: unencrypted HTTP/2 not enabled" against a plaintext hub.
func TestUnaryClientUsesH2CAgainstAnH2CHub(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewServer(t, rec)

	resp, err := get(t, mustNew(t, srv.URL).UnaryClient(DefaultUnaryTimeout), srv.URL+"/probe")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, []string{"HTTP/2.0"}, rec.protos())
}

func TestUnaryClientFallsBackToHTTP11(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewHTTP1Server(t, rec)

	endpoint := mustNew(t, srv.URL)
	resp, err := get(t, endpoint.UnaryClient(DefaultUnaryTimeout), srv.URL+"/probe")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, []string{"HTTP/1.1"}, rec.protos())

	// The probe ran once, reached a verdict, and the verdict stands: a second
	// request costs no second probe.
	_, err = get(t, endpoint.UnaryClient(DefaultUnaryTimeout), srv.URL+"/probe")
	require.NoError(t, err)
	assert.Equal(t, []string{"HTTP/1.1", "HTTP/1.1"}, rec.protos())
	assert.EqualValues(t, 1, endpoint.prober.calls.Load(), "one probe for the life of the endpoint")
}

// TestUnaryClientProbesOnlyOnceAcrossLanes pins that the lanes share one
// verdict: an Endpoint is what a process holds so that four clients cost one
// probe and one pool per protocol.
func TestUnaryClientProbesOnlyOnceAcrossLanes(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewServer(t, rec)
	endpoint := mustNew(t, srv.URL)

	for _, client := range []*http.Client{
		endpoint.UnaryClient(DefaultUnaryTimeout),
		endpoint.StreamClient(),
		endpoint.HTTP2OnlyClient(),
	} {
		_, err := get(t, client, srv.URL+"/probe")
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, endpoint.prober.calls.Load())
	assert.Equal(t, []string{"HTTP/2.0", "HTTP/2.0", "HTTP/2.0"}, rec.protos())
}

// TestHTTP2OnlyClientFailsClosedWithoutH2C covers the worker's own Connect
// stream, which is bidirectional gRPC: HTTP/1.1 cannot carry it, so a silent
// fallback would produce a worker that half-works with nothing naming why.
func TestHTTP2OnlyClientFailsClosedWithoutH2C(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewHTTP1Server(t, rec)

	_, err := get(t, mustNew(t, srv.URL).HTTP2OnlyClient(), srv.URL+"/connect")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrH2CUnsupported)
	assert.Contains(t, err.Error(), srv.URL, "the error must name the endpoint an operator configured")
	assert.Empty(t, rec.protos(), "the request must not reach the endpoint at all")
}

func TestUnaryClientTimeoutApplies(t *testing.T) {
	release := make(chan struct{})
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	// Registered AFTER the server, so it runs BEFORE the server's own cleanup:
	// t.Cleanup is last-in-first-out, and httptest.Server.Close waits for every
	// outstanding request. A handler still parked here would deadlock the test.
	t.Cleanup(func() { close(release) })

	_, err := get(t, mustNew(t, srv.URL).UnaryClient(50*time.Millisecond), srv.URL+"/slow")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"a hub that accepts a connection and never answers must not hang the caller")
}

// TestLaneTimeouts pins the rule that the lane names carry: only the unary
// lane has an overall timeout, because http.Client.Timeout covers the body
// read and a stream's body ends only when the stream does.
func TestLaneTimeouts(t *testing.T) {
	endpoint := mustNew(t, "http://hub.invalid:4327")
	assert.Equal(t, DefaultUnaryTimeout, endpoint.UnaryClient(DefaultUnaryTimeout).Timeout)
	assert.Zero(t, endpoint.StreamClient().Timeout)
	assert.Zero(t, endpoint.HTTP2OnlyClient().Timeout)
	assert.Zero(t, endpoint.WebSocketClient().Timeout)
}

// --- the WebSocket rule -------------------------------------------------

func TestWebSocketClientAlwaysUsesHTTP11(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewServer(t, rec)
	endpoint := mustNew(t, srv.URL)

	// A plain request through the WebSocket lane, against a server that OFFERS
	// h2c. The lane must still be HTTP/1.1, so an upgrade on it cannot land on
	// an HTTP/2 connection that a preceding plain request opened.
	_, err := get(t, endpoint.WebSocketClient(), srv.URL+"/ws")
	require.NoError(t, err)
	assert.Equal(t, []string{"HTTP/1.1"}, rec.protos())
	assert.Zero(t, endpoint.prober.calls.Load(), "the WebSocket lane has no protocol to choose")
}

func TestWebSocketRoundTripsOverPlaintext(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewServer(t, echoWebSocket(t, rec))
	assertWebSocketEcho(t, mustNew(t, srv.URL), srv.URL)
	require.Len(t, rec.all(), 1)
	assert.Equal(t, "HTTP/1.1", rec.all()[0].proto)
}

func TestWebSocketRoundTripsOverTLS(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewTLSServer(t, echoWebSocket(t, rec))
	assertWebSocketEcho(t, trusting(t, srv), srv.URL)
	require.Len(t, rec.all(), 1)
	assert.Equal(t, "HTTP/1.1", rec.all()[0].proto, "the upgrade must not ride the ALPN-negotiated h2 connection")
	assert.True(t, rec.all()[0].tls)
}

// TestNonWebSocketLaneRefusesAnUpgrade covers the defect that broke
// /ws/userevents for every remote hub: one client served the unary bridge and
// the WebSocket streamer, and an HTTP/2 transport cannot carry an upgrade.
func TestNonWebSocketLaneRefusesAnUpgrade(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewServer(t, rec)
	endpoint := mustNew(t, srv.URL)

	for name, client := range map[string]*http.Client{
		"unary":     endpoint.UnaryClient(DefaultUnaryTimeout),
		"stream":    endpoint.StreamClient(),
		"http2only": endpoint.HTTP2OnlyClient(),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := websocket.Dial(context.Background(), srv.URL+"/ws", &websocket.DialOptions{HTTPClient: client})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrWebSocketLane)
		})
	}
	assert.Empty(t, rec.all(), "a refused upgrade must not reach the endpoint")
}

func TestIsWebSocketUpgrade(t *testing.T) {
	cases := []struct {
		name       string
		connection []string
		upgrade    string
		want       bool
	}{
		{"plain upgrade", []string{"Upgrade"}, "websocket", true},
		{"token list", []string{"keep-alive, Upgrade"}, "websocket", true},
		{"case folded", []string{"upgrade"}, "WebSocket", true},
		{"two header lines", []string{"keep-alive", "Upgrade"}, "websocket", true},
		{"h2c upgrade is not a WebSocket", []string{"Upgrade"}, "h2c", false},
		{"upgrade header without connection", nil, "websocket", false},
		{"connection without upgrade header", []string{"Upgrade"}, "", false},
		{"ordinary request", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://hub.invalid/", nil)
			require.NoError(t, err)
			for _, v := range tc.connection {
				req.Header.Add("Connection", v)
			}
			if tc.upgrade != "" {
				req.Header.Set("Upgrade", tc.upgrade)
			}
			assert.Equal(t, tc.want, isWebSocketUpgrade(req))
		})
	}
}

// --- TLS ----------------------------------------------------------------

// TestHTTPSFailsClosedOnSelfSignedCert covers two defects at once. Reaching
// certificate VERIFICATION proves a ClientHello went out, so the worker's hub
// client no longer sends an HTTP/2 cleartext preface at a TLS port; and the
// failure proves the InsecureSkipVerify that carried a delegation bearer is
// gone.
func TestHTTPSFailsClosedOnSelfSignedCert(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewTLSServer(t, rec)

	for name, client := range laneClients(t, mustNew(t, srv.URL)) {
		t.Run(name, func(t *testing.T) {
			_, err := get(t, client, srv.URL+"/probe")
			require.Error(t, err)
			var certErr *tls.CertificateVerificationError
			assert.ErrorAs(t, err, &certErr, "want a certificate-verification failure, got %v", err)
		})
	}
	assert.Empty(t, rec.all())
}

func TestHTTPSNegotiatesH2WhenTrusted(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewTLSServer(t, rec)
	endpoint := trusting(t, srv)

	resp, err := get(t, endpoint.UnaryClient(DefaultUnaryTimeout), srv.URL+"/probe")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, []string{"HTTP/2.0"}, rec.protos())
	assert.True(t, rec.all()[0].tls)
	assert.Nil(t, endpoint.prober, "ALPN settles the protocol; a TLS endpoint must not probe")
}

// TestHTTPSFallsBackToHTTP11ThroughALPN covers the TLS half of the same
// preference: a reverse proxy that offers http/1.1 alone still works.
func TestHTTPSFallsBackToHTTP11ThroughALPN(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewHTTP1TLSServer(t, rec)

	resp, err := get(t, trusting(t, srv).UnaryClient(DefaultUnaryTimeout), srv.URL+"/probe")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, []string{"HTTP/1.1"}, rec.protos())
}

// TestHTTPSHTTP2OnlyLaneFailsWithoutH2 is the TLS half of "the worker's own
// connection never degrades": the lane offers h2 alone in its ALPN list, so a
// proxy that speaks HTTP/1.1 only refuses the handshake instead of serving a
// protocol the bidirectional stream cannot use.
func TestHTTPSHTTP2OnlyLaneFailsWithoutH2(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewHTTP1TLSServer(t, rec)
	endpoint := trusting(t, srv)

	_, err := get(t, endpoint.HTTP2OnlyClient(), srv.URL+"/connect")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no application protocol")
	assert.Empty(t, rec.protos())

	// The lanes that CAN use HTTP/1.1 still reach the same hub.
	resp, err := get(t, endpoint.UnaryClient(DefaultUnaryTimeout), srv.URL+"/rpc")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, []string{"HTTP/1.1"}, rec.protos())
}

// --- local IPC ----------------------------------------------------------

func TestLocalSocketCarriesBothLanes(t *testing.T) {
	rec := &recorder{}
	mux := http.NewServeMux()
	mux.Handle("/ws", echoWebSocket(t, rec))
	mux.Handle("/", rec)
	socketURL := serveOverSocket(t, mux)

	endpoint := mustNew(t, socketURL)
	assert.Equal(t, locallisten.LocalConnectURL, endpoint.BaseURL())
	assert.Equal(t, socketURL, endpoint.URL())

	resp, err := get(t, endpoint.UnaryClient(DefaultUnaryTimeout), endpoint.BaseURL()+"/rpc")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	assertWebSocketEcho(t, endpoint, endpoint.BaseURL())

	protos := []string{}
	for _, req := range rec.all() {
		protos = append(protos, req.proto)
	}
	assert.Equal(t, []string{"HTTP/2.0", "HTTP/1.1"}, protos,
		"the RPC rides h2c and the upgrade rides HTTP/1.1, over one socket")
}

// TestLocalSchemesAreNotDialedOverTCP is the regression guard that used to sit
// beside the worker's hub client: a scheme-dispatch bug routes a `unix:` or
// `npipe:` URL to the TCP dialer, which then resolves the socket path through
// DNS.
func TestLocalSchemesAreNotDialedOverTCP(t *testing.T) {
	for name, url := range map[string]string{
		"unix":  "unix:/nonexistent/leapmux.sock",
		"npipe": "npipe:leapmux-hub-nonexistent",
	} {
		t.Run(name, func(t *testing.T) {
			endpoint := mustNew(t, url)
			require.Equal(t, locallisten.LocalConnectURL, endpoint.BaseURL())

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.BaseURL()+"/probe", nil)
			require.NoError(t, err)
			_, err = endpoint.UnaryClient(time.Second).Do(req)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "no such host", "%s dispatched through DNS", url)
			assert.NotContains(t, err.Error(), "dial tcp", "%s dispatched to the TCP dialer", url)
		})
	}
}

// --- construction -------------------------------------------------------

func TestNewRejectsAnUnsupportedURL(t *testing.T) {
	for name, url := range map[string]string{
		"empty":          "",
		"unknown scheme": "ftp://hub.example",
		"no scheme":      "hub.example:4327",
		"no host":        "http://",
		"unix no target": "unix:",
		"npipe target":   "npipe:",
	} {
		t.Run(name, func(t *testing.T) {
			endpoint, err := New(url)
			require.Error(t, err)
			assert.Nil(t, endpoint)
		})
	}
}

func TestBaseURLIsVerbatimForRemoteEndpoints(t *testing.T) {
	endpoint := mustNew(t, "http://127.0.0.1:4327")
	assert.Equal(t, "http://127.0.0.1:4327", endpoint.BaseURL())
	assert.Equal(t, "http://127.0.0.1:4327", endpoint.URL())
}

func TestCloseIdleConnectionsReachesEveryLeg(t *testing.T) {
	rec := &recorder{}
	srv := hubtransporttest.NewServer(t, rec)
	endpoint := mustNew(t, srv.URL)

	_, err := get(t, endpoint.UnaryClient(DefaultUnaryTimeout), srv.URL+"/rpc")
	require.NoError(t, err)
	_, err = get(t, endpoint.WebSocketClient(), srv.URL+"/rest")
	require.NoError(t, err)

	require.Len(t, endpoint.legs, 2, "a cleartext endpoint has an h2c leg and an HTTP/1.1 leg")
	endpoint.CloseIdleConnections()

	// Both legs answer again afterwards, which is what proves the call closed
	// idle connections rather than the transports.
	_, err = get(t, endpoint.UnaryClient(DefaultUnaryTimeout), srv.URL+"/rpc")
	require.NoError(t, err)
	_, err = get(t, endpoint.WebSocketClient(), srv.URL+"/rest")
	require.NoError(t, err)
}

// --- helpers ------------------------------------------------------------

func laneClients(t *testing.T, endpoint *Endpoint) map[string]*http.Client {
	t.Helper()
	return map[string]*http.Client{
		"unary":     endpoint.UnaryClient(DefaultUnaryTimeout),
		"stream":    endpoint.StreamClient(),
		"http2only": endpoint.HTTP2OnlyClient(),
		"websocket": endpoint.WebSocketClient(),
	}
}

// echoWebSocket accepts one upgrade and echoes one binary message back.
func echoWebSocket(t *testing.T, rec *recorder) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{proto: r.Proto, method: r.Method, path: r.URL.Path, tls: r.TLS != nil})
		rec.mu.Unlock()

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusInternalError, "") }()
		typ, msg, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if err := conn.Write(r.Context(), typ, msg); err != nil {
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
}

func assertWebSocketEcho(t *testing.T, endpoint *Endpoint, baseURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, baseURL+"/ws", &websocket.DialOptions{HTTPClient: endpoint.WebSocketClient()})
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	require.NoError(t, conn.Write(ctx, websocket.MessageBinary, []byte("ping")))
	typ, got, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageBinary, typ)
	assert.Equal(t, []byte("ping"), got)
}

// serveOverSocket starts handler on a unix socket or a Windows named pipe,
// serving both protocols the way the Hub and the worker control-IPC server do.
// httptest cannot do this: its server owns a TCP listener.
func serveOverSocket(t *testing.T, handler http.Handler) string {
	t.Helper()
	socketURL := locallistentest.UniqueListenURL(t, "hubtransport")
	ln, err := locallisten.Listen(socketURL)
	require.NoError(t, err)

	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, Protocols: protocols}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
	require.NoError(t, locallisten.WaitReady(context.Background(), socketURL))
	return socketURL
}

// --- an HTTP proxy in front of a cleartext endpoint ----------------------

// withProxy points the package at a fixed proxy for the duration of one test.
func withProxy(t *testing.T, proxyURL string) {
	t.Helper()
	parsed, err := url.Parse(proxyURL)
	require.NoError(t, err)
	previous := proxyForRequest
	proxyForRequest = func(*http.Request) (*url.URL, error) { return parsed, nil }
	t.Cleanup(func() { proxyForRequest = previous })
}

// TestProxiedEndpointUsesHTTP11 covers the deployment where HTTP_PROXY names a
// proxy for the hub. Prior-knowledge h2c cannot pass through one, so the lanes
// that can use HTTP/1.1 must keep working through the proxy rather than
// insisting on a protocol that the hop in the middle cannot carry.
func TestProxiedEndpointUsesHTTP11(t *testing.T) {
	rec := &recorder{}
	// The "proxy" is an ordinary server: a proxied client sends the absolute
	// URL to it, so what matters here is that the request went to this address
	// and not to the hub's.
	proxy := hubtransporttest.NewServer(t, rec)
	withProxy(t, proxy.URL)

	endpoint := mustNew(t, "http://hub.invalid:4327")
	assert.Nil(t, endpoint.prober, "a proxied endpoint has no h2c support to measure")
	require.Len(t, endpoint.legs, 1, "and therefore no h2c leg to build")

	for name, client := range map[string]*http.Client{
		"unary":     endpoint.UnaryClient(DefaultUnaryTimeout),
		"websocket": endpoint.WebSocketClient(),
	} {
		resp, err := get(t, client, "http://hub.invalid:4327/rpc")
		require.NoError(t, err, name)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode, name)
	}
	assert.Equal(t, []string{"HTTP/1.1", "HTTP/1.1"}, rec.protos())
}

// TestProxiedEndpointRefusesTheHTTP2Lane covers the worker's own Connect
// stream behind such a proxy. It cannot work, so it fails with a message that
// names the proxy rather than with a protocol error from inside connect-go.
func TestProxiedEndpointRefusesTheHTTP2Lane(t *testing.T) {
	rec := &recorder{}
	proxy := hubtransporttest.NewServer(t, rec)
	withProxy(t, proxy.URL)

	_, err := get(t, mustNew(t, "http://hub.invalid:4327").HTTP2OnlyClient(), "http://hub.invalid:4327/connect")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrH2CUnsupported)
	assert.Contains(t, err.Error(), proxy.URL, "the failure must name the proxy in the way")
	assert.Contains(t, err.Error(), "http://hub.invalid:4327")
	assert.Empty(t, rec.all(), "the request must not go out at all")
}
