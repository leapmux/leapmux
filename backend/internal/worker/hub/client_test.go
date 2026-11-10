package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// newBareClient builds the minimal Client the transport tests need: no
// endpoint and no managers, but a working clock.
//
// The clock also drives the retry and identity watchdog tests.
func newBareClient() *Client {
	return &Client{clock: quartz.NewReal()}
}

// newTestClient builds a Client for url. It exists so these tests state a URL
// rather than an Endpoint: hubtransport.New opens no connection, and every
// caller here passes a literal it controls.
func newTestClient(t *testing.T, url string, opts ...ClientOption) *Client {
	t.Helper()
	endpoint, err := hubtransport.New(url)
	require.NoError(t, err, "hubtransport.New(%q)", url)
	return New(endpoint, opts...)
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

func TestWithClockRejectsNil(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { WithClock(nil) })
}

func TestNewUsesRealClockByDefault(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, "http://localhost:0")
	require.IsType(t, quartz.NewReal(), c.clock,
		"New must wire the real clock, not a seeded mock a WithinDuration check would accept")
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

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		if n >= targetAttempts {
			cancel() // Stop after enough attempts.
		}
		return fmt.Errorf("connection lost")
	}

	done := make(chan struct{})
	go func() {
		_ = client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)
		close(done)
	}()
	for range targetAttempts - 1 {
		delay := testutil.WaitForTimer(t, testCtx, newTimer)
		testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	}
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the reconnect loop did not stop after cancellation")
	}

	assert.Equal(t, targetAttempts, attempts.Load(), "connect call count")
}

func TestConnectWithReconnect_StopsOnContextCancel(t *testing.T) {
	var attempts atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

	mockConnect := func(_ context.Context, _ string) error {
		attempts.Add(1)
		return fmt.Errorf("connection lost")
	}

	done := make(chan struct{})
	go func() {
		_ = client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)
		close(done)
	}()
	_ = testutil.WaitForTimer(t, testCtx, newTimer)
	cancel()
	stopTimer.MustWait(testCtx).MustRelease(testCtx)
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the reconnect loop ignored context cancellation")
	}

	assert.Equal(t, int32(1), attempts.Load(), "context cancellation must stop before a second attempt")
}

// A key refusal cannot be fixed by reconnecting: the loop stops, the error wraps
// ErrKeysRefused, and nothing clears the worker's state.
func TestConnectWithReconnect_StopsOnRefusedKeys(t *testing.T) {
	var attempts atomic.Int32
	var deregisters atomic.Int32
	client := &Client{clock: testutil.NewQuartzMock(t), OnDeregister: func() { deregisters.Add(1) }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mockConnect := func(_ context.Context, _ string) error {
		attempts.Add(1)
		return websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: channelwire.WorkerCloseReasonKeysDiffer}
	}

	err := client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeysRefused)
	assert.Equal(t, int32(1), attempts.Load(), "a key refusal must not be retried")
	assert.Zero(t, deregisters.Load(), "a key refusal must not clear the worker's state")
}

// A refusal that carries worker_deleted stops the loop and runs OnDeregister.
// A Client with no OnDeregister stops the same way, because a panic there would
// end the whole solo or desktop process.
func TestConnectWithReconnect_DeregistersADeletedWorker(t *testing.T) {
	t.Parallel()

	for name, withHook := range map[string]bool{"with OnDeregister": true, "with no OnDeregister": false} {
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			var deregisters atomic.Int32
			client := &Client{clock: testutil.NewQuartzMock(t)}
			if withHook {
				client.OnDeregister = func() { deregisters.Add(1) }
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			mockConnect := func(_ context.Context, _ string) error {
				attempts.Add(1)
				return websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: channelwire.WorkerCloseReasonDeleted}
			}

			err := client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)
			require.NoError(t, err, "a deleted worker stops cleanly")
			assert.Equal(t, int32(1), attempts.Load(), "a deleted worker must not be retried")
			if withHook {
				assert.Equal(t, int32(1), deregisters.Load())
			}
		})
	}
}

// A bare PermissionDenied is what a proxy in front of the Hub answers with. It
// carries no refusal detail, so the worker keeps retrying.
func TestConnectWithReconnect_RetriesABarePermissionDenied(t *testing.T) {
	var attempts, deregisters atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock, OnDeregister: func() { deregisters.Add(1) }}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

	mockConnect := func(_ context.Context, _ string) error {
		if attempts.Add(1) >= 2 {
			cancel()
		}
		return &UpgradeRefusedError{StatusCode: 403, Body: "forbidden"}
	}

	done := make(chan struct{})
	go func() {
		_ = client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)
		close(done)
	}()

	delay := testutil.WaitForTimer(t, testCtx, newTimer)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the reconnect loop did not stop after cancellation")
	}
	assert.Equal(t, int32(2), attempts.Load(), "a bare PermissionDenied must be retried")
	assert.Zero(t, deregisters.Load(), "only a permanent Hub close may run OnDeregister")
}

// A bare Unauthenticated is not proof that this worker was deleted. A proxy in
// front of the hub answers 401 by itself, and a hub database restored from an
// older backup does not know the worker's token — neither may destroy the
// worker's identity. Only the permanent Hub close makes a refusal
// permanent; without one the worker retries, and OnDeregister never runs.
func TestConnectWithReconnect_RetriesABareUnauthenticated(t *testing.T) {
	var attempts, deregisters atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock, OnDeregister: func() { deregisters.Add(1) }}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

	mockConnect := func(_ context.Context, _ string) error {
		if attempts.Add(1) >= 2 {
			cancel() // Stop after the retry proves the loop kept going.
		}
		return &UpgradeRefusedError{StatusCode: 401, Body: "unauthorized"}
	}

	done := make(chan struct{})
	go func() {
		_ = client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)
		close(done)
	}()

	// Fast path: the base reads a bare Unauthenticated as a deletion and returns
	// at once, so the loop is already over here.
	select {
	case <-done:
		t.Fatalf("a bare Unauthenticated ended the reconnect loop after %d attempt(s) with %d OnDeregister call(s): a proxy's 401 must not delete this worker's identity",
			attempts.Load(), deregisters.Load())
	default:
	}

	// A healthy worker schedules the next attempt instead.
	call, err := newTimer.Wait(testCtx)
	if err != nil {
		t.Fatalf("a bare Unauthenticated scheduled no retry after %d attempt(s) with %d OnDeregister call(s): the loop ended instead of backing off",
			attempts.Load(), deregisters.Load())
	}
	delay := call.Duration
	call.MustRelease(testCtx)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)

	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the reconnect loop did not stop after cancellation")
	}

	assert.Equal(t, int32(2), attempts.Load(), "a bare Unauthenticated must be retried")
	assert.Zero(t, deregisters.Load(), "only a permanent Hub close may run OnDeregister")
}

// A worker that the hub does not know retries for ever, so it must state its own
// remedy. The record appears exactly once per run of consecutive bare
// Unauthenticated refusals, at the attempt that reaches the threshold, and any
// other outcome starts the count again.
func TestConnectWithReconnect_LogsTheRemedyOnceForAWorkerTheHubDoesNotKnow(t *testing.T) {
	bareUnauthenticated := &UpgradeRefusedError{StatusCode: 401, Body: "unauthorized"}
	lostConnection := errors.New("connection lost")
	repeat := func(err error, n int) []error {
		errs := make([]error, n)
		for i := range errs {
			errs[i] = err
		}
		return errs
	}

	tests := []struct {
		name string
		errs []error
		// wantFirstRecordAt is the 1-based attempt whose refusal logged the
		// remedy, or 0 when no attempt may log it.
		wantFirstRecordAt int
	}{
		{
			name: "one refusal below the threshold logs nothing",
			errs: repeat(bareUnauthenticated, bareUnauthenticatedRemedyThreshold-1),
		},
		{
			name:              "the refusal that reaches the threshold logs once",
			errs:              repeat(bareUnauthenticated, bareUnauthenticatedRemedyThreshold),
			wantFirstRecordAt: bareUnauthenticatedRemedyThreshold,
		},
		{
			name:              "refusals past the threshold do not log again",
			errs:              repeat(bareUnauthenticated, 3*bareUnauthenticatedRemedyThreshold),
			wantFirstRecordAt: bareUnauthenticatedRemedyThreshold,
		},
		{
			name: "another failure resets the count",
			errs: append(append(
				repeat(bareUnauthenticated, bareUnauthenticatedRemedyThreshold-1),
				lostConnection),
				repeat(bareUnauthenticated, bareUnauthenticatedRemedyThreshold-1)...),
		},
		{
			name: "a connection that ends cleanly resets the count",
			errs: append(append(
				repeat(bareUnauthenticated, bareUnauthenticatedRemedyThreshold-1),
				nil),
				repeat(bareUnauthenticated, bareUnauthenticatedRemedyThreshold-1)...),
		},
		{
			name: "a bare PermissionDenied counts as another outcome",
			errs: repeat(&UpgradeRefusedError{StatusCode: 403, Body: "forbidden"}, 2*bareUnauthenticatedRemedyThreshold),
		},
	}

	// CaptureDefaultLogger swaps a process-global, so these cases run without
	// t.Parallel.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			clock := testutil.NewQuartzMock(t)
			client := &Client{clock: clock}
			testCtx := testutil.DeadlineContext(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

			// recordsBefore[i] is the record count when attempt i+1 starts, so
			// it shows which refusal wrote the record. One attempt follows the
			// last refusal and cancels, because the loop ignores the outcome of
			// an attempt that ends with a canceled context.
			var recordsBefore []int
			mockConnect := func(context.Context, string) error {
				attempt := len(recordsBefore)
				recordsBefore = append(recordsBefore, strings.Count(logs.String(), "does not know this worker"))
				if attempt == len(tt.errs) {
					cancel()
					return nil
				}
				return tt.errs[attempt]
			}

			done := make(chan struct{})
			go func() {
				_ = client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), time.Hour)
				close(done)
			}()
			for range tt.errs {
				delay := testutil.WaitForTimer(t, testCtx, newTimer)
				testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
			}
			select {
			case <-done:
			case <-testCtx.Done():
				require.FailNow(t, "the reconnect loop did not stop after cancellation")
			}

			logged := logs.String()
			require.Len(t, recordsBefore, len(tt.errs)+1)
			if tt.wantFirstRecordAt == 0 {
				assert.NotContains(t, logged, "does not know this worker")
				return
			}
			assert.Equal(t, 1, strings.Count(logged, "does not know this worker"), logged)
			assert.Contains(t, logged, "level=ERROR")
			assert.Contains(t, logged, "saved Hub endpoint")
			assert.NotContains(t, logged, "Check --hub")
			assert.Contains(t, logged, "state.json")
			assert.Contains(t, logged, "more than 7 days ago")
			for i, records := range recordsBefore {
				want := 0
				if i+1 > tt.wantFirstRecordAt {
					want = 1
				}
				assert.Equal(t, want, records, "records before attempt %d", i+1)
			}
		})
	}
}

func TestConnectWithReconnect_ResetsBackoffAfterLongConnection(t *testing.T) {
	var attempts atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

	// Zero jitter, so the sequence asserted below is the one the policy states.
	bo := backoffutil.NewBackoff(10*time.Millisecond, 500*time.Millisecond, 0)

	mockConnect := func(_ context.Context, _ string) error {
		switch attempts.Add(1) {
		case 1:
			// Fails at once → backoff 10ms.
			return fmt.Errorf("fail 1")
		case 2:
			// Fails at once → backoff 20ms.
			return fmt.Errorf("fail 2")
		case 3:
			// Fails at once → backoff 40ms.
			return fmt.Errorf("fail 3")
		case 4:
			// Holds longer than the threshold, which resets the backoff.
			// Await the waiter: nothing is armed while connect runs today, so
			// Advance completes at once, but a future timer here would
			// otherwise fire beside the rest of the test. Wait, not MustWait --
			// this is not the test goroutine, and MustWait calls Fatalf.
			assert.NoError(t, clock.Advance(80*time.Millisecond).Wait(testCtx),
				"the long-connection advance must settle")
			return fmt.Errorf("disconnect after long session")
		case 5:
			// Fails at once → back to the initial 10ms.
			return fmt.Errorf("fail 5")
		default:
			cancel()
			return fmt.Errorf("done")
		}
	}

	done := make(chan struct{})
	go func() {
		_ = client.connectWithReconnect(ctx, "token", mockConnect, bo, 50*time.Millisecond)
		close(done)
	}()

	want := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		10 * time.Millisecond,
		20 * time.Millisecond,
	}
	for i, expected := range want {
		delay := testutil.WaitForTimer(t, testCtx, newTimer)
		assert.Equal(t, expected, delay, "reconnect delay %d", i+1)
		testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	}
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the reconnect loop did not stop after the expected attempts")
	}
	require.Equal(t, int32(6), attempts.Load(), "expected connect attempts")
}

func TestConnectWithReconnect_BackoffCapsAtMax(t *testing.T) {
	targetAttempts := int32(8)
	var attempts atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

	const maxInterval = 10 * time.Millisecond
	bo := backoffutil.NewBackoff(2*time.Millisecond, maxInterval, 0)

	mockConnect := func(_ context.Context, _ string) error {
		if attempts.Add(1) >= targetAttempts {
			cancel()
		}
		return fmt.Errorf("fail")
	}

	done := make(chan struct{})
	go func() {
		_ = client.connectWithReconnect(ctx, "token", mockConnect, bo, time.Hour)
		close(done)
	}()
	var waits []time.Duration
	for range targetAttempts - 1 {
		delay := testutil.WaitForTimer(t, testCtx, newTimer)
		waits = append(waits, delay)
		testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	}
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the capped reconnect loop did not stop")
	}
	require.Len(t, waits, int(targetAttempts-1),
		"one recorded wait per failed attempt, or the cap assertion below indexes nothing")
	for i, delay := range waits {
		assert.LessOrEqual(t, delay, maxInterval,
			"wait[%d]=%v exceeds MaxInterval=%v", i, delay, maxInterval)
	}
	assert.Equal(t, maxInterval, waits[len(waits)-1],
		"the sequence must actually reach the cap, or the loop above proves nothing")
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
// never learns who owns it -- leaving the owner gate to fail closed against the
// worker's own legitimate user, permanently and indistinguishably from a genuine
// cross-tenant refusal.
func TestHandleMessage_WorkerIdentity_InvokesCallback(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	var captured *leapmuxv1.WorkerIdentity
	c.OnWorkerIdentity = func(identity *leapmuxv1.WorkerIdentity) bool {
		captured = identity
		return true
	}

	c.handleMessage(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
			WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner-1", RegisteredByUsername: "alice"},
		},
	})

	require.NotNil(t, captured, "OnWorkerIdentity should receive the Hub's identity")
	assert.Equal(t, "owner-1", captured.GetRegisteredBy(), "the callback receives the Hub's owner")
	assert.Equal(t, "alice", captured.GetRegisteredByUsername(), "the callback receives the owner's username")
}

func TestConnectionStateRequiresAcceptedIdentityAndReportsDisconnection(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprint(accept), func(t *testing.T) {
			client := newTestClient(t, "http://localhost:0")
			var transitions []bool
			var causes []error
			client.OnWorkerIdentity = func(*leapmuxv1.WorkerIdentity) bool { return accept }
			client.OnConnectionState = func(connected bool, err error) {
				transitions = append(transitions, connected)
				causes = append(causes, err)
			}
			identity := &leapmuxv1.ConnectResponse{Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner"}}}
			failure := websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: channelwire.WorkerCloseReasonKeysDiffer}
			err := client.connectWithReconnect(t.Context(), "token", func(context.Context, string) error { client.handleMessage(identity); return failure }, newFastBackoff(), time.Second)
			require.ErrorIs(t, err, ErrKeysRefused)
			if accept {
				assert.Equal(t, []bool{true, false}, transitions)
				assert.Nil(t, causes[0])
			} else {
				assert.Equal(t, []bool{false}, transitions)
			}
			assert.ErrorIs(t, causes[len(causes)-1], failure)
			assert.Equal(t, accept, client.identityReceived.Load())
		})
	}
}

// The Hub sends the identity again on a live stream when the owner's account
// changes. Each delivery reaches the callback in order, so the worker follows a
// rename without a reconnect.
func TestHandleMessage_WorkerIdentity_DeliversEveryReDelivery(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	var captured []*leapmuxv1.WorkerIdentity
	c.OnWorkerIdentity = func(identity *leapmuxv1.WorkerIdentity) bool {
		captured = append(captured, identity)
		return true
	}

	first := &leapmuxv1.WorkerIdentity{RegisteredBy: "owner-1", RegisteredByUsername: "alice"}
	renamed := &leapmuxv1.WorkerIdentity{RegisteredBy: "owner-1", RegisteredByUsername: "alice2"}
	for _, identity := range []*leapmuxv1.WorkerIdentity{first, renamed} {
		c.handleMessage(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{WorkerIdentity: identity},
		})
	}

	require.Len(t, captured, 2, "a re-delivery on the same connection is not dropped")
	assert.Same(t, first, captured[0])
	assert.Same(t, renamed, captured[1], "the callback receives the Hub's message as it is")
	assert.True(t, c.identityReceived.Load())
}

// listOwnedTabsConnector serves only ListOwnedTabsForWorker of the connector
// service, and records the bearer each call carries.
type listOwnedTabsConnector struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler
	calls atomic.Int32
	auth  atomic.Pointer[string]
}

func (s *listOwnedTabsConnector) ListOwnedTabsForWorker(ctx context.Context, _ *leapmuxv1.ListOwnedTabsForWorkerRequest) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
	s.calls.Add(1)
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		auth := info.RequestHeader().Get("Authorization")
		s.auth.Store(&auth)
	}
	return &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: "owner-1"}, nil
}

// startListOwnedTabsConnector serves the connector over unencrypted HTTP/2,
// which is what the worker's client speaks.
func startListOwnedTabsConnector(t *testing.T) (*listOwnedTabsConnector, string) {
	t.Helper()
	svc := &listOwnedTabsConnector{}
	mux := http.NewServeMux()
	srv := connect.NewServer()
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(srv, svc)
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(0))
	server := httptest.NewUnstartedServer(mux)
	server.Config.Protocols = &http.Protocols{}
	server.Config.Protocols.SetHTTP1(true)
	server.Config.Protocols.SetUnencryptedHTTP2(true)
	server.Start()
	t.Cleanup(server.Close)
	return svc, server.URL
}

// The orphan reconciler's list call is a method of the connector service. A
// hub that serves only that service answers it, with the worker's own token as
// the bearer.
func TestListOwnedTabsForWorker_CallsTheConnectorService(t *testing.T) {
	svc, url := startListOwnedTabsConnector(t)
	c := newTestClient(t, url)
	c.mu.Lock()
	c.authToken = "worker-token"
	c.mu.Unlock()

	resp, err := c.ListOwnedTabsForWorker(testutil.DeadlineContext(t))
	require.NoError(t, err)
	assert.Equal(t, "owner-1", resp.GetOwnerUserId(), "the whole response reaches the caller")
	require.Equal(t, int32(1), svc.calls.Load())
	require.NotNil(t, svc.auth.Load())
	assert.Equal(t, "Bearer worker-token", *svc.auth.Load())
}

// Before the first Connect the client holds no token, so it refuses locally
// rather than asking the hub without credentials.
func TestListOwnedTabsForWorker_RefusesWithoutAToken(t *testing.T) {
	svc, url := startListOwnedTabsConnector(t)
	c := newTestClient(t, url)

	resp, err := c.ListOwnedTabsForWorker(testutil.DeadlineContext(t))
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "no auth token")
	assert.Zero(t, svc.calls.Load(), "the hub is never asked without a token")
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
	assert.True(t, c.identityReceived.Load(),
		"with no handler nothing consumes the owner, so the watchdog has nothing to protect")
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
// bug never sends it, the owner gate would deny every machine-scoped RPC
// for the connection's life with no recovery short of a reconnect. A watchdog
// force-closes the stream when the greeting does not arrive in time, so the
// reconnect backoff re-runs the greeting on a fresh stream.
func TestWatchForIdentity_ForceCancelsWhenIdentityMissing(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	c := newTestClient(t, "http://localhost:0", WithClock(clock))
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubIdentityTimerTag)
	var cancelled atomic.Bool
	cancelCause := func(cause error) { assert.ErrorIs(t, cause, errNoWorkerIdentity); cancelled.Store(true) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watchForIdentity(ctx, cancelCause)

	delay := testutil.WaitForTimer(t, testCtx, newTimer)
	assert.Equal(t, workerIdentityTimeout, delay)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	assert.True(t, cancelled.Load(),
		"the watchdog must cancel the connection when the Hub omits WorkerIdentity")
}

// This test changes the process-global logger, so it cannot run in parallel.
func TestWatchForIdentity_DoesNotCancelAReplacementConnection(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	c := newTestClient(t, "http://localhost:0", WithClock(clock))
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubIdentityTimerTag)
	timeoutReached := make(chan struct{})
	resume := make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.MessageKey && attr.Value.String() == "hub did not deliver a usable worker identity within timeout; forcing reconnect" {
				close(timeoutReached)
				select {
				case <-resume:
				case <-testCtx.Done():
				}
			}
			return attr
		},
	})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	oldCtx, oldCancel := context.WithCancelCause(t.Context())
	defer oldCancel(nil)
	c.connCancel = oldCancel
	go c.watchForIdentity(oldCtx, oldCancel)
	delay := testutil.WaitForTimer(t, testCtx, newTimer)
	clock.Advance(delay).MustWait(testCtx)
	requireReceive(t, timeoutReached, "the old watchdog did not reach its timeout")

	// Pause after the timeout wins the select, then replace the connection as
	// the reconnect loop does. Checking ctx.Err before the select cannot
	// prevent cancellation of the replacement after this pause.
	oldCancel(context.Canceled)
	nextCtx, nextCancel := context.WithCancelCause(t.Context())
	defer nextCancel(nil)
	c.mu.Lock()
	c.connCancel = nextCancel
	c.mu.Unlock()
	c.identityReceived.Store(false)
	release()
	stopTimer.MustWait(testCtx).MustRelease(testCtx)

	assert.ErrorIs(t, context.Cause(oldCtx), context.Canceled)
	assert.NoError(t, nextCtx.Err(), "the old watchdog must not cancel the replacement connection")
}

func TestWatchForIdentity_DoesNotCancelWhenIdentityReceived(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	c := newTestClient(t, "http://localhost:0", WithClock(clock))
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubIdentityTimerTag)
	c.identityReceived.Store(true)
	var cancelled atomic.Bool
	cancelCause := func(cause error) { assert.ErrorIs(t, cause, errNoWorkerIdentity); cancelled.Store(true) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watchForIdentity(ctx, cancelCause)

	delay := testutil.WaitForTimer(t, testCtx, newTimer)
	assert.Equal(t, workerIdentityTimeout, delay)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	assert.False(t, cancelled.Load(),
		"watchdog must not fire once WorkerIdentity has been received")
}

// An identity with an empty owner, from a Hub bug or a truncated frame, stores
// nothing. On a worker that holds no owner the watchdog must still force a
// reconnect: a greeting that set no owner wedges the owner gate exactly as a missing
// greeting does. A worker that holds an owner keeps it, so its gate still admits
// that owner, and a reconnect would only drop every browser channel of this worker.
func TestWatchForIdentity_ARefusedIdentityForcesAReconnectOnlyWithNoOwner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		holdsOwner bool
	}{
		{name: "a worker with no owner reconnects"},
		{name: "a worker that keeps its owner stays connected", holdsOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := testutil.NewQuartzMock(t)
			c := newTestClient(t, "http://localhost:0", WithClock(clock))
			testCtx := testutil.DeadlineContext(t)
			newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubIdentityTimerTag)
			var cancelled atomic.Bool
			cancelCause := func(cause error) { assert.ErrorIs(t, cause, errNoWorkerIdentity); cancelled.Store(true) }
			// The handler reports the owner, as nudgeOnNewOwner does after
			// ApplyWorkerIdentity refused an empty owner.
			var deliveries int
			c.OnWorkerIdentity = func(*leapmuxv1.WorkerIdentity) bool {
				deliveries++
				return tc.holdsOwner
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go c.watchForIdentity(ctx, cancelCause)
			delay := testutil.WaitForTimer(t, testCtx, newTimer)

			c.handleMessage(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
					WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredByUsername: "nobody"},
				},
			})
			require.Equal(t, 1, deliveries, "the handler still sees the refused identity")
			assert.Equal(t, tc.holdsOwner, c.identityReceived.Load())

			testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
			assert.Equal(t, !tc.holdsOwner, cancelled.Load())
		})
	}
}

// The flag the watchdog reads must be set on every greeting that the handler
// applies, so the watchdog stops as soon as the Hub delivers the identity.
func TestHandleMessage_WorkerIdentity_SetsIdentityReceivedFlag(t *testing.T) {
	c := newTestClient(t, "http://localhost:0")
	c.OnWorkerIdentity = func(*leapmuxv1.WorkerIdentity) bool { return true }
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
	c := newBareClient()
	require.Nil(t, c.currentWriter(), "fixture must model a client that never connected")

	err := c.Send(inventoryMsg())

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

	c := newBareClient()
	c.writer = writer
	require.NotNil(t, c.currentWriter(), "fixture must model an INSTALLED writer, not the nil-writer path")

	err := c.Send(inventoryMsg())

	require.Error(t, err)
	assert.ErrorIs(t, err, channel.ErrTransportGone,
		"a closed writer is the same 'the connection is gone' fact as a nil one")
	assert.ErrorIs(t, err, sendq.ErrClosed, "the underlying cause stays inspectable")
}

func inventoryMsg() *leapmuxv1.ConnectRequest {
	return &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{
			WorkerTabInventory: &leapmuxv1.WorkerTabInventory{},
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

	c := newBareClient()
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

	require.NoError(t, c.Send(inventoryMsg()), "first Send enqueues without waiting on the wire")

	done := make(chan error, 1)
	go func() { done <- c.Send(inventoryMsg()) }()
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
			Payload: &leapmuxv1.ConnectResponse_ReconcileNudge{
				ReconcileNudge: &leapmuxv1.ReconcileNudge{},
			},
		})
	})
}

// fakeHubStream fixes the order of reads, writes, and closes in race tests.
type fakeHubStream struct {
	writeErr   error
	helloErr   error
	refusal    error
	receiveOK  chan struct{}
	closed     chan struct{}
	pings      chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	sends      int
	sent       []*leapmuxv1.ConnectRequest
	messages   []*leapmuxv1.ConnectResponse
	firstClose string
}

func newFakeHubStream(writeErr, refusal error) *fakeHubStream {
	return &fakeHubStream{writeErr: writeErr, refusal: refusal, receiveOK: make(chan struct{}), closed: make(chan struct{}), pings: make(chan struct{}, 1)}
}
func (s *fakeHubStream) Send(_ context.Context, msg *leapmuxv1.ConnectRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	if s.sends == 1 && s.helloErr != nil {
		return s.helloErr
	}
	if s.sends > 1 && s.writeErr != nil {
		return s.writeErr
	}
	s.sent = append(s.sent, proto.CloneOf(msg))
	return nil
}
func (s *fakeHubStream) Receive(context.Context) (*leapmuxv1.ConnectResponse, error) {
	select {
	case <-s.receiveOK:
	case <-s.closed:
		return nil, fmt.Errorf("read: %w", net.ErrClosed)
	}
	select {
	case <-s.closed:
		return nil, fmt.Errorf("read: %w", net.ErrClosed)
	default:
	}
	s.mu.Lock()
	if len(s.messages) > 0 {
		next := s.messages[0]
		s.messages = s.messages[1:]
		s.mu.Unlock()
		return next, nil
	}
	refusal := s.refusal
	s.mu.Unlock()
	if refusal != nil {
		return nil, refusal
	}
	<-s.closed
	return nil, fmt.Errorf("read: %w", net.ErrClosed)
}
func (s *fakeHubStream) finish(method string) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.firstClose = method
		s.mu.Unlock()
		close(s.closed)
	})
	return nil
}
func (s *fakeHubStream) Close() error                  { return s.finish("close") }
func (s *fakeHubStream) CloseNow() error               { return s.finish("close-now") }
func (s *fakeHubStream) PingReceived() <-chan struct{} { return s.pings }
func (s *fakeHubStream) closedWith() string            { s.mu.Lock(); defer s.mu.Unlock(); return s.firstClose }

func writerStreamClient(t *testing.T, writeErr, refusal error) (*Client, *fakeHubStream) {
	t.Helper()
	c := newTestClient(t, "http://127.0.0.1:1", WithClock(testutil.NewQuartzMock(t)))
	stream := newFakeHubStream(writeErr, refusal)
	c.dialStream = func(context.Context, string) (hubStream, error) { return stream, nil }
	c.TabSyncProvider = func() *leapmuxv1.WorkerTabInventory { return &leapmuxv1.WorkerTabInventory{} }
	return c, stream
}

// A closed-socket write preserves the Hub's refusal for the receive loop.
func TestConnect_ReturnsTheHubsRefusalWhenAWriteHitsTheClosedStream(t *testing.T) {
	t.Run("a closed-stream write leaves the refusal to the reader", func(t *testing.T) {
		logs := testutil.CaptureDefaultLogger(t)
		synctest.Test(t, func(t *testing.T) {
			c, stream := writerStreamClient(t, fmt.Errorf("write: %w", net.ErrClosed), websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: channelwire.WorkerCloseReasonDeleted})
			result := make(chan error, 1)
			go func() { result <- c.Connect(t.Context(), "token") }()
			synctest.Wait()
			close(stream.receiveOK)
			assert.Equal(t, refusalWorkerDeleted, classifyStreamRefusal(<-result))
			assert.NotContains(t, logs.String(), "writer gave up; reconnecting")
		})
	})
	t.Run("a write that the Hub did not cause still cancels", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			failure := errors.New("broken pipe")
			c, stream := writerStreamClient(t, failure, nil)
			result := make(chan error, 1)
			go func() { result <- c.Connect(t.Context(), "token") }()
			synctest.Wait()
			err := <-result
			assert.ErrorIs(t, err, failure)
			assert.Equal(t, refusalRetry, classifyStreamRefusal(err))
			assert.Equal(t, "close-now", stream.closedWith())
		})
	})
}

func TestConnect_AControlReplyOnAClosedStreamKeepsTheHubsRefusal(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	synctest.Test(t, func(t *testing.T) {
		c, stream := writerStreamClient(t, net.ErrClosed, websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: channelwire.WorkerCloseReasonDeleted})
		keys, err := noiseutil.GenerateCompositeKeypair()
		require.NoError(t, err)
		c.SetChannelMgr(channel.NewManager(keys, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, c.Send, c.TrySendOrReset, 0, 0))
		stream.messages = []*leapmuxv1.ConnectResponse{{RequestId: "open-1", Payload: &leapmuxv1.ConnectResponse_ChannelOpen{ChannelOpen: &leapmuxv1.ChannelOpenRequest{ChannelId: "channel-1"}}}}
		result := make(chan error, 1)
		go func() { result <- c.Connect(t.Context(), "token") }()
		synctest.Wait()
		close(stream.receiveOK)
		assert.Equal(t, refusalWorkerDeleted, classifyStreamRefusal(<-result))
		assert.Contains(t, logs.String(), "rejecting channel open")
		assert.NotContains(t, logs.String(), "saturated")
		assert.NotContains(t, logs.String(), "over budget")
	})
}

func TestClientWriteFailureCancelsConnection(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	c := newBareClient()
	c.connCancel = cancel
	failure := errors.New("stream write failed")
	c.writer = sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write: func(context.Context, *leapmuxv1.ConnectRequest) error { return failure },
		Size:  connectReqSize, MaxBytes: sendq.DefaultMaxBytes, FrameOverhead: sendq.DefaultFrameOverhead,
		OnGiveUp: c.connectWriterGiveUpHook(c.cancelConn),
	})
	t.Cleanup(c.writer.Close)
	require.NoError(t, c.Send(inventoryMsg()))
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("write failure did not cancel the connection")
	}
	assert.ErrorIs(t, context.Cause(ctx), failure)
	assert.Contains(t, context.Cause(ctx).Error(), sendq.GiveUpWriteError.Label())
}

func TestWriterGiveUpCancelsConnection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason sendq.GiveUpReason
		err    error
		want   bool
	}{
		{sendq.GiveUpWriteError, net.ErrClosed, false},
		{sendq.GiveUpWriteError, fmt.Errorf("write: %w", net.ErrClosed), false},
		{sendq.GiveUpWriteError, context.Canceled, false},
		{sendq.GiveUpWriteError, errors.New("broken pipe"), true},
		{sendq.GiveUpWriteError, io.EOF, true},
		{sendq.GiveUpWriteTimeout, net.ErrClosed, true},
		{sendq.GiveUpOverBudget, net.ErrClosed, true},
		{sendq.GiveUpPoolPressure, net.ErrClosed, true},
		{sendq.GiveUpStall, net.ErrClosed, true},
	} {
		assert.Equal(t, tc.want, writerGiveUpCancelsConnection(tc.reason, tc.err))
	}
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
		Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
			ChannelMessageResp: &leapmuxv1.ChannelMessage{Ciphertext: make([]byte, 400)},
		},
	}
	fillSize := connectReqSize(filler)
	require.Greater(t, fillSize, 400)

	c := newBareClient()
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
		Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
			ChannelMessageResp: &leapmuxv1.ChannelMessage{Ciphertext: make([]byte, 400)},
		},
	}
	fillSize := connectReqSize(filler)
	dataCeiling := int64(fillSize) // one queued filler fills the data budget exactly
	reserve := int64(fillSize)     // one control frame of the same size

	var cancelled atomic.Bool
	c := newBareClient()
	c.connCancel = func(cause error) { assert.ErrorIs(t, cause, errControlQueueSaturated); cancelled.Store(true) }
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

	assert.Equal(t, controlSaturated, c.trySendControl(filler),
		"exhausting the full MaxBytes must drop and cancel")
	assert.True(t, cancelled.Load(), "drop after reserve exhaustion must cancel the connection")
}

// A control frame that finds no writer, or a closed one, finds a connection that
// is already ending. trySendControl drops it without cancelling and without the
// saturation warning, so the receive loop alone reports how the connection
// ended. Only a full control reserve on a live writer cancels (see
// TestClientTrySendOrResetUsesControlReserve).
func TestClientTrySendControlTellsAnEndingConnectionFromASaturatedOne(t *testing.T) {
	for name, closedWriter := range map[string]bool{"no writer": false, "a closed writer": true} {
		t.Run(name, func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			var cancelled atomic.Bool
			c := newBareClient()
			c.connCancel = func(cause error) { assert.ErrorIs(t, cause, errNoWorkerIdentity); cancelled.Store(true) }
			if closedWriter {
				c.writer = sendq.New(t.Context(), sendq.Config[*leapmuxv1.ConnectRequest]{
					Write:          func(context.Context, *leapmuxv1.ConnectRequest) error { return nil },
					Size:           connectReqSize,
					MaxBytes:       sendq.DefaultMaxBytes,
					ControlReserve: sendq.DefaultControlReserve,
				})
				c.writer.Close()
			}

			assert.Equal(t, controlConnectionEnding, c.trySendControl(inventoryMsg()))
			assert.False(t, c.TrySendOrReset(inventoryMsg()), "a frame for an ending connection is not queued")
			assert.False(t, cancelled.Load(), "an ending connection must not be cancelled again")
			assert.NotContains(t, logs.String(), "saturated")
		})
	}
}

// TestClientClockReturnsTheInstalledClock pins the accessor bootstrap.Wire
// reads to give the Service the SAME clock this client and its terminal manager
// run on. Without it the worker holds two clocks and a test can drive only one.
func TestClientClockReturnsTheInstalledClock(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	c := newTestClient(t, "http://localhost:0", WithClock(clock))
	assert.Same(t, clock, c.Clock(), "Clock must return the clock New installed")
	assert.Same(t, clock, c.Clock(), "the accessor must be stable across calls")
}

func TestCancelledConnectionPublishesDisconnectedState(t *testing.T) {
	independent := errors.New("independent connection failure")
	for _, test := range []struct {
		name     string
		failure  error
		preserve bool
	}{
		{"nil", nil, false},
		{"cancelled", context.Canceled, false},
		{"wrapped", fmt.Errorf("transport ended: %w", context.Canceled), false},

		{"independent", independent, true},
		{"joined", errors.Join(context.Canceled, independent), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, "http://localhost:0")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var transitions []bool
			var finalCause error
			client.OnWorkerIdentity = func(*leapmuxv1.WorkerIdentity) bool { return true }
			client.OnConnectionState = func(connected bool, err error) {
				transitions = append(transitions, connected)
				finalCause = err
			}
			attempts := 0
			err := client.connectWithReconnect(ctx, "token", func(context.Context, string) error {
				attempts++
				client.handleMessage(&leapmuxv1.ConnectResponse{Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner"}}})
				cancel()
				return test.failure
			}, newFastBackoff(), time.Second)
			require.NoError(t, err)
			assert.Equal(t, 1, attempts)
			assert.Equal(t, []bool{true, false}, transitions)
			if test.preserve {
				assert.ErrorIs(t, finalCause, independent)
			} else {
				assert.NoError(t, finalCause)
			}
		})
	}
}

func TestClassifyStreamRefusal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want streamRefusal
	}{
		{nil, refusalRetry},
		{errors.New("worker_deleted"), refusalRetry},
		{fmt.Errorf("receive: %w", websocket.CloseError{Code: 1008, Reason: channelwire.WorkerCloseReasonDeleted}), refusalWorkerDeleted},
		{fmt.Errorf("%w: %w", ErrKeysRefused, websocket.CloseError{Code: 1008, Reason: channelwire.WorkerCloseReasonKeysDiffer}), refusalKeysDiffer},
		{websocket.CloseError{Code: 1013, Reason: channelwire.WorkerCloseReasonDeleted}, refusalRetry},
		{websocket.CloseError{Code: 1008, Reason: "too_many_control_frames"}, refusalRetry},
		{websocket.CloseError{Code: 1008}, refusalRetry},
		{&UpgradeRefusedError{StatusCode: 401}, refusalRetry},
	} {
		assert.Equal(t, tc.want, classifyStreamRefusal(tc.err))
	}
}

func TestIsUnauthorizedUpgrade(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&UpgradeRefusedError{StatusCode: 401}, true},
		{fmt.Errorf("connect: %w", &UpgradeRefusedError{StatusCode: 401}), true},
		{&UpgradeRefusedError{StatusCode: 403}, false},
		{websocket.CloseError{Code: 1008, Reason: channelwire.WorkerCloseReasonDeleted}, false},
		{nil, false},
	} {
		assert.Equal(t, tc.want, isUnauthorizedUpgrade(tc.err))
	}
}

func TestConnectWithReconnect_RetriesAPolicyCloseWithAnUnknownReason(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newBareClient()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		attempts := 0
		err := c.connectWithReconnect(ctx, "token", func(context.Context, string) error {
			attempts++
			if attempts == 2 {
				cancel()
			}
			return websocket.CloseError{Code: 1008, Reason: "too_many_control_frames"}
		}, newFastBackoff(), time.Second)
		require.NoError(t, err)
		assert.Equal(t, 2, attempts)
	})
}

func TestReceiveEndError(t *testing.T) {
	t.Parallel()
	giveUp := errors.New("writer gave up")
	refused := websocket.CloseError{Code: 1008, Reason: channelwire.WorkerCloseReasonDeleted}
	for _, tc := range []struct {
		name                   string
		stop                   bool
		cause, errorRead, want error
	}{
		{"a stop before a refusal", true, nil, refused, context.Canceled},
		{"a stop before a closed socket", true, nil, net.ErrClosed, context.Canceled},
		{"a Hub refusal before a give-up", false, giveUp, refused, refused},
		{"Hub silence", false, errHubStoppedPinging, net.ErrClosed, errHubStoppedPinging},
		{"identity timeout", false, errNoWorkerIdentity, net.ErrClosed, errNoWorkerIdentity},
		{"control saturation", false, errControlQueueSaturated, net.ErrClosed, errControlQueueSaturated},
		{"a transport error", false, nil, io.EOF, io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			connCtx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			if tc.cause != nil {
				cancel(tc.cause)
			}
			if tc.stop {
				stop()
			}
			assert.ErrorIs(t, receiveEndError(tc.errorRead, ctx, connCtx), tc.want)
		})
	}
}

func TestConnect_ClosesWithTheHandshakeOnlyOnAStop(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "failure"}[failure], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var writeErr error
				if failure {
					writeErr = errors.New("broken pipe")
				}
				c, stream := writerStreamClient(t, writeErr, nil)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- c.Connect(ctx, "token") }()
				synctest.Wait()
				if !failure {
					cancel()
				}
				require.Error(t, <-result)
				want := "close"
				if failure {
					want = "close-now"
				}
				assert.Equal(t, want, stream.closedWith())
			})
		})
	}
}

func TestConnect_ReportsTheHubsCloseAfterAFailedHello(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, stream := writerStreamClient(t, nil, websocket.CloseError{Code: 1008, Reason: channelwire.WorkerCloseReasonDeleted})
		stream.helloErr = errors.New("broken pipe")
		close(stream.receiveOK)
		assert.Equal(t, refusalWorkerDeleted, classifyStreamRefusal(c.Connect(t.Context(), "token")))
	})
}

func inventoryOfSize(t *testing.T, size int) *leapmuxv1.WorkerTabInventory {
	t.Helper()
	tab := &leapmuxv1.TabIdent{TabId: strings.Repeat("x", size-32), TabType: leapmuxv1.TabType_TAB_TYPE_FILE}
	inventory := &leapmuxv1.WorkerTabInventory{Tabs: []*leapmuxv1.TabIdent{tab}}
	req := &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: inventory}}
	tab.TabId += strings.Repeat("x", size-proto.Size(req))
	require.Equal(t, size, proto.Size(req))
	return inventory
}

func TestConnect_SkipsAnOversizeTabInventory(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(map[int]string{0: "at the limit", 1: "over the limit"}[extra], func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			synctest.Test(t, func(t *testing.T) {
				c, stream := writerStreamClient(t, nil, nil)
				inventory := inventoryOfSize(t, channelwire.WorkerStreamMaxMessageBytes+extra)
				c.TabSyncProvider = func() *leapmuxv1.WorkerTabInventory { return inventory }
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- c.Connect(ctx, "token") }()
				synctest.Wait()
				stream.mu.Lock()
				sent := append([]*leapmuxv1.ConnectRequest(nil), stream.sent...)
				stream.mu.Unlock()
				require.NotEmpty(t, sent)
				assert.NotNil(t, sent[0].GetHello())
				if extra == 0 {
					require.Len(t, sent, 2)
					assert.NotNil(t, sent[1].GetWorkerTabInventory())
					assert.NotContains(t, logs.String(), "oversize")
				} else {
					assert.Equal(t, 1, len(sent))
					assert.Contains(t, logs.String(), "level=ERROR")
					assert.Contains(t, logs.String(), "the periodic reconciler still converges")
				}
				cancel()
				assert.ErrorIs(t, <-result, context.Canceled)
			})
		})
	}
}

// connectedSilenceClient creates both timers before advancing the identity
// deadline. The fake delivers its greeting only after both timer traps release.
func connectedSilenceClient(t *testing.T, redeliver bool) (*quartz.Mock, *fakeHubStream, context.CancelFunc, <-chan error) {
	t.Helper()
	c, stream := writerStreamClient(t, nil, nil)
	clock, ok := c.clock.(*quartz.Mock)
	require.True(t, ok, "the stream fixture must use a mock clock")
	identityNew, identityStop := testutil.NewTimerTraps(t, clock, hubIdentityTimerTag)
	silenceNew := clock.Trap().NewTimer(hubSilenceTimerTag)
	t.Cleanup(silenceNew.Close)
	greeting := &leapmuxv1.ConnectResponse{Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner"}}}
	stream.messages = []*leapmuxv1.ConnectResponse{greeting}
	if redeliver {
		stream.messages = append(stream.messages, proto.CloneOf(greeting), proto.CloneOf(greeting))
	}
	connected := make(chan struct{}, 3)
	c.OnConnectionState = func(ok bool, _ error) {
		if ok {
			connected <- struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- c.Connect(ctx, "token") }()
	testCtx := testutil.DeadlineContext(t)
	assert.Equal(t, hubSilenceTimeout, testutil.WaitForTimer(t, testCtx, silenceNew))
	assert.Equal(t, workerIdentityTimeout, testutil.WaitForTimer(t, testCtx, identityNew))
	close(stream.receiveOK)
	requireReceive(t, connected, "identity was not received")
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, workerIdentityTimeout, identityStop)
	return clock, stream, cancel, result
}

func TestConnect_DropsAHubThatStopsPinging(t *testing.T) {
	clock, stream, _, result := connectedSilenceClient(t, false)
	clock.Advance(hubSilenceTimeout - workerIdentityTimeout).MustWait(testutil.DeadlineContext(t))
	assert.ErrorIs(t, requireReceive(t, result, "silent Hub was not disconnected"), errHubStoppedPinging)
	assert.Equal(t, "close-now", stream.closedWith())
}

func TestConnect_KeepsAHubThatPings(t *testing.T) {
	clock, stream, cancel, result := connectedSilenceClient(t, true)
	reset := clock.Trap().TimerReset(hubSilenceTimerTag)
	defer reset.Close()
	ctx := testutil.DeadlineContext(t)
	for range 6 {
		stream.pings <- struct{}{}
		call := reset.MustWait(ctx)
		assert.Equal(t, hubSilenceTimeout, call.Duration)
		call.MustRelease(ctx)
		clock.Advance(hubSilenceTimeout - time.Second).MustWait(ctx)
		select {
		case err := <-result:
			t.Fatalf("Hub disconnected despite its pings: %v", err)
		default:
		}
	}
	cancel()
	assert.ErrorIs(t, requireReceive(t, result, "worker did not stop"), context.Canceled)
	assert.Equal(t, "close", stream.closedWith())
}

func TestCancelledConnectionPublishesDisconnectedStateAfterAHandlerReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, stream := writerStreamClient(t, nil, nil)
		stream.messages = []*leapmuxv1.ConnectResponse{
			{Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "owner"}}},
			{Payload: &leapmuxv1.ConnectResponse_ReconcileNudge{ReconcileNudge: &leapmuxv1.ReconcileNudge{}}},
		}
		close(stream.receiveOK)
		release := make(chan struct{})
		c.OnReconcileNudge = func() { <-release }
		states := make(chan bool, 2)
		causes := make(chan error, 1)
		c.OnConnectionState = func(ok bool, err error) {
			states <- ok
			if !ok {
				causes <- err
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- c.ConnectWithReconnect(ctx, "token") }()
		synctest.Wait()
		cancel()
		close(release)
		require.NoError(t, <-result)
		assert.True(t, <-states)
		assert.False(t, <-states)
		assert.NoError(t, <-causes)
	})
}
