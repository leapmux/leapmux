package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/validate"
	"github.com/leapmux/leapmux/util/version"
)

// AuthService implements the leapmux.v1.AuthService ConnectRPC handler.
type AuthService struct {
	sqlDB        *sql.DB
	queries      *db.Queries
	cfg          *config.Config
	sessionCache *auth.SessionCache
	keystore     *keystore.Keystore
}

// NewAuthService creates a new AuthService.
func NewAuthService(sqlDB *sql.DB, q *db.Queries, cfg *config.Config, sc *auth.SessionCache, ks *keystore.Keystore) *AuthService {
	return &AuthService{sqlDB: sqlDB, queries: q, cfg: cfg, sessionCache: sc, keystore: ks}
}

func (s *AuthService) Login(ctx context.Context, req *connect.Request[leapmuxv1.LoginRequest]) (*connect.Response[leapmuxv1.LoginResponse], error) {
	token, user, expiresAt, err := auth.Login(ctx, s.queries, req.Msg.GetUsername(), req.Msg.GetPassword())
	if err != nil {
		return nil, err
	}

	org, err := s.queries.GetOrgByID(ctx, user.OrgID)
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
		_ = s.queries.DeleteUserSession(ctx, token)
		if s.sessionCache != nil {
			s.sessionCache.Evict(token)
		}
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

	user, err := s.queries.GetUserByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	org, err := s.queries.GetOrgByID(ctx, user.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var linkedProviders []*leapmuxv1.LinkedOAuthProvider
	links, _ := s.queries.ListOAuthUserLinksByUser(ctx, user.ID)
	for _, link := range links {
		if provider, err := s.queries.GetOAuthProviderByID(ctx, link.ProviderID); err == nil {
			linkedProviders = append(linkedProviders, &leapmuxv1.LinkedOAuthProvider{
				Id:   provider.ID,
				Name: provider.Name,
			})
		}
	}

	return connect.NewResponse(&leapmuxv1.GetCurrentUserResponse{
		User: userToProtoWithOAuth(&user, org.Name, linkedProviders),
	}), nil
}

func (s *AuthService) SignUp(ctx context.Context, req *connect.Request[leapmuxv1.SignUpRequest]) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is not available in solo mode"))
	}

	// Check if this is initial setup (no users exist yet).
	// The first user is always created as an admin, regardless of whether
	// signup is enabled globally.
	hasUser, err := s.queries.HasAnyUser(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
	}
	isSetupMode := hasUser == 0
	if !isSetupMode && !s.cfg.SignupEnabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}

	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	displayName, err := validate.SanitizeDisplayName(req.Msg.GetDisplayName(), username)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display name: %w", err))
	}
	pw := req.Msg.GetPassword()
	if err := validate.ValidatePassword(pw); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Check username uniqueness.
	if err := checkUsernameAvailable(ctx, s.queries, username); err != nil {
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
		if err := checkEmailAvailable(ctx, s.queries, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}

		user, err := createUserWithOrg(ctx, s.sqlDB, s.queries, CreateUserParams{
			Username:     username,
			PasswordHash: hash,
			DisplayName:  displayName,
			Email:        "", // email goes to pending_email
			PasswordSet:  1,
			IsAdmin:      0,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		// Set pending email with verification token (stub: auto-verifies).
		if err := setPendingEmailWithToken(ctx, s.queries, user.ID, email); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		// Re-fetch user after promotion.
		updatedUser, err := s.queries.GetUserByID(ctx, user.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		return connect.NewResponse(&leapmuxv1.SignUpResponse{
			User:                 userToProtoWithOrgName(&updatedUser, username),
			VerificationRequired: true,
		}), nil
	}

	// No verification required — email goes directly to email column.
	if email != "" {
		if err := checkEmailAvailable(ctx, s.queries, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
	}

	user, err := createUserWithOrg(ctx, s.sqlDB, s.queries, CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  displayName,
		Email:        email,
		PasswordSet:  1,
		IsAdmin:      0,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, s.queries, user.ID)
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", sessionErr))
	}

	resp := connect.NewResponse(&leapmuxv1.SignUpResponse{
		User: userToProtoWithOrgName(user, username),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

// signUpSetupMode handles the initial admin account creation when no users
// exist yet. The first user is always an admin with a verified email.
func (s *AuthService) signUpSetupMode(ctx context.Context, username, displayName, email, passwordHash string) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	// Re-check to handle race condition where another request created a user
	// between the initial check and now.
	hasUser, err := s.queries.HasAnyUser(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
	}
	if hasUser != 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}

	if email != "" {
		if err := checkEmailAvailable(ctx, s.queries, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
	}

	var emailVerified int64
	if email != "" {
		emailVerified = 1
	}

	user, err := createUserWithOrg(ctx, s.sqlDB, s.queries, CreateUserParams{
		Username:      username,
		PasswordHash:  passwordHash,
		DisplayName:   displayName,
		Email:         email,
		EmailVerified: emailVerified,
		PasswordSet:   1,
		IsAdmin:       1,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, s.queries, user.ID)
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", sessionErr))
	}

	resp := connect.NewResponse(&leapmuxv1.SignUpResponse{
		User: userToProtoWithOrgName(user, username),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

func (s *AuthService) VerifyEmail(ctx context.Context, req *connect.Request[leapmuxv1.VerifyEmailRequest]) (*connect.Response[leapmuxv1.VerifyEmailResponse], error) {
	updatedUser, err := verifyPendingEmailToken(ctx, s.queries, req.Msg.GetVerificationToken())
	if err != nil {
		return nil, err
	}

	sessionID, sessionExpiresAt, sessionErr := auth.CreateSession(ctx, s.queries, updatedUser.ID)
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", sessionErr))
	}

	org, err := s.queries.GetOrgByID(ctx, updatedUser.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&leapmuxv1.VerifyEmailResponse{
		User: userToProtoWithOrgName(updatedUser, org.Name),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, sessionExpiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

func (s *AuthService) GetSystemInfo(ctx context.Context, req *connect.Request[leapmuxv1.GetSystemInfoRequest]) (*connect.Response[leapmuxv1.GetSystemInfoResponse], error) {
	providers, _ := s.queries.ListEnabledOAuthProviders(ctx)

	var setupRequired bool
	if !s.cfg.SoloMode && !s.cfg.DevMode {
		hasUser, err := s.queries.HasAnyUser(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
		}
		setupRequired = hasUser == 0
	}

	return connect.NewResponse(&leapmuxv1.GetSystemInfoResponse{
		SignupEnabled: s.cfg.SignupEnabled,
		SoloMode:      s.cfg.SoloMode,
		SetupRequired: setupRequired,
		Version:       version.Value,
		CommitHash:    version.CommitHash,
		CommitTime:    version.CommitTime,
		BuildTime:     version.BuildTime,
		OauthEnabled:  len(providers) > 0,
	}), nil
}

func (s *AuthService) GetOAuthProviders(ctx context.Context, req *connect.Request[leapmuxv1.GetOAuthProvidersRequest]) (*connect.Response[leapmuxv1.GetOAuthProvidersResponse], error) {
	providers, err := s.queries.ListEnabledOAuthProviders(ctx)
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
func loadPendingOAuthSignup(ctx context.Context, q *db.Queries, token string) (*db.PendingOauthSignup, error) {
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("signup_token is required"))
	}
	pending, err := q.GetPendingOAuthSignup(ctx, token)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired signup token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if time.Now().UTC().After(pending.ExpiresAt) {
		_ = q.DeletePendingOAuthSignup(ctx, token)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("signup token expired"))
	}
	return &pending, nil
}

func (s *AuthService) GetPendingOAuthSignup(ctx context.Context, req *connect.Request[leapmuxv1.GetPendingOAuthSignupRequest]) (*connect.Response[leapmuxv1.GetPendingOAuthSignupResponse], error) {
	pending, err := loadPendingOAuthSignup(ctx, s.queries, req.Msg.GetSignupToken())
	if err != nil {
		return nil, err
	}

	// Look up provider name for display.
	providerName := ""
	if provider, err := s.queries.GetOAuthProviderByID(ctx, pending.ProviderID); err == nil {
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
	pending, err := loadPendingOAuthSignup(ctx, s.queries, signupToken)
	if err != nil {
		return nil, err
	}

	// Validate username.
	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Check username uniqueness.
	if err := checkUsernameAvailable(ctx, s.queries, username); err != nil {
		return nil, err
	}

	// Check that the OAuth link doesn't already exist (race protection).
	_, err = s.queries.GetOAuthUserLink(ctx, db.GetOAuthUserLinkParams{
		ProviderID:      pending.ProviderID,
		ProviderSubject: pending.ProviderSubject,
	})
	if err == nil {
		_ = s.queries.DeletePendingOAuthSignup(ctx, signupToken)
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("this identity is already linked to an account"))
	}
	if err != sql.ErrNoRows {
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
	oauthProvider, provErr := s.queries.GetOAuthProviderByID(ctx, pending.ProviderID)
	if provErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("lookup provider: %w", provErr))
	}
	trustEmail := oauthProvider.TrustEmail == 1

	var userEmail string
	var emailVerified bool
	var pendingEmail string

	if email != "" {
		if err := checkEmailAvailable(ctx, s.queries, email, ""); err != nil {
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

	user, err := createUserWithOrg(ctx, s.sqlDB, s.queries, CreateUserParams{
		Username:      username,
		PasswordHash:  pwdhash.PlaceholderHash,
		DisplayName:   displayName,
		Email:         userEmail,
		EmailVerified: ptrconv.BoolToInt64(emailVerified),
		PasswordSet:   0, // OAuth users don't have a real password
		IsAdmin:       0,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Handle pending email if needed.
	if pendingEmail != "" {
		if err := setPendingEmailWithToken(ctx, s.queries, user.ID, pendingEmail); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	// Create OAuth user link.
	if err := s.queries.CreateOAuthUserLink(ctx, db.CreateOAuthUserLinkParams{
		UserID:          user.ID,
		ProviderID:      pending.ProviderID,
		ProviderSubject: pending.ProviderSubject,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create user link: %w", err))
	}

	// Decrypt and re-store OAuth tokens with the real user ID as AAD.
	if s.keystore != nil {
		if err := reencryptPendingTokens(ctx, s.keystore, s.queries, pending, signupToken, user.ID); err != nil {
			slog.Error("oauth: re-encrypt tokens for new user", "error", err, "user_id", user.ID)
		}
	}

	// Consume the pending signup.
	_ = s.queries.DeletePendingOAuthSignup(ctx, signupToken)

	// Re-fetch user only when pending email modified the user row.
	finalUser := user
	if pendingEmail != "" {
		refetched, refetchErr := s.queries.GetUserByID(ctx, user.ID)
		if refetchErr != nil {
			return nil, connect.NewError(connect.CodeInternal, refetchErr)
		}
		finalUser = &refetched
	}

	// Create session.
	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, s.queries, finalUser.ID)
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", sessionErr))
	}

	org, err := s.queries.GetOrgByID(ctx, finalUser.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&leapmuxv1.CompleteOAuthSignupResponse{
		User: userToProtoWithOrgName(finalUser, org.Name),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

// reencryptPendingTokens decrypts tokens from a pending signup (keyed by signupToken)
// and re-encrypts them with the real userID as AAD.
func reencryptPendingTokens(ctx context.Context, ks *keystore.Keystore, q *db.Queries, pending *db.PendingOauthSignup, signupToken, userID string) error {
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

	return q.UpsertOAuthTokens(ctx, db.UpsertOAuthTokensParams{
		UserID:       userID,
		ProviderID:   pending.ProviderID,
		AccessToken:  encAccess,
		RefreshToken: encRefresh,
		TokenType:    pending.TokenType,
		ExpiresAt:    pending.TokenExpiresAt,
		KeyVersion:   int64(ks.ActiveVersion()),
	})
}

func userToProto(u *db.User) *leapmuxv1.User {
	return &leapmuxv1.User{
		Id:            u.ID,
		OrgId:         u.OrgID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		IsAdmin:       u.IsAdmin == 1,
		Email:         u.Email,
		EmailVerified: u.EmailVerified == 1,
		PendingEmail:  u.PendingEmail,
		PasswordSet:   u.PasswordSet == 1,
	}
}

func userToProtoWithOrgName(u *db.User, orgName string) *leapmuxv1.User {
	p := userToProto(u)
	p.OrgName = orgName
	return p
}

func userToProtoWithOAuth(u *db.User, orgName string, oauthProviders []*leapmuxv1.LinkedOAuthProvider) *leapmuxv1.User {
	p := userToProtoWithOrgName(u, orgName)
	p.OauthProviders = oauthProviders
	return p
}
