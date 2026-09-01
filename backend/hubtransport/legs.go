package hubtransport

import (
	"fmt"
	"net/http"
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

// guardWebSocket refuses a WebSocket upgrade on a lane that cannot carry one.
//
// Every lane except WebSocketClient wraps its leg in this, so the failure is a
// sentence that names the correct client instead of `http2: invalid Upgrade
// request header` raised three layers down, or — worse — an upgrade that
// travels over HTTP/1.1 by accident and works until the day the protocol
// choice changes.
type guardWebSocket struct{ next http.RoundTripper }

func (g guardWebSocket) RoundTrip(req *http.Request) (*http.Response, error) {
	if isWebSocketUpgrade(req) {
		return nil, fmt.Errorf("%w (requested %s)", ErrWebSocketLane, req.URL.Redacted())
	}
	return g.next.RoundTrip(req)
}

// preferH2C is the cleartext unary and stream lane: h2c when the endpoint
// supports it, HTTP/1.1 when it proves that it does not.
//
// The verdict is settled BEFORE the request touches a leg, and the chosen leg
// then runs the request exactly once. There is no "try the other leg on
// failure" path anywhere, so this layer can never replay a request that the
// endpoint may already have processed — which matters because AddTab,
// CreateWorkspace and SubmitOps are not idempotent.
type preferH2C struct {
	prober *prober
	h2c    http.RoundTripper
	h1     http.RoundTripper
}

func (p preferH2C) RoundTrip(req *http.Request) (*http.Response, error) {
	if p.prober.supportsH2C(req.Context()) == verdictUnsupported {
		return p.h1.RoundTrip(req)
	}
	// An undecided verdict takes h2c: a probe that could not connect says
	// nothing about the endpoint's protocols, and the real request then
	// reports the real failure instead of a downgrade that hides it.
	return p.h2c.RoundTrip(req)
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
	if r.prober.supportsH2C(req.Context()) == verdictUnsupported {
		return nil, fmt.Errorf(
			"%w: %s answers HTTP/1.1 only, and this stream needs HTTP/2; put an h2c-capable reverse proxy in front of the Hub, or point this URL at the Hub itself",
			ErrH2CUnsupported, r.endpointURL)
	}
	return r.h2c.RoundTrip(req)
}

// erringRoundTripper fails every request with one prepared error. It stands in
// for a lane that this endpoint's configuration rules out, so the refusal
// arrives at the call site rather than at construction, where no caller of the
// other lanes could act on it.
type erringRoundTripper struct{ err error }

func (e erringRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }
