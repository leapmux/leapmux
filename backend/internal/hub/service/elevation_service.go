package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// Session elevation ("sudo mode").
//
// One proven factor admits every sensitive action for auth.ElevationWindow,
// and each successful action slides that window forward up to
// auth.ElevationMaxTotal measured from the proof. Two factors can prove it --
// a password (ElevateSession) and a passkey (Begin/FinishPasskeyElevation) --
// and an OAuth-only account uses the re-authentication leg at
// /auth/idp/<provider>/reauth instead, because it holds neither.
//
// Elevation is per PROCESS in exactly one respect: the in-memory UserInfo
// cache. The state itself is a pair of columns, so any later reader -- this
// hub after a restart, or the watcher's replay -- reads it from the row; but
// a process that already cached this session serves the old deadline until
// the grant's user_info event reaches it. Grant and
// drop both emit that event, and a slide deliberately does not -- a stale
// SHORTER deadline fails closed.
//
// That silence costs the CLIENT its copy of the deadline, and
// ElevationExpiresAtHeader supplies the replacement: the hub reports the
// deadline it holds on the response to the request that slid the window, so
// the caller that acted adopts the new value in the same round trip. It
// reaches that caller alone; another tab keeps the deadline it last read until
// it reads the account again.

// elevationRequiredError refuses a sensitive action on a session that is not
// elevated.
//
// FailedPrecondition, deliberately, NOT Unauthenticated. The frontend's
// global interceptor treats Unauthenticated as "your session ended" and
// signs the user out, so the one refusal whose remedy is "prove a factor and
// try again" would instead discard the session the user is about to prove
// a factor for.
//
// The wording is elevationRequiredMessage, the default every caller but one
// takes. See elevationRequiredErrorSaying for the exception.
func elevationRequiredError() error {
	return elevationRequiredErrorSaying(elevationRequiredMessage)
}

// elevationRequiredErrorSaying is the same refusal with different wording,
// for the one caller whose remedy is WIDER than "prove a factor": the
// first-credential rule, where verifying at an identity provider AND signing
// in again both admit, and only that rule can say so.
//
// The message is a required parameter rather than a variadic. A variadic
// accepts two strings and silently ignores all but the first, which is a call
// a reader cannot get right from the signature.
//
// It still WRAPS errElevationRequired, so errors.Is keeps answering for every
// refusal of this kind. Replacing the cause left the sentinel true of five
// refusals and false of the sixth, with nothing but the header to distinguish
// them.
func elevationRequiredErrorSaying(message string) error {
	err := connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("%w: %s", errElevationRequired, message))
	// A machine-readable marker, so a client can distinguish THIS
	// FailedPrecondition from every other one (an account with no password, a
	// last passkey that needs a replacement) and open a step-up prompt for it
	// alone. Matching the message text instead would break on the first
	// rewording, and the wording is user-facing prose that somebody will reword.
	err.Meta().Set(ElevationRequiredHeader, "1")
	return err
}

// ElevationRequiredHeader marks a refusal whose remedy is "prove a factor
// and retry". Exported because the frontend and the E2E suite both key on
// it; the value is always "1" and only its presence is meaningful. The name
// is owned by contracts/headers.json, generated into the hub, the CLI, and
// the browser alike.
const ElevationRequiredHeader = contracts.ElevationRequiredHeader

// ElevationExpiresAtHeader reports the elevation deadline the hub holds NOW,
// on the response to the request that SLID the window. Exported for the same
// reason ElevationRequiredHeader is: a reader outside this package keys on the
// name, and the frontend's transport interceptor reads it off every response
// (frontend/src/api/transport.ts, which carries the lowercased form).
//
// It does not repeat what a GRANT already says. Both factor paths report
// elevation_expires_at in their response BODY, and a client reads it there. A
// slide has no response of its own -- it rides whatever the restricted verb
// returns -- so a header is the only place its new deadline can travel.
//
// The value is RFC 3339 in UTC, never epoch seconds. An integer carries no
// unit, so a client that reads milliseconds where the hub wrote seconds is
// wrong by a factor of 1000 and nothing reports the mistake. RFC 3339 carries
// its own unit and its own zone, JavaScript's Date parses it directly, and Go
// parses it with time.Parse. The name is owned by contracts/headers.json.
const ElevationExpiresAtHeader = contracts.ElevationExpiresAtHeader

// formatElevationDeadline renders one deadline for the header.
//
// UTC, so the value never depends on the hub process's local zone, and
// RFC3339Nano so it keeps the precision the stored row holds rather than
// truncating a deadline the hub enforces to the second.
func formatElevationDeadline(until time.Time) string {
	return until.UTC().Format(time.RFC3339Nano)
}

// elevationSlideKey keys the request-scoped holder below. An unexported empty
// struct type, so nothing outside this package can plant the value the
// interceptor reports.
type elevationSlideKey struct{}

// elevationSlide is where a slide records the deadline it produced, so the
// response of the request that caused that slide can report it.
//
// A HOLDER IN THE CONTEXT rather than a return value, because the two halves
// sit at opposite ends of one request. slideElevation is a free function that
// seven handlers reach through three helpers, and the HANDLER is what owns the
// connect.Response; threading a deadline back through each of them would make
// "report the new window" a rule every new restricted verb has to remember,
// which is the failure the writeUnderElevation and commitUnderElevation
// helpers exist to prevent. Here the interceptor installs the holder, the
// slide fills it, and the interceptor writes the header. A verb that slides
// reports by construction, a verb that does not slide reports nothing, and no
// call site states either.
//
// The mutex is necessary. A handler may slide from a goroutine it started, and
// the interceptor reads the holder on the handler's own goroutine.
type elevationSlide struct {
	mu    sync.Mutex
	until time.Time
}

// record stores the deadline the store now holds. A second slide in one
// request overwrites the first, which is correct: both read the same row, and
// the row only moves forward.
func (s *elevationSlide) record(until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.until = until
}

// deadline reports the recorded deadline, and whether this request slid at all.
// The zero instant means "no slide", because no elevation ever carries one.
func (s *elevationSlide) deadline() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.until, !s.until.IsZero()
}

// withElevationSlide installs the holder on a request context.
//
// NewElevationSlideInterceptor is the only caller, so a request that runs
// outside a Connect chain records nothing. That is how the four CLI consent
// routes opt out: each renders a whole HTML document to a top-level
// navigation, and a browser navigation has no client waiting to read a header
// off it.
func withElevationSlide(ctx context.Context) (context.Context, *elevationSlide) {
	slide := &elevationSlide{}
	return context.WithValue(ctx, elevationSlideKey{}, slide), slide
}

// recordElevationSlide reports the deadline a slide just wrote, when the
// request carries a holder. A request that carries none records nothing, which
// is a no-op rather than a failure.
func recordElevationSlide(ctx context.Context, until time.Time) {
	if slide, ok := ctx.Value(elevationSlideKey{}).(*elevationSlide); ok {
		slide.record(until)
	}
}

// NewElevationSlideInterceptor reports the elevation deadline on the response
// to each request that slid the window.
//
// It exists because the slide is SILENT everywhere else. The hub extends the
// window on every restricted action and deliberately emits no user_info event
// for it, since a cache still holding the shorter deadline fails closed -- so
// a client that adopted a deadline of 14:00 and then renamed a passkey at
// 13:55 keeps showing 14:00 while the hub holds 15:55. That is up to a whole
// auth.ElevationWindow of error, on the one screen the operating documentation
// points a user at when they step away from a shared machine.
//
// One round trip and no polling: the deadline rides the response the action
// already produces. A client that ignores the header keeps exactly the
// behaviour it had. It reaches the CALLER THAT ACTED and no other tab, which
// is accepted -- the elevation belongs to the session row, which every tab of
// the browser shares, and a tab that did not act still re-reads the account
// when its own copy lapses.
//
// UNARY only. No restricted verb is a stream, and a stream's response header
// goes out with its first message -- long before a handler that slides
// returns -- so there would be nothing left to write it on.
// connect.UnaryInterceptorFunc passes both streaming halves through untouched.
func NewElevationSlideInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx, slide := withElevationSlide(ctx)
			resp, err := next(ctx, req)
			until, slid := slide.deadline()
			if !slid {
				return resp, err
			}
			if err != nil {
				return resp, reportDeadlineOnError(err, until)
			}
			if resp != nil {
				resp.Header().Set(ElevationExpiresAtHeader, formatElevationDeadline(until))
			}
			return resp, nil
		}
	}
}

// reportDeadlineOnError carries the deadline on a failure that FOLLOWED a
// slide.
//
// Every helper slides after its write commits, so a handler that fails later
// still leaves a longer window behind, and the client must learn about it.
// connect-go merges a connect.Error's metadata into the response header, which
// is what makes an error path able to carry this at all.
//
// It touches a *connect.Error alone, and a plain error passes through
// untouched. Wrapping one would decide its status code here, and
// connect.CodeOf answers Unknown for a context.Canceled that connect-go itself
// reports as Canceled. The auth interceptor's applyToError refuses the same
// trade for the same reason.
func reportDeadlineOnError(err error, until time.Time) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		connectErr.Meta().Set(ElevationExpiresAtHeader, formatElevationDeadline(until))
	}
	return err
}

// errElevationRequired is the SENTINEL every elevation refusal wraps, so
// errors.Is answers for all of them however each one is worded. There is no
// rate-limit budget keyed on it, because being un-elevated is not a failed
// attempt.
var errElevationRequired = errors.New("this action needs a recent sign-in")

// elevationRequiredMessage is the remedy every caller but one states.
const elevationRequiredMessage = "verify your identity and try again"

// verifyElevationPassword checks the password presented to ElevateSession.
// Wrong password returns ErrInvalidCurrentPassword so the rate-limit
// interceptor can count the failure.
func verifyElevationPassword(user *store.User, currentPassword string) error {
	if currentPassword == "" {
		return credentialRejectedError(fmt.Errorf("%w", auth.ErrInvalidCurrentPassword))
	}
	match, err := password.Verify(user.PasswordHash, currentPassword)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("verify password: %w", err))
	}
	if !match {
		return credentialRejectedError(fmt.Errorf("%w", auth.ErrInvalidCurrentPassword))
	}
	return nil
}

// requireElevation refuses a sensitive action on a session that did not
// prove a factor inside the window.
//
// This is the PLAIN gate, and every sensitive action outside passkey
// management uses it. It has no first-credential branch, because it needs
// none: an account that can sign in holds at least one of a password, a
// passkey, or an OAuth link, and each of the three elevates.
// stepUpMutationAuth carries that extra branch for the one case this
// cannot serve -- attaching the FIRST such credential to an account that
// holds none of them.
//
// Adding the branch here would be worse than redundant. An account with two
// OAuth links and no password would count as having nothing to elevate with,
// and could then detach one of them WITHOUT elevating, although its identity
// provider could prove the factor.
//
// The two refusals are DIFFERENT, and the difference is the marker rather
// than the wording. A session that did not prove a factor gets
// elevationRequiredError, which carries ElevationRequiredHeader and means
// "prove one and retry". A credential that can never carry an elevation --
// a delegation bearer, which a worker mints for an agent with nobody at a
// keyboard -- gets the plain refusal from requireElevatableCredential, with
// NO marker, because a step-up prompt cannot help it: it would ask for a
// factor and then refuse the retry for the same reason.
func requireElevation(userInfo *auth.UserInfo, now time.Time) error {
	// Solo mode has no ceremony to prove a factor with, so the rule has
	// nothing to decide. Its synthetic user carries no credential row, so
	// every test below answers "cannot elevate", and the refusal it produces
	// -- "sign in from a browser" -- describes a sign-in that does not exist
	// there. That refusal is permanent, and it covers the whole
	// hub-administration surface, which solo mode is meant to serve: solo mode
	// withdraws only the HiddenInSolo keys from it.
	if userInfo.SoloAuthenticated() {
		return nil
	}
	if userInfo.Elevated(now) {
		return nil
	}
	// Not elevated. Either the credential could be and is not, or it never
	// can be; only the second has no remedy the caller can act on here.
	if err := requireElevatableCredential(userInfo); err != nil {
		return err
	}
	return elevationRequiredError()
}

// Creating DURABLE NEW AUTHORITY is one class, and these two functions are
// the whole of its rule.
//
// Four verbs on the admin surface create it: IssueAPIToken mints a credential
// that outlives the session by months, CreateUser mints an account (optionally
// an administrator, with a password the caller chooses), SetUserAdmin grants
// administration itself, and ResetPassword sets any account's password without
// the old one. Each gives somebody a new way to sign in that does not depend
// on the credential used to create it.
//
// Only IssueAPIToken required elevation, and requiring it on one of four
// recorded a security property the hub did not have: a stolen admin bearer
// that could not renew itself past the one-year ceiling could instead
// CreateUser a fresh administrator with a chosen password, sign in through a
// browser, elevate with that password, and mint anything. The ceiling on the
// bearer's own descendants gave no protection while that path stayed open.
//
// requireElevatedSessionForDurableAuthority is the strict answer, for the
// three verbs that need no bearer path. requireElevatedActor is the
// one exception, and it carries its own justification.

// requireElevatedSessionForDurableAuthority demands an elevated SESSION, and
// refuses a bearer outright.
//
// The hub mints a bearer once, and it lives for months, so "was somebody at
// the keyboard recently" has no answer for it -- and what these three verbs
// create is a new way into an account that the bearer itself did not have. The
// refusal carries no ElevationRequiredHeader, for the reason
// requireElevatableSession states: a prompt would ask for a factor and then
// refuse the retry for the same reason.
//
// It costs the headless path deliberately. `leapmux control admin user
// create` and `... reset-password` now need a browser session, which is the
// point: creating an administrator is not routine automation. The offline
// `leapmux recover` verbs remain for an operator with no browser at all.
// It returns the acting credential, so the caller can pass it to
// commitUnderElevation -- the gate alone is half the rule.
func requireElevatedSessionForDurableAuthority(ctx context.Context, now time.Time) (*auth.UserInfo, error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	// Solo mode first, for the reason requireElevation states. The session
	// test below would refuse the synthetic user before the code could reach
	// that reason, because solo mode holds no session row.
	if userInfo.SoloAuthenticated() {
		return userInfo, nil
	}
	if _, err := requireElevatableSession(userInfo); err != nil {
		return nil, err
	}
	return userInfo, requireElevation(userInfo, now)
}

// commitUnderElevation runs an irreversible write on behalf of an already
// admitted actor: it re-reads that actor's authority immediately before the
// write, and slides the window after it.
//
// The GATE is only the first third of the rule, and each handler applied the
// other two thirds by hand and unevenly. Of the four durable-authority verbs,
// one re-read the acting authority and one slid the window; the other three
// did neither -- so an administrator whose session another administrator just
// revoked could still mint a fresh administrator inside the auth cache's TTL,
// and an administrator who spent two hours creating accounts met the prompt
// again although every minute of it exercised the guarded surface.
//
// A helper rather than three copies, for the reason writeUnderElevation is
// one: the next verb in this class cannot get half of it. The gate stays at
// the top of each handler, where it can refuse before any argument work; this
// wraps the tail, where the write is.
//
// A read failure REFUSES, and slideElevation is best-effort. See each for the
// direction it fails in.
func commitUnderElevation(ctx context.Context, st store.Store, actor *auth.UserInfo, now func() time.Time, write func() error) error {
	if err := refuseIfActingAuthorityMoved(ctx, st, actor, now()); err != nil {
		return err
	}
	if err := write(); err != nil {
		return err
	}
	slideElevation(ctx, st, actor, now())
	return nil
}

// requireElevatedActor demands a recently proven factor from the acting
// credential. It returns that credential, so the caller can slide the window
// it just used.
//
// TWO classes of verb take this gate, and a caller reaches both from the
// ADMIN surface:
//
//   - The mint that creates a credential outliving the session that asked for
//     it. The three /oauth/* consent legs already demand an elevated
//     session, on the reasoning requireElevatedSession states -- minting a
//     command-line credential is the most consequential thing a session can
//     do. The same mint through AdminUserService.IssueAPIToken asked for
//     nothing, so a stolen administrator cookie obtained a year-long,
//     refreshable, admin-scoped bearer with no factor at all.
//   - A write to the hub's own configuration. Those keys carry the hub's
//     security controls -- sign-up, captcha, the rate limits, SMTP, and the
//     public URL the passkey relying party derives from -- so a stolen
//     administrator cookie that could turn them off gains more than any
//     single account mutation the window already guards.
//
// A COMMAND-LINE CREDENTIAL takes the same rule, and the hub used to exempt
// it. It held no row to stamp, so "was somebody at the keyboard
// recently" had no answer for it and this gate admitted it unconditionally --
// which made possession of the credential file the whole of the check for
// every verb above. A stolen file rewrote the hub's security settings and
// minted itself fresh credentials.
//
// It has a row now (api_tokens.elevation_proven_at / _expires_at) and it
// proves its factor where a person can answer: the browser, through
// /oauth/step-up. The refusal carries
// ElevationRequiredHeader, so the CLI knows to run that leg and retry rather
// than reporting a refusal it cannot act on -- see EnsureElevated in
// internal/cli/control.
//
// What CONTAINS the bearer path is still the mint. A credential a bearer mints
// does not rotate and cannot outlive its minter (mintAuthority.clamp), so
// each generation is strictly shorter than the last and the chain ends
// at the browser consent that started it. Without that clamp a bearer could
// renew itself for ever, with a fresh created_at each time, past the one-year
// ceiling the whole design rests on.
//
// What this gate does NOT narrow is the credential's REACH. That is a separate
// axis, and it is the scope rung one step earlier: the admin family
// (admin:read, admin:users, admin:settings, admin:workers, admin:apps) is
// five scopes rather than one, so a credential minted to administer users no
// longer reaches the hub's security settings. See enforceScope and
// procedure_scopes.go.
//
// The two multiply and neither replaces the other: a scope says WHICH verbs a
// credential reaches, and this window says whether somebody was recently at a
// keyboard. What remains unrecorded is the AUDIT TRAIL -- which writes a person
// verified -- and https://github.com/leapmux/leapmux/issues/418 tracks it.
func requireElevatedActor(ctx context.Context, now time.Time) (*auth.UserInfo, error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	return userInfo, assertElevatedActor(userInfo, now)
}

// assertElevatedActor is the rule itself, over the acting credential alone.
//
// Split out because the MINT enforces it too. Every surface that mints a
// command-line credential enforced it as the first line of its own handler, and
// nothing made it so. The classification tripwires (userProcedureElevation and
// adminProcedureElevation) record the DECISION for each procedure, and a
// record cannot observe a handler; the consent legs are mux routes rather than
// Connect procedures, so no tripwire reaches them at all. So the omission this
// rule exists to prevent was possible at exactly the place it mattered, and it
// already happened once. mintAPIToken calls this, which makes the gate a
// property of minting rather than of what each author remembered to type.
//
// It is deliberately cheap and pure: no store read, no context. The
// authoritative refusal still happens at the handler, early, where it can
// redirect a browser or write a 403; this is the last check, and nothing can
// skip it.
func assertElevatedActor(userInfo *auth.UserInfo, now time.Time) error {
	if userInfo == nil {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}
	return requireElevation(userInfo, now)
}

// writeUnderElevation admits a write to the hub's own CONFIGURATION, runs it,
// and records that the write used the window.
//
// EVERY write handler on the hub-configuration surface takes it, and READS
// take none. A hub setting is deployment-wide, and several of these keys are
// the hub's own security controls: sign-up, captcha, the rate limits, SMTP,
// and the public_url the passkey relying party derives from. An identity
// PROVIDER row is the same class and carries more: it installs a sign-in
// route for the whole hub, and one with trust_email set links an incoming
// identity to any account whose verified address it presents. A stolen
// administrator cookie that could add one gains more than any single account
// mutation the elevation window already guards.
//
// requireElevatedActor, not requireElevation: an admin-scoped bearer holds
// no session row to stamp and can never elevate, and
// `leapmux control admin settings ...` is the documented headless path.
// The hub grants that scope only at a browser consent that itself required an
// elevated session, so somebody proved the factor once for the credential.
// Refusing it here would break the CLI outright rather than ask it for
// anything.
//
// It also SLIDES the window the write used, which is why every handler goes
// through this rather than calling the gate itself: the hub's standing rule
// is that a sensitive action slides the window that admitted it, and a
// protected verb that forgot the slide would be a verb the window does not
// count as use. One helper means the next write verb cannot get half of it.
//
// A FREE FUNCTION, not a method, for the reason slideElevation is one: TWO
// services write the hub's configuration. AdminSettingsService held the only
// copy, so AdminIdPService -- which adds, removes and disables identity
// providers -- shipped with no gate at all while the sign-up toggle beside it
// had one.
//
// It runs AFTER each handler's argument validation, which is where the hub
// puts an argument check that belongs to the caller's own typing (see
// RequestEmailChange). An unknown key is the caller's mistake, and reporting
// it first spares a verification prompt answered for nothing.
func writeUnderElevation(ctx context.Context, st store.Store, now func() time.Time, write func() error) error {
	actor, err := requireElevatedActor(ctx, now())
	if err != nil {
		return err
	}
	if err := write(); err != nil {
		return err
	}
	slideElevation(ctx, st, actor, now())
	return nil
}

// refuseIfActingAuthorityMoved re-reads the acting credential's authority
// immediately before an irreversible write, and refuses when it went away.
//
// The gate that admitted this request read a CACHED UserInfo -- the auth cache
// holds an entry for its TTL, and a revoke raised out of band (a store writer
// beyond this process) reaches it only on the watcher's next sweep -- so "elevated" can be
// true of a session an administrator already took away. Every mutation the
// elevation window guards moves a credential or a recovery identity, so a
// commit on authority that no longer exists is exactly the outcome the window
// exists to prevent.
//
// TWO questions, because a revoke can arrive by either route, and neither
// answers the other:
//
//   - The account's credential EPOCH. Every path that means "this account's
//     credentials are no longer trusted" moves it, whether or not the acting
//     row survives.
//   - The acting SESSION's row. An administrator taking away one session must
//     not sign the user's other sessions out, so it leaves the epoch where it
//     is. Absence alone cannot separate that from the owner's own sign-out in
//     another tab, which must be TOLERATED -- rolling back a change the user
//     legitimately started was a real regression once already -- so the
//     revocation event kind is what separates them.
//
// This is NOT atomic with the write that follows it, and the passkey-management
// mutations do better: they re-derive the admission inside the user-auth
// transaction, under the lock. This removes the CACHE window, which is the
// wide part -- seconds of staleness become one round trip.
// Closing the rest means moving each caller's write into that transaction, and
// RequestEmailChange cannot take one as it stands, because its verification
// branch sends an SMTP message that must not hold SQLite's single writer lock.
//
// A read failure REFUSES. The question it could not answer is "did an
// administrator take this away", and committing on an unanswered version of it
// is the wrong direction to fail in.
//
// A BEARER actor carries no session, and this rule admits it on the epoch
// alone: it has no row to read, and requireElevatedActor states why a bearer
// reaches these surfaces at all.
func refuseIfActingAuthorityMoved(ctx context.Context, st store.Store, actor *auth.UserInfo, now time.Time) error {
	return refuseIfActingAuthorityMovedFrom(ctx, st, nil, actor, now)
}

// refuseIfActingAuthorityMovedFrom is the same rule when the caller ALREADY
// holds the acting account's row.
//
// UnlinkOAuthProvider does: it re-reads the locked user for its own rule, and
// the actor is that same account, so letting the check read it again made the
// transaction read one row twice while holding SQLite's single writer lock.
// A nil locked row means "read it yourself", which is what the callers with
// no row of their own pass.
func refuseIfActingAuthorityMovedFrom(ctx context.Context, st store.Store, locked *store.User, actor *auth.UserInfo, now time.Time) error {
	if actor == nil {
		return nil
	}
	if locked == nil {
		var err error
		locked, err = st.Users().GetByID(ctx, actor.ID.String())
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("re-read the acting account: %w", err))
		}
	}
	if err := recheckCredentialEpochUnderLock(locked, actor); err != nil {
		return err
	}
	sessionID := actor.Credential.SessionID()
	if sessionID == "" {
		return nil
	}
	if _, err := st.Sessions().GetByID(ctx, sessionID, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return refuseIfSessionWasRevoked(ctx, st, sessionID)
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("re-read the acting session: %w", err))
	}
	return nil
}

// slideElevation extends the acting session's elevation window after a
// sensitive action succeeded, and records the deadline the store now holds so
// the response can report it. Best-effort: the store clamps the new deadline
// to the absolute cap, and a failure only means the hub asks the user to
// verify again sooner. It must never turn a committed mutation into an error.
//
// A first-credential admission carries no elevation, so the statement's own
// "elevation_expires_at IS NOT NULL" guard makes this a no-op there rather than
// granting one -- a mutation must not be able to elevate a session that
// never proved a factor.
//
// A FREE FUNCTION, not a UserService method, because THREE surfaces require
// the window and all three must extend it. It was a method, so only
// UserService procedures slid: the three /oauth/* consent legs and
// AdminUserService.IssueAPIToken enforced the window and left it where it
// was. The most consequential action the gate protects was the one action
// that did not count as use, so a user who elevated at 11:58 and consented at
// 11:59 met the prompt again at 12:01. "Each sensitive action slides that
// window forward" is the rule the design and operating/security.md both
// state; this is what makes it true of every surface rather than of one.
//
// now is a parameter so each surface passes its own clock seam, exactly as
// grantSessionElevation takes one.
func slideElevation(ctx context.Context, st store.Store, userInfo *auth.UserInfo, now time.Time) {
	if userInfo == nil {
		return
	}
	// WHICHEVER row carries the window. A command-line credential elevates
	// exactly as a session does, so it must slide exactly as a session does:
	// a CLI that runs one protected command every minute would otherwise
	// return to the browser every two hours, on the rule that says an
	// uninterrupted work session asks for one factor and not one per action.
	sessionID, apiTokenID, ok := userInfo.Credential.ElevatableRow()
	if !ok {
		return
	}
	var n int64
	var err error
	switch {
	case sessionID != "":
		n, err = st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sessionID,
			UserID:         userInfo.ID,
			WindowDeadline: now.Add(auth.ElevationWindow),
		}, now)
	default:
		n, err = st.APITokens().SlideElevation(ctx, store.SlideAPITokenElevationParams{
			TokenID:        apiTokenID,
			UserID:         userInfo.ID,
			WindowDeadline: now.Add(auth.ElevationWindow),
		}, now)
	}
	if err != nil {
		slog.WarnContext(ctx, "could not slide the elevation window", "err", err)
		return
	}
	// Zero rows means the statement's own guards refused: the row carries no
	// live elevation (a first-credential admission, which must not gain one
	// here), or it already holds a deadline no earlier than the one this
	// request asks for. Neither moved anything, so there is nothing new to
	// report and the client keeps the deadline it has.
	if n == 0 {
		return
	}
	if until, ok := storedElevationDeadline(ctx, st, sessionID, apiTokenID, now); ok {
		recordElevationSlide(ctx, until)
	}
}

// storedElevationDeadline re-reads the row a slide just wrote, so the response
// reports the deadline the STORE holds rather than the one the slide asked for.
//
// The two DIFFER, and the difference is the whole reason this read exists. The
// slide statement clamps the new deadline against the stored
// elevation_proven_at plus store.ElevationMaxTotal, and it does that in SQL
// because Go never reads that anchor. So a request in the last stretch of an
// eight-hour elevation asks for now + auth.ElevationWindow and gets the
// ceiling instead. Reporting what the slide ASKED for there would promise the
// user two more hours the hub will not honour -- the same defect as the early
// deadline this report exists to correct, pointing the other way, and the
// direction that fails open.
//
// Computing the clamp in Go instead would put a second copy of the ceiling
// rule beside the statement that enforces it, and auth.Elevation deliberately
// carries no anchor to compute it from.
//
// The read is NOT in the slide's transaction, and it does not need to be. The
// stored deadline only moves forward, so a concurrent slide can leave this one
// reporting a value later than the one it wrote -- which is still a deadline
// the hub holds, never one it does not.
//
// One indexed row read for each restricted action. Nothing on a hot path
// reaches here: the verbs that slide are password changes, passkey management,
// email changes, hub-settings writes, the OAuth provider writes and the admin
// mutations.
//
// It answers through auth.NewElevation and Deadline, the SAME pair the
// admission reads, so the deadline a client renders cannot disagree with the
// one the hub enforces. A read failure reports nothing, which leaves the
// client's copy where it was: this report can improve that copy and must never
// make it worse.
func storedElevationDeadline(ctx context.Context, st store.Store, sessionID, apiTokenID string, now time.Time) (time.Time, bool) {
	var provenAt, expiresAt *time.Time
	if sessionID != "" {
		row, err := st.Sessions().GetByID(ctx, sessionID, now)
		if err != nil {
			slog.WarnContext(ctx, "could not re-read the slid elevation window", "err", err)
			return time.Time{}, false
		}
		provenAt, expiresAt = row.ElevationProvenAt, row.ElevationExpiresAt
	} else {
		row, err := st.APITokens().GetByID(ctx, apiTokenID)
		if err != nil {
			slog.WarnContext(ctx, "could not re-read the slid elevation window", "err", err)
			return time.Time{}, false
		}
		provenAt, expiresAt = row.ElevationProvenAt, row.ElevationExpiresAt
	}
	return auth.NewElevation(provenAt, expiresAt).Deadline(now)
}

// rejectSoloElevation refuses the elevation surface for a caller the SOLO RUNG
// authenticated: it carries the synthetic solo user, which holds no session
// row to stamp and needs none, because requireElevation exempts it outright.
//
// It reads the CALLER and not the hub's mode, and the difference is the whole
// point. A solo hub that holds a password authenticates a TCP caller with an
// ordinary session, which has a row, must present a factor like any other, and
// therefore must be able to prove one -- otherwise the person who set that
// password could never write a hub setting from the browser they set it in.
//
// Keying on cfg.SoloMode instead refused exactly that caller, and the refusal
// was invisible: the frontend prompts for a step-up, the hub answers "not
// available in solo mode", and the prompt has nowhere to go.
func rejectSoloElevation(userInfo *auth.UserInfo) error {
	return rejectSolo(userInfo.SoloAuthenticated(), "session elevation")
}

// accountElevatesOnlyThroughAProvider reports whether the account holds
// neither a password nor a passkey, so its identity provider is the only
// thing that can confirm the person is still there.
//
// It is the ONE place that decides that shape, because two rules must agree
// on it: the first-credential branch of stepUpMutationAuth reads it to
// know that an account has nothing to elevate WITH, and
// IdPHandler.providerMayElevateAccount calls it, so the OAuth
// re-authentication leg reaches the same answer rather than a second copy of
// it.
//
// The OAuth path is deliberately narrow. Widening it to an account that
// holds a password would make "the browser can still reach the provider
// session" equivalent to knowing the password, which is a different and
// weaker security claim than the one the account was set up with.
//
// It reads the passkey COUNT and never whether this hub can currently run a
// ceremony with it. An account whose passkey the hub cannot run still HOLDS
// one, so it is not an account with nothing to attach a first credential to
// -- admitting it here would give a recently signed-in session the
// first-credential rule for an account that already has a durable factor.
func accountElevatesOnlyThroughAProvider(ctx context.Context, st store.Store, user *store.User) (bool, error) {
	if user.FirstCredentialExempt {
		return false, nil
	}
	count, err := st.PasskeyCredentials().CountByUser(ctx, user.ID)
	if err != nil {
		return false, err
	}
	return accountShapeElevatesOnlyThroughAProvider(user.FirstCredentialExempt, count), nil
}

// accountShapeElevatesOnlyThroughAProvider is the rule itself, over the two
// facts it reads.
//
// Split out because GetCurrentUser holds both facts already -- it counts
// passkeys for the number it reports -- and spelled the rule inline rather
// than paying a second COUNT for the answer. That inline copy is the drift
// this function exists to remove: what counts as "a factor" is now one
// expression, so a change to it cannot land in the leg that ENFORCES the rule
// and miss the screen that OFFERS it.
//
// The wrapper above keeps its own early return, so an account with a password
// still runs no query at all.
func accountShapeElevatesOnlyThroughAProvider(passwordSet bool, passkeyCount int64) bool {
	return !passwordSet && passkeyCount == 0
}

// requireElevatableCredential refuses a credential that can carry no
// elevation at all.
//
// TWO kinds can: a session cookie, and a command-line credential, which
// proves its factor in a browser through the /oauth/step-up
// leg. Each has a row to stamp and a person who can be prompted. A
// DELEGATION bearer has neither -- a worker mints it for an agent that reads
// untrusted input -- and neither does solo mode, which authenticates a
// synthetic user against no row.
//
// The refusal deliberately carries NO ElevationRequiredHeader. The marker
// means "prove a factor and retry", and there is nothing this caller can
// prove: a step-up prompt would collect a factor and then refuse the retry
// for the same reason. See requireElevation, which chooses between the two
// refusals.
//
// Nil-safe, like every sibling in this family. requireElevation reaches it
// through UserInfo.Elevated, which is documented nil-safe, so a nil that
// slipped past would turn a refusal into a handler panic here.
func requireElevatableCredential(userInfo *auth.UserInfo) error {
	if userInfo != nil {
		if _, _, ok := userInfo.Credential.ElevatableRow(); ok {
			return nil
		}
	}
	return errCredentialNotElevatable()
}

// errCredentialNotElevatable is the refusal both elevatable-credential rules
// return. One constructor, because the two rules differ in WHICH credentials
// they admit and not in what they tell a caller that holds none of them: the
// remedy is the same sentence, and two copies of a security message drift the
// moment somebody rewords one.
func errCredentialNotElevatable() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("this credential cannot verify your identity; sign in from a browser to perform this action"))
}

// requireElevatableSession returns the acting SESSION id, refusing every
// other credential -- a command-line credential included.
//
// The narrower rule, and the two callers need it. The elevation RPCs collect
// a factor and stamp a row: a bearer that could call them would elevate
// ITSELF, which is exactly the property the browser leg exists to prevent
// (the factor must be proven somewhere the credential file cannot reach).
// requireElevatedSessionForDurableAuthority needs it for its own reason,
// which it states.
//
// Nil-safe, like requireElevatableCredential above.
func requireElevatableSession(userInfo *auth.UserInfo) (string, error) {
	var sessionID string
	if userInfo != nil {
		sessionID = userInfo.Credential.SessionID()
	}
	if sessionID == "" {
		return "", errCredentialNotElevatable()
	}
	return sessionID, nil
}

// errElevationSessionEnded reports a grant that found no live session to
// stamp. Every factor path reports it, and each maps it to its own transport:
// an RPC answers Unauthenticated, the OAuth leg answers 401.
var errElevationSessionEnded = errors.New("your session ended; sign in again")

// grantSessionElevation stamps a fresh window on one session and reports the
// new deadline. It is the ONE place that grants an elevation, because three
// factor paths grant one -- a password, a passkey, and the OAuth
// re-authentication leg -- and the third lives in an HTTP handler rather
// than an RPC. A change to the window, to the zero-row refusal, or to the
// cache invalidation must reach all three by construction.
//
// The write requires a live session, so a zero row count means the session
// expired or a revoke ended it between the factor check and here. This
// function reports that as a refusal rather than a silent success: the
// caller would otherwise read that it may proceed while nothing was recorded.
//
// now is a parameter so each caller passes its own clock seam, rather than
// this function inventing a fourth notion of the current instant.
func grantSessionElevation(
	ctx context.Context,
	st store.Store,
	lifecycle *auth.CredentialLifecycleEffects,
	sessionID string,
	userID userid.UserID,
	now time.Time,
) (time.Time, error) {
	until := now.Add(auth.ElevationWindow)
	n, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
		SessionID:          sessionID,
		UserID:             userID,
		ElevationProvenAt:  now,
		ElevationExpiresAt: until,
	}, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("record session elevation: %w", err)
	}
	if n == 0 {
		return time.Time{}, errElevationSessionEnded
	}
	// The cached UserInfo still carries the OLD deadline, and this process
	// is the one serving the next request. Drop it through the lane whose
	// contract is exactly "re-read the user without logging them out"; the
	// durable event the store emitted covers the watcher's replay.
	lifecycle.UserInfoInvalidated(userID.String())
	return until, nil
}

// grantElevation is the RPC paths' wrapper: it supplies the service's clock
// and maps the shared refusals to Connect codes. Both factor paths report the
// SAME field, so a client has one success path; the two response messages
// differ only because a proto response type may serve one RPC.
func (s *UserService) grantElevation(ctx context.Context, userInfo *auth.UserInfo, sessionID string) (time.Time, error) {
	until, err := grantSessionElevation(ctx, s.store, s.lifecycle, sessionID, userInfo.ID, s.now())
	switch {
	case errors.Is(err, errElevationSessionEnded):
		return time.Time{}, connect.NewError(connect.CodeUnauthenticated, err)
	case err != nil:
		return time.Time{}, connect.NewError(connect.CodeInternal, err)
	}
	return until, nil
}

// elevationCaller runs the three checks every elevation RPC opens with, in
// the one order that is correct, and returns the acting user and session.
//
// A helper rather than four copies, because the ORDER is the rule: it resolves
// the caller, refuses one the solo rung authenticated (which holds no session
// row to stamp), then requires a credential that can carry an elevation. A
// fifth handler that forgets the solo check would otherwise stamp an elevation
// against a user with no row.
//
// The solo test now needs the CALLER, so it runs second rather than first; see
// rejectSoloElevation for why the hub's mode is the wrong question.
func (s *UserService) elevationCaller(ctx context.Context) (*auth.UserInfo, string, error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := rejectSoloElevation(userInfo); err != nil {
		return nil, "", err
	}
	sessionID, err := requireElevatableSession(userInfo)
	if err != nil {
		return nil, "", err
	}
	return userInfo, sessionID, nil
}

// ElevateSession proves a password.
func (s *UserService) ElevateSession(ctx context.Context, req *connect.Request[leapmuxv1.ElevateSessionRequest]) (*connect.Response[leapmuxv1.ElevateSessionResponse], error) {
	userInfo, sessionID, err := s.elevationCaller(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("query user: %w", err))
	}
	if !user.FirstCredentialExempt {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("this account has no password; verify with a passkey or your identity provider instead"))
	}
	// Argon2 runs here, OUTSIDE any transaction: the write below takes the
	// user-auth lock, which on SQLite is the single database writer lock
	// (see auth.Login's comment on the same trade).
	if err := verifyElevationPassword(user, req.Msg.GetCurrentPassword()); err != nil {
		return nil, err
	}
	until, err := s.grantElevation(ctx, userInfo, sessionID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.ElevateSessionResponse{
		ElevationExpiresAt: timestamppb.New(until),
	}), nil
}

// BeginPasskeyElevation starts the step-up assertion.
func (s *UserService) BeginPasskeyElevation(ctx context.Context, req *connect.Request[leapmuxv1.BeginPasskeyElevationRequest]) (*connect.Response[leapmuxv1.BeginPasskeyElevationResponse], error) {
	// The PASSKEY factor is refused by mode, unlike the elevation surface
	// around it. rejectSoloElevation reads the caller, because a solo hub with
	// a password has real sessions to elevate -- but no account there can ever
	// hold a passkey, and GetSystemInfo reports passkey_enabled false for
	// every origin. Without this a signed-in solo caller reached the WebAuthn
	// engine and was answered "no passkeys registered", which reads as a
	// missing credential rather than a feature the hub does not offer.
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, _, err := s.elevationCaller(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	sessionID, optionsJSON, err := wa.BeginElevation(ctx, user.ID, originFromRequest(req))
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	return connect.NewResponse(&leapmuxv1.BeginPasskeyElevationResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
	}), nil
}

// FinishPasskeyElevation verifies the step-up assertion and grants the
// window. It reports elevation_expires_at, the same field the password path
// reports, so a client has one success path.
func (s *UserService) FinishPasskeyElevation(ctx context.Context, req *connect.Request[leapmuxv1.FinishPasskeyElevationRequest]) (*connect.Response[leapmuxv1.FinishPasskeyElevationResponse], error) {
	// Refused by mode, for the reason BeginPasskeyElevation states.
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, actingSessionID, err := s.elevationCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("session_id is required"))
	}
	if req.Msg.GetCredentialJson() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("credential_json is required"))
	}
	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	// This verifies the assertion OUTSIDE the elevation write, for the same
	// reason the password path hashes outside it.
	if err := wa.FinishElevation(ctx, user.ID, req.Msg.GetSessionId(), req.Msg.GetCredentialJson()); err != nil {
		return nil, mapElevationAssertionError(ctx, user.ID, err)
	}
	until, err := s.grantElevation(ctx, userInfo, actingSessionID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.FinishPasskeyElevationResponse{
		ElevationExpiresAt: timestamppb.New(until),
	}), nil
}

// mapElevationAssertionError classifies a failed step-up assertion.
//
// This re-labels only a genuine credential failure as
// auth.ErrInvalidElevationAssertion, because that sentinel is what the
// rate-limit interceptor counts. A clone warning is a security event rather
// than a wrong answer, and a store or configuration failure is not the
// user's attempt at all -- neither may spend the user's budget.
//
// Both rejected-credential paths carry CredentialRejectedHeader: the SESSION
// is fine, and a client that signed the user out here would end the session
// the prompt exists to protect.
func mapElevationAssertionError(ctx context.Context, userID string, err error) error {
	switch classifyWebAuthnError(err) {
	case webAuthnErrorClone:
		slog.WarnContext(ctx, "passkey clone warning during elevation", "user_id", userID)
		return credentialRejectedError(err)
	case webAuthnErrorCredential:
		return credentialRejectedError(fmt.Errorf("%w: %s", auth.ErrInvalidElevationAssertion, err.Error()))
	case webAuthnErrorUnavailable:
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// DropElevation ends the elevation now.
//
// It is idempotent: a credential that holds none is already in the state the
// caller asked for, so a zero row count is success rather than NotFound.
//
// It routes on ElevatableRow rather than demanding a SESSION, because the
// three other operations on the window already do: the grant stamps whichever
// row carries it, slideElevation extends whichever row carries it, and
// Elevation.IsCurrent reads whichever row carries it. A session-only drop left
// the one operation that only REDUCES access as the one a command-line
// credential could not perform -- so a user whose laptop credential file was
// exposed could end its authority only by revoking the whole credential.
func (s *UserService) DropElevation(ctx context.Context, _ *connect.Request[leapmuxv1.DropElevationRequest]) (*connect.Response[leapmuxv1.DropElevationResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := rejectSoloElevation(userInfo); err != nil {
		return nil, err
	}
	sessionID, apiTokenID, ok := userInfo.Credential.ElevatableRow()
	if !ok {
		return nil, errCredentialNotElevatable()
	}
	var n int64
	if sessionID != "" {
		n, err = s.store.Sessions().DropElevation(ctx, store.DropSessionElevationParams{
			SessionID: sessionID,
			UserID:    userInfo.ID,
		}, s.now())
	} else {
		n, err = s.store.APITokens().DropElevation(ctx, store.DropAPITokenElevationParams{
			TokenID: apiTokenID,
			UserID:  userInfo.ID,
		}, s.now())
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("drop elevation: %w", err))
	}
	if n > 0 {
		// A cached LONGER deadline fails open, so this invalidation is not
		// an optimisation the way the grant's is -- it is the drop.
		s.lifecycle.UserInfoInvalidated(userInfo.ID.String())
	}
	return connect.NewResponse(&leapmuxv1.DropElevationResponse{}), nil
}

// elevationExpiresAtProto reports the deadline a client should show, or nil when
// the session is not elevated at now. Shared by GetCurrentUser and anything
// else that reports the state, so no client ever renders a lapsed window as
// live.
func elevationExpiresAtProto(userInfo *auth.UserInfo, now time.Time) *timestamppb.Timestamp {
	until, ok := userInfo.ElevationDeadline(now)
	if !ok {
		return nil
	}
	return timestamppb.New(until)
}
