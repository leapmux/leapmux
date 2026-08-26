package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

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
// /auth/oauth/<provider>/reauth instead, because it holds neither.
//
// Elevation is per hub PROCESS in exactly one respect: the in-memory
// UserInfo cache. The state itself is a pair of columns, so another hub
// reads it from the row; but a hub that already cached this session serves
// the old deadline until the grant's user_info event reaches it. Grant and
// drop both emit that event, and a slide deliberately does not -- a stale
// SHORTER deadline fails closed.

// elevationRequiredError refuses a sensitive action on a session that is not
// elevated.
//
// FailedPrecondition, deliberately, NOT Unauthenticated. The frontend's
// global interceptor treats Unauthenticated as "your session ended" and
// signs the user out, so the one refusal whose remedy is "prove a factor and
// try again" would instead throw away the session the user is about to prove
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
// that cannot be got right by reading the signature.
//
// It still WRAPS errElevationRequired, so errors.Is keeps answering for every
// refusal of this kind. Replacing the cause left the sentinel true of five
// refusals and false of the sixth, with nothing but the header to tell them
// apart.
func elevationRequiredErrorSaying(message string) error {
	err := connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("%w: %s", errElevationRequired, message))
	// A machine-readable marker, so a client can tell THIS FailedPrecondition
	// from every other one (an account with no password, a last passkey that
	// needs a replacement) and open a step-up prompt for it alone. Matching
	// the message text instead would break on the first rewording, and the
	// wording is user-facing prose that will be reworded.
	err.Meta().Set(ElevationRequiredHeader, "1")
	return err
}

// ElevationRequiredHeader marks a refusal whose remedy is "prove a factor
// and retry". Exported because the frontend and the E2E suite both key on
// it; the value is always "1" and only its presence is meaningful.
const ElevationRequiredHeader = "Leapmux-Elevation-Required"

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
// passkeyManagementAuth carries that extra branch for the one case this
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
// the old one. Each hands somebody a way back in that does not depend on the
// credential used to create it.
//
// Only IssueAPIToken was gated, and gating one of four recorded a security
// property the hub did not have: a stolen admin bearer that could not renew
// itself past the one-year ceiling could instead CreateUser a fresh
// administrator with a chosen password, sign in through a browser, elevate
// with that password, and mint whatever it liked. The ceiling on the bearer's
// own descendants bought nothing while that door was open.
//
// requireElevatedSessionForDurableAuthority is the strict answer, for the
// three verbs that need no bearer path. requireElevatedActor is the
// one exception, and it carries its own justification.

// requireElevatedSessionForDurableAuthority demands an elevated SESSION, and
// refuses a bearer outright.
//
// A bearer is minted once and lives for months, so "was somebody at the
// keyboard recently" has no answer for it -- and what these three verbs create
// is a new way into an account that the bearer itself did not have. The
// refusal carries no ElevationRequiredHeader, for the reason
// requireElevatableSession states: a prompt would ask for a factor and then
// refuse the retry for the same reason.
//
// It costs the headless path deliberately. `leapmux control admin user
// create` and `... reset-password` now need a browser session, which is the
// point: creating an administrator is not routine automation. The offline
// `leapmux recover` verbs remain for an operator with no browser at all.
func requireElevatedSessionForDurableAuthority(ctx context.Context, now time.Time) error {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return err
	}
	if _, err := requireElevatableSession(userInfo); err != nil {
		return err
	}
	return requireElevation(userInfo, now)
}

// requireElevatedActor demands a recently proven factor from the acting
// credential. It returns that credential, so the caller can slide the window
// it just used.
//
// TWO classes of verb take this gate, and both are reached from the ADMIN
// surface:
//
//   - The mint that creates a credential outliving the session that asked for
//     it. The four /auth/cli/* consent legs already demand an elevated
//     session, on the reasoning requireElevatedSession states -- minting a
//     command-line credential is the most consequential thing a session can
//     do. The same mint through AdminUserService.IssueAPIToken asked for
//     nothing, so a stolen administrator cookie bought a year-long,
//     refreshable, admin-scoped bearer with no factor at all.
//   - A write to the hub's own configuration. Those keys carry the hub's
//     security controls -- sign-up, captcha, the rate limits, SMTP, and the
//     public URL the passkey relying party derives from -- so a stolen
//     administrator cookie that could turn them off buys more than any single
//     account mutation the window already guards.
//
// A COMMAND-LINE CREDENTIAL takes the same rule, and used to be waved
// through. It held no row to stamp, so "was somebody at the keyboard
// recently" had no answer for it and this gate admitted it unconditionally --
// which made possession of the credential file the whole of the check for
// every verb above. A stolen file rewrote the hub's security settings and
// minted itself fresh credentials.
//
// It has a row now (api_tokens.elevation_proven_at / _expires_at) and it
// proves its factor where a person can answer: the browser, through
// /auth/cli/elevate-authorization. The refusal carries
// ElevationRequiredHeader, so the CLI knows to run that leg and retry rather
// than reporting a dead end -- see EnsureElevated in internal/cli/control.
//
// What CONTAINS the bearer arm is still the mint. A credential a bearer mints
// does not rotate and cannot outlive its minter (mintAuthority.clamp), so
// each generation is strictly shorter than the last and the chain terminates
// at the browser consent that started it. Without that clamp a bearer could
// renew itself for ever, with a fresh created_at each time, past the one-year
// ceiling the whole design rests on.
//
// What this gate does NOT narrow is the credential's REACH: one admin scope
// still admits every verb on the admin surface, so a credential minted for
// user administration can also rewrite the hub's security settings. Splitting
// a settings scope out of it, and recording on the audit trail which writes a
// person verified, are tracked in
// https://github.com/leapmux/leapmux/issues/418.
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
// command-line credential gated it as the first line of its own handler, and
// nothing made it so -- the classification tripwire in
// user_procedures_internal_test.go cannot reach an Admin* procedure, and the
// consent legs are mux routes rather than Connect procedures. So the omission
// this rule exists to prevent was possible at exactly the place it mattered,
// and it had already happened once. mintAPIToken calls this, which makes the
// gate a property of minting rather than of what each author remembered to
// type.
//
// It is deliberately cheap and pure: no store read, no context. The
// authoritative refusal still happens at the handler, early, where it can
// bounce a browser or write a 403; this is the backstop that cannot be
// skipped.
func assertElevatedActor(userInfo *auth.UserInfo, now time.Time) error {
	if userInfo == nil {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}
	return requireElevation(userInfo, now)
}

// refuseIfActingAuthorityMoved re-reads the acting credential's authority
// immediately before an irreversible write, and refuses when it went away.
//
// The gate that admitted this request read a CACHED UserInfo -- the auth cache
// holds an entry for its TTL, and a revoke raised on another hub reaches this
// process only on the revocation watcher's next sweep -- so "elevated" can be
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
// transaction, under the lock. What this buys is the removal of the CACHE
// window, which is the wide part -- seconds of staleness become one round trip.
// Closing the rest means moving each caller's write into that transaction, and
// RequestEmailChange cannot take one as it stands, because its verification arm
// sends an SMTP message that must not hold SQLite's single writer lock.
//
// A read failure REFUSES. The question it could not answer is "did an
// administrator take this away", and committing on an unanswered version of it
// is the wrong direction to fail in.
//
// A BEARER actor carries no session and is admitted on the epoch alone: it has
// no row to read, and requireElevatedActor states why a bearer reaches
// these surfaces at all.
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
// sensitive action succeeded. Best-effort: the store clamps the new deadline
// to the absolute cap, and a failure only means the user is asked to verify
// again sooner. It must never turn a committed mutation into an error.
//
// A first-credential admission carries no elevation, so the statement's own
// "elevation_expires_at IS NOT NULL" guard makes this a no-op there rather than
// granting one -- a mutation must not be able to elevate a session that
// never proved a factor.
//
// A FREE FUNCTION, not a UserService method, because THREE surfaces gate on
// the window and all three must extend it. It was a method, so only
// UserService procedures slid: the four /auth/cli/* consent legs and
// AdminUserService.IssueAPIToken enforced the window and left it where it
// was. The most consequential action the gate protects was the one action
// that did not count as use, so a user who elevated at 11:58 and consented at
// 11:59 was prompted again at 12:01. "Each sensitive action slides that
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
	// a CLI that runs one gated command every minute would otherwise be sent
	// back to the browser every two hours, on the rule that says an
	// uninterrupted work session asks for one factor and not one per action.
	sessionID, apiTokenID, ok := userInfo.Credential.ElevatableRow()
	if !ok {
		return
	}
	var err error
	switch {
	case sessionID != "":
		_, err = st.Sessions().SlideElevation(ctx, store.SlideSessionElevationParams{
			SessionID:      sessionID,
			UserID:         userInfo.ID,
			WindowDeadline: now.Add(auth.ElevationWindow),
			MaxTotal:       auth.ElevationMaxTotal,
		}, now)
	default:
		_, err = st.APITokens().SlideElevation(ctx, store.SlideAPITokenElevationParams{
			TokenID:        apiTokenID,
			UserID:         userInfo.ID,
			WindowDeadline: now.Add(auth.ElevationWindow),
			MaxTotal:       auth.ElevationMaxTotal,
		}, now)
	}
	if err != nil {
		slog.WarnContext(ctx, "could not slide the elevation window", "err", err)
	}
}

// rejectSoloElevation refuses the elevation surface in solo mode, which
// authenticates every request as the synthetic solo user and has no session
// row to stamp.
func rejectSoloElevation(solo bool) error {
	return rejectSolo(solo, "session elevation")
}

// accountElevatesOnlyThroughAProvider reports whether the account holds
// neither a password nor a passkey, so its identity provider is the only
// thing that can confirm the person is still there.
//
// It is the ONE place that shape is decided, because two rules must agree
// on it: the first-credential branch of passkeyManagementAuth reads it to
// know that an account has nothing to elevate WITH, and
// OAuthHandler.providerMayElevateAccount calls it, so the OAuth
// re-authentication leg reaches the same answer rather than a second copy of
// it.
//
// The OAuth arm is deliberately narrow. Widening it to an account that
// holds a password would make "the browser can still reach the provider
// session" equivalent to knowing the password, which is a different and
// weaker security claim than the one the account was set up with.
//
// It reads the passkey COUNT and never whether this hub can currently run a
// ceremony with it. An account whose passkey the hub cannot run still HOLDS
// one, so it is not an account with nothing to attach a first credential to
// -- admitting it here would hand a recently signed-in session the
// first-credential rule for an account that already has a durable factor.
func accountElevatesOnlyThroughAProvider(ctx context.Context, st store.Store, user *store.User) (bool, error) {
	if user.PasswordSet {
		return false, nil
	}
	count, err := st.PasskeyCredentials().CountByUser(ctx, user.ID)
	if err != nil {
		return false, err
	}
	return accountShapeElevatesOnlyThroughAProvider(user.PasswordSet, count), nil
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
// proves its factor in a browser through the /auth/cli/elevate-authorization
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
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("this credential cannot verify your identity; sign in from a browser to perform this action"))
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
		return "", connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("this credential cannot verify your identity; sign in from a browser to perform this action"))
	}
	return sessionID, nil
}

// errElevationSessionEnded reports a grant that found no live session to
// stamp. Every factor arm reports it, and each maps it to its own transport:
// an RPC answers Unauthenticated, the OAuth leg answers 401.
var errElevationSessionEnded = errors.New("your session ended; sign in again")

// grantSessionElevation stamps a fresh window on one session and reports the
// new deadline. It is the ONE place an elevation is granted, because three
// factor arms grant one -- a password, a passkey, and the OAuth
// re-authentication leg -- and the third lives in an HTTP handler rather
// than an RPC. A change to the window, to the zero-row refusal, or to the
// cache invalidation must reach all three by construction.
//
// The write is guarded on the session still being live, so a zero row count
// means the session expired or was revoked between the factor check and
// here. That is reported as a refusal rather than a silent success: the
// caller would otherwise be told it may proceed while nothing was recorded.
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
	// durable event the store emitted covers every other hub.
	lifecycle.UserInfoInvalidated(userID.String())
	return until, nil
}

// grantElevation is the RPC arms' wrapper: it supplies the service's clock
// and maps the shared refusals to Connect codes. Both factor arms report the
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
// A helper rather than four copies, because the ORDER is the rule: solo mode
// is refused first (it authenticates every request as the synthetic solo
// user and has no session row to stamp), then the credential is resolved,
// then the credential is required to be one that can carry an elevation. A
// fifth handler that forgets the solo check would otherwise stamp an
// elevation against a user with no row.
func (s *UserService) elevationCaller(ctx context.Context) (*auth.UserInfo, string, error) {
	if err := rejectSoloElevation(s.cfg.SoloMode); err != nil {
		return nil, "", err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
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
	if !user.PasswordSet {
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
	sessionID, optionsJSON, rpID, err := wa.BeginElevation(ctx, user.ID, originFromRequest(req))
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	return connect.NewResponse(&leapmuxv1.BeginPasskeyElevationResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
		RpId:        rpID,
	}), nil
}

// FinishPasskeyElevation verifies the step-up assertion and grants the
// window. It reports elevation_expires_at, the same field the password arm
// reports, so a client has one success path.
func (s *UserService) FinishPasskeyElevation(ctx context.Context, req *connect.Request[leapmuxv1.FinishPasskeyElevationRequest]) (*connect.Response[leapmuxv1.FinishPasskeyElevationResponse], error) {
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
	// The assertion is verified OUTSIDE the elevation write, for the same
	// reason the password arm hashes outside it.
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
// Only a genuine credential failure is re-labelled as
// auth.ErrInvalidElevationAssertion, because that sentinel is what the
// rate-limit interceptor counts. A clone warning is a security event rather
// than a wrong answer, and a store or configuration failure is not the
// user's attempt at all -- neither may spend the user's budget.
//
// Both rejected-credential arms carry CredentialRejectedHeader: the SESSION
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
// It is idempotent: a session that holds none is already in the state the
// caller asked for, so a zero row count is success rather than NotFound.
func (s *UserService) DropElevation(ctx context.Context, _ *connect.Request[leapmuxv1.DropElevationRequest]) (*connect.Response[leapmuxv1.DropElevationResponse], error) {
	userInfo, sessionID, err := s.elevationCaller(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.store.Sessions().DropElevation(ctx, store.DropSessionElevationParams{
		SessionID: sessionID,
		UserID:    userInfo.ID,
	}, s.now())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("drop session elevation: %w", err))
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
// else that reports the state, so a lapsed window is never rendered as live.
func elevationExpiresAtProto(userInfo *auth.UserInfo, now time.Time) *timestamppb.Timestamp {
	until, ok := userInfo.ElevationDeadline(now)
	if !ok {
		return nil
	}
	return timestamppb.New(until)
}
