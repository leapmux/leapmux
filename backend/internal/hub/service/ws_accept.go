package service

import (
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
)

// Endpoint names, and the only producer of these values.
//
// Each is a log field and a control-limiter label, spelled at the acceptWebSocket call
// and again where the keepalive is armed. Two literals per endpoint is how the
// accept log and the keepalive log come to disagree about which socket they
// describe -- the same drift metrics.go identifies as the reason its pool
// labels are constants rather than literals at each call site.
const (
	endpointChannel    = "channel"
	endpointUserEvents = "userevents"
	endpointWorker     = "worker"
)

// wsSubject supplies the authenticated identity in upgrade and probe logs.
type wsSubject struct{ key, id string }

func userSubject(id string) wsSubject   { return wsSubject{key: "user_id", id: id} }
func workerSubject(id string) wsSubject { return wsSubject{key: "worker_id", id: id} }

func (s wsSubject) logAttrs(endpoint string) []any {
	attrs := []any{"endpoint", endpoint}
	if s.id != "" {
		attrs = append(attrs, s.key, s.id)
	}
	return attrs
}

// acceptWebSocket upgrades an authenticated request and installs everything a
// long-lived socket must have before it carries a byte: the control-frame
// limiter's hooks, the limiter's handle on the connection it may close, and the
// per-message read limit. Reports false when the upgrade failed, in which case
// it already logged and the caller must return.
//
// One helper because the browser endpoints held a copy-pasted sequence, verbatim
// comment included, and one of its steps is silently skippable: a
// missed attach leaves the limiter's conn nil, so closeAbusive logs a flood and
// closes nothing -- the defence becomes a no-op with no signal that it did.
// acceptOptions' own doc already states this principle for the hooks ("One
// constructor so a new long-lived endpoint cannot pick up the subprotocol and
// silently miss the control-frame bound"); attach and the read limit are the
// rest of the same set, so they belong behind the same one call.
//
// The read limit is a parameter rather than a constant here because the
// endpoints carry different payloads; specifying it at the call site is what
// keeps each socket and the code reading from it from drifting apart.
func acceptWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	endpoint, subprotocol string,
	readLimit int64,
	subject wsSubject,
) (*websocket.Conn, bool) {
	// The library answers every inbound ping with a pong, unconditionally: a peer
	// that floods them makes the Hub write one per ping, contending with the
	// writer serving real traffic. The limiter caps that, and unsolicited
	// pongs on the same budget.
	controlLimit := newWSControlLimiter(endpoint, subject, nil)
	conn, err := websocket.Accept(w, r, controlLimit.acceptOptions(subprotocol))
	if err != nil {
		slog.Error("websocket upgrade failed", append(subject.logAttrs(endpoint), "error", err)...)
		return nil, false
	}
	controlLimit.attach(conn)
	conn.SetReadLimit(readLimit)
	return conn, true
}
