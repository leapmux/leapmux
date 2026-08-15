package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// fakeNow backs m.now so window-expiry tests can advance the clock without
// sleeping. Tests in this package run sequentially (no t.Parallel).
var fakeNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newTestManager(t *testing.T, solo bool) *Manager {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	m := NewManager(st, solo)
	m.now = func() time.Time { return fakeNow }
	return m
}

func upsertLimit(t *testing.T, st store.Store, op string, enabled bool, maxAttempts, windowSeconds int64) {
	t.Helper()
	require.NoError(t, st.RateLimitConfig().Upsert(context.Background(), store.UpsertRateLimitConfigParams{
		Operation:     op,
		Enabled:       enabled,
		MaxAttempts:   maxAttempts,
		WindowSeconds: windowSeconds,
	}))
}

func wrongPasswordError() error {
	return connect.NewError(connect.CodeUnauthenticated, auth.ErrInvalidCurrentPassword)
}

// changePasswordClient wires a one-procedure ChangePassword handler behind
// the rate-limit interceptor, with a fixed authenticated user injected by
// the surrounding HTTP middleware (the auth interceptor's job in the real
// chain). handlerCalled reports whether the protected handler ran.
func changePasswordClient(t *testing.T, m *Manager, handlerErr error, authenticated bool) (leapmuxv1connect.UserServiceClient, *int) {
	t.Helper()
	handlerCalls := 0
	handler := connect.NewUnaryHandler(
		leapmuxv1connect.UserServiceChangePasswordProcedure,
		func(ctx context.Context, req *connect.Request[leapmuxv1.ChangePasswordRequest]) (*connect.Response[leapmuxv1.ChangePasswordResponse], error) {
			handlerCalls++
			if handlerErr != nil {
				return nil, handlerErr
			}
			return connect.NewResponse(&leapmuxv1.ChangePasswordResponse{}), nil
		},
		connect.WithInterceptors(NewInterceptor(m)),
	)
	mux := http.NewServeMux()
	mux.Handle(leapmuxv1connect.UserServiceChangePasswordProcedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticated {
			r = r.WithContext(auth.WithUser(r.Context(), &auth.UserInfo{ID: userid.MustNew("usr_test123")}))
		}
		handler.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL), &handlerCalls
}

func attempt(t *testing.T, client leapmuxv1connect.UserServiceClient) error {
	t.Helper()
	_, err := client.ChangePassword(context.Background(), connect.NewRequest(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "wrong",
		NewPassword:     "whatever1!",
	}))
	return err
}

func TestLimiterAllowsBudgetThenDeniesWithoutCallingHandler(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m.st, "change-password", true, 3, 900)
	m.cached = nil // bust the config cache

	client, calls := changePasswordClient(t, m, wrongPasswordError(), true)
	for i := 1; i <= 3; i++ {
		err := attempt(t, client)
		require.Error(t, err, "attempt %d within budget", i)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "attempt %d ran the handler", i)
	}
	assert.Equal(t, 3, *calls)
	// 4th attempt: denied before the handler — no more Argon2 spend. The
	// retry window is measured on the manager's (here frozen) clock, so it
	// reports the full 900s window, not the real wall clock's past-tense 1s.
	err := attempt(t, client)
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "try again")
	assert.Contains(t, err.Error(), "900 seconds")
	assert.Equal(t, 3, *calls, "denied attempt must not reach the handler")
}

func TestLimiterCountsOnlyCurrentPasswordFailures(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m.st, "change-password", true, 2, 900)
	m.cached = nil

	// Weak-new-password validation errors must not count.
	weakClient, _ := changePasswordClient(t, m, connect.NewError(connect.CodeInvalidArgument, errors.New("password too weak")), true)
	for i := 0; i < 5; i++ {
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(attempt(t, weakClient)))
	}
	// Internal errors must not count.
	internalClient, _ := changePasswordClient(t, m, connect.NewError(connect.CodeInternal, errors.New("db down")), true)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(attempt(t, internalClient)))

	// Only genuine credential failures consume the budget.
	wrongClient, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	for i := 0; i < 2; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(attempt(t, wrongClient)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(attempt(t, wrongClient)))
}

func TestLimiterSuccessResetsWindow(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m.st, "change-password", true, 2, 900)
	m.cached = nil

	wrongClient, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(attempt(t, wrongClient)))

	// A success wipes the slate while budget remains; the full budget is
	// available again afterwards.
	okClient, _ := changePasswordClient(t, m, nil, true)
	require.NoError(t, attempt(t, okClient))
	for i := 0; i < 2; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(attempt(t, wrongClient)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(attempt(t, wrongClient)))
}

func TestLimiterWindowExpires(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m.st, "change-password", true, 1, 900)
	m.cached = nil

	client, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(attempt(t, client)))
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(attempt(t, client)))

	// Advance past the window; the counter self-expires.
	fakeNow = fakeNow.Add(901 * time.Second)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(attempt(t, client)))
}

// TestExpiredWindowEntryIsReclaimed pins the memory bound: an allow() that
// observes an expired window deletes the entry instead of leaving it inert
// in the map for the life of the process.
func TestExpiredWindowEntryIsReclaimed(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m.st, "change-password", true, 1, 900)
	m.cached = nil

	m.recordFailure(OpChangePassword, "usr_test123", 900)
	m.windowMu.Lock()
	assert.Len(t, m.windows, 1)
	m.windowMu.Unlock()

	fakeNow = fakeNow.Add(901 * time.Second)
	_, _, _, err := m.allow(context.Background(), OpChangePassword, "usr_test123")
	require.NoError(t, err)
	m.windowMu.Lock()
	assert.Empty(t, m.windows, "expired window entries must be deleted on observation")
	m.windowMu.Unlock()
}

// TestConfigUnreachableFailsClosedWithUnavailableCode pins the honest
// failure code: a config-store outage must surface as Unavailable, not as
// the same ResourceExhausted code a genuine lockout uses.
func TestConfigUnreachableFailsClosedWithUnavailableCode(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m.st, "change-password", true, 1, 900)
	m.cached = nil
	require.NoError(t, m.st.Close())

	client, calls := changePasswordClient(t, m, nil, true)
	err := attempt(t, client)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "rate limit unavailable")
	assert.Equal(t, 0, *calls, "fail-closed denial must not reach the handler")
}

func TestKnownOperationsSortedAndEffectiveLimitsOverlay(t *testing.T) {
	assert.Equal(t, []Operation{OpChangePassword}, KnownOperations())

	// No row: defaults, enabled.
	enabled, limits := EffectiveLimits(OpChangePassword, nil)
	assert.True(t, enabled)
	assert.EqualValues(t, 5, limits.MaxAttempts)
	assert.EqualValues(t, 900, limits.WindowSeconds)

	// Stored row: values overlay, zero keeps the default (disabled row
	// with partial values still reports its numbers).
	enabled, limits = EffectiveLimits(OpChangePassword, &store.RateLimitConfig{
		Operation:     "change-password",
		Enabled:       false,
		MaxAttempts:   0,
		WindowSeconds: 120,
	})
	assert.False(t, enabled)
	assert.EqualValues(t, 5, limits.MaxAttempts, "zero attempts keeps the default")
	assert.EqualValues(t, 120, limits.WindowSeconds)

	// Unknown operation: disabled, zero limits.
	enabled, limits = EffectiveLimits(Operation("nope"), nil)
	assert.False(t, enabled)
	assert.Equal(t, Limits{}, limits)
}

func TestLimiterDisabledAndSoloBypass(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m.st, "change-password", false, 1, 900)
	m.cached = nil
	client, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	for i := 0; i < 10; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(attempt(t, client)))
	}

	solo := newTestManager(t, true)
	soloClient, _ := changePasswordClient(t, solo, wrongPasswordError(), true)
	for i := 0; i < 10; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(attempt(t, soloClient)))
	}
}

func TestLimiterDefaultsApplyWithoutRow(t *testing.T) {
	m := newTestManager(t, false)
	limits, ok := DefaultLimits(OpChangePassword)
	require.True(t, ok)
	assert.EqualValues(t, 5, limits.MaxAttempts)
	assert.EqualValues(t, 900, limits.WindowSeconds)

	client, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	for i := 0; i < 5; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(attempt(t, client)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(attempt(t, client)))
}

func TestLimiterUnauthenticatedCallsPassThrough(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m.st, "change-password", true, 1, 900)
	m.cached = nil

	// No user in context: the limiter stands down (in the real chain the
	// auth interceptor has already rejected the call).
	client, _ := changePasswordClient(t, m, nil, false)
	for i := 0; i < 3; i++ {
		require.NoError(t, attempt(t, client))
	}
}

func TestValidateLimits(t *testing.T) {
	assert.NoError(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 0, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5000, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 30}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 100000}))
}
