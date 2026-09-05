package hubtransport

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
)

// --- the decision, with the probe replaced -------------------------------
//
// These cases drive the sharing and caching rules. They inject the probe, so
// there is no timer to size and no server to race: the seam that ends each
// state is a channel this test closes.

func TestProbeRunsOnceUnderConcurrentFirstRequests(t *testing.T) {
	release := make(chan struct{})
	probing := make(chan struct{}, 64)
	p := newProber("http://hub.invalid", nil)
	p.run = func(context.Context) verdict {
		probing <- struct{}{}
		<-release
		return verdictSupported
	}

	const callers = 32
	results := make([]verdict, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = p.supportsH2C(context.Background())
		}()
	}
	// Every caller is either probing or waiting once the first probe starts.
	<-probing
	close(release)
	wg.Wait()

	assert.EqualValues(t, 1, p.calls.Load(), "concurrent first requests must share one probe")
	for i, got := range results {
		assert.Equal(t, verdictSupported, got, "caller %d", i)
	}
	assert.Len(t, probing, 0, "no second probe started")
}

func TestVerdictIsCachedForTheLifeOfTheEndpoint(t *testing.T) {
	for name, answer := range map[string]verdict{"supported": verdictSupported, "unsupported": verdictUnsupported} {
		t.Run(name, func(t *testing.T) {
			p := newProber("http://hub.invalid", nil)
			p.run = func(context.Context) verdict { return answer }

			for range 3 {
				assert.Equal(t, answer, p.supportsH2C(context.Background()))
			}
			assert.EqualValues(t, 1, p.calls.Load())
		})
	}
}

// newTestProber is newProber on a clock the test owns.
func newTestProber(t *testing.T) (*prober, *quartz.Mock) {
	t.Helper()
	clock := quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
	// The zero value of undecidedUntil must be before the first reading.
	clock.Set(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	p := newProber("http://hub.invalid", nil)
	p.now = func() time.Time { return clock.Now() }
	return p, clock
}

// TestUndecidedVerdictIsNotCached pins the rule that keeps a momentary outage
// from pinning a healthy hub to HTTP/1.1 for the life of the process: the
// answer is never remembered, only the RE-ASKING is paced.
func TestUndecidedVerdictIsNotCached(t *testing.T) {
	var answers atomic.Int64
	p, clock := newTestProber(t)
	p.run = func(context.Context) verdict {
		if answers.Add(1) == 1 {
			return verdictUndecided // the endpoint did not answer at all
		}
		return verdictSupported
	}

	assert.Equal(t, verdictUndecided, p.supportsH2C(context.Background()))
	clock.Advance(undecidedCooldown)
	assert.Equal(t, verdictSupported, p.supportsH2C(context.Background()), "the next request after the cooldown re-probes")
	assert.EqualValues(t, 2, p.calls.Load())
}

// TestUndecidedVerdictPacesTheNextProbe pins the cooldown itself.
//
// Before it, EVERY request against an endpoint that accepts a connection and
// then says nothing started its own probe and blocked on it for a whole
// probeTimeout -- inside the caller's own http.Client.Timeout, so a `leapmux
// control version` with a 5s budget reached the hub with an already-expired
// context. The wait bought nothing: both cleartext lanes take h2c on
// undecided, so the caller does the same thing either way.
func TestUndecidedVerdictPacesTheNextProbe(t *testing.T) {
	p, clock := newTestProber(t)
	p.run = func(context.Context) verdict { return verdictUndecided }

	require.Equal(t, verdictUndecided, p.supportsH2C(context.Background()))
	require.EqualValues(t, 1, p.calls.Load())

	// Inside the cooldown: answered from the cooldown, with no probe started.
	for range 5 {
		assert.Equal(t, verdictUndecided, p.supportsH2C(context.Background()))
	}
	assert.EqualValues(t, 1, p.calls.Load(), "a request inside the cooldown must not start a probe")

	// One nanosecond short of the deadline is still inside it.
	clock.Advance(undecidedCooldown - 1)
	assert.Equal(t, verdictUndecided, p.supportsH2C(context.Background()))
	assert.EqualValues(t, 1, p.calls.Load(), "the boundary is exclusive at the deadline, not before it")

	// At the deadline the next request probes again.
	clock.Advance(1)
	assert.Equal(t, verdictUndecided, p.supportsH2C(context.Background()))
	assert.EqualValues(t, 2, p.calls.Load(), "the cooldown paces the re-probe, it does not stop it")
}

// The cooldown must not survive a verdict. An endpoint that comes back is
// measured, and its answer is then kept for the life of the process.
func TestACooldownDoesNotOutlastAVerdict(t *testing.T) {
	var answers atomic.Int64
	p, clock := newTestProber(t)
	p.run = func(context.Context) verdict {
		if answers.Add(1) == 1 {
			return verdictUndecided
		}
		return verdictUnsupported
	}

	require.Equal(t, verdictUndecided, p.supportsH2C(context.Background()))
	clock.Advance(undecidedCooldown)
	require.Equal(t, verdictUnsupported, p.supportsH2C(context.Background()))

	// Decided now, so neither the cooldown nor the clock matters again.
	for range 3 {
		assert.Equal(t, verdictUnsupported, p.supportsH2C(context.Background()))
	}
	assert.EqualValues(t, 2, p.calls.Load())
}

// A prober that has NEVER probed must not be suppressed by the zero value of
// undecidedUntil, whatever the clock reads.
func TestAFreshProberIsNotInACooldown(t *testing.T) {
	p, _ := newTestProber(t)
	p.run = func(context.Context) verdict { return verdictSupported }

	assert.Equal(t, verdictSupported, p.supportsH2C(context.Background()))
	assert.EqualValues(t, 1, p.calls.Load())
}

// TestAWaiterTakesTheUndecidedAnswerInsteadOfReprobing guards against the
// shape where a caller that waited on a probe starts its own when that probe
// reached no verdict: every waiter would then probe in turn, each under the
// full probe deadline, against an endpoint that is simply down.
//
// The next request is still free to re-probe -- see
// TestUndecidedVerdictIsNotCached. The rule here is about the wait itself.
func TestAWaiterTakesTheUndecidedAnswerInsteadOfReprobing(t *testing.T) {
	release := make(chan struct{})
	probing := make(chan struct{}, 1)
	waiting := make(chan struct{})
	p := newProber("http://hub.invalid", nil)
	p.run = func(context.Context) verdict {
		probing <- struct{}{}
		<-release
		return verdictUndecided
	}
	p.onWait = func() { close(waiting) }

	first := make(chan verdict, 1)
	go func() { first <- p.supportsH2C(context.Background()) }()
	<-probing // the first caller owns the probe and is parked in it

	second := make(chan verdict, 1)
	go func() { second <- p.supportsH2C(context.Background()) }()
	<-waiting // the second caller reached the wait rather than probing

	close(release)
	assert.Equal(t, verdictUndecided, <-first)
	assert.Equal(t, verdictUndecided, <-second)
	assert.EqualValues(t, 1, p.calls.Load(), "the waiter must take the undecided answer, not probe again")
}

// TestCancelledFirstRequestDoesNotPoisonTheProbe covers the caller that gives
// up while others wait behind it: the one that leaves must decide nothing.
func TestCancelledFirstRequestDoesNotPoisonTheProbe(t *testing.T) {
	release := make(chan struct{})
	probing := make(chan struct{}, 1)
	waiting := make(chan struct{})
	p := newProber("http://hub.invalid", nil)
	p.run = func(context.Context) verdict {
		probing <- struct{}{}
		<-release
		return verdictSupported
	}
	p.onWait = func() { close(waiting) }

	// The first caller starts the probe. The second waits on it, and is the one
	// cancelled -- a waiter is the caller that can walk away mid-probe.
	first := make(chan verdict, 1)
	go func() { first <- p.supportsH2C(context.Background()) }()
	<-probing

	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan verdict, 1)
	go func() { second <- p.supportsH2C(ctx) }()
	<-waiting
	cancel()
	assert.Equal(t, verdictUndecided, <-second, "a cancelled caller gets no verdict")

	close(release)
	assert.Equal(t, verdictSupported, <-first)
	assert.Equal(t, verdictSupported, p.supportsH2C(context.Background()),
		"the answer survived the cancellation")
	assert.EqualValues(t, 1, p.calls.Load())
}

// TestCancelledStartingRequestDoesNotWaitOutTheProbe covers the caller whose
// request STARTED the probe. It must be able to leave too: a shutdown during
// the first request otherwise waits out the whole probe deadline, and the
// process it is trying to stop stays up for it.
func TestCancelledStartingRequestDoesNotWaitOutTheProbe(t *testing.T) {
	release := make(chan struct{})
	probing := make(chan struct{}, 1)
	p := newProber("http://hub.invalid", nil)
	p.run = func(context.Context) verdict {
		probing <- struct{}{}
		<-release
		return verdictSupported
	}

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan verdict, 1)
	go func() { first <- p.supportsH2C(ctx) }()
	<-probing
	cancel()
	assert.Equal(t, verdictUndecided, <-first, "the starting caller must return with its request")

	// The probe it started outlives it and still publishes its answer, so the
	// callers behind it -- and the next request -- get the verdict.
	close(release)
	assert.Equal(t, verdictSupported, p.supportsH2C(context.Background()))
	assert.EqualValues(t, 1, p.calls.Load())
}

// --- the real probe, against real listeners ------------------------------

func TestPingAcceptsAnH2CEndpoint(t *testing.T) {
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	assert.Equal(t, verdictSupported, probeAddr(t, srv.Listener.Addr().String(), 5*time.Second))
}

func TestPingRejectsAnHTTP11OnlyEndpoint(t *testing.T) {
	srv := hubtransporttest.NewHTTP1Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	assert.Equal(t, verdictUnsupported, probeAddr(t, srv.Listener.Addr().String(), 5*time.Second))
}

// TestPingRejectsAnOriginThatAnswersHTTP11ToThePreface is the nginx shape,
// which a Go server cannot produce: the origin answers the HTTP/2 connection
// preface with an HTTP/1.1 error response and closes.
func TestPingRejectsAnOriginThatAnswersHTTP11ToThePreface(t *testing.T) {
	addr := rawListener(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n"))
		_ = conn.Close()
	})
	assert.Equal(t, verdictUnsupported, probeAddr(t, addr, 5*time.Second))
}

// TestPingLeavesAStalledEndpointUndecided covers the origin that accepts a
// connection and then says nothing. A timeout is not a protocol answer, so
// pinning the process to HTTP/1.1 on it would hide the real failure.
func TestPingLeavesAStalledEndpointUndecided(t *testing.T) {
	addr := rawListener(t, func(net.Conn) { /* accept and stay silent */ })
	assert.Equal(t, verdictUndecided, probeAddr(t, addr, 100*time.Millisecond))
}

// TestPingLeavesAnUnreachableEndpointUndecided covers a hub that is down.
func TestPingLeavesAnUnreachableEndpointUndecided(t *testing.T) {
	// Port 1 on loopback refuses immediately on every platform CI runs.
	assert.Equal(t, verdictUndecided, probeAddr(t, "127.0.0.1:1", time.Second))
}

// TestPingClosesItsConnection pins that a probe costs one connection and not
// one leaked socket per process.
func TestPingClosesItsConnection(t *testing.T) {
	closed := make(chan struct{})
	addr := rawListener(t, func(conn net.Conn) {
		buf := make([]byte, 1)
		// Read until the peer closes, which is what the probe must do.
		for {
			if _, err := conn.Read(buf); err != nil {
				close(closed)
				return
			}
		}
	})
	assert.Equal(t, verdictUndecided, probeAddr(t, addr, 200*time.Millisecond))
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not close its connection")
	}
}

// probeAddr runs the real probe against addr under timeout.
//
// It calls ping rather than probe so the deadline is the test's, not the
// package's five seconds. probe adds only the detach-from-caller wrapper,
// which TestCancelledFirstRequestDoesNotPoisonTheProbe covers.
func probeAddr(t *testing.T, addr string, timeout time.Duration) verdict {
	t.Helper()
	p := newProber("http://"+addr, func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", addr)
	})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.ping(ctx)
}

// rawListener accepts connections and hands each to handle. It exists because
// an origin that is not a Go HTTP server -- nginx answering 400, a proxy that
// accepts and stalls -- is exactly what the fallback has to classify.
func rawListener(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return ln.Addr().String()
}
