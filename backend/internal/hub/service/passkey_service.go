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

// deactivationNeedsPasswordError is the same rule for the other leg:
// DeactivatePasskeyAuth deletes every passkey at once, so an account with no
// password must supply one here too.
//
// A SEPARATE constructor rather than a shared one, and the wording is the
// reason. lastPasskeyNeedsPasswordError ends "or use DeactivatePasskeyAuth",
// which offers a remedy that this RPC IS -- inside it that clause reads as an
// instruction to call the call that is already running.
func deactivationNeedsPasswordError() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("cannot turn off passkey sign-in without a password; provide new_password"))
}

func rejectSoloPasskeyManagement(solo bool) error {
	return rejectSolo(solo, "passkey management")
}

// stepUpAdmission records HOW stepUpMutationAuth admitted a mutation, so
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
// its own four: the ORDER is the rule. This refuses solo mode FIRST, so
// resolving the credential cannot answer a solo caller with a message about a
// user that is not the one it means. A seventh handler that forgot the
// refusal would reach a surface the mode does not serve.
//
// It refuses the MODE and not the caller, unlike rejectSoloElevation next
// door, and the difference is deliberate: a passkey is unusable on a solo hub
// whoever asks. GetSystemInfo reports passkey_enabled false for every origin
// there, so admitting a signed-in caller here would offer management verbs for
// a capability the same hub says it does not have.
func (s *UserService) passkeyManagementCaller(ctx context.Context) (*auth.UserInfo, error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	return auth.MustGetUser(ctx)
}

// stepUpMutationAuth decides whether a step-up mutation may proceed. The
// mutations are the four passkey verbs and ChangePassword.
//
// It admits every ELEVATABLE credential: a browser session, and a
// command-line credential, which proves its factor in a browser through the
// /oauth/step-up leg and carries a window of its own. A
// delegation bearer and solo mode carry no window and cannot obtain one, so
// requireElevation refuses them with the plain message that states the
// remedy: sign in from a browser.
//
// There is NO extra session rule here, and an earlier pass that added one was
// wrong twice over. It refused a credential the whole elevation design admits
// -- every other protected surface takes it -- and it hid two real defects
// rather than correcting them: the locked re-check was session-shaped, and
// the revocation had no exclusion for the acting command-line credential.
// Both are corrected at the source, in recheckActingCredentialUnderLock and
// in revokeOtherCredentialsPreservingActingCredential, so the whole path is
// credential-shaped rather than session-shaped.
//
// The one leg a command line still cannot run is the REGISTRATION ceremony.
// BeginPasskeyRegistration and FinishPasskeyRegistration need a WebAuthn
// authenticator, which is a property of WebAuthn and not of this gate; the
// management verbs (rename, delete, deactivate) and ChangePassword work from
// an elevated command-line credential.
//
// Every admitted account elevates, through requireElevation: one proven
// factor admits every sensitive action for auth.ElevationWindow, and each
// success slides the window.
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
//     only branch, that leg could never help the two procedures it exists
//     for. A user proved a factor at their identity provider, came back, and
//     the hub refused them with the same message as before; only signing out
//     and in again worked.
//
// Each branch answers "a credential that never can prove itself" for itself,
// so neither one gives such a caller a prompt that its retry would fail
// again: requireElevation reaches requireElevatableCredential on the
// elevation branch, and assertFirstCredentialAuthIsFresh refuses a bearer on
// the first-credential branch. The two answers differ, and the difference is
// the account shape rather than an oversight. An elevated command-line
// credential proves that somebody stood at a browser inside the window, so
// the elevation branch takes it; the first-credential branch has no
// elevation to read and rests on a sign-in instant instead, which a
// long-lived bearer does not have.
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
// The locked re-check in runStepUpMutationTx takes the same p, which is
// what keeps the two evaluations of one window consistent.
func (s *UserService) stepUpMutationAuth(ctx context.Context, p stepUpParams, user *store.User) (stepUpAdmission, error) {
	// One instant for the whole fork. The two rules decide the same question
	// -- may this caller act now -- so a clock that moved between them would
	// let a test pin a disagreement rather than the rule.
	now := s.now()
	// The elevated branch runs first for EVERY shape, because this admits a
	// session that proved a factor on the strongest authority available and
	// must not send it back for a weaker one. The admission reports the
	// elevated branch too (firstCredential stays false), so the locked
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
// can separate: resolveEmailVerified excludes a cleared address from the
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
	// through the same rule, so the old message told a user who set their
	// FIRST password to act before adding a passkey they never asked for.
	// One message, because both callers share one rule and a second wording
	// would drift.
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
// legs cannot make the guarantee for ANY value: Finish refuses a Begin
// admitted at the last moment, AFTER the user answered the biometric
// prompt, and the hub discards the credential the authenticator already
// created. Finish is unreachable without a Begin that passed within one
// ceremony, so extending by exactly one ceremony covers the gap and widens
// nothing else.
//
// It applies to the ELEVATION rule as well as the first-credential one,
// because both rules can lapse the same way. An earlier change widened the
// first-credential rule and left the elevation rule alone, so Finish refused
// a Begin admitted in the last seconds of the two-hour window and the client
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
// This refuses a bearer credential outright rather than giving it a window.
// The hub mints an API or delegation token once, and it lives until a revoke
// ends it, so its creation time says nothing about who holds it now, and no
// human is present to re-authenticate.
//
// The bearer refusal below is the AUTHORITATIVE one for this branch, and it
// is the only refusal a bearer meets on it: stepUpMutationAuth admits every
// elevatable credential, so a command-line credential reaches this line
// whenever the account holds no password and no passkey. Its remedy is the
// sibling branch -- elevate through the browser leg, and the elevation
// branch takes it before this rule ever runs. The same refusal also fails
// closed on a nil UserInfo, which must never reach the AuthenticatedAt read
// below.
//
// SOLO REACHES THIS, since ChangePassword stopped refusing solo mode, and it
// misses this branch only by way of a column: bootstrap.Run writes
// PasswordSet true with an EMPTY hash, so accountElevatesOnlyThroughAProvider
// short-circuits on the claim rather than on a password that works. Correct
// that column to the honest false and every solo ChangePassword lands here,
// where the remedy -- verify an email, or link a provider -- is one the solo
// account can never take, and the first password becomes impossible to set.
func assertFirstCredentialAuthIsFresh(userInfo *auth.UserInfo, now time.Time, maxAge time.Duration) error {
	if userInfo == nil || userInfo.Credential.SessionID() == "" {
		// NO marker, and the message states the remedy instead. A delegation
		// bearer can carry no elevation at all, so a step-up prompt would ask
		// for a factor and then refuse the retry for the same reason -- what
		// requireElevatableCredential says about its own refusal. A
		// command-line credential can carry one, and the elevated branch
		// above already took it if it did; reaching this line means it did
		// not, and this account elevates only at its identity provider, in a
		// browser. Signing in there is a remedy BOTH kinds can act on, and it
		// is the only remedy for an account whose durable identity is a
		// verified email with no provider behind it.
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("sign in from a browser to set this account's first password or passkey"))
	}
	if userInfo.AuthenticatedAt.IsZero() || now.Sub(userInfo.AuthenticatedAt) > maxAge {
		// WITH the marker, because a prompt now resolves this. The two
		// admissions are siblings, so the elevated branch admits a session that
		// proves a factor at its identity provider even though its
		// sign-in is stale -- and for an account that cannot elevate at all,
		// the prompt's own copy states the remedy ("sign in again, then set a
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
// half and leave the other admitting.
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
	// admission applied it and the locked re-check did not, so the transaction
	// refused a Finish again although the admission admitted it -- exactly the
	// case ceremonyGrace exists for.
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

// stepUpMutationResult is what a committed mutation tells
// runStepUpMutationTx to do after the transaction closes.
//
// The zero value means "nothing further", which is the right default: a
// rename and a registration Finish change no credential the account's other
// sessions stand on, so they revoke nothing.
type stepUpMutationResult struct {
	// revokeOtherCredentials asks for the in-process teardown of every
	// credential this account holds EXCEPT the acting session, which the
	// transaction restamped to authGeneration.
	revokeOtherCredentials bool
	// authGeneration is the generation the transaction committed. It is what
	// the teardown compares against, so the teardown closes an older lease or
	// channel and spares the surviving session's own.
	authGeneration int64
}

// runStepUpMutationTx admits and runs a passkey-management mutation
// (Finish/Rename/Delete/Deactivate, and ChangePassword's step-up side). The
// admission runs OUTSIDE the user-auth transaction, and so does prepare --
// callers hash a new password there, and Argon2 must not hold SQLite's
// single writer lock (see auth.Login's comment on the same trade). Inside
// the transaction this re-reads the user row and re-derives the admission
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
// inside the closure and repeat the same post-commit revocation, so each
// site re-typed the rule "revoke after the transaction commits, never inside
// it", and a fourth mutation could commit and forget it. Here the
// ordering is a property of this function.
func (s *UserService) runStepUpMutationTx(
	ctx context.Context,
	p stepUpParams,
	prepare func(peek *store.User) error,
	mutate func(tx store.Store, user *store.User) (stepUpMutationResult, error),
) error {
	userInfo := p.userInfo
	var outcome stepUpMutationResult
	peek, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("query user: %w", err))
	}
	admission, err := s.stepUpMutationAuth(ctx, p, peek)
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
		// AFTER the commit, and this is the one place that states the
		// ordering. The acting session survives at the new generation, which
		// the transaction already restamped, so the in-process teardown of
		// every older-generation lease and channel runs against a session
		// that it does not catch.
		//
		// A command-line credential passes an empty session id, so the
		// preserve step does nothing and the teardown closes that
		// credential's own leases and channels along with the rest. It is a
		// transient disconnect and never a sign-out: the transaction
		// restamped the api_tokens row too, so the very next request
		// authenticates and rebuilds the context. Restamping a BEARER's
		// holders needs a bearer-keyed index that the lease registry and the
		// channel manager do not have today --
		// AuthContextRegistry.RestampSessionLeaseGeneration and
		// Manager.RestampSessionGeneration are both keyed by session id.
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
// branch of stepUpMutationAuth applies, so the admission the caller
// obtained no longer describes the state the mutation would commit against;
// it fails with a clean retry error.
//
// There is no password re-verification here any more. The elevation branch
// verifies no secret at THIS call -- somebody proved a factor earlier and the
// session carries the result -- so a concurrently rotated password hash has
// nothing left to re-check against. The password-set flip above still
// catches the case that matters, because it is the case that changes the
// rule.
//
// What a rotation DOES invalidate is the acting session, and that is what
// the elevated branch re-reads. Every password rotation revokes the account's
// sessions under this same lock, so a mutation admitted before the rotation
// waits here and would otherwise commit on the authority of a session the
// rotation already deleted -- the owner changes their password to lock an
// attacker out, and the attacker's queued passkey deletion lands anyway.
// One indexed primary-key read, only on the elevated branch.
//
// This RE-DERIVES the first-credential admission rather than spot-checking
// it. It used to re-read the passkey count alone -- an enumeration of the
// inputs somebody believed a concurrent write could move -- which left the
// durable-identity half unguarded: an administrator who cleared the account's
// verified address while this request queued still let the session attach the
// account's first password or passkey on an authority that already
// disappeared. admitFirstCredential already takes a store.Store, so running
// the WHOLE rule against the locked row costs the same query the enumeration
// did and cannot drift from the rule it is meant to re-check.
//
// It also re-evaluates the five-minute window at now, which refuses a request
// whose window lapsed during the Argon2 hash and the lock wait -- the same
// answer the elevated branch gives for its own lapsed window.
//
// The re-derivation runs only for a passwordless account that had no passkey,
// so the writer lock does not pay for it on the common path.
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
	if locked.FirstCredentialExempt != peek.FirstCredentialExempt {
		return stepUpStateMovedError()
	}
	if !admission.firstCredential {
		return recheckActingCredentialUnderLock(ctx, tx, p, now)
	}
	// stepUpMutationAuth admitted the account BECAUSE it held no credential
	// to present. A registration that committed in the window gave it one, so
	// the caller must elevate instead; without this re-read, two concurrent
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
	// is the rule's own message rather than the retry error, because it states
	// what the caller must do next and a retry would meet the same answer.
	return admitFirstCredential(ctx, tx, p.userInfo, locked, now, p.firstCredentialWindow())
}

// recheckActingCredentialUnderLock refuses a mutation whose elevation window
// closed while the request waited for the lock.
//
// It runs only on the elevated branch: the credential's authentication
// instant admits the first-credential branch, and no concurrent write moves
// that instant, so its own count re-read covers what does.
//
// It routes on ElevatableRow, exactly as slideElevation and DropElevation
// do, because BOTH elevatable kinds reach it. A session-shaped re-check was
// a defect that no test caught while a second rule refused a command-line
// credential at the gate: SessionID() is empty for an api_tokens row, so an
// elevated command-line credential passed the gate and then met "account
// credentials changed; please retry" inside the transaction, on every
// attempt, permanently. A credential kind that carries no window at all keeps
// the refusal. One caller reaches that case: the solo rung's, which holds no
// credential at all, and the body answers it before the switch.
//
// refuseIfActingAuthorityMovedFrom is the same rule at the other three
// surfaces, and this stays consistent with it rather than contradicting it:
// there a bearer passes on the credential epoch alone, here it re-reads the
// api_tokens row for the one further fact this call needs, which is the
// window.
//
// Nil-safe, like the elevatable-credential rules in elevation_service.go.
// recheckCredentialEpochUnderLock already refuses a nil UserInfo before this
// runs, so this guard is the second one; it stays because a rule that reads
// a credential must never panic on the absence of one.
func recheckActingCredentialUnderLock(ctx context.Context, tx store.Store, p stepUpParams, now time.Time) error {
	if p.userInfo == nil {
		return stepUpStateMovedError()
	}
	// A solo caller the hub authenticated with no credentials has no row to
	// re-read: it holds the zero CredentialIdentity, so there is no session
	// whose elevation could lapse and no token that could be revoked. Its
	// admission comes from the TRANSPORT -- requireElevation exempts it -- and
	// the transport is re-decided on every request rather than stored.
	//
	// The case became reachable when ChangePassword stopped refusing solo
	// mode; before that, rejectSolo was what kept it out, which is why the
	// paragraph above still says requireElevation refuses it first.
	//
	// The window this leaves open is two credential-free callers racing to set
	// the first password, where the second overwrites the first. Both held
	// full administrator access at admission, so neither gained anything by
	// racing, and last writer wins is the honest outcome.
	if p.userInfo.SoloAuthenticated() {
		return nil
	}
	sessionID, apiTokenID, ok := p.userInfo.Credential.ElevatableRow()
	switch {
	case !ok:
		return stepUpStateMovedError()
	case sessionID != "":
		return recheckActingSessionUnderLock(ctx, tx, sessionID, p, now)
	default:
		return recheckActingAPITokenUnderLock(ctx, tx, apiTokenID, p, now)
	}
}

// recheckActingSessionUnderLock is the session branch of the re-check.
//
// This tolerates an ABSENT session row when the OWNER ended the session, and
// that is the one case it does not refuse. Sessions().Delete does not
// contend on the user-auth lock, so a plain sign-out in another tab can
// remove the acting session in the middle of a change the user legitimately
// started, and rolling that change back was a real regression once already
// (see TestChangePassword_ToleratesConcurrentActingSessionDeletion).
// Refusing it would gain very little: a caller that reached this point
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
// A LAPSED window is different, and this refuses it. Nothing legitimate waits
// two hours on this lock, so a request whose window closed while it queued
// is not a race to tolerate. The leg's grace applies to that predicate and
// to nothing else here: the row lookup keeps the true instant, or a grace
// would revive a session that expired rather than extend a proven factor.
func recheckActingSessionUnderLock(
	ctx context.Context, tx store.Store, sessionID string, p stepUpParams, now time.Time,
) error {
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

// recheckActingAPITokenUnderLock is the command-line branch of the re-check.
// It asks the api_tokens row the same question the session branch asks the
// user_sessions row: is the window that admitted this request still current
// at this instant.
//
// It REFUSES an absent row, where the session branch tolerates one. The
// tolerance there exists for the owner's own sign-out in another tab, which
// deletes the session row and means "end this browser", not "end this
// change". A command-line credential has no such verb: nothing deletes a
// live api_tokens row, and the cleanup sweep removes only a row that is
// already revoked or long expired. So absence here means the credential is
// gone, and committing a password change on a credential that is gone is the
// wrong direction to fail in.
//
// A REVOKED row is refused for the same reason, and it is the case the
// session branch answers with the revocation event kind. A single-credential
// revoke -- an administrator's, or the owner's own from the credential list
// -- leaves the account's credential epoch where it is, so
// recheckCredentialEpochUnderLock cannot see it. The column can, and it is
// already on the row this read returned.
//
// The leg's grace applies to the window predicate and to nothing else, for
// the reason the session branch gives.
func recheckActingAPITokenUnderLock(
	ctx context.Context, tx store.Store, apiTokenID string, p stepUpParams, now time.Time,
) error {
	row, err := tx.APITokens().GetByID(ctx, apiTokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return stepUpStateMovedError()
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("re-read the acting credential: %w", err))
	}
	if row.RevokedAt != nil {
		return stepUpStateMovedError()
	}
	if !auth.NewElevation(row.ElevationProvenAt, row.ElevationExpiresAt).IsCurrent(p.elevationInstant(now)) {
		return stepUpStateMovedError()
	}
	return nil
}

// refuseIfSessionWasRevoked answers the absent-row case: it refuses an
// administrator's revoke, and it tolerates the owner's own sign-out.
//
// A read failure REFUSES. This runs only when the acting session already
// disappeared, so the question it cannot answer is "did an administrator
// take this session away", and committing a step-up mutation on an
// unanswered version of that question is the wrong direction to fail in.
// The cost of the strict branch is one retry for a user who signed out in
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
// model removed. The elevated branch verifies no secret at THIS call --
// somebody proved a factor earlier and the session carries the result -- so a
// concurrently rotated hash has nothing left to re-check against. What a
// rotation DOES do is move the account's credential epoch, and so does an
// administrator's reset and a "revoke every credential". Reading the epoch
// therefore refuses every one of them with one comparison.
//
// It must run before recheckActingSessionUnderLock, and it is the reason
// that function may tolerate an absent row at all. Both a revocation and a
// plain sign-out DELETE the acting session, so row presence cannot separate
// them; the epoch can. Sessions().Delete alone -- Logout, and the admin's
// per-session revoke -- leaves the epoch where it is, while every path that
// means "this account's credentials are no longer trusted"
// (RevokeCredentialsAfterRotation,
// revokeOtherCredentialsPreservingActingCredential)
// moves it.
//
// Without this, the owner changes their password to lock an attacker out,
// the rotation deletes the attacker's session, and the attacker's queued
// passkey deletion then finds no row, escapes the refusal, and commits
// anyway.
//
// It covers BOTH branches of the admission. The first-credential branch rests
// on the session's authentication instant, which no concurrent write moves --
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
	if _, err := s.stepUpMutationAuth(ctx, entryStepUp(userInfo), user); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}

	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	sessionID, optionsJSON, err := wa.BeginRegistration(ctx, user.ID, originFromRequest(req))
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	return connect.NewResponse(&leapmuxv1.BeginPasskeyRegistrationResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
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
	// prepare verifies the attestation -- after admission, still
	// OUTSIDE the write transaction. It is a keystore decrypt per existing
	// credential plus a JSON/base64/CBOR parse of a body capped only at the
	// request limit, and on SQLite the transaction holds the single writer
	// lock, so every other write on the hub would queue behind it. Only the
	// credential INSERT needs the lock.
	//
	// The FINISH leg of a ceremony a Begin already admitted, so the
	// first-credential window covers that ceremony too. Without it Finish
	// refuses a registration admitted near the end of the window, AFTER
	// the user answered the biometric prompt.
	if err := s.runStepUpMutationTx(ctx, finishStepUp(userInfo),
		func(peek *store.User) error {
			wa, err := s.webauthnService(ctx)
			if err != nil {
				return err
			}
			verified, err = wa.VerifyRegistration(ctx, peek.ID, req.Msg.GetSessionId(), req.Msg.GetCredentialJson())
			return err
		},
		func(tx store.Store, user *store.User) (stepUpMutationResult, error) {
			wa, err := s.webauthnServiceWithStore(ctx, tx)
			if err != nil {
				return stepUpMutationResult{}, err
			}
			passkey, err = wa.StoreCredential(ctx, tx, id.Generate(), user.ID, verified, friendlyName)
			return stepUpMutationResult{}, err
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
	// list: the client cannot distinguish "no passkeys" from "passkeys cannot
	// run here", and the settings page would render a broken section.
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
	if err := s.runStepUpMutationTx(ctx, entryStepUp(userInfo), nil, func(tx store.Store, user *store.User) (stepUpMutationResult, error) {
		got, err := tx.PasskeyCredentials().GetByID(ctx, req.Msg.GetId())
		if err != nil {
			return stepUpMutationResult{}, err
		}
		if got.UserID != user.ID {
			return stepUpMutationResult{}, store.ErrNotFound
		}
		if err := tx.PasskeyCredentials().UpdateFriendlyName(ctx, got.ID, got.UserID, friendlyName); err != nil {
			return stepUpMutationResult{}, err
		}
		got.FriendlyName = friendlyName
		row = got
		return stepUpMutationResult{}, nil
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
		if peek.FirstCredentialExempt {
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
	if err := s.runStepUpMutationTx(ctx, entryStepUp(userInfo), prepare, func(tx store.Store, user *store.User) (stepUpMutationResult, error) {
		row, err := tx.PasskeyCredentials().GetByID(ctx, req.Msg.GetId())
		if err != nil {
			return stepUpMutationResult{}, err
		}
		if row.UserID != user.ID {
			return stepUpMutationResult{}, store.ErrNotFound
		}
		count, err := tx.PasskeyCredentials().CountByUser(ctx, user.ID)
		if err != nil {
			return stepUpMutationResult{}, err
		}
		// prepare hashed a replacement password because the PEEK said this
		// delete would leave the account with no login method. The locked
		// state disagrees, so the plain-delete branch below is the correct
		// one -- and that branch has nowhere to put the hash. It dropped it
		// and answered success: the account stayed passwordless while the
		// user believed they set a password. The retry recomputes the branch
		// against the settled state.
		//
		// The guard used to run in one direction only. commitPasskeyDeactivation
		// catches a FALLING count, because it refuses an empty hash; a
		// RISING one had nothing to catch it.
		if hashedNewPassword != "" && (user.FirstCredentialExempt || count > 1) {
			return stepUpMutationResult{}, stepUpStateMovedError()
		}
		// Last passkey on a passkey-only account: delegate to the
		// deactivation commit (plan CommitPasskeyDelete).
		if !user.FirstCredentialExempt && count <= 1 {
			return s.commitPasskeyDeactivation(ctx, tx, user, hashedNewPassword, userInfo.Credential)
		}
		return stepUpMutationResult{}, tx.PasskeyCredentials().Delete(ctx, row.ID, user.ID)
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
		if peek.FirstCredentialExempt {
			return nil
		}
		// This answers the empty string FIRST, with the rule. Straight to
		// hashReplacementPassword, validate.ValidatePassword reported how a
		// password must be built -- an answer about a field the caller never
		// filled, on a request whose real fault is that this account keeps no
		// other way to sign in. DeletePasskey states its own leg's rule the
		// same way; the wording differs because that leg offers this RPC as a
		// remedy and this one cannot offer itself.
		if req.Msg.GetNewPassword() == "" {
			return deactivationNeedsPasswordError()
		}
		hashed, err := hashReplacementPassword(req.Msg.GetNewPassword())
		if err != nil {
			return err
		}
		hashedNewPassword = hashed
		return nil
	}
	if err := s.runStepUpMutationTx(ctx, entryStepUp(userInfo), prepare, func(tx store.Store, user *store.User) (stepUpMutationResult, error) {
		return s.commitPasskeyDeactivation(ctx, tx, user, hashedNewPassword, userInfo.Credential)
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
// revokes other sessions and tokens while preserving the acting CREDENTIAL
// (mirror ChangePassword). Caller must hold the user-auth lock and must
// verify auth before this call.
func (s *UserService) commitPasskeyDeactivation(
	ctx context.Context,
	tx store.Store,
	user *store.User,
	hashedNewPassword string,
	acting auth.CredentialIdentity,
) (stepUpMutationResult, error) {
	wasPasskeyOnly := !user.FirstCredentialExempt
	if wasPasskeyOnly {
		// DeletePasskey is the leg that reaches this: its prepare skips the
		// hash while the account holds a second passkey, and a registration
		// that the delete removes again leaves the count at one under the
		// lock. DeactivatePasskeyAuth cannot reach it -- its prepare hashes
		// for every passwordless peek, and recheckStepUpUnderLock refuses a
		// password_set flip -- so the message states the delete's remedy.
		if hashedNewPassword == "" {
			return stepUpMutationResult{}, lastPasskeyNeedsPasswordError()
		}
		if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			PasswordHash: hashedNewPassword,
			ID:           user.ID,
		}); err != nil {
			return stepUpMutationResult{}, fmt.Errorf("update password: %w", err)
		}
	}
	if err := tx.PasskeyCredentials().DeleteAllByUser(ctx, user.ID); err != nil {
		return stepUpMutationResult{}, err
	}
	if !wasPasskeyOnly {
		return stepUpMutationResult{}, nil
	}
	gen, err := s.revokeOtherCredentialsPreservingActingCredential(ctx, tx, user.ID, acting)
	if err != nil {
		return stepUpMutationResult{}, err
	}
	return stepUpMutationResult{revokeOtherCredentials: true, authGeneration: gen}, nil
}

// revokeOtherCredentialsPreservingActingCredential deletes other sessions,
// revokes other API tokens and every delegation token, increments
// auth_generation, and restamps the acting credential onto the new
// generation. Caller must hold the user-auth transaction. Shared by
// ChangePassword, DeletePasskey, and DeactivatePasskeyAuth.
//
// It preserves the ONE row the caller acts on, whichever kind that is, and
// it takes the whole CredentialIdentity rather than a session id so that no
// call site can preserve one kind and forget the other. Both halves route on
// ElevatableRow, exactly as slideElevation and recheckActingCredentialUnderLock
// do.
//
// The command-line case is the reason the parameter changed. Before it, a
// credential that successfully changed its owner's password destroyed itself:
// auth.RevokeAllUserCredentials revoked every api_tokens row with no
// exclusion, the mutation committed, and every later call answered
// Unauthenticated with the credential file still on disk.
//
// PRESERVING a row takes two writes, and each one alone is not enough. The
// revoke must skip the row, and the restamp must move it onto the new
// auth_generation -- validation refuses a row behind users.auth_generation
// whether or not revoked_at is set, so an unrevoked row at the old
// generation reads as revoked.
//
// A credential kind that carries no elevatable row (a delegation bearer, or
// solo mode) preserves nothing and revokes everything. No caller reaches
// that case, because stepUpMutationAuth refuses such a credential before the
// transaction opens; the branch is the second guard, and it fails in the
// safe direction.
//
// RefreshAuthGeneration returning n==0 means a concurrent request removed
// the acting row (a same-user logout does not contend on this user-auth
// lock) after the transaction began. The password change itself stays valid
// and there is no surviving row left to restamp, so the caller does not roll
// the change back; the post-transaction revocation is a harmless no-op for a
// same-process logout and self-heals across hubs once the durable
// session-revoked event replays. n>1 is impossible (each id is a primary
// key) and indicates corruption, so it stays fatal.
func (s *UserService) revokeOtherCredentialsPreservingActingCredential(
	ctx context.Context, tx store.Store, userID string, acting auth.CredentialIdentity,
) (int64, error) {
	rowUID, err := mintRowUserID(userID)
	if err != nil {
		return 0, err
	}
	// ok is false for a credential that carries no window: it keeps nothing,
	// so both ids stay empty and both statements address the whole set.
	actingSessionID, actingAPITokenID, _ := acting.ElevatableRow()
	if err := tx.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
		UserID: rowUID,
		KeepID: actingSessionID,
	}); err != nil {
		return 0, fmt.Errorf("delete other sessions: %w", err)
	}
	if _, _, err := auth.RevokeUserCredentialsExceptAPIToken(ctx, tx, rowUID, actingAPITokenID); err != nil {
		return 0, err
	}
	if err := restampActingCredential(ctx, tx, rowUID, actingSessionID, actingAPITokenID); err != nil {
		return 0, err
	}
	updatedUser, err := tx.Users().GetByID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("query updated user auth generation: %w", err)
	}
	return updatedUser.AuthGeneration, nil
}

// restampActingCredential moves the preserved row onto the account's new
// auth_generation. Exactly one of the two ids is non-empty on every path a
// caller can take, and both are empty for a credential that preserves
// nothing.
//
// A free function rather than two branches inline, so the rule that each
// preserved kind takes a restamp reads once. See the caller for why the
// restamp is half of "preserve", and for what n==0 means.
func restampActingCredential(
	ctx context.Context, tx store.Store, userID userid.UserID, sessionID, apiTokenID string,
) error {
	var n int64
	var err error
	switch {
	case sessionID != "":
		n, err = tx.Sessions().RefreshAuthGeneration(ctx, store.RefreshSessionAuthGenerationParams{
			SessionID: sessionID,
			UserID:    userID,
		})
	case apiTokenID != "":
		n, err = tx.APITokens().RefreshAuthGeneration(ctx, store.RefreshAPITokenAuthGenerationParams{
			TokenID: apiTokenID,
			UserID:  userID,
		})
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("refresh acting credential auth generation: %w", err)
	}
	if n > 1 {
		return fmt.Errorf("refresh acting credential auth generation: updated %d rows", n)
	}
	return nil
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
	// means something outside that path wrote the row.
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
		// it the client's general rule read this Unauthenticated as "your
		// session ended" and signed the user out mid-dialog: a mismatched RP
		// ID after public_url changed, an expired or replayed ceremony
		// session, or a clone warning sent them back to /login instead of
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
