package hub

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
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
				WorkerId:     "worker-123",
				AuthToken:    "auth-token-abc",
				RegisteredBy: "user-1",
			}), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := registerWithClient(ctx, mock, "key123", "0.0.1", nil, nil, nil, newFastBackoff())
	require.NoError(t, err)

	assert.Equal(t, int32(failCount+1), attempts.Load(), "Register call count")
	assert.Equal(t, "worker-123", result.WorkerID)
	assert.Equal(t, "auth-token-abc", result.AuthToken)
	assert.Equal(t, "user-1", result.RegisteredBy)
}

func TestRegisterWithClient_RejectsEmptyKey(t *testing.T) {
	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RegisterRequest]) (*connect.Response[leapmuxv1.RegisterResponse], error) {
			t.Fatal("Register must not be called with an empty key")
			return nil, nil
		},
	}
	_, err := registerWithClient(context.Background(), mock, "", "v", nil, nil, nil, newFastBackoff())
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

	_, err := registerWithClient(ctx, mock, "k", "0.0.1", nil, nil, nil, newFastBackoff())
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
	_, err := registerWithClient(context.Background(), mock, "k", "v", nil, nil, nil, newFastBackoff())
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load(), "Unauthenticated must not be retried")
}

// recordingBackoff records each NextBackOff result so tests can assert
// on the values requested rather than wall-clock elapsed time, which is
// noisy on Windows where the scheduler tick (~15.6ms) dwarfs 10ms sleeps.
type recordingBackoff struct {
	inner     backoff.BackOff
	intervals []time.Duration
}

func (r *recordingBackoff) NextBackOff() time.Duration {
	d := r.inner.NextBackOff()
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

	inner := backoff.NewExponentialBackOff()
	inner.InitialInterval = 10 * time.Millisecond
	inner.MaxInterval = 100 * time.Millisecond
	inner.Multiplier = 2.0
	inner.RandomizationFactor = 0
	inner.Reset()
	rec := &recordingBackoff{inner: inner}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := registerWithClient(ctx, mock, "k", "0.0.1", nil, nil, nil, rec)
	require.NoError(t, err)

	require.Len(t, rec.intervals, failCount,
		"expected one backoff interval per failed attempt")
	for i := 1; i < len(rec.intervals); i++ {
		assert.GreaterOrEqual(t, rec.intervals[i], rec.intervals[i-1])
	}
}
