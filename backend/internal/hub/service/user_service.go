package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/validate"
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
}

// UserService implements the leapmux.v1.UserService ConnectRPC handler.
type UserService struct {
	queries      *db.Queries
	cfg          *config.Config
	sessionCache *auth.SessionCache
}

// NewUserService creates a new UserService.
func NewUserService(q *db.Queries, cfg *config.Config, sc *auth.SessionCache) *UserService {
	return &UserService{queries: q, cfg: cfg, sessionCache: sc}
}

func (s *UserService) UpdateProfile(ctx context.Context, req *connect.Request[leapmuxv1.UpdateProfileRequest]) (*connect.Response[leapmuxv1.UpdateProfileResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("profile changes are not available in solo mode"))
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.queries.GetUserByID(ctx, userInfo.ID)
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
		DisplayName: displayName,
		ID:          user.ID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &leapmuxv1.UpdateProfileResponse{
		Username:     newUsername,
		DisplayName:  displayName,
		Email:        user.Email,
		PendingEmail: user.PendingEmail,
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

	user, err := s.queries.GetUserByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if newEmail == user.Email {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("email is unchanged"))
	}

	// Check that no other user has this email.
	if err := checkEmailAvailable(ctx, s.queries, newEmail, user.ID); err != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	}

	// Admin: immediate change, trusted.
	if userInfo.IsAdmin {
		if err := setEmailAndClearCompeting(ctx, s.queries, user.ID, newEmail, 1); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&leapmuxv1.RequestEmailChangeResponse{
			VerificationRequired: false,
		}), nil
	}

	// Non-admin, verification not required: immediate change, unverified.
	if !s.cfg.EmailVerificationRequired {
		if err := setEmailAndClearCompeting(ctx, s.queries, user.ID, newEmail, 0); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&leapmuxv1.RequestEmailChangeResponse{
			VerificationRequired: false,
		}), nil
	}

	// Non-admin, verification required: set pending email (stub: auto-verifies).
	if err := setPendingEmailWithToken(ctx, s.queries, user.ID, newEmail); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.RequestEmailChangeResponse{
		VerificationRequired: true,
	}), nil
}

func (s *UserService) VerifyEmailChange(ctx context.Context, req *connect.Request[leapmuxv1.VerifyEmailChangeRequest]) (*connect.Response[leapmuxv1.VerifyEmailChangeResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	updatedUser, err := verifyPendingEmailToken(ctx, s.queries, req.Msg.GetVerificationToken())
	if err != nil {
		return nil, err
	}

	if updatedUser.ID != userInfo.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("token does not belong to this user"))
	}

	// Evict so the next request picks up the updated EmailVerified status.
	s.sessionCache.Evict(userInfo.SessionID)

	org, err := s.queries.GetOrgByID(ctx, updatedUser.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.VerifyEmailChangeResponse{
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

	user, err := s.queries.GetUserByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if err := validate.ValidatePassword(req.Msg.GetNewPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// OAuth-only users (password_set == 0) can set a password without providing
	// the current one. Users with a password must verify it first.
	if user.PasswordSet == 1 {
		match, err := password.Verify(user.PasswordHash, req.Msg.GetCurrentPassword())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("verify password: %w", err))
		}
		if !match {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("current password is incorrect"))
		}
	}

	hashed, err := password.Hash(req.Msg.GetNewPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	if err := s.queries.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		PasswordHash: hashed,
		ID:           user.ID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Invalidate all other sessions so stolen sessions can't survive a
	// password change. Keep the current session alive.
	_ = s.queries.DeleteOtherUserSessions(ctx, db.DeleteOtherUserSessionsParams{
		UserID: user.ID,
		ID:     userInfo.SessionID,
	})

	// Evict all cached sessions for this user so that deleted sessions
	s.sessionCache.EvictByUserID(user.ID)

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

	user, err := s.queries.GetUserByID(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	links, err := s.queries.ListOAuthUserLinksByUser(ctx, user.ID)
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
	if len(links) <= 1 && user.PasswordSet == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot unlink your only login method; set a password first"))
	}

	if err := s.queries.DeleteOAuthUserLink(ctx, db.DeleteOAuthUserLinkParams{
		UserID:     user.ID,
		ProviderID: providerID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.UnlinkOAuthProviderResponse{}), nil
}

func (s *UserService) GetPreferences(ctx context.Context, req *connect.Request[leapmuxv1.GetPreferencesRequest]) (*connect.Response[leapmuxv1.GetPreferencesResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	prefs, err := s.queries.GetUserPrefs(ctx, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var sp storedPreferences
	if err := json.Unmarshal([]byte(prefs), &sp); err != nil {
		sp = storedPreferences{}
	}

	return connect.NewResponse(&leapmuxv1.GetPreferencesResponse{
		Preferences: &leapmuxv1.UserPreferences{
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
		},
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
	}

	prefsJSON, err := json.Marshal(sp)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal prefs: %w", err))
	}

	if err := s.queries.UpdateUserPrefs(ctx, db.UpdateUserPrefsParams{
		Prefs: string(prefsJSON),
		ID:    userInfo.ID,
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
			TurnEndSoundVolume:    req.Msg.TurnEndSoundVolume,
			DebugLogging:          req.Msg.GetDebugLogging(),
		},
	}), nil
}
