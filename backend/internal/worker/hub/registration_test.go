package hub

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := registerWithClient(ctx, mock, "key123", "0.0.1", nil, nil, nil, newFastBackoff(), registerAttemptTimeout)
	require.NoError(t, err)

	assert.Equal(t, int32(failCount+1), attempts.Load(), "Register call count")
	assert.Equal(t, "worker-123", result.WorkerID)
	assert.Equal(t, "auth-token-abc", result.AuthToken)
}

func TestRegisterWithClient_RejectsEmptyKey(t *testing.T) {
	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			t.Fatal("Register must not be called with an empty key")
			return nil, nil
		},
	}
	_, err := registerWithClient(context.Background(), mock, "", "v", nil, nil, nil, newFastBackoff(), registerAttemptTimeout)
	require.Error(t, err)
}

func TestRegisterWithClient_StopsOnContextCancel(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			attempts.Add(1)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub down"))
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := registerWithClient(ctx, mock, "k", "0.0.1", nil, nil, nil, newFastBackoff(), registerAttemptTimeout)
	assert.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, attempts.Load(), int32(1))
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
	_, err := registerWithClient(context.Background(), mock, "k", "v", nil, nil, nil, newFastBackoff(), registerAttemptTimeout)
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load(), "Unauthenticated must not be retried")
}

// recordingBackoff records each Next result so tests can assert
// on the values requested rather than wall-clock elapsed time, which is
// noisy on Windows where the scheduler tick (~15.6ms) dwarfs 10ms sleeps.
type recordingBackoff struct {
	inner     *backoffutil.Backoff
	intervals []time.Duration
}

func (r *recordingBackoff) Next() time.Duration {
	d := r.inner.Next()
	r.intervals = append(r.intervals, d)
	return d
}

func (r *recordingBackoff) Reset() { r.inner.Reset() }

func TestRegisterWithClient_BackoffIncreases(t *testing.T) {
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

	inner := backoffutil.NewBackoff(10*time.Millisecond, 100*time.Millisecond, 0)
	rec := &recordingBackoff{inner: inner}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := registerWithClient(ctx, mock, "k", "0.0.1", nil, nil, nil, rec, registerAttemptTimeout)
	require.NoError(t, err)

	require.Len(t, rec.intervals, failCount,
		"expected one backoff interval per failed attempt")
	for i := 1; i < len(rec.intervals); i++ {
		assert.GreaterOrEqual(t, rec.intervals[i], rec.intervals[i-1])
	}
}

// TestRegisterWithClient_LimitsOneAttempt pins the limit that keeps a stalled
// hub from wedging worker startup.
//
// Register runs on the HTTP2Only lane, which carries NO http.Client timeout by
// design: that lane also carries the worker's bidirectional Connect stream,
// whose body ends only when the stream does. The retry loop sits BELOW the
// call, so before this an attempt that never returned took the loop with it,
// and `leapmux worker` hung at startup for ever with no retry and no log line.
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := registerWithClient(ctx, mock, "key123", "0.0.1", nil, nil, nil, newFastBackoff(), 20*time.Millisecond)

	require.NoError(t, err, "a stalled attempt must end and let the retry run")
	assert.Equal(t, "w-1", result.WorkerID)
	assert.True(t, sawDeadline.Load(), "each attempt must carry its own deadline")
	assert.EqualValues(t, 2, attempts.Load(), "the stalled attempt is abandoned and retried exactly once")
}

// The per-attempt limit must not swallow the CALLER's cancellation: a worker
// shutting down during registration still stops rather than retrying.
func TestRegisterWithClient_AttemptLimitDoesNotHideCallerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := &mockConnectorClient{
		registerFn: func(ctx context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	_, err := registerWithClient(ctx, mock, "k", "0.0.1", nil, nil, nil, newFastBackoff(), time.Minute)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
