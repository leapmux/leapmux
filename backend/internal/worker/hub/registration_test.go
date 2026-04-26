package hub

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// mockConnectorClient implements WorkerConnectorServiceClient for testing.
type mockConnectorClient struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler

	requestRegistrationFn func(ctx context.Context, req *connect.Request[leapmuxv1.RequestRegistrationRequest]) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error)
	pollRegistrationFn    func(ctx context.Context, req *connect.Request[leapmuxv1.PollRegistrationRequest]) (*connect.Response[leapmuxv1.PollRegistrationResponse], error)
}

func (m *mockConnectorClient) RequestRegistration(ctx context.Context, req *connect.Request[leapmuxv1.RequestRegistrationRequest]) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error) {
	return m.requestRegistrationFn(ctx, req)
}

func (m *mockConnectorClient) PollRegistration(ctx context.Context, req *connect.Request[leapmuxv1.PollRegistrationRequest]) (*connect.Response[leapmuxv1.PollRegistrationResponse], error) {
	return m.pollRegistrationFn(ctx, req)
}

func (m *mockConnectorClient) Connect(_ context.Context) *connect.BidiStreamForClient[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse] {
	return nil
}

func TestRegisterWithClient_RetriesUntilHubAvailable(t *testing.T) {
	var attempts atomic.Int32
	failCount := 3

	mock := &mockConnectorClient{
		requestRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RequestRegistrationRequest]) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error) {
			n := int(attempts.Add(1))
			if n <= failCount {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub down"))
			}
			return connect.NewResponse(&leapmuxv1.RequestRegistrationResponse{
				RegistrationToken: "test-token",
				RegistrationUrl:   "/register/test-token",
			}), nil
		},
		pollRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.PollRegistrationRequest]) (*connect.Response[leapmuxv1.PollRegistrationResponse], error) {
			return connect.NewResponse(&leapmuxv1.PollRegistrationResponse{
				Status:    leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED,
				WorkerId:  "worker-123",
				AuthToken: "auth-token-abc",
			}), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := registerWithClient(ctx, mock, "http://localhost:0", "0.0.1", nil, nil, nil, newFastBackoff())
	require.NoError(t, err, "registerWithClient failed")

	assert.Equal(t, int32(failCount+1), attempts.Load(), "RequestRegistration call count")
	assert.Equal(t, "worker-123", result.WorkerID)
	assert.Equal(t, "auth-token-abc", result.AuthToken)
}

func TestRegisterWithClient_StopsOnContextCancel(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockConnectorClient{
		requestRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RequestRegistrationRequest]) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error) {
			attempts.Add(1)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub down"))
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := registerWithClient(ctx, mock, "http://localhost:0", "0.0.1", nil, nil, nil, newFastBackoff())
	assert.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, attempts.Load(), int32(1), "expected at least 1 attempt")
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
		requestRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RequestRegistrationRequest]) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error) {
			n := int(attempts.Add(1))
			if n <= failCount {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub down"))
			}
			return connect.NewResponse(&leapmuxv1.RequestRegistrationResponse{
				RegistrationToken: "t",
				RegistrationUrl:   "/register/t",
			}), nil
		},
		pollRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.PollRegistrationRequest]) (*connect.Response[leapmuxv1.PollRegistrationResponse], error) {
			return connect.NewResponse(&leapmuxv1.PollRegistrationResponse{
				Status: leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED,
			}), nil
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

	_, err := registerWithClient(ctx, mock, "http://localhost:0", "0.0.1", nil, nil, nil, rec)
	require.NoError(t, err, "registerWithClient failed")

	// One NextBackOff per failed attempt — the success doesn't sleep.
	require.Len(t, rec.intervals, failCount,
		"expected one backoff interval per failed attempt")

	// Verify the intervals returned by the BackOff are non-decreasing.
	// Asserting on the values the function actually requests is exact, vs.
	// measuring elapsed wall-clock time which is noisy on Windows where
	// the default scheduler tick (~15.6ms) dwarfs 10ms intervals.
	for i := 1; i < len(rec.intervals); i++ {
		assert.GreaterOrEqual(t, rec.intervals[i], rec.intervals[i-1],
			"interval[%d]=%v < interval[%d]=%v, expected non-decreasing",
			i, rec.intervals[i], i-1, rec.intervals[i-1])
	}
}

func TestRegisterWithClient_LongPollPendingThenApproved(t *testing.T) {
	var pollCount atomic.Int32

	mock := &mockConnectorClient{
		requestRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RequestRegistrationRequest]) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error) {
			return connect.NewResponse(&leapmuxv1.RequestRegistrationResponse{
				RegistrationToken: "lp-token",
				RegistrationUrl:   "/register/lp-token",
			}), nil
		},
		pollRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.PollRegistrationRequest]) (*connect.Response[leapmuxv1.PollRegistrationResponse], error) {
			n := int(pollCount.Add(1))
			if n <= 2 {
				// Simulate Hub long-poll timeout returning pending.
				return connect.NewResponse(&leapmuxv1.PollRegistrationResponse{
					Status: leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING,
				}), nil
			}
			return connect.NewResponse(&leapmuxv1.PollRegistrationResponse{
				Status:    leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED,
				WorkerId:  "worker-lp",
				AuthToken: "auth-lp",
			}), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := registerWithClient(ctx, mock, "http://localhost:0", "0.0.1", nil, nil, nil, newFastBackoff())
	require.NoError(t, err, "registerWithClient failed")

	assert.Equal(t, int32(3), pollCount.Load(), "PollRegistration call count")
	assert.Equal(t, "worker-lp", result.WorkerID)
	assert.Equal(t, "auth-lp", result.AuthToken)
}

func TestRegisterWithClient_PollErrorRetries(t *testing.T) {
	var pollCount atomic.Int32

	mock := &mockConnectorClient{
		requestRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.RequestRegistrationRequest]) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error) {
			return connect.NewResponse(&leapmuxv1.RequestRegistrationResponse{
				RegistrationToken: "err-token",
				RegistrationUrl:   "/register/err-token",
			}), nil
		},
		pollRegistrationFn: func(_ context.Context, _ *connect.Request[leapmuxv1.PollRegistrationRequest]) (*connect.Response[leapmuxv1.PollRegistrationResponse], error) {
			n := int(pollCount.Add(1))
			if n <= 1 {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub down"))
			}
			return connect.NewResponse(&leapmuxv1.PollRegistrationResponse{
				Status:    leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED,
				WorkerId:  "worker-err",
				AuthToken: "auth-err",
			}), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := registerWithClient(ctx, mock, "http://localhost:0", "0.0.1", nil, nil, nil, newFastBackoff())
	require.NoError(t, err, "registerWithClient failed")

	assert.Equal(t, int32(2), pollCount.Load(), "PollRegistration call count")
	assert.Equal(t, "worker-err", result.WorkerID)
}
