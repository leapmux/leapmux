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
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/util/validate"
)

// storedPreferences maps to the JSON blob stored in user_preferences.prefs.
type storedPreferences struct {
	Theme                 string   `json:"theme,omitempty"`
	TerminalTheme         string   `json:"terminalTheme,omitempty"`
	UIFontCustomEnabled   bool     `json:"uiFontCustomEnabled,omitempty"`
	MonoFontCustomEnabled bool     `json:"monoFontCustomEnabled,omitempty"`
	UIFonts               []string `json:"uiFonts,omitempty"`
	MonoFonts             []string `json:"monoFonts,omitempty"`
	DiffView              int      `json:"diffView,omitempty"`
	TurnEndSound          int      `json:"turnEndSound,omitempty"`
	TurnEndSoundVolume    *int     `json:"turnEndSoundVolume,omitempty"`
	DebugLogging          bool     `json:"debugLogging,omitempty"`
	CustomKeybindingsJSON string   `json:"customKeybindingsJSON,omitempty"`
}

// maxCustomKeybindings is the maximum number of keybinding overrides allowed.
const maxCustomKeybindings = 200

// maxKeybindingFieldLen is the maximum length of any single field in a keybinding override.
const maxKeybindingFieldLen = 256

// customKeybindingEntry matches the expected shape of each element in the
// custom keybindings JSON array.
type customKeybindingEntry struct {
	Key     string `json:"key"`
	Command string `json:"command"`
	When    string `json:"when,omitempty"`
}

// validateCustomKeybindingsJSON validates the custom keybindings JSON string.
// Returns an error if the JSON is invalid or exceeds limits.
// An empty string or "[]" is always valid.
func validateCustomKeybindingsJSON(raw string) error {
	if raw == "" || raw == "[]" {
		return nil
	}

	var entries []customKeybindingEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	if len(entries) > maxCustomKeybindings {
		return fmt.Errorf("too many keybinding overrides: %d (max %d)", len(entries), maxCustomKeybindings)
	}

	for i, e := range entries {
		if e.Command == "" {
			return fmt.Errorf("entry %d: command is required", i)
		}
		if len(e.Key) > maxKeybindingFieldLen {
			return fmt.Errorf("entry %d: key too long (%d > %d)", i, len(e.Key), maxKeybindingFieldLen)
		}
		if len(e.Command) > maxKeybindingFieldLen {
			return fmt.Errorf("entry %d: command too long (%d > %d)", i, len(e.Command), maxKeybindingFieldLen)
		}
		if len(e.When) > maxKeybindingFieldLen {
			return fmt.Errorf("entry %d: when too long (%d > %d)", i, len(e.When), maxKeybindingFieldLen)
		}
	}

	return nil
}

// UserService implements the leapmux.v1.UserService ConnectRPC handler.
type UserService struct {
	store     store.Store
	cfg       *config.Config
	lifecycle *auth.CredentialLifecycleEffects
	mail      mail.Sender
	renderer  mail.Renderer
}

// NewUserService creates a new UserService. renderer carries the hub's
// public URL used to build absolute deep-links in the verification
// emails sent on email-change and resend.
func NewUserService(st store.Store, cfg *config.Config, lifecycle *auth.CredentialLifecycleEffects, sender mail.Sender, renderer mail.Renderer) *UserService {
	if lifecycle == nil {
		panic("user service requires credential lifecycle effects")
	}
	return &UserService{store: st, cfg: cfg, lifecycle: lifecycle, mail: sender, renderer: renderer}
}

func (s *UserService) UpdateProfile(ctx context.Context, req *connect.Request[leapmuxv1.UpdateProfileRequest]) (*connect.Response[leapmuxv1.UpdateProfileResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("profile changes are not available in solo mode"))
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.store.Users().GetByID(ctx, userInfo.ID)
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

	// Users().UpdateProfile is self-transactional: it pairs UpdateUserProfile
	// with RenameUserPersonalOrg in one store transaction, so a username change
	// can never leave the /o/ slug stale -- and a future store-level caller
	// that changes the username cannot reintroduce the bug by skipping this
	// service method. No outer transaction is needed here; it would wrap a
	// single already-atomic statement.
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

	resp := &leapmuxv1.UpdateProfileResponse{
		Username:     newUsername,
		DisplayName:  displayName,
		Email:        user.Email,
		PendingEmail: user.PendingEmail,
	}

	if usernameChanged {
		resp.OrgName = newUsername
	}

	return connect.NewResponse(resp), nil
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

	user, err := s.store.Users().GetByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if newEmail == user.Email {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("email is unchanged"))
	}

	// Check that no other user has this email.
	if err := CheckEmailAvailable(ctx, s.store, newEmail, user.ID); err != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	}

	// Immediate change with no verification round-trip: an admin edit is
	// trusted-verified, and a non-admin edit made when verification isn't
	// required lands unverified (verified == userInfo.IsAdmin). Admin is checked
	// first via the disjunct, so an admin under a verification-required
	// deployment still gets a trusted immediate change. Both flush cached
	// UserInfo (UserInfo.Email is cached) so the new value is observable on the
	// very next request rather than after sessionCacheTTL.
	if userInfo.IsAdmin || !s.cfg.EmailVerificationRequired {
		if err := SetEmailAndClearCompeting(ctx, s.store, user.ID, newEmail, userInfo.IsAdmin); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		s.lifecycle.UserInfoInvalidated(user.ID)
		return connect.NewResponse(&leapmuxv1.RequestEmailChangeResponse{
			VerificationRequired: false,
		}), nil
	}

	// Non-admin, verification required: set pending email and dispatch
	// the verification mail. On send failure the helper rolls back the
	// row so the user can retry from a clean slate.
	if err := issuePendingEmailVerificationOrRollback(ctx, s.store, s.mail, s.renderer, user.ID, newEmail); err != nil {
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

// ResendVerificationEmail re-issues the verification mail for the
// session user's pending email. It's authenticated and gated to users
// who actually have a pending row — there's nothing to re-send
// otherwise. Cooldown is enforced server-side; frontend rate-limit UI
// is purely cosmetic.
func (s *UserService) ResendVerificationEmail(ctx context.Context, _ *connect.Request[leapmuxv1.ResendVerificationEmailRequest]) (*connect.Response[leapmuxv1.ResendVerificationEmailResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	full, err := s.store.Users().GetByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if full.PendingEmail == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
	}

	// Refuse if the previous code was issued less than the cooldown ago.
	// The TTL is constant, so the "issued at" time can be reconstructed
	// from expires_at. A nil expires_at means no live row to throttle
	// against — fall through.
	if full.PendingEmailExpiresAt != nil {
		issuedAt := full.PendingEmailExpiresAt.Add(-pendingEmailExpiry)
		if time.Since(issuedAt) < resendVerificationCooldown {
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("please wait before requesting another verification email"))
		}
	}

	sent, err := issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, full.ID, full.PendingEmail)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.ResendVerificationEmailResponse{
		EmailSent: sent,
	}), nil
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

	updatedUser, err := verifyPendingEmailToken(ctx, s.store, userInfo.ID, req.Msg.GetVerificationToken())
	if err != nil {
		return nil, err
	}

	// Flush all sessions so the new Email + EmailVerified are picked up
	// across every device the user is signed in on, not just the one
	// that hit /verify-email.
	s.lifecycle.UserInfoInvalidated(userInfo.ID)

	org, err := s.store.Orgs().GetByID(ctx, updatedUser.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.VerifyEmailResponse{
		User: userToProtoWithOrgName(updatedUser, org.Name),
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

	hashed, err := password.Hash(req.Msg.GetNewPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	var user *store.User
	var committedAuthGeneration int64
	if err := s.store.RunInUserAuthTransaction(ctx, userInfo.ID, func(tx store.Store) error {
		var err error
		user, err = tx.Users().GetByID(ctx, userInfo.ID)
		if err != nil {
			return fmt.Errorf("query user: %w", err)
		}
		// OAuth-only users can set a password without a current one. For
		// password users, verify while holding the same auth-state lock used
		// by login so the checked hash cannot change before commit.
		if user.PasswordSet {
			match, err := password.Verify(user.PasswordHash, req.Msg.GetCurrentPassword())
			if err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("verify password: %w", err))
			}
			if !match {
				return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("current password is incorrect"))
			}
		}
		if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			PasswordHash: hashed,
			ID:           user.ID,
		}); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		sessionID := userInfo.Credential.SessionID()
		if err := tx.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
			UserID: user.ID,
			KeepID: sessionID,
		}); err != nil {
			return fmt.Errorf("delete other sessions: %w", err)
		}
		if _, _, err := auth.RevokeAllUserCredentials(ctx, tx, user.ID); err != nil {
			return err
		}
		if sessionID != "" {
			n, err := tx.Sessions().RefreshAuthGeneration(ctx, store.RefreshSessionAuthGenerationParams{
				SessionID: sessionID,
				UserID:    user.ID,
			})
			if err != nil {
				return fmt.Errorf("refresh current session auth generation: %w", err)
			}
			// n==0 means the acting session was concurrently deleted (a
			// same-user logout / admin force-logout does not contend on this
			// user-auth lock) after the tx began. The password change itself is
			// valid and there is no surviving session row left to restamp, so do
			// not roll the whole change back. The post-tx restamp is a true no-op
			// for a same-process logout (which already tore down this Hub's
			// in-memory leases/channels for the session); if the logout happened
			// on another Hub, this Hub may still hold the deleted session's
			// in-memory holders until the durable session-revoked event replays,
			// and the restamp briefly re-stamps those before the following
			// UserRevoked and the replayed SessionRevoked tear them down -- benign
			// and self-healing. n>1 is impossible (session id is unique) and
			// indicates corruption, so it stays fatal.
			if n > 1 {
				return fmt.Errorf("refresh current session auth generation: updated %d rows", n)
			}
		}
		updatedUser, err := tx.Users().GetByID(ctx, user.ID)
		if err != nil {
			return fmt.Errorf("query updated user auth generation: %w", err)
		}
		committedAuthGeneration = updatedUser.AuthGeneration
		return nil
	}); err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// The acting session survives at the new generation (RefreshAuthGeneration
	// above), so re-stamp both its leases and its channels to that generation
	// before the user-wide revocation below -- which cancels older-generation
	// leases and closes older-generation channels -- would otherwise tear down
	// the surviving session's own live WebSocket connections and channels.
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
	// Restamp the acting session (empty SessionID for a non-cookie caller, which
	// then only revokes) before evicting every credential older than the committed
	// generation; RevokeUserPreservingSession enforces that preserve-before-revoke
	// ordering in one call. A concurrent login committed afterward already belongs
	// to this generation and survives.
	s.lifecycle.RevokeUserPreservingSession(user.ID, userInfo.Credential.SessionID(), committedAuthGeneration)

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

	user, err := s.store.Users().GetByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	links, err := s.store.OAuthUserLinks().ListByUser(ctx, user.ID)
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

	// Guard: cannot unlink the last provider if the user has no password set.
	if len(links) <= 1 && !user.PasswordSet {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot unlink your only login method; set a password first"))
	}

	if err := s.store.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
		UserID:     user.ID,
		ProviderID: providerID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.UnlinkOAuthProviderResponse{}), nil
}

// preferencesToProto maps a stored preference record to its proto form. Shared
// by GetPreferences and UpdatePreferences so the update response echoes exactly
// what was persisted -- the SANITIZED theme/terminalTheme and the round-tripped
// scalar fields -- rather than the raw request values (which would report an
// unsanitized theme the store never kept, drifting from the next GetPreferences).
func preferencesToProto(sp storedPreferences) *leapmuxv1.UserPreferences {
	return &leapmuxv1.UserPreferences{
		Theme:                 sp.Theme,
		TerminalTheme:         sp.TerminalTheme,
		UiFontCustomEnabled:   sp.UIFontCustomEnabled,
		MonoFontCustomEnabled: sp.MonoFontCustomEnabled,
		UiFonts:               sp.UIFonts,
		MonoFonts:             sp.MonoFonts,
		DiffView:              leapmuxv1.DiffView(sp.DiffView),
		TurnEndSound:          leapmuxv1.TurnEndSound(sp.TurnEndSound),
		TurnEndSoundVolume:    ptrconv.Convert[int, uint32](sp.TurnEndSoundVolume),
		DebugLogging:          sp.DebugLogging,
		CustomKeybindingsJson: sp.CustomKeybindingsJSON,
	}
}

func (s *UserService) GetPreferences(ctx context.Context, req *connect.Request[leapmuxv1.GetPreferencesRequest]) (*connect.Response[leapmuxv1.GetPreferencesResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	prefs, err := s.store.Users().GetPrefs(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var sp storedPreferences
	if err := json.Unmarshal([]byte(prefs), &sp); err != nil {
		sp = storedPreferences{}
	}

	return connect.NewResponse(&leapmuxv1.GetPreferencesResponse{
		Preferences: preferencesToProto(sp),
	}), nil
}

func (s *UserService) GetTimeouts(ctx context.Context, req *connect.Request[leapmuxv1.GetTimeoutsRequest]) (*connect.Response[leapmuxv1.GetTimeoutsResponse], error) {
	if _, err := auth.MustGetUser(ctx); err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.GetTimeoutsResponse{
		ApiTimeoutSeconds:            int32(s.cfg.APITimeout().Seconds()),
		AgentStartupTimeoutSeconds:   int32(s.cfg.AgentStartupTimeout().Seconds()),
		WorktreeCreateTimeoutSeconds: int32(s.cfg.WorktreeCreateTimeout().Seconds()),
	}), nil
}

// GetUser resolves a minimal user record (id, org_id, username) for
// the caller or for another member of the caller's org. The
// `leapmux remote` CLI universal resolver uses this to derive
// org_id from user_id when scripts pass `--user-id`. Cross-org
// lookups collapse to NotFound rather than PermissionDenied so we
// don't leak the existence of users in other orgs.
func (s *UserService) GetUser(ctx context.Context, req *connect.Request[leapmuxv1.GetUserRequest]) (*connect.Response[leapmuxv1.GetUserResponse], error) {
	caller, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	target := req.Msg.GetUserId()
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user_id is required"))
	}
	// Self-lookups skip the org check (no cross-tenant concern).
	if target == caller.ID {
		return connect.NewResponse(&leapmuxv1.GetUserResponse{
			UserId:   caller.ID,
			OrgId:    caller.OrgID,
			Username: caller.Username,
		}), nil
	}
	u, err := s.store.Users().GetByID(ctx, target)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get user: %w", err))
	}
	if u.OrgID != caller.OrgID {
		// Cross-tenant: collapse to NotFound rather than leaking existence.
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
	}
	return connect.NewResponse(&leapmuxv1.GetUserResponse{
		UserId:   u.ID,
		OrgId:    u.OrgID,
		Username: u.Username,
	}), nil
}

func (s *UserService) UpdatePreferences(ctx context.Context, req *connect.Request[leapmuxv1.UpdatePreferencesRequest]) (*connect.Response[leapmuxv1.UpdatePreferencesResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	// Sanitize and validate font names.
	uiFonts := req.Msg.GetUiFonts()
	for i, name := range uiFonts {
		sanitized, err := validate.SanitizeName(name)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid UI font name %q: %w", name, err))
		}
		uiFonts[i] = sanitized
	}
	monoFonts := req.Msg.GetMonoFonts()
	for i, name := range monoFonts {
		sanitized, err := validate.SanitizeName(name)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid mono font name %q: %w", name, err))
		}
		monoFonts[i] = sanitized
	}

	theme := req.Msg.GetTheme()
	if theme != "" {
		theme, err = validate.SanitizeSlug("theme", theme)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	terminalTheme := req.Msg.GetTerminalTheme()
	if terminalTheme != "" {
		terminalTheme, err = validate.SanitizeSlug("terminal theme", terminalTheme)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

	// Validate custom keybindings JSON if provided.
	customKeybindingsJSON := ""
	if req.Msg.CustomKeybindingsJson != nil {
		customKeybindingsJSON = *req.Msg.CustomKeybindingsJson
		if err := validateCustomKeybindingsJSON(customKeybindingsJSON); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("custom_keybindings_json: %w", err))
		}
	} else {
		// Preserve existing value when the field is not provided.
		existing, err := s.store.Users().GetPrefs(ctx, userInfo.ID)
		if err == nil {
			var prev storedPreferences
			if json.Unmarshal([]byte(existing), &prev) == nil {
				customKeybindingsJSON = prev.CustomKeybindingsJSON
			}
		}
	}

	sp := storedPreferences{
		Theme:                 theme,
		TerminalTheme:         terminalTheme,
		UIFontCustomEnabled:   req.Msg.GetUiFontCustomEnabled(),
		MonoFontCustomEnabled: req.Msg.GetMonoFontCustomEnabled(),
		UIFonts:               uiFonts,
		MonoFonts:             monoFonts,
		DiffView:              int(req.Msg.GetDiffView()),
		TurnEndSound:          int(req.Msg.GetTurnEndSound()),
		TurnEndSoundVolume:    ptrconv.Convert[uint32, int](req.Msg.TurnEndSoundVolume),
		DebugLogging:          req.Msg.GetDebugLogging(),
		CustomKeybindingsJSON: customKeybindingsJSON,
	}

	prefsJSON, err := json.Marshal(sp)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal prefs: %w", err))
	}

	if err := s.store.Users().UpdatePrefs(ctx, store.UpdateUserPrefsParams{
		Prefs: string(prefsJSON),
		ID:    userInfo.ID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Echo the persisted record, not the raw request: the response must report the
	// SANITIZED theme/terminalTheme that were actually stored (SanitizeSlug above),
	// so a client re-reading preferences sees the same values it was just handed.
	return connect.NewResponse(&leapmuxv1.UpdatePreferencesResponse{
		Preferences: preferencesToProto(sp),
	}), nil
}
