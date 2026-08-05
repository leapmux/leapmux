package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The amplification the limiter exists to close: a ping is cheap to send and the
// library answers EVERY one with a pong, which costs a write on the connection's
// write mutex -- the same mutex the writer serving real frames needs.
func TestWSControlLimiterRefusesAFloodAfterTheBurst(t *testing.T) {
	t.Parallel()

	// A frozen clock, so the bucket refills by exactly what the test says and
	// the assertion is on the rule rather than on how fast the machine ran.
	now := time.Now()
	l := newWSControlLimiter("test", "user-1", func() time.Time { return now })

	for i := range int(wsControlFrameBurst) {
		ok, abusive := l.allow()
		assert.True(t, ok, "the burst must be absorbed in full (frame %d)", i)
		assert.False(t, abusive)
	}

	ok, _ := l.allow()
	assert.False(t, ok, "past the burst, a frame must not be answered")
	assert.Equal(t, 1, l.refusedCount())

	// Refills at the sustained rate, and not faster.
	now = now.Add(time.Second)
	for range int(wsControlFrameRate) {
		ok, _ = l.allow()
		assert.True(t, ok, "a second of credit must buy exactly the sustained rate")
	}
	ok, _ = l.allow()
	assert.False(t, ok, "and no more than that")
}

// Refusing has to stay cheaper than answering, so a sustained flood is not
// forgiven: past the abuse limit the connection goes.
func TestWSControlLimiterReportsSustainedAbuse(t *testing.T) {
	t.Parallel()

	now := time.Now()
	l := newWSControlLimiter("test", "user-1", func() time.Time { return now })

	var abusive bool
	for range int(wsControlFrameBurst) + wsControlFrameAbuseLimit {
		_, abusive = l.allow()
	}
	assert.True(t, abusive, "a peer that keeps flooding must eventually be closed")
	assert.GreaterOrEqual(t, l.refusedCount(), wsControlFrameAbuseLimit)
}

// The two hooks spend ONE budget, so a peer that mixes pings and pongs drives
// both past the abuse limit -- and the teardown they share must still run once.
//
// A sync.Once per hook let each of them fire: two goroutines, two warnings, two
// Closes on one connection, against the single teardown onPingReceived's doc
// promises. onAbuse is called synchronously here (acceptOptions is what spawns
// the real close off the read path), so this counts rather than races.
func TestWSControlLimiterTearsTheConnectionDownOnce(t *testing.T) {
	t.Parallel()

	now := time.Now()
	l := newWSControlLimiter("test", "user-1", func() time.Time { return now })

	var abuses int
	onAbuse := func() { abuses++ }
	ping, pong := l.onPingReceived(onAbuse), l.onPongReceived(onAbuse)

	ctx := context.Background()
	// Exhaust the burst and then keep flooding, through the PING hook, until it
	// has refused enough frames to stop looking like a bug.
	for range int(wsControlFrameBurst) + wsControlFrameAbuseLimit {
		ping(ctx, nil)
	}
	require.Equal(t, 1, abuses, "a sustained ping flood must close the connection")

	// The pong hook now meets a budget that is already exhausted and a refusal
	// count already past the limit, so it reports abuse on its very first frame.
	pong(ctx, nil)
	assert.Equal(t, 1, abuses,
		"the two hooks share one connection, so they must share its one teardown")

	// ...and neither hook forgets that, however long the flood lasts.
	for range 100 {
		ping(ctx, nil)
		pong(ctx, nil)
	}
	assert.Equal(t, 1, abuses)
	assert.GreaterOrEqual(t, l.refusedCount(), wsControlFrameAbuseLimit,
		"the refusals are still counted; it is only the teardown that is once")
}

// A well-behaved client must never reach the limit. Our own keepalive probes
// once every wsKeepaliveInterval and the peer answers each one, so the steady
// state is two control frames per interval against a budget of two per SECOND.
func TestWSControlLimiterLeavesNormalTrafficAlone(t *testing.T) {
	t.Parallel()

	now := time.Now()
	l := newWSControlLimiter("test", "user-1", func() time.Time { return now })

	for range 1000 {
		now = now.Add(wsKeepaliveInterval)
		for range 2 { // a probe's pong, plus an unsolicited heartbeat pong
			ok, abusive := l.allow()
			require.True(t, ok, "normal keepalive traffic must never be refused")
			require.False(t, abusive)
		}
	}
	assert.Zero(t, l.refusedCount())
}

// A peer that clumps its control frames but goes quiet between clumps is not a
// flood: the overflow of each clump is refused for free, and the bucket is back
// at full burst seconds later. Judged by a LIFETIME total it was closed after
// about 34 hours at an average of 0.014 frames/sec -- with StatusPolicyViolation,
// which latches the browser transport permanently, so the recovery is a reload.
func TestWSControlLimiterForgivesABurstyPeerThatRecovers(t *testing.T) {
	t.Parallel()

	now := time.Now()
	l := newWSControlLimiter("test", "user-1", func() time.Time { return now })

	// A hundred hours of fifty-frame clumps. Each clump spends the burst and
	// then refuses its remaining 30, so 3000 frames are refused in all -- three
	// times the abuse limit, and every one of them free.
	for range 100 {
		now = now.Add(time.Hour)
		for range 50 {
			_, abusive := l.allow()
			require.False(t, abusive,
				"a peer that fully recovers between clumps is not abusive")
		}
	}
	assert.Equal(t, 30, l.refusedCount(),
		"only the clump still in progress is counted; the 99 before it are closed episodes")

	// And the last one closes the moment the bucket is full again.
	now = now.Add(time.Hour)
	_, abusive := l.allow()
	require.False(t, abusive)
	assert.Zero(t, l.refusedCount(),
		"a full bucket ends the episode its refusals belonged to")
}

// Liveness is READING, not sending. A peer that writes steadily while never
// reading is exactly the half-open case the probe loop exists to catch, and it
// must be dropped no matter how busy its inbound half looks.
//
// Deliberately NOT a test of the stronger claim wsControlLimiter's doc makes --
// that an unsolicited pong cannot satisfy a probe. That property is
// coder/websocket's (it matches a pong against outstanding ping ids), and
// reaching it needs a raw pong frame, which the library exposes no writer for;
// a data frame does not exercise its pong arm at all. The paired assertion that
// a peer which DOES read survives is
// TestWSKeepaliveDrainingReadsAnswersProbesForAHandlerThatNeverReads -- the two
// together are what make this one discriminating rather than a restatement of
// TestWSKeepaliveDropsAPeerThatStopsAnsweringPings.
func TestWSInboundTrafficDoesNotStandInForAPong(t *testing.T) {
	t.Parallel()

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

	// A client that never READS -- so it can never answer a probe -- while
	// writing steadily. Its half of the socket is busy the entire time.
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	go func() {
		for i := range 200 {
			if ctx.Err() != nil {
				return
			}
			_ = conn.Write(ctx, websocket.MessageText, fmt.Appendf(nil, "%d", i))
			time.Sleep(time.Millisecond)
		}
	}()

	select {
	case <-dropped:
	case <-ctx.Done():
		t.Fatal("inbound traffic must not stand in for the pong a probe is waiting for")
	}
}
