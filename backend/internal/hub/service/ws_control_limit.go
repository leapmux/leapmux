package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/generated/contracts"
)

const (
	// wsControlFrameRate is the sustained inbound control-frame rate one
	// connection may cost the Hub, in frames per second.
	//
	// A legitimate client sends almost none: our own keepalive probes once every
	// wsKeepaliveInterval and the browser answers those, so two per second is
	// four orders of magnitude above anything real while still being a rate
	// rather than a hair trigger.
	wsControlFrameRate = 2.0
	// wsControlFrameBurst absorbs a genuine clump -- a client that reconnects,
	// a proxy that replays a couple of frames -- without the sustained rate
	// having to be set high enough to cover it.
	wsControlFrameBurst = 20.0
	// wsControlFrameAbuseLimit is how many control frames a connection may have
	// refused WITHOUT ONCE letting its bucket refill before the Hub stops
	// treating it as a client with a bug and closes it. Refusing costs nothing,
	// so this only has to be far enough above the burst that no clumsy client
	// reaches it inside one episode.
	wsControlFrameAbuseLimit = 1000
)

// wsControlLimiter bounds how much work one peer's control frames can make the
// Hub do.
//
// The library answers every inbound PING with a pong, unconditionally and with
// no rate limit of its own. That is an amplification lever: a ping is cheap to
// send, a pong costs a write that takes the connection's write mutex, so a peer
// that floods pings makes the Hub write a pong per ping and contends with the
// frame writer that is trying to serve actual traffic. Nothing else on these
// sockets bounds it -- the read limit caps a frame's SIZE, not the rate, and a
// control frame carries at most 125 bytes anyway.
//
// Unsolicited PONGS are counted on the same budget. RFC 6455 explicitly permits
// them as a unidirectional heartbeat, so one is not misbehaviour and must not
// drop the connection; a flood of them is still per-frame read work bought for
// nothing, and rate is the honest thing to bound.
//
// It does NOT try to match pongs against outstanding pings. The library already
// does that correctly -- it keys each probe on a monotonic id and ignores a pong
// whose payload matches no outstanding one -- so a peer cannot satisfy a
// liveness probe by replaying or pre-sending pongs. Re-implementing the pairing
// here would be a second source of truth for a question that is already answered
// where the state lives.
type wsControlLimiter struct {
	now      func() time.Time
	endpoint string
	userID   string
	// conn is stored after Accept returns, which is necessarily before any read
	// can invoke a hook. An atomic rather than a captured variable so the store
	// and the hook goroutine's load are ordered -- a plain capture would be a
	// data race even though the timing makes it unreachable.
	conn atomic.Pointer[websocket.Conn]

	// abuseOnce guards the teardown, ONE per connection however many hooks
	// notice. It lives here rather than inside each hook because the two hooks
	// spend one budget: a peer mixing pings and pongs drives both past the abuse
	// limit, so a Once per hook let each of them tear the connection down --
	// two goroutines, two warnings, two Closes -- against the single teardown
	// this type promises.
	abuseOnce sync.Once

	mu sync.Mutex
	// allowance is the token bucket, in frames.
	allowance float64
	last      time.Time
	// refused counts control frames dropped in the CURRENT pressure episode --
	// the run that began when the bucket first ran dry. Cleared the moment the
	// bucket refills to the full burst (see allow), because abuse is a RATE and
	// a lifetime total cannot express one: any non-zero refusal rate reaches
	// wsControlFrameAbuseLimit given a long enough connection, and the close is
	// StatusPolicyViolation, which latches the browser transport permanently
	// (frontend/src/lib/wsCloseCodes.ts).
	refused int
}

func newWSControlLimiter(endpoint, userID string, now func() time.Time) *wsControlLimiter {
	if now == nil {
		now = time.Now
	}
	return &wsControlLimiter{
		now:       now,
		endpoint:  endpoint,
		userID:    userID,
		allowance: wsControlFrameBurst,
		last:      now(),
	}
}

// acceptOptions returns the upgrade options that install this limiter. One
// constructor so a new long-lived endpoint cannot pick up the subprotocol and
// silently miss the control-frame bound.
func (l *wsControlLimiter) acceptOptions(subprotocol string) *websocket.AcceptOptions {
	// Off the read path deliberately: closing writes a control frame, and doing
	// that from inside the read path's own callback would have the connection
	// waiting on itself. The spawn is HERE rather than inside the hooks so the
	// hooks call onAbuse synchronously -- which is what lets a test assert "at
	// most once" without racing a goroutine.
	// refusedCount() is sampled HERE, synchronously: arguments to `go` are
	// evaluated at the statement, so the number logged is the one that produced
	// the verdict rather than whatever the reset below has since made of it.
	abuse := func() { go l.closeAbusive(l.refusedCount()) }
	return &websocket.AcceptOptions{
		Subprotocols:   []string{subprotocol},
		OnPingReceived: l.onPingReceived(abuse),
		OnPongReceived: l.onPongReceived(abuse),
	}
}

// attach hands the limiter the connection it may close. Called immediately
// after Accept; the hooks cannot fire before then, because nothing has read.
func (l *wsControlLimiter) attach(c *websocket.Conn) { l.conn.Store(c) }

// closeAbusive ends a connection that would not stop. Runs on its own goroutine
// (see acceptOptions), so it is free to write and to log.
func (l *wsControlLimiter) closeAbusive(refused int) {
	slog.Warn("closing a websocket that flooded control frames",
		"endpoint", l.endpoint, "user_id", l.userID, "refused", refused)
	if c := l.conn.Load(); c != nil {
		_ = c.Close(websocket.StatusPolicyViolation, contracts.CloseReasonControlFlood)
	}
}

// allow reports whether this control frame may be answered, and whether the
// connection has refused so many that it should be closed.
//
// Called synchronously from the read path, so it does arithmetic under a mutex
// and nothing else -- no logging, no I/O, no channel send.
func (l *wsControlLimiter) allow() (ok, abusive bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.last = now
		l.allowance += elapsed.Seconds() * wsControlFrameRate
		if l.allowance >= wsControlFrameBurst {
			l.allowance = wsControlFrameBurst
			// A full bucket means the peer stopped long enough to earn its
			// entire burst back, so the episode those refusals belonged to is
			// over. Without this they integrate over the whole connection and
			// the limit becomes a lifetime total tested against a rate: fifty
			// frames once an hour averages 0.014 frames/sec and still reached
			// wsControlFrameAbuseLimit after about 34 hours. A peer under
			// continuous pressure never refills, so a real flood is closed on
			// exactly the frame it was before.
			l.refused = 0
		}
	}
	if l.allowance < 1 {
		l.refused++
		return false, l.refused >= wsControlFrameAbuseLimit
	}
	l.allowance--
	return true, false
}

// onPingReceived is the AcceptOptions hook. Returning false suppresses the pong
// the library would otherwise write, which is the whole defence: the refusal
// itself has to be cheaper than the reply, or rate-limiting would be its own
// amplification.
//
// onAbuse fires at most once PER LIMITER -- not once per hook -- when a
// connection has refused enough frames to stop looking like a bug. See
// abuseOnce for why the difference is not academic.
func (l *wsControlLimiter) onPingReceived(onAbuse func()) func(context.Context, []byte) bool {
	return func(context.Context, []byte) bool {
		ok, abusive := l.allow()
		if abusive {
			l.abuseOnce.Do(onAbuse)
		}
		return ok
	}
}

// onPongReceived charges an inbound pong against the same budget. It answers
// nothing, so there is no reply to suppress -- the only lever is closing a
// connection that will not stop, and it shares that lever with onPingReceived.
func (l *wsControlLimiter) onPongReceived(onAbuse func()) func(context.Context, []byte) {
	return func(context.Context, []byte) {
		if _, abusive := l.allow(); abusive {
			l.abuseOnce.Do(onAbuse)
		}
	}
}

// refusedCount reports how many control frames have been dropped in the current
// pressure episode. For logging and tests.
func (l *wsControlLimiter) refusedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused
}
