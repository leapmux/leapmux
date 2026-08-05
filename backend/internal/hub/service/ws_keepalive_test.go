package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPace drives the probe loop fast enough to assert against, without
// touching any state another parallel test can see.
var testPace = wsKeepalivePace{interval: 20 * time.Millisecond, timeout: 200 * time.Millisecond}

// A half-open peer -- one that stops answering without sending a close frame --
// is invisible to every other bound on these sockets, because they all fire on a
// WRITE. Its lease counts against max_connections_per_user the whole time, and
// the refusal message a capped user gets tells them to close a tab, which does
// nothing for a socket that is already gone.
func TestWSKeepaliveDropsAPeerThatStopsAnsweringPings(t *testing.T) {
	t.Parallel()

	// A server that accepts, then reads (so pongs are processed) and lets the
	// keepalive run against a client we can silence.
	dropped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
			}
		}()
		testPace.startBesideAReadLoop(ctx, conn, cancel, "test", "user-1")
		<-ctx.Done()
		close(dropped)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	// The client never reads, so it never answers a ping. That is exactly the
	// half-open shape: the TCP connection is up, the peer is not participating.
	select {
	case <-dropped:
	case <-ctx.Done():
		t.Fatal("the keepalive must drop a peer that stops answering")
	}
}

// A probe cannot see its own answer unless something is reading the socket:
// coder/websocket waits for a Reader call to process the pong. So a handler
// that has no use for what the peer sends must not be able to arm a probe loop
// on its own -- and startDrainingReads is the shape that makes it impossible,
// by starting the reader itself.
//
// The handler here never reads, which is exactly the /ws/userevents shape that
// used to arm its probe ~200 lines before its discard goroutine existed: the
// first probe could not be answered, and at the second the loop cancelled a
// perfectly healthy connection mid-bootstrap.
//
// Probes are forced through an injected channel rather than a fast interval, so
// nothing here is sized by a duration: each send completes only when the loop
// comes back for another tick, which it does only after a probe SUCCEEDED -- a
// failed one exits the loop and cancels.
func TestWSKeepaliveDrainingReadsAnswersProbesForAHandlerThatNeverReads(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time)
	dropped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		pace := wsKeepalivePace{timeout: 10 * time.Second, ticks: ticks}
		pace.startDrainingReads(ctx, conn, cancel, "test", "user-1")
		// The handler itself never touches the socket again -- the keepalive owns
		// every read on it.
		<-ctx.Done()
		close(dropped)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	// A healthy peer: it reads, so the library answers each ping for it.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	for i := range 3 {
		select {
		case ticks <- time.Now():
		case <-dropped:
			t.Fatalf("the keepalive dropped a healthy connection at probe %d: "+
				"its pong had nobody to read it", i)
		case <-ctx.Done():
			t.Fatalf("probe %d was never taken", i)
		}
	}
}

// A single failed probe is not a dead peer, so it must not be the whole
// verdict. coder/websocket bounds every control write at five seconds of its
// own, and that budget covers ACQUIRING the connection's write mutex -- which a
// data frame holds for as long as it is in flight. A tab whose receive window is
// full during a terminal burst is precisely the case ws_relay_writer budgets ten
// seconds and thirty seconds of stall to survive, and condemning on one sample
// let the keepalive pre-empt those bounds and blame a client that was alive.
//
// Deterministic without a clock: an unbuffered send on `ticks` completes only
// when the probe loop is back at its select, so the SECOND send landing is proof
// the loop survived the first failure. A loop that dropped on one failure would
// have returned instead, and the send would never land.
func TestWSKeepaliveSurvivesASingleFailedProbe(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time)
	dropped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		// Short, because every probe here is meant to time out.
		pace := wsKeepalivePace{timeout: 50 * time.Millisecond, ticks: ticks}
		pace.startDrainingReads(ctx, conn, cancel, "test", "user-1")
		<-ctx.Done()
		close(dropped)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A peer that never reads, so no probe is ever answered.
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	for i := range wsKeepaliveFailuresBeforeDrop {
		select {
		case ticks <- time.Now():
		case <-dropped:
			t.Fatalf("dropped after %d consecutive failures; the threshold is %d",
				i, wsKeepaliveFailuresBeforeDrop)
		case <-ctx.Done():
			t.Fatalf("probe %d was never taken", i)
		}
	}

	// ...and the threshold is a threshold, not an amnesty: once it is reached the
	// connection still goes.
	select {
	case <-dropped:
	case <-ctx.Done():
		t.Fatal("a peer that answers nothing must eventually be dropped")
	}
}

// The other half of the property: a healthy peer must survive. A probe that
// disconnected a client answering normally would be strictly worse than the
// silence it replaced. This is the startBesideAReadLoop shape, where the
// HANDLER owns the read loop; its sibling above covers startDrainingReads.
//
// Bounded by an event, not a duration. An unbuffered send on `ticks` lands only
// when the probe loop comes back around to receive it, so "four sends landed
// and nothing was dropped" is the assertion -- no sleep to outrun, and nothing
// for a loaded box to overshoot. Sizing this window with a real timeout instead
// made it fail on the HEALTHY path under `-race`, which is the failure mode
// da194c71 removed everywhere else.
func TestWSKeepaliveLeavesAnAnsweringPeerAlone(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time)
	dropped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
			}
		}()
		pace := wsKeepalivePace{timeout: 10 * time.Second, ticks: ticks}
		pace.startBesideAReadLoop(ctx, conn, cancel, "test", "user-1")
		<-ctx.Done()
		close(dropped)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	// A healthy peer: it reads, so the library answers each ping for it.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	// More probes than wsKeepaliveFailuresBeforeDrop, so a loop that was
	// miscounting successes as failures would have dropped this connection
	// before the last send landed.
	for i := range wsKeepaliveFailuresBeforeDrop + 2 {
		select {
		case ticks <- time.Now():
		case <-dropped:
			t.Fatalf("a peer that is reading normally must not be dropped (probe %d)", i)
		case <-ctx.Done():
			t.Fatalf("probe %d was never taken", i)
		}
	}
	assert.NoError(t, ctx.Err())
}
