package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/util/validate"
	"github.com/leapmux/leapmux/util/version"
)

// AuthService implements the leapmux.v1.AuthService ConnectRPC handler.
type AuthService struct {
	store        store.Store
	cfg          *config.Config
	sessionCache *auth.SessionCache
	keystore     *keystore.Keystore
	mail         mail.Sender
	hasAnyUser   atomic.Bool // one-way latch: once true, never re-queried
}

// NewAuthService creates a new AuthService.
func NewAuthService(st store.Store, cfg *config.Config, sc *auth.SessionCache, ks *keystore.Keystore, sender mail.Sender) *AuthService {
	return &AuthService{store: st, cfg: cfg, sessionCache: sc, keystore: ks, mail: sender}
}

// checkHasAnyUser returns true if at least one user exists. The result is
// cached with a one-way latch: once true, the DB is never queried again
// (users cannot be un-created).
func (s *AuthService) checkHasAnyUser(ctx context.Context) (bool, error) {
	if s.hasAnyUser.Load() {
		return true, nil
	}
	v, err := s.store.Users().HasAny(ctx)
	if err != nil {
		return false, err
	}
	if v {
		s.hasAnyUser.Store(true)
		return true, nil
	}
	return false, nil
}

func (s *AuthService) Login(ctx context.Context, req *connect.Request[leapmuxv1.LoginRequest]) (*connect.Response[leapmuxv1.LoginResponse], error) {
	token, user, expiresAt, err := auth.Login(ctx, s.store, req.Msg.GetUsername(), req.Msg.GetPassword())
	if err != nil {
		return nil, err
	}

	org, err := s.store.Orgs().GetByID(ctx, user.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&leapmuxv1.LoginResponse{
		User: userToProtoWithOrgName(user, org.Name),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(token, expiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

func (s *AuthService) Logout(ctx context.Context, req *connect.Request[leapmuxv1.LogoutRequest]) (*connect.Response[leapmuxv1.LogoutResponse], error) {
	token := auth.SessionIDFromHeader(req.Header().Get("Cookie"), s.cfg.SecureCookies)
	if token != "" {
		_, _ = s.store.Sessions().Delete(ctx, token)
		s.sessionCache.Evict(token)
	}
	resp := connect.NewResponse(&leapmuxv1.LogoutResponse{})
	resp.Header().Set("Set-Cookie", auth.ClearSessionCookie(s.cfg.SecureCookies).String())
	return resp, nil
}

func (s *AuthService) GetCurrentUser(ctx context.Context, req *connect.Request[leapmuxv1.GetCurrentUserRequest]) (*connect.Response[leapmuxv1.GetCurrentUserResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.store.Users().GetByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	org, err := s.store.Orgs().GetByID(ctx, user.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var linkedProviders []*leapmuxv1.LinkedOAuthProvider
	links, _ := s.store.OAuthUserLinks().ListByUser(ctx, user.ID)
	if len(links) > 0 {
		// ListAll is acceptable here: the number of configured OAuth
		// providers is typically in the single digits, and adding a
		// GetByIDs method to every backend is not worth the complexity.
		providers, _ := s.store.OAuthProviders().ListAll(ctx)
		providerNames := make(map[string]string, len(providers))
		for _, p := range providers {
			providerNames[p.ID] = p.Name
		}
		for _, link := range links {
			name, ok := providerNames[link.ProviderID]
			if !ok {
				continue
			}
			linkedProviders = append(linkedProviders, &leapmuxv1.LinkedOAuthProvider{
				Id:   link.ProviderID,
				Name: name,
			})
		}
	}

	return connect.NewResponse(&leapmuxv1.GetCurrentUserResponse{
		User: userToProtoWithOAuth(user, org.Name, linkedProviders),
	}), nil
}

func (s *AuthService) SignUp(ctx context.Context, req *connect.Request[leapmuxv1.SignUpRequest]) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is not available in solo mode"))
	}

	// The first user is always created as an admin, regardless of whether
	// signup is enabled globally.
	hasUser, err := s.checkHasAnyUser(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
	}
	isSetupMode := !hasUser
	if !isSetupMode && !s.cfg.SignupEnabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}

	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// `solo` is rejected in every mode: if a non-solo data-dir ever gets opened
	// in solo mode, the interceptor auto-authenticates every request as that
	// user. `admin` is allowed in setup mode so the first operator can
	// legitimately claim it; in public signup it's squat-protected.
	if usernames.IsReservedSystem(username) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is a reserved username", username))
	}
	if !isSetupMode && usernames.IsReservedPublic(username) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is a reserved username", username))
	}
	displayName, err := validate.SanitizeDisplayName(req.Msg.GetDisplayName(), username)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display name: %w", err))
	}
	pw := req.Msg.GetPassword()
	if err := validate.ValidatePassword(pw); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err := checkUsernameAvailable(ctx, s.store, username); err != nil {
		return nil, err
	}

	email := req.Msg.GetEmail()
	if err := validate.ValidateEmail(email); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hash, err := pwdhash.Hash(pw)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	if isSetupMode {
		return s.signUpSetupMode(ctx, username, displayName, email, hash)
	}

	if s.cfg.EmailVerificationRequired && email != "" {
		// Email goes to pending_email; email column stays empty until verified.
		if err := CheckEmailAvailable(ctx, s.store, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}

		user, err := CreateUserWithOrg(ctx, s.store, CreateUserParams{
			Username:     username,
			PasswordHash: hash,
			DisplayName:  displayName,
			Email:        "", // email goes to pending_email
			PasswordSet:  true,
			IsAdmin:      false,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		// Issue the verification email. Failure does NOT roll back the
		// user: signup succeeds and the frontend surfaces a Resend prompt
		// driven by verification_email_sent=false. The pending row stays
		// in place so a future Resend can reuse the same address slot.
		emailSent, err := issuePendingEmailVerification(ctx, s.store, s.mail, user.ID, email)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if !emailSent {
			slog.Warn("verification email send failed during signup",
				"user_id", user.ID,
			)
		}

		// Re-fetch so the User proto reflects the just-set pending fields.
		updatedUser, err := s.store.Users().GetByID(ctx, user.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		// Always create the session — without it, the user can't call the
		// authenticated VerifyEmail RPC.
		sessionID, sessionExpires, err := auth.CreateSession(ctx, s.store, user.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
		}
		resp := connect.NewResponse(&leapmuxv1.SignUpResponse{
			User:                  userToProtoWithOrgName(updatedUser, username),
			VerificationRequired:  true,
			VerificationEmailSent: emailSent,
		})
		resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, sessionExpires, s.cfg.SecureCookies).String())
		return resp, nil
	}

	// No verification required — email goes directly to email column.
	if email != "" {
		if err := CheckEmailAvailable(ctx, s.store, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
	}

	user, err := CreateUserWithOrg(ctx, s.store, CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  displayName,
		Email:        email,
		PasswordSet:  true,
		IsAdmin:      false,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return s.signUpResponse(ctx, user, username)
}

// signUpSetupMode handles the initial admin account creation when no users
// exist yet. The first user is always an admin with a verified email.
func (s *AuthService) signUpSetupMode(ctx context.Context, username, displayName, email, passwordHash string) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	// Re-check to handle race condition where another request created a user
	// between the initial check and now.
	hasUser, err := s.store.Users().HasAny(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
	}
	if hasUser {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}

	if email != "" {
		if err := CheckEmailAvailable(ctx, s.store, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
	}

	user, err := CreateUserWithOrg(ctx, s.store, CreateUserParams{
		Username:      username,
		PasswordHash:  passwordHash,
		DisplayName:   displayName,
		Email:         email,
		EmailVerified: email != "",
		PasswordSet:   true,
		IsAdmin:       true,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	s.hasAnyUser.Store(true)
	return s.signUpResponse(ctx, user, username)
}

// signUpResponse creates a session, sets the cookie, and returns the SignUpResponse.
func (s *AuthService) signUpResponse(ctx context.Context, user *store.User, orgName string) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	sessionID, expiresAt, err := auth.CreateSession(ctx, s.store, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}

	resp := connect.NewResponse(&leapmuxv1.SignUpResponse{
		User: userToProtoWithOrgName(user, orgName),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

func (s *AuthService) GetSystemInfo(ctx context.Context, req *connect.Request[leapmuxv1.GetSystemInfoRequest]) (*connect.Response[leapmuxv1.GetSystemInfoResponse], error) {
	providers, _ := s.store.OAuthProviders().ListEnabled(ctx)

	var setupRequired bool
	if !s.cfg.SoloMode {
		hasUser, err := s.checkHasAnyUser(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
		}
		setupRequired = !hasUser
	}

	// Only emit a worker_hub_url when the hub has no TCP listener (e.g.
	// the desktop app's NoTCP mode). With TCP enabled, the browser's
	// window.location.origin is already the right value — and in
	// reverse-proxied deployments it's *more* correct than what the hub
	// could reconstruct from cfg.Addr. With TCP disabled, the browser
	// origin in Tauri is `tauri://localhost`, which is unusable; the
	// only viable URL is the local unix-socket / named-pipe address.
	var workerHubURL string
	if s.cfg.Addr == "" {
		if u, err := s.cfg.LocalListenURL(); err == nil {
			workerHubURL = u
		}
	}

	return connect.NewResponse(&leapmuxv1.GetSystemInfoResponse{
		SignupEnabled: s.cfg.SignupEnabled,
		SoloMode:      s.cfg.SoloMode,
		SetupRequired: setupRequired,
		Version:       version.Value,
		CommitHash:    version.CommitHash,
		CommitTime:    version.CommitTime,
		BuildTime:     version.BuildTime,
		Branch:        version.Branch,
		OauthEnabled:  len(providers) > 0,
		WorkerHubUrl:  workerHubURL,
	}), nil
}

func (s *AuthService) GetOAuthProviders(ctx context.Context, req *connect.Request[leapmuxv1.GetOAuthProvidersRequest]) (*connect.Response[leapmuxv1.GetOAuthProvidersResponse], error) {
	providers, err := s.store.OAuthProviders().ListEnabled(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	baseURL := s.cfg.BaseURL()

	var pbProviders []*leapmuxv1.OAuthProviderInfo
	for _, p := range providers {
		pbProviders = append(pbProviders, &leapmuxv1.OAuthProviderInfo{
			Id:           p.ID,
			Name:         p.Name,
			ProviderType: p.ProviderType,
			LoginUrl:     fmt.Sprintf("%s/auth/oauth/%s/login", baseURL, p.ID),
		})
	}

	return connect.NewResponse(&leapmuxv1.GetOAuthProvidersResponse{
		Providers: pbProviders,
	}), nil
}

// loadPendingOAuthSignup fetches and validates a pending OAuth signup by token.
// It returns a connect error on missing/expired tokens.
func loadPendingOAuthSignup(ctx context.Context, st store.Store, token string) (*store.PendingOAuthSignup, error) {
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("signup_token is required"))
	}
	pending, err := st.PendingOAuthSignups().Get(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired signup token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if time.Now().UTC().After(pending.ExpiresAt) {
		_ = st.PendingOAuthSignups().Delete(ctx, token)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("signup token expired"))
	}
	return pending, nil
}

func (s *AuthService) GetPendingOAuthSignup(ctx context.Context, req *connect.Request[leapmuxv1.GetPendingOAuthSignupRequest]) (*connect.Response[leapmuxv1.GetPendingOAuthSignupResponse], error) {
	pending, err := loadPendingOAuthSignup(ctx, s.store, req.Msg.GetSignupToken())
	if err != nil {
		return nil, err
	}

	// Look up provider name for display.
	providerName := ""
	if provider, err := s.store.OAuthProviders().GetByID(ctx, pending.ProviderID); err == nil {
		providerName = provider.Name
	}

	return connect.NewResponse(&leapmuxv1.GetPendingOAuthSignupResponse{
		Email:        pending.Email,
		DisplayName:  pending.DisplayName,
		ProviderName: providerName,
	}), nil
}

func (s *AuthService) CompleteOAuthSignup(ctx context.Context, req *connect.Request[leapmuxv1.CompleteOAuthSignupRequest]) (*connect.Response[leapmuxv1.CompleteOAuthSignupResponse], error) {
	signupToken := req.Msg.GetSignupToken()
	pending, err := loadPendingOAuthSignup(ctx, s.store, signupToken)
	if err != nil {
		return nil, err
	}

	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// OAuth completion is always treated as public signup — the first-admin
	// flow lives at /setup, so both reserved rules apply.
	if usernames.IsReservedForPublicSignup(username) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is a reserved username", username))
	}

	if err := checkUsernameAvailable(ctx, s.store, username); err != nil {
		return nil, err
	}

	// Check that the OAuth link doesn't already exist (race protection).
	_, err = s.store.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{
		ProviderID:      pending.ProviderID,
		ProviderSubject: pending.ProviderSubject,
	})
	if err == nil {
		_ = s.store.PendingOAuthSignups().Delete(ctx, signupToken)
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("this identity is already linked to an account"))
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Use the email from the pending signup (provider-reported, already validated
	// at callback time). The request's email field is ignored.
	email := pending.Email
	rawDisplayName := req.Msg.GetDisplayName()
	if rawDisplayName == "" {
		rawDisplayName = pending.DisplayName
	}
	displayName, err := validate.SanitizeDisplayName(rawDisplayName, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display name: %w", err))
	}

	// Look up the provider's trust_email setting.
	oauthProvider, provErr := s.store.OAuthProviders().GetByID(ctx, pending.ProviderID)
	if provErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("lookup provider: %w", provErr))
	}
	trustEmail := oauthProvider.TrustEmail

	var userEmail string
	var emailVerified bool
	var pendingEmail string

	if email != "" {
		if err := CheckEmailAvailable(ctx, s.store, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}

		if trustEmail {
			// Trusted OAuth email — goes directly to email column as verified.
			userEmail = email
			emailVerified = true
		} else if s.cfg.EmailVerificationRequired {
			// Untrusted + verification required — goes to pending_email.
			pendingEmail = email
		} else {
			// Untrusted + verification not required — goes to email column unverified.
			userEmail = email
		}
	}

	user, err := CreateUserWithOrg(ctx, s.store, CreateUserParams{
		Username:      username,
		PasswordHash:  pwdhash.PlaceholderHash,
		DisplayName:   displayName,
		Email:         userEmail,
		EmailVerified: emailVerified,
		PasswordSet:   false, // OAuth users don't have a real password
		IsAdmin:       false,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Handle pending email if needed. Send failures during OAuth signup
	// don't roll back: the OAuth-linked account exists, and the user can
	// re-trigger verification via Resend later. emailSent flows back to
	// the response so the frontend can surface a Resend prompt when the
	// SMTP send failed but the row was written.
	emailSent := false
	if pendingEmail != "" {
		sent, err := issuePendingEmailVerification(ctx, s.store, s.mail, user.ID, pendingEmail)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		emailSent = sent
		if !sent {
			slog.Warn("verification email send failed during oauth signup",
				"user_id", user.ID,
			)
		}
	}

	if err := s.store.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
		UserID:          user.ID,
		ProviderID:      pending.ProviderID,
		ProviderSubject: pending.ProviderSubject,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create user link: %w", err))
	}

	// Decrypt and re-store OAuth tokens with the real user ID as AAD.
	if s.keystore != nil {
		if err := reencryptPendingTokens(ctx, s.keystore, s.store, pending, signupToken, user.ID); err != nil {
			slog.Error("oauth: re-encrypt tokens for new user", "error", err, "user_id", user.ID)
		}
	}

	_ = s.store.PendingOAuthSignups().Delete(ctx, signupToken)

	// Re-fetch user only when pending email modified the user row.
	finalUser := user
	if pendingEmail != "" {
		refetched, refetchErr := s.store.Users().GetByID(ctx, user.ID)
		if refetchErr != nil {
			return nil, connect.NewError(connect.CodeInternal, refetchErr)
		}
		finalUser = refetched
	}

	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, s.store, finalUser.ID)
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", sessionErr))
	}

	org, err := s.store.Orgs().GetByID(ctx, finalUser.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&leapmuxv1.CompleteOAuthSignupResponse{
		User:                  userToProtoWithOrgName(finalUser, org.Name),
		VerificationRequired:  pendingEmail != "",
		VerificationEmailSent: emailSent,
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

// reencryptPendingTokens decrypts tokens from a pending signup (keyed by signupToken)
// and re-encrypts them with the real userID as AAD.
func reencryptPendingTokens(ctx context.Context, ks *keystore.Keystore, st store.Store, pending *store.PendingOAuthSignup, signupToken, userID string) error {
	accessPlain, err := ks.Decrypt(pending.AccessToken, keystore.AccessTokenAAD(signupToken, pending.ProviderID))
	if err != nil {
		return fmt.Errorf("decrypt access token: %w", err)
	}
	refreshPlain, err := ks.Decrypt(pending.RefreshToken, keystore.RefreshTokenAAD(signupToken, pending.ProviderID))
	if err != nil {
		return fmt.Errorf("decrypt refresh token: %w", err)
	}

	encAccess, encRefresh, err := encryptTokenPair(ks, string(accessPlain), string(refreshPlain), userID, pending.ProviderID)
	if err != nil {
		return err
	}

	return st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
		UserID:       userID,
		ProviderID:   pending.ProviderID,
		AccessToken:  encAccess,
		RefreshToken: encRefresh,
		TokenType:    pending.TokenType,
		ExpiresAt:    pending.TokenExpiresAt,
		KeyVersion:   int64(ks.ActiveVersion()),
	})
}

func userToProto(u *store.User) *leapmuxv1.User {
	return &leapmuxv1.User{
		Id:            u.ID,
		OrgId:         u.OrgID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		IsAdmin:       u.IsAdmin,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		PendingEmail:  u.PendingEmail,
		PasswordSet:   u.PasswordSet,
	}
}

func userToProtoWithOrgName(u *store.User, orgName string) *leapmuxv1.User {
	p := userToProto(u)
	p.OrgName = orgName
	return p
}

func userToProtoWithOAuth(u *store.User, orgName string, oauthProviders []*leapmuxv1.LinkedOAuthProvider) *leapmuxv1.User {
	p := userToProtoWithOrgName(u, orgName)
	p.OauthProviders = oauthProviders
	return p
}
