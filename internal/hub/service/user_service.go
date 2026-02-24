package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/validate"
	"golang.org/x/crypto/bcrypt"
)

// UserService implements the leapmux.v1.UserService ConnectRPC handler.
type UserService struct {
	queries    *db.Queries
	timeoutCfg *timeout.Config
}

// NewUserService creates a new UserService.
func NewUserService(q *db.Queries, tc *timeout.Config) *UserService {
	return &UserService{queries: q, timeoutCfg: tc}
}

func (s *UserService) UpdateProfile(ctx context.Context, req *connect.Request[leapmuxv1.UpdateProfileRequest]) (*connect.Response[leapmuxv1.UpdateProfileResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.queries.GetUserByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	newUsername := req.Msg.GetUsername()
	usernameChanged := newUsername != user.Username

	// If the username is changing, check that the new one is not already taken.
	if usernameChanged {
		existing, err := s.queries.GetUserByUsername(ctx, newUsername)
		if err != nil && err != sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if err == nil && existing.ID != user.ID {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("username %q is already taken", newUsername))
		}
	}

	if err := s.queries.UpdateUserProfile(ctx, db.UpdateUserProfileParams{
		Username:    newUsername,
		DisplayName: req.Msg.GetDisplayName(),
		Email:       req.Msg.GetEmail(),
		ID:          user.ID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &leapmuxv1.UpdateProfileResponse{
		Username:    newUsername,
		DisplayName: req.Msg.GetDisplayName(),
		Email:       req.Msg.GetEmail(),
	}

	// Rename the personal org to match the new username.
	if usernameChanged {
		if err := s.queries.UpdateOrgName(ctx, db.UpdateOrgNameParams{
			Name: newUsername,
			ID:   user.OrgID,
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		resp.OrgName = newUsername
	}

	return connect.NewResponse(resp), nil
}

func (s *UserService) ChangePassword(ctx context.Context, req *connect.Request[leapmuxv1.ChangePasswordRequest]) (*connect.Response[leapmuxv1.ChangePasswordResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.queries.GetUserByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Msg.GetCurrentPassword())); err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("current password is incorrect"))
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Msg.GetNewPassword()), bcrypt.DefaultCost)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	if err := s.queries.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		PasswordHash: string(hashed),
		ID:           user.ID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.ChangePasswordResponse{}), nil
}

func (s *UserService) GetPreferences(ctx context.Context, req *connect.Request[leapmuxv1.GetPreferencesRequest]) (*connect.Response[leapmuxv1.GetPreferencesResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	prefs, err := s.queries.GetUserPreferences(ctx, userInfo.ID)
	if err != nil {
		if err == sql.ErrNoRows {
			return connect.NewResponse(&leapmuxv1.GetPreferencesResponse{
				Preferences: &leapmuxv1.UserPreferences{},
			}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var uiFonts []string
	if err := json.Unmarshal([]byte(prefs.UiFonts), &uiFonts); err != nil {
		uiFonts = nil
	}
	var monoFonts []string
	if err := json.Unmarshal([]byte(prefs.MonoFonts), &monoFonts); err != nil {
		monoFonts = nil
	}

	return connect.NewResponse(&leapmuxv1.GetPreferencesResponse{
		Preferences: &leapmuxv1.UserPreferences{
			Theme:                 prefs.Theme,
			TerminalTheme:         prefs.TerminalTheme,
			UiFontCustomEnabled:   prefs.UiFontCustomEnabled != 0,
			MonoFontCustomEnabled: prefs.MonoFontCustomEnabled != 0,
			UiFonts:               uiFonts,
			MonoFonts:             monoFonts,
			DiffView:              leapmuxv1.DiffView(prefs.DiffView),
			TurnEndSound:          leapmuxv1.TurnEndSound(prefs.TurnEndSound),
			TurnEndSoundVolume:    uint32(prefs.TurnEndSoundVolume),
		},
	}), nil
}

func (s *UserService) GetTimeouts(ctx context.Context, req *connect.Request[leapmuxv1.GetTimeoutsRequest]) (*connect.Response[leapmuxv1.GetTimeoutsResponse], error) {
	if _, err := auth.MustGetUser(ctx); err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.GetTimeoutsResponse{
		ApiTimeoutSeconds:            int32(s.timeoutCfg.APITimeout().Seconds()),
		AgentStartupTimeoutSeconds:   int32(s.timeoutCfg.AgentStartupTimeout().Seconds()),
		WorktreeCreateTimeoutSeconds: int32(s.timeoutCfg.WorktreeCreateTimeout().Seconds()),
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

	uiFontsJSON, err := json.Marshal(uiFonts)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal ui_fonts: %w", err))
	}
	monoFontsJSON, err := json.Marshal(monoFonts)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal mono_fonts: %w", err))
	}

	var uiFontCustomEnabled int64
	if req.Msg.GetUiFontCustomEnabled() {
		uiFontCustomEnabled = 1
	}
	var monoFontCustomEnabled int64
	if req.Msg.GetMonoFontCustomEnabled() {
		monoFontCustomEnabled = 1
	}

	if err := s.queries.UpsertUserPreferences(ctx, db.UpsertUserPreferencesParams{
		UserID:                userInfo.ID,
		Theme:                 req.Msg.GetTheme(),
		TerminalTheme:         req.Msg.GetTerminalTheme(),
		UiFontCustomEnabled:   uiFontCustomEnabled,
		MonoFontCustomEnabled: monoFontCustomEnabled,
		UiFonts:               string(uiFontsJSON),
		MonoFonts:             string(monoFontsJSON),
		DiffView:              int64(req.Msg.GetDiffView()),
		TurnEndSound:          int64(req.Msg.GetTurnEndSound()),
		TurnEndSoundVolume:    int64(req.Msg.GetTurnEndSoundVolume()),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.UpdatePreferencesResponse{
		Preferences: &leapmuxv1.UserPreferences{
			Theme:                 req.Msg.GetTheme(),
			TerminalTheme:         req.Msg.GetTerminalTheme(),
			UiFontCustomEnabled:   req.Msg.GetUiFontCustomEnabled(),
			MonoFontCustomEnabled: req.Msg.GetMonoFontCustomEnabled(),
			UiFonts:               req.Msg.GetUiFonts(),
			MonoFonts:             req.Msg.GetMonoFonts(),
			DiffView:              req.Msg.GetDiffView(),
			TurnEndSound:          req.Msg.GetTurnEndSound(),
			TurnEndSoundVolume:    req.Msg.GetTurnEndSoundVolume(),
		},
	}), nil
}
