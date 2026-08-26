package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

// lastPasskeyNeedsPasswordError is the one refusal that protects a user
// from locking themselves out of a passwordless account.
//
// Two sites raise it: DeletePasskey's pre-lock decision and the locked
// re-derivation in commitPasskeyDeactivation. They must stay byte-identical
// -- the pre-lock message is what the user sees on the common path, and the
// locked one is what they see when the state moved under them, so a drift
// between the two reads as two different rules.
func lastPasskeyNeedsPasswordError() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("cannot delete your only passkey; provide new_password or use DeactivatePasskeyAuth"))
}

func rejectSoloPasskeyManagement(solo bool) error {
	return rejectSolo(solo, "passkey management")
}

// stepUpAdmission records HOW passkeyManagementAuth admitted a mutation, so
// the locked re-check can re-derive the same decision from the locked row
// instead of trusting the pre-lock peek.
type stepUpAdmission struct {
	// firstCredential is true when the account had NO password and NO
	// passkey, so no step-up credential existed to present at all.
	//
	// This is the one admission input a concurrent write can invalidate: a
	// registration that commits between the peek and the lock gives the
	// account a credential, so the caller must elevate instead.
	// recheckStepUpUnderLock re-reads the count for exactly this case, and
	// only for it.
	firstCredential bool
}

// passkeyManagementCaller runs the two checks every passkey-management RPC
// opens with, in the one order that is correct, and returns the acting user.
//
// A helper rather than six copies, for the reason elevationCaller gives for
// its own four: the ORDER is the rule. Solo mode is refused FIRST, because it
// authenticates every request as the synthetic solo user and has no
// credential store to act on, so resolving the credential first would answer
// a solo caller with a message about a user that is not the one it means.
// A seventh handler that forgot the refusal would reach a store the mode does
// not serve.
func (s *UserService) passkeyManagementCaller(ctx context.Context) (*auth.UserInfo, error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	return auth.MustGetUser(ctx)
}

// passkeyManagementAuth decides whether a passkey-management mutation (or
// ChangePassword) may proceed.
//
// Every account elevates, through requireElevation: one proven factor admits
// every sensitive action for auth.ElevationWindow, and each success slides
// the window.
//
// An account with no password and no passkey has nothing to elevate WITH, so
// it takes a SECOND rule beside that one -- the first-credential rule: a
// recent sign-in on a durable identity. The two are SIBLINGS, and either
// admits:
//
//   - The first-credential rule must be reachable WITHOUT an elevation, or
//     such an account could never attach its first credential and could
//     therefore never become elevatable. That deadlock is the single most
//     important property of this function.
//   - A live elevation must ALSO admit, and leaving it out was a real
//     defect. The OAuth re-authentication leg grants an elevation to exactly
//     this account shape -- providerMayElevateAccount reads the same
//     predicate this branch does -- so with the first-credential rule as the
//     only arm, that leg could never help the two procedures it exists for.
//     A user proved a factor at their identity provider, came back, and was
//     refused with the same message as before; only signing out and in again
//     worked.
//
// Both branches already separate "a session that never proved itself" from
// "a credential that never can": the first-credential branch does it in
// assertFirstCredentialAuthIsFresh, and requireElevation does it for the
// elevation branch. So a bearer is told to sign in from a browser on either
// path, and never handed a prompt that its retry would fail again.
//
// The count read happens outside the user-auth transaction: it is one
// indexed COUNT, and on SQLite that transaction holds the single writer lock
// (see auth.Login's comment on the same trade). It runs only when the session
// is NOT already elevated, so the ordinary path pays for no query here.
//
// p carries the acting credential and the leg's slack, and BOTH rules
// take that slack from it rather than from a raw duration: p.grace reaches
// each rule only through firstCredentialWindow and elevationInstant, so a
// third rule added here cannot read the credential and forget the slack.
// The locked re-check in runPasskeyManagementTx takes the same p, which is
// what keeps the two evaluations of one window in step.
func (s *UserService) passkeyManagementAuth(ctx context.Context, p stepUpParams, user *store.User) (stepUpAdmission, error) {
	// One instant for the whole fork. The two rules decide the same question
	// -- may this caller act now -- so a clock that moved between them would
	// let a test pin a disagreement rather than the rule.
	now := s.now()
	// The elevated arm is tried first for EVERY shape, because a session that
	// proved a factor is admitted on the strongest authority available and
	// must not be sent back for a weaker one. The admission is reported as
	// the elevated arm too (firstCredential stays false), so the locked
	// re-check verifies the window rather than the account shape -- see
	// recheckStepUpUnderLock.
	//
	// It also runs BEFORE the account-shape read, so the common path pays no
	// passkey COUNT at all.
	if p.userInfo.Elevated(p.elevationInstant(now)) {
		return stepUpAdmission{}, nil
	}
	// Not elevated. An account that holds no password and no passkey has
	// nothing to elevate WITH, so the sibling rule decides. Its own two
	// halves: accountElevatesOnlyThroughAProvider answers the
	// password-and-passkey question, and
	// assertFirstCredentialWithoutPasswordAllowed the durable-identity one.
	nothingToElevateWith, err := accountElevatesOnlyThroughAProvider(ctx, s.store, user)
	if err != nil {
		return stepUpAdmission{}, connect.NewError(connect.CodeInternal, err)
	}
	if nothingToElevateWith {
		return stepUpAdmission{firstCredential: true}, admitFirstCredential(ctx, s.store, p.userInfo, user, now, p.firstCredentialWindow())
	}
	// An account that holds a factor must present it.
	return stepUpAdmission{}, requireElevation(p.userInfo, p.elevationInstant(now))
}

// assertFirstCredentialWithoutPasswordAllowed refuses a session-only first
// lasting credential (a passkey, or the first password) when the account has
// no durable identity (verified email or OAuth link). A stolen session on an
// unverified shell must not attach a credential it can keep.
//
// The caller decides WHEN the rule applies -- it holds both facts already
// (no password, no passkey) -- and this decides WHETHER the account has a
// durable identity. It carried its own copies of the caller's two checks
// plus a second CountByUser for the same answer; both branches were
// unreachable, and the query doubled a round trip on every passwordless
// admission.
//
// A raised flag is a durable identity only while an ADDRESS exists. The two
// can come apart: resolveEmailVerified excludes a cleared address from the
// lowering rule on purpose, so an administrator who clears a verified
// address leaves email_verified raised over an empty column. The flag alone
// then proves nothing about who holds the session -- and this is the rule
// that decides whether a session may attach the account's first password or
// passkey, which is durable, silently usable, and enough to sign in later.
func assertFirstCredentialWithoutPasswordAllowed(ctx context.Context, tx store.Store, user *store.User) error {
	if user.EmailVerified && user.Email != "" {
		return nil
	}
	uid, ok := userid.New(user.ID)
	if !ok {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("invalid user id"))
	}
	links, err := tx.OAuthUserLinks().ListByUser(ctx, uid)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if len(links) > 0 {
		return nil
	}
	// "password or passkey", not "passkey": ChangePassword reaches this
	// through the same rule, so a user setting their FIRST password was told
	// to act before adding a passkey they never asked for. One message,
	// because both callers share one rule and a second wording would drift.
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("verify your email or link an OAuth provider before setting this account's first password or passkey"))
}

// firstCredentialAuthFreshness limits how long after an authentication a
// session may START attaching the account's FIRST lasting credential.
//
// Five minutes is a re-sign-in, not a work session: long enough to complete
// an identity provider that asks for a second factor, short enough that a
// cookie captured earlier in the day is already stale.
const firstCredentialAuthFreshness = 5 * time.Minute

// ceremonyGrace is the slack the FINISH leg of a passkey registration adds
// to BOTH freshness rules.
//
// It is DERIVED, not chosen. Finish re-runs the same admission that Begin
// ran, against the same fixed instants, so one window evaluated at both
// legs cannot make the guarantee for ANY value: a Begin admitted at the
// last moment is refused at Finish, AFTER the user answered the biometric
// prompt, and the credential the authenticator already created is
// discarded. Finish is unreachable without a Begin that passed within one
// ceremony, so extending by exactly one ceremony covers the gap and widens
// nothing else.
//
// It applies to the ELEVATION rule as well as the first-credential one,
// because both rules can lapse the same way. The first-credential rule was
// widened first and the elevation rule was not, so a Begin admitted in the
// last seconds of the two-hour window was refused at Finish and the client
// -- whose gate re-runs the WHOLE action -- opened a second ceremony while
// the authenticator's first credential was never stored.
const ceremonyGrace = hubwebauthn.CeremonyTTL

// assertFirstCredentialAuthIsFresh requires a recent authentication before a
// session attaches the account's first lasting credential.
//
// An account with no password and no passkey has no step-up credential to
// present, so this is the one admission that runs on session authority
// alone -- and what it attaches is durable, silently usable, and sufficient
// to sign in later. On an OAuth-only account a stolen cookie would otherwise
// convert directly into a permanent password of the attacker's choosing,
// which outlives both the cookie and any OAuth revocation.
//
// Recency is the step-up such an account CAN satisfy. It holds an OAuth link
// or a verified email, every authentication mints a fresh session row, and
// AuthenticatedAt reads that row's created_at rather than its sliding
// expiry -- so "sign in again" is a self-service remedy that a long-lived
// stolen cookie cannot perform without the identity provider.
//
// A bearer credential is refused outright rather than given a window. An API
// or delegation token is minted once and lives until it is revoked, so its
// creation time says nothing about who holds it now, and no human is present
// to re-authenticate. Solo mode never arrives here: every caller refuses it
// before the admission runs.
func assertFirstCredentialAuthIsFresh(userInfo *auth.UserInfo, now time.Time, maxAge time.Duration) error {
	if userInfo == nil || userInfo.Credential.SessionID() == "" {
		// NO marker. A bearer can never carry an elevation, so a prompt would
		// ask for a factor and then refuse the retry for the same reason --
		// which is exactly what requireElevatableSession says about its own
		// refusal.
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("sign in from a browser to set this account's first password or passkey"))
	}
	if userInfo.AuthenticatedAt.IsZero() || now.Sub(userInfo.AuthenticatedAt) > maxAge {
		// WITH the marker, because a prompt now resolves this. The two
		// admissions are siblings, so a session that proves a factor at its
		// identity provider is admitted on the elevated arm even though its
		// sign-in is stale -- and for an account that cannot elevate at all,
		// the prompt's own copy names the remedy ("sign in again, then set a
		// password"), which is this message. Without the marker the client
		// printed this sentence as raw text beside a form and offered
		// nothing.
		return elevationRequiredErrorSaying(
			"sign in again, or verify with your identity provider, then set this account's first password or passkey")
	}
	return nil
}

// admitFirstCredential runs the whole first-credential admission: the
// account must hold a durable identity, and the session must have
// authenticated recently. One function so the two halves of one rule have
// one name and one test seam, and so a later edit cannot add a check to one
// arm and leave the other admitting.
//
// now is a parameter rather than a call to time.Now, so this branch reads
// the SAME clock as the elevation branch beside it -- see UserService.Now.
// It stays a plain function rather than a method because it takes a
// store.Store, which a caller can supply as a transaction.
func admitFirstCredential(ctx context.Context, tx store.Store, userInfo *auth.UserInfo, user *store.User, now time.Time, maxAge time.Duration) error {
	if err := assertFirstCredentialWithoutPasswordAllowed(ctx, tx, user); err != nil {
		return err
	}
	return assertFirstCredentialAuthIsFresh(userInfo, now, maxAge)
}

// stepUpParams carries what a passkey-management admission needs from its
// caller: the acting credential, and how much slack the LEG allows. It is a
// struct rather than two more parameters because the slack differs on
// exactly one leg, and a positional duration at the end of a call reads as
// noise at every site that does not care about it.
type stepUpParams struct {
	userInfo *auth.UserInfo
	// grace widens BOTH freshness rules -- the first-credential window and
	// the elevation window. Every entry leg passes zero; the Finish leg of a
	// registration passes ceremonyGrace, because it re-runs an admission its
	// own Begin already passed. See that constant.
	//
	// Read it through the two methods below and never directly. The grace
	// must reach EVERY predicate a leg evaluates, and it did not: the
	// admission applied it and the locked re-check did not, so a Finish that
	// the admission admitted was refused again inside the transaction --
	// exactly the case ceremonyGrace exists for.
	grace time.Duration
}

// firstCredentialWindow is how old this leg lets the acting authentication
// be. It is the whole first-credential rule the leg applies, so a caller
// cannot apply the window and forget the grace.
func (p stepUpParams) firstCredentialWindow() time.Duration {
	return firstCredentialAuthFreshness + p.grace
}

// elevationInstant is the instant this leg evaluates the elevation window
// at. The grace moves the instant BACK, which asks the same question a
// wider window would ask and needs no second deadline.
//
// Only the elevation predicate takes it. A session-liveness predicate keeps
// the true instant, because a grace there would revive an expired session
// rather than extend a proven factor.
func (p stepUpParams) elevationInstant(now time.Time) time.Time {
	return now.Add(-p.grace)
}

// entryStepUp builds the params for a leg a user starts, which is every leg
// except the Finish of a registration.
func entryStepUp(userInfo *auth.UserInfo) stepUpParams {
	return stepUpParams{userInfo: userInfo}
}

// finishStepUp builds the params for the Finish leg of a registration,
// which re-runs an admission that its own Begin already passed.
func finishStepUp(userInfo *auth.UserInfo) stepUpParams {
	p := entryStepUp(userInfo)
	p.grace = ceremonyGrace
	return p
}

// passkeyMutationResult is what a committed mutation tells
// runPasskeyManagementTx to do after the transaction closes.
//
// The zero value means "nothing further", which is the right default: a
// rename and a registration Finish change no credential the account's other
// sessions stand on, so they revoke nothing.
type passkeyMutationResult struct {
	// revokeOtherCredentials asks for the in-process teardown of every
	// credential this account holds EXCEPT the acting session, which the
	// transaction restamped to authGeneration.
	revokeOtherCredentials bool
	// authGeneration is the generation the transaction committed. It is what
	// the teardown compares against, so an older lease or channel is closed
	// and the surviving session's own are not.
	authGeneration int64
}

// runPasskeyManagementTx admits and runs a passkey-management mutation
// (Finish/Rename/Delete/Deactivate, and ChangePassword's step-up side). The
// admission runs OUTSIDE the user-auth transaction, and so does prepare --
// callers hash a new password there, and Argon2 must not hold SQLite's
// single writer lock (see auth.Login's comment on the same trade). Inside
// the transaction the user row is re-read and the admission is re-derived
// when the credential state moved between the peek and the lock, mirroring
// auth.Login's prelock-verify/locked-recheck pattern.
//
// A committed mutation slides the elevation window, so a user working
// through several settings answers one prompt rather than one per action.
// The slide runs AFTER the transaction commits: it is a second write on the
// same session row, and it must never be the reason a successful mutation
// rolls back.
//
// mutate REPORTS what the commit did, and this runs the after-commit half.
// Three callers used to hoist a pair of variables above the call, assign them
// inside the closure and repeat the same post-commit revocation, so the rule
// "revoke after the transaction commits, never inside it" was re-typed at
// every site and a fourth mutation could commit and forget it. Here the
// ordering is a property of this function.
func (s *UserService) runPasskeyManagementTx(
	ctx context.Context,
	p stepUpParams,
	prepare func(peek *store.User) error,
	mutate func(tx store.Store, user *store.User) (passkeyMutationResult, error),
) error {
	userInfo := p.userInfo
	var outcome passkeyMutationResult
	peek, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("query user: %w", err))
	}
	admission, err := s.passkeyManagementAuth(ctx, p, peek)
	if err != nil {
		return err
	}
	if prepare != nil {
		if err := prepare(peek); err != nil {
			return err
		}
	}
	if err := s.store.RunInUserAuthTransaction(ctx, userInfo.ID, func(tx store.Store) error {
		user, err := tx.Users().GetByID(ctx, userInfo.ID.String())
		if err != nil {
			return fmt.Errorf("query user: %w", err)
		}
		if err := recheckStepUpUnderLock(ctx, tx, user, peek, admission, p, s.now()); err != nil {
			return err
		}
		var mutateErr error
		// Assigned, never accumulated: the store may run this callback more
		// than once when a distributed backend aborts the transaction, and a
		// re-run must overwrite what the aborted attempt reported rather than
		// add to it. See store.Store.RunInTransaction.
		outcome, mutateErr = mutate(tx, user)
		return mutateErr
	}); err != nil {
		return err
	}
	if outcome.revokeOtherCredentials {
		// AFTER the commit, and this is the one place that ordering is
		// stated. The acting session survives at the new generation, which
		// the transaction already restamped, so the in-process teardown of
		// every older-generation lease and channel runs against a session
		// that will not be caught by it.
		s.lifecycle.RevokeUserPreservingSession(
			userInfo.ID.String(), userInfo.Credential.SessionID(), outcome.authGeneration)
	}
	slideElevation(ctx, s.store, userInfo, s.now())
	return nil
}

// recheckStepUpUnderLock re-derives the admission from the LOCKED row when
// a concurrent write could have invalidated the pre-transaction peek.
//
// A structural flip (a password added or removed concurrently) changes WHICH
// branch of passkeyManagementAuth applies, so the admission the caller
// obtained no longer describes the state the mutation would commit against;
// it fails with a clean retry error.
//
// There is no password re-verification here any more. The elevation branch
// verifies no secret at THIS call -- a factor was proven earlier and the
// session carries the result -- so a concurrently rotated password hash has
// nothing left to re-check against. The password-set flip above still
// catches the case that matters, because it is the case that changes the
// rule.
//
// What a rotation DOES invalidate is the acting session, and that is what
// the elevated arm re-reads. Every password rotation revokes the account's
// sessions under this same lock, so a mutation admitted before the rotation
// waits here and would otherwise commit on the authority of a session the
// rotation already deleted -- the owner changes their password to lock an
// attacker out, and the attacker's queued passkey deletion lands anyway.
// One indexed primary-key read, only on the elevated arm.
//
// The first-credential admission is RE-DERIVED rather than spot-checked. It
// used to re-read the passkey count alone -- an enumeration of the inputs
// somebody believed a concurrent write could move -- which left the
// durable-identity half unguarded: an administrator who cleared the account's
// verified address while this request queued still let the session attach the
// account's first password or passkey on an authority that had disappeared.
// admitFirstCredential already takes a store.Store, so running the WHOLE rule
// against the locked row costs the same query the enumeration did and cannot
// fall behind the rule it is meant to re-check.
//
// It also re-evaluates the five-minute window at now, which refuses a request
// whose window lapsed during the Argon2 hash and the lock wait -- the same
// answer the elevated arm gives for its own lapsed window.
//
// The re-derivation runs only for a passwordless account that had no passkey,
// so the writer lock is not paying for it on the common path.
func recheckStepUpUnderLock(
	ctx context.Context,
	tx store.Store,
	locked, peek *store.User,
	admission stepUpAdmission,
	p stepUpParams,
	now time.Time,
) error {
	if err := recheckCredentialEpochUnderLock(locked, p.userInfo); err != nil {
		return err
	}
	if locked.PasswordSet != peek.PasswordSet {
		return stepUpStateMovedError()
	}
	if !admission.firstCredential {
		return recheckActingSessionUnderLock(ctx, tx, p, now)
	}
	// The account was admitted BECAUSE it held no credential to present. A
	// registration that committed in the window gave it one, so the caller
	// must elevate instead; without this re-read, two concurrent
	// first-credential mutations both commit and the second sets a password
	// with no step-up at all.
	nothingToElevateWith, err := accountElevatesOnlyThroughAProvider(ctx, tx, locked)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if !nothingToElevateWith {
		return stepUpStateMovedError()
	}
	// The other half of the same rule, against the locked row. A refusal here
	// is the rule's own message rather than the retry error, because it names
	// what the caller must do next and a retry would meet the same answer.
	return admitFirstCredential(ctx, tx, p.userInfo, locked, now, p.firstCredentialWindow())
}

// recheckActingSessionUnderLock refuses a mutation whose elevation window
// closed while the request waited for the lock.
//
// It runs only on the elevated arm: the first-credential arm is admitted by
// the session's authentication instant, which no concurrent write moves, and
// its own count re-read covers what does. A bearer never reaches here,
// because requireElevation refused it before the transaction opened.
//
// An ABSENT session row is tolerated when the OWNER ended the session, and
// that is the one case this does not refuse. Sessions().Delete does not
// contend on the user-auth lock, so a plain sign-out in another tab can
// remove the acting session in the middle of a change the user legitimately
// started, and rolling that change back was a real regression once already
// (see TestChangePassword_ToleratesConcurrentActingSessionDeletion).
// Refusing it would buy very little: a caller that reached this point
// already proved a factor, so the window it would close is one in-flight
// request by somebody who held the credential.
//
// Absence alone cannot separate that race from a REVOCATION, because both
// paths delete the row, so absence asks two further questions.
// recheckCredentialEpochUnderLock runs first and refuses every revocation
// that moves the account's credential epoch, whether or not the row
// survives. What the epoch does NOT cover is an administrator taking away
// this ONE session: that must not sign the user's other sessions out, so it
// leaves the epoch where it is. The revocation event kind is the fact that
// separates it, and this reads it -- an administrator's revoke writes
// RevocationEventKindSessionRevoked, a sign-out writes
// RevocationEventKindSession.
//
// Both reads run in the same transaction and neither one locks, so they
// share one snapshot and cannot disagree: an administrator's DELETE is
// visible here only together with the event its own transaction inserted.
//
// A LAPSED window is different, and it is refused. Nothing legitimate waits
// two hours on this lock, so a request whose window closed while it queued
// is not a race to tolerate. The leg's grace applies to that predicate and
// to nothing else here: the row lookup keeps the true instant, or a grace
// would revive a session that expired rather than extend a proven factor.
func recheckActingSessionUnderLock(ctx context.Context, tx store.Store, p stepUpParams, now time.Time) error {
	sessionID := p.userInfo.Credential.SessionID()
	if sessionID == "" {
		return stepUpStateMovedError()
	}
	session, err := tx.Sessions().GetByID(ctx, sessionID, now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return refuseIfSessionWasRevoked(ctx, tx, sessionID)
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("re-read the acting session: %w", err))
	}
	if !auth.NewElevation(session.ElevationProvenAt, session.ElevationExpiresAt).IsCurrent(p.elevationInstant(now)) {
		return stepUpStateMovedError()
	}
	return nil
}

// refuseIfSessionWasRevoked answers the absent-row case: an administrator's
// revoke is refused, and the owner's own sign-out is tolerated.
//
// A read failure REFUSES. This runs only when the acting session already
// disappeared, so the question it cannot answer is "did an administrator
// take this session away", and committing a step-up mutation on an
// unanswered version of that question is the wrong direction to fail in.
// The cost of the strict arm is one retry for a user who signed out in
// another tab while the database was unreachable.
func refuseIfSessionWasRevoked(ctx context.Context, tx store.Store, sessionID string) error {
	revoked, err := tx.RevocationEvents().SessionWasRevoked(ctx, sessionID)
	if err != nil {
		return connect.NewError(connect.CodeInternal,
			fmt.Errorf("check whether the acting session was revoked: %w", err))
	}
	if revoked {
		return stepUpStateMovedError()
	}
	return nil
}

// recheckCredentialEpochUnderLock refuses a mutation whose credential the
// account revoked while the request waited for the lock.
//
// This is what replaces the password-hash re-verification the elevation
// model removed. The elevated arm verifies no secret at THIS call -- a
// factor was proven earlier and the session carries the result -- so a
// concurrently rotated hash has nothing left to re-check against. What a
// rotation DOES do is bump the account's credential epoch, and so does an
// administrator's reset and a "revoke every credential". Reading the epoch
// therefore refuses every one of them with one comparison.
//
// It must run before recheckActingSessionUnderLock, and it is the reason
// that function may tolerate an absent row at all. Both a revocation and a
// plain sign-out DELETE the acting session, so row presence cannot separate
// them; the epoch can. Sessions().Delete alone -- Logout, and the admin's
// per-session revoke -- leaves the epoch where it is, while every path that
// means "this account's credentials are no longer trusted"
// (revokeEveryUserCredential, revokeOtherCredentialsPreservingSession)
// moves it.
//
// Without this, the owner changes their password to lock an attacker out,
// the rotation deletes the attacker's session, and the attacker's queued
// passkey deletion then finds no row, is tolerated, and commits anyway.
//
// It covers BOTH arms of the admission. The first-credential arm rests on
// the session's authentication instant, which no concurrent write moves --
// but the epoch that authorises that session is a different fact, and a
// revocation moves it.
func recheckCredentialEpochUnderLock(locked *store.User, userInfo *auth.UserInfo) error {
	if userInfo == nil {
		return stepUpStateMovedError()
	}
	if locked.AuthGeneration > userInfo.UserAuthGeneration {
		return stepUpStateMovedError()
	}
	return nil
}

// stepUpStateMovedError reports credential state that moved between the
// admission and the lock. The caller must retry, because the credential
// they presented no longer matches the state the mutation would commit
// against.
func stepUpStateMovedError() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("account credentials changed; please retry"))
}

func (s *UserService) BeginPasskeyRegistration(ctx context.Context, req *connect.Request[leapmuxv1.BeginPasskeyRegistrationRequest]) (*connect.Response[leapmuxv1.BeginPasskeyRegistrationResponse], error) {
	userInfo, err := s.passkeyManagementCaller(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Peek only: the ceremony writes nothing yet, so this refuses an
	// un-elevated caller BEFORE the browser prompt rather than after it.
	if _, err := s.passkeyManagementAuth(ctx, entryStepUp(userInfo), user); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}

	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	sessionID, optionsJSON, rpID, err := wa.BeginRegistration(ctx, user.ID, originFromRequest(req))
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	return connect.NewResponse(&leapmuxv1.BeginPasskeyRegistrationResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
		RpId:        rpID,
	}), nil
}

func (s *UserService) FinishPasskeyRegistration(ctx context.Context, req *connect.Request[leapmuxv1.FinishPasskeyRegistrationRequest]) (*connect.Response[leapmuxv1.FinishPasskeyRegistrationResponse], error) {
	userInfo, err := s.passkeyManagementCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("session_id is required"))
	}
	if req.Msg.GetCredentialJson() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("credential_json is required"))
	}
	friendlyName, err := validatePasskeyFriendlyName(req.Msg.GetFriendlyName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var passkey *store.PasskeyCredential
	var verified hubwebauthn.FinishedSignUpCredential
	// The attestation is verified in prepare -- after admission, still
	// OUTSIDE the write transaction. It is a keystore decrypt per existing
	// credential plus a JSON/base64/CBOR parse of a body capped only at the
	// request limit, and on SQLite the transaction holds the single writer
	// lock, so every other write on the hub would queue behind it. Only the
	// credential INSERT needs the lock.
	//
	// The FINISH leg of a ceremony a Begin already admitted, so the
	// first-credential window covers that ceremony too. Without it a
	// registration admitted near the end of the window is refused AFTER
	// the user answered the biometric prompt.
	if err := s.runPasskeyManagementTx(ctx, finishStepUp(userInfo),
		func(peek *store.User) error {
			wa, err := s.webauthnService(ctx)
			if err != nil {
				return err
			}
			verified, err = wa.VerifyRegistration(ctx, peek.ID, req.Msg.GetSessionId(), req.Msg.GetCredentialJson())
			return err
		},
		func(tx store.Store, user *store.User) (passkeyMutationResult, error) {
			wa, err := s.webauthnServiceWithStore(ctx, tx)
			if err != nil {
				return passkeyMutationResult{}, err
			}
			passkey, err = wa.StoreCredential(ctx, tx, id.Generate(), user.ID, verified, friendlyName)
			return passkeyMutationResult{}, err
		}); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}

	return connect.NewResponse(&leapmuxv1.FinishPasskeyRegistrationResponse{
		Passkey: passkeyInfoToProto(ctx, passkey),
	}), nil
}

func (s *UserService) ListPasskeys(ctx context.Context, _ *connect.Request[leapmuxv1.ListPasskeysRequest]) (*connect.Response[leapmuxv1.ListPasskeysResponse], error) {
	userInfo, err := s.passkeyManagementCaller(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.PasskeyCredentials().ListByUser(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Surface an unconfigured hub instead of hiding it behind an empty
	// list: the client cannot tell "no passkeys" from "passkeys cannot run
	// here", and the settings page would render a broken section.
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	passkeys := make([]*leapmuxv1.PasskeyInfo, 0, len(rows))
	for i := range rows {
		passkeys = append(passkeys, passkeyInfoToProto(ctx, &rows[i]))
	}
	return connect.NewResponse(&leapmuxv1.ListPasskeysResponse{Passkeys: passkeys, RpId: wa.RPID()}), nil
}

func (s *UserService) RenamePasskey(ctx context.Context, req *connect.Request[leapmuxv1.RenamePasskeyRequest]) (*connect.Response[leapmuxv1.RenamePasskeyResponse], error) {
	userInfo, err := s.passkeyManagementCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	friendlyName, err := validatePasskeyFriendlyName(req.Msg.GetFriendlyName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var row *store.PasskeyCredential
	if err := s.runPasskeyManagementTx(ctx, entryStepUp(userInfo), nil, func(tx store.Store, user *store.User) (passkeyMutationResult, error) {
		got, err := tx.PasskeyCredentials().GetByID(ctx, req.Msg.GetId())
		if err != nil {
			return passkeyMutationResult{}, err
		}
		if got.UserID != user.ID {
			return passkeyMutationResult{}, store.ErrNotFound
		}
		if err := tx.PasskeyCredentials().UpdateFriendlyName(ctx, got.ID, got.UserID, friendlyName); err != nil {
			return passkeyMutationResult{}, err
		}
		got.FriendlyName = friendlyName
		row = got
		return passkeyMutationResult{}, nil
	}); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	return connect.NewResponse(&leapmuxv1.RenamePasskeyResponse{
		Passkey: passkeyInfoToProto(ctx, row),
	}), nil
}

func (s *UserService) DeletePasskey(ctx context.Context, req *connect.Request[leapmuxv1.DeletePasskeyRequest]) (*connect.Response[leapmuxv1.DeletePasskeyResponse], error) {
	userInfo, err := s.passkeyManagementCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	// Hash the replacement password outside the transaction (Argon2 must
	// not hold the SQLite writer lock). Only the last-passkey branch on a
	// passwordless account needs one; the transaction re-derives that
	// decision from the locked row and refuses an empty hash.
	var hashedNewPassword string
	prepare := func(peek *store.User) error {
		if peek.PasswordSet {
			return nil
		}
		count, err := s.store.PasskeyCredentials().CountByUser(ctx, peek.ID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		if count > 1 {
			return nil
		}
		if req.Msg.GetNewPassword() == "" {
			return lastPasskeyNeedsPasswordError()
		}
		hashed, err := hashReplacementPassword(req.Msg.GetNewPassword())
		if err != nil {
			return err
		}
		hashedNewPassword = hashed
		return nil
	}
	if err := s.runPasskeyManagementTx(ctx, entryStepUp(userInfo), prepare, func(tx store.Store, user *store.User) (passkeyMutationResult, error) {
		row, err := tx.PasskeyCredentials().GetByID(ctx, req.Msg.GetId())
		if err != nil {
			return passkeyMutationResult{}, err
		}
		if row.UserID != user.ID {
			return passkeyMutationResult{}, store.ErrNotFound
		}
		count, err := tx.PasskeyCredentials().CountByUser(ctx, user.ID)
		if err != nil {
			return passkeyMutationResult{}, err
		}
		// Last passkey on a passkey-only account: delegate to the
		// deactivation commit (plan CommitPasskeyDelete).
		if !user.PasswordSet && count <= 1 {
			return s.commitPasskeyDeactivation(ctx, tx, user, hashedNewPassword, userInfo.Credential.SessionID())
		}
		return passkeyMutationResult{}, tx.PasskeyCredentials().Delete(ctx, row.ID, user.ID)
	}); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	return connect.NewResponse(&leapmuxv1.DeletePasskeyResponse{}), nil
}

func (s *UserService) DeactivatePasskeyAuth(ctx context.Context, req *connect.Request[leapmuxv1.DeactivatePasskeyAuthRequest]) (*connect.Response[leapmuxv1.DeactivatePasskeyAuthResponse], error) {
	userInfo, err := s.passkeyManagementCaller(ctx)
	if err != nil {
		return nil, err
	}

	// Hash the replacement password outside the transaction (Argon2 must
	// not hold the SQLite writer lock). Only a passwordless account needs
	// one; the transaction re-derives that from the locked row.
	var hashedNewPassword string
	prepare := func(peek *store.User) error {
		if peek.PasswordSet {
			return nil
		}
		hashed, err := hashReplacementPassword(req.Msg.GetNewPassword())
		if err != nil {
			return err
		}
		hashedNewPassword = hashed
		return nil
	}
	if err := s.runPasskeyManagementTx(ctx, entryStepUp(userInfo), prepare, func(tx store.Store, user *store.User) (passkeyMutationResult, error) {
		return s.commitPasskeyDeactivation(ctx, tx, user, hashedNewPassword, userInfo.Credential.SessionID())
	}); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	return connect.NewResponse(&leapmuxv1.DeactivatePasskeyAuthResponse{}), nil
}

// hashReplacementPassword validates and hashes a replacement password for
// the passkey-deactivation paths. Call outside the user-auth transaction:
// Argon2 must not hold the SQLite writer lock.
func hashReplacementPassword(newPassword string) (string, error) {
	if err := validate.ValidatePassword(newPassword); err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	hashed, err := password.Hash(newPassword)
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}
	return hashed, nil
}

// commitPasskeyDeactivation deletes every passkey for the user. On a
// passkey-only account it also sets the pre-hashed replacement password
// (hashedNewPassword; empty means the caller did not supply one) and
// revokes other sessions and tokens while preserving the acting session
// (mirror ChangePassword). Caller must hold the user-auth lock and must
// verify auth before this call.
func (s *UserService) commitPasskeyDeactivation(ctx context.Context, tx store.Store, user *store.User, hashedNewPassword, actingSessionID string) (passkeyMutationResult, error) {
	wasPasskeyOnly := !user.PasswordSet
	if wasPasskeyOnly {
		if hashedNewPassword == "" {
			return passkeyMutationResult{}, lastPasskeyNeedsPasswordError()
		}
		if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			PasswordHash: hashedNewPassword,
			ID:           user.ID,
		}); err != nil {
			return passkeyMutationResult{}, fmt.Errorf("update password: %w", err)
		}
	}
	if err := tx.PasskeyCredentials().DeleteAllByUser(ctx, user.ID); err != nil {
		return passkeyMutationResult{}, err
	}
	if !wasPasskeyOnly {
		return passkeyMutationResult{}, nil
	}
	gen, err := s.revokeOtherCredentialsPreservingSession(ctx, tx, user.ID, actingSessionID)
	if err != nil {
		return passkeyMutationResult{}, err
	}
	return passkeyMutationResult{revokeOtherCredentials: true, authGeneration: gen}, nil
}

// revokeOtherCredentialsPreservingSession deletes other sessions, revokes API
// and delegation tokens, bumps auth_generation, and restamps the acting
// session. Caller must hold the user-auth transaction. Shared by
// ChangePassword, DeletePasskey, and DeactivatePasskeyAuth.
//
// RefreshAuthGeneration returning n==0 means the acting session was
// concurrently deleted (a same-user logout does not contend on this
// user-auth lock) after the transaction began. The password change itself
// stays valid and there is no surviving session row left to restamp, so the
// caller does not roll the change back; the post-transaction revocation is
// a harmless no-op for a same-process logout and self-heals across hubs
// once the durable session-revoked event replays. n>1 is impossible
// (session id is unique) and indicates corruption, so it stays fatal.
func (s *UserService) revokeOtherCredentialsPreservingSession(ctx context.Context, tx store.Store, userID, actingSessionID string) (int64, error) {
	rowUID, err := mintRowUserID(userID)
	if err != nil {
		return 0, err
	}
	if err := tx.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
		UserID: rowUID,
		KeepID: actingSessionID,
	}); err != nil {
		return 0, fmt.Errorf("delete other sessions: %w", err)
	}
	if _, _, err := auth.RevokeAllUserCredentials(ctx, tx, rowUID); err != nil {
		return 0, err
	}
	if actingSessionID != "" {
		n, err := tx.Sessions().RefreshAuthGeneration(ctx, store.RefreshSessionAuthGenerationParams{
			SessionID: actingSessionID,
			UserID:    rowUID,
		})
		if err != nil {
			return 0, fmt.Errorf("refresh current session auth generation: %w", err)
		}
		if n > 1 {
			return 0, fmt.Errorf("refresh current session auth generation: updated %d rows", n)
		}
	}
	updatedUser, err := tx.Users().GetByID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("query updated user auth generation: %w", err)
	}
	return updatedUser.AuthGeneration, nil
}

func validatePasskeyFriendlyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Passkey", nil
	}
	// Count characters, not bytes: a CJK or emoji name under 64 characters
	// is valid input and must not be rejected by a message about characters.
	if utf8.RuneCountInString(name) > 64 {
		return "", fmt.Errorf("friendly name must be at most 64 characters")
	}
	return name, nil
}

func passkeyInfoToProto(ctx context.Context, row *store.PasskeyCredential) *leapmuxv1.PasskeyInfo {
	if row == nil {
		return nil
	}
	info := &leapmuxv1.PasskeyInfo{
		Id:           row.ID,
		FriendlyName: row.FriendlyName,
		CreatedAt:    timestamppb.New(row.CreatedAt),
		// Base64url credential id for the browser Signal API after delete.
		CredentialId: base64.RawURLEncoding.EncodeToString(row.CredentialID),
	}
	if row.LastUsedAt != nil {
		info.LastUsedAt = timestamppb.New(*row.LastUsedAt)
	}
	// A malformed transports column degrades the browser hint rather than
	// failing the RPC, but it must not pass silently: the only production
	// writer emits json.Marshal output, so a value that will not parse
	// means the row was written from outside that path.
	transports, err := parsePasskeyTransports(row.Transports)
	if err != nil {
		slog.WarnContext(ctx, "passkey transports column did not parse",
			"passkey_id", row.ID, "err", err)
	}
	info.Transports = transports
	return info
}

func parsePasskeyTransports(raw string) ([]string, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var transports []string
	if err := json.Unmarshal([]byte(raw), &transports); err != nil {
		return nil, fmt.Errorf("parse passkey transports: %w", err)
	}
	return transports, nil
}

// mapPasskeyConnectError classifies every error the passkey-management and
// registration surface can return. A connect error that a handler already
// built passes through; a store miss is NotFound; everything else routes
// through classifyWebAuthnError, so a cancelled prompt and an unserved
// origin answer the same way here as they do on the login surface.
func mapPasskeyConnectError(ctx context.Context, err error) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr
	}
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	switch classifyWebAuthnError(err) {
	case webAuthnErrorClone:
		// A clone warning is a security event, not a registration failure.
		slog.WarnContext(ctx, "passkey clone warning during passkey management")
		return credentialRejectedError(err)
	case webAuthnErrorCredential:
		// CredentialRejectedHeader, because the rejected credential is the
		// one the REQUEST carried and not the session that made it. Without
		// it the client's blanket rule read this Unauthenticated as "your
		// session ended" and signed the user out mid-dialog: a mismatched RP
		// ID after public_url changed, an expired or replayed ceremony
		// session, or a clone warning threw them back to /login instead of
		// showing "Failed to add passkey".
		//
		// The session is never what fails here. auth.MustGetUser and the
		// interceptor answer a dead session before this runs, and every
		// concurrency refusal on this surface is FailedPrecondition
		// (stepUpStateMovedError), which takes the *connect.Error early
		// return above.
		return credentialRejectedError(err)
	case webAuthnErrorUnavailable:
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case webAuthnErrorInfrastructure:
		return connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
