package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
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
}

// NewAuthService creates a new AuthService.
func NewAuthService(sqlDB *sql.DB, q *db.Queries, cfg *config.Config, sc *auth.SessionCache) *AuthService {
	return &AuthService{sqlDB: sqlDB, queries: q, cfg: cfg, sessionCache: sc}
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
	password := req.Msg.GetPassword()
	if password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("password is required"))
	}

	// Check username uniqueness.
	_, err = s.queries.GetUserByUsername(ctx, username)
	if err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username already taken"))
	}
	if err != sql.ErrNoRows {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	hash, err := pwdhash.Hash(password)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	user, err := createUserWithOrg(ctx, s.sqlDB, s.queries, CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  req.Msg.GetDisplayName(),
		Email:        req.Msg.GetEmail(),
		IsAdmin:      0,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Check if email verification is required.
	if s.cfg.EmailVerificationRequired && req.Msg.GetEmail() != "" {
		verificationID := id.Generate()
		verificationToken := id.Generate()
		expiresAt := time.Now().Add(24 * time.Hour).UTC()

		if err := s.queries.CreateEmailVerification(ctx, db.CreateEmailVerificationParams{
			ID:        verificationID,
			UserID:    user.ID,
			Token:     verificationToken,
			ExpiresAt: expiresAt,
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create verification: %w", err))
		}

		return connect.NewResponse(&leapmuxv1.SignUpResponse{
			User:                 userToProtoWithOrgName(user, username),
			VerificationRequired: true,
		}), nil
	}

	// No verification required — create session immediately.
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
	verificationToken := req.Msg.GetVerificationToken()
	if verificationToken == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("verification_token is required"))
	}

	verification, err := s.queries.GetEmailVerificationByToken(ctx, verificationToken)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid verification token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if time.Now().UTC().After(verification.ExpiresAt) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("verification token expired"))
	}

	if err := s.queries.UpdateUserEmailVerified(ctx, db.UpdateUserEmailVerifiedParams{
		EmailVerified: 1,
		ID:            verification.UserID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if err := s.queries.DeleteEmailVerificationsByUserID(ctx, verification.UserID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	user, err := s.queries.GetUserByID(ctx, verification.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	sessionID, sessionExpiresAt, sessionErr := auth.CreateSession(ctx, s.queries, user.ID, "", "")
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, sessionErr)
	}

	org, err := s.queries.GetOrgByID(ctx, user.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&leapmuxv1.VerifyEmailResponse{
		User: userToProtoWithOrgName(&user, org.Name),
	})
	resp.Header().Set("Set-Cookie", auth.BuildSessionCookie(sessionID, sessionExpiresAt, s.cfg.SecureCookies).String())
	return resp, nil
}

func (s *AuthService) GetSystemInfo(ctx context.Context, req *connect.Request[leapmuxv1.GetSystemInfoRequest]) (*connect.Response[leapmuxv1.GetSystemInfoResponse], error) {
	// Check if any OAuth providers are configured.
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

func userToProto(u *db.User) *leapmuxv1.User {
	return &leapmuxv1.User{
		Id:            u.ID,
		OrgId:         u.OrgID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		IsAdmin:       u.IsAdmin == 1,
		Email:         u.Email,
		EmailVerified: u.EmailVerified == 1,
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
