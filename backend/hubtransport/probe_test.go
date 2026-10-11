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
// These tests inject the probe to verify its sharing and caching rules.
// Each test closes a channel to end the probe without a timer or server race.

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
	// The first probe starts before the test releases it.
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

// TestUndecidedVerdictIsNotCached verifies that a temporary outage creates no permanent HTTP/1.1 verdict.
// The prober limits repeated probes without caching the undecided verdict.
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

// TestUndecidedVerdictPacesTheNextProbe verifies the cooldown.
//
// Without it, each request to a silent endpoint waits for the full probeTimeout inside its own http.Client.Timeout.
// That consumes the five-second limit of `leapmux control version` before its request reaches the Hub.
// Both cleartext transports use h2c on an undecided verdict, so the extra wait changes no behavior.
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
	assert.EqualValues(t, 2, p.calls.Load(), "the cooldown delays another probe and permits it at the deadline")
}

// A decided verdict ends the cooldown and stays cached for the process lifetime.
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

// The zero value of undecidedUntil must not suppress the first probe, whatever the clock reads.
func TestAFreshProberIsNotInACooldown(t *testing.T) {
	p, _ := newTestProber(t)
	p.run = func(context.Context) verdict { return verdictSupported }

	assert.Equal(t, verdictSupported, p.supportsH2C(context.Background()))
	assert.EqualValues(t, 1, p.calls.Load())
}

// TestAWaiterTakesTheUndecidedAnswerInsteadOfReprobing verifies that a waiter starts no second probe after an undecided result.
// Otherwise each waiter consumes another full probe deadline against the same unreachable endpoint.
//
// A later request can probe again. See TestUndecidedVerdictIsNotCached.
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
	<-probing // The first caller waits in its own probe.

	second := make(chan verdict, 1)
	go func() { second <- p.supportsH2C(context.Background()) }()
	<-waiting // the second caller reached the wait rather than probing

	close(release)
	assert.Equal(t, verdictUndecided, <-first)
	assert.Equal(t, verdictUndecided, <-second)
	assert.EqualValues(t, 1, p.calls.Load(), "the waiter must take the undecided answer, not probe again")
}

// TestCancelledFirstRequestDoesNotPoisonTheProbe verifies that a cancelled waiter does not change the probe's verdict.
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

	// The first caller starts the probe. The test cancels the second caller while it waits on that probe.
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

// TestCancelledStartingRequestDoesNotWaitOutTheProbe verifies that the caller that starts a probe can cancel its own wait.
// Otherwise process shutdown during the first request must wait for the full probe deadline.
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

	// The probe continues after that caller cancels. Other callers and later requests receive its verdict.
	close(release)
	assert.Equal(t, verdictSupported, p.supportsH2C(context.Background()))
	assert.EqualValues(t, 1, p.calls.Load())
}

// --- the real probe, against real listeners ------------------------------

func TestPingAcceptsAnH2CEndpoint(t *testing.T) {
	var requests atomic.Int64
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	assert.Equal(t, verdictSupported, probeAddr(t, srv.Listener.Addr().String(), 5*time.Second))
	assert.Zero(t, requests.Load(), "the PING probe must reach no application handler")
}

func TestPingRejectsAnHTTP11OnlyEndpoint(t *testing.T) {
	srv := hubtransporttest.NewHTTP1Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	assert.Equal(t, verdictUnsupported, probeAddr(t, srv.Listener.Addr().String(), 5*time.Second))
}

// TestPingRejectsAnOriginThatAnswersHTTP11ToThePreface verifies rejection with an HTTP/1.1 400 response.
// The raw listener fixes the response bytes and closes the connection.
func TestPingRejectsAnOriginThatAnswersHTTP11ToThePreface(t *testing.T) {
	addr := rawListener(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n"))
		_ = conn.Close()
	})
	assert.Equal(t, verdictUnsupported, probeAddr(t, addr, 5*time.Second))
}

// TestPingLeavesAStalledEndpointUndecided verifies a silent endpoint.
// A timeout proves no protocol limit and must not select HTTP/1.1 permanently.
func TestPingLeavesAStalledEndpointUndecided(t *testing.T) {
	addr := rawListener(t, func(net.Conn) { /* accept and stay silent */ })
	assert.Equal(t, verdictUndecided, probeAddr(t, addr, 100*time.Millisecond))
}

// TestPingLeavesAnUnreachableEndpointUndecided covers a hub that is down.
func TestPingLeavesAnUnreachableEndpointUndecided(t *testing.T) {
	// Port 1 on loopback refuses immediately on every platform CI runs.
	assert.Equal(t, verdictUndecided, probeAddr(t, "127.0.0.1:1", time.Second))
}

// TestPingClosesItsConnection verifies that the probe closes its own socket.
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
	case <-time.After(30 * time.Second):
		t.Fatal("probe did not close its connection")
	}
}

// probeAddr runs the real probe against addr under timeout.
//
// It calls ping with the test's deadline instead of probe's five-second deadline.
// TestCancelledFirstRequestDoesNotPoisonTheProbe covers probe's separate ownership of the context.
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

// TestRawListenerClosesAcceptedConnections requires cleanup to close each accepted socket.
func TestRawListenerClosesAcceptedConnections(t *testing.T) {
	accepted := make(chan net.Conn, 1)
	var serverConn net.Conn
	require.True(t, t.Run("listener ownership", func(child *testing.T) {
		addr := rawListener(child, func(conn net.Conn) { accepted <- conn })
		clientConn, err := net.DialTimeout("tcp", addr, 30*time.Second)
		require.NoError(child, err)
		t.Cleanup(func() { _ = clientConn.Close() })
		select {
		case serverConn = <-accepted:
		case <-time.After(30 * time.Second):
			child.Fatal("the listener accepted no connection")
		}
		t.Cleanup(func() { _ = serverConn.Close() })
	}))
	_, err := serverConn.Write([]byte("the subtest ended"))
	require.ErrorIs(t, err, net.ErrClosed, "fixture cleanup must close every accepted socket")
}

// rawListener supplies a raw endpoint for protocol rejection and silent-endpoint tests.
func rawListener(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	acceptedDone := make(chan struct{})
	var connections []net.Conn
	var handlers sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		<-acceptedDone
		for _, conn := range connections {
			_ = conn.Close()
		}
		handlers.Wait()
	})
	go func() {
		defer close(acceptedDone)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connections = append(connections, conn)
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				handle(conn)
			}()
		}
	}()
	return ln.Addr().String()
}
