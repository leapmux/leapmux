package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"connectrpc.com/connect/v2/connectproto"
	"github.com/coder/quartz"
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
// The clock is not optional here even though these tests never advance it. New
// is the only place that installs one, and a successful enqueue stamps
// lastSendTime through it -- so a bare `&Client{}` that reaches Send with a
// live writer panics on a nil interface instead of failing an assertion. One
// helper means no test has to decide whether its path reaches the clock.
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
		return refusalReasonError(t, connect.CodePermissionDenied,
			leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_KEYS_DIFFER)
	}

	err := client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeysRefused)
	assert.Equal(t, int32(1), attempts.Load(), "a key refusal must not be retried")
	assert.Zero(t, deregisters.Load(), "a key refusal must not clear the worker's state")
}

// A refusal that carries WORKER_DELETED stops the loop and runs OnDeregister.
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
				return refusalReasonError(t, connect.CodeUnauthenticated,
					leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED)
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
		return connect.NewError(connect.CodePermissionDenied, "forbidden")
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
	assert.Zero(t, deregisters.Load(), "only a WorkerConnectRefusal detail may run OnDeregister")
}

// A bare Unauthenticated is not proof that this worker was deleted. A proxy in
// front of the hub answers 401 by itself, and a hub database restored from an
// older backup does not know the worker's token — neither may destroy the
// worker's identity. Only the WorkerConnectRefusal detail makes a refusal
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
		return connect.NewError(connect.CodeUnauthenticated, "unauthorized")
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
	assert.Zero(t, deregisters.Load(), "only a WorkerConnectRefusal detail may run OnDeregister")
}

// A worker that the hub does not know retries for ever, so it must state its own
// remedy. The record appears exactly once per run of consecutive bare
// Unauthenticated refusals, at the attempt that reaches the threshold, and any
// other outcome starts the count again.
func TestConnectWithReconnect_LogsTheRemedyOnceForAWorkerTheHubDoesNotKnow(t *testing.T) {
	bareUnauthenticated := connect.NewError(connect.CodeUnauthenticated, "unauthorized")
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
			errs: repeat(connect.NewError(connect.CodePermissionDenied, "forbidden"), 2*bareUnauthenticatedRemedyThreshold),
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

// refusalReasonError builds the wire shape a refusal arrives in: a connect.Error
// with one WorkerConnectRefusal detail.
func refusalReasonError(t *testing.T, code connect.Code, reason leapmuxv1.WorkerConnectRefusalReason) *connect.Error {
	t.Helper()
	detail, err := connectproto.NewErrorDetail(&leapmuxv1.WorkerConnectRefusal{Reason: reason})
	require.NoError(t, err)
	return connect.NewError(code, "refused").WithDetail(detail)
}

func TestConnectRefusalReason(t *testing.T) {
	t.Parallel()

	keysDiffer := leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_KEYS_DIFFER
	deleted := leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED
	unspecified := leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_UNSPECIFIED

	otherTypeDetail := &connect.ErrorDetail{Type: "leapmux.v1.SomeOtherMessage", Value: []byte{0x08, 0x01}}
	undecodableDetail := &connect.ErrorDetail{Type: "leapmux.v1.WorkerConnectRefusal", Value: []byte{0xFF, 0xFF}}

	for name, tc := range map[string]struct {
		err  error
		want leapmuxv1.WorkerConnectRefusalReason
	}{
		"nil": {err: nil, want: unspecified},
		"a non-connect error whose text contains the words": {
			err: errors.New("unauthenticated: worker was deleted"), want: unspecified,
		},
		"a bare code": {
			err: connect.NewError(connect.CodeUnauthenticated, "invalid auth token"), want: unspecified,
		},
		"the detail under a receive wrap": {
			err:  fmt.Errorf("receive: %w", refusalReasonError(t, connect.CodeUnauthenticated, deleted)),
			want: deleted,
		},
		"the detail under a hello wrap": {
			err:  fmt.Errorf("hello: %w", refusalReasonError(t, connect.CodePermissionDenied, keysDiffer)),
			want: keysDiffer,
		},
		"the detail under the ErrKeysRefused double wrap": {
			err: fmt.Errorf("%w: %w", ErrKeysRefused,
				refusalReasonError(t, connect.CodePermissionDenied, keysDiffer)),
			want: keysDiffer,
		},
		"a detail of another type": {
			err:  connect.NewError(connect.CodeInternal, "x").WithDetail(otherTypeDetail),
			want: unspecified,
		},
		"the right type with an undecodable value": {
			err:  connect.NewError(connect.CodeInternal, "x").WithDetail(undecodableDetail),
			want: unspecified,
		},
		"a reason of UNSPECIFIED": {
			err:  refusalReasonError(t, connect.CodeUnauthenticated, unspecified),
			want: unspecified,
		},
		"a decodable detail of another type, then the refusal": {
			err: func() error {
				other, err := connectproto.NewErrorDetail(&leapmuxv1.Heartbeat{})
				require.NoError(t, err)
				refusal, err := connectproto.NewErrorDetail(&leapmuxv1.WorkerConnectRefusal{Reason: keysDiffer})
				require.NoError(t, err)
				return connect.NewError(connect.CodePermissionDenied, "x").WithDetail(other).WithDetail(refusal)
			}(),
			want: keysDiffer,
		},
		"two details, the refusal second": {
			err: func() error {
				detail, err := connectproto.NewErrorDetail(&leapmuxv1.WorkerConnectRefusal{Reason: deleted})
				require.NoError(t, err)
				return connect.NewError(connect.CodeUnauthenticated, "x").WithDetail(otherTypeDetail).WithDetail(detail)
			}(),
			want: deleted,
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, connectRefusalReason(tc.err))
		})
	}
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
			failure := refusalReasonError(t, connect.CodePermissionDenied, leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_KEYS_DIFFER)
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
	c.connCancel = func() { cancelled.Store(true) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watchForIdentity(ctx)

	delay := testutil.WaitForTimer(t, testCtx, newTimer)
	assert.Equal(t, workerIdentityTimeout, delay)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	assert.True(t, cancelled.Load(),
		"the watchdog must cancel the connection when the Hub omits WorkerIdentity")
}

func TestWatchForIdentity_DoesNotCancelWhenIdentityReceived(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	c := newTestClient(t, "http://localhost:0", WithClock(clock))
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubIdentityTimerTag)
	c.identityReceived.Store(true)
	var cancelled atomic.Bool
	c.connCancel = func() { cancelled.Store(true) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watchForIdentity(ctx)

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
			c.connCancel = func() { cancelled.Store(true) }
			// The handler reports the owner, as nudgeOnNewOwner does after
			// ApplyWorkerIdentity refused an empty owner.
			var deliveries int
			c.OnWorkerIdentity = func(*leapmuxv1.WorkerIdentity) bool {
				deliveries++
				return tc.holdsOwner
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go c.watchForIdentity(ctx)
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

	c := newBareClient()
	c.writer = writer
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

// fakeConnectStream is a connect.ClientStream the give-up tests drive by hand,
// shaped after connecthttp's own duplexHTTPCall:
//
//   - The hello is accepted.
//   - Every later Send fails with the configured write error.
//   - Receive answers only once the test releases it. It answers the queued
//     messages first, and then the way duplexHTTPCall.Read does: a bare
//     Canceled when the call's context ended first, the peer's status
//     otherwise.
type fakeConnectStream struct {
	ctx context.Context

	writeErr  error
	refusal   error
	receiveOK chan struct{}
	closed    chan struct{}
	closeOnce sync.Once

	mu    sync.Mutex
	sends int
	// messages are the frames the Hub sent before it ended the stream. Receive
	// answers them in order, once the test releases it, before the status.
	messages []*leapmuxv1.ConnectResponse
}

func newFakeConnectStream(ctx context.Context, writeErr, refusal error) *fakeConnectStream {
	return &fakeConnectStream{
		ctx:       ctx,
		writeErr:  writeErr,
		refusal:   refusal,
		receiveOK: make(chan struct{}),
		closed:    make(chan struct{}),
	}
}

func (s *fakeConnectStream) SendHeaders() error { return nil }

func (s *fakeConnectStream) Send(any) error {
	s.mu.Lock()
	s.sends++
	n := s.sends
	s.mu.Unlock()
	if n == 1 {
		// The hello. The transport takes it from the request pipe, so it
		// succeeds even when the Hub refuses before reading it.
		return nil
	}
	return s.writeErr
}

func (s *fakeConnectStream) CloseSend() error { return nil }

func (s *fakeConnectStream) Receive(msg any) error {
	select {
	case <-s.receiveOK:
	case <-s.closed:
		return connect.NewError(connect.CodeCanceled, "stream closed")
	}
	s.mu.Lock()
	var next *leapmuxv1.ConnectResponse
	if len(s.messages) > 0 {
		next, s.messages = s.messages[0], s.messages[1:]
	}
	s.mu.Unlock()
	if next != nil {
		proto.Merge(msg.(*leapmuxv1.ConnectResponse), next)
		return nil
	}
	if err := s.ctx.Err(); err != nil {
		// duplexHTTPCall.Read checks its context before it reads the response,
		// so a cancel that wins the race hides the peer's status.
		return connect.NewError(connect.CodeCanceled, err.Error())
	}
	return s.refusal
}

func (s *fakeConnectStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// fakeConnectTransport hands back the one stream the test prepared.
type fakeConnectTransport struct{ stream *fakeConnectStream }

func (t *fakeConnectTransport) NewClientStream(ctx context.Context, _ connect.Spec) (connect.ClientStream, error) {
	t.stream.ctx = ctx
	return t.stream, nil
}

// writerStreamClient builds the Client the give-up race needs: a real New over
// a dummy endpoint, whose one RPC is served by the fake stream, and whose
// TabSyncProvider makes Connect write a second frame right after the hello --
// the write that hits an already-refused stream.
func writerStreamClient(t *testing.T, writeErr, refusal error) (*Client, *fakeConnectStream) {
	t.Helper()
	endpoint, err := hubtransport.New("http://127.0.0.1:1")
	require.NoError(t, err)
	c := New(endpoint, WithClock(testutil.NewQuartzMock(t)))
	stream := newFakeConnectStream(context.Background(), writeErr, refusal)
	c.connector = leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(&fakeConnectTransport{stream: stream}))
	c.TabSyncProvider = func() *leapmuxv1.WorkerTabInventory { return &leapmuxv1.WorkerTabInventory{} }
	return c, stream
}

// When the Hub has already refused a connection, a later write fails with an
// error that wraps io.EOF (connecthttp's duplexHTTPCall.Send maps a closed
// request pipe to io.EOF) while Receive still holds the Hub's real status. The
// writer's give-up hook must leave that stream to the reader instead of
// cancelling it: a cancel makes duplexHTTPCall.Read check its context first and
// report a bare Canceled, which discards the refusal AND its
// WorkerConnectRefusal detail. The worker then retries a refusal it should
// obey.
func TestConnect_ReturnsTheHubStatusWhenAWriteHitsTheEndedStream(t *testing.T) {
	t.Run("an io.EOF write leaves the refusal to the reader", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// connecthttp's envelopeWriter.write builds exactly this shape.
			writeErr := connect.Errorf(connect.CodeUnknown, "write envelope: %s", io.EOF).WithCause(io.EOF)
			// The refusal the Hub pairs with WORKER_DELETED.
			refusal := refusalReasonError(t, connect.CodeUnauthenticated,
				leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED)
			c, stream := writerStreamClient(t, writeErr, refusal)

			errCh := make(chan error, 1)
			go func() { errCh <- c.Connect(context.Background(), "token") }()

			// Returns only once every goroutine in the bubble is durably
			// blocked, which includes the writer having run its give-up hook
			// and exited.
			synctest.Wait()
			close(stream.receiveOK)

			err := <-errCh
			require.Error(t, err)
			assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
				"the reader must report the Hub's status, not a Canceled the writer caused")
			assert.Equal(t, leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED,
				connectRefusalReason(err), "the refusal's detail must survive the write failure")
		})
	})

	t.Run("a write that the Hub did not cause still cancels", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c, stream := writerStreamClient(t, errors.New("broken pipe"),
				connect.NewError(connect.CodeUnauthenticated, "auth_token required"))

			errCh := make(chan error, 1)
			go func() { errCh <- c.Connect(context.Background(), "token") }()

			synctest.Wait()
			close(stream.receiveOK)

			err := <-errCh
			require.Error(t, err)
			assert.Equal(t, connect.CodeCanceled, connect.CodeOf(err),
				"a genuine write failure must still cancel the connection")
		})
	})
}

// The Hub can send a frame that needs a control reply and then end the stream,
// for example a ChannelOpen just before it refuses the connection. The write
// that hits the ended stream fails with io.EOF, and the writer gives up without
// cancelling, so its closed writer stays installed until the receive loop ends.
// The reply to the buffered frame then finds a CLOSED writer, not a saturated
// one: it must not warn that the control queue is saturated, and it must not
// cancel the connection, or Receive reports Canceled and the Hub's refusal with
// its detail is lost.
func TestConnect_AControlReplyOnAnEndedStreamKeepsTheHubStatus(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	synctest.Test(t, func(t *testing.T) {
		writeErr := connect.Errorf(connect.CodeUnknown, "write envelope: %s", io.EOF).WithCause(io.EOF)
		refusal := refusalReasonError(t, connect.CodeUnauthenticated,
			leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED)
		c, stream := writerStreamClient(t, writeErr, refusal)
		keys, err := noiseutil.GenerateCompositeKeypair()
		require.NoError(t, err)
		c.SetChannelMgr(channel.NewManager(keys, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			c.Send, c.TrySendOrReset, 0, 0))
		stream.messages = []*leapmuxv1.ConnectResponse{{
			RequestId: "open-1",
			Payload: &leapmuxv1.ConnectResponse_ChannelOpen{
				ChannelOpen: &leapmuxv1.ChannelOpenRequest{ChannelId: "channel-1"},
			},
		}}

		errCh := make(chan error, 1)
		go func() { errCh <- c.Connect(context.Background(), "token") }()

		// The writer has run its give-up hook and exited once every goroutine in
		// the bubble is durably blocked.
		synctest.Wait()
		close(stream.receiveOK)

		err = <-errCh
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
			"the reply on the closed writer must not replace the Hub's status with Canceled")
		assert.Equal(t, leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED,
			connectRefusalReason(err), "the refusal's detail must survive the dropped reply")
		assert.Contains(t, logs.String(), "rejecting channel open",
			"the receive loop handled the ChannelOpen, so its reply met the closed writer")
		assert.NotContains(t, logs.String(), "saturated", "a closed writer is not a saturated control queue")
		assert.NotContains(t, logs.String(), "over budget", "a closed writer is not over budget")
	})
}

// The connect writer's give-up hook cancels the connection on a write failure
// that the Hub did not cause by ending the stream, so ConnectWithReconnect
// re-establishes it.
func TestClientWriteFailureCancelsConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cancelled atomic.Bool
	c := newBareClient()
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
		OnGiveUp:      c.connectWriterGiveUpHook(func() { c.cancelConn() }),
	})
	t.Cleanup(func() { c.writer.Close() })

	require.NoError(t, c.Send(heartbeatMsg()))
	require.Eventually(t, cancelled.Load, 2*time.Second, 5*time.Millisecond,
		"a write failure the Hub did not cause must cancel the connection")
}

// A write that failed because the peer ended the stream wraps io.EOF, and the
// receive loop reports the peer's status. Every other give-up still cancels.
func TestWriterGiveUpCancelsConnection(t *testing.T) {
	t.Parallel()

	eofShape := connect.Errorf(connect.CodeUnknown, "write envelope: %s", io.EOF).WithCause(io.EOF)
	for name, tc := range map[string]struct {
		reason sendq.GiveUpReason
		err    error
		want   bool
	}{
		"a write error wrapping io.EOF directly": {
			reason: sendq.GiveUpWriteError, err: io.EOF, want: false,
		},
		"a write error wrapping io.EOF": {
			reason: sendq.GiveUpWriteError, err: fmt.Errorf("write: %w", io.EOF), want: false,
		},
		"the production write-envelope shape": {
			reason: sendq.GiveUpWriteError, err: eofShape, want: false,
		},
		"a write error the Hub did not cause": {
			reason: sendq.GiveUpWriteError, err: errors.New("broken pipe"), want: true,
		},
		"a write timeout wrapping io.EOF": {
			reason: sendq.GiveUpWriteTimeout, err: eofShape, want: true,
		},
		"a stall wrapping io.EOF": {
			reason: sendq.GiveUpStall, err: eofShape, want: true,
		},
		"an over-budget give-up wrapping io.EOF": {
			reason: sendq.GiveUpOverBudget, err: eofShape, want: true,
		},
		"pool pressure wrapping io.EOF": {
			reason: sendq.GiveUpPoolPressure, err: eofShape, want: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, writerGiveUpCancelsConnection(tc.reason, tc.err))
		})
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
			c.connCancel = func() { cancelled.Store(true) }
			if closedWriter {
				c.writer = sendq.New(t.Context(), sendq.Config[*leapmuxv1.ConnectRequest]{
					Write:          func(context.Context, *leapmuxv1.ConnectRequest) error { return nil },
					Size:           connectReqSize,
					MaxBytes:       sendq.DefaultMaxBytes,
					ControlReserve: sendq.DefaultControlReserve,
				})
				c.writer.Close()
			}

			assert.Equal(t, controlConnectionEnding, c.trySendControl(heartbeatMsg()))
			assert.False(t, c.TrySendOrReset(heartbeatMsg()), "a frame for an ending connection is not queued")
			assert.False(t, cancelled.Load(), "an ending connection must not be cancelled again")
			assert.NotContains(t, logs.String(), "saturated")
		})
	}
}

// lastSendTime must advance on enqueue so the idle heartbeat does not fire
// spuriously while the queue still has work.
func TestClientLastSendTimeAdvancesOnEnqueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	clock := testutil.NewQuartzMock(t)
	c := &Client{clock: clock}
	now := clock.Now()
	c.lastSendTime = now.Add(-time.Hour)
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
	assert.Equal(t, now, after, "Send must use the client's clock for its enqueue timestamp")
	assert.True(t, after.After(before), "lastSendTime must advance on enqueue")

	// Reset before TrySend so the assertion does not depend on wall-clock
	// ticks between two rapid enqueues (Windows time.Now can share a tick).
	c.mu.Lock()
	c.lastSendTime = now.Add(-time.Hour)
	before = c.lastSendTime
	c.mu.Unlock()
	require.True(t, c.TrySend(heartbeatMsg()))
	c.mu.Lock()
	after = c.lastSendTime
	c.mu.Unlock()
	assert.Equal(t, now, after, "TrySend must use the client's clock for its enqueue timestamp")
	assert.True(t, after.After(before), "lastSendTime must advance on TrySend enqueue")
}

// TestClientLastSendTimeNeverMovesBackwards pins the monotonicity guard in
// markEnqueued. The clock read sits outside c.mu so a trap on it cannot park
// the whole client, which means two concurrent enqueues can read the clock in
// one order and reach the lock in the other. The later instant must win.
//
// heartbeatIdleTimeout is 5s, so a regression here is not a wrong heartbeat --
// it is lastSendTime drifting backwards under load, which makes the idle window
// longer than the connection has really been quiet.
func TestClientLastSendTimeNeverMovesBackwards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	clock := testutil.NewQuartzMock(t)
	c := &Client{clock: clock}
	// A stamp from an enqueue whose clock read happened LATER, but which won the
	// race to the lock first. The enqueue below reads an earlier instant.
	ahead := clock.Now().Add(time.Minute)
	c.lastSendTime = ahead

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
	assert.Equal(t, ahead, after,
		"an enqueue that read an earlier instant must not pull lastSendTime backwards")
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

// TestEnqueueStampDoesNotCarryTheConnectTag pins the tag split. One tag for two
// call sites made a trap catch both, so a test that held the connect-time stamp
// also parked every enqueue on the send path.
func TestEnqueueStampDoesNotCarryTheConnectTag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NotEqual(t, hubSendTimestampTag, hubConnectTimestampTag,
		"the two stamps must be separately trappable")

	clock := testutil.NewQuartzMock(t)
	c := &Client{clock: clock}
	connectStamps := clock.Trap().Now(hubConnectTimestampTag)
	defer connectStamps.Close()

	c.writer = sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write:         func(context.Context, *leapmuxv1.ConnectRequest) error { return nil },
		Size:          connectReqSize,
		MaxBytes:      sendq.DefaultMaxBytes,
		FrameOverhead: sendq.DefaultFrameOverhead,
	})
	t.Cleanup(func() { c.writer.Close() })

	// A trapped call parks its caller, so a Send that returned at all proves the
	// enqueue stamp did not match the connect trap.
	require.NoError(t, c.Send(heartbeatMsg()))
	c.mu.Lock()
	stamped := c.lastSendTime
	c.mu.Unlock()
	assert.Equal(t, clock.Now(), stamped, "the enqueue must still stamp its own instant")
}

func TestHeartbeatLoopUsesClockAndStopsTicker(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	written := make(chan *leapmuxv1.ConnectRequest, 1)
	start := clock.Now()
	c := &Client{clock: clock, lastSendTime: start}
	c.writer = sendq.New(ctx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write: func(_ context.Context, msg *leapmuxv1.ConnectRequest) error {
			written <- msg
			return nil
		},
		Size:          connectReqSize,
		MaxBytes:      sendq.DefaultMaxBytes,
		FrameOverhead: sendq.DefaultFrameOverhead,
	})
	t.Cleanup(c.writer.Close)

	newTicker := clock.Trap().NewTicker(hubHeartbeatTickerTag)
	defer newTicker.Close()
	stopTicker := clock.Trap().TickerStop(hubHeartbeatTickerTag)
	defer stopTicker.Close()
	// The idle measurement is the loop's synchronization point AND its subject.
	// Its trapped argument is the lastSendTime the loop read, so each tick
	// reports what the PREVIOUS tick did: an unchanged argument proves the
	// previous tick sent nothing, and a moved one proves the heartbeat stamped
	// its enqueue on this clock. The frame's arrival orders only the writer
	// goroutine, so the sendStamps trap below is what orders markEnqueued's own
	// clock read against the next advance.
	idleReads := clock.Trap().Since(hubHeartbeatIdleTag)
	defer idleReads.Close()
	// The enqueue stamp is the value under test, so the test must own the
	// instant markEnqueued reads. Nothing else orders that read: Send returns
	// once the frame is QUEUED, so the loop's clock read runs beside the writer
	// goroutine and beside the next Advance. Without this trap the fourth tick
	// moved the clock first and the stamp reported 8s where 6s belongs.
	sendStamps := clock.Trap().Now(hubSendTimestampTag)
	defer sendStamps.Close()
	done := make(chan struct{})
	go func() {
		c.heartbeatLoop(ctx)
		close(done)
	}()
	call := newTicker.MustWait(testCtx)
	assert.Equal(t, 2*time.Second, call.Duration)
	call.MustRelease(testCtx)

	// tick returns the lastSendTime the loop measured its idle window against.
	tick := func() time.Time {
		t.Helper()
		advance := clock.Advance(2 * time.Second)
		idle := idleReads.MustWait(testCtx)
		measuredAgainst := idle.Time
		idle.MustRelease(testCtx)
		advance.MustWait(testCtx)
		return measuredAgainst
	}

	// Ticks 1 and 2 sit at 2s and 4s idle, both under the 5s timeout.
	assert.Equal(t, start, tick(), "the first tick measures idle time from the last enqueue")
	assert.Equal(t, start, tick(), "the first tick was under the idle timeout, so it sent nothing")
	// Tick 3 reaches 6s of idle time and sends.
	assert.Equal(t, start, tick(), "the second tick was under the idle timeout, so it sent nothing")
	heartbeatAt := clock.Now()
	select {
	case msg := <-written:
		require.NotNil(t, msg.GetHeartbeat())
	case <-testCtx.Done():
		require.FailNow(t, "the idle heartbeat did not run on the third tick")
	}
	// Release the stamp BEFORE the next advance. Now reads the clock after the
	// release and completes before MustRelease returns, so the stamp is the
	// instant the heartbeat went out, not the one the fourth tick moves to.
	sendStamps.MustWait(testCtx).MustRelease(testCtx)
	assert.Equal(t, heartbeatAt, tick(),
		"the heartbeat enqueue timestamp must come from the client clock")

	cancel()
	stopTicker.MustWait(testCtx).MustRelease(testCtx)
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the heartbeat loop did not stop after cancellation")
	}
	_, running := clock.Peek()
	assert.False(t, running, "the heartbeat loop must stop its ticker")
}

// TestHeartbeatLoopStopsWhenTheSendFails covers the loop's error path: a
// heartbeat that cannot reach the transport ends the loop. Without it the loop
// keeps ticking against a connection that is already gone, and each tick logs
// the same failure for the life of the process.
func TestHeartbeatLoopStopsWhenTheSendFails(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	// No writer, so Send reports the transport is gone. That is the ordinary
	// end of a disconnect, and the loop must return on it rather than on the
	// context, which stays live for the whole test.
	c := &Client{clock: clock, lastSendTime: clock.Now()}

	newTicker := clock.Trap().NewTicker(hubHeartbeatTickerTag)
	defer newTicker.Close()
	stopTicker := clock.Trap().TickerStop(hubHeartbeatTickerTag)
	defer stopTicker.Close()
	idleReads := clock.Trap().Since(hubHeartbeatIdleTag)
	defer idleReads.Close()

	done := make(chan struct{})
	go func() {
		c.heartbeatLoop(ctx)
		close(done)
	}()
	newTicker.MustWait(testCtx).MustRelease(testCtx)

	// Three ticks reach 6s of idle time, which is past the 5s timeout.
	for range 3 {
		advance := clock.Advance(2 * time.Second)
		idleReads.MustWait(testCtx).MustRelease(testCtx)
		advance.MustWait(testCtx)
	}

	stopTicker.MustWait(testCtx).MustRelease(testCtx)
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the heartbeat loop kept ticking after its send failed")
	}
	require.NoError(t, ctx.Err(), "the loop must have ended on the send, not on the context")
	_, running := clock.Peek()
	assert.False(t, running, "a loop that gave up must stop its ticker")
}
