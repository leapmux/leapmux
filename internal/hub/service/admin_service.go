package service

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"connectrpc.com/connect"
	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/validate"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// AdminService implements the leapmux.v1.AdminService ConnectRPC handler.
type AdminService struct {
	queries    *db.Queries
	timeoutCfg *timeout.Config
}

// NewAdminService creates a new AdminService.
func NewAdminService(q *db.Queries, tc *timeout.Config) *AdminService {
	return &AdminService{queries: q, timeoutCfg: tc}
}

func requireAdmin(ctx context.Context) (*auth.UserInfo, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if !user.IsAdmin {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("admin access required"))
	}
	return user, nil
}

func (s *AdminService) GetSettings(ctx context.Context, req *connect.Request[leapmuxv1.GetSettingsRequest]) (*connect.Response[leapmuxv1.GetSettingsResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}

	settings, err := s.queries.GetSystemSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get settings: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.GetSettingsResponse{
		Settings: settingsToProto(&settings),
	}), nil
}

func (s *AdminService) UpdateSettings(ctx context.Context, req *connect.Request[leapmuxv1.UpdateSettingsRequest]) (*connect.Response[leapmuxv1.UpdateSettingsResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}

	// Get current settings to preserve SMTP password if not provided.
	current, err := s.queries.GetSystemSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get current settings: %w", err))
	}

	reqSettings := req.Msg.GetSettings()

	smtpPassword := current.SmtpPassword
	if reqSettings.GetSmtp().GetPassword() != "" {
		smtpPassword = reqSettings.GetSmtp().GetPassword()
	}

	var signupEnabled int64
	if reqSettings.GetSignupEnabled() {
		signupEnabled = 1
	}
	var emailVerificationRequired int64
	if reqSettings.GetEmailVerificationRequired() {
		emailVerificationRequired = 1
	}
	var smtpUseTls int64
	if reqSettings.GetSmtp().GetUseTls() {
		smtpUseTls = 1
	}

	apiTimeout := int64(reqSettings.GetApiTimeoutSeconds())
	if apiTimeout <= 0 {
		apiTimeout = timeout.DefaultAPITimeout
	}
	agentStartupTimeout := int64(reqSettings.GetAgentStartupTimeoutSeconds())
	if agentStartupTimeout <= 0 {
		agentStartupTimeout = timeout.DefaultAgentStartupTimeout
	}
	worktreeCreateTimeout := int64(reqSettings.GetWorktreeCreateTimeoutSeconds())
	if worktreeCreateTimeout <= 0 {
		worktreeCreateTimeout = timeout.DefaultWorktreeCreateTimeout
	}

	if err := s.queries.UpdateSystemSettings(ctx, db.UpdateSystemSettingsParams{
		SignupEnabled:                signupEnabled,
		EmailVerificationRequired:    emailVerificationRequired,
		SmtpHost:                     reqSettings.GetSmtp().GetHost(),
		SmtpPort:                     int64(reqSettings.GetSmtp().GetPort()),
		SmtpUsername:                 reqSettings.GetSmtp().GetUsername(),
		SmtpPassword:                 smtpPassword,
		SmtpFromAddress:              reqSettings.GetSmtp().GetFromAddress(),
		SmtpUseTls:                   smtpUseTls,
		ApiTimeoutSeconds:            apiTimeout,
		AgentStartupTimeoutSeconds:   agentStartupTimeout,
		WorktreeCreateTimeoutSeconds: worktreeCreateTimeout,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update settings: %w", err))
	}

	// Re-fetch to get updated_at.
	updated, err := s.queries.GetSystemSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get updated settings: %w", err))
	}

	// Refresh the in-memory timeout config.
	s.timeoutCfg.Refresh(updated)

	return connect.NewResponse(&leapmuxv1.UpdateSettingsResponse{
		Settings: settingsToProto(&updated),
	}), nil
}

func (s *AdminService) ListUsers(ctx context.Context, req *connect.Request[leapmuxv1.ListUsersRequest]) (*connect.Response[leapmuxv1.ListUsersResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}

	limit := int64(50)
	if req.Msg.GetPage() != nil && req.Msg.GetPage().GetLimit() > 0 {
		limit = int64(req.Msg.GetPage().GetLimit())
	}

	var offset int64
	if req.Msg.GetPage() != nil && req.Msg.GetPage().GetCursor() != "" {
		parsed, err := strconv.ParseInt(req.Msg.GetPage().GetCursor(), 10, 64)
		if err == nil {
			offset = parsed
		}
	}

	var users []db.User
	query := req.Msg.GetQuery()
	if query != "" {
		var err error
		users, err = s.queries.SearchUsers(ctx, db.SearchUsersParams{
			Query:  sql.NullString{String: query, Valid: true},
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("search users: %w", err))
		}
	} else {
		var err error
		users, err = s.queries.ListAllUsers(ctx, db.ListAllUsersParams{
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list users: %w", err))
		}
	}

	protoUsers := make([]*leapmuxv1.AdminUserView, 0, len(users))
	for i := range users {
		orgName := ""
		org, err := s.queries.GetOrgByID(ctx, users[i].OrgID)
		if err == nil {
			orgName = org.Name
		}
		protoUsers = append(protoUsers, adminUserToProto(&users[i], orgName))
	}

	var nextCursor string
	hasMore := int64(len(users)) == limit
	if hasMore {
		nextCursor = strconv.FormatInt(offset+limit, 10)
	}

	return connect.NewResponse(&leapmuxv1.ListUsersResponse{
		Users: protoUsers,
		Page: &leapmuxv1.PageResponse{
			NextCursor: nextCursor,
			HasMore:    hasMore,
		},
	}), nil
}

func (s *AdminService) GetUser(ctx context.Context, req *connect.Request[leapmuxv1.GetUserRequest]) (*connect.Response[leapmuxv1.GetUserResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}

	user, err := s.queries.GetUserByID(ctx, req.Msg.GetUserId())
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get user: %w", err))
	}

	orgName := ""
	org, err := s.queries.GetOrgByID(ctx, user.OrgID)
	if err == nil {
		orgName = org.Name
	}

	return connect.NewResponse(&leapmuxv1.GetUserResponse{
		User: adminUserToProto(&user, orgName),
	}), nil
}

func (s *AdminService) CreateUser(ctx context.Context, req *connect.Request[leapmuxv1.CreateUserRequest]) (*connect.Response[leapmuxv1.CreateUserResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}

	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Create personal org.
	orgID := id.Generate()
	if err := s.queries.CreateOrg(ctx, db.CreateOrgParams{
		ID:         orgID,
		Name:       username,
		IsPersonal: 1,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create org: %w", err))
	}

	// Hash password.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Msg.GetPassword()), bcrypt.DefaultCost)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	var isAdmin int64
	if req.Msg.GetIsAdmin() {
		isAdmin = 1
	}

	userID := id.Generate()
	if err := s.queries.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     username,
		PasswordHash: string(hash),
		DisplayName:  req.Msg.GetDisplayName(),
		Email:        req.Msg.GetEmail(),
		IsAdmin:      isAdmin,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create user: %w", err))
	}

	// Create org membership.
	if err := s.queries.CreateOrgMember(ctx, db.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create org member: %w", err))
	}

	// Create default user preferences.
	if err := s.queries.UpsertUserPreferences(ctx, db.UpsertUserPreferencesParams{
		UserID: userID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create user preferences: %w", err))
	}

	// Fetch the created user to get timestamps.
	user, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get created user: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.CreateUserResponse{
		User: adminUserToProto(&user, username),
	}), nil
}

func (s *AdminService) UpdateUser(ctx context.Context, req *connect.Request[leapmuxv1.UpdateUserRequest]) (*connect.Response[leapmuxv1.UpdateUserResponse], error) {
	caller, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.queries.GetUserByID(ctx, req.Msg.GetUserId())
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get user: %w", err))
	}

	// Prevent removing own admin.
	if user.ID == caller.ID && !req.Msg.GetIsAdmin() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot remove your own admin privileges"))
	}

	// Update admin status if changed.
	currentIsAdmin := user.IsAdmin == 1
	if req.Msg.GetIsAdmin() != currentIsAdmin {
		var isAdmin int64
		if req.Msg.GetIsAdmin() {
			isAdmin = 1
		}
		if err := s.queries.UpdateUserAdmin(ctx, db.UpdateUserAdminParams{
			IsAdmin: isAdmin,
			ID:      user.ID,
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update user admin: %w", err))
		}
	}

	// Update profile fields (keep existing username).
	if err := s.queries.UpdateUserProfile(ctx, db.UpdateUserProfileParams{
		Username:    user.Username,
		DisplayName: req.Msg.GetDisplayName(),
		Email:       req.Msg.GetEmail(),
		ID:          user.ID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update user profile: %w", err))
	}

	// Fetch updated user.
	updated, err := s.queries.GetUserByID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get updated user: %w", err))
	}

	orgName := ""
	org, err := s.queries.GetOrgByID(ctx, updated.OrgID)
	if err == nil {
		orgName = org.Name
	}

	return connect.NewResponse(&leapmuxv1.UpdateUserResponse{
		User: adminUserToProto(&updated, orgName),
	}), nil
}

func (s *AdminService) DeleteUser(ctx context.Context, req *connect.Request[leapmuxv1.DeleteUserRequest]) (*connect.Response[leapmuxv1.DeleteUserResponse], error) {
	caller, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}

	if req.Msg.GetUserId() == caller.ID {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot delete yourself"))
	}

	user, err := s.queries.GetUserByID(ctx, req.Msg.GetUserId())
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get user: %w", err))
	}

	// Delete user (cascade will clean up related rows).
	if err := s.queries.DeleteUser(ctx, user.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete user: %w", err))
	}

	// Delete personal org.
	// Note: DeleteOrg has is_personal = 0 constraint, so we use it
	// only for non-personal orgs. For personal orgs, the cascade from
	// user deletion should handle cleanup. If not, we attempt it anyway.
	_ = s.queries.DeleteOrg(ctx, user.OrgID)

	return connect.NewResponse(&leapmuxv1.DeleteUserResponse{}), nil
}

func (s *AdminService) ResetUserPassword(ctx context.Context, req *connect.Request[leapmuxv1.ResetUserPasswordRequest]) (*connect.Response[leapmuxv1.ResetUserPasswordResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Msg.GetNewPassword()), bcrypt.DefaultCost)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	if err := s.queries.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		PasswordHash: string(hash),
		ID:           req.Msg.GetUserId(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update password: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.ResetUserPasswordResponse{}), nil
}

func (s *AdminService) GetLogLevel(ctx context.Context, req *connect.Request[leapmuxv1.GetLogLevelRequest]) (*connect.Response[leapmuxv1.GetLogLevelResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.GetLogLevelResponse{
		Level: logging.GetLevel().String(),
	}), nil
}

func (s *AdminService) SetLogLevel(ctx context.Context, req *connect.Request[leapmuxv1.SetLogLevelRequest]) (*connect.Response[leapmuxv1.SetLogLevelResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	level, err := logging.ParseLevel(req.Msg.GetLevel())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid log level: %w", err))
	}
	logging.SetLevel(level)
	return connect.NewResponse(&leapmuxv1.SetLogLevelResponse{
		Level: level.String(),
	}), nil
}

func adminUserToProto(u *db.User, orgName string) *leapmuxv1.AdminUserView {
	return &leapmuxv1.AdminUserView{
		Id:            u.ID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		Email:         u.Email,
		EmailVerified: u.EmailVerified == 1,
		IsAdmin:       u.IsAdmin == 1,
		OrgId:         u.OrgID,
		OrgName:       orgName,
		CreatedAt:     timefmt.Format(u.CreatedAt),
		UpdatedAt:     timefmt.Format(u.UpdatedAt),
	}
}

func settingsToProto(s *db.SystemSetting) *leapmuxv1.SystemSettings {
	return &leapmuxv1.SystemSettings{
		SignupEnabled:             s.SignupEnabled == 1,
		EmailVerificationRequired: s.EmailVerificationRequired == 1,
		Smtp: &leapmuxv1.SmtpConfig{
			Host:        s.SmtpHost,
			Port:        int32(s.SmtpPort),
			Username:    s.SmtpUsername,
			PasswordSet: s.SmtpPassword != "",
			FromAddress: s.SmtpFromAddress,
			UseTls:      s.SmtpUseTls == 1,
		},
		ApiTimeoutSeconds:            int32(s.ApiTimeoutSeconds),
		AgentStartupTimeoutSeconds:   int32(s.AgentStartupTimeoutSeconds),
		WorktreeCreateTimeoutSeconds: int32(s.WorktreeCreateTimeoutSeconds),
	}
}
