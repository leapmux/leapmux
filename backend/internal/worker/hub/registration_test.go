package hub

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// mockConnectorClient implements WorkerConnectorServiceClient for
// testing the Register flow.
type mockConnectorClient struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler

	registerFn func(ctx context.Context, req *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error)
}

func (m *mockConnectorClient) Register(ctx context.Context, req *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
	return m.registerFn(ctx, req)
}

func (m *mockConnectorClient) Connect(_ context.Context) *connect.BidiStreamForClient[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse] {
	return nil
}

// fastRegisterRetry is the production registration policy with a tiny backoff
// ladder and the clock the test drives, so a retry costs no wall time.
func fastRegisterRetry(clock quartz.Clock) registerRetry {
	return registerRetry{
		backoff:        newFastBackoff(),
		attemptTimeout: registerAttemptTimeout,
		clock:          clock,
	}
}

func TestRegisterWithClient_RetriesUntilHubAvailable(t *testing.T) {
	var attempts atomic.Int32
	failCount := 3

	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, req *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			// Bearer must be passed through on every retry.
			assert.Equal(t, "Bearer key123", req.Header().Get("Authorization"))
			n := int(attempts.Add(1))
			if n <= failCount {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub down"))
			}
			return connect.NewResponse(&leapmuxv1.RegisterResponse{
				WorkerId:  "worker-123",
				AuthToken: "auth-token-abc",
			}), nil
		},
	}

	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, registerRetryTimerTag)

	type outcome struct {
		result *RegistrationResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := registerWithClient(t.Context(), mock, "key123", "0.0.1", nil, nil, nil, fastRegisterRetry(clock))
		done <- outcome{result: result, err: err}
	}()
	for range failCount {
		delay := testutil.WaitForTimer(t, testCtx, newTimer)
		testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	}

	var got outcome
	select {
	case got = <-done:
	case <-testCtx.Done():
		require.FailNow(t, "registration never returned after the hub came back")
	}
	require.NoError(t, got.err)
	assert.Equal(t, int32(failCount+1), attempts.Load(), "Register call count")
	assert.Equal(t, "worker-123", got.result.WorkerID)
	assert.Equal(t, "auth-token-abc", got.result.AuthToken)
	_, running := clock.Peek()
	assert.False(t, running, "the successful attempt must leave no retry armed")
}

func TestRegisterWithClient_RejectsEmptyKey(t *testing.T) {
	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			t.Fatal("Register must not be called with an empty key")
			return nil, nil
		},
	}
	clock := testutil.NewQuartzMock(t)
	_, err := registerWithClient(t.Context(), mock, "", "v", nil, nil, nil, fastRegisterRetry(clock))
	require.Error(t, err)
	_, running := clock.Peek()
	assert.False(t, running, "a missing key is permanent, so it must arm no retry")
}

func TestRegisterWithClient_StopsOnContextCancel(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			attempts.Add(1)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub down"))
		},
	}

	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, registerRetryTimerTag)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		_, err := registerWithClient(ctx, mock, "k", "0.0.1", nil, nil, nil, fastRegisterRetry(clock))
		errCh <- err
	}()

	// The first attempt failed and its retry is armed. Cancelling HERE lands
	// strictly inside the backoff window, which the sleep this replaces could
	// only approximate -- and the exact attempt count below is what that
	// certainty buys: the loop must stop without a second attempt.
	_ = testutil.WaitForTimer(t, testCtx, newTimer)
	cancel()
	stopTimer.MustWait(testCtx).MustRelease(testCtx)

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-testCtx.Done():
		require.FailNow(t, "registration ignored the cancelled context and waited out its backoff")
	}
	assert.Equal(t, int32(1), attempts.Load(), "cancellation must stop before a second attempt")
	_, running := clock.Peek()
	assert.False(t, running, "cancellation must release the retry timer")
}

func TestRegisterWithClient_DoesNotRetryUnauthenticated(t *testing.T) {
	// An invalid or already-consumed key surfaces as Unauthenticated.
	// We must NOT retry — every retry is another wasted RPC against a
	// hub that already told us "no". The user has to mint a fresh key.
	var attempts atomic.Int32
	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			attempts.Add(1)
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("nope"))
		},
	}
	clock := testutil.NewQuartzMock(t)
	_, err := registerWithClient(t.Context(), mock, "k", "v", nil, nil, nil, fastRegisterRetry(clock))
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load(), "Unauthenticated must not be retried")
	_, running := clock.Peek()
	assert.False(t, running, "a permanent rejection must arm no retry timer")
}

// TestRegisterWithClient_BackoffFollowsItsLadder pins the interval sequence the
// retry policy asks for, rung by rung.
//
// The delays the code REQUESTED, never the gaps they produced: a measured gap
// carries the host's timer granularity too, about 15.6ms on Windows, which
// dwarfs the 10ms first rung. That is why this used to assert only that the
// sequence never decreased -- an assertion a policy stuck at one interval also
// satisfies.
func TestRegisterWithClient_BackoffFollowsItsLadder(t *testing.T) {
	var attempts atomic.Int32
	failCount := 4

	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			n := int(attempts.Add(1))
			if n <= failCount {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub down"))
			}
			return connect.NewResponse(&leapmuxv1.RegisterResponse{WorkerId: "w", AuthToken: "t"}), nil
		},
	}

	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, registerRetryTimerTag)
	// Zero jitter, so the ladder below is the one the policy states.
	retry := registerRetry{
		backoff:        backoffutil.NewBackoff(10*time.Millisecond, 100*time.Millisecond, 0),
		attemptTimeout: registerAttemptTimeout,
		clock:          clock,
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := registerWithClient(t.Context(), mock, "k", "0.0.1", nil, nil, nil, retry)
		errCh <- err
	}()
	want := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		80 * time.Millisecond,
	}
	for i, expected := range want {
		delay := testutil.WaitForTimer(t, testCtx, newTimer)
		assert.Equal(t, expected, delay, "backoff interval %d", i+1)
		testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	}

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-testCtx.Done():
		require.FailNow(t, "registration never returned after its backoff ladder")
	}
	assert.Equal(t, int32(failCount+1), attempts.Load(), "one attempt per rung, then the success")
}

// TestRegisterWithClient_LimitsOneAttempt pins the limit that keeps a stalled
// hub from wedging worker startup.
//
// Register runs on the HTTP2Only lane, which carries NO http.Client timeout by
// design: that lane also carries the worker's bidirectional Connect stream,
// whose body ends only when the stream does. The retry loop sits BELOW the
// call, so before this an attempt that never returned took the loop with it,
// and `leapmux worker` hung at startup for ever with no retry and no log line.
//
// The attempt limit is a real 20ms here, not a mocked one: it is a context
// deadline, and context.WithTimeout reads the real clock. Only the wait
// BETWEEN attempts is on the test's clock.
func TestRegisterWithClient_LimitsOneAttempt(t *testing.T) {
	var attempts atomic.Int32
	var sawDeadline atomic.Bool

	mock := &mockConnectorClient{
		registerFn: func(ctx context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			if _, ok := ctx.Deadline(); ok {
				sawDeadline.Store(true)
			}
			if attempts.Add(1) == 1 {
				// The hub that accepted the connection and then said nothing.
				// Without a per-attempt limit this blocks for ever.
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return connect.NewResponse(&leapmuxv1.RegisterResponse{
				WorkerId: "w-1", AuthToken: "tok",
			}), nil
		},
	}

	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, registerRetryTimerTag)
	retry := registerRetry{
		backoff:        newFastBackoff(),
		attemptTimeout: 20 * time.Millisecond,
		clock:          clock,
	}

	type outcome struct {
		result *RegistrationResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := registerWithClient(t.Context(), mock, "key123", "0.0.1", nil, nil, nil, retry)
		done <- outcome{result: result, err: err}
	}()
	delay := testutil.WaitForTimer(t, testCtx, newTimer)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)

	var got outcome
	select {
	case got = <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the stalled attempt never ended")
	}
	require.NoError(t, got.err, "a stalled attempt must end and let the retry run")
	assert.Equal(t, "w-1", got.result.WorkerID)
	assert.True(t, sawDeadline.Load(), "each attempt must carry its own deadline")
	assert.EqualValues(t, 2, attempts.Load(), "the stalled attempt is abandoned and retried exactly once")
}

// The per-attempt limit must not swallow the CALLER's cancellation: a worker
// shutting down during registration still stops rather than retrying.
func TestRegisterWithClient_AttemptLimitDoesNotHideCallerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	mock := &mockConnectorClient{
		registerFn: func(ctx context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	clock := testutil.NewQuartzMock(t)
	retry := registerRetry{
		backoff:        newFastBackoff(),
		attemptTimeout: time.Minute,
		clock:          clock,
	}
	_, err := registerWithClient(ctx, mock, "k", "0.0.1", nil, nil, nil, retry)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	_, running := clock.Peek()
	assert.False(t, running, "a cancelled caller must not leave a retry armed")
}
