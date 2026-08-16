// Package ratelimit provides per-user, per-operation fixed-window rate
// limiting for authenticated procedures whose handlers are expensive to
// retry (ChangePassword runs Argon2 per attempt). Operations and their
// default limits are catalogued here; admins override them per operation
// as rate_limit.<operation> settings keys via the admin CLI.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// Operation identifies a rate-limited procedure family. The string is the
// suffix of the rate_limit.<operation> settings key.
type Operation string

const (
	// OpChangePassword rate-limits UserService.ChangePassword per user.
	OpChangePassword Operation = "change-password"
)

// Limits is one operation's effective budget.
type Limits struct {
	MaxAttempts   int64
	WindowSeconds int64
}

// ValidateLimits rejects budgets that could brick an account (absurdly
// small windows or attempt counts far outside anything legitimate).
func ValidateLimits(l Limits) error {
	if l.MaxAttempts < 1 || l.MaxAttempts > 1000 {
		return fmt.Errorf("max attempts must be between 1 and 1000 (got %d)", l.MaxAttempts)
	}
	if l.WindowSeconds < 60 || l.WindowSeconds > 86400 {
		return fmt.Errorf("window must be between 60s and 86400s (got %ds)", l.WindowSeconds)
	}
	return nil
}

// LimitValue is the rate_limit.<operation> settings document: the enabled
// switch plus the budget. A stored document's omitted fields keep the
// operation's catalogue defaults, so `{"enabled": false}` disables
// without restating the budget.
type LimitValue struct {
	Enabled       bool  `json:"enabled"`
	MaxAttempts   int64 `json:"max_attempts,omitempty"`
	WindowSeconds int64 `json:"window_seconds,omitempty"`
}

// opSpec is one catalogue entry: the default budget plus the predicate
// that classifies a handler error as a countable credential failure. The
// predicate lives here, not in the interceptor, so a new operation brings
// its own failure signal with it instead of editing interceptor control
// flow.
type opSpec struct {
	limits              Limits
	isCredentialFailure func(error) bool
}

// defaults is the code-side source of truth applied when no settings row
// exists for an operation. Adding an operation here plus a
// procedureOperations entry below is all it takes to protect a new
// procedure — no schema change.
var defaults = map[Operation]opSpec{
	OpChangePassword: {
		limits: Limits{MaxAttempts: 5, WindowSeconds: 900},
		isCredentialFailure: func(err error) bool {
			return errors.Is(err, auth.ErrInvalidCurrentPassword)
		},
	},
}

// limitKeys holds one settings key per catalogued operation, derived from
// the same catalogue so a new operation brings its key with it.
var limitKeys = func() map[Operation]*settings.Key[LimitValue] {
	keys := make(map[Operation]*settings.Key[LimitValue], len(defaults))
	for op, spec := range defaults {
		keys[op] = settings.NewKey[LimitValue]("rate_limit." + string(op)).
			WithDefault(LimitValue{
				Enabled:       true,
				MaxAttempts:   spec.limits.MaxAttempts,
				WindowSeconds: spec.limits.WindowSeconds,
			}).
			WithValidate(func(v LimitValue) error {
				return ValidateLimits(Limits{MaxAttempts: v.MaxAttempts, WindowSeconds: v.WindowSeconds})
			})
	}
	return keys
}()

// LimitKey returns the settings key for one operation (the admin CLI's
// read/write handle).
func LimitKey(op Operation) (*settings.Key[LimitValue], bool) {
	key, ok := limitKeys[op]
	return key, ok
}

// SettingsDescriptors lists the rate-limit keys for settings-manager
// registration, in catalogue order.
func SettingsDescriptors() []settings.Descriptor {
	out := make([]settings.Descriptor, 0, len(limitKeys))
	for _, op := range KnownOperations() {
		out = append(out, limitKeys[op])
	}
	return out
}

// DefaultLimits returns the built-in budget for an operation.
func DefaultLimits(op Operation) (Limits, bool) {
	spec, ok := defaults[op]
	return spec.limits, ok
}

// KnownOperations lists every catalogued operation, sorted, for the admin
// CLI and error messages.
func KnownOperations() []Operation {
	ops := make([]Operation, 0, len(defaults))
	for op := range defaults {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i] < ops[j] })
	return ops
}

// procedureOperations routes ConnectRPC procedures to their operations.
// The interceptor must be registered AFTER the auth interceptor so the
// authenticated user is already in the context.
var procedureOperations = map[string]Operation{
	leapmuxv1connect.UserServiceChangePasswordProcedure: OpChangePassword,
}

// effectiveLimit is the resolved per-operation policy.
type effectiveLimit struct {
	enabled bool
	limits  Limits
}

// Manager tracks fixed-window failure counters per (operation, user).
//
// Counters are in-memory per hub instance: restarting clears them and
// multi-instance deployments count independently. The window limits the
// exposure both ways — an attacker cannot inherit a lockout, and a victim
// is never locked out for longer than one window.
type Manager struct {
	set  *settings.Manager
	solo bool
	now  func() time.Time

	windowMu sync.Mutex // guards windows, inFlight, and minResetAt
	windows  map[windowKey]*windowState
	// inFlight counts attempts allow() reserved but complete() has not
	// closed yet, so a concurrent burst cannot slip past a failures-only
	// check (every burst member reads failures below max before any of
	// them lands).
	inFlight map[windowKey]int64
	// minResetAt is the earliest resetAt among live windows (zero when
	// unknown). It keeps the prune sweep off the hot path: the pass runs
	// only when some window has actually expired, never on a timer.
	minResetAt time.Time
}

type windowKey struct {
	op     Operation
	userID string
}

type windowState struct {
	failures int64
	resetAt  time.Time
}

// errHandlerPanicked marks an attempt whose handler panicked below the
// interceptor. It is not a credential failure, so complete() releases the
// reservation without counting it.
var errHandlerPanicked = errors.New("handler panicked")

// NewManager creates a rate-limit manager over the shared settings
// snapshot (its TTL is the admin-CLI propagation bound). Solo mode never
// limits: it is a local single-user deployment whose only "attacker" is
// the local user.
func NewManager(set *settings.Manager, soloMode bool) *Manager {
	return &Manager{
		set:      set,
		solo:     soloMode,
		now:      time.Now,
		windows:  make(map[windowKey]*windowState),
		inFlight: make(map[windowKey]int64),
	}
}

// attempt is one admitted try. allow() reserves it; complete() closes it.
// The caller passes the handler's outcome to complete, which releases the
// reservation and records the result under the policy that admitted the
// attempt — never a policy a cache refresh swapped in mid-flight.
type attempt struct {
	op        Operation
	userID    string
	window    int64 // effective window seconds at admission
	countable bool  // the effective policy had the limit enabled
	// reserved is true only when allow() counted this attempt against the
	// in-flight budget. A denial, or an admission under a disabled policy,
	// makes no reservation, so complete() on such an attempt releases
	// nothing — it cannot consume a slot a different attempt holds.
	reserved bool
}

// allow reports whether userID may attempt op right now and, when it
// admits the try, reserves one in-flight slot against the budget. The
// caller MUST pass the attempt and the handler's outcome to complete;
// the reservation is what stops a concurrent burst from all passing a
// failures-only check. retryAfter is nonzero only on a denial that a
// failure window drove; a denial driven by in-flight reservations clears
// within one handler latency and reports zero.
func (m *Manager) allow(ctx context.Context, op Operation, userID string) (*attempt, bool, time.Duration, error) {
	limit, err := m.resolve(ctx, op)
	if err != nil {
		return nil, false, 0, err
	}
	a := &attempt{op: op, userID: userID, window: limit.limits.WindowSeconds, countable: limit.enabled}
	if !limit.enabled {
		return a, true, 0, nil
	}

	now := m.now()
	m.windowMu.Lock()
	defer m.windowMu.Unlock()
	m.pruneExpiredLocked(now)
	key := windowKey{op, userID}
	w := m.windows[key]
	if w != nil && now.After(w.resetAt) {
		// The window expired: drop the entry instead of leaving it inert
		// in the map.
		delete(m.windows, key)
		w = nil
	}
	if w != nil && w.failures+m.inFlight[key] >= limit.limits.MaxAttempts {
		if w.failures >= limit.limits.MaxAttempts {
			// resetAt came from m.now(), so the retry duration must be
			// measured against the same clock — time.Until would use the
			// wall clock and diverge wherever now is injected (and in
			// these tests, frozen).
			return a, false, w.resetAt.Sub(now), nil
		}
		// The denial is driven by in-flight reservations, not recorded
		// failures: the reservations land within one handler latency, so
		// the zero retry duration renders as the one-second floor rather
		// than a full window the user does not actually owe.
		return a, false, 0, nil
	}
	if w == nil && m.inFlight[key] >= limit.limits.MaxAttempts {
		// No failure window is open, but a concurrent burst holds the whole
		// budget in flight. Deny so the burst cannot slip past a
		// failures-only check.
		return a, false, 0, nil
	}
	m.inFlight[key]++
	a.reserved = true
	return a, true, 0, nil
}

// complete closes the reservation allow() made and records the handler's
// outcome: a success resets the window (the user just proved knowledge of
// the credential, so accumulated failures were noise), a countable
// credential failure extends it, and any other error keeps it untouched.
// An attempt without a reservation (denied, or admitted under a disabled
// policy) releases nothing.
func (m *Manager) complete(a *attempt, handlerErr error) {
	if a == nil || !a.reserved {
		return
	}
	now := m.now()
	m.windowMu.Lock()
	defer m.windowMu.Unlock()
	key := windowKey{a.op, a.userID}
	if n := m.inFlight[key]; n <= 1 {
		delete(m.inFlight, key)
	} else {
		m.inFlight[key] = n - 1
	}
	if handlerErr == nil {
		delete(m.windows, key)
		return
	}
	if !a.countable {
		return
	}
	spec, known := defaults[a.op]
	if !known || !spec.isCredentialFailure(handlerErr) {
		return
	}
	w := m.windows[key]
	if w == nil || now.After(w.resetAt) {
		// Fixed window anchored at the first failure: a burst of N failures
		// trips the limit, and the counter self-expires one window later.
		w = &windowState{resetAt: now.Add(time.Duration(a.window) * time.Second)}
		m.windows[key] = w
	}
	if m.minResetAt.IsZero() || w.resetAt.Before(m.minResetAt) {
		m.minResetAt = w.resetAt
	}
	w.failures++
}

// pruneExpiredLocked drops windows whose reset has passed. The same-key
// lazy delete in allow only fires when that user returns, so without this
// sweep a user who fails once and never retries would occupy memory for
// the life of the process. minResetAt keeps the pass off the hot path:
// it runs only when some window has actually expired, and recomputes the
// next expiry so the map holds nothing inert.
func (m *Manager) pruneExpiredLocked(now time.Time) {
	if !m.minResetAt.IsZero() && now.Before(m.minResetAt) {
		return
	}
	next := time.Time{}
	for k, w := range m.windows {
		if now.After(w.resetAt) {
			delete(m.windows, k)
			continue
		}
		if next.IsZero() || w.resetAt.Before(next) {
			next = w.resetAt
		}
	}
	m.minResetAt = next
}

// resolve returns the operation's effective policy from the shared
// settings snapshot: the stored document merged onto the catalogue
// default, so what the admin CLI shows and writes is exactly what the hub
// enforces.
func (m *Manager) resolve(ctx context.Context, op Operation) (effectiveLimit, error) {
	key, ok := limitKeys[op]
	if !ok {
		return effectiveLimit{}, fmt.Errorf("unknown rate-limit operation %q", op)
	}
	v := key.Of(m.set.Snapshot(ctx))
	return effectiveLimit{
		enabled: v.Enabled,
		limits:  Limits{MaxAttempts: v.MaxAttempts, WindowSeconds: v.WindowSeconds},
	}, nil
}

// NewInterceptor returns a unary interceptor enforcing per-user limits on
// the routed procedures. It must be chained inside (after) the auth
// interceptor: it reads the authenticated user from the context, and for
// unauthenticated calls it stands down and lets auth produce the error.
func NewInterceptor(m *Manager) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			op, ok := procedureOperations[req.Spec().Procedure]
			if !ok {
				return next(ctx, req)
			}
			user := auth.GetUser(ctx)
			if user == nil {
				return next(ctx, req)
			}
			if m.solo {
				return next(ctx, req)
			}
			userID := user.ID.String()

			att, allowed, retryAfter, err := m.allow(ctx, op, userID)
			if err != nil {
				// Config unreachable: fail closed for safety, same as the
				// captcha interceptor — but with an honest code, so
				// monitoring can tell an infrastructure fault from an
				// enforced lockout (both surface as ResourceExhausted
				// otherwise). The handler would likely fail on the same
				// store anyway.
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("rate limit unavailable, try again"))
			}
			if !allowed {
				return nil, connect.NewError(connect.CodeResourceExhausted,
					fmt.Errorf("too many attempts; try again in %s", formatWindow(retryAfter)))
			}

			// A panicking handler must not leak the reservation: complete
			// runs on the unwind with a non-countable error (the panic is
			// not a credential failure), and the panic continues up to
			// net/http's per-connection recover exactly as before.
			var resp connect.AnyResponse
			var handlerErr error
			func() {
				defer func() {
					if r := recover(); r != nil {
						m.complete(att, errHandlerPanicked)
						panic(r)
					}
				}()
				resp, handlerErr = next(ctx, req)
			}()
			m.complete(att, handlerErr)
			return resp, handlerErr
		}
	}
}

// formatWindow renders a retry window in whole seconds, minimum one.
func formatWindow(d time.Duration) string {
	s := int64(d.Seconds())
	if s < 1 {
		s = 1
	}
	plural := "s"
	if s == 1 {
		plural = ""
	}
	return fmt.Sprintf("%d second%s", s, plural)
}
