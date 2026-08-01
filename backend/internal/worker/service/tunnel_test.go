package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/tunnelflow"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func tunnelTestSetup(t *testing.T) (*Service, *channel.Dispatcher, *testResponseWriter) {
	t.Helper()
	// setupTestService already registers the worker to "user-1", the identity its
	// test channel is opened with.
	return setupTestService(t)
}

func TestOpenTunnelConn_HappyPath(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "happy-path",
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.errors, 0, "expected no errors")
	require.Len(t, w.responses, 1, "expected one response")

	var resp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.NotEmpty(t, resp.GetConnId(), "expected non-empty conn_id")
}

func TestOpenTunnelConn_OwnershipEnforcement(t *testing.T) {
	_, d, _ := tunnelTestSetup(t)
	w2 := newTestWriter()
	// Dispatch as user-2 (not the owner "user-1").
	payload, _ := proto.Marshal(&leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "wrong-owner",
		TargetAddr: "127.0.0.1",
		TargetPort: 1234,
	})
	d.DispatchWith(context.Background(), userid.MustNew("user-2"), &leapmuxv1.InnerRpcRequest{
		Method:  "OpenTunnelConn",
		Payload: payload,
	}, w2)

	require.Len(t, w2.errors, 1)
	assert.Equal(t, int32(7), w2.errors[0].code, "expected PERMISSION_DENIED")
}

func TestTunnelMutationOwnershipEnforcement(t *testing.T) {
	_, d, _ := tunnelTestSetup(t)
	for _, test := range []struct {
		name    string
		method  string
		request proto.Message
	}{
		{
			name:    "send data",
			method:  "SendTunnelData",
			request: &leapmuxv1.SendTunnelDataRequest{ConnId: "owner-connection", Data: []byte("secret")},
		},
		{
			name:    "close connection",
			method:  "CloseTunnelConn",
			request: &leapmuxv1.CloseTunnelConnRequest{ConnId: "owner-connection"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := proto.Marshal(test.request)
			require.NoError(t, err)
			writer := newTestWriter()
			d.DispatchWith(context.Background(), userid.MustNew("user-2"), &leapmuxv1.InnerRpcRequest{
				Method:  test.method,
				Payload: payload,
			}, writer)

			require.Len(t, writer.errors, 1)
			assert.Equal(t, int32(7), writer.errors[0].code, "expected PERMISSION_DENIED")
			require.Empty(t, writer.responses)
		})
	}
}

func TestOpenTunnelConn_DialFailure(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	// Use localhost with a port that nothing is listening on.
	// Port 1 requires root on most systems, so dial should fail immediately.
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "dial-failure",
		TargetAddr: "127.0.0.1",
		TargetPort: 1,
	}, w)

	require.Len(t, w.errors, 1, "expected dial error")
	assert.Equal(t, int32(13), w.errors[0].code, "expected INTERNAL error")
}

func TestOpenTunnelConn_InvalidTargetAddr(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "invalid-address",
		TargetAddr: "",
		TargetPort: 80,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(3), w.errors[0].code, "expected INVALID_ARGUMENT")
}

func TestOpenTunnelConn_RequiresClientConnID(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: "127.0.0.1",
		TargetPort: 80,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(3), w.errors[0].code)
}

func TestTunnelManagerCancelBeforeOpenPreventsLateStore(t *testing.T) {
	manager := newTunnelManager()
	const connID = "canceled-before-open"
	require.Nil(t, manager.cancel(connID))
	assert.False(t, manager.beginOpen(connID, func() {}), "a close processed first must cancel the later open")

	const inFlightID = "canceled-during-open"
	require.True(t, manager.beginOpen(inFlightID, func() {}))
	require.Nil(t, manager.cancel(inFlightID))
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	tc := newTunnelConn(nil, "", client, nil, nil)
	assert.False(t, manager.store(inFlightID, tc), "a close that won the race must refuse the late store")
	assert.True(t, tc.closed.Load())
	assert.Nil(t, manager.get(inFlightID))
}

func TestTunnelManagerCancelAbortsInFlightDial(t *testing.T) {
	manager := newTunnelManager()
	const connID = "in-flight-dial"
	abortCalled := make(chan struct{})
	dialCancel := func() { close(abortCalled) }
	require.True(t, manager.beginOpen(connID, dialCancel))

	// A close that arrives while the dial is still in flight must abort the dial
	// immediately (causal cancel) rather than waiting for the dial timeout.
	require.Nil(t, manager.cancel(connID))
	select {
	case <-abortCalled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not invoke the in-flight dial's CancelFunc")
	}

	// A late store for the same conn_id must be refused — the close won the race.
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	tc := newTunnelConn(nil, "", client, nil, nil)
	assert.False(t, manager.store(connID, tc))
	assert.True(t, tc.closed.Load())
	assert.Nil(t, manager.get(connID))
}

func TestTunnelManagerOldReadLoopCannotRemoveReusedConnID(t *testing.T) {
	manager := newTunnelManager()
	const connID = "reused-id"
	oldClient, oldPeer := net.Pipe()
	t.Cleanup(func() { _ = oldPeer.Close() })
	oldConn := newTunnelConn(nil, "", oldClient, nil, nil)
	require.True(t, manager.beginOpen(connID, func() {}))
	require.True(t, manager.store(connID, oldConn))
	require.Same(t, oldConn, manager.cancel(connID))
	_ = oldClient.Close()

	newClient, newPeer := net.Pipe()
	t.Cleanup(func() { _ = newClient.Close() })
	t.Cleanup(func() { _ = newPeer.Close() })
	newConn := newTunnelConn(nil, "", newClient, nil, nil)
	require.True(t, manager.beginOpen(connID, func() {}))
	require.True(t, manager.store(connID, newConn))
	manager.removeIf(connID, oldConn)
	assert.Same(t, newConn, manager.get(connID))
}

// TestTunnelManagerCloseAndRemoveEvictsWatcherClosedConn covers the
// open-response-failure path: the response write fails after the conn is stored,
// and the per-conn lifetime watcher (session/channel death mid-open) already
// closed the conn. The old code gated the eviction on winning the close race, so
// a watcher-closed conn leaked in m.conns for the process lifetime -- nothing
// else evicts it because tunnelReadLoop never starts on this path.
func TestTunnelManagerCloseAndRemoveEvictsWatcherClosedConn(t *testing.T) {
	manager := newTunnelManager()
	const connID = "open-response-failed"
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	tc := newTunnelConn(nil, "", client, nil, nil)
	require.True(t, manager.beginOpen(connID, func() {}))
	require.True(t, manager.store(connID, tc))

	// Simulate the per-conn watcher winning the close race during teardown, so a
	// later close() no longer reports that it performed the close.
	require.True(t, tc.close())
	require.False(t, tc.close(), "precondition: tc already closed, so close() no longer wins the race")

	// The eviction must still happen -- otherwise the dead conn leaks in m.conns.
	manager.closeAndRemove(connID, tc)
	assert.Nil(t, manager.get(connID), "a watcher-closed conn must be evicted, not leaked")
}

func TestTunnelConnWriteStopsWhenContextIsCanceled(t *testing.T) {
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = peer.Close() })
	// Cancelling the conn's lifetime context must unblock a stuck target write via
	// the single per-conn watcher (newTunnelConn closes the conn), which replaces
	// the old per-frame deadline watcher.
	ctx, cancel := context.WithCancel(context.Background())
	tc := newTunnelConn(nil, "", client, nil, ctx)
	result := make(chan error, 1)
	go func() { result <- tc.writeData(ctx, 0, []byte("blocked"), false) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("canceled tunnel write remained blocked")
	}
}

func TestTunnelReadLoopClosesIdleTargetWithChannelContext(t *testing.T) {
	manager := newTunnelManager()
	ctx, cancel := context.WithCancel(context.Background())
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	tc := newTunnelConn(nil, "", client, nil, ctx)
	const connID = "idle-target"
	require.True(t, manager.beginOpen(connID, func() {}))
	require.True(t, manager.store(connID, tc))
	go tunnelReadLoop(manager, connID, tc)
	cancel()
	testutil.AssertEventually(t, func() bool { return manager.get(connID) == nil }, "channel cancellation should close idle tunnel target")
}

// TestTunnelReadLoopHalfCloseEvictsOnLifetimeCancel covers the leak the target
// half-close path would otherwise create. On a target FIN (io.EOF) the read loop
// forwards the read-EOF and RETURNS early WITHOUT evicting, keeping the conn open
// for the client's remaining writes -- so on session death the per-conn lifetime
// watcher is the only reclaimer left. The watcher must evict from the manager,
// not merely close the socket; otherwise the dead conn leaks in the
// worker-lifetime m.conns map (registerTunnelHandlers builds one manager per
// worker, shared across every channel session).
func TestTunnelReadLoopHalfCloseEvictsOnLifetimeCancel(t *testing.T) {
	manager := newTunnelManager()
	// The half-close EOF branch arms the idle reaper (markHalfClosed -> the
	// sweeper goroutine), so stop it at cleanup -- otherwise a ticker + goroutine
	// leak into later tests. The shortened knobs are not needed here (the lifetime
	// watcher reaps immediately), but Stop() is.
	t.Cleanup(manager.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	target, peer := net.Pipe()
	const connID = "half-closed-target"
	tc := newTunnelConn(manager, connID, target, newTestWriter(), ctx)
	require.True(t, manager.beginOpen(connID, func() {}))
	require.True(t, manager.store(connID, tc))

	// The target half-closes its write side: net.Pipe delivers io.EOF to the
	// target's read side, so the read loop forwards the EOF and returns early,
	// leaving the conn registered.
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); tunnelReadLoop(manager, connID, tc) }()
	_ = peer.Close()

	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("read loop did not exit on target half-close")
	}
	require.NotNil(t, manager.get(connID), "a half-closed conn must stay registered for the client's remaining writes")
	require.False(t, tc.closed.Load(), "the half-close path must not close the conn")

	// Session death: cancelling the lifetime context must evict the conn via the
	// per-conn watcher, not merely close its socket. This path runs IMMEDIATELY on
	// cancellation -- the idle reaper is a second, time-bounded reclaimer, not a
	// replacement for it; sweepHalfClosed only reconciles entries whose conns this
	// watcher already closed on the next tick.
	cancel()
	testutil.AssertEventually(t, func() bool { return manager.get(connID) == nil },
		"a half-closed conn must be evicted on lifetime cancellation, not leaked")
	assert.True(t, tc.closed.Load())
	_ = target.Close()
}

// withShortHalfCloseReaper shortens the idle-reaper knobs for a test and restores
// them on cleanup, mirroring the staleCancelMarkerTTL pattern. It also stops the
// manager's sweeper goroutine at cleanup so a self-stopping-but-in-flight sweeper
// from one test does not leak into the next. Returns the (shortened) idle timeout.
func withShortHalfCloseReaper(t *testing.T, manager *tunnelManager) time.Duration {
	t.Helper()
	idleOrig, sweepOrig := halfCloseIdleTimeout, halfCloseSweepInterval
	halfCloseIdleTimeout = 60 * time.Millisecond
	halfCloseSweepInterval = 15 * time.Millisecond
	t.Cleanup(func() {
		halfCloseIdleTimeout, halfCloseSweepInterval = idleOrig, sweepOrig
		if manager != nil {
			manager.Stop()
		}
	})
	return halfCloseIdleTimeout
}

// halfCloseTarget drives a target conn into the half-closed state the reaper
// targets: the worker's read loop reads io.EOF, relays it, returns, and arms the
// idle reaper. It returns when the read loop has exited. It is for the basic
// reap-on-idle tests that send NO further writes after the half-close -- the peer
// is FULLY closed (net.Pipe cannot half-close; a full close still delivers io.EOF
// to the worker's read side, which is the signal the half-close branch keys on).
// Tests that need to keep draining the upload direction after the half-close must
// use a TCP pair (net.Listen + CloseWrite) instead -- see
// TestTunnelHalfCloseReaper_WriteActivityResetsClock.
func halfCloseTarget(t *testing.T, manager *tunnelManager, connID, ctxKey string) (
	workerConn, peerConn net.Conn, tc *tunnelConn, cancel context.CancelFunc,
) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	worker, peer := net.Pipe()
	// net.Pipe cannot half-close; for the basic reap tests (no writes after) a
	// full peer close delivers io.EOF to the worker read, which is the signal the
	// half-close branch keys on. Tests that need to keep draining writes use a
	// TCP pair instead (see TestTunnelHalfCloseReaper_WriteActivityResetsClock).
	tc = newTunnelConn(manager, connID, worker, newTestWriter(), ctx)
	require.True(t, manager.beginOpen(connID, func() {}))
	require.True(t, manager.store(connID, tc))
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); tunnelReadLoop(manager, connID, tc) }()
	_ = peer.Close()
	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("read loop did not exit on target half-close")
	}
	return worker, peer, tc, cancel
}

// TestTunnelHalfCloseReaper_ReclaimsOnIdle covers the primary acceptance: a
// half-closed conn whose client CloseTunnelConn is LOST is reclaimed within the
// idle deadline WITHOUT waiting for channel death. Before #318 such a conn
// lingered in the worker-lifetime m.conns map for the channel's lifetime (hours
// for a pooled SSH/port-forward session); the idle reaper bounds that leak.
func TestTunnelHalfCloseReaper_ReclaimsOnIdle(t *testing.T) {
	manager := newTunnelManager()
	withShortHalfCloseReaper(t, manager)
	const connID = "half-closed-idle"
	workerConn, _, tc, cancel := halfCloseTarget(t, manager, connID, connID)
	defer cancel()
	t.Cleanup(func() { _ = workerConn.Close() })

	// Precondition: the half-close path parked the conn (registered, not closed)
	// and armed the reaper.
	require.NotNil(t, manager.get(connID), "a half-closed conn must stay registered for the client's remaining writes")
	require.False(t, tc.closed.Load(), "the half-close path must not close the conn")

	// No further writes -> the reaper closes and evicts within the idle window.
	testutil.AssertEventually(t, func() bool { return manager.get(connID) == nil },
		"a half-closed conn with a lost CloseTunnelConn must be reaped within the idle deadline, not leaked until channel death")
	assert.True(t, tc.closed.Load(), "the reaper must close the conn it reaps")
}

// TestTunnelHalfCloseReaper_WriteActivityResetsClock covers the second
// acceptance: an ACTIVE upload (writes still progressing) is not reaped
// mid-transfer. writeData bumps the conn's activity clock on each successful
// write, so a progressing upload outruns the reaper; once the writes stop, the
// clock expires and the reaper reclaims the conn.
//
// It uses a TCP loopback pair (not net.Pipe) so the peer can half-close its write
// side -- delivering io.EOF to the worker's read loop -- while keeping its read
// side open to drain the worker's writes, which is exactly the target-half-close
// shape the reaper is built for.
//
// The reaper's clock is INJECTED and the sweep is driven directly, rather than
// letting a background ticker race real time. The previous version wrote in a loop
// with real sleeps and asserted survival across a real 60ms idle budget, which a
// loaded machine can blow through between two writes -- so it failed under the
// full parallel suite while passing in isolation. Nothing here now depends on how
// fast the machine is.
func TestTunnelHalfCloseReaper_WriteActivityResetsClock(t *testing.T) {
	manager := newTunnelManager()
	// A controlled clock, read by BOTH the activity stamp and the sweep deadline.
	clock := time.Now()
	manager.now = func() time.Time { return clock }
	// Disable the background sweeper: a non-positive interval makes the goroutine
	// exit without ticking, so every sweep below is one this test asked for.
	idleOrig, sweepOrig := halfCloseIdleTimeout, halfCloseSweepInterval
	const idle = 60 * time.Second
	halfCloseIdleTimeout, halfCloseSweepInterval = idle, 0
	t.Cleanup(func() {
		halfCloseIdleTimeout, halfCloseSweepInterval = idleOrig, sweepOrig
		manager.Stop()
	})
	const connID = "half-closed-active"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	peerAccept := make(chan net.Conn, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr == nil {
			peerAccept <- c
		}
	}()
	workerConn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = workerConn.Close() })
	peer := <-peerAccept
	require.NotNil(t, peer)
	t.Cleanup(func() { _ = peer.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	tc := newTunnelConn(manager, connID, workerConn, newTestWriter(), ctx)
	require.True(t, manager.beginOpen(connID, func() {}))
	require.True(t, manager.store(connID, tc))

	// Peer half-closes its WRITE side -> worker reads io.EOF -> read loop relays
	// EOF, returns, arms the reaper. The peer's read side stays open.
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); tunnelReadLoop(manager, connID, tc) }()
	require.NoError(t, peer.(*net.TCPConn).CloseWrite())
	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("read loop did not exit on target half-close")
	}
	require.False(t, tc.closed.Load(), "precondition: the half-close path must not close the conn")

	// Drain the peer's read side on a goroutine so the worker's writes do not
	// block on a full pipe. The writes below keep the upload direction live.
	peerDone := make(chan struct{})
	go func() { defer close(peerDone); _, _ = io.Copy(io.Discard, peer) }()

	// Advance most of the way to the deadline, then write. Each write bumps
	// lastActivity, so the conn must survive a sweep taken PAST the original
	// deadline -- which is the whole claim.
	var seq uint64
	for range 5 {
		clock = clock.Add(idle - time.Second)
		require.NoError(t, tc.writeData(ctx, seq, []byte("x"), false),
			"writeData failed mid-upload at seq %d", seq)
		seq++
		// One second past where the PREVIOUS activity would have expired. Without
		// the per-write bump this reaps immediately.
		clock = clock.Add(2 * time.Second)
		manager.sweepHalfClosed(clock, idle)
		require.NotNil(t, manager.get(connID),
			"an active upload must not be reaped mid-transfer (reaped at seq %d)", seq)
		require.False(t, tc.closed.Load(), "...nor closed (seq %d)", seq)
	}

	// Stop writing. Now the clock expires and the very next sweep reclaims it --
	// deterministically, with no polling.
	clock = clock.Add(idle + time.Second)
	manager.sweepHalfClosed(clock, idle)
	assert.Nil(t, manager.get(connID),
		"a half-closed conn must be reaped once writes stop and the idle window elapses")
	assert.True(t, tc.closed.Load(), "the reaper must close the conn it reaps")
	cancel()
	<-peerDone
}

// TestTunnelHalfCloseReaper_ChannelTeardownStillReapsImmediately re-asserts the
// third acceptance on top of TestTunnelReadLoopHalfCloseEvictsOnLifetimeCancel:
// channel teardown (lifetime-context cancellation) evicts a parked half-closed
// conn IMMEDIATELY via the per-conn watcher -- far faster than the idle deadline
// -- so the reaper is an addition to, not a replacement for, session-death
// reclamation.
//
// The idle window here is widened deliberately (vs withShortHalfCloseReaper's 60ms)
// so the "teardown beat the idle window" timing assertion has headroom: the
// lifetime watcher fires on context.AfterFunc (scheduler latency) and
// AssertEventually polls every ~10ms, so a 60ms margin is flake-prone under CI
// load. 250ms keeps the assertion decisive (the reaper cannot have run: a conn
// parked at mark time is not idle-reapable for the full window) while leaving
// comfortable room for scheduler + poll jitter.
func TestTunnelHalfCloseReaper_ChannelTeardownStillReapsImmediately(t *testing.T) {
	manager := newTunnelManager()
	// Override to a wider idle window with generous headroom for the timing check.
	origIdle, origInterval := halfCloseIdleTimeout, halfCloseSweepInterval
	halfCloseIdleTimeout = 250 * time.Millisecond
	halfCloseSweepInterval = 50 * time.Millisecond
	t.Cleanup(func() { halfCloseIdleTimeout, halfCloseSweepInterval = origIdle, origInterval })
	t.Cleanup(manager.Stop)
	const connID = "half-closed-teardown"
	workerConn, _, tc, cancel := halfCloseTarget(t, manager, connID, connID)
	t.Cleanup(func() { _ = workerConn.Close() })

	require.NotNil(t, manager.get(connID), "precondition: conn parked half-closed")
	start := time.Now()
	cancel() // session death
	testutil.AssertEventually(t, func() bool { return manager.get(connID) == nil },
		"channel teardown must evict a half-closed conn immediately, not wait for the idle reaper")
	assert.True(t, tc.closed.Load())
	// The conn was parked at mark time, so it is not idle-reapable until a full
	// idle window elapses. Asserting teardown finished well inside that window
	// proves the lifetime watcher (not the reaper) reclaimed it.
	assert.Less(t, time.Since(start), halfCloseIdleTimeout,
		"teardown must reclaim via the lifetime watcher, faster than the idle deadline")
}

// TestTunnelHalfCloseReaper_ReapsSequentialHalfCloses pins that the sweeper, once
// running, keeps reaping: after the first half-close is reaped, a SUBSEQUENT
// half-close on a different conn is also reaped within the idle window. The
// sweeper runs until Stop (it does not self-stop on an empty set), so this holds
// structurally; the test exists so a regression that stops the sweeper when the
// set drains (or otherwise fails to keep it alive) would let the second conn leak.
// The two conns use DISTINCT ids (conn_id collision is astronomically unlikely
// with nanoid-generated ids), so the case under test is sequential reaps, not
// conn_id reuse.
func TestTunnelHalfCloseReaper_ReapsSequentialHalfCloses(t *testing.T) {
	manager := newTunnelManager()
	withShortHalfCloseReaper(t, manager)

	for _, connID := range []string{"half-closed-A", "half-closed-B"} {
		workerConn, _, _, cancel := halfCloseTarget(t, manager, connID, connID)
		t.Cleanup(cancel)
		t.Cleanup(func() { _ = workerConn.Close() })
		require.NotNil(t, manager.get(connID), "conn %q parked half-closed", connID)
		testutil.AssertEventually(t, func() bool { return manager.get(connID) == nil },
			"half-closed conn %q must be reaped within the idle window", connID)
	}
}

// TestTunnelHalfCloseReaper_ReconcilesOutOfBandClose pins that a half-closed conn
// fully closed OUT-OF-BAND (here, by the lifetime watcher firing mid-park) has its
// halfClosed tracker entry dropped PROMPTLY by closeAndRemove's clearHalfClosed,
// without waiting for the sweeper's next tick and without the sweeper re-closing an
// already-closed conn. close() is idempotent, but the prompt drop keeps the
// halfClosed map from pinning a closed conn's *tunnelConn until the next tick.
func TestTunnelHalfCloseReaper_ReconcilesOutOfBandClose(t *testing.T) {
	manager := newTunnelManager()
	withShortHalfCloseReaper(t, manager)
	const connID = "half-closed-reconcile"
	workerConn, _, tc, cancel := halfCloseTarget(t, manager, connID, connID)
	t.Cleanup(func() { _ = workerConn.Close() })

	require.NotNil(t, manager.get(connID), "precondition: conn parked half-closed")
	// The conn is now in m.halfClosed (markHalfClosed) with a non-nil tracker.
	require.NotNil(t, tc.halfCloseTrack.Load(), "precondition: half-close tracker armed")

	// Out-of-band full close via the lifetime watcher: closeAndRemove closes the
	// conn, evicts from m.conns, AND (since this diff) drops the halfClosed entry.
	cancel()
	testutil.AssertEventually(t, func() bool { return manager.get(connID) == nil },
		"out-of-band close must evict from m.conns")
	require.True(t, tc.closed.Load(), "precondition: conn fully closed out-of-band")

	// closeAndRemove's clearHalfClosed must have dropped the tracker entry
	// already -- without waiting for the sweeper's next tick. Assert it directly
	// (the map is empty, the tracker pointer is nil) rather than via a sweep, so a
	// regression that removes the opportunistic cleanup fails here, not silently.
	manager.mu.Lock()
	_, present := manager.halfClosed[connID]
	manager.mu.Unlock()
	assert.False(t, present, "clearHalfClosed must drop the halfClosed entry on out-of-band close")
	assert.Nil(t, tc.halfCloseTrack.Load(), "clearHalfClosed must clear the conn's tracker pointer")
}

// TestRegisterAll_SetsTunnelManagerAndStopStopsSweeper covers the
// Shutdown->svc.tunnels.Stop() wiring added for this change, in two parts the
// full Shutdown path (which needs terminal/agent subsystems not relevant here)
// would obscure: (1) RegisterAll sets svc.tunnels, so Shutdown's nil-guarded
// `svc.tunnels.Stop()` branch is reachable; (2) that Stop on an armed manager
// actually joins the sweeper goroutine. Together they pin that an orderly
// Shutdown reaps the sweeper rather than orphaning it.
func TestRegisterAll_SetsTunnelManagerAndStopStopsSweeper(t *testing.T) {
	sqlDB := newServiceTestDB(t)
	svc := newMinimalService(t, sqlDB)
	d := channel.NewDispatcher()
	RegisterAll(d, svc)

	// (1) The wiring: RegisterAll assigns the worker-singleton tunnel manager.
	tunnels := svc.tunnels.Load()
	require.NotNil(t, tunnels, "RegisterAll must set svc.tunnels so Shutdown can Stop it")

	// (2) The behavior Shutdown relies on: Stop() on an armed manager joins the
	// sweeper goroutine. Arm it by parking a half-closed conn via the shared
	// halfCloseTarget helper (it drives the manager's read loop to EOF -> arm).
	const connID = "shutdown-reaper"
	target, _, _, _ := halfCloseTarget(t, tunnels, connID, connID)
	t.Cleanup(func() { _ = target.Close() })

	tunnels.mu.Lock()
	doneCh := tunnels.sweeper.done
	startedBefore := tunnels.sweeper.started
	tunnels.mu.Unlock()
	require.True(t, startedBefore && doneCh != nil, "precondition: sweeper armed by the half-close")

	tunnels.Stop() // the exact call Service.Shutdown makes
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tunnels.Stop() did not join the sweeper goroutine")
	}
	tunnels.mu.Lock()
	startedAfter := tunnels.sweeper.started
	stopped := tunnels.sweeper.stopped
	tunnels.mu.Unlock()
	assert.False(t, startedAfter, "Stop must clear sweeper.started")
	assert.True(t, stopped, "Stop must mark the sweeper stopped so it cannot restart")
}

// TestRegisterAllReassignmentStopsPriorManager pins that re-registering handlers
// does not orphan the prior tunnel manager's reaper goroutine: a second RegisterAll
// (a future reconnect/re-registration flow) must Stop the prior manager before
// reassigning svc.tunnels. Without the Stop, the prior manager's sweeper goroutine
// (if armed) leaks for the process lifetime -- the exact leak the reaper exists to
// prevent. RegisterAll on a manager whose sweeper never started (the common case
// for test re-registration) is a no-op Stop, so this stays behavior-preserving.
func TestRegisterAllReassignmentStopsPriorManager(t *testing.T) {
	sqlDB := newServiceTestDB(t)
	svc := newMinimalService(t, sqlDB)
	d := channel.NewDispatcher()
	RegisterAll(d, svc)
	prior := svc.tunnels.Load()
	require.NotNil(t, prior)

	// Arm the prior manager's sweeper so a leaked one would be observable.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	target, peer := net.Pipe()
	t.Cleanup(func() { _ = target.Close() })
	t.Cleanup(func() { _ = peer.Close() })
	const connID = "prior-reaper"
	tc := newTunnelConn(prior, connID, target, newTestWriter(), ctx)
	require.True(t, prior.beginOpen(connID, func() {}))
	require.True(t, prior.store(connID, tc))
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); tunnelReadLoop(prior, connID, tc) }()
	_ = peer.Close()
	<-loopDone
	prior.mu.Lock()
	doneCh := prior.sweeper.done
	prior.mu.Unlock()
	require.NotNil(t, doneCh, "precondition: prior manager's sweeper armed")

	// Re-register. The prior manager must be Stopped (its sweeper joined) and
	// svc.tunnels replaced with a fresh manager.
	RegisterAll(d, svc)
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("re-registration did not Stop the prior tunnel manager's sweeper (orphaned goroutine)")
	}
	require.NotSame(t, prior, svc.tunnels.Load(), "re-registration must replace svc.tunnels with a fresh manager")
	// The prior manager is now stopped: ensureRunning is a permanent no-op.
	prior.mu.Lock()
	stopped := prior.sweeper.stopped
	prior.mu.Unlock()
	assert.True(t, stopped, "the prior manager's sweeper must be marked stopped")
}

// TestTunnelStopDoesNotDeadlockVsSweep is a regression test for a deadlock where
// Stop held m.mu while waiting on the sweeper's done channel, but an in-flight
// sweep needed m.mu to observe the stop and exit. It hammers Stop() against an
// armed, fast-ticking sweeper; a regression that rejoins under m.mu hangs the
// test. Found by deep-review and confirmed under -race.
func TestTunnelStopDoesNotDeadlockVsSweep(t *testing.T) {
	orig := halfCloseSweepInterval
	halfCloseSweepInterval = time.Millisecond
	t.Cleanup(func() { halfCloseSweepInterval = orig })

	for i := 0; i < 500; i++ {
		manager := newTunnelManager()
		manager.mu.Lock()
		manager.sweeper.ensureRunning(manager)
		manager.mu.Unlock()
		// Let at least one tick land so the sweeper is likely mid-sweep.
		time.Sleep(2 * time.Millisecond)
		done := make(chan struct{})
		go func() { manager.Stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("Stop() deadlocked on iteration %d (Stop held m.mu across <-done while the sweeper blocked on m.mu)", i)
		}
	}
}

// TestTunnelSweepReapsUnderLockWithoutDeadlock pins that sweepHalfClosed reaps
// inline under m.mu -- closing and evicting each reaped conn while holding the
// lock -- without self-deadlocking against the conn's lifetime-cancellation
// AfterFunc callback. That callback (closeAndRemove -> clearHalfClosed) re-takes
// m.mu, so a sweep that held m.mu across a clearHalfClosed call would deadlock;
// the sweep instead detaches the tracker via detachHalfClosedLocked (which
// expects the caller to hold m.mu) and calls tc.close()+removeIf directly,
// neither of which re-enters m.mu. tc.close()'s stopCtxWatch() is
// context.AfterFunc's stop func, which does NOT block on an in-flight callback
// (verified), so holding m.mu across it is safe: a concurrent callback simply
// blocks on m.mu until the sweep releases it, then proceeds -- no cycle.
//
// This races a sweep against a lifetime-context cancellation (which fires the
// callback) and asserts both finish; a regression that routes the sweep's reap
// through clearAndRemove (re-entering m.mu) hangs here.
func TestTunnelSweepReapsUnderLockWithoutDeadlock(t *testing.T) {
	origInterval, origIdle := halfCloseSweepInterval, halfCloseIdleTimeout
	halfCloseSweepInterval = 5 * time.Millisecond
	halfCloseIdleTimeout = 5 * time.Millisecond
	t.Cleanup(func() { halfCloseSweepInterval, halfCloseIdleTimeout = origInterval, origIdle })

	for i := 0; i < 200; i++ {
		manager := newTunnelManager()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		target, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		t.Cleanup(func() { _ = target.Close() })
		tc := newTunnelConn(manager, "dlq", target, newTestWriter(), ctx)
		require.True(t, manager.beginOpen("dlq", func() {}))
		require.True(t, manager.store("dlq", tc))
		manager.markHalfClosed("dlq", tc)

		// Race: cancel the lifetime ctx (fires the AfterFunc -> closeAndRemove ->
		// clearHalfClosed, which needs m.mu) concurrent with a sweeper tick
		// reaping the same conn under m.mu.
		go cancel()

		// Drive one synchronous sweep on the test goroutine so the reap-under-lock
		// path is exercised deterministically, not just via the ticker.
		sweepDone := make(chan struct{})
		go func() {
			manager.sweepHalfClosed(time.Now(), halfCloseIdleTimeout)
			close(sweepDone)
		}()

		// Stop joins the sweeper goroutine; if anything self-deadlocked on m.mu,
		// Stop (which also needs m.mu via stopLocked) hangs.
		stopDone := make(chan struct{})
		go func() { manager.Stop(); close(stopDone) }()
		select {
		case <-sweepDone:
		case <-time.After(3 * time.Second):
			t.Fatalf("sweep did not complete on iteration %d (reap-under-lock self-deadlocked)", i)
		}
		select {
		case <-stopDone:
		case <-time.After(3 * time.Second):
			t.Fatalf("Stop hung on iteration %d (sweep held m.mu across a re-entrant clearHalfClosed/closeAndRemove)", i)
		}
	}
}

// TestTunnelClientCloseDropsHalfClosedTracker pins the fix for the gap where the
// client CloseTunnelConn path (closeConn -> cancel -> tc.close) did NOT call
// clearHalfClosed, leaving a stale halfClosed entry pinning the dead tc until the
// sweeper's next tick. Now closeConn clears the tracker, so the entry is gone
// immediately -- without waiting for a sweep.
func TestTunnelClientCloseDropsHalfClosedTracker(t *testing.T) {
	manager := newTunnelManager()
	// DO NOT shorten the idle window: the fix is that the entry is gone BEFORE any
	// sweep tick could fire. A long idle window makes a lingering entry
	// (the regression) detectable: the test would block on the assertion.
	origIdle, origInterval := halfCloseIdleTimeout, halfCloseSweepInterval
	halfCloseIdleTimeout = 10 * time.Second
	halfCloseSweepInterval = 10 * time.Second
	t.Cleanup(func() { halfCloseIdleTimeout, halfCloseSweepInterval = origIdle, origInterval })
	t.Cleanup(manager.Stop)

	const connID = "half-closed-client"
	target, peer, tc, cancel := halfCloseTarget(t, manager, connID, connID)
	t.Cleanup(func() { _ = target.Close() })
	t.Cleanup(func() { _ = peer.Close() })
	t.Cleanup(cancel)
	require.NotNil(t, tc.halfCloseTrack.Load(), "precondition: half-close tracker armed")

	// Simulate the client CloseTunnelConn's teardown: cancel (evict from conns) +
	// tc.close() + clearHalfClosed. This mirrors closeConn exactly.
	evicted := manager.cancel(connID)
	require.NotNil(t, evicted)
	evicted.close()
	manager.clearHalfClosed(connID, evicted)

	// The tracker entry must be gone IMMEDIATELY (no sweep has run -- the idle
	// window is 10s). Before the fix this assertion failed: the entry lingered.
	manager.mu.Lock()
	_, present := manager.halfClosed[connID]
	manager.mu.Unlock()
	assert.False(t, present, "client close must drop the halfClosed entry via clearHalfClosed, not wait for a sweep")
	assert.Nil(t, tc.halfCloseTrack.Load(), "client close must clear the conn's tracker pointer")
}

// TestTunnelClearHalfClosedIsNoOpForNeverMarkedConn pins the lock-free fast path
// in clearHalfClosed: a conn that was never marked half-closed has a nil tracker,
// so clearHalfClosed must short-circuit on a single atomic load without touching
// m.mu (and without leaving anything in m.halfClosed). Most conns never half-close,
// so this is the close hot path; a regression that drops the fast path takes m.mu
// on every full close.
func TestTunnelClearHalfClosedIsNoOpForNeverMarkedConn(t *testing.T) {
	manager := newTunnelManager()
	t.Cleanup(manager.Stop)
	const connID = "never-marked"
	tc := &tunnelConn{}

	// The conn was never markHalfClosed'd, so its tracker is nil and it is not in
	// m.halfClosed.
	require.Nil(t, tc.halfCloseTrack.Load())

	// clearHalfClosed must be a harmless no-op that changes no state.
	assert.NotPanics(t, func() { manager.clearHalfClosed(connID, tc) })

	manager.mu.Lock()
	_, present := manager.halfClosed[connID]
	manager.mu.Unlock()
	assert.False(t, present, "a clear on a never-marked conn must not insert into m.halfClosed")
	assert.Nil(t, tc.halfCloseTrack.Load(), "the conn's tracker stays nil")
}

// TestTunnelSweepDetachesTrackerAndMapTogether pins the detach invariant
// detachHalfClosedLocked owns: when the sweeper reaps a conn, BOTH the
// m.halfClosed map slot AND the conn's halfCloseTrack pointer are cleared in the
// same step (the conn is then closed+evicted under the same lock). A regression
// that clears one without the other orphans either a dangling map entry (pinning
// tc) or a stale tracker pointer (so writeData keeps bumping a dead entry).
func TestTunnelSweepDetachesTrackerAndMapTogether(t *testing.T) {
	manager := newTunnelManager()
	// Pin the stamp side of the reap decision to a FIXED instant, and take the
	// sweep's deadline from that same instant below, so whether the conn is reaped
	// never depends on the host clock advancing between markHalfClosed and the
	// sweep. It does not advance on Windows: the wall clock there ticks at the
	// system timer granularity (~0.5-16ms) despite its 100ns unit, so two
	// time.Now() calls microseconds apart return the SAME UnixNano -- the sweep's
	// `lastActivity < now` was then false, nothing was reaped, and all four
	// assertions below failed for a reason this test is not about. The clock is
	// never reassigned, so the background sweeper reading it concurrently is safe
	// (and a no-op: with the entry stamped at base, base-idle is never past it).
	base := time.Now()
	manager.now = func() time.Time { return base }
	withShortHalfCloseReaper(t, manager)
	const connID = "sweep-detach"
	workerConn, _, tc, cancel := halfCloseTarget(t, manager, connID, connID)
	defer cancel()
	t.Cleanup(func() { _ = workerConn.Close() })
	require.NotNil(t, tc.halfCloseTrack.Load(), "precondition: half-close tracker armed")

	// Drive one sweep from a clock a second past the entry's stamp, with a zero
	// idle window, so the conn is reaped; then assert the detach was total (map
	// slot gone, pointer nil, conn closed, evicted).
	manager.sweepHalfClosed(base.Add(time.Second), 0)

	manager.mu.Lock()
	_, present := manager.halfClosed[connID]
	manager.mu.Unlock()
	assert.False(t, present, "sweep must delete the reaped conn's halfClosed entry")
	assert.Nil(t, tc.halfCloseTrack.Load(), "sweep must clear the reaped conn's tracker pointer")
	assert.True(t, tc.closed.Load(), "sweep must close the reaped conn")
	assert.Nil(t, manager.get(connID), "sweep must evict the reaped conn from m.conns")
}

// TestTunnelMarkHalfClosedClearsStaleEntry pins markHalfClosed's pointer-identity
// guard: if a stale halfClosed entry for the same conn_id survived (the prior conn
// was never cleared before this mark, e.g. by a buggy/hostile client reusing a
// conn_id), marking a NEW conn clears the old conn's tracker pointer so its
// writeData stops bumping a dead entry.
func TestTunnelMarkHalfClosedClearsStaleEntry(t *testing.T) {
	manager := newTunnelManager()
	t.Cleanup(manager.Stop)
	const connID = "reuse-id"
	// Bare tunnelConns suffice: markHalfClosed touches only the halfCloseTrack
	// pointer and the map entry, never read-side state like closed/gate/credit.
	old := &tunnelConn{}
	fresh := &tunnelConn{}

	manager.markHalfClosed(connID, old)
	require.Same(t, old, old.halfCloseTrack.Load().tc, "precondition: old conn's tracker set")

	manager.markHalfClosed(connID, fresh)

	assert.Nil(t, old.halfCloseTrack.Load(), "markHalfClosed must clear a stale entry's tracker pointer")
	manager.mu.Lock()
	entry := manager.halfClosed[connID]
	manager.mu.Unlock()
	require.NotNil(t, entry)
	assert.Same(t, fresh, entry.tc, "the map must point at the fresh conn after a reuse-mark")
}

// writeGate.waitTurn must NAK a provably-illegitimate seq -- a duplicate/replay behind
// the gate, or one further ahead than the client's bounded send window can reach
// -- instead of parking the handler goroutine (and pinning a client send-window
// slot) until the graceful-close timeout. Defense-in-depth against a buggy or
// compromised authenticated tunnel owner. A benign in-window out-of-order seq
// still parks and resolves when the gate advances, so a correct client is never
// rejected.
func TestWaitWriteTurnRejectsIllegitimateSeq(t *testing.T) {
	tc := newTunnelConn(nil, "", nil, nil, nil)

	// Far ahead of the send window: NAKed (rejected, not a conn-closed race), not parked.
	assert.Equal(t, writeTurnRejected, tc.writeGate.waitTurn(tunnelflow.MaxWriteSeqLookahead+1),
		"a seq beyond the client's send window must be NAKed, not parked")

	// Advance the gate, then a seq BEHIND it (an already-applied replay) is NAKed.
	tc.writeGate.advance() // nextWriteSeq = 1
	assert.Equal(t, writeTurnRejected, tc.writeGate.waitTurn(0),
		"a seq behind the gate (already applied) must be NAKed")

	// A legitimate in-window seq parks until its turn -- proving the bound never
	// false-rejects a correct client's out-of-order arrival.
	done := make(chan writeTurn, 1)
	go func() { done <- tc.writeGate.waitTurn(2) }() // nextWriteSeq=1, seq=2 is next+1
	select {
	case <-done:
		t.Fatal("writeGate.waitTurn returned before its turn for an in-window seq")
	case <-time.After(50 * time.Millisecond):
	}
	tc.writeGate.advance() // nextWriteSeq = 2 -> unblocks seq 2
	select {
	case got := <-done:
		assert.Equal(t, writeTurnProceed, got, "an in-window seq must proceed once its turn arrives")
	case <-time.After(time.Second):
		t.Fatal("writeGate.waitTurn did not proceed after its turn arrived")
	}
}

// A close for a conn that is neither open nor in-flight (the common case: the
// target EOFs and the read loop removes the conn before the client's
// CloseTunnelConn arrives) must not leak a permanent canceled marker. conn_ids
// are unique, so no later beginOpen/store clears it; the sweep bound added to
// cancel must reclaim it.
func TestTunnelManagerCancelSweepsStaleMarker(t *testing.T) {
	manager := newTunnelManager()
	originalTTL := staleCancelMarkerTTL
	staleCancelMarkerTTL = 20 * time.Millisecond
	t.Cleanup(func() { staleCancelMarkerTTL = originalTTL })

	// Read canceled under manager.mu: sweepCanceledLocked deletes the marker
	// under the same lock, so an unlocked poll would race the sweep's map write.
	hasMarker := func(connID string) bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		ok := manager.canceled.has(connID)
		return ok
	}

	require.Nil(t, manager.cancel("already-gone"))
	require.True(t, hasMarker("already-gone"), "cancel records a marker for a racing open")

	// The marker is GC'd opportunistically -- once its TTL elapses, the next
	// cancel/beginOpen sweeps it under the manager mutex, with no per-marker
	// timer or background goroutine.
	time.Sleep(2 * staleCancelMarkerTTL)
	require.Nil(t, manager.cancel("unrelated")) // touches the manager, triggering the sweep
	assert.False(t, hasMarker("already-gone"),
		"the stale marker is swept on the first cancel/beginOpen after its TTL")
}

// openTestTunnelConn opens a conn to a fresh echo server and returns its conn_id,
// so a test that needs a LIVE conn (the size gate now lives past the conn lookup,
// inside the write gate) does not restate the open.
func openTestTunnelConn(t *testing.T, d *channel.Dispatcher, connID string) {
	t.Helper()
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)
	w := newTestWriter()
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     connID,
		TargetAddr: host,
		TargetPort: port,
	}, w)
	require.Empty(t, w.errors, "precondition: the tunnel conn must open")
	require.Len(t, w.responses, 1)
}

// The worker must enforce the per-frame chunk bound itself, not trust the sender to
// have split.
//
// Conn.Write splitting at MaxChunkBytes is what makes the send window a BYTE
// bound rather than a frame-count bound -- but that is the client's convention. A
// client that does not split is otherwise capped only by the channel's
// inner-message limit, and the write gate parks up to tunnelflow.MaxWriteSeqLookahead (256)
// in-flight seqs per conn, so an unchecked handler admits ~4 GiB of buffered payload
// on the worker per conn. The gate already NAKs an out-of-window seq for exactly this
// "a compromised owner must not park the worker" reason; the byte bound belongs
// beside it.
func TestSendTunnelData_RejectsOversizeFrame(t *testing.T) {
	_, d, _ := tunnelTestSetup(t)
	const connID = "oversize-frame"
	openTestTunnelConn(t, d, connID)

	w := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Seq:    0,
		Data:   make([]byte, tunnelflow.MaxChunkBytes+1),
	}, w)

	require.Len(t, w.errors, 1, "an oversize frame must be refused")
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
	assert.Contains(t, w.errors[0].message, "chunk bound")
	assert.Empty(t, w.responses, "a refused frame must not be applied")
}

// A provably-illegitimate write seq (a replay behind the gate, or one beyond the
// lookahead window) must be NAKed to the CLIENT as FailedPrecondition, not reported
// as a worker-side INTERNAL fault. Collapsing the gate's rejection into net.ErrClosed
// made sendData surface a client protocol violation as `internal: write: closed
// network connection` (which reads as a retryable worker fault) and log it at Error
// level -- an operator-log amplification vector a misbehaving peer can drive. Only a
// genuine conn-teardown race stays INTERNAL.
func TestSendTunnelData_RejectsIllegitimateSeqAsClientNAK(t *testing.T) {
	_, d, _ := tunnelTestSetup(t)
	const connID = "seq-nak"
	openTestTunnelConn(t, d, connID)

	// Apply seq 0 so the gate advances to 1; seq 0 is now a replay of an applied frame.
	first := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{ConnId: connID, Seq: 0, Data: []byte("hi")}, first)
	require.Empty(t, first.errors, "the first in-order frame must apply")

	replay := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{ConnId: connID, Seq: 0, Data: []byte("dup")}, replay)
	require.Len(t, replay.errors, 1, "a replayed write seq must be NAKed")
	assert.Equal(t, codeFailedPrecondition, replay.errors[0].code,
		"a replayed write seq is a client protocol violation, not a worker INTERNAL fault")
	assert.Empty(t, replay.responses)

	// A seq beyond the lookahead window is the same class of violation.
	beyond := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{ConnId: connID, Seq: tunnelflow.MaxWriteSeqLookahead + 5, Data: []byte("x")}, beyond)
	require.Len(t, beyond.errors, 1, "a seq beyond the lookahead window must be NAKed")
	assert.Equal(t, codeFailedPrecondition, beyond.errors[0].code,
		"a beyond-window write seq must NAK the client, not report a worker INTERNAL fault")
}

// A genuine conn-teardown race (writeSeqGate.waitTurn returns writeTurnClosed, the
// errWriteSeqRejected doc's "stays internal" exception) must NOT log at Error level.
// On a busy tunnel whose channels churn normally -- reconnects, credential rotations,
// pool evictions -- every SendTunnelData that races teardown would otherwise emit one
// Error per racing frame, spamming the operator log with expected events. The race is
// not reachable through a normal CloseTunnelConn (which sets closed.Load() and
// short-circuits sendData to NotFound before writeData runs), so the test exercises
// the classifier directly with the net.ErrClosed writeData returns from the gate's
// writeTurnClosed arm.
func TestClassifyTunnelWriteError_TeardownRaceIsInternalButNotErrorLogged(t *testing.T) {
	var logMu sync.Mutex
	logs := map[slog.Level]int{}
	logger := slog.New(&levelCountingHandler{mu: &logMu, counts: logs})
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	w := newTestWriter()
	classifyTunnelWriteError(net.ErrClosed, "conn-race", 7, w)

	require.Len(t, w.errors, 1, "the teardown race surfaces the documented internal code")
	assert.Equal(t, int32(13), w.errors[0].code, "a teardown race stays internal per the writeSeqGate doc (INTERNAL = 13)")

	logMu.Lock()
	defer logMu.Unlock()
	assert.Zero(t, logs[slog.LevelError],
		"an expected teardown race must not log at Error (it would spam the operator log on a churning tunnel)")
	assert.GreaterOrEqual(t, logs[slog.LevelDebug], 1, "a teardown race logs at Debug so the operator log stays quiet")
}

// A generic worker-fault write error (not a teardown race, not a client NAK) still
// logs at Error and surfaces Internal -- the teardown-race Debug downgrade must not
// silence genuine faults.
func TestClassifyTunnelWriteError_GenericFaultIsErrorLogged(t *testing.T) {
	var logMu sync.Mutex
	logs := map[slog.Level]int{}
	logger := slog.New(&levelCountingHandler{mu: &logMu, counts: logs})
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	w := newTestWriter()
	classifyTunnelWriteError(errors.New("target disk full"), "conn-fault", 1, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(13), w.errors[0].code, "a genuine write fault surfaces Internal")
	logMu.Lock()
	defer logMu.Unlock()
	assert.GreaterOrEqual(t, logs[slog.LevelError], 1, "a genuine write fault logs at Error")
}

// levelCountingHandler is a minimal slog.Handler that tallies records by level
// behind a caller-supplied mutex, for tests that assert log levels.
type levelCountingHandler struct {
	mu     *sync.Mutex
	counts map[slog.Level]int
}

func (h *levelCountingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *levelCountingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counts[r.Level]++
	return nil
}

func (h *levelCountingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *levelCountingHandler) WithGroup(_ string) slog.Handler      { return h }

// ...and refusing it must not strand the conn's write-seq gate.
//
// The bound used to be checked in the handler ABOVE the conn lookup, so a NAKed seq
// never reached writeData and never ran its writeGate.advance defer: the gate stuck at
// that seq forever. Every subsequent frame on the conn then parked in waitTurn (one
// dispatcher goroutine each) until the lookahead bound started NAKing too -- one
// permanently wedged conn per oversize frame, recoverable only by burning a full
// gracefulCloseTimeout on a seq that can never arrive, or by session teardown.
//
// Against the old code the seq-1 dispatch below hangs and this test fails on the
// timeout.
func TestSendTunnelData_OversizeFrameDoesNotWedgeConn(t *testing.T) {
	_, d, _ := tunnelTestSetup(t)
	const connID = "wedge-check"
	openTestTunnelConn(t, d, connID)

	oversize := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Seq:    0,
		Data:   make([]byte, tunnelflow.MaxChunkBytes+1),
	}, oversize)
	require.Len(t, oversize.errors, 1)
	require.Equal(t, codeInvalidArgument, oversize.errors[0].code)

	// The NAK consumed seq 0's turn, so seq 1 must be writable. Dispatch it off the
	// test goroutine so a stranded gate surfaces as a bounded failure rather than
	// hanging the whole package.
	next := newTestWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
			ConnId: connID,
			Seq:    1,
			Data:   []byte("hello"),
		}, next)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a NAKed oversize frame stranded the conn's write-seq gate: seq 1 never got its turn")
	}
	assert.Empty(t, next.errors, "the frame after a NAKed oversize one must be written")
	assert.Len(t, next.responses, 1)
}

// grantReadCredit is registered behind registerConnHandler's shared conn_id prelude,
// so the contract is testable on the method directly -- no dispatcher required. An
// empty conn_id must never reach the manager's maps.
func TestGrantTunnelReadCredit_RequiresConnID(t *testing.T) {
	_, d, w := tunnelTestSetup(t)

	dispatch(d, "GrantTunnelReadCredit", &leapmuxv1.GrantTunnelReadCreditRequest{
		Credit: 4,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
	assert.Contains(t, w.errors[0].message, "conn_id is required")
	assert.Empty(t, w.responses)
}

// The extracted handler is callable directly, which is the seam registerConnHandler
// buys: a grant for a conn the manager never knew is a benign race (the read loop
// ended and evicted it), so it acks rather than erroring.
func TestTunnelManagerGrantReadCredit_UnknownConnAcks(t *testing.T) {
	manager := newTunnelManager()
	w := newTestWriter()

	manager.grantReadCredit(context.Background(), userid.MustNew("user-1"), &leapmuxv1.GrantTunnelReadCreditRequest{
		ConnId: "never-opened",
		Credit: 4,
	}, w)

	assert.Empty(t, w.errors, "a grant for an evicted conn is a benign race, not a failure")
	assert.Len(t, w.responses, 1)
}

// ...and a frame at exactly the bound is accepted: the check must not be off by one
// against the size a correct client actually emits.
func TestSendTunnelData_AcceptsFrameAtChunkBound(t *testing.T) {
	_, d, w := tunnelTestSetup(t)

	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: "conn-missing",
		Seq:    0,
		Data:   make([]byte, tunnelflow.MaxChunkBytes),
	}, w)

	// The conn does not exist, so this gets NOT_FOUND -- proving it passed the size
	// gate rather than being rejected by it.
	require.Len(t, w.errors, 1)
	assert.Equal(t, codeNotFound, w.errors[0].code,
		"a frame at exactly the bound must reach the conn lookup, not be size-refused")
}

// A cancel marker must be expirable only by its OWN deadline. A conn_id that is
// marked, cleared early by beginOpen, then re-marked leaves the first marker's
// expiry entry orphaned in canceledExpiry; sweeping on conn_id alone let that
// orphan drop the SECOND, live marker as soon as the FIRST entry's TTL elapsed --
// up to a full TTL early -- un-fencing the close so a later beginOpen for that id
// would open a target conn the client had already given up on.
func TestTunnelManagerSweepKeepsReMarkedCancelMarker(t *testing.T) {
	manager := newTunnelManager()
	originalTTL := staleCancelMarkerTTL
	staleCancelMarkerTTL = 60 * time.Millisecond
	t.Cleanup(func() { staleCancelMarkerTTL = originalTTL })

	hasMarker := func(connID string) bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		ok := manager.canceled.has(connID)
		return ok
	}

	// First mark (no in-flight open) appends an expiry entry for "reused".
	require.Nil(t, manager.cancel("reused"))
	require.True(t, hasMarker("reused"))

	// beginOpen clears the marker but leaves its expiry entry orphaned.
	require.False(t, manager.beginOpen("reused", func() {}),
		"beginOpen is fenced by the marker and clears it")
	require.False(t, hasMarker("reused"))

	// Re-mark near the end of the first entry's TTL, so the orphan expires while
	// the new marker is still well within its own.
	time.Sleep(staleCancelMarkerTTL - 20*time.Millisecond)
	require.Nil(t, manager.cancel("reused"))
	require.True(t, hasMarker("reused"))

	// Let the ORPHANED first entry expire; the second marker's own TTL has not.
	time.Sleep(30 * time.Millisecond)
	require.Nil(t, manager.cancel("unrelated")) // touches the manager, triggering the sweep
	assert.True(t, hasMarker("reused"),
		"the orphaned entry must not expire the live marker that replaced it")

	// The live marker still expires on its own deadline.
	time.Sleep(2 * staleCancelMarkerTTL)
	require.Nil(t, manager.cancel("unrelated-2"))
	assert.False(t, hasMarker("reused"), "the live marker expires by its own TTL")
}

// A close for an in-flight open must NOT schedule a sweep -- the marker is
// cleared deterministically by store/abortOpen, so it must not depend on the
// TTL (which would briefly leave the conn unfenced near the end of the window).
func TestTunnelManagerCancelInFlightOpenMarkerClearsOnStore(t *testing.T) {
	manager := newTunnelManager()
	require.True(t, manager.beginOpen("in-flight", func() {}))
	require.Nil(t, manager.cancel("in-flight"))
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	tc := newTunnelConn(nil, "", client, nil, nil)
	// store clears the marker even though the close won the race.
	require.False(t, manager.store("in-flight", tc))
	require.True(t, tc.closed.Load())
	present := manager.canceled.has("in-flight")
	require.False(t, present, "store must clear the in-flight cancel marker")
}

func TestOpenTunnelConn_InvalidTargetPort(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "invalid-port",
		TargetAddr: "127.0.0.1",
		TargetPort: 0,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(3), w.errors[0].code, "expected INVALID_ARGUMENT")
}

func TestSendTunnelData_HappyPath(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "send-data",
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	w2 := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("hello"),
	}, w2)

	require.Len(t, w2.errors, 0, "expected no errors")
	require.Len(t, w2.responses, 1, "expected success response")
}

func TestSendTunnelData_UnknownConnID(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: "nonexistent",
		Data:   []byte("hello"),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(5), w.errors[0].code, "expected NOT_FOUND")
}

func TestCloseTunnelConn_HappyPath(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "close-happy",
		TargetAddr: host,
		TargetPort: port,
	}, w)

	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	w2 := newTestWriter()
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
		ConnId: connID,
	}, w2)

	require.Len(t, w2.errors, 0, "expected no errors")
	require.Len(t, w2.responses, 1, "expected success response")

	// Subsequent SendTunnelData should fail.
	w3 := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("hello"),
	}, w3)
	require.Len(t, w3.errors, 1)
	assert.Equal(t, int32(5), w3.errors[0].code, "expected NOT_FOUND after close")
}

func TestCloseTunnelConn_UnknownConnIDIsIdempotent(t *testing.T) {
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
		ConnId: "nonexistent",
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
}

func TestTunnelTargetEOF(t *testing.T) {
	// Server writes "hello" then closes.
	serverAddr := testutil.StartWriteThenCloseServer(t, []byte("hello"))
	host, port := testutil.ParseAddr(serverAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "target-eof",
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))

	// Wait for stream messages (data + EOF).
	testutil.AssertEventually(t, func() bool {
		data, hasEOF := collectTunnelEvents(w.streamsSnapshot())
		return len(data) > 0 && hasEOF
	}, "expected both data and EOF stream messages")

	data, hasEOF := collectTunnelEvents(w.streamsSnapshot())
	assert.Equal(t, "hello", string(data), "expected data stream message")
	assert.True(t, hasEOF, "expected EOF stream message")
}

// collectTunnelEvents decodes stream payloads and returns the concatenated
// data bytes plus whether any event signalled EOF.
func collectTunnelEvents(streams []*leapmuxv1.InnerStreamMessage) (data []byte, hasEOF bool) {
	for _, s := range streams {
		var event leapmuxv1.TunnelConnEvent
		if proto.Unmarshal(s.GetPayload(), &event) != nil {
			continue
		}
		if len(event.GetData()) > 0 {
			data = append(data, event.GetData()...)
		}
		if event.GetEof() {
			hasEOF = true
		}
	}
	return
}

func TestTunnelConcurrentConnections(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, _ := tunnelTestSetup(t)

	const numConns = 10
	var wg sync.WaitGroup
	connIDs := make([]string, numConns)

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := newTestWriter()
			dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
				ConnId:     fmt.Sprintf("concurrent-%d", idx),
				TargetAddr: host,
				TargetPort: port,
			}, w)

			require.Len(t, w.responses, 1)
			var resp leapmuxv1.OpenTunnelConnResponse
			require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
			connIDs[idx] = resp.GetConnId()
		}(i)
	}
	wg.Wait()

	// All conn_ids should be unique.
	idSet := make(map[string]bool)
	for _, id := range connIDs {
		assert.NotEmpty(t, id)
		assert.False(t, idSet[id], "duplicate conn_id: %s", id)
		idSet[id] = true
	}

	// Close all connections.
	for _, connID := range connIDs {
		w := newTestWriter()
		dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
			ConnId: connID,
		}, w)
		require.Len(t, w.errors, 0)
	}
}

func TestTunnelEchoIntegration(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "echo-integration",
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	// Send data.
	w2 := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("echo test"),
	}, w2)
	require.Len(t, w2.errors, 0)

	// Wait for echo response via stream.
	testutil.AssertEventually(t, func() bool {
		echoed, _ := collectTunnelEvents(w.streamsSnapshot())
		return len(echoed) >= len("echo test")
	}, "expected echoed data via stream")

	echoed, _ := collectTunnelEvents(w.streamsSnapshot())
	assert.Equal(t, "echo test", string(echoed), "expected echoed data")

	// Clean up.
	w3 := newTestWriter()
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
		ConnId: connID,
	}, w3)
	require.Len(t, w3.errors, 0)
}

func TestSendTunnelData_AfterClose(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "after-close",
		TargetAddr: host,
		TargetPort: port,
	}, w)

	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	// Close the connection.
	w2 := newTestWriter()
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{ConnId: connID}, w2)
	require.Len(t, w2.errors, 0)

	// Sending data after close should fail.
	w3 := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("should fail"),
	}, w3)
	require.Len(t, w3.errors, 1)
	assert.Equal(t, int32(5), w3.errors[0].code, "expected NOT_FOUND after close")
}

func TestTunnelLargeDataTransfer(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "large-transfer",
		TargetAddr: host,
		TargetPort: port,
	}, w)

	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	// Send 1 MB of data in 32 KB chunks.
	totalSize := 1024 * 1024
	chunkSize := 32 * 1024
	chunk := make([]byte, chunkSize)
	for i := range chunk {
		chunk[i] = byte(i % 256)
	}

	var seq uint64
	for sent := 0; sent < totalSize; sent += chunkSize {
		w2 := newTestWriter()
		dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
			ConnId: connID,
			Data:   chunk,
			Seq:    seq,
		}, w2)
		require.Len(t, w2.errors, 0, "send chunk at offset %d failed", sent)
		seq++
	}

	// Wait for the full echo to come back via stream, replenishing read credit
	// as frames arrive so the worker's read window (tunnelflow.InitialReadWindow) never
	// stalls this multi-frame transfer -- modelling a client that grants credit
	// as it consumes (what Conn.Read does automatically).
	granted := 0
	totalReceived := func() int {
		frames := w.streamsSnapshot()
		if len(frames) > granted {
			gw := newTestWriter()
			dispatch(d, "GrantTunnelReadCredit", &leapmuxv1.GrantTunnelReadCreditRequest{
				ConnId: connID,
				Credit: uint64(len(frames) - granted),
			}, gw)
			granted = len(frames)
		}
		echoed, _ := collectTunnelEvents(frames)
		return len(echoed)
	}
	testutil.AssertEventually(t, func() bool {
		return totalReceived() >= totalSize
	}, "expected all echoed data via stream")
	assert.Equal(t, totalSize, totalReceived(), "expected all data echoed back")

	w3 := newTestWriter()
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{ConnId: connID}, w3)
	require.Len(t, w3.errors, 0)
}

func TestTunnelMultipleSequentialConnections(t *testing.T) {
	echoAddr := testutil.StartEchoServer(t)
	host, port := testutil.ParseAddr(echoAddr)

	_, d, _ := tunnelTestSetup(t)

	for i := 0; i < 5; i++ {
		w := newTestWriter()
		dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
			ConnId:     fmt.Sprintf("sequential-%d", i),
			TargetAddr: host,
			TargetPort: port,
		}, w)
		require.Len(t, w.errors, 0, "open %d failed", i)
		require.Len(t, w.responses, 1)

		var openResp leapmuxv1.OpenTunnelConnResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
		connID := openResp.GetConnId()

		// Send and verify data.
		w2 := newTestWriter()
		dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
			ConnId: connID,
			Data:   []byte(fmt.Sprintf("msg-%d", i)),
		}, w2)
		require.Len(t, w2.errors, 0)

		// Close.
		w3 := newTestWriter()
		dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{ConnId: connID}, w3)
		require.Len(t, w3.errors, 0)
	}
}

func TestTunnelHalfClose_TargetClosesFirst(t *testing.T) {
	// Start a server that reads one message, echoes it, then closes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				buf := make([]byte, 1024)
				n, _ := conn.Read(buf)
				if n > 0 {
					_, _ = conn.Write(buf[:n])
				}
				// Close after echoing.
			}()
		}
	}()

	host, port := testutil.ParseAddr(ln.Addr().String())

	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "half-close",
		TargetAddr: host,
		TargetPort: port,
	}, w)

	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	// Send data.
	w2 := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: connID,
		Data:   []byte("half-close-test"),
	}, w2)
	require.Len(t, w2.errors, 0)

	// Wait for echo + EOF.
	testutil.AssertEventually(t, func() bool {
		data, hasEOF := collectTunnelEvents(w.streamsSnapshot())
		return len(data) > 0 && hasEOF
	}, "expected echo data followed by EOF")

	data, hasEOF := collectTunnelEvents(w.streamsSnapshot())
	assert.Equal(t, "half-close-test", string(data), "expected echo data")
	assert.True(t, hasEOF, "expected EOF after target closes")
}

// When the TARGET half-closes its write side (FIN -> the worker read loop sees
// io.EOF) but keeps its read side open, the client must still be able to finish
// uploading: the worker must not full-close/evict the conn on target EOF, or
// those writes are NAKed as "connection not found" and the client->target
// upload is truncated. This is the target->client mirror of the half-close the
// port-forward/SOCKS5 copy propagates.
func TestTunnelReadLoop_TargetHalfCloseKeepsClientUploadOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	received := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		tcpConn := conn.(*net.TCPConn)
		buf := make([]byte, 1024)
		n, _ := tcpConn.Read(buf)
		first := string(buf[:n])
		// Half-close the write side (send FIN) but keep reading the rest.
		_ = tcpConn.CloseWrite()
		rest, _ := io.ReadAll(tcpConn)
		received <- first + string(rest)
	}()

	host, port := testutil.ParseAddr(ln.Addr().String())
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId: "half-open", TargetAddr: host, TargetPort: port,
	}, w)
	require.Len(t, w.responses, 1)

	// First upload chunk (seq 0).
	w0 := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: "half-open", Data: []byte("part1"), Seq: 0,
	}, w0)
	require.Len(t, w0.errors, 0)

	// The target's FIN must surface to the client as an EOF event.
	testutil.AssertEventually(t, func() bool {
		_, hasEOF := collectTunnelEvents(w.streamsSnapshot())
		return hasEOF
	}, "expected EOF event from the target half-close")

	// Client keeps uploading AFTER the target half-closed (seq 1). Pre-fix this
	// was rejected because the conn had already been full-closed and evicted.
	w1 := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId: "half-open", Data: []byte("part2"), Seq: 1,
	}, w1)
	require.Len(t, w1.errors, 0, "upload after target half-close must not be NAKed")

	// Graceful close flushes the upload, then closes the target so ReadAll returns.
	wc := newTestWriter()
	dispatch(d, "CloseTunnelConn", &leapmuxv1.CloseTunnelConnRequest{
		ConnId: "half-open", Seq: 2,
	}, wc)

	select {
	case got := <-received:
		assert.Equal(t, "part1part2", got, "target must receive the full upload across its half-close")
	case <-time.After(2 * time.Second):
		t.Fatal("target never received the full upload after its half-close")
	}
}

// credit.Grant clamps to tunnelflow.InitialReadWindow so a buggy or hostile owner cannot
// accumulate an unbounded window that starves the shared channel. A correct client
// never grants past the window, so the clamp cannot under-credit a well-behaved stream.
func TestTunnelConnAddReadCredit_ClampsToWindow(t *testing.T) {
	tc := newTunnelConn(nil, "", nil, nil, nil)
	require.Equal(t, tunnelflow.InitialReadWindow, tc.credit.Available())

	// Drain the whole seeded window.
	for i := 0; i < tunnelflow.InitialReadWindow; i++ {
		require.NoError(t, tc.credit.Acquire(context.Background()))
	}
	require.Equal(t, 0, tc.credit.Available())

	// An overflowing grant must not leave the window negative: it clamps to the
	// ceiling so the read loop keeps flowing.
	tc.credit.Grant(^uint64(0))
	require.Equal(t, tunnelflow.InitialReadWindow, tc.credit.Available())
	require.NoError(t, tc.credit.Acquire(context.Background()), "read loop must not be wedged by an overflowing grant")

	// A large-but-positive over-grant also clamps to the window.
	tc.credit.Grant(uint64(tunnelflow.InitialReadWindow) * 10)
	require.Equal(t, tunnelflow.InitialReadWindow, tc.credit.Available())

	// A normal grant that stays within the window is applied verbatim, so the
	// clamp never under-credits a well-behaved stream.
	require.NoError(t, tc.credit.Acquire(context.Background()))
	require.NoError(t, tc.credit.Acquire(context.Background()))
	require.Equal(t, tunnelflow.InitialReadWindow-2, tc.credit.Available())
	tc.credit.Grant(1)
	require.Equal(t, tunnelflow.InitialReadWindow-1, tc.credit.Available())
}

// A SendTunnelData with close_write=true must write the data AND half-close the
// target's write side, so a target that reads until EOF receives the full
// request and then observes read-EOF from the client's FIN. This is the
// client->target half-close direction the desktop port-forward/SOCKS5 copy
// forwards; without it the target never sees the client's write-close and a
// request/response protocol that delimits its request with a FIN hangs.
func TestSendTunnelData_HalfCloseForwardsClientEOF(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	serverGot := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// ReadAll returns only once the client half-closes its write side.
		data, _ := io.ReadAll(conn)
		serverGot <- data
	}()

	host, port := testutil.ParseAddr(ln.Addr().String())
	_, d, w := tunnelTestSetup(t)
	dispatch(d, "OpenTunnelConn", &leapmuxv1.OpenTunnelConnRequest{
		ConnId:     "half-close-fwd",
		TargetAddr: host,
		TargetPort: port,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTunnelConnResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	connID := openResp.GetConnId()

	const request = "GET / HTTP/1.0\r\n\r\n"
	w2 := newTestWriter()
	dispatch(d, "SendTunnelData", &leapmuxv1.SendTunnelDataRequest{
		ConnId:     connID,
		Data:       []byte(request),
		CloseWrite: true,
	}, w2)
	require.Len(t, w2.errors, 0)

	select {
	case got := <-serverGot:
		assert.Equal(t, request, string(got),
			"target must receive the full request then read-EOF from the client half-close")
	case <-time.After(2 * time.Second):
		t.Fatal("target never observed the client's write-half-close (read-EOF)")
	}
}

// writeData must apply a conn's frames to the target in seq order even when the
// worker dispatches them out of order (each SendTunnelData runs on its own
// goroutine). Here seq 1 is submitted and blocks in the gate until seq 0 is
// applied, so the target sees "zero" before "one" -- not the submission order.
func TestTunnelConnWriteDataAppliesInSeqOrder(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		data, _ := io.ReadAll(conn)
		got <- data
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	tc := newTunnelConn(nil, "", client, nil, nil)

	// Submit seq 1 first; it must block in the gate until seq 0 is applied.
	seqOneReturned := make(chan struct{})
	go func() {
		defer close(seqOneReturned)
		_ = tc.writeData(context.Background(), 1, []byte("one"), false)
	}()
	// Give the seq-1 goroutine time to reach the gate and block on its turn.
	time.Sleep(20 * time.Millisecond)

	// Apply seq 0 (releases seq 1), then seq 2 half-closes so the server's
	// ReadAll returns once the whole ordered stream is delivered.
	require.NoError(t, tc.writeData(context.Background(), 0, []byte("zero"), false))
	<-seqOneReturned
	require.NoError(t, tc.writeData(context.Background(), 2, nil, true))

	select {
	case data := <-got:
		assert.Equal(t, "zeroone", string(data),
			"frames must be applied in seq order despite out-of-order submission")
	case <-time.After(2 * time.Second):
		t.Fatal("target did not receive the ordered stream")
	}
	_ = client.Close()
}

// A graceful close must not wait forever for a write that will never land (a
// target that stopped draining leaves a lower-seq write stuck). writeGate.waitReached
// is bounded by its context so the close handler goroutine cannot wedge until
// the whole channel tears down.
func TestWaitWriteSeqReachedIsBoundedByContext(t *testing.T) {
	tc := newTunnelConn(nil, "", &recordingWriteConn{}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// nextWriteSeq is 0 and no writeData will ever advance it, so without the
		// context bound this would block forever.
		tc.writeGate.waitReached(ctx, 1)
	}()
	select {
	case <-done:
		assert.Less(t, time.Since(start), time.Second,
			"the flush wait must return promptly once its context expires")
	case <-time.After(2 * time.Second):
		t.Fatal("writeGate.waitReached did not return when its context expired")
	}
}

// The read loop must block once its read-send credit is exhausted (read flow
// control) and resume as soon as the client grants more, so the worker never
// sends more inbound frames than the client can buffer.
func TestTunnelConnReadCreditBlocksAndReleases(t *testing.T) {
	tc := newTunnelConn(nil, "", &recordingWriteConn{}, nil, nil)
	for range tunnelflow.InitialReadWindow {
		require.NoError(t, tc.credit.Acquire(context.Background()), "the initial window is available")
	}

	acquired := make(chan error, 1)
	go func() { acquired <- tc.credit.Acquire(context.Background()) }()
	select {
	case <-acquired:
		t.Fatal("credit.Acquire returned though the read window was exhausted")
	case <-time.After(50 * time.Millisecond):
	}

	tc.credit.Grant(1)
	select {
	case err := <-acquired:
		require.NoError(t, err, "credit.Acquire proceeds once the client grants credit")
	case <-time.After(time.Second):
		t.Fatal("credit.Acquire did not proceed after a grant")
	}
}

// close() must wake a read loop parked on an exhausted read window so teardown
// never strands it waiting on a credit that will never be granted.
func TestTunnelConnReadCreditUnblocksOnClose(t *testing.T) {
	tc := newTunnelConn(nil, "", &recordingWriteConn{}, nil, nil)
	for range tunnelflow.InitialReadWindow {
		require.NoError(t, tc.credit.Acquire(context.Background()))
	}

	acquired := make(chan error, 1)
	go func() { acquired <- tc.credit.Acquire(context.Background()) }()
	time.Sleep(50 * time.Millisecond) // let it park on the exhausted window

	tc.close()
	select {
	case err := <-acquired:
		require.ErrorIs(t, err, tunnelflow.ErrWindowClosed,
			"a credit-starved Acquire returns ErrWindowClosed once the conn closes")
	case <-time.After(time.Second):
		t.Fatal("close did not wake the credit-starved read loop")
	}
}

// framingConn is a net.Conn whose Read returns one queued frame per call (so the
// read loop produces a deterministic number of TunnelConnEvent frames), then
// blocks until closed. Writes are discarded.
type framingConn struct {
	frames chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFramingConn(frames [][]byte) *framingConn {
	c := &framingConn{frames: make(chan []byte, len(frames)), closed: make(chan struct{})}
	for _, f := range frames {
		c.frames <- f
	}
	return c
}

func (c *framingConn) Read(b []byte) (int, error) {
	select {
	case f := <-c.frames:
		return copy(b, f), nil
	case <-c.closed:
		return 0, io.EOF
	}
}
func (c *framingConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *framingConn) Close() error                     { c.once.Do(func() { close(c.closed) }); return nil }
func (c *framingConn) LocalAddr() net.Addr              { return dummyAddr{} }
func (c *framingConn) RemoteAddr() net.Addr             { return dummyAddr{} }
func (c *framingConn) SetDeadline(time.Time) error      { return nil }
func (c *framingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *framingConn) SetWriteDeadline(time.Time) error { return nil }

// The read loop must send no more than the initial read window's worth of
// inbound frames until the client grants more credit -- the E6 backpressure that
// stops one stalled tunnel consumer from starving the shared channel. Granting
// credit then releases exactly the withheld frames.
func TestTunnelReadLoopCapsSendsAtReadWindow(t *testing.T) {
	const extra = 5
	frames := make([][]byte, int(tunnelflow.InitialReadWindow)+extra)
	for i := range frames {
		frames[i] = []byte{byte(i)}
	}
	conn := newFramingConn(frames)
	w := newTestWriter()
	tc := newTunnelConn(nil, "", conn, w, context.Background())
	mgr := newTunnelManager()
	go tunnelReadLoop(mgr, "capped", tc)
	t.Cleanup(func() { tc.close() })

	// Without a grant the worker sends exactly the initial window, then blocks --
	// it must not drain the extra frames.
	testutil.AssertEventually(t, func() bool {
		return len(w.streamsSnapshot()) == int(tunnelflow.InitialReadWindow)
	}, "read loop should send the full initial window")
	time.Sleep(50 * time.Millisecond) // give it a chance to (wrongly) send more
	assert.Equal(t, int(tunnelflow.InitialReadWindow), len(w.streamsSnapshot()),
		"read loop must not send past the window without a credit grant")

	// Granting credit lets exactly the remaining frames flow.
	tc.credit.Grant(uint64(extra))
	testutil.AssertEventually(t, func() bool {
		return len(w.streamsSnapshot()) == int(tunnelflow.InitialReadWindow)+extra
	}, "granting credit releases the withheld frames")
}

// tunnelReadLoop hands buf[:n] to sendTunnelEvent WITHOUT a defensive copy,
// relying on proto.Marshal to serialize the payload synchronously before the
// shared read buffer is reused by the next target Read. Drive several distinct,
// varying-length frames through the loop and assert every streamed event carries
// its own frame's bytes intact -- a regression that let buf be reused (or
// aliased into the wire payload) before the marshal would corrupt earlier
// frames' data. Varying lengths (a short frame after a long one) also catch a
// stale-tail leak past n.
func TestTunnelReadLoopPreservesFrameContent(t *testing.T) {
	frames := [][]byte{
		[]byte("frame-zero-AAAAAAAAAAAA"),
		[]byte("frame-one-BBBBBBBBBBBBBBBBBBBBBBBB"),
		[]byte("f2-C"),
		[]byte("frame-three-DDDDDDDDDDDDDDDDDDDDDDDDDDDD"),
	}
	require.LessOrEqual(t, len(frames), int(tunnelflow.InitialReadWindow),
		"frames must fit the initial window so no credit grant is needed")
	conn := newFramingConn(frames)
	w := newTestWriter()
	tc := newTunnelConn(nil, "content", conn, w, context.Background())
	mgr := newTunnelManager()
	go tunnelReadLoop(mgr, "content", tc)
	t.Cleanup(func() { tc.close() })

	testutil.AssertEventually(t, func() bool {
		return len(w.streamsSnapshot()) == len(frames)
	}, "read loop should stream every frame")

	got := w.streamsSnapshot()
	require.Len(t, got, len(frames))
	for i, msg := range got {
		var evt leapmuxv1.TunnelConnEvent
		require.NoError(t, proto.Unmarshal(msg.GetPayload(), &evt))
		assert.Equal(t, frames[i], evt.GetData(),
			"streamed frame %d must carry its own bytes, uncorrupted by buffer reuse", i)
	}
}

// A marker cleared early by a racing beginOpen leaves a stale entry in the
// expiry slice; a later sweep must pop it harmlessly while still reclaiming a
// genuinely-expired marker.
func TestTunnelManagerSweepSkipsEarlyClearedMarker(t *testing.T) {
	manager := newTunnelManager()
	originalTTL := staleCancelMarkerTTL
	staleCancelMarkerTTL = 20 * time.Millisecond
	t.Cleanup(func() { staleCancelMarkerTTL = originalTTL })

	present := func(connID string) bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		ok := manager.canceled.has(connID)
		return ok
	}

	// cancel-before-open records a marker + expiry-slice entry, which a racing
	// beginOpen consumes (clearing the map entry but leaving the slice entry).
	require.Nil(t, manager.cancel("early"))
	require.False(t, manager.beginOpen("early", func() {}), "the marker fences the racing beginOpen")
	require.False(t, present("early"), "beginOpen cleared the map entry")

	// A second marker must survive the sweep that later pops the stale "early"
	// slice entry alongside this now-expired one.
	require.Nil(t, manager.cancel("late"))
	time.Sleep(2 * staleCancelMarkerTTL)
	require.True(t, manager.beginOpen("fresh", func() {})) // triggers the sweep
	assert.False(t, present("late"), "the expired marker is swept")
}

// recordingWriteConn is a net.Conn whose Write appends to an in-memory buffer
// (never blocks) and records every SetWriteDeadline call. writeData no longer
// manages a per-frame write deadline (cancellation closes the conn via the
// per-conn watcher instead), so it must never call SetWriteDeadline at all.
type recordingWriteConn struct {
	mu             sync.Mutex
	buf            bytes.Buffer
	writeDeadlines []time.Time
}

func (c *recordingWriteConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *recordingWriteConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(b)
}
func (c *recordingWriteConn) Close() error                    { return nil }
func (c *recordingWriteConn) LocalAddr() net.Addr             { return dummyAddr{} }
func (c *recordingWriteConn) RemoteAddr() net.Addr            { return dummyAddr{} }
func (c *recordingWriteConn) SetDeadline(time.Time) error     { return nil }
func (c *recordingWriteConn) SetReadDeadline(time.Time) error { return nil }
func (c *recordingWriteConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadlines = append(c.writeDeadlines, t)
	c.mu.Unlock()
	return nil
}

type dummyAddr struct{}

func (dummyAddr) Network() string { return "test" }
func (dummyAddr) String() string  { return "test" }

// TestWriteDataDoesNotTouchWriteDeadline pins the deadline-free write hot path:
// writeData relies on the per-conn ctx watcher (which closes the conn on
// cancellation) rather than a per-frame SetWriteDeadline, so it must never issue
// a SetWriteDeadline syscall -- neither to arm one nor to reset it -- on any
// ~32 KiB chunk.
func TestWriteDataDoesNotTouchWriteDeadline(t *testing.T) {
	rc := &recordingWriteConn{}
	tc := newTunnelConn(nil, "", rc, nil, nil)
	require.NoError(t, tc.writeData(context.Background(), 0, []byte("hello"), false))

	rc.mu.Lock()
	calls := rc.writeDeadlines
	rc.mu.Unlock()
	assert.Empty(t, calls, "the write path must not touch SetWriteDeadline")
}
