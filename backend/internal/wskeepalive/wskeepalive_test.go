package wskeepalive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPace drives the probe loop fast enough to assert against, without
// touching any state another parallel test can see.
var testPace = Pace{Interval: 20 * time.Millisecond, Timeout: 200 * time.Millisecond}

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
		testPace.StartBesideAReadLoop(ctx, Probe{Conn: conn, Cancel: cancel, Attrs: []any{"endpoint", "test", "user_id", "user-1"}})
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
// on its own -- and StartDrainingReads is the shape that makes it impossible,
// by starting the reader itself.
//
// The handler here never reads, which is exactly the /ws/userevents shape that
// used to arm its probe ~200 lines before its discard goroutine existed: the
// first probe could not be answered, and at the second the loop cancelled a
// perfectly healthy connection mid-bootstrap.
//
// Probes are forced through an injected channel rather than a fast interval, so
// each tick starts one probe. Its OnAnswer acknowledgement proves that the
// probe succeeded; a second tick alone would also follow one tolerated failure.
func TestWSKeepaliveDrainingReadsAnswersProbesForAHandlerThatNeverReads(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time)
	answers := make(chan struct{})
	dropped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		pace := Pace{Timeout: 10 * time.Second, Ticks: ticks}
		pace.StartDrainingReads(ctx, Probe{Conn: conn, Cancel: cancel, Attrs: []any{"endpoint", "test", "user_id", "user-1"}, OnAnswer: func() {
			select {
			case answers <- struct{}{}:
			case <-ctx.Done():
			}
		}})
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
		select {
		case <-answers:
		case <-dropped:
			t.Fatalf("the keepalive dropped the peer before probe %d was answered", i)
		case <-ctx.Done():
			t.Fatalf("probe %d was not answered", i)
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
		pace := Pace{Timeout: 50 * time.Millisecond, Ticks: ticks}
		pace.StartDrainingReads(ctx, Probe{Conn: conn, Cancel: cancel, Attrs: []any{"endpoint", "test", "user_id", "user-1"}})
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

	for i := range FailuresBeforeDrop {
		select {
		case ticks <- time.Now():
		case <-dropped:
			t.Fatalf("dropped after %d consecutive failures; the threshold is %d",
				i, FailuresBeforeDrop)
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
// silence it replaced. This is the StartBesideAReadLoop shape, where the
// HANDLER owns the read loop; its sibling above covers StartDrainingReads.
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
	handlerDone := make(chan struct{})
	fixtureCtx, cancelFixture := context.WithCancel(t.Context())
	handlerStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		close(handlerStarted)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		stopFixtureCancel := context.AfterFunc(fixtureCtx, cancel)
		defer stopFixtureCancel()
		readDone := make(chan struct{})
		defer func() { cancel(); _ = conn.CloseNow(); <-readDone }()
		go func() {
			defer close(readDone)
			defer cancel()
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
			}
		}()
		pace := Pace{Timeout: 10 * time.Second, Ticks: ticks}
		pace.StartBesideAReadLoop(ctx, Probe{Conn: conn, Cancel: cancel, Attrs: []any{"endpoint", "test", "user_id", "user-1"}})
		<-ctx.Done()
		close(dropped)
	}))
	t.Cleanup(func() {
		cancelFixture()
		srv.Close()
		select {
		case <-handlerStarted:
			<-handlerDone
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	// A healthy peer reads so the library can answer each ping.
	clientReadDone := make(chan struct{})
	t.Cleanup(func() { cancel(); _ = conn.CloseNow(); <-clientReadDone })
	go func() {
		defer close(clientReadDone)
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	// More probes than FailuresBeforeDrop, so a loop that was
	// miscounting successes as failures would have dropped this connection
	// before the last send landed.
	for i := range FailuresBeforeDrop + 2 {
		select {
		case ticks <- time.Now():
		case <-dropped:
			t.Fatalf("a peer that is reading normally must not be dropped (probe %d)", i)
		case <-ctx.Done():
			t.Fatalf("probe %d was never taken", i)
		}
	}
	assert.NoError(t, ctx.Err())
	require.NoError(t, conn.CloseNow())
	select {
	case <-dropped:
	case <-ctx.Done():
		t.Fatal("the server handler did not retire after its peer closed")
	}
	<-handlerDone
}

func TestProductionPace(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 10*time.Second, Production().Interval)
	assert.Equal(t, 10*time.Second, Production().Timeout)
	assert.Equal(t, 2, FailuresBeforeDrop)
}

func TestAZeroPacePanics(t *testing.T) {
	t.Parallel()
	require.Panics(t, func() { (Pace{}).tick() })
}

func TestOnAnswerRunsAfterEachAnsweredProbe(t *testing.T) {
	testOnAnswer(t, true)
}

func TestOnAnswerDoesNotRunForAnUnansweredProbe(t *testing.T) {
	testOnAnswer(t, false)
}

func testOnAnswer(t *testing.T, responds bool) {
	t.Helper()
	timeout := 10 * time.Second
	if !responds {
		timeout = 50 * time.Millisecond
	}
	ticks := make(chan time.Time)
	answers := make(chan struct{})
	dropped := make(chan struct{})
	attrs := []any{"endpoint", "test", "preserve-1", "preserve-2", "preserve-3", "preserve-4"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		(Pace{Ticks: ticks, Timeout: timeout}).StartDrainingReads(ctx, Probe{Conn: conn, Cancel: cancel, Attrs: attrs[:2], OnAnswer: func() {
			select {
			case answers <- struct{}{}:
			case <-ctx.Done():
			}
		}})
		<-ctx.Done()
		close(dropped)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	if responds {
		go func() {
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
			}
		}()
	}
	select {
	case <-answers:
		t.Fatal("answer before a probe")
	default:
	}
	count := FailuresBeforeDrop
	if responds {
		count = 4
	}
	for range count {
		select {
		case ticks <- time.Now():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if responds {
			select {
			case <-answers:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	if !responds {
		select {
		case <-answers:
			t.Fatal("unanswered probe called OnAnswer")
		case <-dropped:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	assert.Equal(t, []any{"endpoint", "test", "preserve-1", "preserve-2", "preserve-3", "preserve-4"}, attrs, "probe logging must preserve caller-owned storage")
}

func TestWSKeepalive_EnforcesTheEarlyDisconnectPolicyDuringDataWrites(t *testing.T) {
	for _, writeDuration := range []time.Duration{26 * time.Second, 9 * time.Second} {
		t.Run(writeDuration.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := testutil.NewDelayedWebSocketTransport(writeDuration)
				conn, _, err := websocket.Dial(t.Context(), "ws://probe", &websocket.DialOptions{HTTPClient: &http.Client{Transport: body}})
				require.NoError(t, err)
				defer func() { _ = conn.CloseNow() }()
				start := time.Now()
				completed := make(chan int, 1)
				go func() {
					count := 0
					for {
						if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("valid data")); err != nil {
							completed <- count
							return
						}
						count++
					}
				}()
				<-body.WriteStarted
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				Production().StartDrainingReads(ctx, Probe{Conn: conn, Cancel: func() { cancel(); _ = conn.CloseNow() }})
				want := 0
				if writeDuration == 9*time.Second {
					want = 2
				}
				assert.Equal(t, want, <-completed)
				assert.Equal(t, 25*time.Second, time.Since(start), "two five-second probe failures can preempt a progressing write")
			})
		})
	}
}
