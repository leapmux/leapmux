package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/sendq"
)

// TestNew_DispatchesOnURLScheme verifies the scheme-dispatch branches in
// New() construct a non-nil *Client for every supported URL shape.
// Transport-level round-trip tests live alongside each scheme.
func TestNew_DispatchesOnURLScheme(t *testing.T) {
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
			client := New(tc.url)
			require.NotNil(t, client, "New(%q) returned nil", tc.url)
			assert.Equal(t, tc.url, client.hubURL, "hubURL preserved verbatim")
		})
	}
}

// Regression guards: a scheme-dispatch bug silently routes local URLs to
// the plain h2c dialer, which then resolves them as DNS host:port. Each
// test asserts the resulting error is NOT from the TCP/DNS path.
func TestHTTPClientForHubURL_NpipeDispatches(t *testing.T) {
	assertDialNotRoutedToTCP(t, "npipe:leapmux-hub-nonexistent")
}

func TestHTTPClientForHubURL_UnixDispatches(t *testing.T) {
	assertDialNotRoutedToTCP(t, "unix:/nonexistent/leapmux.sock")
}

func TestHTTPClientForHubURL_HTTPFallsBackToH2C(t *testing.T) {
	httpClient, connectURL := clientForHubURL("http://127.0.0.1:1")
	require.NotNil(t, httpClient)
	assert.Equal(t, "http://127.0.0.1:1", connectURL, "remote URL should pass through verbatim")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:1/probe", nil)
	require.NoError(t, err)

	_, err = httpClient.Do(req)
	require.Error(t, err)
}

func assertDialNotRoutedToTCP(t *testing.T, url string) {
	t.Helper()
	httpClient, connectURL := clientForHubURL(url)
	require.NotNil(t, httpClient)
	assert.Equal(t, "http://localhost", connectURL, "%s should route through localhost placeholder", url)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/probe", nil)
	require.NoError(t, err)

	_, err = httpClient.Do(req)
	require.Error(t, err)
	msg := err.Error()
	assert.NotContains(t, msg, "no such host", "%s dispatched through DNS", url)
	assert.NotContains(t, msg, "dial tcp", "%s dispatched to TCP dialer", url)
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

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 10 * time.Millisecond
	bo.MaxInterval = 500 * time.Millisecond
	bo.Multiplier = 4.0
	bo.RandomizationFactor = 0
	bo.Reset()

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		timestamps = append(timestamps, time.Now())
		switch n {
		case 1:
			// First call: fail immediately → backoff=10ms.
			return fmt.Errorf("fail 1")
		case 2:
			// Second call: fail immediately → backoff=40ms.
			return fmt.Errorf("fail 2")
		case 3:
			// Third call: fail immediately → backoff=160ms.
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

	// Gap between call 3 and 4 should be large (160ms backoff).
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

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 2 * time.Millisecond
	bo.MaxInterval = 10 * time.Millisecond
	bo.Multiplier = 2.0
	bo.RandomizationFactor = 0
	bo.Reset()

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
		assert.LessOrEqual(t, gap, bo.MaxInterval+tolerance, "gap[%d]=%v exceeds MaxInterval=%v", i, gap, bo.MaxInterval)
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
// WorkspaceTabsSync. Without this case, handleMessage falls through
// to the "unhandled hub message" warn — the response would be wasted
// protocol and reconnects would have to wait for the orphan
// reconciler's hourly tick to converge worker state.
func TestHandleMessage_WorkspaceTabsSyncResp_InvokesCallback(t *testing.T) {
	c := New("http://localhost:0")
	var captured *leapmuxv1.WorkspaceTabsSyncResponse
	c.OnTabSyncResponse = func(resp *leapmuxv1.WorkspaceTabsSyncResponse) {
		captured = resp
	}

	resp := &leapmuxv1.WorkspaceTabsSyncResponse{
		OrphanTabIds: []*leapmuxv1.TabIdent{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "orphan-1"},
		},
	}
	c.handleMessage(&leapmuxv1.ConnectResponse{
		RequestId: "req-7",
		Payload: &leapmuxv1.ConnectResponse_WorkspaceTabsSyncResp{
			WorkspaceTabsSyncResp: resp,
		},
	})

	require.NotNil(t, captured, "OnTabSyncResponse should be invoked")
	assert.Same(t, resp, captured, "callback should receive the original response message verbatim")
}

// The Hub delivers the worker's owner on every connect; without this dispatch arm
// handleMessage falls through to the "unhandled hub message" warn and the worker
// never learns who owns it -- leaving requireWorkerOwner to fail closed against the
// worker's own legitimate user, permanently and indistinguishably from a genuine
// cross-tenant refusal.
func TestHandleMessage_WorkerIdentity_InvokesCallback(t *testing.T) {
	c := New("http://localhost:0")
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
	c := New("http://localhost:0")
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
	c := New("http://localhost:0")
	require.Nil(t, c.OnTabSyncResponse)

	assert.NotPanics(t, func() {
		c.handleMessage(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_WorkspaceTabsSyncResp{
				WorkspaceTabsSyncResp: &leapmuxv1.WorkspaceTabsSyncResponse{},
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

	c := New("http://localhost:0")
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

	c := New("http://localhost:0")
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
	c := New("http://localhost:0")
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
		MaxBytes:      connectQueueMaxBytes,
		FrameOverhead: connectFrameOverhead,
		OnGiveUp:      func(error) { cancel() },
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
		MaxBytes:      connectQueueMaxBytes,
		FrameOverhead: connectFrameOverhead,
		OnGiveUp: func(error) {
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
		MaxBytes:      fillSize + 10, // room for one QUEUED filler beyond the one in flight
		FrameOverhead: 0,
		OnGiveUp:      func(error) { t.Error("TrySend must not give up the connection") },
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
		MaxBytes:      connectQueueMaxBytes,
		FrameOverhead: connectFrameOverhead,
	})
	t.Cleanup(func() { c.writer.Close() })

	require.NoError(t, c.Send(heartbeatMsg()))
	c.mu.Lock()
	after := c.lastSendTime
	c.mu.Unlock()
	assert.True(t, after.After(before), "lastSendTime must advance on enqueue")

	before = after
	require.True(t, c.TrySend(heartbeatMsg()))
	c.mu.Lock()
	after = c.lastSendTime
	c.mu.Unlock()
	assert.True(t, after.After(before), "lastSendTime must advance on TrySend enqueue")
}
