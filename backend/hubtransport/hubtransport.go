// Package hubtransport builds every HTTP client that reaches a LeapMux
// endpoint: a Hub over `http://`, `https://`, `unix:` or `npipe:`, and a
// worker's control-IPC socket, which speaks the same protocols. One package
// makes the protocol decision, so no two clients in the tree can disagree
// about it.
//
// # Protocol policy
//
// A cleartext endpoint (`http://`, `unix:`, `npipe:`) prefers HTTP/2
// cleartext (h2c) and drops to HTTP/1.1 only when the endpoint proves that it
// does not speak h2c. See probe.go for the proof. A TLS endpoint (`https://`)
// picks the protocol with ALPN and always verifies the certificate against
// the system trust store.
//
// # Lanes
//
// A caller asks for a LANE, never for a protocol, because the protocol that a
// lane needs is a property of what rides the connection:
//
//   - UnaryClient and StreamClient carry request/response calls and Connect
//     server streams. Both work over HTTP/1.1, so both accept the fallback.
//   - HTTP2OnlyClient carries a bidirectional gRPC stream, which HTTP/1.1
//     cannot express. It fails with ErrH2CUnsupported instead of degrading.
//   - WebSocketClient carries an upgrade. A WebSocket cannot ride HTTP/2:
//     coder/websocket needs http.Hijacker, which the HTTP/2 ResponseWriter
//     does not implement. This lane is HTTP/1.1 always.
//
// Every other lane REFUSES a WebSocket upgrade with ErrWebSocketLane. An
// h2c transport that receives one produces `http2: invalid Upgrade request
// header` from three layers down, which identifies neither the cause nor the
// remedy, so the refusal happens here instead.
//
// # TLS
//
// There is no option that turns certificate verification off, and no field
// that could carry one. A private certificate authority belongs in the
// operating system trust store.
package hubtransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/locallisten"
)

// DefaultUnaryTimeout limits one whole request/response exchange on the unary
// lane: the dial, the write, the response and the body read.
//
// A unary lane needs a timeout because a hub that accepts a connection and
// never answers otherwise hangs the caller for ever. A stream lane must NOT
// have one, because http.Client.Timeout covers the body read, and a stream's
// body ends only when the stream does. That is why StreamClient,
// HTTP2OnlyClient and WebSocketClient take no timeout argument.
const DefaultUnaryTimeout = 60 * time.Second

// ErrH2CUnsupported reports that a cleartext endpoint does not speak
// prior-knowledge HTTP/2, on a lane that cannot use HTTP/1.1 instead.
var ErrH2CUnsupported = errors.New("hubtransport: endpoint does not support cleartext HTTP/2 (h2c)")

// ErrWebSocketLane reports a WebSocket upgrade sent through a client that
// cannot carry one.
var ErrWebSocketLane = errors.New("hubtransport: this client cannot carry a WebSocket upgrade; use Endpoint.WebSocketClient")

// Endpoint holds the transports for one endpoint URL. Every client it returns
// shares its connection pools and its h2c verdict, so a process that holds
// four lanes against one hub still probes once and pools once.
//
// A client this hands out is the caller's own value. A caller that needs a
// cookie jar or a redirect policy sets those fields on it; they are properties
// of one caller's session, not of the endpoint's protocols.
type Endpoint struct {
	url     string
	baseURL string

	// rootCAs replaces the system trust store for an `https://` endpoint.
	//
	// UNEXPORTED, with no exported way to reach it, and that is the mechanism
	// rather than a style choice: this package is the one place hub TLS is
	// configured, and a hook for "trust something else" is the road back to
	// the InsecureSkipVerify that this package exists to delete. Only the
	// tests in this package set it, to trust an httptest server's own
	// certificate.
	rootCAs *x509.CertPool

	// The three lane round trippers. Each one is a transport, or a transport
	// behind a guard, decided once in New.
	unary     http.RoundTripper
	http2Only http.RoundTripper
	webSocket http.RoundTripper

	// transports holds every transport built for this endpoint, so
	// CloseIdleConnections reaches all of them.
	transports []*http.Transport

	// prober is nil when the protocol needs no proof, which today means a TLS
	// endpoint: ALPN settles it inside the handshake, per connection.
	//
	// A CLEARTEXT endpoint always has one, even where a proxy is configured for
	// it. Whether a proxy stands in the way is a property of one REQUEST rather
	// than of the endpoint -- NO_PROXY answers per host, and a redirect carries
	// a request to a host the endpoint's own answer does not describe -- so the
	// lanes test it per request and reach the prober only for a direct one.
	prober *prober
}

// New returns the Endpoint for endpointURL, which may be:
//
//   - http://host:port  — a cleartext endpoint reached over TCP
//   - https://host:port — a TLS endpoint, verified against the system roots
//   - unix:<path>       — a Unix domain socket
//   - npipe:<name>      — a Windows named pipe
//
// It builds transports only. No connection opens until a client runs a
// request, so a caller may construct an Endpoint for an address that is not
// listening yet.
func New(endpointURL string) (*Endpoint, error) {
	return newEndpoint(endpointURL, nil)
}

// newEndpoint is New plus the trust-store seam this package's own tests need.
func newEndpoint(endpointURL string, rootCAs *x509.CertPool) (*Endpoint, error) {
	if endpointURL == "" {
		return nil, errors.New("hubtransport: endpoint URL is required")
	}
	e := &Endpoint{url: endpointURL, rootCAs: rootCAs}

	if _, _, err := locallisten.Parse(endpointURL); err == nil {
		if err := e.buildLocal(endpointURL); err != nil {
			return nil, err
		}
		return e, nil
	} else if errors.Is(err, locallisten.ErrMissingTarget) {
		// "unix:" / "npipe:" with nothing after it. Report what the URL is
		// missing; falling through would report "unsupported scheme", which
		// points at the part that IS correct.
		return nil, fmt.Errorf("hubtransport: %w", err)
	}

	u, err := url.Parse(endpointURL)
	if err != nil {
		return nil, fmt.Errorf("hubtransport: parse endpoint URL %q: %w", endpointURL, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("hubtransport: endpoint URL %q has no host", endpointURL)
	}
	e.baseURL = endpointURL
	switch u.Scheme {
	case "http":
		e.buildPlaintext(u)
		return e, nil
	case "https":
		e.buildTLS()
		return e, nil
	default:
		return nil, fmt.Errorf("hubtransport: unsupported endpoint URL %q (expected http://, https://, unix:<path> or npipe:<name>)", endpointURL)
	}
}

// buildLocal wires both transports to the socket dialer. A local endpoint is never
// proxied and never uses TLS: the socket's file permissions are its access
// control.
func (e *Endpoint) buildLocal(endpointURL string) error {
	dial, err := locallisten.Dialer(endpointURL)
	if err != nil {
		return fmt.Errorf("hubtransport: %w", err)
	}
	// ConnectRPC, net/http and coder/websocket all reject a URL whose scheme
	// is not http(s), so a local endpoint presents a placeholder origin. The
	// dial is wired into the transport, which makes the host portion cosmetic.
	e.baseURL = locallisten.LocalConnectURL

	h2c := e.newTransport(protocolsH2C)
	h2c.DialContext = locallisten.HTTPDialContext(dial)
	h2c.Proxy = nil
	h1 := e.newTransport(protocolsHTTP1)
	h1.DialContext = locallisten.HTTPDialContext(dial)
	h1.Proxy = nil

	e.wireCleartext(h2c, h1, dial)
	return nil
}

// proxyForRequest is http.ProxyFromEnvironment, replaced by this package's own
// tests. net/http reads the proxy environment ONCE per process and keeps the
// answer, so a test that set HTTP_PROXY would take effect or not depending on
// what ran before it.
var proxyForRequest = http.ProxyFromEnvironment

// buildPlaintext wires the transports for an `http://` endpoint.
func (e *Endpoint) buildPlaintext(u *url.URL) {
	h1 := e.newTransport(protocolsHTTP1)
	h2c := e.newTransport(protocolsH2C)
	// The h2c transport dials the endpoint directly. Prior-knowledge h2c cannot
	// pass through a plain HTTP proxy -- a proxy reads an HTTP/1.1 request line
	// -- so the lanes in lanes.go send a proxied request to HTTP/1.1 before
	// they ever reach this transport. Leaving the inherited proxy here would
	// only offer a route that cannot work.
	h2c.Proxy = nil

	address := dialAddress(u)
	var dialer net.Dialer
	e.wireCleartext(h2c, h1, func(ctx context.Context) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", address)
	})
}

// dialAddress returns the `host:port` the probe dials for a cleartext endpoint.
//
// It reads u.Hostname(), not u.Host, because u.Host KEEPS the brackets around
// an IPv6 literal and net.JoinHostPort adds its own. `http://[::1]` would
// otherwise dial `[[::1]]:80`, which no resolver accepts, so the probe failed
// for every bracketed IPv6 endpoint with no explicit port, the verdict stayed
// undecided, and an endpoint that speaks HTTP/1.1 only never got the fallback.
func dialAddress(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), "80")
}

// lanes is what one build path decides: the round tripper behind each of the
// three lanes, before the WebSocket guard.
//
// A struct with named fields rather than three parameters, because `unary` and
// `http2Only` share a type. Positionally they would be silently transposable,
// and swapping them gives a worker's bidirectional stream the lane that falls
// back to HTTP/1.1 — a mistake no compiler and no test in this package would
// report.
type lanes struct {
	unary     http.RoundTripper
	http2Only http.RoundTripper
	// webSocket is the concrete transport, never a wrapper: this lane is the
	// one that MUST carry an upgrade, so guardWebSocket never wraps it.
	webSocket *http.Transport
}

// setLanes installs one build path's decision, and applies the WebSocket guard
// to the two lanes that must refuse an upgrade.
//
// Every build path routes through here, so the guard is applied in ONE place
// and a path that forgets it cannot exist. The compiler also demands all three
// fields of a `lanes` value, so a path cannot leave a lane nil either.
func (e *Endpoint) setLanes(l lanes) {
	e.unary = guardWebSocket{next: l.unary}
	e.http2Only = guardWebSocket{next: l.http2Only}
	e.webSocket = l.webSocket
}

// wireCleartext installs the probing lanes shared by a local and a plaintext
// endpoint. probeDial opens one connection to the endpoint for the h2c proof.
func (e *Endpoint) wireCleartext(h2c, h1 *http.Transport, probeDial func(ctx context.Context) (net.Conn, error)) {
	e.prober = newProber(e.url, probeDial)
	e.setLanes(lanes{
		unary:     preferH2C{prober: e.prober, h2c: h2c, h1: h1},
		http2Only: requireH2C{prober: e.prober, h2c: h2c, endpointURL: e.url},
		webSocket: h1,
	})
}

// buildTLS wires the three ALPN transports. No probe runs: ALPN settles the protocol
// inside the handshake, and it settles it per connection rather than once.
func (e *Endpoint) buildTLS() {
	e.setLanes(lanes{
		unary:     e.newTransport(protocolsHTTP1AndHTTP2),
		http2Only: e.newTransport(protocolsHTTP2),
		webSocket: e.newTransport(protocolsHTTP1),
	})
}

// URL returns the endpoint address the caller supplied. Use it in a log line
// or an error message; it states what an operator configured.
func (e *Endpoint) URL() string { return e.url }

// BaseURL returns what ConnectRPC clients, REST paths and WebSocket dials must
// target: the endpoint URL verbatim for http(s), and the placeholder
// `http://localhost` for a `unix:`/`npipe:` endpoint, whose dial is wired into
// the transport.
func (e *Endpoint) BaseURL() string { return e.baseURL }

// UnaryClient returns the client for request/response calls: ConnectRPC unary
// RPCs and plain REST. timeout limits the whole exchange; pass
// DefaultUnaryTimeout unless the caller has a reason for a different bound.
func (e *Endpoint) UnaryClient(timeout time.Duration) *http.Client {
	return e.client(e.unary, timeout)
}

// StreamClient returns the client for a long-lived Connect stream. It carries
// no overall timeout, so the caller must limit the stream with its context.
func (e *Endpoint) StreamClient() *http.Client {
	return e.client(e.unary, 0)
}

// HTTP2OnlyClient returns the client for a lane that HTTP/1.1 cannot carry —
// today, the worker's bidirectional Connect stream. Against a cleartext
// endpoint with no h2c it fails with ErrH2CUnsupported, which identifies the endpoint,
// rather than degrading to a protocol on which the stream cannot work.
func (e *Endpoint) HTTP2OnlyClient() *http.Client {
	return e.client(e.http2Only, 0)
}

// WebSocketClient returns the HTTP/1.1 client for a WebSocket upgrade. It
// carries no overall timeout, because that timeout would end the connection
// rather than the handshake.
func (e *Endpoint) WebSocketClient() *http.Client {
	return e.client(e.webSocket, 0)
}

func (e *Endpoint) client(rt http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{Transport: rt, Timeout: timeout}
}

// CloseIdleConnections closes the idle connections of every transport this
// endpoint built. A caller that holds one lane's *http.Client can call
// CloseIdleConnections on it -- every lane wrapper forwards the method -- but
// that reaches only the transports of THAT lane, so a caller that shuts the
// whole endpoint down calls this instead.
func (e *Endpoint) CloseIdleConnections() {
	for _, transport := range e.transports {
		transport.CloseIdleConnections()
	}
}

// newTransport clones http.DefaultTransport — for its dial timeout, its
// keep-alive, its TLS handshake timeout and its idle-connection limits — and
// replaces the protocol set. Protocols overrides the conservative HTTP/2
// detection that DefaultTransport's ForceAttemptHTTP2 exists for, so the
// transport gets exactly the protocols this asks for and no others.
func (e *Endpoint) newTransport(setProtocols func(*http.Protocols)) *http.Transport {
	// http.DefaultTransport is a package-level variable, so an instrumentation
	// or tracing package can replace it with a type that is not
	// *http.Transport. A bare type assertion would then panic inside New for
	// every endpoint in the process; an empty transport carries the same
	// protocols and loses only DefaultTransport's tuning.
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	protocols := &http.Protocols{}
	setProtocols(protocols)
	transport.Protocols = protocols
	// The transport and the per-request proxy test in lanes.go must consult one
	// function, or a transport could route around a proxy the lane accounted
	// for. The clone brings its own http.ProxyFromEnvironment; replacing it
	// keeps both on this package's one seam.
	transport.Proxy = proxyForRequest
	if e.rootCAs != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: e.rootCAs, MinVersion: tls.VersionTLS12}
	}
	e.transports = append(e.transports, transport)
	return transport
}

func protocolsH2C(p *http.Protocols)           { p.SetUnencryptedHTTP2(true) }
func protocolsHTTP1(p *http.Protocols)         { p.SetHTTP1(true) }
func protocolsHTTP2(p *http.Protocols)         { p.SetHTTP2(true) }
func protocolsHTTP1AndHTTP2(p *http.Protocols) { p.SetHTTP1(true); p.SetHTTP2(true) }
