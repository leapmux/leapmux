package hubtransport

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

// verdict is what the prober knows about one cleartext endpoint.
type verdict int

const (
	// verdictUndecided means the question is still open: no probe ran yet, or
	// the probe could not reach the endpoint. A caller that can use HTTP/1.1
	// still tries h2c on this verdict, because "unreachable" is not "HTTP/1.1
	// only", and the real request reports the real failure.
	verdictUndecided verdict = iota
	// verdictSupported means the endpoint answered an HTTP/2 PING.
	verdictSupported
	// verdictUnsupported means the endpoint refused the HTTP/2 connection
	// preface. A Go server that serves HTTP/1.1 alone answers 400; nginx
	// without h2c does the same.
	verdictUnsupported
)

// undecidedCooldown is how long an UNDECIDED probe suppresses the next one.
//
// An undecided verdict means the probe could not reach the endpoint, so it
// settles nothing and is not cached. Without a cooldown the NEXT request
// started another probe and blocked on it, and that wait buys nothing: both
// cleartext lanes take h2c on undecided, so the caller's behaviour is the same
// whether it waited or not. Against an endpoint that accepts a connection and
// then says nothing, that cost every request a whole probeTimeout of dead time
// -- inside the caller's own http.Client.Timeout, so `leapmux control version`
// (a 5s budget against a 5s probe) reached the hub with an already-expired
// context, and the worker's reconnect loop paid it before every backoff.
//
// The cooldown is longer than probeTimeout, so a request that arrives while a
// stalled probe is still running joins that probe rather than starting a new
// one, and shorter than a person's patience with a hub that came back: an
// endpoint that recovers is measured again within the minute.
const undecidedCooldown = 30 * time.Second

// probeTimeout limits one probe. It has to outlast a dial and one round trip
// on a slow link, and it has to end well inside a caller's patience when the
// endpoint accepts a connection and then says nothing.
const probeTimeout = 5 * time.Second

// prober answers "does this cleartext endpoint speak h2c?" for one Endpoint,
// with at most one probe in flight and at most one answer kept.
//
// # Why probe instead of trying and retrying
//
// Go does not negotiate cleartext HTTP/2: net/http uses h2c only when
// Transport.Protocols holds UnencryptedHTTP2 and does NOT hold HTTP1, so a
// transport speaks one of the two and never discovers the other. The
// alternative — send the request on h2c and repeat it on HTTP/1.1 when it
// fails — would have to tell "this endpoint has no h2c" apart from "the hub
// restarted mid-request" using error strings from three layers, and it would
// repeat a request that the endpoint may already have processed. A probe on
// its own connection answers the question with no application request at all.
//
// # What the probe costs the endpoint
//
// Against a LeapMux hub, nothing that a handler can see: net/http consumes the
// HTTP/2 client preface in maybeServeUnencryptedHTTP2 before it routes, so the
// probe reaches no route, no access log and no rate limiter. Against an
// endpoint with no h2c the preface DOES arrive as a malformed request, so a
// proxy log gets one 400 line per process. That deployment is already
// misconfigured for the worker's bidirectional stream, which needs HTTP/2 on
// the same URL.
//
// # Why a DECIDED answer is kept for the life of the process
//
// An endpoint that gains or loses h2c did so because somebody changed a
// reverse proxy. A time-to-live would pick that up a few minutes sooner, and
// would cost an expiry rule for a fact that changes at a restart anyway. The
// restart that follows such a change picks it up instead.
//
// An UNDECIDED answer is different, and does carry a clock: it settles
// nothing, so it must not be cached, but repeating it on the next request
// costs a whole probeTimeout for no information. See undecidedCooldown.
type prober struct {
	endpointURL string
	dial        func(ctx context.Context) (net.Conn, error)

	// run replaces the real probe in this package's tests, so a test can
	// count probes, hold one open, or dictate a verdict without a server.
	run func(ctx context.Context) verdict
	// onWait, when set, is called by a caller that is about to block on a probe
	// another caller started. Only this package's tests set it: it is the seam
	// that makes "the waiter reached the wait" observable, so those tests
	// synchronise on an event instead of on a sleep long enough to look safe.
	onWait func()
	// now reads the clock the cooldown below measures against. A test supplies
	// its own and advances it by hand, so the cooldown is exercised with no
	// sleep and no window to race: the seam that ends the state is a value the
	// test sets, not a real timer it has to outlast.
	now func() time.Time

	mu      sync.Mutex
	decided bool
	result  verdict
	// undecidedUntil is the instant an UNDECIDED verdict stops suppressing the
	// next probe. Zero means "probe now". See undecidedCooldown.
	undecidedUntil time.Time
	inflight       chan struct{}

	warnOnce sync.Once
	// calls counts the probes that ran. Only the tests read it.
	calls atomic.Int64
}

func newProber(endpointURL string, dial func(ctx context.Context) (net.Conn, error)) *prober {
	return &prober{endpointURL: endpointURL, dial: dial, now: time.Now}
}

// supportsH2C returns the endpoint's verdict, running at most one probe at a
// time.
//
// The probe runs on its OWN goroutine under its own deadline, and every
// caller -- including the one whose request started it -- waits on the probe
// or on its own request context, whichever ends first. That split is what
// makes both halves true at once: a caller that gives up (a cancelled request,
// a shutdown) returns immediately and never waits out the probe deadline, and
// its cancellation does not take the answer away from the callers still
// waiting.
func (p *prober) supportsH2C(ctx context.Context) verdict {
	p.mu.Lock()
	if p.decided {
		result := p.result
		p.mu.Unlock()
		return result
	}
	wait := p.inflight
	if wait == nil && p.now().Before(p.undecidedUntil) {
		// A recent probe reached the endpoint and learned nothing. Answer
		// undecided at once rather than starting another and blocking on it:
		// the caller takes h2c either way, so the wait cannot change what it
		// does. See undecidedCooldown.
		p.mu.Unlock()
		return verdictUndecided
	}
	started := wait == nil
	if started {
		wait = make(chan struct{})
		p.inflight = wait
		go p.runProbe(wait)
	}
	p.mu.Unlock()

	if !started && p.onWait != nil {
		p.onWait()
	}
	select {
	case <-wait:
	case <-ctx.Done():
		return verdictUndecided
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.decided {
		return p.result
	}
	// The probe reached no verdict, because it could not reach the endpoint.
	// Do NOT start another one here: every waiter would then probe in turn,
	// and a hub that is down would cost one probe per request. The next
	// request after the cooldown starts the next probe.
	return verdictUndecided
}

// runProbe performs one probe and publishes its verdict. It owns the in-flight
// channel and closes it, whether the probe reached a verdict or not.
func (p *prober) runProbe(done chan struct{}) {
	result := p.probe()

	p.mu.Lock()
	p.inflight = nil
	if result != verdictUndecided {
		p.decided = true
		p.result = result
	} else {
		// Not cached -- an unreachable endpoint settles nothing -- but the next
		// request does not repeat it either. See undecidedCooldown.
		p.undecidedUntil = p.now().Add(undecidedCooldown)
	}
	p.mu.Unlock()
	close(done)

	if result == verdictUnsupported {
		p.warnOnce.Do(func() {
			slog.Warn("endpoint does not support cleartext HTTP/2 (h2c); unary calls fall back to HTTP/1.1, but a worker's Connect stream needs HTTP/2 on this URL",
				"endpoint", p.endpointURL)
		})
	}
}

// probe runs one probe under its own deadline. It takes no caller context:
// the goroutine that runs it belongs to no single request.
func (p *prober) probe() verdict {
	p.calls.Add(1)
	probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if p.run != nil {
		return p.run(probeCtx)
	}
	return p.ping(probeCtx)
}

// ping opens one connection, completes the HTTP/2 handshake on it and waits
// for a PING acknowledgement. It sends HTTP/2 frames only: no method, no path,
// no body and no credential.
func (p *prober) ping(ctx context.Context) verdict {
	conn, err := p.dial(ctx)
	if err != nil {
		return verdictUndecided
	}
	defer func() { _ = conn.Close() }()

	// NewClientConn writes the connection preface and our SETTINGS, and starts
	// the read loop. It does NOT wait for the server's SETTINGS, so the answer
	// comes from the PING acknowledgement below rather than from this call.
	var transport http2.Transport
	clientConn, err := transport.NewClientConn(conn)
	if err != nil {
		return verdictUnsupported
	}
	defer func() { _ = clientConn.Close() }()

	if err := clientConn.Ping(ctx); err != nil {
		if ctx.Err() != nil {
			// The endpoint accepted the connection and then said nothing. That
			// is a stalled endpoint, not a protocol answer.
			return verdictUndecided
		}
		return verdictUnsupported
	}
	return verdictSupported
}
