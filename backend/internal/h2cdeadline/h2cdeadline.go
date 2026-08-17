// Package h2cdeadline works around golang/go#80876: when an http.Server
// configured with ReadHeaderTimeout (and ReadTimeout == 0) accepts a
// connection that turns out to speak prior-knowledge HTTP/2 (h2c), the read
// deadline net/http arms before probing for the HTTP/2 preface is never
// disarmed after the handoff to the HTTP/2 server. The deadline then fires
// ReadHeaderTimeout after ACCEPT, and the HTTP/2 framer's next read fails
// with "i/o timeout", closing the whole connection — no matter how active
// the streams on it are.
//
// Tracking issue for removing this once Go ships the fix: leapmux/leapmux#393.
//
// The Hub hosts long-lived h2c bidi streams (the worker's Connect RPC, over
// the local-IPC listener in solo/desktop and over plain TCP for remote
// workers) on the same http.Server as ordinary HTTP/1.1 traffic, so neither
// upstream workaround applies: ReadHeaderTimeout=0 would drop slowloris
// protection for the public TCP listener, and ReadTimeout>0 makes the HTTP/2
// server arm a per-stream watchdog that kills ACTIVE streams once it fires.
// Wrap instead disarms the stale deadline the moment a connection identifies
// itself as h2c, leaving HTTP/1.1 connections — where the deadline is the
// header timeout doing its intended job — untouched.
package h2cdeadline

import (
	"bytes"
	"net"
	"time"
)

// clientPreface is the HTTP/2 prior-knowledge connection preface every h2c
// client must send as the first bytes on the wire (RFC 9113, section 3.4).
// No HTTP/1.x request can start with it: its request line would have to
// begin "PRI * HTTP/2.0", which is not a method any HTTP/1.x speaker sends.
var clientPreface = []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")

// Wrap returns a listener whose accepted connections disarm the
// ReadHeaderTimeout read deadline once they turn out to speak h2c. The
// underlying listener is untouched, so listener-composition interfaces
// (locallisten's CloseAccepted) keep working on the original.
func Wrap(ln net.Listener) net.Listener {
	return listener{Listener: ln}
}

type listener struct {
	net.Listener
}

func (l listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &conn{Conn: c}, nil
}

// conn inspects the first bytes read through it for the h2c client preface.
//
// It only ever OBSERVES reads: bytes are returned to the caller verbatim, so
// net/http's own preface probe (conn.maybeServeUnencryptedHTTP2 peeks the
// same bytes) is unaffected. The probe state needs no lock because a fresh
// connection has exactly one reader (net/http's connReader until the protocol
// is settled, then the HTTP/2 framer or the hijacker), and the probe stops
// touching state the moment the connection resolves.
type conn struct {
	net.Conn
	// matched is how many leading bytes of the preface the reads so far
	// spelled out; resolved is set once the connection is known not to be
	// (or to have finished being) h2c, after which Read is a passthrough.
	matched  int
	resolved bool
}

func (c *conn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && !c.resolved {
		take := min(n, len(clientPreface)-c.matched)
		if !bytes.Equal(p[:take], clientPreface[c.matched:c.matched+take]) {
			// An HTTP/1.x (or garbage) request: the header-read deadline is
			// the slowloris guard it was armed to be. Leave it alone.
			c.resolved = true
		} else {
			c.matched += take
			if c.matched == len(clientPreface) {
				c.resolved = true
				// The full preface is on the wire, so this connection is
				// h2c and is about to be handed to the HTTP/2 server with
				// the deadline still armed. Disarm it now — during the very
				// read that completes the preface, i.e. before any frame
				// read can park against it. An error here means the
				// connection does not support deadlines at all, in which
				// case net/http's own arming failed the same way and there
				// is nothing to disarm.
				_ = c.SetReadDeadline(time.Time{})
			}
		}
	}
	return n, err
}
