package hub

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

// mockConnectorClient implements WorkerConnectorServiceClient for
// testing the Register flow.
type mockConnectorClient struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler

	registerFn   func(ctx context.Context, req *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error)
	challengeFn  func(ctx context.Context, req *leapmuxv1.GetRegistrationChallengeRequest) (*leapmuxv1.GetRegistrationChallengeResponse, error)
	recoverFn    func(ctx context.Context, req *leapmuxv1.RecoverRegistrationRequest) (*leapmuxv1.RegisterResponse, error)
	deregisterFn func(ctx context.Context, req *leapmuxv1.DeregisterSelfRequest) (*leapmuxv1.DeregisterSelfResponse, error)
}

func (m *mockConnectorClient) Register(ctx context.Context, req *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
	return m.registerFn(ctx, req)
}

func (m *mockConnectorClient) GetRegistrationChallenge(ctx context.Context, req *leapmuxv1.GetRegistrationChallengeRequest) (*leapmuxv1.GetRegistrationChallengeResponse, error) {
	if m.challengeFn == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "no challenge in this test")
	}
	return m.challengeFn(ctx, req)
}

func (m *mockConnectorClient) RecoverRegistration(ctx context.Context, req *leapmuxv1.RecoverRegistrationRequest) (*leapmuxv1.RegisterResponse, error) {
	if m.recoverFn == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "no recovery in this test")
	}
	return m.recoverFn(ctx, req)
}

func (m *mockConnectorClient) DeregisterSelf(ctx context.Context, req *leapmuxv1.DeregisterSelfRequest) (*leapmuxv1.DeregisterSelfResponse, error) {
	if m.deregisterFn == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "no deregistration in this test")
	}
	return m.deregisterFn(ctx, req)
}

// testRegistrationOptions states one valid attempt the tests reuse. The
// signer is optional: only a test that drives AlreadyExists reaches it.
func testRegistrationOptions(key string) RegistrationOptions {
	return RegistrationOptions{
		AttemptID:       "a9a0425886d148f68a8f94716ed47a78",
		RegistrationKey: key,
		Keys:            workerkeystest.Keys(workerkeystest.SeededWorkerFill),
	}
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

func TestRegistrationProtocolSharesOneAttemptDeadline(t *testing.T) {
	opts := testRegistrationOptions("key")
	opts.Signer = func([]byte) ([]byte, error) { return []byte("proof"), nil }
	var deadline time.Time
	checkDeadline := func(ctx context.Context) {
		got, ok := ctx.Deadline()
		assert.True(t, ok, "every recovery request needs the attempt deadline")
		assert.Equal(t, deadline, got, "recovery must not extend the protocol pass")
	}
	client := &mockConnectorClient{
		registerFn: func(ctx context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			var ok bool
			deadline, ok = ctx.Deadline()
			require.True(t, ok)
			return nil, connect.NewError(connect.CodeAlreadyExists, "committed")
		},
		challengeFn: func(ctx context.Context, _ *leapmuxv1.GetRegistrationChallengeRequest) (*leapmuxv1.GetRegistrationChallengeResponse, error) {
			checkDeadline(ctx)
			return &leapmuxv1.GetRegistrationChallengeResponse{ChallengeId: "b9a0425886d148f68a8f94716ed47a78", Nonce: make([]byte, 32)}, nil
		},
		recoverFn: func(ctx context.Context, _ *leapmuxv1.RecoverRegistrationRequest) (*leapmuxv1.RegisterResponse, error) {
			checkDeadline(ctx)
			return &leapmuxv1.RegisterResponse{WorkerId: "worker", AuthToken: "token"}, nil
		},
	}
	_, outcome, err := registerProtocolOnce(context.Background(), client, "key", time.Hour, opts)
	require.NoError(t, err)
	assert.Equal(t, outcomeSuccess, outcome)
}

func TestRegistrationProtocolStopsStalledRecoveryAtAttemptDeadline(t *testing.T) {
	for _, stage := range []string{"challenge", "recovery"} {
		t.Run(stage, func(t *testing.T) {
			opts := testRegistrationOptions("key")
			opts.Signer = func([]byte) ([]byte, error) { return []byte("proof"), nil }
			stall := func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					return errors.New("the recovery request escaped its attempt deadline")
				}
				<-ctx.Done()
				return ctx.Err()
			}
			client := &mockConnectorClient{
				// A completed response can arrive as the budget expires. Recovery
				// must retain that expired budget rather than begin another wait.
				registerFn: func(context.Context, *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
					return nil, connect.NewError(connect.CodeAlreadyExists, "committed")
				},
				challengeFn: func(ctx context.Context, _ *leapmuxv1.GetRegistrationChallengeRequest) (*leapmuxv1.GetRegistrationChallengeResponse, error) {
					if stage == "challenge" {
						return nil, stall(ctx)
					}
					return &leapmuxv1.GetRegistrationChallengeResponse{ChallengeId: "b9a0425886d148f68a8f94716ed47a78", Nonce: make([]byte, 32)}, nil
				},
				recoverFn: func(ctx context.Context, _ *leapmuxv1.RecoverRegistrationRequest) (*leapmuxv1.RegisterResponse, error) {
					return nil, stall(ctx)
				},
			}
			_, outcome, err := registerProtocolOnce(context.Background(), client, "key", time.Nanosecond, opts)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Equal(t, outcomeTransient, outcome, "an attempt deadline must not end the overall retry policy")
		})
	}
}

func TestRegistrationRecoveryRefusalClassification(t *testing.T) {
	for _, test := range []struct {
		name, stage string
		code        connect.Code
		want        registerOutcome
	}{
		{"challenge capacity", "challenge", connect.CodeResourceExhausted, outcomeTransient},
		{"challenge owner refusal", "challenge", connect.CodeFailedPrecondition, outcomePermanentRefusal},
		{"stale challenge", "recovery", connect.CodeAborted, outcomeTransient},
		{"recovery owner refusal", "recovery", connect.CodeFailedPrecondition, outcomePermanentRefusal},
		{"rejected signature", "recovery", connect.CodeUnauthenticated, outcomePermanentRefusal},
		{"worker maximum", "register", connect.CodeResourceExhausted, outcomePermanentRefusal},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := testRegistrationOptions("key")
			opts.Signer = func([]byte) ([]byte, error) { return []byte("proof"), nil }
			refusal := connect.NewError(test.code, "refusal")
			client := &mockConnectorClient{
				registerFn: func(context.Context, *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
					if test.stage == "register" {
						return nil, refusal
					}
					return nil, connect.NewError(connect.CodeAlreadyExists, "committed")
				},
				challengeFn: func(context.Context, *leapmuxv1.GetRegistrationChallengeRequest) (*leapmuxv1.GetRegistrationChallengeResponse, error) {
					if test.stage == "challenge" {
						return nil, refusal
					}
					return &leapmuxv1.GetRegistrationChallengeResponse{ChallengeId: "b9a0425886d148f68a8f94716ed47a78", Nonce: make([]byte, 32)}, nil
				},
				recoverFn: func(context.Context, *leapmuxv1.RecoverRegistrationRequest) (*leapmuxv1.RegisterResponse, error) {
					return nil, refusal
				},
			}
			_, outcome, err := registerProtocolOnce(context.Background(), client, "key", time.Hour, opts)
			require.ErrorIs(t, err, refusal)
			assert.Equal(t, test.want, outcome)
		})
	}
}

type cancelRegistrationBackoff struct {
	cancel context.CancelFunc
	calls  int
}

func (b *cancelRegistrationBackoff) Next() time.Duration {
	b.calls++
	b.cancel()
	return time.Hour
}

func (b *cancelRegistrationBackoff) Reset() {}

func TestRegistrationRecoveryRefusesIncompleteCredentialsWithoutRetry(t *testing.T) {
	for _, response := range []*leapmuxv1.RegisterResponse{
		nil, {}, {WorkerId: "worker"}, {AuthToken: "token"},
	} {
		t.Run(response.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
			defer cancel()
			policy := &cancelRegistrationBackoff{cancel: cancel}
			opts := testRegistrationOptions("key")
			opts.Signer = func([]byte) ([]byte, error) { return []byte("proof"), nil }
			var registrations, challenges, recoveries int
			client := &mockConnectorClient{
				registerFn: func(context.Context, *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
					registrations++
					return nil, connect.NewError(connect.CodeAlreadyExists, "committed")
				},
				challengeFn: func(context.Context, *leapmuxv1.GetRegistrationChallengeRequest) (*leapmuxv1.GetRegistrationChallengeResponse, error) {
					challenges++
					return &leapmuxv1.GetRegistrationChallengeResponse{ChallengeId: "b9a0425886d148f68a8f94716ed47a78", Nonce: make([]byte, 32)}, nil
				},
				recoverFn: func(context.Context, *leapmuxv1.RecoverRegistrationRequest) (*leapmuxv1.RegisterResponse, error) {
					recoveries++
					return response, nil
				},
			}
			result, err := registerWithClient(ctx, client, "key", registerRetry{
				backoff: policy, clock: testutil.NewQuartzMock(t), attemptTimeout: time.Minute,
			}, opts)
			require.ErrorIs(t, err, ErrRegistrationRefused)
			require.Contains(t, err.Error(), "incomplete credentials")
			require.Nil(t, result)
			require.Zero(t, policy.calls, "a malformed success must not enter backoff")
			require.Equal(t, 1, registrations)
			require.Equal(t, 1, challenges)
			require.Equal(t, 1, recoveries)
		})
	}
}

func TestRegistrationRecoveryRetriesWithFreshChallenge(t *testing.T) {
	for _, stage := range []string{"challenge capacity", "stale proof"} {
		t.Run(stage, func(t *testing.T) {
			clock := testutil.NewQuartzMock(t)
			ctx := testutil.DeadlineContext(t)
			newTimer, stopTimer := testutil.NewTimerTraps(t, clock, registerRetryTimerTag)
			opts := testRegistrationOptions("key")
			opts.Signer = func([]byte) ([]byte, error) { return []byte("proof"), nil }
			var registrations, challenges, recoveries atomic.Int32
			client := &mockConnectorClient{
				registerFn: func(context.Context, *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
					registrations.Add(1)
					return nil, connect.NewError(connect.CodeAlreadyExists, "committed")
				},
				challengeFn: func(context.Context, *leapmuxv1.GetRegistrationChallengeRequest) (*leapmuxv1.GetRegistrationChallengeResponse, error) {
					if challenges.Add(1) == 1 && stage == "challenge capacity" {
						return nil, connect.NewError(connect.CodeResourceExhausted, "temporary challenge capacity")
					}
					return &leapmuxv1.GetRegistrationChallengeResponse{ChallengeId: "b9a0425886d148f68a8f94716ed47a78", Nonce: make([]byte, 32)}, nil
				},
				recoverFn: func(context.Context, *leapmuxv1.RecoverRegistrationRequest) (*leapmuxv1.RegisterResponse, error) {
					if recoveries.Add(1) == 1 && stage == "stale proof" {
						return nil, connect.NewError(connect.CodeAborted, "challenge replaced")
					}
					return &leapmuxv1.RegisterResponse{WorkerId: "original worker", AuthToken: "original token"}, nil
				},
			}
			done := make(chan error, 1)
			go func() {
				_, err := registerWithClient(ctx, client, "key", fastRegisterRetry(clock), opts)
				done <- err
			}()
			delay := testutil.WaitForTimer(t, ctx, newTimer)
			testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("registration did not recover after the transient challenge failure")
			}
			assert.EqualValues(t, 2, registrations.Load())
			assert.EqualValues(t, 2, challenges.Load(), "a retry must request a fresh challenge")
			_, running := clock.Peek()
			assert.False(t, running)
		})
	}
}

func TestRegisterWithClient_RetriesUntilHubAvailable(t *testing.T) {
	var attempts atomic.Int32
	failCount := 3

	mock := &mockConnectorClient{
		registerFn: func(ctx context.Context, req *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			// Bearer must be passed through on every retry. The mock
			// stands in for the transport, so it reads the request
			// header off the CallInfo the production call seeded.
			info, _ := connect.CallInfoForClientContext(ctx)
			assert.Equal(t, "Bearer key123", info.RequestHeader().Get("Authorization"))
			n := int(attempts.Add(1))
			if n <= failCount {
				return nil, connect.Errorf(connect.CodeUnavailable, "hub down")
			}
			return &leapmuxv1.RegisterResponse{
				WorkerId:  "worker-123",
				AuthToken: "auth-token-abc",
			}, nil
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
		result, err := registerWithClient(t.Context(), mock, "key123", fastRegisterRetry(clock), testRegistrationOptions("key123"))
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
		registerFn: func(_ context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			t.Fatal("Register must not be called with an empty key")
			return nil, nil
		},
	}
	clock := testutil.NewQuartzMock(t)
	_, err := registerWithClient(t.Context(), mock, "", fastRegisterRetry(clock), testRegistrationOptions(""))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistrationRefused, "a missing key is a permanent refusal")
	_, running := clock.Peek()
	assert.False(t, running, "a missing key is permanent, so it must arm no retry")
}

func TestRegisterWithClient_StopsOnContextCancel(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			attempts.Add(1)
			return nil, connect.Errorf(connect.CodeUnavailable, "hub down")
		},
	}

	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, registerRetryTimerTag)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		_, err := registerWithClient(ctx, mock, "k", fastRegisterRetry(clock), testRegistrationOptions("k"))
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
		registerFn: func(_ context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			attempts.Add(1)
			return nil, connect.Errorf(connect.CodeUnauthenticated, "nope")
		},
	}
	clock := testutil.NewQuartzMock(t)
	_, err := registerWithClient(t.Context(), mock, "k", fastRegisterRetry(clock), testRegistrationOptions("k"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistrationRefused, "an invalid or consumed key is permanent")
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
		registerFn: func(_ context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			n := int(attempts.Add(1))
			if n <= failCount {
				return nil, connect.Errorf(connect.CodeUnavailable, "hub down")
			}
			return &leapmuxv1.RegisterResponse{WorkerId: "w", AuthToken: "t"}, nil
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
		_, err := registerWithClient(t.Context(), mock, "k", retry, testRegistrationOptions("k"))
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
// An attempt that never returns prevents every retry. The attempt deadline
// applies even when the client has a longer timeout of its own.
//
// The attempt limit is a real 20ms here, not a mocked one: it is a context
// deadline, and context.WithTimeout reads the real clock. Only the wait
// BETWEEN attempts is on the test's clock.
func TestRegisterWithClient_LimitsOneAttempt(t *testing.T) {
	var attempts atomic.Int32
	var sawDeadline atomic.Bool

	mock := &mockConnectorClient{
		registerFn: func(ctx context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			if _, ok := ctx.Deadline(); ok {
				sawDeadline.Store(true)
			}
			if attempts.Add(1) == 1 {
				// The hub that accepted the connection and then said nothing.
				// Without a per-attempt limit this blocks for ever.
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &leapmuxv1.RegisterResponse{
				WorkerId: "w-1", AuthToken: "tok",
			}, nil
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
		result, err := registerWithClient(t.Context(), mock, "key123", retry, testRegistrationOptions("key123"))
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
		registerFn: func(ctx context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
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
	_, err := registerWithClient(ctx, mock, "k", retry, testRegistrationOptions("k"))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	_, running := clock.Peek()
	assert.False(t, running, "a cancelled caller must not leave a retry armed")
}

// The registration request carries the public keys the Hub pins, because a
// hub that pins keys at registration is what makes a stolen auth token useless
// on its own.
func TestRegisterWithClient_SendsTheKeys(t *testing.T) {
	keys := workerkeystest.Keys(workerkeystest.SeededWorkerFill)
	var seen *leapmuxv1.WorkerPublicKeys
	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, req *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			seen = req.GetKeys()
			return &leapmuxv1.RegisterResponse{WorkerId: "worker-1", AuthToken: "token-1"}, nil
		},
	}

	opts := testRegistrationOptions("k")
	opts.Keys = keys
	result, err := registerWithClient(t.Context(), mock, "k", fastRegisterRetry(testutil.NewQuartzMock(t)), opts)
	require.NoError(t, err)
	require.NotNil(t, seen, "the request must carry a key set")
	assert.True(t, workerkeys.Equal(keys, seen))
	assert.Equal(t, "worker-1", result.WorkerID)
}

// The worker maximum is a permanent refusal: every retry burns the same
// key against the same answer and keeps the machine from telling the
// user their plan is full. The base treated CodeResourceExhausted as
// transient and armed another attempt, which this regression records.
func TestRegisterWithClient_RejectsWorkerLimit(t *testing.T) {
	var attempts atomic.Int32
	mock := &mockConnectorClient{
		registerFn: func(_ context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
			attempts.Add(1)
			return nil, connect.Errorf(connect.CodeResourceExhausted, "worker limit reached for this account")
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	clock := testutil.NewQuartzMock(t)
	testCtx := testutil.DeadlineContext(t)
	newTimer, _ := testutil.NewTimerTraps(t, clock, registerRetryTimerTag)

	// The watcher catches an armed retry timer without parking the test on
	// it: on the corrected behavior no timer is ever armed, and a plain
	// WaitForTimer would sit out the whole deadline before failing. The
	// call stays unreleased until the cleanup frees it, so the loop the
	// base wrongly entered parks inside its backoff rather than firing a
	// second attempt.
	armed := make(chan struct{}, 1)
	freeArmed := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		call, err := newTimer.Wait(testCtx)
		if err != nil {
			// The trap closed or the deadline expired: nothing was armed.
			return
		}
		armed <- struct{}{}
		<-freeArmed
		call.MustRelease(testCtx)
	}()
	t.Cleanup(func() {
		close(freeArmed)
		watcher.Wait()
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := registerWithClient(ctx, mock, "k", fastRegisterRetry(clock), testRegistrationOptions("k"))
		errCh <- err
	}()

	select {
	case err := <-errCh:
		require.Error(t, err, "the worker limit must surface as an error, not a retry")
		assert.NotErrorIs(t, err, context.Canceled)
	case <-armed:
		t.Fatal("the worker-limit refusal must not schedule another registration attempt")
	}

	assert.Equal(t, int32(1), attempts.Load(), "a permanent refusal must see exactly one attempt")
	_, running := clock.Peek()
	assert.False(t, running, "a permanent refusal must arm no retry timer")
}

// A successful response with no worker ID or no token is not a
// registration: the worker would save credentials it can never present.
// The base accepted all three shapes, which this regression records.
func TestRegisterWithClient_RejectsIncompleteCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp *leapmuxv1.RegisterResponse
	}{
		{"no worker id", &leapmuxv1.RegisterResponse{WorkerId: "", AuthToken: "tok"}},
		{"no auth token", &leapmuxv1.RegisterResponse{WorkerId: "w-1", AuthToken: ""}},
		{"neither", &leapmuxv1.RegisterResponse{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockConnectorClient{
				registerFn: func(_ context.Context, _ *leapmuxv1.RegisterRequest) (*leapmuxv1.RegisterResponse, error) {
					return tc.resp, nil
				},
			}
			clock := testutil.NewQuartzMock(t)
			result, err := registerWithClient(t.Context(), mock, "k", fastRegisterRetry(clock), testRegistrationOptions("k"))
			require.Error(t, err, "an incomplete success response must not report a registration")
			assert.Nil(t, result)
			_, running := clock.Peek()
			assert.False(t, running, "an incomplete response must arm no retry timer")
		})
	}
}
