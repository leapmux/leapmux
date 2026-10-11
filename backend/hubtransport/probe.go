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
	// verdictUndecided means that no probe ran or the probe reached no protocol verdict.
	// A caller still tries cleartext HTTP/2 (h2c), even when it can use HTTP/1.1.
	// An unreachable endpoint proves no protocol limit. The real request reports its failure.
	verdictUndecided verdict = iota
	// verdictSupported means the endpoint answered an HTTP/2 PING.
	verdictSupported
	// verdictUnsupported means that connection setup failed or a PING failed before context cancellation.
	// An HTTP/1.1 response to the preface does not satisfy the HTTP/2 handshake.
	verdictUnsupported
)

// undecidedCooldown specifies how long an undecided probe suppresses another probe.
//
// An undecided verdict proves no protocol support, so the prober does not cache it.
// Without a cooldown, each request starts another probe and waits for it.
// Both cleartext transports use h2c on an undecided verdict, so the extra wait changes no behavior.
// A silent endpoint consumes the full probeTimeout inside each caller's http.Client.Timeout.
// A five-second probe then consumes the full five-second limit of `leapmux control version`.
// The Worker reconnect loop incurs that wait before every backoff also.
//
// Requests that arrive during a probe share its completion channel.
// An undecided verdict then suppresses another probe for longer than probeTimeout.
// A recovered endpoint can receive another probe within a minute.
const undecidedCooldown = 30 * time.Second

// probeTimeout permits a dial and one round trip on a slow link.
// It also limits the wait when an endpoint accepts a connection and sends nothing.
const probeTimeout = 5 * time.Second

// prober checks whether one Endpoint supports h2c.
// It runs at most one probe at a time and keeps at most one decided verdict.
//
// # Protocol selection
//
// Go does not negotiate cleartext HTTP/2.
// net/http uses h2c only when Transport.Protocols enables UnencryptedHTTP2 and disables HTTP1.
// Each transport therefore uses one protocol without detecting the other.
// Repeating a failed h2c request over HTTP/1.1 requires protocol detection from error strings across three layers.
// That approach cannot safely distinguish absent h2c support from a Hub restart during a request.
// It can repeat a request that the endpoint already processed.
//
// A probe uses a separate connection and sends no application request.
//
// # Endpoint effects
//
// A LeapMux Hub consumes the preface in net/http.maybeServeUnencryptedHTTP2 before routing.
// No application handler receives the probe.
// The probe reaches no application access log or rate limiter.
// An endpoint without h2c can route the preface as a PRI request or reject it.
// A proxy can log one 400 response per process.
// That endpoint cannot serve the Worker's bidirectional stream, which requires HTTP/2 at the same URL.
//
// # Verdict lifetime
//
// An endpoint gains or loses h2c when its reverse proxy configuration changes.
// The process restart after that change permits another probe.
// An expiry rule detects the change sooner but adds recurring probes for a fact that changes at restart.
// The prober therefore keeps a decided verdict for the process lifetime.
// An undecided verdict proves nothing and must permit another probe after undecidedCooldown.
type prober struct {
	endpointURL string
	dial        func(ctx context.Context) (net.Conn, error)

	// run replaces the real probe in this package's tests.
	// A test can count probes or control their completion and verdict without a server.
	run func(ctx context.Context) verdict
	// A caller invokes onWait before it waits on another caller's probe.
	// Only package tests set this hook. They synchronize on that event without a sleep.
	onWait func()
	// now reads the cooldown clock. A test supplies and advances its own clock.
	// The test controls the end of the cooldown without a sleep or a real timer.
	now func() time.Time

	mu      sync.Mutex
	decided bool
	result  verdict
	// undecidedUntil specifies when an undecided verdict permits another probe.
	// Zero permits a probe now. See undecidedCooldown.
	undecidedUntil time.Time
	inflight       chan struct{}

	warnOnce sync.Once
	// calls counts the probes that ran. Only the tests read it.
	calls atomic.Int64
}

func newProber(endpointURL string, dial func(ctx context.Context) (net.Conn, error)) *prober {
	return &prober{endpointURL: endpointURL, dial: dial, now: time.Now}
}

// supportsH2C returns the endpoint's verdict and runs at most one probe at a time.
//
// The probe uses its own goroutine and deadline.
// Each caller, including the caller that starts it, waits for probe completion or its own context cancellation.
// A cancelled caller returns immediately without waiting for the probe deadline.
// Its cancellation does not change the verdict for other callers.
func (p *prober) supportsH2C(ctx context.Context) verdict {
	p.mu.Lock()
	if p.decided {
		result := p.result
		p.mu.Unlock()
		return result
	}
	wait := p.inflight
	if wait == nil && p.now().Before(p.undecidedUntil) {
		// A recent probe reached no verdict. Return undecided without another wait.
		// The caller uses h2c on that verdict. See undecidedCooldown.
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
	// The probe reached no verdict. Another probe here makes each waiter repeat the same failed attempt.
	// The next request after the cooldown starts another probe.
	return verdictUndecided
}

// runProbe runs one probe and publishes its verdict.
// It owns and closes the completion channel, including when the probe reaches no verdict.
func (p *prober) runProbe(done chan struct{}) {
	result := p.probe()

	p.mu.Lock()
	p.inflight = nil
	if result != verdictUndecided {
		p.decided = true
		p.result = result
	} else {
		// Keep no decided verdict after an inconclusive probe. Delay another probe until the cooldown ends.
		p.undecidedUntil = p.now().Add(undecidedCooldown)
	}
	p.mu.Unlock()
	close(done)

	if result == verdictUnsupported {
		p.warnOnce.Do(func() {
			slog.Warn("endpoint does not support cleartext HTTP/2 (h2c). Unary calls use HTTP/1.1. The Worker Connect stream requires HTTP/2 at this URL.",
				"endpoint", p.endpointURL)
		})
	}
}

// probe runs one probe under its own deadline.
// It takes no caller context because its goroutine belongs to no single request.
func (p *prober) probe() verdict {
	p.calls.Add(1)
	probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if p.run != nil {
		return p.run(probeCtx)
	}
	return p.ping(probeCtx)
}

// ping opens one connection and completes its HTTP/2 handshake.
// It waits for a PING acknowledgement and sends only HTTP/2 frames.
// It sends no application method or path. It sends no application body or credential.
func (p *prober) ping(ctx context.Context) verdict {
	conn, err := p.dial(ctx)
	if err != nil {
		return verdictUndecided
	}
	defer func() { _ = conn.Close() }()

	// net/http.ClientConn exposes no PING method. The probe must send no application request.
	// The low-level http2 API retains that capability after its transport deprecation.
	// NewClientConn sends the preface and SETTINGS, then starts the read loop.
	// It does not wait for the server's SETTINGS. Only the PING acknowledgement proves support.
	var transport http2.Transport                    //nolint:staticcheck // net/http.ClientConn exposes no PING method.
	clientConn, err := transport.NewClientConn(conn) //nolint:staticcheck // The probe must send a PING without an application request.
	if err != nil {
		return verdictUnsupported
	}
	defer func() { _ = clientConn.Close() }()

	if err := clientConn.Ping(ctx); err != nil {
		if ctx.Err() != nil {
			// The endpoint accepted the connection and sent nothing before the deadline.
			// Return undecided because the timeout proves no protocol limit.
			return verdictUndecided
		}
		return verdictUnsupported
	}
	return verdictSupported
}
