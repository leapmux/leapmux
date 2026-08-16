package ratelimit

import (
	"context"
	"encoding/json"
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
	"github.com/leapmux/leapmux/internal/hub/settings"
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
	set := settings.NewManager(st, nil, SettingsDescriptors())
	require.NoError(t, set.Load(context.Background()))
	m := NewManager(set, solo)
	m.now = func() time.Time { return fakeNow }
	return m
}

func upsertLimit(t *testing.T, m *Manager, op Operation, enabled bool, maxAttempts, windowSeconds int64) {
	t.Helper()
	key, ok := LimitKey(op)
	require.True(t, ok)
	require.NoError(t, key.Set(context.Background(), m.set, LimitValue{
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

func tryChangePassword(t *testing.T, client leapmuxv1connect.UserServiceClient) error {
	t.Helper()
	_, err := client.ChangePassword(context.Background(), connect.NewRequest(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "wrong",
		NewPassword:     "whatever1!",
	}))
	return err
}

func TestLimiterAllowsBudgetThenDeniesWithoutCallingHandler(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 3, 900)

	client, calls := changePasswordClient(t, m, wrongPasswordError(), true)
	for i := 1; i <= 3; i++ {
		err := tryChangePassword(t, client)
		require.Error(t, err, "attempt %d within budget", i)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "attempt %d ran the handler", i)
	}
	assert.Equal(t, 3, *calls)
	// 4th attempt: denied before the handler — no more Argon2 spend. The
	// retry window is measured on the manager's (here frozen) clock, so it
	// reports the full 900s window, not the real wall clock's past-tense 1s.
	err := tryChangePassword(t, client)
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "try again")
	assert.Contains(t, err.Error(), "900 seconds")
	assert.Equal(t, 3, *calls, "denied attempt must not reach the handler")
}

func TestLimiterCountsOnlyCurrentPasswordFailures(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 2, 900)

	// Weak-new-password validation errors must not count.
	weakClient, _ := changePasswordClient(t, m, connect.NewError(connect.CodeInvalidArgument, errors.New("password too weak")), true)
	for i := 0; i < 5; i++ {
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(tryChangePassword(t, weakClient)))
	}
	// Internal errors must not count.
	internalClient, _ := changePasswordClient(t, m, connect.NewError(connect.CodeInternal, errors.New("db down")), true)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(tryChangePassword(t, internalClient)))

	// Only genuine credential failures consume the budget.
	wrongClient, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	for i := 0; i < 2; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, wrongClient)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryChangePassword(t, wrongClient)))
}

func TestLimiterSuccessResetsWindow(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 2, 900)

	wrongClient, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, wrongClient)))

	// A success wipes the slate while budget remains; the full budget is
	// available again afterwards.
	okClient, _ := changePasswordClient(t, m, nil, true)
	require.NoError(t, tryChangePassword(t, okClient))
	for i := 0; i < 2; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, wrongClient)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryChangePassword(t, wrongClient)))
}

func TestLimiterWindowExpires(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 1, 900)

	client, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, client)))
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryChangePassword(t, client)))

	// Advance past the window; the counter self-expires.
	fakeNow = fakeNow.Add(901 * time.Second)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, client)))
}

// recordCredentialFailure runs one admitted try through complete with a
// countable failure, the equivalent of one rejected ChangePassword.
func recordCredentialFailure(t *testing.T, m *Manager, userID string) {
	t.Helper()
	a, allowed, _, err := m.allow(context.Background(), OpChangePassword, userID)
	require.NoError(t, err)
	require.True(t, allowed)
	m.complete(a, wrongPasswordError())
}

// TestExpiredWindowEntryIsReclaimed pins the memory bound: an allow() that
// observes an expired window deletes the entry instead of leaving it inert
// in the map for the life of the process.
func TestExpiredWindowEntryIsReclaimed(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 1, 900)

	recordCredentialFailure(t, m, "usr_test123")
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

// TestExpiredWindowsAreSweptForAbsentUsers pins the other memory bound:
// a user who fails once and never returns still leaves the map, because
// the expiry-gated sweep in allow() drops expired entries no same-key
// lazy delete would ever reach.
func TestExpiredWindowsAreSweptForAbsentUsers(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 1, 900)

	recordCredentialFailure(t, m, "usr_gone")
	recordCredentialFailure(t, m, "usr_back")
	m.windowMu.Lock()
	assert.Len(t, m.windows, 2)
	m.windowMu.Unlock()

	// Past the earliest window reset: any allow() now sweeps every
	// expired entry, including usr_gone's, which never returns.
	fakeNow = fakeNow.Add(901 * time.Second)
	_, _, _, err := m.allow(context.Background(), OpChangePassword, "usr_back")
	require.NoError(t, err)
	m.windowMu.Lock()
	assert.Empty(t, m.windows, "the sweep must drop expired entries of users who never retry")
	m.windowMu.Unlock()
}

// TestConcurrentBurstCannotExceedBudget pins the reservation: allow()
// counts in-flight attempts against the budget, so a burst of parallel
// calls cannot all pass a failures-only check before any of them lands.
func TestConcurrentBurstCannotExceedBudget(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 3, 900)

	// Three concurrent in-flight attempts (allowed, not yet completed).
	inFlight := make([]*attempt, 0, 3)
	for i := 0; i < 3; i++ {
		a, allowed, _, err := m.allow(context.Background(), OpChangePassword, "usr_test123")
		require.NoError(t, err)
		require.True(t, allowed, "attempt %d within budget", i+1)
		inFlight = append(inFlight, a)
	}
	// The fourth concurrent attempt is denied even though zero failures
	// have completed yet; the zero retry duration is the in-flight bound,
	// not an open window.
	_, allowed, _, err := m.allow(context.Background(), OpChangePassword, "usr_test123")
	require.NoError(t, err)
	assert.False(t, allowed, "an in-flight burst must not exceed the budget")

	// Completing the burst with non-countable errors frees every
	// reservation without opening a failure window, so the next attempt
	// passes again.
	for _, a := range inFlight {
		m.complete(a, connect.NewError(connect.CodeInternal, errors.New("db down")))
	}
	m.windowMu.Lock()
	assert.Empty(t, m.inFlight, "completed attempts must release their reservations")
	assert.Empty(t, m.windows, "non-countable errors must not open a failure window")
	m.windowMu.Unlock()
	_, allowed, _, err = m.allow(context.Background(), OpChangePassword, "usr_test123")
	require.NoError(t, err)
	assert.True(t, allowed, "released reservations restore the budget")
}

// TestInFlightDrivenDenialReportsZeroRetryAfter pins the denial reason's
// honest retry duration: when recorded failures alone have NOT exhausted
// the budget and in-flight reservations drive the denial, the reservations
// clear within one handler latency, so the caller owes no failure window —
// only a failures-driven denial reports the window remainder.
func TestInFlightDrivenDenialReportsZeroRetryAfter(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 3, 900)

	recordCredentialFailure(t, m, "usr_test123")
	recordCredentialFailure(t, m, "usr_test123")

	// A third attempt is admitted and stays in flight: 2 failures + 1
	// reservation fills the budget of 3.
	a3, allowed, _, err := m.allow(context.Background(), OpChangePassword, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)

	_, allowed, retryAfter, err := m.allow(context.Background(), OpChangePassword, "usr_test123")
	require.NoError(t, err)
	assert.False(t, allowed, "the in-flight reservation must close the budget")
	assert.Zero(t, retryAfter, "an in-flight-driven denial owes no failure window")

	// Once the failure lands, the denial is failures-driven and reports the
	// window remainder measured on the frozen clock.
	m.complete(a3, wrongPasswordError())
	_, allowed, retryAfter, err = m.allow(context.Background(), OpChangePassword, "usr_test123")
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, 900*time.Second, retryAfter, "a failures-driven denial reports the window remainder")
}

// TestUnreservedAttemptCompletionReleasesNothing pins the reservation's
// ownership: an attempt admitted under a disabled policy never reserved a
// slot, so completing it must not release a slot a concurrent reserved
// attempt still holds.
func TestUnreservedAttemptCompletionReleasesNothing(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 2, 900)

	reserved, allowed, _, err := m.allow(context.Background(), OpChangePassword, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)

	// The policy flips to disabled mid-flight; the next attempt is admitted
	// without a reservation.
	upsertLimit(t, m, OpChangePassword, false, 2, 900)
	unreserved, allowed, _, err := m.allow(context.Background(), OpChangePassword, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)

	m.complete(unreserved, wrongPasswordError())
	m.windowMu.Lock()
	assert.EqualValues(t, 1, m.inFlight[windowKey{OpChangePassword, "usr_test123"}],
		"an unreserved attempt's completion must not release another attempt's reservation")
	m.windowMu.Unlock()

	m.complete(reserved, connect.NewError(connect.CodeInternal, errors.New("db down")))
	m.windowMu.Lock()
	assert.Empty(t, m.inFlight, "the reserved attempt's completion releases its own reservation")
	m.windowMu.Unlock()
}

// TestPanickingHandlerReleasesReservation pins the panic path: net/http
// recovers a handler panic per connection, so the interceptor must close
// the reservation on the unwind — a leaked slot would deny the user's
// every later attempt until hub restart, because nothing sweeps inFlight.
func TestPanickingHandlerReleasesReservation(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 1, 900)

	handler := connect.NewUnaryHandler(
		leapmuxv1connect.UserServiceChangePasswordProcedure,
		func(ctx context.Context, req *connect.Request[leapmuxv1.ChangePasswordRequest]) (*connect.Response[leapmuxv1.ChangePasswordResponse], error) {
			panic("boom")
		},
		connect.WithInterceptors(NewInterceptor(m)),
	)
	mux := http.NewServeMux()
	mux.Handle(leapmuxv1connect.UserServiceChangePasswordProcedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithUser(r.Context(), &auth.UserInfo{ID: userid.MustNew("usr_test123")}))
		handler.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)

	_, err := client.ChangePassword(context.Background(), connect.NewRequest(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "wrong",
		NewPassword:     "whatever1!",
	}))
	require.Error(t, err, "the aborted connection surfaces as an error to the client")

	m.windowMu.Lock()
	assert.Empty(t, m.inFlight, "a panicking handler must not leak its reservation")
	assert.Empty(t, m.windows, "a panic is not a credential failure and must not open a window")
	m.windowMu.Unlock()

	// The budget is whole again: the next attempt reaches the handler
	// instead of failing with ResourceExhausted.
	wrongClient, calls := changePasswordClient(t, m, wrongPasswordError(), true)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, wrongClient)),
		"the released reservation must re-open the budget")
	assert.Equal(t, 1, *calls)
}

func TestKnownOperationsSortedAndEffectiveLimitsOverlay(t *testing.T) {
	assert.Equal(t, []Operation{OpChangePassword}, KnownOperations())

	// No row: defaults, enabled.
	m := newTestManager(t, false)
	key, ok := LimitKey(OpChangePassword)
	require.True(t, ok)
	v := key.Of(m.set.Snapshot(context.Background()))
	assert.True(t, v.Enabled)
	assert.EqualValues(t, 5, v.MaxAttempts)
	assert.EqualValues(t, 900, v.WindowSeconds)

	// Stored document: fields it omits keep the defaults (a disabled row
	// with a partial budget still reports its numbers).
	require.NoError(t, m.set.Update(context.Background(), key, json.RawMessage(
		`{"enabled":false,"window_seconds":120}`)))
	v = key.Of(m.set.Snapshot(context.Background()))
	assert.False(t, v.Enabled)
	assert.EqualValues(t, 5, v.MaxAttempts, "an omitted attempts count keeps the default")
	assert.EqualValues(t, 120, v.WindowSeconds)

	// Unknown operation: no key, no defaults.
	_, ok = LimitKey(Operation("nope"))
	assert.False(t, ok)
	limits, ok := DefaultLimits(Operation("nope"))
	assert.False(t, ok)
	assert.Equal(t, Limits{}, limits)
}

func TestLimiterDisabledAndSoloBypass(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, false, 1, 900)
	client, _ := changePasswordClient(t, m, wrongPasswordError(), true)
	for i := 0; i < 10; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, client)))
	}

	solo := newTestManager(t, true)
	soloClient, _ := changePasswordClient(t, solo, wrongPasswordError(), true)
	for i := 0; i < 10; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, soloClient)))
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
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryChangePassword(t, client)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryChangePassword(t, client)))
}

func TestLimiterUnauthenticatedCallsPassThrough(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpChangePassword, true, 1, 900)

	// No user in context: the limiter stands down (in the real chain the
	// auth interceptor has already rejected the call).
	client, _ := changePasswordClient(t, m, nil, false)
	for i := 0; i < 3; i++ {
		require.NoError(t, tryChangePassword(t, client))
	}
}

// TestProcedureRoutingAndCatalogueAgree pins the two hand-maintained maps
// that must change together: every routed operation must be catalogued
// (an uncatalogued op fails closed with CodeUnavailable on every call),
// and every catalogued operation must be routed (KnownOperations advertises
// it to the admin CLI, which would configure a budget nothing enforces).
func TestProcedureRoutingAndCatalogueAgree(t *testing.T) {
	assert.NotEmpty(t, procedureOperations, "no procedures are routed; the tripwire is vacuous")
	for proc, op := range procedureOperations {
		assert.Containsf(t, defaults, op,
			"procedure %q routes to operation %q with no defaults entry; every call would fail closed with CodeUnavailable", proc, op)
	}
	for op := range defaults {
		routed := false
		for _, rop := range procedureOperations {
			if rop == op {
				routed = true
				break
			}
		}
		assert.Truef(t, routed,
			"operation %q is catalogued but no procedureOperations entry routes to it; the admin CLI configures it but nothing enforces it", op)
	}
}

func TestValidateLimits(t *testing.T) {
	assert.NoError(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 0, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5000, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 30}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 100000}))
}
