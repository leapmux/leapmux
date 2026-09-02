package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usersettings"
	"github.com/leapmux/leapmux/util/validate"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// UserService implements the leapmux.v1.UserService ConnectRPC handler.
type UserService struct {
	store     store.Store
	cfg       *config.Config
	set       *settings.Manager
	lifecycle *auth.CredentialLifecycleEffects
	mail      mail.Sender
	renderer  mail.Renderer
	keystore  *keystore.Keystore
	// soloGate is the hub's solo-admission gate. ChangePassword tells it the
	// account now holds a password, and reads it to decide whether the caller
	// it just re-armed the rule against needs a session of its own. Nil
	// outside solo mode, and safe there: every method on it accepts a nil
	// receiver.
	soloGate *auth.SoloGate

	// The clock every instant on the elevation path comes from: the grant,
	// the predicate, the slide, and the first-credential freshness rule.
	clockSeam
}

// NewUserService creates a new UserService. renderer carries the hub's
// public URL used to build absolute deep-links in the verification
// emails sent on email-change and resend. ks encrypts passkey material;
// pass nil only in tests that do not exercise passkey RPCs.
func NewUserService(st store.Store, cfg *config.Config, set *settings.Manager, lifecycle *auth.CredentialLifecycleEffects, sender mail.Sender, renderer mail.Renderer, ks *keystore.Keystore) *UserService {
	if lifecycle == nil {
		panic("user service requires credential lifecycle effects")
	}
	return &UserService{store: st, cfg: cfg, set: set, lifecycle: lifecycle, mail: sender, renderer: renderer, keystore: ks}
}

// WithSoloGate attaches the hub's solo-admission gate. Only the hub calls it,
// and only in solo mode; every other construction leaves the gate nil.
func (s *UserService) WithSoloGate(gate *auth.SoloGate) *UserService {
	s.soloGate = gate
	return s
}

func (s *UserService) UpdateProfile(ctx context.Context, req *connect.Request[leapmuxv1.UpdateProfileRequest]) (*connect.Response[leapmuxv1.UpdateProfileResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "profile changes"); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	newUsername, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	displayName, err := validate.SanitizeDisplayName(req.Msg.GetDisplayName(), newUsername)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display name: %w", err))
	}

	usernameChanged := newUsername != user.Username

	// If the username changes, check that the new one is not already taken.
	if usernameChanged {
		existing, err := s.store.Users().GetByUsername(ctx, newUsername)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if err == nil && existing.ID != user.ID {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("username %q is already taken", newUsername))
		}
	}

	// Users().UpdateProfile updates username/display name atomically.
	if err := s.store.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
		Username:    newUsername,
		DisplayName: displayName,
		ID:          user.ID,
	}); err != nil {
		// The pre-check above is only a fast path: two profile updates racing for
		// the same free slug both pass it, then one loses at the unique index
		// (idx_users_username), which the store surfaces as ErrConflict. Map that
		// to the same clear "already taken" error the pre-check returns rather than
		// leaking an opaque 500.
		if errors.Is(err, store.ErrConflict) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("username %q is already taken", newUsername))
		}
		// The store re-validates the slug it will actually persist
		// (UpdateUserProfileParams.Validate); the sanitize above makes that
		// unreachable from this handler, but a validation the store adds later
		// must surface as bad input, not an opaque 500.
		if errors.Is(err, store.ErrInvalidArgument) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Drop the local cached UserInfo only when a cached field (username) changed;
	// a display-name-only edit touches nothing UserInfo caches. This mirrors the
	// store's conditional durable event so both invalidation paths agree.
	if usernameChanged {
		s.lifecycle.UserInfoInvalidated(user.ID)
	}

	return connect.NewResponse(&leapmuxv1.UpdateProfileResponse{
		Username:     newUsername,
		DisplayName:  displayName,
		Email:        user.Email,
		PendingEmail: user.PendingEmail,
	}), nil
}

func (s *UserService) RequestEmailChange(ctx context.Context, req *connect.Request[leapmuxv1.RequestEmailChangeRequest]) (*connect.Response[leapmuxv1.RequestEmailChangeResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "email changes"); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	newEmail := req.Msg.GetNewEmail()
	if newEmail == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("email cannot be empty"))
	}
	if err := validate.ValidateEmail(newEmail); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// The account email is a recovery identity: it receives the account-recovery
	// link. A stolen session that could move it can then confirm the new
	// address itself -- ResendVerificationEmail and VerifyEmail are both
	// allowlisted for an unverified user -- and owns the recovery channel
	// permanently, past the cookie it started from. So this requires a
	// recently proven factor.
	//
	// The gate sits after the syntax checks and before the availability
	// probe. A malformed address is the caller's own typing and reporting it
	// early spares them a prompt they would answer for nothing; the
	// availability probe answers a question about OTHER accounts, so it must
	// not be reachable without the factor.
	if err := requireElevation(userInfo, s.now()); err != nil {
		return nil, err
	}

	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if newEmail == user.Email {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("email is unchanged"))
	}

	// Check that no other user has this email.
	if err := CheckEmailAvailable(ctx, s.store, newEmail, user.ID); err != nil {
		return nil, AvailabilityConnectError(err)
	}

	// Immediate change with no verification round-trip. An administrator
	// takes it under any configuration; everybody else takes it only where
	// the hub cannot verify at all.
	//
	// The new address lands UNVERIFIED either way, and the false below is
	// the whole point. email_verified records whether somebody confirmed
	// THIS address, which nobody did, and raising it for an administrator
	// is the force this change removed from every other site: it made an
	// administrator's unconfirmed address a valid self-service
	// account-recovery target, because RequestAccountRecovery reads the column
	// and cannot take the sign-in exemption. The admin edit of ANOTHER
	// user's address already lowers the flag (see resolveEmailVerified);
	// this is the same rule on the self-service path.
	//
	// The re-read and the write run in ONE user-auth transaction, and only
	// the write is inside it.
	//
	// The gate at the top of this handler answered from a CACHED UserInfo, so
	// "elevated" could be true of a session an administrator already took
	// away -- and the account email receives the recovery link, so a
	// change that lands on revoked authority gives the account away. Under
	// the lock, every path that moves the credential epoch has to wait.
	//
	// The SEND stays outside. An SMTP exchange must never hold SQLite's
	// single writer lock, which is the same trade auth.Login makes for
	// Argon2, so only mintPendingEmailVerification is in the callback and
	// deliverPendingEmailVerification runs after the commit.
	verificationRequired := !userInfo.IsAdmin && settings.EmailVerificationEffective(s.set.Snapshot(ctx))
	// A captured RESULT, which store.Store's contract permits: the callback
	// may run more than once, and a re-run OVERWRITES this rather than adding
	// to it, so the caller reads the attempt that committed.
	var storedCode string
	var mintedUnblockedAt time.Time
	if err := s.store.RunInUserAuthTransaction(ctx, userInfo.ID, func(tx store.Store) error {
		if err := refuseIfActingAuthorityMoved(ctx, tx, userInfo, s.now()); err != nil {
			return err
		}
		if !verificationRequired {
			if err := SetEmailAndClearCompeting(ctx, tx, user.ID, newEmail, false); err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
			return nil
		}
		code, blockedUntil, err := mintPendingEmailVerification(ctx, tx, user.ID, newEmail, s.now())
		if err != nil {
			// The conditional mint refuses inside the cooldown, which is the
			// one thing that stops this RPC from being an open relay: the
			// address is caller-supplied, so an unconditional mint sends a
			// message per request to any address the caller specifies.
			if errors.Is(err, ErrVerificationCooldown) {
				return connect.NewError(connect.CodeResourceExhausted, err)
			}
			return connect.NewError(connect.CodeUnavailable, err)
		}
		storedCode = code
		mintedUnblockedAt = blockedUntil
		return nil
	}); err != nil {
		return nil, err
	}

	// Both effects run AFTER the commit, because both accumulate and the
	// callback may repeat. UserInfoInvalidated is what makes the new address
	// observable on the very next request rather than after sessionCacheTTL,
	// because UserInfo.Email is cached.
	if !verificationRequired {
		s.lifecycle.UserInfoInvalidated(user.ID)
		slideElevation(ctx, s.store, userInfo, s.now())
		return connect.NewResponse(&leapmuxv1.RequestEmailChangeResponse{
			VerificationRequired: false,
		}), nil
	}

	// On a send failure the helper drops the undelivered code and keeps the
	// pending address, so Resend retries the same change; the failure
	// window's deadline is what the clear leaves behind.
	if sent, _ := deliverPendingEmailVerification(ctx, s.store, s.mail, s.renderer, pendingEmailDelivery{
		userID:          user.ID,
		email:           newEmail,
		code:            storedCode,
		mintUnblockedAt: mintedUnblockedAt,
		failureCooldown: mailFailureCooldown(ctx, s.set),
		now:             s.now,
	}); !sent {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("the hub could not send the verification email"))
	}

	slideElevation(ctx, s.store, userInfo, s.now())
	return connect.NewResponse(&leapmuxv1.RequestEmailChangeResponse{
		VerificationRequired: true,
	}), nil
}

// ResendVerificationEmail re-issues the verification mail for the
// session user's pending email. It is authenticated and restricted to users
// who actually have a pending row — there is nothing to re-send
// otherwise. The hub enforces the cooldown on the server; the frontend
// rate-limit UI is purely cosmetic.
func (s *UserService) ResendVerificationEmail(ctx context.Context, _ *connect.Request[leapmuxv1.ResendVerificationEmailRequest]) (*connect.Response[leapmuxv1.ResendVerificationEmailResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	full, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	targetEmail := full.PendingEmail
	if targetEmail == "" && full.Email != "" && !full.EmailVerified && settings.EmailVerificationEffective(s.set.Snapshot(ctx)) {
		// An administrator enabled SMTP after a signup with no verification: the
		// user has a primary email but never received a pending row. Seed one now.
		targetEmail = full.Email
	}
	if targetEmail == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
	}

	// The cooldown lives in the conditional mint inside
	// issuePendingEmailVerification, not in a read-then-check here: two
	// concurrent resends both passed a Go check and both sent.

	sent, nextResend, err := issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, full.ID, targetEmail, s.now,
		mailFailureCooldown(ctx, s.set))
	if err != nil {
		// A claimed address surfaces as AlreadyExists, not Internal: the
		// user can act on "email already in use", and the transport error
		// chain stays out of the response.
		return nil, AvailabilityConnectError(err)
	}
	// Advertise the deadline the gate enforces whichever way the send went:
	// after a successful send it derives from the mint's own issue instant,
	// and after a refused one from the failure stamp the clear writes -- a
	// response that reports no cooldown would invite the retry the hub then
	// refuses for the failure window.
	resp := &leapmuxv1.ResendVerificationEmailResponse{EmailSent: sent}
	if nextResend != nil {
		resp.NextResendAvailableAt = timestamppb.New(*nextResend)
	}
	return connect.NewResponse(resp), nil
}

// VerifyEmail handles both signup verification and email-change verification.
// Authenticated; this matches the verification code against the *session
// user's* pending row, so user B cannot redeem user A's code (the code
// simply does not exist for user B). See verifyPendingEmailToken for the
// per-user lookup, expiry/mismatch oracle handling, and rate limit.
func (s *UserService) VerifyEmail(ctx context.Context, req *connect.Request[leapmuxv1.VerifyEmailRequest]) (*connect.Response[leapmuxv1.VerifyEmailResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	updatedUser, err := verifyPendingEmailToken(ctx, s.store, userInfo.ID.String(), req.Msg.GetVerificationToken(), s.now())
	if err != nil {
		return nil, err
	}

	// Flush all sessions so every device the user is signed in on reads the
	// new Email + EmailVerified, not just the one that reached
	// /verify-email.
	s.lifecycle.UserInfoInvalidated(userInfo.ID.String())

	userProto := userToProtoWithPasskeys(ctx, s.store, updatedUser)

	return connect.NewResponse(&leapmuxv1.VerifyEmailResponse{
		User: userProto,
	}), nil
}

// ChangePassword sets or replaces the caller's password.
//
// It is REACHABLE IN SOLO MODE, unlike the account verbs around it, and that
// is the feature: the solo account's password is what lets the hub answer on a
// network address at all, so a hub that refused this could never be published.
// The other solo refusals stay -- there is still no sign-up, no passkey, no
// account recovery and no provider link.
func (s *UserService) ChangePassword(ctx context.Context, req *connect.Request[leapmuxv1.ChangePasswordRequest]) (*connect.Response[leapmuxv1.ChangePasswordResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	if err := validate.ValidatePassword(req.Msg.GetNewPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// runStepUpMutationTx hashes the new password and commits the write: the
	// admission (an elevated session, or the first-credential rule on an
	// account with nothing to elevate with) runs outside the user-auth
	// transaction, and so does the Argon2 hash, which must not hold the
	// database writer lock. An identity-less shell (no
	// password, no passkeys, no verified email, no OAuth link) may not
	// attach a first password with the session alone -- the same rule the
	// first-passkey path enforces.
	var hashed string
	prepare := func(*store.User) error {
		h, err := password.Hash(req.Msg.GetNewPassword())
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
		}
		hashed = h
		return nil
	}
	// The revocation runs AFTER the commit, and runStepUpMutationTx owns
	// that ordering for all three mutations that need it. The acting
	// credential survives at the new generation, which
	// revokeOtherCredentialsPreservingActingCredential restamped inside the
	// transaction, so the teardown of every older-generation lease and
	// channel cannot reach the surviving session's own live connections.
	//
	// Only the in-process path enforces that restamp-before-revoke ordering.
	// The same-process revocation watcher independently replays the
	// durable user_tokens event and also calls UserRevoked; that replay waits
	// for a publish sweep plus several DB round-trips, so it lands long
	// after this synchronous restamp. If it ever won the race it would tear
	// down the acting session's own connections -- but the session survives
	// durably at the new generation, so the client reconnects with its
	// still-valid cookie and rebuilds its context: a spurious transient
	// disconnect, never a lost revocation or a forced logout.
	if err := s.runStepUpMutationTx(ctx, entryStepUp(userInfo), prepare, func(tx store.Store, user *store.User) (stepUpMutationResult, error) {
		if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			PasswordHash: hashed,
			ID:           user.ID,
		}); err != nil {
			return stepUpMutationResult{}, fmt.Errorf("update password: %w", err)
		}
		gen, err := s.revokeOtherCredentialsPreservingActingCredential(ctx, tx, user.ID, userInfo.Credential)
		if err != nil {
			return stepUpMutationResult{}, err
		}
		return stepUpMutationResult{revokeOtherCredentials: true, authGeneration: gen}, nil
	}); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}

	resp := connect.NewResponse(&leapmuxv1.ChangePasswordResponse{})
	if err := s.handOverSoloSession(ctx, userInfo, resp.Header()); err != nil {
		return nil, err
	}
	return resp, nil
}

// handOverSoloSession gives a credential-free solo caller a real session,
// because the write it just made is what ended its credential-free access.
//
// A solo hub authenticates a TCP caller with nothing while the account holds
// no password. Storing the first password re-arms the rule against the very
// browser that stored it: without this, the response returns 200 and the next
// request from that page is Unauthenticated -- the user is signed out of the
// form they are standing in, having done nothing wrong.
//
// It runs only for a caller the solo rung admitted (SoloAuthenticated), so an
// ordinary session that changed its password is untouched, and it is a no-op
// outside solo mode.
//
// The session is ELEVATED, deliberately. This caller held unauthenticated full
// administrator access one request ago, so the grant cannot widen what it can
// do; refusing it would only ask the user to present, as a step-up proof, the
// secret they chose in the request before.
func (s *UserService) handOverSoloSession(ctx context.Context, userInfo *auth.UserInfo, h http.Header) error {
	if !userInfo.SoloAuthenticated() {
		return nil
	}
	// Before the session exists, so no window admits a credential-free caller
	// between the commit and the gate learning about it.
	s.soloGate.NotePasswordSet()

	sessionID, expiresAt, err := auth.CreateSession(ctx, s.store, userInfo.ID, settings.SessionDuration(s.set.Snapshot(ctx)))
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}
	if _, err := grantSessionElevation(ctx, s.store, s.lifecycle, sessionID, userInfo.ID, s.now()); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("elevate session: %w", err))
	}
	h.Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt,
		settings.KeySecureCookies.Of(s.set.Snapshot(ctx))).String())
	return nil
}

func (s *UserService) UnlinkOAuthProvider(ctx context.Context, req *connect.Request[leapmuxv1.UnlinkOAuthProviderRequest]) (*connect.Response[leapmuxv1.UnlinkOAuthProviderResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "provider links"); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	providerID := req.Msg.GetProviderId()
	if providerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider_id is required"))
	}

	// Detaching a provider removes a login method, and for an OAuth-only
	// account it removes the very factor that account elevates WITH. The
	// last-login-method guard below stops the account from becoming
	// unreachable, but it does not make this reversible: the owner cannot
	// re-attach the link without signing in through it. So this requires the
	// factor first, exactly as a password change does.
	if err := requireElevation(userInfo, s.now()); err != nil {
		return nil, err
	}

	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	links, err := s.store.OAuthUserLinks().ListByUser(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Verify the user actually has a link to this provider.
	found := false
	for _, link := range links {
		if link.ProviderID == providerID {
			found = true
			break
		}
	}
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no linked account for provider %q", providerID))
	}

	// ONE read of the enabled-provider set, for BOTH runs of the rule below.
	//
	// It runs OUTSIDE the transaction on purpose. RunInUserAuthTransaction
	// takes the SQLite writer lock at the start -- LockUserAuthState is a no-op
	// UPDATE for exactly that -- so a ListAll inside it makes every other
	// writer on the hub queue behind one more round trip. The lock protects
	// the account's own credential rows; it does not protect oauth_providers,
	// which only the administrator verbs write and none of them takes this
	// lock. So reading the set before the lock reads the same set.
	//
	// The RULE itself still re-runs under the lock, against the locked row
	// and the locked link list. That is the TOCTOU guard, and it stays.
	//
	// It runs UNCONDITIONALLY, although the rule returns before it reads the
	// map for an account that holds a password. One small read on a rare verb
	// gives one snapshot that both runs share. Reading it only for a
	// passwordless peek would leave the locked run with a nil map whenever
	// the two rows disagree, and a nil map counts every link as disabled --
	// a refusal the account did not earn.
	enabled, err := enabledProviderIDs(ctx, s.store)
	if err != nil {
		return nil, err
	}

	// The same rule the transaction below re-runs under the lock. Here it
	// answers the ordinary caller early, from reads it already holds, rather
	// than opening a transaction to refuse.
	if err := assertRemovingTheLinkLeavesALoginMethod(ctx, s.store, user, links, enabled, providerID); err != nil {
		return nil, err
	}

	rowUID, mintErr := mintRowUserID(user.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	// The rule and the write run in ONE user-auth transaction, and this
	// re-reads the acting authority inside it.
	//
	// Two races close at once. The rule above reads its link list before the
	// lock, so two concurrent requests each detaching one of the account's
	// last two providers both passed it and the account kept no
	// login method at all. And the gate read the elevation that admitted this
	// request from a cached UserInfo, so an administrator's revoke could be
	// seconds old -- detaching a linked provider removes a recovery identity
	// the owner cannot re-attach, which is why the window guards it.
	if err := s.store.RunInUserAuthTransaction(ctx, userInfo.ID, func(tx store.Store) error {
		// ONE read of the locked row, shared by both rules below. The actor
		// and the owner are the same account here -- this RPC only ever
		// detaches the caller's own link -- so a second read would fetch the
		// same row while holding the writer lock.
		lockedUser, err := tx.Users().GetByID(ctx, user.ID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		if err := refuseIfActingAuthorityMovedFrom(ctx, tx, lockedUser, userInfo, s.now()); err != nil {
			return err
		}
		lockedLinks, err := tx.OAuthUserLinks().ListByUser(ctx, userInfo.ID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		if err := assertRemovingTheLinkLeavesALoginMethod(ctx, tx, lockedUser, lockedLinks, enabled, providerID); err != nil {
			return err
		}
		if err := tx.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
			UserID:     rowUID,
			ProviderID: providerID,
		}); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	slideElevation(ctx, s.store, userInfo, s.now())
	return connect.NewResponse(&leapmuxv1.UnlinkOAuthProviderResponse{}), nil
}

// enabledProviderIDs reports whether each configured OAuth provider is
// currently enabled.
//
// ListAll is acceptable here for the reason GetCurrentUser gives for its own
// use of it: the number of configured providers is typically in the single
// digits, and adding a GetByIDs method to every backend is not worth the
// complexity.
func enabledProviderIDs(ctx context.Context, st store.Store) (map[string]bool, error) {
	rows, err := st.OAuthProviders().ListAll(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("list the configured OAuth providers: %w", err))
	}
	enabled := make(map[string]bool, len(rows))
	for _, row := range rows {
		enabled[row.ID] = row.Enabled
	}
	return enabled, nil
}

// assertRemovingTheLinkLeavesALoginMethod refuses an unlink that would leave
// the account with no way to sign in.
//
// A passkey is a login method like a password, so either one keeps the unlink
// allowed, and so does a second linked provider.
//
// It counts what would REMAIN rather than how many links exist. The rule is
// about the account AFTER the unlink, and "one link total" only happens to
// mean the same thing while the request specifies a link the account holds --
// which the locked re-run cannot assume, because the pre-lock check that
// established it ran against an older list.
//
// A link whose provider an administrator DISABLED is not a login method, and
// does not count. Nothing behind it works: loadEnabledProvider answers 403
// "provider disabled" at the login leg, the re-authentication leg and the
// callback alike. Counting it let an account with no password, no passkey and
// two links -- one live, one disabled -- remove the live one and become
// permanently unreachable, which is the exact outcome this refuses.
//
// It TAKES the link list rather than reading one, so the caller decides which
// snapshot the rule runs against: the pre-lock peek answers an ordinary
// refusal early, and the re-run inside the transaction is the one that
// decides.
//
// It TAKES the enabled-provider set for a different reason. That set is
// hub-wide state the user-auth lock does not protect, so both runs read one
// map that the caller fetched before the lock. Reading it here ran ListAll a
// second time while the request held the writer lock, for an answer the first
// read already had. The passkey COUNT below stays inside, because the lock DOES
// protect the account's passkeys, and the locked run must see them.
func assertRemovingTheLinkLeavesALoginMethod(
	ctx context.Context,
	st store.Store,
	user *store.User,
	links []store.OAuthUserLink,
	enabled map[string]bool,
	providerID string,
) error {
	if user.PasswordSet {
		return nil
	}
	remaining := 0
	for _, link := range links {
		if link.ProviderID != providerID && enabled[link.ProviderID] {
			remaining++
		}
	}
	if remaining > 0 {
		return nil
	}
	passkeys, err := st.PasskeyCredentials().CountByUser(ctx, user.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if passkeys > 0 {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		// "unlink", the word the button, the RPC and the docs all use. One
		// term per concept: a second word for this action would read as a
		// second action.
		fmt.Errorf("cannot unlink your only login method; set a password first"))
}

// userSettingValueToProto assembles one account setting's wire value from
// the raw blob document: value_json is the stored sub-document verbatim,
// effective_json the decoded value (which degrades a bad sub-document to
// the key's default), customized whether a sub-document exists at all.
func userSettingValueToProto(key string, raw json.RawMessage, decoded any, customized bool) *leapmuxv1.SettingValue {
	v := &leapmuxv1.SettingValue{
		Key:           key,
		EffectiveJson: marshalSettingJSON(decoded),
		Customized:    customized,
	}
	if customized {
		v.ValueJson = string(raw)
	}
	return v
}

// userSettingValue reads one key's current state from the blob. A key the
// registry does not know resolves to the zero state, which is what the
// caller's own unknown-key refusal already reported.
func userSettingValue(prefsJSON, key string) *leapmuxv1.SettingValue {
	state, _ := usersettings.Default.State(prefsJSON, key)
	return userSettingValueToProto(key, state.Raw, state.Value, state.Customized)
}

func (s *UserService) ListUserSettings(ctx context.Context, _ *connect.Request[leapmuxv1.ListUserSettingsRequest]) (*connect.Response[leapmuxv1.ListUserSettingsResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	prefs, err := s.store.Users().GetPrefs(ctx, userInfo.ID.String())
	if err != nil {
		// The same classification the write path uses. GetPrefs filters
		// `deleted_at IS NULL`, so a user soft-deleted while a session is
		// still live answers ErrNotFound — an ordinary deleted-account
		// condition, and it must not read as a hub fault on the read path
		// while the write path calls it NotFound.
		return nil, storeConnectError(err, "read preferences")
	}
	// One pass over the blob for every key. Resolving each key on its own
	// re-parsed the whole document once per key.
	states := usersettings.Default.States(prefs)

	descrs := make([]*leapmuxv1.SettingDescriptor, 0)
	vals := make([]*leapmuxv1.SettingValue, 0)
	for _, d := range usersettings.Default.Descriptors() {
		name := d.Name()
		state := states[name]
		descrs = append(descrs, settingDescriptorToProto(d))
		vals = append(vals, userSettingValueToProto(name, state.Raw, state.Value, state.Customized))
	}
	return connect.NewResponse(&leapmuxv1.ListUserSettingsResponse{
		Descriptors: descrs,
		Values:      vals,
	}), nil
}

// mutateUserPrefs runs one key-scoped mutation over the caller's prefs
// blob and answers with the key's new wire value.
//
// ONE transaction, row locked: read the blob, apply the mutation to the
// one key, write the whole blob back. The lock is what makes concurrent
// updates to different keys (two tabs, two devices) both survive. Update
// and Reset share it so a third mutation cannot arrive with a weaker lock
// or a missing error branch.
func (s *UserService) mutateUserPrefs(
	ctx context.Context,
	key string,
	mutate func(prefs string) (string, error),
) (*leapmuxv1.SettingValue, error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("key is required"))
	}

	var updated string
	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
		prefs, err := tx.Users().GetPrefsForUpdate(ctx, userInfo.ID.String())
		if err != nil {
			return err
		}
		next, err := mutate(prefs)
		if err != nil {
			return err
		}
		if err := tx.Users().UpdatePrefs(ctx, store.UpdateUserPrefsParams{
			Prefs: next,
			ID:    userInfo.ID.String(),
		}); err != nil {
			return err
		}
		updated = next
		return nil
	})
	if err != nil {
		var invalid *settings.InvalidError
		if errors.As(err, &invalid) {
			return nil, connect.NewError(connect.CodeInvalidArgument, invalid)
		}
		// Stored corruption, not caller input: say so rather than blaming
		// the request or answering with a bare 500.
		if errors.Is(err, usersettings.ErrMalformedBlob) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("stored preferences are malformed; contact support: %w", err))
		}
		// The store's own sentinels classify the same way here as on the
		// admin surface. A user soft-deleted while a session is still live
		// makes GetPrefsForUpdate answer ErrNotFound, which is an ordinary
		// deleted-account condition and not a hub fault.
		return nil, storeConnectError(err, "update preferences")
	}
	return userSettingValue(updated, key), nil
}

func (s *UserService) UpdateUserSetting(ctx context.Context, req *connect.Request[leapmuxv1.UpdateUserSettingRequest]) (*connect.Response[leapmuxv1.UpdateUserSettingResponse], error) {
	key := req.Msg.GetKey()
	value, err := s.mutateUserPrefs(ctx, key, func(prefs string) (string, error) {
		return usersettings.Default.ApplyPartial(prefs, key, json.RawMessage(req.Msg.GetPartialJson()))
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.UpdateUserSettingResponse{Value: value}), nil
}

func (s *UserService) ResetUserSetting(ctx context.Context, req *connect.Request[leapmuxv1.ResetUserSettingRequest]) (*connect.Response[leapmuxv1.ResetUserSettingResponse], error) {
	key := req.Msg.GetKey()
	value, err := s.mutateUserPrefs(ctx, key, func(prefs string) (string, error) {
		return usersettings.Default.Reset(prefs, key)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.ResetUserSettingResponse{Value: value}), nil
}

func (s *UserService) GetTimeouts(ctx context.Context, req *connect.Request[leapmuxv1.GetTimeoutsRequest]) (*connect.Response[leapmuxv1.GetTimeoutsResponse], error) {
	if _, err := auth.MustGetUser(ctx); err != nil {
		return nil, err
	}

	t := settings.KeyTimeouts.Of(s.set.Snapshot(ctx))
	// The narrowing conversions are safe because validateTimeouts caps
	// every budget at settings.MaxTimeoutSeconds, and the snapshot degrades
	// a value that fails validation to the default. Widen that cap and the
	// wire type has to widen with it.
	return connect.NewResponse(&leapmuxv1.GetTimeoutsResponse{
		ApiTimeoutSeconds:            int32(t.APITimeoutSeconds),
		AgentStartupTimeoutSeconds:   int32(t.AgentStartupTimeoutSeconds),
		WorktreeCreateTimeoutSeconds: int32(t.WorktreeCreateSecs),
	}), nil
}
