package hubtransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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

	require.Len(t, endpoint.transports, 2, "a cleartext endpoint has an h2c transport and an HTTP/1.1 one")
	endpoint.CloseIdleConnections()

	// Both transports answer again afterwards, which proves the call closed
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

// --- IPv6, idle connections, and the per-request proxy test ---------------

// TestDialAddressBracketsAnIPv6LiteralOnce pins the address the probe dials.
//
// u.Host KEEPS the brackets around an IPv6 literal and net.JoinHostPort adds
// its own, so the default-port path produced `[[::1]]:80`. net.Dial refuses
// that, so the probe never reached a bracketed IPv6 endpoint, the verdict
// stayed undecided, and such an endpoint never fell back to HTTP/1.1.
func TestDialAddressBracketsAnIPv6LiteralOnce(t *testing.T) {
	for raw, want := range map[string]string{
		"http://[::1]":            "[::1]:80",
		"http://[::1]:8080":       "[::1]:8080",
		"http://[fe80::1%25en0]":  "[fe80::1%en0]:80",
		"http://hub.example":      "hub.example:80",
		"http://hub.example:4327": "hub.example:4327",
		"http://127.0.0.1":        "127.0.0.1:80",
	} {
		u, err := url.Parse(raw)
		require.NoError(t, err, raw)
		address := dialAddress(u)
		assert.Equal(t, want, address, raw)
		// The address must be one net.Dial accepts, which is the property the
		// double-bracketed form broke.
		host, port, err := net.SplitHostPort(address)
		require.NoErrorf(t, err, "%s -> %q is not a dialable address", raw, address)
		assert.NotEmpty(t, host)
		assert.NotEmpty(t, port)
	}
}

// countingTransport records CloseIdleConnections reaching the wrapped
// transport, which is the call the lane wrappers must forward.
type countingTransport struct{ closed atomic.Int64 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("countingTransport does not carry requests")
}

func (c *countingTransport) CloseIdleConnections() { c.closed.Add(1) }

// TestLaneWrappersForwardCloseIdleConnections pins the forwarding that
// http.Client.CloseIdleConnections depends on.
//
// net/http reaches a transport's idle connections through an OPTIONAL
// interface: Client.CloseIdleConnections type-asserts its Transport for
// `interface{ CloseIdleConnections() }` and does NOTHING when the assertion
// fails. A wrapper carrying RoundTrip alone therefore turns the call into a
// silent no-op — which is how the desktop shutdown stopped releasing the
// named-pipe handles it must free before it starts the Hub again.
func TestLaneWrappersForwardCloseIdleConnections(t *testing.T) {
	t.Run("guardWebSocket forwards to the wrapped round tripper", func(t *testing.T) {
		inner := &countingTransport{}
		guardWebSocket{next: inner}.CloseIdleConnections()
		assert.EqualValues(t, 1, inner.closed.Load())
	})

	t.Run("preferH2C closes BOTH of its transports", func(t *testing.T) {
		h2c, h1 := &countingTransport{}, &countingTransport{}
		preferH2C{h2c: h2c, h1: h1}.CloseIdleConnections()
		assert.EqualValues(t, 1, h2c.closed.Load(), "the h2c transport")
		assert.EqualValues(t, 1, h1.closed.Load(), "the HTTP/1.1 transport")
	})

	t.Run("requireH2C forwards to its transport", func(t *testing.T) {
		h2c := &countingTransport{}
		requireH2C{h2c: h2c}.CloseIdleConnections()
		assert.EqualValues(t, 1, h2c.closed.Load())
	})
}

// TestEveryLaneClientCanCloseIdleConnections is the end-to-end half: net/http
// must find the method on whatever each lane hands out, for every scheme.
func TestEveryLaneClientCanCloseIdleConnections(t *testing.T) {
	tls := hubtransporttest.NewTLSServer(t, &recorder{})
	plaintext := hubtransporttest.NewServer(t, &recorder{})
	socket := t.TempDir() + "/ht.sock"

	for name, endpoint := range map[string]*Endpoint{
		"plaintext": mustNew(t, plaintext.URL),
		"tls":       trusting(t, tls),
		"socket":    mustNew(t, "unix:"+socket),
	} {
		for lane, client := range map[string]*http.Client{
			"unary":     endpoint.UnaryClient(DefaultUnaryTimeout),
			"stream":    endpoint.StreamClient(),
			"http2only": endpoint.HTTP2OnlyClient(),
			"websocket": endpoint.WebSocketClient(),
		} {
			_, ok := client.Transport.(interface{ CloseIdleConnections() })
			assert.Truef(t, ok, "%s/%s: net/http reaches idle connections only through this interface", name, lane)
		}
	}
}

// TestProxiedRequestNeverRoutesAroundTheProxy pins the per-request proxy test.
//
// The proxy decision used to run ONCE, at construction, against the endpoint
// URL, while the h2c transport carried `Proxy = nil` and dialed every host
// itself. With a proxy configured and NO_PROXY covering the hub, the two
// disagreed: construction saw no proxy, so it built the h2c transport, and
// that transport then dialed a proxied host directly — routing around the
// proxy the operator configured. net/http follows a redirect through the same
// round tripper with the new host, and no client here sets CheckRedirect.
func TestProxiedRequestNeverRoutesAroundTheProxy(t *testing.T) {
	rec := &recorder{}
	proxy := hubtransporttest.NewServer(t, rec)
	hub := hubtransporttest.NewServer(t, &recorder{})

	hubURL, err := url.Parse(hub.URL)
	require.NoError(t, err)
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)

	// The shape NO_PROXY produces: the hub is direct, every other host is
	// proxied. The endpoint IS the hub, so construction sees no proxy at all.
	previous := proxyForRequest
	proxyForRequest = func(req *http.Request) (*url.URL, error) {
		if req.URL.Host == hubURL.Host {
			return nil, nil
		}
		return proxyURL, nil
	}
	t.Cleanup(func() { proxyForRequest = previous })

	endpoint := mustNew(t, hub.URL)
	require.NotNil(t, endpoint.prober, "the hub itself is direct, so its h2c support is still worth measuring")

	// A request to a DIFFERENT host on the h2c-preferring lane. Without the
	// per-request test this dials elsewhere.invalid directly and fails to
	// resolve; with it the request goes to the proxy, which answers.
	resp, err := get(t, endpoint.UnaryClient(DefaultUnaryTimeout), "http://elsewhere.invalid/rpc")
	require.NoError(t, err, "a proxied host must reach the proxy from every lane")
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.NotEmpty(t, rec.all(), "the proxy carries the request")
}

// TestProxiedHTTP2LaneNamesTheProxyPerRequest is the requireH2C half of the
// same rule: the stream lane refuses, and says which proxy is in the way.
func TestProxiedHTTP2LaneNamesTheProxyPerRequest(t *testing.T) {
	proxy := hubtransporttest.NewServer(t, &recorder{})
	hub := hubtransporttest.NewServer(t, &recorder{})

	hubURL, err := url.Parse(hub.URL)
	require.NoError(t, err)
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	previous := proxyForRequest
	proxyForRequest = func(req *http.Request) (*url.URL, error) {
		if req.URL.Host == hubURL.Host {
			return nil, nil
		}
		return proxyURL, nil
	}
	t.Cleanup(func() { proxyForRequest = previous })

	endpoint := mustNew(t, hub.URL)
	// The hub itself is direct, so the stream lane works against it.
	_, err = get(t, endpoint.HTTP2OnlyClient(), hub.URL+"/connect")
	require.NoError(t, err, "a direct host keeps the HTTP/2 lane")

	// A proxied host does not, and the refusal identifies the proxy.
	_, err = get(t, endpoint.HTTP2OnlyClient(), "http://elsewhere.invalid/connect")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrH2CUnsupported)
	assert.Contains(t, err.Error(), proxy.URL, "the failure must identify the proxy in the way")
}

// closeCountingBody records Close, so a refusal can be checked against the
// RoundTripper contract.
type closeCountingBody struct {
	io.Reader
	closed atomic.Int64
}

func (b *closeCountingBody) Close() error {
	b.closed.Add(1)
	return nil
}

// TestARefusedRequestClosesItsBody pins the half of the http.RoundTripper
// contract a refusing lane can break.
//
// "RoundTrip must always close the body, including on errors" — a caller may be
// blocked writing to it. A lane that refuses never reaches the transport that
// would have closed it. connect-go closes its own pipe on a RoundTrip error, so
// nothing leaks today; that is its defensive code, not a promise this package
// may rely on.
func TestARefusedRequestClosesItsBody(t *testing.T) {
	srv := hubtransporttest.NewHTTP1Server(t, &recorder{})
	endpoint := mustNew(t, srv.URL)

	t.Run("a WebSocket upgrade on a lane that cannot carry one", func(t *testing.T) {
		body := &closeCountingBody{Reader: strings.NewReader("payload")}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/ws", body)
		require.NoError(t, err)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")

		_, err = endpoint.UnaryClient(DefaultUnaryTimeout).Do(req)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrWebSocketLane)
		assert.EqualValues(t, 1, body.closed.Load(), "the refused request's body must be closed")
	})

	t.Run("an HTTP/2-only lane against an endpoint with no h2c", func(t *testing.T) {
		body := &closeCountingBody{Reader: strings.NewReader("payload")}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/connect", body)
		require.NoError(t, err)

		_, err = endpoint.HTTP2OnlyClient().Do(req)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrH2CUnsupported)
		assert.EqualValues(t, 1, body.closed.Load(), "the refused request's body must be closed")
	})
}

// TestEveryLaneRefusesOrCarriesAnUpgradeBySchema is what setLanes exists to
// guarantee.
//
// Three build paths decide the three lanes, and each one used to assign the
// fields and apply guardWebSocket by hand. A fourth path that forgot the guard
// would put an upgrade on an h2c connection, where coder/websocket cannot work
// at all -- and nothing would report it until the first upgrade in production.
// setLanes applies the guard itself, so this holds for every scheme.
func TestEveryLaneRefusesOrCarriesAnUpgradeBySchema(t *testing.T) {
	tlsSrv := hubtransporttest.NewTLSServer(t, &recorder{})
	plaintext := hubtransporttest.NewServer(t, &recorder{})

	for name, endpoint := range map[string]*Endpoint{
		"plaintext": mustNew(t, plaintext.URL),
		"tls":       trusting(t, tlsSrv),
		"socket":    mustNew(t, "unix:"+t.TempDir()+"/lanes.sock"),
	} {
		t.Run(name, func(t *testing.T) {
			// Every lane but the WebSocket one refuses an upgrade.
			for lane, client := range map[string]*http.Client{
				"unary":     endpoint.UnaryClient(DefaultUnaryTimeout),
				"stream":    endpoint.StreamClient(),
				"http2only": endpoint.HTTP2OnlyClient(),
			} {
				req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://hub.invalid/ws", nil)
				require.NoError(t, err)
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")

				_, err = client.Do(req)
				require.Errorf(t, err, "%s/%s", name, lane)
				assert.ErrorIsf(t, err, ErrWebSocketLane,
					"%s/%s must refuse an upgrade with the error that identifies the right client", name, lane)
			}

			// And the WebSocket lane does NOT refuse: it is the one that must
			// carry the upgrade, so the guard must never reach it.
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://hub.invalid/ws", nil)
			require.NoError(t, err)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			_, err = endpoint.WebSocketClient().Do(req)
			// It fails to CONNECT (hub.invalid resolves nowhere), which is the
			// point: it got past the guard and reached the transport.
			require.Error(t, err)
			assert.NotErrorIsf(t, err, ErrWebSocketLane, "%s/websocket must carry an upgrade, not refuse one", name)
		})
	}
}
