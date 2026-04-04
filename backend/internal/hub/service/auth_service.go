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
	"github.com/leapmux/leapmux/internal/util/id"
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

	var oauthProviderName string
	links, _ := s.queries.ListOAuthUserLinksByUser(ctx, user.ID)
	if len(links) > 0 {
		if provider, err := s.queries.GetOAuthProviderByID(ctx, links[0].ProviderID); err == nil {
			oauthProviderName = provider.Name
		}
	}

	return connect.NewResponse(&leapmuxv1.GetCurrentUserResponse{
		User: userToProtoWithOAuth(&user, org.Name, oauthProviderName),
	}), nil
}

func (s *AuthService) SignUp(ctx context.Context, req *connect.Request[leapmuxv1.SignUpRequest]) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is not available in solo mode"))
	}
	if !s.cfg.SignupEnabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}

	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	pw := req.Msg.GetPassword()
	if pw == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("password is required"))
	}

	// Check username uniqueness.
	if err := checkUsernameAvailable(ctx, s.queries, username); err != nil {
		return nil, err
	}

	email := req.Msg.GetEmail()
	hash, err := pwdhash.Hash(pw)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	if s.cfg.EmailVerificationRequired && email != "" {
		// Email goes to pending_email; email column stays empty until verified.
		if err := checkEmailAvailable(ctx, s.queries, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}

		user, err := createUserWithOrg(ctx, s.sqlDB, s.queries, CreateUserParams{
			Username:     username,
			PasswordHash: hash,
			DisplayName:  req.Msg.GetDisplayName(),
			Email:        "", // email goes to pending_email
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
		DisplayName:  req.Msg.GetDisplayName(),
		Email:        email,
		IsAdmin:      0,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, s.queries, user.ID, "", "")
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
	token := req.Msg.GetVerificationToken()
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("verification_token is required"))
	}

	user, err := s.queries.GetUserByPendingEmailToken(ctx, token)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid verification token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if user.PendingEmailExpiresAt.Valid && time.Now().UTC().After(user.PendingEmailExpiresAt.Time) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("verification token expired"))
	}

	if err := promotePendingEmail(ctx, s.queries, user.ID, user.PendingEmail); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	updatedUser, err := s.queries.GetUserByID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	sessionID, sessionExpiresAt, sessionErr := auth.CreateSession(ctx, s.queries, updatedUser.ID, "", "")
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, sessionErr)
	}

	org, err := s.queries.GetOrgByID(ctx, updatedUser.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&leapmuxv1.VerifyEmailResponse{
		User: userToProtoWithOrgName(&updatedUser, org.Name),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, sessionExpiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

func (s *AuthService) GetSystemInfo(ctx context.Context, req *connect.Request[leapmuxv1.GetSystemInfoRequest]) (*connect.Response[leapmuxv1.GetSystemInfoResponse], error) {
	providers, _ := s.queries.ListEnabledOAuthProviders(ctx)

	return connect.NewResponse(&leapmuxv1.GetSystemInfoResponse{
		SignupEnabled: s.cfg.SignupEnabled,
		SoloMode:      s.cfg.SoloMode,
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

func (s *AuthService) GetPendingOAuthSignup(ctx context.Context, req *connect.Request[leapmuxv1.GetPendingOAuthSignupRequest]) (*connect.Response[leapmuxv1.GetPendingOAuthSignupResponse], error) {
	token := req.Msg.GetSignupToken()
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("signup_token is required"))
	}

	pending, err := s.queries.GetPendingOAuthSignup(ctx, token)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired signup token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if time.Now().UTC().After(pending.ExpiresAt) {
		_ = s.queries.DeletePendingOAuthSignup(ctx, token)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("signup token expired"))
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
	if signupToken == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("signup_token is required"))
	}

	pending, err := s.queries.GetPendingOAuthSignup(ctx, signupToken)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired signup token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if time.Now().UTC().After(pending.ExpiresAt) {
		_ = s.queries.DeletePendingOAuthSignup(ctx, signupToken)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("signup token expired"))
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

	// Determine email handling.
	email := req.Msg.GetEmail()
	displayName := req.Msg.GetDisplayName()
	if displayName == "" {
		displayName = pending.DisplayName
	}

	var userEmail string
	var emailVerified bool
	var pendingEmail string

	if email != "" && s.cfg.OAuthTrustEmail {
		// Trusted OAuth email — goes directly to email column.
		if err := checkEmailAvailable(ctx, s.queries, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		userEmail = email
		emailVerified = true
	} else if email != "" && !s.cfg.OAuthTrustEmail && s.cfg.EmailVerificationRequired {
		// Untrusted + verification required — goes to pending_email.
		if err := checkEmailAvailable(ctx, s.queries, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		pendingEmail = email
	} else if email != "" {
		// Untrusted + verification not required — goes to email column unverified.
		if err := checkEmailAvailable(ctx, s.queries, email, ""); err != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		userEmail = email
	}

	// Generate random password hash for NOT NULL constraint.
	randomPwdHash, err := pwdhash.Hash(id.Generate())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generate random password: %w", err))
	}

	user, err := createUserWithOrg(ctx, s.sqlDB, s.queries, CreateUserParams{
		Username:     username,
		PasswordHash: randomPwdHash,
		DisplayName:  displayName,
		Email:        userEmail,
		IsAdmin:      0,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Set email_verified if trusted OAuth.
	if emailVerified {
		var ev int64 = 1
		if err := s.queries.UpdateUserEmailVerified(ctx, db.UpdateUserEmailVerifiedParams{
			EmailVerified: ev,
			ID:            user.ID,
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
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
		accessTokenPlain, err := s.keystore.Decrypt(pending.AccessToken, keystore.AccessTokenAAD(signupToken, pending.ProviderID))
		if err != nil {
			slog.Error("oauth: decrypt access token for re-encryption", "error", err, "user_id", user.ID)
		} else {
			refreshTokenPlain, err := s.keystore.Decrypt(pending.RefreshToken, keystore.RefreshTokenAAD(signupToken, pending.ProviderID))
			if err != nil {
				slog.Error("oauth: decrypt refresh token for re-encryption", "error", err, "user_id", user.ID)
			} else {
				// Re-encrypt with user ID AAD.
				encAccess, errA := s.keystore.Encrypt(accessTokenPlain, keystore.AccessTokenAAD(user.ID, pending.ProviderID))
				encRefresh, errR := s.keystore.Encrypt(refreshTokenPlain, keystore.RefreshTokenAAD(user.ID, pending.ProviderID))
				if errA != nil {
					slog.Error("oauth: encrypt access token", "error", errA, "user_id", user.ID)
				} else if errR != nil {
					slog.Error("oauth: encrypt refresh token", "error", errR, "user_id", user.ID)
				} else if err := s.queries.UpsertOAuthTokens(ctx, db.UpsertOAuthTokensParams{
					UserID:       user.ID,
					ProviderID:   pending.ProviderID,
					AccessToken:  encAccess,
					RefreshToken: encRefresh,
					TokenType:    pending.TokenType,
					ExpiresAt:    pending.TokenExpiresAt,
					KeyVersion:   pending.KeyVersion,
				}); err != nil {
					slog.Error("oauth: upsert re-encrypted tokens", "error", err, "user_id", user.ID)
				}
			}
		}
	}

	// Consume the pending signup.
	_ = s.queries.DeletePendingOAuthSignup(ctx, signupToken)

	// Re-fetch user to get final state.
	finalUser, err := s.queries.GetUserByID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Create session.
	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, s.queries, finalUser.ID, "", "")
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, sessionErr)
	}

	org, err := s.queries.GetOrgByID(ctx, finalUser.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&leapmuxv1.CompleteOAuthSignupResponse{
		User: userToProtoWithOrgName(&finalUser, org.Name),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt, s.cfg.SecureCookies).String())
	return resp, nil
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
	}
}

func userToProtoWithOrgName(u *db.User, orgName string) *leapmuxv1.User {
	p := userToProto(u)
	p.OrgName = orgName
	return p
}

func userToProtoWithOAuth(u *db.User, orgName, oauthProvider string) *leapmuxv1.User {
	p := userToProtoWithOrgName(u, orgName)
	p.OauthProvider = oauthProvider
	return p
}
