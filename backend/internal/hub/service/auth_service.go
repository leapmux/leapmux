package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/util/validate"
	"github.com/leapmux/leapmux/util/version"
)

// CaptchaService is the captcha surface AuthService consumes. The hub
// passes the SAME *captcha.Manager the captcha interceptor enforces with,
// so challenge issuance and enforcement cannot disagree; tests pass a stub.
type CaptchaService interface {
	Describe(ctx context.Context) (captcha.Config, bool, error)
	// AltchaChallengeJSON is ALTCHA-specific by name because it is by
	// behavior: it returns "" when the selected provider is external
	// (those mint tokens client-side and have nothing to issue).
	AltchaChallengeJSON(ctx context.Context) (string, error)
}

// disabledCaptcha serves the nil Captcha case: test and embedding wiring
// without the captcha subsystem report captcha as disabled and issue no
// challenges.
type disabledCaptcha struct{}

func (disabledCaptcha) Describe(context.Context) (captcha.Config, bool, error) {
	cfg := captcha.DefaultConfig()
	cfg.Enabled = false
	return cfg, false, nil
}

func (disabledCaptcha) AltchaChallengeJSON(context.Context) (string, error) {
	return "", nil
}

// AuthServiceDeps carries AuthService's collaborators. The struct keeps
// the constructor readable as dependencies accumulate, and Captcha is an
// interface with a disabled default so callers omit it without nil checks.
type AuthServiceDeps struct {
	Store     store.Store
	Config    *config.Config
	Lifecycle *auth.CredentialLifecycleEffects
	Keystore  *keystore.Keystore
	Mail      mail.Sender
	Renderer  mail.Renderer
	Captcha   CaptchaService // nil reports captcha as disabled
}

// AuthService implements the leapmux.v1.AuthService ConnectRPC handler.
type AuthService struct {
	store      store.Store
	cfg        *config.Config
	lifecycle  *auth.CredentialLifecycleEffects
	keystore   *keystore.Keystore
	mail       mail.Sender
	renderer   mail.Renderer
	captcha    CaptchaService
	hasAnyUser atomic.Bool // one-way latch: once true, never re-queried
}

// NewAuthService creates a new AuthService. renderer carries the hub's
// public URL used to build absolute deep-links in verification emails.
func NewAuthService(deps AuthServiceDeps) *AuthService {
	if deps.Lifecycle == nil {
		panic("auth service requires credential lifecycle effects")
	}
	captchaSvc := deps.Captcha
	if captchaSvc == nil {
		captchaSvc = disabledCaptcha{}
	}
	return &AuthService{store: deps.Store, cfg: deps.Config, lifecycle: deps.Lifecycle, keystore: deps.Keystore, mail: deps.Mail, renderer: deps.Renderer, captcha: captchaSvc}
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

// setSessionCookie writes the cookie for a session that this response
// established. One helper for the four mint paths -- login, both sign-up
// branches, and the OAuth signup completion -- so the secure-name choice and
// the header write cannot drift apart between them.
//
// Set, not Add: this cookie is the response's answer about the session, and it
// replaces anything already written for that name. The interceptor's slide
// refresh stands back from a response that already carries a session cookie,
// which is what keeps these two writers from fighting.
func (s *AuthService) setSessionCookie(h http.Header, sessionID string, expiresAt time.Time) {
	h.Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt, s.cfg.SecureCookies).String())
}

func (s *AuthService) Login(ctx context.Context, req *connect.Request[leapmuxv1.LoginRequest]) (*connect.Response[leapmuxv1.LoginResponse], error) {
	token, user, expiresAt, err := auth.Login(ctx, s.store, req.Msg.GetUsername(), req.Msg.GetPassword(), s.cfg.SessionDuration)
	if err != nil {
		return nil, err
	}

	resp := connect.NewResponse(&leapmuxv1.LoginResponse{
		User: userToProto(user),
	})
	s.setSessionCookie(resp.Header(), token, expiresAt)
	return resp, nil
}

func (s *AuthService) Logout(ctx context.Context, req *connect.Request[leapmuxv1.LogoutRequest]) (*connect.Response[leapmuxv1.LogoutResponse], error) {
	token := auth.SessionIDFromHeader(req.Header().Get("Cookie"), s.cfg.SecureCookies)
	if token != "" {
		if _, err := s.store.Sessions().Delete(ctx, token); err != nil {
			connectErr := connect.NewError(connect.CodeInternal, fmt.Errorf("delete session: %w", err))
			connectErr.Meta().Set("Set-Cookie", auth.ClearSessionCookie(s.cfg.SecureCookies).String())
			return nil, connectErr
		}
		s.lifecycle.SessionRevoked(token)
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

	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var linkedProviders []*leapmuxv1.LinkedOAuthProvider
	links, _ := s.store.OAuthUserLinks().ListByUser(ctx, userInfo.ID)
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
		User: userToProtoWithOAuth(user, linkedProviders),
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

	if err := CheckUsernameAvailable(ctx, s.store, username); err != nil {
		return nil, AvailabilityConnectError(err)
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
			return nil, AvailabilityConnectError(err)
		}

		user, err := CreateUser(ctx, s.store, CreateUserParams{
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
		emailSent, err := issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, user.ID, email)
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
		uid, mintErr := mintRowUserID(user.ID)
		if mintErr != nil {
			return nil, mintErr
		}
		sessionID, sessionExpires, err := auth.CreateSession(ctx, s.store, uid, s.cfg.SessionDuration)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
		}
		resp := connect.NewResponse(&leapmuxv1.SignUpResponse{
			User:                  userToProto(updatedUser),
			VerificationRequired:  true,
			VerificationEmailSent: emailSent,
		})
		s.setSessionCookie(resp.Header(), sessionID, sessionExpires)
		return resp, nil
	}

	// No verification required — email goes directly to email column.
	if email != "" {
		if err := CheckEmailAvailable(ctx, s.store, email, ""); err != nil {
			return nil, AvailabilityConnectError(err)
		}
	}

	user, err := CreateUser(ctx, s.store, CreateUserParams{
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

	return s.signUpResponse(ctx, user)
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
			return nil, AvailabilityConnectError(err)
		}
	}

	user, err := CreateUser(ctx, s.store, CreateUserParams{
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
	return s.signUpResponse(ctx, user)
}

// signUpResponse creates a session, sets the cookie, and returns the SignUpResponse.
func (s *AuthService) signUpResponse(ctx context.Context, user *store.User) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	loginUID, err := mintRowUserID(user.ID)
	if err != nil {
		return nil, err
	}
	sessionID, expiresAt, err := auth.CreateSession(ctx, s.store, loginUID, s.cfg.SessionDuration)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}

	resp := connect.NewResponse(&leapmuxv1.SignUpResponse{
		User: userToProto(user),
	})
	s.setSessionCookie(resp.Header(), sessionID, expiresAt)
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

	// A captcha-config read failure degrades to "disabled" instead of
	// failing the whole endpoint, mirroring the providers flag above: the
	// rest of the system info stays usable, and the captcha interceptor
	// still fails closed on its own for the procedures it protects.
	//
	// The provider is zero (UNSPECIFIED) on that degraded path — never a
	// wrong concrete provider — and clients treat anything but a known
	// enum as altcha.
	var captchaEnabled bool
	var captchaProvider leapmuxv1.CaptchaProvider
	var captchaSiteKey string
	var altchaAlgorithm string
	captchaCfg, _, err := s.captcha.Describe(ctx)
	if err != nil {
		slog.Warn("describe captcha config failed; reporting captcha disabled", "error", err)
	} else {
		captchaEnabled = captchaCfg.Enabled
		captchaProvider = captchaCfg.Provider
		captchaSiteKey = captchaCfg.SiteKey()
		altchaAlgorithm = captchaCfg.AltchaAlgorithm()
	}

	// Decide what URL workers should target. Precedence:
	//   1. An explicit --public-url wins (admin's canonical external URL,
	//      typically used when the hub is behind a reverse proxy).
	//   2. If TCP is disabled (desktop's NoTCP mode), the browser origin is
	//      `tauri://localhost`, which is unusable; emit the local unix-socket
	//      / named-pipe address so workers can dial the hub locally.
	//   3. Otherwise leave it empty — the frontend falls back to
	//      window.location.origin, which already reflects whatever proxy or
	//      hostname the user is connecting through.
	var workerHubURL string
	switch {
	case s.cfg.PublicURL != "":
		workerHubURL = s.cfg.PublicURL
	case s.cfg.Listen == "":
		if u, err := s.cfg.LocalListenURL(); err == nil {
			workerHubURL = u
		}
	}

	return connect.NewResponse(&leapmuxv1.GetSystemInfoResponse{
		SignupEnabled:   s.cfg.SignupEnabled,
		SoloMode:        s.cfg.SoloMode,
		SetupRequired:   setupRequired,
		Version:         version.Value,
		CommitHash:      version.CommitHash,
		CommitTime:      version.CommitTime,
		BuildTime:       version.BuildTime,
		Branch:          version.Branch,
		OauthEnabled:    len(providers) > 0,
		WorkerHubUrl:    workerHubURL,
		EmailEnabled:    s.cfg.SmtpHost != "",
		CaptchaEnabled:  captchaEnabled,
		AltchaAlgorithm: altchaAlgorithm,
		CaptchaProvider: captchaProvider,
		CaptchaSiteKey:  captchaSiteKey,
	}), nil
}

// GetAltchaChallenge issues a fresh ALTCHA challenge for the caller to
// solve before submitting Login/SignUp-family requests. It is public: the
// challenge carries no secret, and issuance costs one HMAC. Empty when
// captcha is disabled; FailedPrecondition when another provider is
// selected (those mint tokens client-side, so the caller is on a stale
// snapshot and must re-fetch the system info).
func (s *AuthService) GetAltchaChallenge(ctx context.Context, req *connect.Request[leapmuxv1.GetAltchaChallengeRequest]) (*connect.Response[leapmuxv1.GetAltchaChallengeResponse], error) {
	challengeJSON, err := s.captcha.AltchaChallengeJSON(ctx)
	if err != nil {
		if errors.Is(err, captcha.ErrProviderNotAltcha) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.GetAltchaChallengeResponse{
		ChallengeJson: challengeJSON,
	}), nil
}

func (s *AuthService) GetOAuthProviders(ctx context.Context, req *connect.Request[leapmuxv1.GetOAuthProvidersRequest]) (*connect.Response[leapmuxv1.GetOAuthProvidersResponse], error) {
	providers, err := s.store.OAuthProviders().ListEnabled(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Sourced from cfg directly, not s.renderer.HubURL — the renderer is
	// the email-templating seam; OAuth login URLs are an unrelated kind
	// of absolute URL the OAuth flow needs the frontend to redirect to.
	// They happen to resolve to the same value today, but they're
	// conceptually different and should not be conflated.
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
	if auth.IsExpired(time.Now().UTC(), pending.ExpiresAt) {
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

	if err := CheckUsernameAvailable(ctx, s.store, username); err != nil {
		return nil, AvailabilityConnectError(err)
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
			return nil, AvailabilityConnectError(err)
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

	user, err := CreateUser(ctx, s.store, CreateUserParams{
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
		sent, err := issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, user.ID, pendingEmail)
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

	linkUID, err := mintRowUserID(user.ID)
	if err != nil {
		return nil, err
	}
	if err := s.store.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
		UserID:          linkUID,
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

	finalUID, mintErr := mintRowUserID(finalUser.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, s.store, finalUID, s.cfg.SessionDuration)
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", sessionErr))
	}

	resp := connect.NewResponse(&leapmuxv1.CompleteOAuthSignupResponse{
		User:                  userToProto(finalUser),
		VerificationRequired:  pendingEmail != "",
		VerificationEmailSent: emailSent,
	})
	s.setSessionCookie(resp.Header(), sessionID, expiresAt)
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

	tokUID, mintErr := mintRowUserID(userID)
	if mintErr != nil {
		return mintErr
	}
	return st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
		UserID:       tokUID,
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
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		IsAdmin:       u.IsAdmin,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		PendingEmail:  u.PendingEmail,
		PasswordSet:   u.PasswordSet,
	}
}

func userToProtoWithOAuth(u *store.User, oauthProviders []*leapmuxv1.LinkedOAuthProvider) *leapmuxv1.User {
	p := userToProto(u)
	p.OauthProviders = oauthProviders
	return p
}
