package hubtransport

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// isWebSocketUpgrade reports whether req is a WebSocket handshake.
//
// It repeats net/http's own rule (Request.requiresHTTP1, which net/http uses
// to keep an upgrade off an HTTP/2 connection) so that the two cannot drift.
// coder/websocket sets both headers before it calls the client, so the test is
// exact rather than a guess.
//
// The repetition is necessary, not redundant. net/http applies its rule to a
// TLS connection and NOT to a cleartext HTTP/2 one: the unencryptedHTTP2
// branch of Transport.dialConn ignores the connectMethod's onlyH1 field. So an
// h2c transport carries an upgrade onto an HTTP/2 stream, where it cannot
// work.
func isWebSocketUpgrade(req *http.Request) bool {
	return httpguts.HeaderValuesContainsToken(req.Header["Connection"], "Upgrade") &&
		strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
}

// closeIdleConnections asks rt to drop its idle connections, when it can.
//
// net/http reaches a transport's idle connections through an OPTIONAL
// interface: http.Client.CloseIdleConnections type-asserts its Transport for
// `interface{ CloseIdleConnections() }` and does nothing when the assertion
// fails. So every wrapper in this file forwards the method, or the call
// becomes a silent no-op for the caller that holds only the *http.Client --
// which is how a desktop shutdown stopped releasing the named-pipe handles it
// must free before it starts the Hub again.
func closeIdleConnections(rt http.RoundTripper) {
	if c, ok := rt.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// refuse returns err after closing req.Body.
//
// http.RoundTripper's contract requires an implementation to close the body
// ALWAYS, including on the error path, because a caller may be blocked writing
// to it. A lane that refuses a request never reaches the transport that would
// have closed it, so each refusal closes it here. The one caller that streams a
// live body today is connect-go, which closes its own pipe on a RoundTrip
// error — but that is its defensive code, not a promise this package may rely
// on, and the next caller that hands a lane a *os.File body would leak it.
func refuse(req *http.Request, err error) error {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return err
}

// guardWebSocket refuses a WebSocket upgrade on a lane that cannot carry one.
//
// Every lane except WebSocketClient wraps its transport in this, so the
// failure is a sentence that identifies the correct client instead of
// `http2: invalid Upgrade request header` raised three layers down, or --
// worse -- an upgrade that travels over HTTP/1.1 by accident and works until
// the day the protocol choice changes.
type guardWebSocket struct{ next http.RoundTripper }

func (g guardWebSocket) RoundTrip(req *http.Request) (*http.Response, error) {
	if isWebSocketUpgrade(req) {
		return nil, refuse(req, fmt.Errorf("%w (requested %s)", ErrWebSocketLane, req.URL.Redacted()))
	}
	return g.next.RoundTrip(req)
}

func (g guardWebSocket) CloseIdleConnections() { closeIdleConnections(g.next) }

// proxiedAway reports the proxy that stands between us and req's host, or nil
// when this request is dialed directly.
//
// The question belongs to ONE REQUEST and not to the endpoint. NO_PROXY
// answers per host, and net/http follows a redirect through the same
// RoundTripper with the new host, so a request that leaves the endpoint's own
// origin can meet a different answer than the endpoint does.
func proxiedAway(req *http.Request) *url.URL {
	proxyURL, err := proxyForRequest(req)
	if err != nil {
		return nil
	}
	return proxyURL
}

// preferH2C is the cleartext unary and stream lane: h2c when the endpoint
// supports it, HTTP/1.1 when a proxy rules h2c out or the endpoint proves that
// it does not speak it.
//
// The verdict is settled BEFORE the request touches a transport, and the
// chosen transport then runs the request exactly once. There is no "try the
// other transport on failure" path anywhere, so this layer can never replay a
// request that the endpoint may already have processed -- which matters
// because AddTab, CreateWorkspace and SubmitOps are not idempotent.
type preferH2C struct {
	prober *prober
	h2c    http.RoundTripper
	h1     http.RoundTripper
}

func (p preferH2C) RoundTrip(req *http.Request) (*http.Response, error) {
	// Prior-knowledge h2c cannot pass through a plain HTTP proxy, which reads
	// an HTTP/1.1 request line, so a proxied request takes HTTP/1.1 and the
	// endpoint's own h2c support is neither used nor measured. The h2c
	// transport dials directly (Proxy is nil on it), so without this test it
	// would route around the very proxy the operator configured.
	if proxiedAway(req) != nil {
		return p.h1.RoundTrip(req)
	}
	if p.prober.supportsH2C(req.Context()) == verdictUnsupported {
		return p.h1.RoundTrip(req)
	}
	// An undecided verdict takes h2c: a probe that could not connect says
	// nothing about the endpoint's protocols, and the real request then
	// reports the real failure instead of a downgrade that hides it.
	return p.h2c.RoundTrip(req)
}

func (p preferH2C) CloseIdleConnections() {
	closeIdleConnections(p.h2c)
	closeIdleConnections(p.h1)
}

// requireH2C is the cleartext lane for a bidirectional gRPC stream, which
// HTTP/1.1 cannot express. It fails closed with a message an operator can act
// on, rather than degrading to a protocol on which the stream cannot work.
type requireH2C struct {
	prober      *prober
	h2c         http.RoundTripper
	endpointURL string
}

func (r requireH2C) RoundTrip(req *http.Request) (*http.Response, error) {
	if proxyURL := proxiedAway(req); proxyURL != nil {
		return nil, refuse(req, fmt.Errorf(
			"%w: an HTTP proxy (%s) is configured for %s, and prior-knowledge h2c cannot pass through one",
			ErrH2CUnsupported, proxyURL.Redacted(), r.endpointURL))
	}
	if r.prober.supportsH2C(req.Context()) == verdictUnsupported {
		return nil, refuse(req, fmt.Errorf(
			"%w: %s answers HTTP/1.1 only, and this stream needs HTTP/2; put an h2c-capable reverse proxy in front of the Hub, or point this URL at the Hub itself",
			ErrH2CUnsupported, r.endpointURL))
	}
	return r.h2c.RoundTrip(req)
}

func (r requireH2C) CloseIdleConnections() { closeIdleConnections(r.h2c) }
