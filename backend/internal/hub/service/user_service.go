package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (s *UserService) UpdateProfile(ctx context.Context, req *connect.Request[leapmuxv1.UpdateProfileRequest]) (*connect.Response[leapmuxv1.UpdateProfileResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("profile changes are not available in solo mode"))
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

	// If the username is changing, check that the new one is not already taken.
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
	// store's gated durable event so both invalidation paths agree.
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
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("email changes are not available in solo mode"))
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

	// Immediate change with no verification round-trip: an admin edit is
	// trusted-verified, and a non-admin edit made when verification isn't
	// required lands unverified (verified == userInfo.IsAdmin). Admin is checked
	// first via the disjunct, so an admin under a verification-required
	// deployment still gets a trusted immediate change. Both flush cached
	// UserInfo (UserInfo.Email is cached) so the new value is observable on the
	// very next request rather than after sessionCacheTTL.
	if userInfo.IsAdmin || !settings.EmailVerificationEffective(s.set.Snapshot(ctx)) {
		if err := SetEmailAndClearCompeting(ctx, s.store, user.ID, newEmail, userInfo.IsAdmin); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		s.lifecycle.UserInfoInvalidated(user.ID)
		return connect.NewResponse(&leapmuxv1.RequestEmailChangeResponse{
			VerificationRequired: false,
		}), nil
	}

	// Non-admin, verification required: set pending email and dispatch
	// the verification mail. On send failure the helper drops the
	// undelivered code and keeps the pending address, so Resend retries
	// the same change.
	if err := issuePendingEmailVerificationOrFail(ctx, s.store, s.mail, s.renderer, user.ID, newEmail); err != nil {
		// The conditional mint refuses inside the cooldown, which is the
		// one thing that stops this RPC from being an open relay: the
		// address is caller-supplied, so an unconditional mint sends a
		// message per request to any address the caller names.
		if errors.Is(err, ErrVerificationCooldown) {
			return nil, connect.NewError(connect.CodeResourceExhausted, err)
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	return connect.NewResponse(&leapmuxv1.RequestEmailChangeResponse{
		VerificationRequired: true,
	}), nil
}

// resendVerificationCooldown caps how often a user can ask the hub to
// regenerate-and-resend their pending-email verification. Without this,
// nothing stops a logged-in user from spamming their own (or someone
// else's, via email-change) inbox. The cooldown is derived against the
// previous code's expires_at — since the TTL is constant, "issued_at"
// is just expires_at - pendingEmailExpiry.
const resendVerificationCooldown = 60 * time.Second

// issuedAtFromExpiry reconstructs when a pending token was issued from the
// expiry it set and the constant TTL used to mint it. SetPendingEmail and
// SetPendingPasswordReset store no issued-at, so every cooldown derivation
// goes through here: one spelling of the expiry-minus-TTL trick, paired
// with the cooldown constant below.
func issuedAtFromExpiry(expiresAt time.Time, ttl time.Duration) time.Time {
	return expiresAt.Add(-ttl)
}

// nextResendAt seeds the next-resend timestamp a successful send returns.
func nextResendAt(issuedAt time.Time) time.Time {
	return issuedAt.Add(resendVerificationCooldown)
}

// ResendVerificationEmail re-issues the verification mail for the
// session user's pending email. It's authenticated and restricted to users
// who actually have a pending row — there's nothing to re-send
// otherwise. Cooldown is enforced server-side; frontend rate-limit UI
// is purely cosmetic.
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
		// SMTP was enabled after signup without verification: the user has a
		// primary email but never received a pending row. Seed one now.
		targetEmail = full.Email
	}
	if targetEmail == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
	}

	// The cooldown lives in the conditional mint inside
	// issuePendingEmailVerification, not in a read-then-check here: two
	// concurrent resends both passed a Go check and both sent.

	sent, err := issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, full.ID, targetEmail)
	if err != nil {
		// A claimed address surfaces as AlreadyExists, not Internal: the
		// user can act on "email already in use", and the transport error
		// chain stays out of the response.
		return nil, AvailabilityConnectError(err)
	}
	// Advertise a cooldown only after a successful send: a failed send
	// leaves no live code, so blocking the retry the failure message
	// invites would contradict the response.
	resp := &leapmuxv1.ResendVerificationEmailResponse{EmailSent: sent}
	if sent {
		resp.NextResendAvailableAt = timestamppb.New(nextResendAt(time.Now().UTC()))
	}
	return connect.NewResponse(resp), nil
}

// VerifyEmail handles both signup verification and email-change verification.
// Authenticated; the verification code is matched against the *session
// user's* pending row, so user B cannot redeem user A's code (the code
// simply doesn't exist for user B). See verifyPendingEmailToken for the
// per-user lookup, expiry/mismatch oracle handling, and rate limit.
func (s *UserService) VerifyEmail(ctx context.Context, req *connect.Request[leapmuxv1.VerifyEmailRequest]) (*connect.Response[leapmuxv1.VerifyEmailResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	updatedUser, err := verifyPendingEmailToken(ctx, s.store, userInfo.ID.String(), req.Msg.GetVerificationToken())
	if err != nil {
		return nil, err
	}

	// Flush all sessions so the new Email + EmailVerified are picked up
	// across every device the user is signed in on, not just the one
	// that hit /verify-email.
	s.lifecycle.UserInfoInvalidated(userInfo.ID.String())

	userProto := userToProtoWithPasskeys(ctx, s.store, updatedUser)

	return connect.NewResponse(&leapmuxv1.VerifyEmailResponse{
		User: userProto,
	}), nil
}

func (s *UserService) ChangePassword(ctx context.Context, req *connect.Request[leapmuxv1.ChangePasswordRequest]) (*connect.Response[leapmuxv1.ChangePasswordResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("password changes are not available in solo mode"))
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	if err := validate.ValidatePassword(req.Msg.GetNewPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// The step-up credential is verified, the new password hashed, and the
	// write committed through runPasskeyManagementTx: admission runs outside
	// the user-auth transaction (Argon2 must not hold the database writer
	// lock), the locked row is re-checked before it writes, and the reauth
	// proof is consumed inside the transaction after the write. An
	// identity-less shell (no password, no passkeys, no verified email, no
	// OAuth link) may not attach a first password with the session alone --
	// the same rule the first-passkey path enforces.
	var hashed string
	prepare := func(*store.User) error {
		h, err := password.Hash(req.Msg.GetNewPassword())
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
		}
		hashed = h
		return nil
	}
	var committedAuthGeneration int64
	if err := s.runPasskeyManagementTx(ctx, userInfo, req.Msg.GetCurrentPassword(), req.Msg.GetReauthProof(), prepare, func(tx store.Store, user *store.User) error {
		if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			PasswordHash: hashed,
			ID:           user.ID,
		}); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		gen, err := s.revokeOtherCredentialsPreservingSession(ctx, tx, user.ID, userInfo.Credential.SessionID())
		if err != nil {
			return err
		}
		committedAuthGeneration = gen
		return nil
	}); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}

	// The acting session survives at the new generation
	// (revokeOtherCredentialsPreservingSession restamped it inside the
	// transaction), so re-stamp both its leases and its channels to that
	// generation before the user-wide revocation below -- which cancels
	// older-generation leases and closes older-generation channels -- would
	// otherwise tear down the surviving session's own live WebSocket
	// connections and channels.
	//
	// This restamp-before-revoke ordering is enforced only on the in-process
	// path. The same-process revocation watcher independently replays the durable
	// user_tokens event and also calls UserRevoked; that replay is gated on a
	// publish sweep plus several DB round-trips, so it lands long after this
	// synchronous restamp. If it ever won the race it would tear down the acting
	// session's own connections -- but the session survives durably at the new
	// generation, so the client reconnects with its still-valid cookie and
	// rebuilds its context: a spurious transient disconnect, never a lost
	// revocation or a forced logout.
	s.lifecycle.RevokeUserPreservingSession(userInfo.ID.String(), userInfo.Credential.SessionID(), committedAuthGeneration)

	return connect.NewResponse(&leapmuxv1.ChangePasswordResponse{}), nil
}

func (s *UserService) UnlinkOAuthProvider(ctx context.Context, req *connect.Request[leapmuxv1.UnlinkOAuthProviderRequest]) (*connect.Response[leapmuxv1.UnlinkOAuthProviderResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("not available in solo mode"))
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	providerID := req.Msg.GetProviderId()
	if providerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider_id is required"))
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

	// Guard: cannot unlink the last provider when the user has no other
	// login method. A passkey is a login method like a password, so it
	// keeps the unlink allowed.
	if len(links) <= 1 && !user.PasswordSet {
		passkeys, err := s.store.PasskeyCredentials().CountByUser(ctx, user.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if passkeys == 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot unlink your only login method; set a password first"))
		}
	}

	unlinkUID, mintErr := mintRowUserID(user.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	if err := s.store.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
		UserID:     unlinkUID,
		ProviderID: providerID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.UnlinkOAuthProviderResponse{}), nil
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
