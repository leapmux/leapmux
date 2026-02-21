package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
)

// AuthService implements the leapmux.v1.AuthService ConnectRPC handler.
type AuthService struct {
	queries *db.Queries
}

// NewAuthService creates a new AuthService.
func NewAuthService(q *db.Queries) *AuthService {
	return &AuthService{queries: q}
}

func (s *AuthService) Login(ctx context.Context, req *connect.Request[leapmuxv1.LoginRequest]) (*connect.Response[leapmuxv1.LoginResponse], error) {
	token, user, err := auth.Login(ctx, s.queries, req.Msg.GetUsername(), req.Msg.GetPassword())
	if err != nil {
		return nil, err
	}

	org, err := s.queries.GetOrgByID(ctx, user.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.LoginResponse{
		Token: token,
		User:  userToProtoWithOrgName(user, org.Name),
	}), nil
}

func (s *AuthService) Logout(ctx context.Context, req *connect.Request[leapmuxv1.LogoutRequest]) (*connect.Response[leapmuxv1.LogoutResponse], error) {
	token := auth.TokenFromHeader(req.Header().Get("Authorization"))
	if token != "" {
		_ = s.queries.DeleteUserSession(ctx, token)
	}
	return connect.NewResponse(&leapmuxv1.LogoutResponse{}), nil
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

	return connect.NewResponse(&leapmuxv1.GetCurrentUserResponse{
		User: userToProtoWithOrgName(&user, org.Name),
	}), nil
}

func (s *AuthService) SignUp(ctx context.Context, req *connect.Request[leapmuxv1.SignUpRequest]) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	// Check if sign-up is enabled.
	settings, err := s.queries.GetSystemSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get settings: %w", err))
	}
	if settings.SignupEnabled == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}

	username := req.Msg.GetUsername()
	password := req.Msg.GetPassword()
	if username == "" || password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("username and password are required"))
	}

	// Check username uniqueness.
	_, err = s.queries.GetUserByUsername(ctx, username)
	if err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username already taken"))
	}
	if err != sql.ErrNoRows {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	// Create personal org.
	orgID := id.Generate()
	if err := s.queries.CreateOrg(ctx, db.CreateOrgParams{
		ID:         orgID,
		Name:       username,
		IsPersonal: 1,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create personal org: %w", err))
	}

	// Create user.
	userID := id.Generate()
	if err := s.queries.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     username,
		PasswordHash: string(hash),
		DisplayName:  req.Msg.GetDisplayName(),
		Email:        req.Msg.GetEmail(),
		IsAdmin:      0,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create user: %w", err))
	}

	// Add to org_members.
	if err := s.queries.CreateOrgMember(ctx, db.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create org member: %w", err))
	}

	// Create default preferences.
	if err := s.queries.UpsertUserPreferences(ctx, db.UpsertUserPreferencesParams{
		UserID: userID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create preferences: %w", err))
	}

	user, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Check if email verification is required.
	if settings.EmailVerificationRequired == 1 && req.Msg.GetEmail() != "" {
		// Create verification token.
		verificationID := id.Generate()
		verificationToken := id.Generate()
		expiresAt := time.Now().Add(24 * time.Hour).UTC()

		if err := s.queries.CreateEmailVerification(ctx, db.CreateEmailVerificationParams{
			ID:        verificationID,
			UserID:    userID,
			Token:     verificationToken,
			ExpiresAt: expiresAt,
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create verification: %w", err))
		}

		return connect.NewResponse(&leapmuxv1.SignUpResponse{
			User:                 userToProtoWithOrgName(&user, username),
			VerificationRequired: true,
		}), nil
	}

	// No verification required — create session immediately.
	token, _, err := auth.Login(ctx, s.queries, username, password)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("auto-login: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.SignUpResponse{
		Token: token,
		User:  userToProtoWithOrgName(&user, username),
	}), nil
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

	// Check expiration.
	if time.Now().UTC().After(verification.ExpiresAt) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("verification token expired"))
	}

	// Mark email as verified.
	if err := s.queries.UpdateUserEmailVerified(ctx, db.UpdateUserEmailVerifiedParams{
		EmailVerified: 1,
		ID:            verification.UserID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Clean up verification tokens.
	if err := s.queries.DeleteEmailVerificationsByUserID(ctx, verification.UserID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Create session for the user.
	user, err := s.queries.GetUserByID(ctx, verification.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	sessionID := id.Generate()
	sessionExpiresAt := time.Now().Add(24 * time.Hour).UTC()
	if err := s.queries.CreateUserSession(ctx, db.CreateUserSessionParams{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: sessionExpiresAt,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	org, err := s.queries.GetOrgByID(ctx, user.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.VerifyEmailResponse{
		Token: sessionID,
		User:  userToProtoWithOrgName(&user, org.Name),
	}), nil
}

func (s *AuthService) GetSystemInfo(ctx context.Context, req *connect.Request[leapmuxv1.GetSystemInfoRequest]) (*connect.Response[leapmuxv1.GetSystemInfoResponse], error) {
	settings, err := s.queries.GetSystemSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get system settings: %w", err))
	}
	return connect.NewResponse(&leapmuxv1.GetSystemInfoResponse{
		SignupEnabled: settings.SignupEnabled == 1,
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
