package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoregistry"

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

// elevateSessionClient wires a one-procedure ElevateSession handler behind
// the rate-limit interceptor, with a fixed authenticated user injected by
// the surrounding HTTP middleware (the auth interceptor's job in the real
// chain). handlerCalled reports whether the protected handler ran.
func elevateSessionClient(t *testing.T, m *Manager, handlerErr error, authenticated bool) (leapmuxv1connect.UserServiceClient, *int) {
	t.Helper()
	handlerCalls := 0
	handler := connect.NewUnaryHandler(
		leapmuxv1connect.UserServiceElevateSessionProcedure,
		func(ctx context.Context, req *connect.Request[leapmuxv1.ElevateSessionRequest]) (*connect.Response[leapmuxv1.ElevateSessionResponse], error) {
			handlerCalls++
			if handlerErr != nil {
				return nil, handlerErr
			}
			return connect.NewResponse(&leapmuxv1.ElevateSessionResponse{}), nil
		},
		connect.WithInterceptors(NewInterceptor(m)),
	)
	mux := http.NewServeMux()
	mux.Handle(leapmuxv1connect.UserServiceElevateSessionProcedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticated {
			r = r.WithContext(auth.WithUser(r.Context(), &auth.UserInfo{ID: userid.MustNew("usr_test123")}))
		}
		handler.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL), &handlerCalls
}

func rejectedAssertionError() error {
	return connect.NewError(connect.CodeUnauthenticated, auth.ErrInvalidElevationAssertion)
}

// finishPasskeyElevationClient is the passkey path's twin of
// elevateSessionClient. The two paths share ONE budget, which is what stops
// an attacker doubling their attempts by alternating between them.
func finishPasskeyElevationClient(t *testing.T, m *Manager, handlerErr error, authenticated bool) (leapmuxv1connect.UserServiceClient, *int) {
	t.Helper()
	handlerCalls := 0
	handler := connect.NewUnaryHandler(
		leapmuxv1connect.UserServiceFinishPasskeyElevationProcedure,
		func(ctx context.Context, req *connect.Request[leapmuxv1.FinishPasskeyElevationRequest]) (*connect.Response[leapmuxv1.ElevateSessionResponse], error) {
			handlerCalls++
			if handlerErr != nil {
				return nil, handlerErr
			}
			return connect.NewResponse(&leapmuxv1.ElevateSessionResponse{}), nil
		},
		connect.WithInterceptors(NewInterceptor(m)),
	)
	mux := http.NewServeMux()
	mux.Handle(leapmuxv1connect.UserServiceFinishPasskeyElevationProcedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticated {
			r = r.WithContext(auth.WithUser(r.Context(), &auth.UserInfo{ID: userid.MustNew("usr_test123")}))
		}
		handler.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL), &handlerCalls
}

func tryFinishPasskeyElevation(t *testing.T, client leapmuxv1connect.UserServiceClient) error {
	t.Helper()
	_, err := client.FinishPasskeyElevation(context.Background(), connect.NewRequest(&leapmuxv1.FinishPasskeyElevationRequest{
		SessionId:      "wa-1",
		CredentialJson: "{}",
	}))
	return err
}

func tryElevateSession(t *testing.T, client leapmuxv1connect.UserServiceClient) error {
	t.Helper()
	_, err := client.ElevateSession(context.Background(), connect.NewRequest(&leapmuxv1.ElevateSessionRequest{
		CurrentPassword: "wrong",
	}))
	return err
}

func TestLimiterAllowsBudgetThenDeniesWithoutCallingHandler(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 3, 900)

	client, calls := elevateSessionClient(t, m, wrongPasswordError(), true)
	for i := 1; i <= 3; i++ {
		err := tryElevateSession(t, client)
		require.Error(t, err, "attempt %d within budget", i)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "attempt %d ran the handler", i)
	}
	assert.Equal(t, 3, *calls)
	// 4th attempt: denied before the handler — no more Argon2 spend. The
	// retry window is measured on the manager's (here frozen) clock, so it
	// reports the full 900s window, not the real wall clock's past-tense 1s.
	err := tryElevateSession(t, client)
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "try again")
	assert.Contains(t, err.Error(), "900 seconds")
	assert.Equal(t, 3, *calls, "denied attempt must not reach the handler")
}

func TestLimiterCountsOnlyCredentialFailures(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 2, 900)

	// Weak-new-password validation errors must not count.
	weakClient, _ := elevateSessionClient(t, m, connect.NewError(connect.CodeInvalidArgument, errors.New("password too weak")), true)
	for i := 0; i < 5; i++ {
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(tryElevateSession(t, weakClient)))
	}
	// Internal errors must not count.
	internalClient, _ := elevateSessionClient(t, m, connect.NewError(connect.CodeInternal, errors.New("db down")), true)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(tryElevateSession(t, internalClient)))

	// Only genuine credential failures consume the budget.
	wrongClient, _ := elevateSessionClient(t, m, wrongPasswordError(), true)
	for i := 0; i < 2; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, wrongClient)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryElevateSession(t, wrongClient)))
}

func TestLimiterSuccessResetsWindow(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 2, 900)

	wrongClient, _ := elevateSessionClient(t, m, wrongPasswordError(), true)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, wrongClient)))

	// A success clears the failure count while budget remains; the full
	// budget is available again afterwards.
	okClient, _ := elevateSessionClient(t, m, nil, true)
	require.NoError(t, tryElevateSession(t, okClient))
	for i := 0; i < 2; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, wrongClient)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryElevateSession(t, wrongClient)))
}

func TestLimiterWindowExpires(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 1, 900)

	client, _ := elevateSessionClient(t, m, wrongPasswordError(), true)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, client)))
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryElevateSession(t, client)))

	// Advance past the window; the counter self-expires.
	fakeNow = fakeNow.Add(901 * time.Second)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, client)))
}

// elevateSpec is the routing entry ElevateSession carries: the password path
// PROVES the credential the failure window counts, so a success there
// resets it. Tests that exercise the window itself use this one, because a
// non-proving entry can never reset and would pin nothing.
var elevateSpec = procedureSpec{op: OpElevation, provesCredential: true}

// beginElevationSpec is the routing entry BeginPasskeyElevation carries. It
// mints assertion options and verifies no secret, so it takes an in-flight
// slot and must NEVER reset the failure window.
var beginElevationSpec = procedureSpec{op: OpElevation}

// recordCredentialFailure runs one admitted try through complete with a
// countable failure, the equivalent of one rejected ElevateSession.
func recordCredentialFailure(t *testing.T, m *Manager, userID string) {
	t.Helper()
	a, allowed, _, err := m.allow(context.Background(), elevateSpec, userID)
	require.NoError(t, err)
	require.True(t, allowed)
	m.complete(a, wrongPasswordError())
}

// TestExpiredWindowEntryIsReclaimed pins the memory limit: an allow() that
// observes an expired window deletes the entry instead of leaving it inert
// in the map for the life of the process.
func TestExpiredWindowEntryIsReclaimed(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 1, 900)

	recordCredentialFailure(t, m, "usr_test123")
	m.windowMu.Lock()
	assert.Len(t, m.windows, 1)
	m.windowMu.Unlock()

	fakeNow = fakeNow.Add(901 * time.Second)
	_, _, _, err := m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	m.windowMu.Lock()
	assert.Empty(t, m.windows, "expired window entries must be deleted on observation")
	m.windowMu.Unlock()
}

// TestExpiredWindowsAreSweptForAbsentUsers pins the other memory limit:
// a user who fails once and never returns still leaves the map, because
// the expiry-controlled sweep in allow() drops expired entries no same-key
// lazy delete would ever reach.
func TestExpiredWindowsAreSweptForAbsentUsers(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 1, 900)

	recordCredentialFailure(t, m, "usr_gone")
	recordCredentialFailure(t, m, "usr_back")
	m.windowMu.Lock()
	assert.Len(t, m.windows, 2)
	m.windowMu.Unlock()

	// Past the earliest window reset: any allow() now sweeps every
	// expired entry, including usr_gone's, which never returns.
	fakeNow = fakeNow.Add(901 * time.Second)
	_, _, _, err := m.allow(context.Background(), elevateSpec, "usr_back")
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
	upsertLimit(t, m, OpElevation, true, 3, 900)

	// Three concurrent in-flight attempts (allowed, not yet completed).
	inFlight := make([]*attempt, 0, 3)
	for i := 0; i < 3; i++ {
		a, allowed, _, err := m.allow(context.Background(), elevateSpec, "usr_test123")
		require.NoError(t, err)
		require.True(t, allowed, "attempt %d within budget", i+1)
		inFlight = append(inFlight, a)
	}
	// The fourth concurrent attempt is denied although no failure completed
	// yet; the zero retry duration is the in-flight limit, not an open
	// window.
	_, allowed, _, err := m.allow(context.Background(), elevateSpec, "usr_test123")
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
	_, allowed, _, err = m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	assert.True(t, allowed, "released reservations restore the budget")
}

// TestInFlightDrivenDenialReportsZeroRetryAfter pins the denial reason's
// honest retry duration: when recorded failures alone do NOT exhaust
// the budget and in-flight reservations drive the denial, the reservations
// clear within one handler latency, so the caller owes no failure window —
// only a failures-driven denial reports the window remainder.
func TestInFlightDrivenDenialReportsZeroRetryAfter(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 3, 900)

	recordCredentialFailure(t, m, "usr_test123")
	recordCredentialFailure(t, m, "usr_test123")

	// A third attempt is admitted and stays in flight: 2 failures + 1
	// reservation fills the budget of 3.
	a3, allowed, _, err := m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)

	_, allowed, retryAfter, err := m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	assert.False(t, allowed, "the in-flight reservation must close the budget")
	assert.Zero(t, retryAfter, "an in-flight-driven denial owes no failure window")

	// Once the failure lands, the denial is failures-driven and reports the
	// window remainder measured on the frozen clock.
	m.complete(a3, wrongPasswordError())
	_, allowed, retryAfter, err = m.allow(context.Background(), elevateSpec, "usr_test123")
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
	upsertLimit(t, m, OpElevation, true, 2, 900)

	reserved, allowed, _, err := m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)

	// The policy flips to disabled mid-flight; the next attempt is admitted
	// without a reservation.
	upsertLimit(t, m, OpElevation, false, 2, 900)
	unreserved, allowed, _, err := m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)

	m.complete(unreserved, wrongPasswordError())
	m.windowMu.Lock()
	assert.EqualValues(t, 1, m.inFlight[windowKey{OpElevation, "usr_test123"}],
		"an unreserved attempt's completion must not release another attempt's reservation")
	m.windowMu.Unlock()

	m.complete(reserved, connect.NewError(connect.CodeInternal, errors.New("db down")))
	m.windowMu.Lock()
	assert.Empty(t, m.inFlight, "the reserved attempt's completion releases its own reservation")
	m.windowMu.Unlock()
}

// TestNonProvingSuccessKeepsTheFailureWindow pins the guard that makes the
// guess cap real.
//
// Every procedure the operation routes takes an in-flight slot, and most of
// them verify no secret at all: BeginPasskeyElevation mints assertion
// options, ChangePassword runs on an already elevated session. If a success
// on one of those reset the window, an attacker holding a stolen session
// cookie would clear their own failure count between guesses -- guess,
// call Begin, guess again -- and the 5-per-window cap on the hub's only
// credential-guess surface would be unlimited.
func TestNonProvingSuccessKeepsTheFailureWindow(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 2, 900)

	recordCredentialFailure(t, m, "usr_test123")

	// A success on the Begin leg: admitted, completed with no error.
	begin, allowed, _, err := m.allow(context.Background(), beginElevationSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)
	m.complete(begin, nil)

	m.windowMu.Lock()
	w := m.windows[windowKey{OpElevation, "usr_test123"}]
	require.NotNil(t, w, "a success that proves nothing must not delete the window")
	assert.EqualValues(t, 1, w.failures, "the recorded failure must survive")
	m.windowMu.Unlock()

	// The budget of 2 is therefore spent by one more failure, not two.
	recordCredentialFailure(t, m, "usr_test123")
	_, allowed, _, err = m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	assert.False(t, allowed, "the cap must hold across a non-proving success")
}

// TestProvingSuccessResetsTheFailureWindow is the other half: a caller who
// really presented the secret makes the accumulated failures irrelevant, so
// the manager drops the window. Without this the guard above would be
// indistinguishable from
// "never reset", and a user who mistypes twice and then answers correctly
// would carry the failures for the rest of the window.
func TestProvingSuccessResetsTheFailureWindow(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 2, 900)

	recordCredentialFailure(t, m, "usr_test123")

	proved, allowed, _, err := m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)
	m.complete(proved, nil)

	m.windowMu.Lock()
	assert.Empty(t, m.windows, "a proven credential clears the accumulated failures")
	m.windowMu.Unlock()
}

// TestEveryRoutedProcedureReservesASlot pins the OTHER budget the routing
// carries. The failure window counts only a wrong secret, but every routed
// procedure runs work that is expensive to repeat -- an Argon2 hash, or a
// ceremony write that takes SQLite's single writer lock -- so each one must
// take an in-flight slot. A procedure routed with provesCredential unset
// still reserves; only the RESET is restricted.
func TestEveryRoutedProcedureReservesASlot(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 2, 900)

	first, allowed, _, err := m.allow(context.Background(), beginElevationSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)
	second, allowed, _, err := m.allow(context.Background(), beginElevationSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)

	_, allowed, _, err = m.allow(context.Background(), beginElevationSpec, "usr_test123")
	require.NoError(t, err)
	assert.False(t, allowed, "a non-proving procedure must still reserve against the budget")

	m.complete(first, nil)
	m.complete(second, nil)
	m.windowMu.Lock()
	assert.Empty(t, m.inFlight, "completed attempts release their reservations")
	m.windowMu.Unlock()
}

// TestExpensiveMutationsAreRouted pins the set of procedures that must take
// an in-flight slot, and which of them prove a credential.
//
// The map is the whole mechanism: a sensitive procedure that nobody routes
// runs its Argon2 hash or its ceremony write with no per-user concurrency
// cap at all, and that is invisible until somebody measures the hub under
// load. Listing the set here makes a removal fail the suite.
func TestExpensiveMutationsAreRouted(t *testing.T) {
	proving := map[string]bool{
		leapmuxv1connect.UserServiceElevateSessionProcedure:         true,
		leapmuxv1connect.UserServiceFinishPasskeyElevationProcedure: true,
		// Everything below runs on an already elevated session, or mints
		// options, so none of them verifies a secret.
		leapmuxv1connect.UserServiceBeginPasskeyElevationProcedure:     false,
		leapmuxv1connect.UserServiceChangePasswordProcedure:            false,
		leapmuxv1connect.UserServiceBeginPasskeyRegistrationProcedure:  false,
		leapmuxv1connect.UserServiceFinishPasskeyRegistrationProcedure: false,
		leapmuxv1connect.UserServiceRenamePasskeyProcedure:             false,
		leapmuxv1connect.UserServiceDeletePasskeyProcedure:             false,
		leapmuxv1connect.UserServiceDeactivatePasskeyAuthProcedure:     false,
	}
	assert.Len(t, procedureOperations, len(proving),
		"a procedure added to or removed from the routing map must be reflected here")
	for procedure, provesCredential := range proving {
		spec, ok := procedureOperations[procedure]
		require.True(t, ok, "%s must be routed, or its expensive work runs uncapped", procedure)
		assert.Equal(t, OpElevation, spec.op, "%s", procedure)
		assert.Equal(t, provesCredential, spec.provesCredential, "%s", procedure)
	}

	// The negative half, and it is the half OpElevation's doc has to keep
	// true. Both of these are elevation-admitted mutations, so a reader who
	// took "every mutation an elevation admits" literally would look for
	// them above -- and both are deliberately absent, because neither runs
	// Argon2 or a ceremony write. RequestEmailChange is capped instead by
	// the pending-email mint cooldown, in SQL, on the row.
	for _, procedure := range []string{
		leapmuxv1connect.UserServiceRequestEmailChangeProcedure,
		leapmuxv1connect.UserServiceUnlinkOAuthProviderProcedure,
	} {
		_, routed := procedureOperations[procedure]
		assert.Falsef(t, routed,
			"%s is not expensive to repeat; routing it means OpElevation's doc must say so too", procedure)
	}
}

// TestPanickingHandlerReleasesReservation pins the panic path: net/http
// recovers a handler panic per connection, so the interceptor must close
// the reservation on the unwind — a leaked slot would deny the user's
// every later attempt until hub restart, because nothing sweeps inFlight.
func TestPanickingHandlerReleasesReservation(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 1, 900)

	handler := connect.NewUnaryHandler(
		leapmuxv1connect.UserServiceElevateSessionProcedure,
		func(ctx context.Context, req *connect.Request[leapmuxv1.ElevateSessionRequest]) (*connect.Response[leapmuxv1.ElevateSessionResponse], error) {
			panic("boom")
		},
		connect.WithInterceptors(NewInterceptor(m)),
	)
	mux := http.NewServeMux()
	mux.Handle(leapmuxv1connect.UserServiceElevateSessionProcedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithUser(r.Context(), &auth.UserInfo{ID: userid.MustNew("usr_test123")}))
		handler.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)

	require.Error(t, tryElevateSession(t, client), "the aborted connection surfaces as an error to the client")

	m.windowMu.Lock()
	assert.Empty(t, m.inFlight, "a panicking handler must not leak its reservation")
	assert.Empty(t, m.windows, "a panic is not a credential failure and must not open a window")
	m.windowMu.Unlock()

	// The budget is whole again: the next attempt reaches the handler
	// instead of failing with ResourceExhausted.
	wrongClient, calls := elevateSessionClient(t, m, wrongPasswordError(), true)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, wrongClient)),
		"the released reservation must re-open the budget")
	assert.Equal(t, 1, *calls)
}

func TestKnownOperationsSortedAndEffectiveLimitsOverlay(t *testing.T) {
	assert.Equal(t, []Operation{OpElevation}, KnownOperations())

	// No row: defaults, enabled.
	m := newTestManager(t, false)
	key, ok := LimitKey(OpElevation)
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
	upsertLimit(t, m, OpElevation, false, 1, 900)
	client, _ := elevateSessionClient(t, m, wrongPasswordError(), true)
	for i := 0; i < 10; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, client)))
	}

	solo := newTestManager(t, true)
	soloClient, _ := elevateSessionClient(t, solo, wrongPasswordError(), true)
	for i := 0; i < 10; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, soloClient)))
	}
}

func TestLimiterDefaultsApplyWithoutRow(t *testing.T) {
	m := newTestManager(t, false)
	limits, ok := DefaultLimits(OpElevation)
	require.True(t, ok)
	assert.EqualValues(t, 5, limits.MaxAttempts)
	assert.EqualValues(t, 900, limits.WindowSeconds)

	client, _ := elevateSessionClient(t, m, wrongPasswordError(), true)
	for i := 0; i < 5; i++ {
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, client)))
	}
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryElevateSession(t, client)))
}

func TestLimiterUnauthenticatedCallsPassThrough(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 1, 900)

	// No user in context: the limiter stands down (in the real chain the
	// auth interceptor already rejected the call).
	client, _ := elevateSessionClient(t, m, nil, false)
	for i := 0; i < 3; i++ {
		require.NoError(t, tryElevateSession(t, client))
	}
}

// TestProcedureRoutingAndCatalogueAgree pins the two hand-maintained maps
// that must change together: every routed operation must be catalogued
// (an uncatalogued op fails closed with CodeUnavailable on every call),
// and every catalogued operation must be routed (KnownOperations advertises
// it to the admin CLI, which would configure a budget nothing enforces).
func TestProcedureRoutingAndCatalogueAgree(t *testing.T) {
	assert.NotEmpty(t, procedureOperations, "no procedures are routed; the tripwire is vacuous")
	for proc, spec := range procedureOperations {
		assert.Containsf(t, defaults, spec.op,
			"procedure %q routes to operation %q with no defaults entry; every call would fail closed with CodeUnavailable", proc, spec.op)
	}
	for op := range defaults {
		routed := false
		for _, spec := range procedureOperations {
			if spec.op == op {
				routed = true
				break
			}
		}
		assert.Truef(t, routed,
			"operation %q is catalogued but no procedureOperations entry routes to it; the admin CLI configures it but nothing enforces it", op)
	}
}

// TestElevationBudgetIsSharedByBothFactorPaths pins that the password path
// and the passkey path spend ONE budget. Two budgets would let an attacker
// take 2N guesses by alternating, which is the whole reason elevation is a
// single operation rather than one per procedure.
func TestElevationBudgetIsSharedByBothFactorPaths(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 2, 900)

	passwordClient, passwordCalls := elevateSessionClient(t, m, wrongPasswordError(), true)
	passkeyClient, passkeyCalls := finishPasskeyElevationClient(t, m, rejectedAssertionError(), true)

	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryElevateSession(t, passwordClient)))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(tryFinishPasskeyElevation(t, passkeyClient)))
	assert.Equal(t, 1, *passwordCalls)
	assert.Equal(t, 1, *passkeyCalls)

	// The budget of 2 is spent -- by one attempt on EACH path.
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryElevateSession(t, passwordClient)))
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(tryFinishPasskeyElevation(t, passkeyClient)))
	assert.Equal(t, 1, *passwordCalls, "a denied attempt must not reach the handler")
	assert.Equal(t, 1, *passkeyCalls, "a denied attempt must not reach the handler")
}

// credentialBearingFields lists every request field through which a user
// presents something the hub VERIFIES: a password, or a WebAuthn assertion
// or attestation. Each one is a guess an attacker can repeat, so a procedure
// carrying one must be rate limited.
//
// credential_json is on the list because it is what a passkey ceremony
// finishes with, and every passkey procedure carries it. Without that entry
// the walk covers ElevateSession alone -- and it would say so silently,
// because a shorter walk still passes.
var credentialBearingFields = map[string]bool{
	"current_password": true,
	"credential_json":  true,
}

// TestCredentialConsumingProceduresAreRouted walks the live user.proto
// descriptor: every UserService method whose request carries something the
// hub verifies must have a procedureOperations entry. The routing map is
// hand-maintained; a new credential-consuming RPC that ships unrouted keeps
// unlimited retries while its siblings are capped, and only a descriptor
// walk catches the forgotten direction.
func TestCredentialConsumingProceduresAreRouted(t *testing.T) {
	fd, err := protoregistry.GlobalFiles.FindFileByPath("leapmux/v1/user.proto")
	require.NoError(t, err, "user.proto descriptor must be registered; import the generated pb package")
	services := fd.Services()
	require.Equal(t, 1, services.Len(), "expected exactly the UserService in user.proto")
	methods := services.Get(0).Methods()
	covered := 0
	for i := 0; i < methods.Len(); i++ {
		method := methods.Get(i)
		consumesSecret := false
		fields := method.Input().Fields()
		for j := 0; j < fields.Len(); j++ {
			if credentialBearingFields[string(fields.Get(j).Name())] {
				consumesSecret = true
				break
			}
		}
		if !consumesSecret {
			continue
		}
		covered++
		procedure := "/" + string(services.Get(0).FullName()) + "/" + string(method.Name())
		_, routed := procedureOperations[procedure]
		assert.Truef(t, routed,
			"UserService.%s carries a credential but has no procedureOperations entry; it ships with unlimited retries (procedure %q)",
			method.Name(), procedure)
	}
	// Non-vacuity: a walk that matched nothing would pass while covering
	// nothing, which is what a renamed field would silently produce.
	assert.GreaterOrEqual(t, covered, 3,
		"the walk found fewer credential-bearing procedures than exist; check credentialBearingFields against user.proto")
}

func TestValidateLimits(t *testing.T) {
	assert.NoError(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 0, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5000, WindowSeconds: 900}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 30}))
	assert.Error(t, ValidateLimits(Limits{MaxAttempts: 5, WindowSeconds: 100000}))
}

// TestElevationRefusalDoesNotSpendTheBudget pins the polarity that matters
// most after the step-up moved off the per-request secret: being UN-elevated
// is a precondition failure, not a wrong guess. If it counted, a user whose
// window simply lapsed would lock themselves out of the very prompt that
// would fix it.
func TestElevationRefusalDoesNotSpendTheBudget(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 1, 900)

	refusal := connect.NewError(connect.CodeFailedPrecondition, errors.New("this action needs a recent sign-in"))
	client, calls := elevateSessionClient(t, m, refusal, true)
	for i := 0; i < 5; i++ {
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(tryElevateSession(t, client)))
	}
	assert.Equal(t, 5, *calls, "every attempt must reach the handler; none counted against the budget")
}

// TestSpentFailureWindowStillAdmitsTheProceduresThatVerifyNothing pins the
// polarity of the DENIAL, which is the other half of what one shared
// operation buys.
//
// One operation covers every path that can present a wrong secret, so an
// attacker cannot alternate between the password and the passkey to double
// their guess budget. But the same key routes the seven mutations an
// elevation ADMITS, and those verify nothing: an un-elevated caller is
// refused with FailedPrecondition, which is not a guess. Denying them on the
// failure count handed an attacker the account owner's whole remedy surface
// -- five wrong passwords locked the owner out of passkey elevation, passkey
// management and the password change for the window, renewably, from a
// stolen cookie the owner wants to defeat.
//
// The set comes from the live routing map, so a procedure added on either
// side of provesCredential is covered without an edit here.
func TestSpentFailureWindowStillAdmitsTheProceduresThatVerifyNothing(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 2, 900)
	recordCredentialFailure(t, m, "usr_test123")
	recordCredentialFailure(t, m, "usr_test123")

	// The precondition: the guess surface really is closed. Without this the
	// case below could pass on a window that was never spent.
	_, allowed, retryAfter, err := m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	require.False(t, allowed, "the budget must be spent before the admissions below mean anything")
	require.Positive(t, retryAfter, "a failure-driven denial reports the window the caller must wait out")

	nonProving := 0
	for procedure, spec := range procedureOperations {
		if spec.provesCredential {
			continue
		}
		nonProving++
		a, allowed, _, err := m.allow(context.Background(), spec, "usr_test123")
		require.NoError(t, err)
		assert.Truef(t, allowed,
			"%s verifies no secret, so a wrong-password burst must not deny it", procedure)
		m.complete(a, nil)
	}
	require.Equal(t, 7, nonProving,
		"the routing map must still carry the seven mutations an elevation admits")

	// And the window is untouched by those admissions: a procedure that
	// proves nothing must neither spend the budget nor clear it.
	_, allowed, _, err = m.allow(context.Background(), elevateSpec, "usr_test123")
	require.NoError(t, err)
	assert.False(t, allowed, "the guess cap must still hold after the admitted mutations ran")
}

// TestSpentFailureWindowStillCapsConcurrency is the limit the change above
// must NOT remove.
//
// The failure count and the in-flight reservation are separate budgets on one
// key. Only the first became conditional; every routed procedure runs an
// Argon2 hash or a ceremony write that takes SQLite's single writer lock, and
// that cost is what the concurrent cap protects, whatever the procedure
// proves.
func TestSpentFailureWindowStillCapsConcurrency(t *testing.T) {
	m := newTestManager(t, false)
	upsertLimit(t, m, OpElevation, true, 2, 900)
	recordCredentialFailure(t, m, "usr_test123")
	recordCredentialFailure(t, m, "usr_test123")

	// Two admitted mutations, left IN FLIGHT.
	first, allowed, _, err := m.allow(context.Background(), beginElevationSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)
	second, allowed, _, err := m.allow(context.Background(), beginElevationSpec, "usr_test123")
	require.NoError(t, err)
	require.True(t, allowed)

	_, allowed, retryAfter, err := m.allow(context.Background(), beginElevationSpec, "usr_test123")
	require.NoError(t, err)
	assert.False(t, allowed, "the concurrent cap stays unconditional")
	assert.Zero(t, retryAfter,
		"an in-flight denial owes no window: the reservations land within one handler latency")

	m.complete(first, nil)
	m.complete(second, nil)
	m.windowMu.Lock()
	assert.Empty(t, m.inFlight, "completed attempts release their reservations")
	m.windowMu.Unlock()
}
