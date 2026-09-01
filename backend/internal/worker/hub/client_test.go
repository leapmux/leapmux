package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// newTestClient builds a Client for url. It exists so these tests state a URL
// rather than an Endpoint: hubtransport.New opens no connection, and every
// caller here passes a literal it controls.
func newTestClient(t *testing.T, url string) *Client {
	t.Helper()
	endpoint, err := hubtransport.New(url)
	require.NoError(t, err, "hubtransport.New(%q)", url)
	return New(endpoint)
}

// TestNew_PreservesTheEndpointURL verifies that New() builds a client for
// every supported URL shape and reports the address the operator configured.
//
// The transport behind it -- which scheme dials a socket, which prefers h2c,
// which verifies a certificate -- belongs to hubtransport and is tested there,
// against real servers of each protocol.
func TestNew_PreservesTheEndpointURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"http", "http://localhost:4327"},
		{"https", "https://hub.example:443"},
		{"unix", "unix:/tmp/hub.sock"},
		{"npipe short", "npipe:leapmux-hub-test"},
		{"npipe full NT", `npipe:\\.\pipe\leapmux-hub-test`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, err := hubtransport.New(tc.url)
			require.NoError(t, err, "hubtransport.New(%q)", tc.url)
			client := New(endpoint)
			require.NotNil(t, client, "New(%q) returned nil", tc.url)
			assert.Equal(t, tc.url, client.Endpoint().URL(), "endpoint URL preserved verbatim")
		})
	}
}

func TestResolveWorkingDir_HomeDir(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "UserHomeDir")

	got, err := resolveWorkingDir("~")
	require.NoError(t, err, "resolveWorkingDir(~)")
	assert.Equal(t, home, got)
}

func TestResolveWorkingDir_HomeSubdir(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "UserHomeDir")

	// Use a subdirectory that exists under home. On macOS/Linux, home itself
	// always exists, so we create a temp dir under it for a reliable test.
	sub := filepath.Join(home, "Documents")
	if _, statErr := os.Stat(sub); statErr != nil {
		t.Skipf("~/Documents does not exist, skipping")
	}

	got, err := resolveWorkingDir("~/Documents")
	require.NoError(t, err, "resolveWorkingDir(~/Documents)")
	assert.Equal(t, sub, got)
}

func TestResolveWorkingDir_TildeInMiddle(t *testing.T) {
	// /foo/~/bar is NOT a tilde prefix — should be treated literally.
	// This path likely doesn't exist, so we expect an error.
	_, err := resolveWorkingDir("/foo/~/bar")
	assert.Error(t, err, "expected error for /foo/~/bar (path should not exist)")
}

func TestResolveWorkingDir_DoubleTilde(t *testing.T) {
	// ~~ is NOT a tilde prefix — resolves relative to cwd as ./~~
	_, err := resolveWorkingDir("~~")
	assert.Error(t, err, "expected error for ~~ (no such directory)")
}

func TestResolveWorkingDir_DoubleTildeSubpath(t *testing.T) {
	_, err := resolveWorkingDir("~~/foo")
	assert.Error(t, err, "expected error for ~~/foo (no such directory)")
}

func TestResolveWorkingDir_ExistingDir(t *testing.T) {
	// Use a temp directory to avoid symlink issues (/tmp -> /private/tmp on macOS).
	dir := t.TempDir()

	got, err := resolveWorkingDir(dir)
	require.NoError(t, err, "resolveWorkingDir(%s)", dir)
	expected := filepath.Clean(dir)
	assert.Equal(t, expected, got)
}

func TestResolveWorkingDir_NonexistentPath(t *testing.T) {
	_, err := resolveWorkingDir("/nonexistent/path/that/does/not/exist")
	assert.Error(t, err, "expected error for nonexistent path")
}

func TestResolveWorkingDir_FileNotDir(t *testing.T) {
	// Create a temporary file (not a directory).
	f, err := os.CreateTemp("", "resolveWorkingDir-test-*")
	require.NoError(t, err)
	_ = f.Close()
	defer func() { _ = os.Remove(f.Name()) }()

	_, err = resolveWorkingDir(f.Name())
	assert.Error(t, err, "expected error for a file path (not a directory)")
}

func TestResolveWorkingDir_Empty(t *testing.T) {
	// Empty string resolves to cwd.
	cwd, err := os.Getwd()
	require.NoError(t, err)

	got, err := resolveWorkingDir("")
	require.NoError(t, err, "resolveWorkingDir('')")
	assert.Equal(t, cwd, got)
}

func TestResolveWorkingDir_RelativePath(t *testing.T) {
	// "." should resolve to cwd.
	cwd, err := os.Getwd()
	require.NoError(t, err)

	got, err := resolveWorkingDir(".")
	require.NoError(t, err, "resolveWorkingDir('.')")
	assert.Equal(t, cwd, got)
}

func TestConnectWithReconnect_ReconnectsOnFailure(t *testing.T) {
	var attempts atomic.Int32
	targetAttempts := int32(4)

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		if n >= targetAttempts {
			cancel() // Stop after enough attempts.
		}
		return fmt.Errorf("connection lost")
	}

	client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)

	assert.GreaterOrEqual(t, attempts.Load(), targetAttempts, "connect call count")
}

func TestConnectWithReconnect_StopsOnContextCancel(t *testing.T) {
	var attempts atomic.Int32

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	mockConnect := func(_ context.Context, _ string) error {
		attempts.Add(1)
		return fmt.Errorf("connection lost")
	}

	// Cancel after a short delay.
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()

	client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)

	assert.GreaterOrEqual(t, attempts.Load(), int32(1), "expected at least 1 attempt")
}

func TestConnectWithReconnect_ResetsBackoffAfterLongConnection(t *testing.T) {
	// Track when each connect call happens.
	var timestamps []time.Time
	var attempts atomic.Int32

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	bo := backoffutil.NewBackoff(10*time.Millisecond, 500*time.Millisecond, 0)

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		timestamps = append(timestamps, time.Now())
		switch n {
		case 1:
			// First call: fail immediately → backoff=10ms.
			return fmt.Errorf("fail 1")
		case 2:
			// Second call: fail immediately → backoff=20ms.
			return fmt.Errorf("fail 2")
		case 3:
			// Third call: fail immediately → backoff=40ms.
			return fmt.Errorf("fail 3")
		case 4:
			// Fourth call: succeed for longer than threshold → should reset backoff.
			time.Sleep(80 * time.Millisecond)
			return fmt.Errorf("disconnect after long session")
		case 5:
			// Fifth call: fail → backoff should have been reset to 10ms (InitialInterval).
			return fmt.Errorf("fail 5")
		default:
			cancel()
			return fmt.Errorf("done")
		}
	}

	client.connectWithReconnect(ctx, "token", mockConnect, bo, 50*time.Millisecond)

	require.GreaterOrEqual(t, len(timestamps), 6, "expected at least 6 timestamps")

	// Gap between call 3 and 4 should be large (40ms backoff).
	// Gap between call 5 and 6 should be small (10ms, reset to InitialInterval).
	gap34 := timestamps[3].Sub(timestamps[2])
	gap56 := timestamps[5].Sub(timestamps[4])

	assert.Less(t, gap56, gap34, "gap after reset should be shorter than gap before long connection")
}

func TestConnectWithReconnect_BackoffCapsAtMax(t *testing.T) {
	var timestamps []time.Time
	targetAttempts := int32(8)
	var attempts atomic.Int32

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	const maxInterval = 10 * time.Millisecond
	bo := backoffutil.NewBackoff(2*time.Millisecond, maxInterval, 0)

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		timestamps = append(timestamps, time.Now())
		if n >= targetAttempts {
			cancel()
		}
		return fmt.Errorf("fail")
	}

	client.connectWithReconnect(ctx, "token", mockConnect, bo, 1*time.Hour)

	// Verify that later gaps don't exceed MaxInterval + tolerance.
	// Use a generous tolerance because OS scheduling jitter on short intervals
	// can easily add several milliseconds.
	tolerance := 50 * time.Millisecond
	for i := 1; i < len(timestamps); i++ {
		gap := timestamps[i].Sub(timestamps[i-1])
		assert.LessOrEqual(t, gap, maxInterval+tolerance, "gap[%d]=%v exceeds MaxInterval=%v", i, gap, maxInterval)
	}
}

func TestIsCodeUnauthenticated(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		assert.False(t, isCodeUnauthenticated(nil))
	})
	t.Run("direct connect.Error unauthenticated", func(t *testing.T) {
		err := connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("bad token"))
		assert.True(t, isCodeUnauthenticated(err))
	})
	t.Run("wrapped connect.Error unauthenticated", func(t *testing.T) {
		inner := connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("bad token"))
		err := fmt.Errorf("connect to hub: %w", inner)
		assert.True(t, isCodeUnauthenticated(err), "errors.As should unwrap")
	})
	t.Run("other connect code", func(t *testing.T) {
		err := connect.NewError(connect.CodeUnavailable, fmt.Errorf("server down"))
		assert.False(t, isCodeUnauthenticated(err))
	})
	t.Run("non-connect error containing the word unauthenticated", func(t *testing.T) {
		err := fmt.Errorf("some other unauthenticated failure")
		assert.False(t, isCodeUnauthenticated(err), "string match must not leak through")
	})
}

// TestHandleMessage_WorkspaceTabsSyncResp_InvokesCallback pins the
// dispatch wiring for the hub's reply to the connect-time
// WorkerTabInventory. Without this case, handleMessage falls through
// to the "unhandled hub message" warn — the response would be wasted
// protocol and reconnects would have to wait for the orphan
// reconciler's hourly tick to converge worker state.
func TestHandleMessage_WorkspaceTabsSyncResp_InvokesCallback(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	var captured *leapmuxv1.WorkerTabInventoryResponse
	c.OnTabSyncResponse = func(resp *leapmuxv1.WorkerTabInventoryResponse) {
		captured = resp
	}

	// The message is intentionally empty -- its whole contract is "a sync was
	// handled, run a reconcile pass". What matters here is that the dispatch
	// arm exists and hands the message to the callback.
	resp := &leapmuxv1.WorkerTabInventoryResponse{}
	c.handleMessage(&leapmuxv1.ConnectResponse{
		RequestId: "req-7",
		Payload: &leapmuxv1.ConnectResponse_WorkerTabInventoryResp{
			WorkerTabInventoryResp: resp,
		},
	})

	require.NotNil(t, captured, "OnTabSyncResponse should be invoked")
	assert.Same(t, resp, captured, "callback should receive the original response message verbatim")
}

// TestHandleMessage_ReconcileNudge_InvokesCallback pins the dispatch wiring for
// the Hub's mid-session convergence nudge.
//
// Without this arm handleMessage falls through to the "unhandled hub message"
// warn, and a tab close whose E2EE RPC failed -- or one a peer client or
// `leapmux control tab close` performed CRDT-only -- would again wait out the
// orphan reconciler's hourly tick, leaving the agent subprocess running the
// whole time.
func TestHandleMessage_ReconcileNudge_InvokesCallback(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	var nudges int
	c.OnReconcileNudge = func() { nudges++ }

	// Deliberately empty and unsolicited: the nudge names no tabs, because the
	// reconciler re-reads the hub's authoritative owned-tab list on every pass
	// and a tab list here would be a second, staler source for the same fact.
	c.handleMessage(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ReconcileNudge{
			ReconcileNudge: &leapmuxv1.ReconcileNudge{},
		},
	})

	assert.Equal(t, 1, nudges, "OnReconcileNudge should be invoked")
}

// A nil callback must be tolerated: the nudge is advisory, and a worker wired
// without a reconciler still has to survive the message.
func TestHandleMessage_ReconcileNudge_NilCallbackIsSafe(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	require.Nil(t, c.OnReconcileNudge)
	c.handleMessage(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ReconcileNudge{
			ReconcileNudge: &leapmuxv1.ReconcileNudge{},
		},
	})
}

// The Hub delivers the worker's owner on every connect; without this dispatch arm
// handleMessage falls through to the "unhandled hub message" warn and the worker
// never learns who owns it -- leaving requireWorkerOwner to fail closed against the
// worker's own legitimate user, permanently and indistinguishably from a genuine
// cross-tenant refusal.
func TestHandleMessage_WorkerIdentity_InvokesCallback(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	var captured string
	c.OnWorkerIdentity = func(registeredBy string) { captured = registeredBy }

	c.handleMessage(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
			WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner-1"},
		},
	})

	assert.Equal(t, "owner-1", captured, "OnWorkerIdentity should receive the Hub's owner")
}

// The optional-callback contract: a client with no identity consumer wired (tests,
// minimal embeddings) must consume the message without panicking.
func TestHandleMessage_WorkerIdentity_NilCallbackIsSafe(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	require.Nil(t, c.OnWorkerIdentity)

	assert.NotPanics(t, func() {
		c.handleMessage(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
				WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner-1"},
			},
		})
	})
}

// TestHandleMessage_WorkspaceTabsSyncResp_NilCallbackIsSafe documents
// the optional-callback contract. Clients with no reconciler wired
// (tests, minimal embeddings) must still consume the response without
// panicking; the orphan reconciler is the only consumer in production.
func TestHandleMessage_WorkspaceTabsSyncResp_NilCallbackIsSafe(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	require.Nil(t, c.OnTabSyncResponse)

	assert.NotPanics(t, func() {
		c.handleMessage(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_WorkerTabInventoryResp{
				WorkerTabInventoryResp: &leapmuxv1.WorkerTabInventoryResponse{},
			},
		})
	})
}

// The worker owner is sourced ONLY from the Hub's connect-time WorkerIdentity
// greeting; if a proxy strips the oneof, a partial upgrade drops it, or a Hub
// bug never sends it, requireWorkerOwner would deny every machine-scoped RPC
// for the connection's life with no recovery short of a reconnect. A watchdog
// force-closes the stream when the greeting does not arrive in time, so the
// reconnect backoff re-runs the greeting on a fresh stream.
func TestWatchForIdentity_ForceCancelsWhenIdentityMissing(t *testing.T) {
	old := workerIdentityTimeout
	workerIdentityTimeout = 20 * time.Millisecond
	defer func() { workerIdentityTimeout = old }()

	c := newTestClient(t, "http://localhost:0")
	var cancelled atomic.Bool
	c.connCancel = func() { cancelled.Store(true) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watchForIdentity(ctx)

	require.Eventually(t, func() bool { return cancelled.Load() },
		1*time.Second, 5*time.Millisecond,
		"watchdog must force-cancel the connection when WorkerIdentity is not delivered")
}

func TestWatchForIdentity_DoesNotCancelWhenIdentityReceived(t *testing.T) {
	old := workerIdentityTimeout
	workerIdentityTimeout = 20 * time.Millisecond
	defer func() { workerIdentityTimeout = old }()

	c := newTestClient(t, "http://localhost:0")
	c.identityReceived.Store(true)
	var cancelled atomic.Bool
	c.connCancel = func() { cancelled.Store(true) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watchForIdentity(ctx)

	time.Sleep(80 * time.Millisecond)
	assert.False(t, cancelled.Load(),
		"watchdog must not fire once WorkerIdentity has been received")
}

// The flag the watchdog reads must be set on every greeting, so the watchdog
// stops as soon as the Hub delivers the identity.
func TestHandleMessage_WorkerIdentity_SetsIdentityReceivedFlag(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	c.OnWorkerIdentity = func(string) {}
	assert.False(t, c.identityReceived.Load())
	c.handleMessage(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
			WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner-1"},
		},
	})
	assert.True(t, c.identityReceived.Load(),
		"identityReceived must be set when WorkerIdentity arrives")
}

// A Client with no live Connect stream must report the sentinel the channel
// layer classifies on, not an anonymous error: every handler holding an open
// stream tries one last frame as the connection unwinds, and Send is where that
// attempt learns there is nothing left to send on.
func TestClientSendReturnsTransportGoneWhenNotConnected(t *testing.T) {
	c := &Client{}
	require.Nil(t, c.currentWriter(), "fixture must model a client that never connected")

	err := c.Send(heartbeatMsg())

	require.Error(t, err)
	assert.ErrorIs(t, err, channel.ErrTransportGone)
}

// TestClientSendReportsAClosedWriterAsTransportGone covers the OTHER door into
// a gone transport, and the one a disconnect actually walks through: the writer
// is still installed (Connect's teardown defer clears it only later) but sendq
// has already closed it. Without the translation this returns a bare
// sendq.ErrClosed, which sendFailureLevel does not recognise -- so every handler
// holding an open stream logs a warning on an ordinary reconnect.
func TestClientSendReportsAClosedWriterAsTransportGone(t *testing.T) {
	writer := sendq.NewUnstarted(context.Background(), sendq.Config[*leapmuxv1.ConnectRequest]{
		Write:          func(context.Context, *leapmuxv1.ConnectRequest) error { return nil },
		Size:           func(m *leapmuxv1.ConnectRequest) int { return proto.Size(m) },
		MaxBytes:       sendq.DefaultMaxBytes,
		ControlReserve: sendq.DefaultControlReserve,
		FrameOverhead:  sendq.DefaultFrameOverhead,
	})
	writer.Close()

	c := &Client{writer: writer}
	require.NotNil(t, c.currentWriter(), "fixture must model an INSTALLED writer, not the nil-writer path")

	err := c.Send(heartbeatMsg())

	require.Error(t, err)
	assert.ErrorIs(t, err, channel.ErrTransportGone,
		"a closed writer is the same 'the connection is gone' fact as a nil one")
	assert.ErrorIs(t, err, sendq.ErrClosed, "the underlying cause stays inspectable")
}

func heartbeatMsg() *leapmuxv1.ConnectRequest {
	return &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{},
		},
	}
}

func connectReqSize(m *leapmuxv1.ConnectRequest) int { return proto.Size(m) }

// With the transport blocked, a second Send and the receive loop must both
// proceed: Send only enqueues (#293). A process-global mutex across the write
// would park every producer — and the receive loop that serves every channel —
// behind one wedged peer.
func TestClientSendDoesNotBlockReceiveWhenTransportBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })

	c := &Client{}
	c.writer = sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write: func(context.Context, *leapmuxv1.ConnectRequest) error {
			<-blocked
			return nil
		},
		Size:          connectReqSize,
		MaxBytes:      sendq.DefaultMaxBytes,
		FrameOverhead: sendq.DefaultFrameOverhead,
		OnGiveUp:      func(sendq.GiveUpReason, error) { cancel() },
	})
	t.Cleanup(func() { c.writer.Close() })

	require.NoError(t, c.Send(heartbeatMsg()), "first Send enqueues without waiting on the wire")

	done := make(chan error, 1)
	go func() { done <- c.Send(heartbeatMsg()) }()
	select {
	case err := <-done:
		require.NoError(t, err, "second Send must return while the transport is blocked")
	case <-time.After(2 * time.Second):
		t.Fatal("second Send blocked on the transport — #293 regression")
	}

	// The receive path must also stay free: handleMessage runs on the Connect
	// receive goroutine and must not contend with a wedged Send.
	assert.NotPanics(t, func() {
		c.handleMessage(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_Heartbeat{
				Heartbeat: &leapmuxv1.Heartbeat{},
			},
		})
	})
}

// A drain write failure must cancel the connection so ConnectWithReconnect
// re-establishes the stream.
func TestClientWriteFailureCancelsConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cancelled atomic.Bool
	c := &Client{}
	c.connCancel = func() {
		cancelled.Store(true)
		cancel()
	}
	c.writer = sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write: func(context.Context, *leapmuxv1.ConnectRequest) error {
			return errors.New("stream write failed")
		},
		Size:          connectReqSize,
		MaxBytes:      sendq.DefaultMaxBytes,
		FrameOverhead: sendq.DefaultFrameOverhead,
		OnGiveUp: func(sendq.GiveUpReason, error) {
			c.cancelConn()
		},
	})
	t.Cleanup(func() { c.writer.Close() })

	require.NoError(t, c.Send(heartbeatMsg()))
	require.Eventually(t, cancelled.Load, 2*time.Second, 5*time.Millisecond,
		"a write failure must cancel the connection")
}

// TrySend must drop rather than block or give up when the byte budget is full —
// it runs on the shared receive goroutine.
func TestClientTrySendDropsWhenBudgetFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// writeStarted fires once the drain has POPPED the first item (freeing its
	// budget) and entered Write; release keeps Write blocked so nothing else
	// drains. Synchronising on it removes the race where the drain pops the
	// first filler before the second TrySend runs -- which would free the
	// budget and let the "over-budget" enqueue succeed, flaking the assert.
	writeStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	filler := &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{PublicKey: make([]byte, 400)},
		},
	}
	fillSize := connectReqSize(filler)
	require.Greater(t, fillSize, 400)

	c := &Client{}
	c.writer = sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write: func(context.Context, *leapmuxv1.ConnectRequest) error {
			select {
			case writeStarted <- struct{}{}:
			default:
			}
			<-release
			return nil
		},
		Size:          connectReqSize,
		MaxBytes:      int64(fillSize) + 10, // room for one QUEUED filler beyond the one in flight
		FrameOverhead: 0,
		OnGiveUp:      func(sendq.GiveUpReason, error) { t.Error("TrySend must not give up the connection") },
	})
	t.Cleanup(func() { c.writer.Close() })

	// The first enqueue is popped by the drain and parks in Write; wait for that
	// so the queue is empty and the budget free again.
	require.True(t, c.TrySend(filler), "first TrySend enqueues")
	<-writeStarted

	// This one has to sit in the queue (Write is still parked), filling the budget.
	require.True(t, c.TrySend(filler), "second TrySend fills the now-idle budget")
	// Budget is full and the drain is blocked, so the third must drop.
	assert.False(t, c.TrySend(filler), "over-budget TrySend must drop")
}

// TrySendOrReset must use the control reserve so a saturated data budget
// still delivers must-deliver frames, and only cancel when even that
// headroom is gone.
func TestClientTrySendOrResetUsesControlReserve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	writeStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	filler := &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{PublicKey: make([]byte, 400)},
		},
	}
	fillSize := connectReqSize(filler)
	dataCeiling := int64(fillSize) // one queued filler fills the data budget exactly
	reserve := int64(fillSize)     // one control frame of the same size

	var cancelled atomic.Bool
	c := &Client{}
	c.connCancel = func() { cancelled.Store(true) }
	c.writer = sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write: func(context.Context, *leapmuxv1.ConnectRequest) error {
			select {
			case writeStarted <- struct{}{}:
			default:
			}
			<-release
			return nil
		},
		Size:           connectReqSize,
		MaxBytes:       dataCeiling + reserve,
		ControlReserve: reserve,
		FrameOverhead:  0,
	})
	t.Cleanup(func() { c.writer.Close() })

	// First filler is popped into Write; wait so the queue is empty again.
	require.True(t, c.TrySend(filler))
	<-writeStarted
	// Second fills the data ceiling (in-flight write holds no queued bytes).
	require.True(t, c.TrySend(filler), "second fills the data ceiling")
	assert.False(t, c.TrySend(filler), "data path must leave the control reserve free")

	require.True(t, c.TrySendOrReset(filler),
		"must-deliver control frame must consume the reserved headroom")
	assert.False(t, cancelled.Load(), "control reserve hit must not cancel")

	assert.False(t, c.TrySendOrReset(filler),
		"exhausting the full MaxBytes must drop and cancel")
	assert.True(t, cancelled.Load(), "drop after reserve exhaustion must cancel the connection")
}

// lastSendTime must advance on enqueue so the idle heartbeat does not fire
// spuriously while the queue still has work.
func TestClientLastSendTimeAdvancesOnEnqueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := &Client{}
	c.lastSendTime = time.Now().Add(-time.Hour)
	before := c.lastSendTime

	c.writer = sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write:         func(context.Context, *leapmuxv1.ConnectRequest) error { return nil },
		Size:          connectReqSize,
		MaxBytes:      sendq.DefaultMaxBytes,
		FrameOverhead: sendq.DefaultFrameOverhead,
	})
	t.Cleanup(func() { c.writer.Close() })

	require.NoError(t, c.Send(heartbeatMsg()))
	c.mu.Lock()
	after := c.lastSendTime
	c.mu.Unlock()
	assert.True(t, after.After(before), "lastSendTime must advance on enqueue")

	// Reset before TrySend so the assertion does not depend on wall-clock
	// ticks between two rapid enqueues (Windows time.Now can share a tick).
	c.mu.Lock()
	c.lastSendTime = time.Now().Add(-time.Hour)
	before = c.lastSendTime
	c.mu.Unlock()
	require.True(t, c.TrySend(heartbeatMsg()))
	c.mu.Lock()
	after = c.lastSendTime
	c.mu.Unlock()
	assert.True(t, after.After(before), "lastSendTime must advance on TrySend enqueue")
}
