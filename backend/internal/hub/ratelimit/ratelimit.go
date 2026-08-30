// Package ratelimit provides per-user, per-operation fixed-window rate
// limiting for authenticated procedures whose handlers are expensive to
// retry (elevation runs Argon2 or a WebAuthn verification per attempt).
// Operations and their default limits are catalogued here; admins override
// them per operation as rate_limit.<operation> settings keys via the admin
// CLI.
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
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/windowed"
)

// SettingKeyPrefix starts every rate_limit.<operation> settings key.
// limitKeys builds the keys with it and the admin CLI's `rate-limit list`
// selects on it, so the two cannot disagree about which keys belong to
// this domain.
const SettingKeyPrefix = "rate_limit."

// Operation identifies a rate-limited procedure family. The string is the
// suffix of the rate_limit.<operation> settings key.
type Operation string

const (
	// OpElevation limits the per-user elevation surface of UserService:
	// session elevation ("sudo mode") and the elevation-admitted mutations
	// that are EXPENSIVE TO REPEAT.
	//
	// Two elevation-admitted mutations stay out of THIS operation, because
	// neither runs an Argon2 hash or a ceremony write. UnlinkOAuthProvider
	// needs no budget of its own; RequestEmailChange has its own below,
	// because the mint cooldown this comment once cited as its cap holds
	// only while every send succeeds -- the failure path clears the row and
	// exempts the retry. procedureOperations below lists what takes a slot,
	// and TestExpensiveMutationsAreRouted pins that set exactly.
	//
	// One operation carries TWO budgets that the same numbers express, and
	// the difference between them is why procedureSpec exists:
	//
	//   - The FAILURE window counts a wrong secret. Only ElevateSession and
	//     FinishPasskeyElevation can produce one, because they are the only
	//     two procedures that verify a secret the caller must know. The two
	//     factors share the window, so an attacker cannot double their
	//     attempts by alternating between the password path and the passkey
	//     path.
	//   - The IN-FLIGHT reservation caps concurrency. Every routed procedure
	//     takes a slot, because each one runs work that is expensive to
	//     repeat: an Argon2 hash (ElevateSession, ChangePassword, and the
	//     replacement password that DeletePasskey and DeactivatePasskeyAuth
	//     accept) or a ceremony write that takes SQLite's single writer lock
	//     (every Begin stage).
	//
	// One operation rather than several, so the two budgets cannot drift
	// apart and an operator has one number to set.
	OpElevation Operation = "elevation"

	// OpEmailChange limits UserService.RequestEmailChange, the one mail RPC
	// behind neither a captcha nor this package's failure window: it needs
	// only an elevated session, and the send it drives costs the relay a
	// transaction whether the relay accepts it or not.
	//
	// It counts requests that PROCEEDED -- reached the mint and the send --
	// in a fixed window (countsProceededRequests on the catalogue entry),
	// not failures and not admissions. Not failures, because an abuse loop
	// here SUCCEEDS per request: the mint lands, the relay answers, the
	// failed code clears, so a failures-only window never sees it. Not
	// admissions, because the legitimate user's first attempt is usually
	// REFUSED before anything mails: the step-up prompt answers
	// FailedPrecondition and the transport retries once, a malformed or
	// unchanged address answers InvalidArgument, and a taken address
	// answers AlreadyExists -- none of them cost the relay anything, and
	// the mint cooldown and the per-recipient budget independently cap
	// every send. Proceeded outcomes only: success, a send the relay
	// refused (Unavailable), and a mint the cooldown refused
	// (ResourceExhausted) -- the hammering loop hits exactly those.
	OpEmailChange Operation = "email_change"

	// OpOAuthAnonymous limits the authorization server's ANONYMOUS endpoints --
	// every route mounted through anonymousLeg: device authorization, the
	// token exchange, revocation, dynamic registration, step-up, and the app
	// icons.
	//
	// They are the only endpoints on the hub an unauthenticated caller can
	// drive in a loop against the store, and no interceptor sees them: they
	// are mux routes rather than Connect procedures, so they reach this
	// package through AllowHTTP instead.
	//
	// It is keyed by CLIENT ADDRESS rather than by user, because there is no
	// user. See clientAddressKey for what that costs behind a proxy, and why
	// the budget is sized the way it is.
	//
	// The budget is deliberately generous: a device-code client polls every
	// five seconds for up to ten minutes, which is 120 requests for ONE
	// authorization, and several clients legitimately share one address. What
	// it stops is the unbounded case -- a script minting device grants or
	// registrations as fast as the hub answers.
	//
	// It counts ADMITTED REQUESTS in a fixed window (allowWindowed), not
	// credential failures: a wrong device_code is not a guess at a secret (the
	// code is 128 bits of entropy the hub issued), and counting only failures
	// would let an attacker exhaust a shared address's budget with garbage.
	//
	// It stays ENFORCED in solo mode, unlike every per-user operation. The
	// per-user stand-down reasons about the thing counted ("one user, so no
	// per-user abuse surface"); this budget counts addresses, and a solo hub
	// that listens beyond the loopback interface serves anonymous addresses
	// like any other -- which is exactly why its settings key stays visible
	// there.
	OpOAuthAnonymous Operation = "oauth_anonymous"
)

// Limits is one operation's effective budget.
type Limits struct {
	MaxAttempts   int64
	WindowSeconds int64
}

// ValidateLimits rejects budgets that could make an account unusable
// (absurdly small windows or attempt counts far outside anything
// legitimate).
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
	// countsProceededRequests counts, in the fixed window, the requests
	// whose handler outcome says they reached the thing the budget guards.
	// An operation whose abuse loop succeeds per request needs this: a
	// failures-only window never sees a caller the handler keeps answering.
	// Counting happens in complete(), against the handler outcome, so the
	// refusals that precede the work (elevation prompts, validation,
	// taken-address checks) spend nothing, and provesCredential stays false
	// for every such operation so a success never resets the window.
	countsProceededRequests bool
	// proceedsToBudget classifies a NON-NIL handler error as one that
	// reached the thing the budget guards (nil always counts -- a success
	// proceeded). It is required when countsProceededRequests is set.
	proceedsToBudget func(error) bool
	// hiddenInSolo drops the operation's settings key from ListSettings on a
	// solo hub, and it is a PER-OPERATION answer rather than a property of
	// rate limiting.
	//
	// The question it asks is whether the thing counted still happens with one
	// user and no sign-up. For a per-user credential ceremony the answer is no,
	// so the key is inert clutter. For a limit keyed by client ADDRESS on an
	// endpoint solo also serves, the answer is yes -- and hiding the key would
	// take it out of the preferences dialog AND out of `leapmux control admin
	// settings`, leaving an operator who runs `leapmux solo -listen
	// 0.0.0.0:4327` no way to reach it.
	hiddenInSolo bool
}

// defaults is the code-side source of truth applied when no settings row
// exists for an operation. Adding an operation here plus a
// procedureOperations entry below is all it takes to protect a new
// procedure — no schema change.
var defaults = map[Operation]opSpec{
	OpElevation: {
		limits: Limits{MaxAttempts: 5, WindowSeconds: 900},
		// One user, and elevation is keyed by that user.
		hiddenInSolo: true,
		isCredentialFailure: func(err error) bool {
			// The two factors a user may present to elevate share one
			// budget, so an attacker cannot double their attempts by
			// alternating between the password and the passkey path.
			return errors.Is(err, auth.ErrInvalidCurrentPassword) || errors.Is(err, auth.ErrInvalidElevationAssertion)
		},
	},
	OpEmailChange: {
		// Six per fifteen minutes: a person fixing a typo changes an
		// address two or three times, and the step-up prompt plus one
		// validation miss cost them nothing; a loop is capped at six
		// mail-driving attempts a quarter hour instead of request speed.
		limits: Limits{MaxAttempts: 6, WindowSeconds: 900},
		// No error here is a credential guess, and the proceeded requests
		// carry their own counting, so nothing lands in a failure window.
		isCredentialFailure:     func(error) bool { return false },
		countsProceededRequests: true,
		// Proceeded outcomes: the send landed (nil), the relay or the mint
		// failed after the request reached them (Unavailable), or the mint
		// cooldown refused (ResourceExhausted) -- the hammering loop's own
		// shape. FailedPrecondition (elevation prompt), InvalidArgument
		// (malformed or unchanged address) and AlreadyExists (taken
		// address) all answer before anything mails, so they spend nothing.
		proceedsToBudget: func(err error) bool {
			code := connect.CodeOf(err)
			return code == connect.CodeUnavailable || code == connect.CodeResourceExhausted
		},
		// Solo refuses email changes outright (rejectSolo), so the key is
		// inert there.
		hiddenInSolo: true,
	},
	OpOAuthAnonymous: {
		// 600 in ten minutes: five device-code polls' worth of headroom on one
		// shared address, and still a hard ceiling on a loop.
		limits: Limits{MaxAttempts: 600, WindowSeconds: 600},
		// Nothing here presents a secret the caller had to guess, so no error
		// counts against the failure window. See OpOAuthAnonymous.
		isCredentialFailure: func(error) bool { return false },
		// A solo hub authorizes apps like any other -- the solo rung yields to
		// a presented bearer -- and these endpoints are anonymous there too.
		hiddenInSolo: false,
	},
}

// limitKeys holds one settings key per catalogued operation, derived from
// the same catalogue so a new operation brings its key with it.
var limitKeys = func() map[Operation]*settings.Key[LimitValue] {
	keys := make(map[Operation]*settings.Key[LimitValue], len(defaults))
	for op, spec := range defaults {
		// The summary states what one unit of the budget is, because the
		// three counting modes size different quantities: a failures window
		// counts refused credential guesses, an admitted-request window
		// (OpOAuthAnonymous, through allowWindowed on the HTTP path) counts
		// every request the limiter admits, and OpEmailChange counts the
		// requests that reached the mail machinery. An operator tuning a
		// key must know which quantity the knobs raise.
		countedQuantity := "failed attempts"
		if spec.countsProceededRequests {
			countedQuantity = "mail-driving requests"
		} else if op == OpOAuthAnonymous {
			countedQuantity = "admitted requests"
		}
		keys[op] = settings.NewKey[LimitValue](SettingKeyPrefix + string(op)).
			WithDefault(LimitValue{
				Enabled:       true,
				MaxAttempts:   spec.limits.MaxAttempts,
				WindowSeconds: spec.limits.WindowSeconds,
			}).
			WithValidate(func(v LimitValue) error {
				return ValidateLimits(Limits{MaxAttempts: v.MaxAttempts, WindowSeconds: v.WindowSeconds})
			}).
			WithUI(settings.UIMeta{
				Category:     "rate-limits",
				Title:        "Rate limit - " + string(op),
				Summary:      fmt.Sprintf("rate limit for %s (%s per window)", op, countedQuantity),
				HiddenInSolo: spec.hiddenInSolo,
				Fields: []settings.Field{
					{Name: "enabled", Label: "Enabled", Kind: settings.FieldBool},
					{Name: "max_attempts", Label: "Max attempts", Kind: settings.FieldInt,
						Min: ptrconv.Ptr[int64](1), Max: ptrconv.Ptr[int64](1000), Unit: "count"},
					{Name: "window_seconds", Label: "Window", Kind: settings.FieldInt,
						Min: ptrconv.Ptr[int64](60), Max: ptrconv.Ptr[int64](86400), Unit: "seconds"},
				},
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

// procedureSpec routes one procedure to its operation and records whether a
// SUCCESS there proves the credential the failure window counts.
//
// The distinction is the whole reason this is a struct and not a bare
// Operation. complete() resets the window on a success, because a user who
// proved the secret makes the accumulated failures irrelevant. That reasoning
// holds only for a procedure that actually verifies the secret. A procedure
// that verifies nothing and succeeds for anyone -- BeginPasskeyElevation
// mints assertion options, ChangePassword runs on an already elevated
// session -- would otherwise clear the attacker's own failure count on
// demand, and the password-guess cap would be unlimited.
type procedureSpec struct {
	op Operation
	// provesCredential is true only when a success means the caller
	// presented the secret the operation's failure window counts.
	provesCredential bool
}

// procedureOperations routes ConnectRPC procedures to their operations.
// The interceptor must be registered AFTER the auth interceptor so the
// authenticated user is already in the context.
//
// Everything routed here takes an in-flight slot. Only the two paths that
// verify a secret carry provesCredential; see procedureSpec.
var procedureOperations = map[string]procedureSpec{
	// The two factor paths. A wrong answer counts, and a right one clears.
	leapmuxv1connect.UserServiceElevateSessionProcedure:         {op: OpElevation, provesCredential: true},
	leapmuxv1connect.UserServiceFinishPasskeyElevationProcedure: {op: OpElevation, provesCredential: true},
	// The Begin stage of the passkey path proves nothing: it mints assertion
	// options for the caller's own session and succeeds for any account
	// that holds a passkey. It is routed for the ceremony write it performs,
	// never for the budget.
	leapmuxv1connect.UserServiceBeginPasskeyElevationProcedure: {op: OpElevation},
	// The mutations an elevation admits. None verifies a secret -- an
	// un-elevated caller is refused with FailedPrecondition, which is not a
	// guess -- so none counts and none clears. They are routed for the
	// Argon2 hash and the ceremony write they run.
	leapmuxv1connect.UserServiceChangePasswordProcedure:            {op: OpElevation},
	leapmuxv1connect.UserServiceBeginPasskeyRegistrationProcedure:  {op: OpElevation},
	leapmuxv1connect.UserServiceFinishPasskeyRegistrationProcedure: {op: OpElevation},
	leapmuxv1connect.UserServiceRenamePasskeyProcedure:             {op: OpElevation},
	leapmuxv1connect.UserServiceDeletePasskeyProcedure:             {op: OpElevation},
	leapmuxv1connect.UserServiceDeactivatePasskeyAuthProcedure:     {op: OpElevation},
	// The mail RPC with no captcha and no secret to guess: it takes its
	// budget from OpEmailChange, which counts every admitted request. See
	// that operation's catalogue entry for why failures would not do.
	leapmuxv1connect.UserServiceRequestEmailChangeProcedure: {op: OpEmailChange},
}

// effectiveLimit is the resolved per-operation policy.
type effectiveLimit struct {
	enabled bool
	limits  Limits
}

// Manager tracks fixed-window failure counters per (operation, user).
//
// Counters are in-memory per process: a restart clears them, and the
// singleton runtime lease means a successor hub starts from zero rather than
// inheriting anybody's window. The window limits the
// exposure both ways — an attacker cannot inherit a lockout, and a victim
// is never locked out for longer than one window.
type Manager struct {
	set  *settings.Manager
	solo bool
	now  func() time.Time

	windowMu sync.Mutex // guards windows and inFlight
	windows  windowed.Windows[windowKey]
	// inFlight counts attempts allow() reserved but complete() did not
	// close yet, so a concurrent burst cannot evade a failures-only
	// check (every burst member reads failures below max before any of
	// them lands).
	inFlight map[windowKey]int64
}

type windowKey struct {
	op     Operation
	userID string
}

// errHandlerPanicked marks an attempt whose handler panicked below the
// interceptor. It is not a credential failure, so complete() releases the
// reservation without counting it.
var errHandlerPanicked = errors.New("handler panicked")

// NewManager creates a rate-limit manager over the shared settings
// snapshot (its TTL is the admin-CLI propagation limit). Solo mode stands
// down every PER-USER budget: it is a local single-user deployment whose
// only "attacker" is the local user. The one ADDRESS-keyed budget
// (OpOAuthAnonymous) still enforces there; see its catalogue entry.
func NewManager(set *settings.Manager, soloMode bool) *Manager {
	return &Manager{
		set:      set,
		solo:     soloMode,
		now:      time.Now,
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
	// provesCredential carries the routed procedure's own answer to "does a
	// success here mean the caller presented the secret". complete() resets
	// the failure window only for those, so a procedure that verifies
	// nothing cannot clear an attacker's accumulated failures. The attempt
	// carries it rather than complete() reading it again, for the same
	// reason window and countable do: the attempt records the policy that
	// admitted it.
	provesCredential bool
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
func (m *Manager) allow(ctx context.Context, spec procedureSpec, userID string) (*attempt, bool, time.Duration, error) {
	op := spec.op
	limit, err := m.resolve(ctx, op)
	if err != nil {
		return nil, false, 0, err
	}
	a := &attempt{
		op:               op,
		userID:           userID,
		window:           limit.limits.WindowSeconds,
		countable:        limit.enabled,
		provesCredential: spec.provesCredential,
	}
	if !limit.enabled {
		return a, true, 0, nil
	}

	now := m.now()
	m.windowMu.Lock()
	defer m.windowMu.Unlock()
	m.windows.Sweep(now)
	key := windowKey{op, userID}
	w := m.windows.Get(key, now)
	// Recorded outcomes deny only the procedures whose budget they spent.
	//
	// One operation covers every path that can present a wrong secret, so
	// that an attacker cannot alternate between the password and the passkey
	// to double its guess budget. But the same key also routes the seven
	// mutations an elevation ADMITS, and those verify nothing: an
	// un-elevated caller is refused with FailedPrecondition, which is not a
	// guess. Denying them on the failure count handed an attacker the
	// account owner's whole remedy surface -- five wrong passwords locked
	// the owner out of passkey elevation, passkey management and the
	// password change for the window, renewably. The seven already require
	// an elevation the guesser does not hold, so admitting them widens
	// nothing. The proceeded-request window (email change) reads the same
	// way: only an outcome that reached the mail machinery spent it.
	//
	// The IN-FLIGHT reservation stays unconditional. Every routed procedure
	// runs an Argon2 hash or a ceremony write, and that cost is what the
	// concurrent cap protects, whatever the procedure proves.
	var spent int64
	readsSpentWindow := spec.provesCredential || defaults[op].countsProceededRequests
	if readsSpentWindow && w != nil {
		spent = w.Count
	}
	if spent >= limit.limits.MaxAttempts {
		// resetAt came from m.now(), so the retry duration must be
		// measured against the same clock — time.Until would use the
		// wall clock and diverge wherever now is injected (and in
		// these tests, frozen).
		return a, false, w.ResetAt.Sub(now), nil
	}
	if spent+m.inFlight[key] >= limit.limits.MaxAttempts {
		// The denial is driven by in-flight reservations, not recorded
		// outcomes: the reservations land within one handler latency, so
		// the zero retry duration renders as the one-second floor rather
		// than a full window the user does not actually owe.
		return a, false, 0, nil
	}
	m.inFlight[key]++
	a.reserved = true
	return a, true, 0, nil
}

// allowWindowed reports whether userID may drive op right now, counting the
// request in the SAME fixed window the failure path keeps.
//
// It exists for the anonymous HTTP endpoints, where no handler completion
// exists to close an in-flight reservation. Reusing allow() there was not an
// option: allow() reserves an in-flight slot that only complete() releases,
// and an endpoint with no completion would turn the concurrency counter into a
// monotone lifetime counter -- after MaxAttempts requests from one address,
// ever, every later request 429s until the hub restarts, and the in-flight
// map retains one entry per address for the process lifetime.
//
// The window semantics are the same ones complete() writes: anchored at the
// first request, self-expiring one window later through the prune sweep.
func (m *Manager) allowWindowed(ctx context.Context, op Operation, userID string) (bool, time.Duration, error) {
	limit, err := m.resolve(ctx, op)
	if err != nil {
		return false, 0, err
	}
	if !limit.enabled {
		return true, 0, nil
	}
	now := m.now()
	m.windowMu.Lock()
	defer m.windowMu.Unlock()
	m.windows.Sweep(now)
	key := windowKey{op, userID}
	if w := m.windows.Get(key, now); w != nil && w.Count >= limit.limits.MaxAttempts {
		return false, w.ResetAt.Sub(now), nil
	}
	w := m.windows.Anchor(key, now, time.Duration(limit.limits.WindowSeconds)*time.Second)
	// Count names no policy: for a windowed operation it counts admitted
	// requests, which is the quantity this budget limits.
	w.Count++
	return true, 0, nil
}

// complete closes the reservation allow() made and records the handler's
// outcome: a success on a procedure that PROVES the credential resets the
// window (the user just showed they know the secret, so the accumulated
// failures were irrelevant), a countable credential failure extends it, and any
// other error keeps it untouched. An attempt without a reservation (denied,
// or admitted under a disabled policy) releases nothing.
//
// The reset is restricted to a proving procedure, and the restriction is
// the guard rather than a refinement. Every routed procedure takes a slot,
// so most of them succeed without verifying anything: an unrestricted reset
// would let a caller clear its own failure count by calling one of those
// between guesses, and the cap on the hub's only credential-guess surface
// would be unlimited. See procedureSpec.
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
	// A proceeded-request budget spends on the outcomes that reached the
	// thing it guards: success always proceeded, and the catalogue's
	// predicate classifies the failures that did. Refusals that answer
	// before the work -- the elevation prompt, validation, a taken
	// address -- spend nothing, so a step-up retry costs the caller one
	// slot, not two.
	if spec, known := defaults[a.op]; a.countable && known && spec.countsProceededRequests {
		if handlerErr == nil || spec.proceedsToBudget(handlerErr) {
			// Fixed window anchored at the first proceeded request:
			// self-expires one window later through the sweep.
			m.windows.Anchor(key, now, time.Duration(a.window)*time.Second).Count++
		}
	}
	if handlerErr == nil {
		if a.provesCredential {
			m.windows.Delete(key)
		}
		return
	}
	if !a.countable {
		return
	}
	spec, known := defaults[a.op]
	if !known || !spec.isCredentialFailure(handlerErr) {
		return
	}
	// Fixed window anchored at the first failure: a burst of N failures
	// trips the limit, and the counter self-expires one window later.
	m.windows.Anchor(key, now, time.Duration(a.window)*time.Second).Count++
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
			spec, ok := procedureOperations[req.Spec().Procedure]
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

			att, allowed, retryAfter, err := m.allow(ctx, spec, userID)
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
