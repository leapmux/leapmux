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
	"github.com/leapmux/leapmux/internal/hub/listenset"
	"github.com/leapmux/leapmux/internal/hub/mail"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
	"github.com/leapmux/leapmux/util/version"
)

// CaptchaService is the captcha surface AuthService consumes. The hub
// passes the SAME *captcha.Manager the captcha interceptor enforces with,
// so challenge issuance and enforcement cannot disagree; tests pass a stub.
type CaptchaService interface {
	Describe(ctx context.Context) captcha.Config
	// AltchaChallengeJSON is ALTCHA-specific by name because it is by
	// behavior: it returns "" when the selected provider is external
	// (those mint tokens client-side and have nothing to issue).
	AltchaChallengeJSON(ctx context.Context) (string, error)
}

// disabledCaptcha serves the nil Captcha case: test and embedding wiring
// without the captcha subsystem report captcha as disabled and issue no
// challenges.
type disabledCaptcha struct{}

func (disabledCaptcha) Describe(context.Context) captcha.Config {
	return captcha.DisabledConfig()
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
	Settings  *settings.Manager
	Lifecycle *auth.CredentialLifecycleEffects
	Keystore  *keystore.Keystore
	Mail      mail.Sender
	Renderer  mail.Renderer
	Captcha   CaptchaService // nil reports captcha as disabled
	// Listen reports the hub's live listeners: the address a browser reaches
	// it at, and whether any of them is reachable from another machine.
	//
	// Nil falls back to Config.Listen for the address and to "no" for the
	// reach, which is exact everywhere the two cannot differ: extra listen
	// addresses are hidden_in_hub, so only a solo hub has any.
	Listen ListenReporter
	// SoloGate answers whether THIS connection is authenticated without
	// credentials, and whether the single account holds a password. Nil
	// outside solo mode.
	SoloGate *auth.SoloGate
}

// AuthService implements the leapmux.v1.AuthService ConnectRPC handler.
type AuthService struct {
	store      store.Store
	cfg        *config.Config
	set        *settings.Manager
	lifecycle  *auth.CredentialLifecycleEffects
	keystore   *keystore.Keystore
	mail       mail.Sender
	renderer   mail.Renderer
	captcha    CaptchaService
	listen     ListenReporter
	soloGate   *auth.SoloGate
	hasAnyUser atomic.Bool // one-way latch: once true, never re-queried

	// The clock GetCurrentUser reports the elevation deadline against. It
	// must be the same seam UserService grants on, or a test that advances
	// one reads a deadline the other never wrote.
	clockSeam
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
	// A nil reporter is a hub with no listener set to ask, so it answers from
	// the address -listen gave; see ConfiguredListen for why that default is
	// stated once rather than at each read.
	listen := deps.Listen
	if listen == nil {
		configured := ""
		if deps.Config != nil {
			configured = deps.Config.Listen
		}
		listen = ConfiguredListen{Listen: configured}
	}
	return &AuthService{store: deps.Store, cfg: deps.Config, set: deps.Settings, lifecycle: deps.Lifecycle, keystore: deps.Keystore, mail: deps.Mail, renderer: deps.Renderer, captcha: captchaSvc, listen: listen, soloGate: deps.SoloGate}
}

// snap resolves the current settings snapshot for the settings-backed
// values this service reads per request.
func (s *AuthService) snap(ctx context.Context) *settings.Snapshot {
	return s.set.Snapshot(ctx)
}

// secureCookies reads the cookie-name policy for the current request.
func (s *AuthService) secureCookies(ctx context.Context) bool {
	return settings.KeySecureCookies.Of(s.snap(ctx))
}

// sessionDuration reads the sliding session lifetime.
func (s *AuthService) sessionDuration(ctx context.Context) time.Duration {
	return settings.SessionDuration(s.snap(ctx))
}

// signupEnabled reads the signup gate, with dev mode's open-signup
// default applied at read time.
func (s *AuthService) signupEnabled(ctx context.Context) bool {
	return settings.SignupEnabledEffective(s.snap(ctx), s.cfg.DevMode)
}

// emailVerificationRequired reports whether the hub requires a verified
// email before an account may act. Verification follows SMTP
// (settings.EmailVerificationEffective): without a mail transport there is
// no channel to verify through.
func (s *AuthService) emailVerificationRequired(ctx context.Context) bool {
	return settings.EmailVerificationEffective(s.snap(ctx))
}

// baseURL derives the hub's public base URL for deep-links.
func (s *AuthService) baseURL(ctx context.Context) string {
	return settings.BaseURL(s.snap(ctx), s.listenAddr())
}

// soloPasswordSetupRequired reports whether the app must block itself with a
// password-setup screen.
//
// The condition is EXPOSURE without a credential: the hub answers on an
// address another machine can reach, and its one account has no password. In
// that state everything the app offers is offered to whoever reaches the port,
// and no sign-in stands between them -- so the one useful thing the app can do
// is ask for a password.
//
// A loopback-only hub does NOT trigger it. `leapmux solo` with no -listen, and
// `leapmux solo -listen 127.0.0.1:5000`, expose nothing, so demanding a
// password there would be friction with nothing behind it.
//
// It takes the gate's two answers rather than reading them again: its one
// caller reports three solo facts from the same row, and asking a second time
// costs a second store read on every hub that has no password yet.
//
// credentialFree is what carries the MODE, and it must stay in the condition.
// A gate that is not solo answers false to it, so a multi-user hub with a
// non-loopback listener reports false here -- where testing `!passwordSet`
// alone would report TRUE and replace the whole app with the password-setup
// screen for every visitor to `leapmux hub`.
func (s *AuthService) soloPasswordSetupRequired(credentialFree, passwordSet bool) bool {
	if !credentialFree || passwordSet {
		return false
	}
	return listenset.AnyNonLoopback(s.listen.Bound())
}

// listenAddr is the TCP address a browser reaches this hub at, in the form
// -listen carries.
//
// The REPORTER owns the rule -- the live primary address, and the configured
// one when nothing is bound -- so this is a plain read. Spelling the fallback
// here as well is what let the mail links, the OAuth issuer URL, the banner
// and GetSystemInfo disagree about which address this hub is at.
func (s *AuthService) listenAddr() string {
	return s.listen.PrimaryListenAddr()
}

// checkHasAnyUser returns true if at least one user exists. A one-way latch
// caches the result: once true, nothing queries the DB again (users cannot
// be un-created).
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
// refresh does not touch a response that already carries a session cookie,
// which is what keeps these two writers from overwriting each other.
func (s *AuthService) setSessionCookie(ctx context.Context, h http.Header, sessionID string, expiresAt time.Time) {
	h.Set("Set-Cookie", auth.BuildSessionCookie(sessionID, expiresAt, s.secureCookies(ctx)).String())
}

func (s *AuthService) Login(ctx context.Context, req *connect.Request[leapmuxv1.LoginRequest]) (*connect.Response[leapmuxv1.LoginResponse], error) {
	token, user, expiresAt, err := auth.Login(ctx, s.store, req.Msg.GetUsername(), req.Msg.GetPassword(), s.sessionDuration(ctx))
	if err != nil {
		return nil, err
	}

	resp := connect.NewResponse(&leapmuxv1.LoginResponse{})
	resp.Msg.User = userToProtoWithPasskeys(ctx, s.store, user)
	resp.Msg.EmailVerification = emailVerificationToProto(s.loginVerificationOutcome(ctx, user))
	s.setSessionCookie(ctx, resp.Header(), token, expiresAt)
	return resp, nil
}

func (s *AuthService) Logout(ctx context.Context, req *connect.Request[leapmuxv1.LogoutRequest]) (*connect.Response[leapmuxv1.LogoutResponse], error) {
	token := auth.SessionIDFromCookieHeader(req.Header().Get("Cookie"), s.secureCookies(ctx))
	if token != "" {
		if _, err := s.store.Sessions().Delete(ctx, token); err != nil {
			connectErr := connect.NewError(connect.CodeInternal, fmt.Errorf("delete session: %w", err))
			connectErr.Meta().Set("Set-Cookie", auth.ClearSessionCookie(s.secureCookies(ctx)).String())
			return nil, connectErr
		}
		s.lifecycle.SessionRevoked(token)
	}
	resp := connect.NewResponse(&leapmuxv1.LogoutResponse{})
	resp.Header().Set("Set-Cookie", auth.ClearSessionCookie(s.secureCookies(ctx)).String())
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

	// ONE count, for the number this REPORTS and for the step-up option rule
	// below. Best-effort for the same reason userToProtoWithPasskeys is:
	// this runs at every page load, so a transient count failure must not
	// fail a request whose real work is elsewhere.
	//
	// The two readers take the failure differently, and they must. The
	// reported number degrades to zero, which is a stale count on a screen.
	// The RULE takes no answer at all: a zero that a failed read produced is
	// not a fact about the account, and feeding it to the predicate below
	// reports a passkey-only account as "verify through your provider", which
	// providerMayElevateAccount then re-reads from the store and refuses. The
	// client would have hidden the option that works and offered the one that
	// cannot.
	passkeyCount, counted := countPasskeysBestEffort(ctx, s.store, user)
	// The step-up option rule, from the ONE predicate that decides it. It
	// reads the two facts already in hand rather than paying a second COUNT.
	//
	// An unread count fails CLOSED, so the screen offers no option rather
	// than only a refused one. The user still reaches every factor the
	// account really holds through the ordinary prompt, and the next page
	// load answers the question properly.
	elevatesOnlyThroughAProvider := counted && accountShapeElevatesOnlyThroughAProvider(user.PasswordSet, passkeyCount)

	// The link read REPORTS its failure only for the account shape that has
	// nothing else to fall back on, and degrades for every other.
	//
	// An empty list is indistinguishable from "this account has no linked
	// provider". For an account that holds neither a password nor a passkey
	// the provider IS its only step-up option, so the empty list becomes
	// "this account has nothing to verify with yet" on the screen the
	// command-line consent leg just bounced the user to -- a false and
	// alarming answer to a transient read.
	//
	// Every other account keeps a factor it can present, so the same failure
	// costs it a missing Linked Accounts section until the next page load. It
	// must NOT cost it the whole session: this runs on every page load, and a
	// non-Unauthenticated failure leaves the client on a bootstrap error with
	// no way into the app -- for a query most accounts never have a row for.
	// An account whose passkey count did not read is in that second group by
	// construction, because the shape above is false for it -- so one failed
	// COUNT cannot turn this page load into a fatal one.
	linkedProviders, err := s.linkedProvidersFor(ctx, userInfo.ID)
	if err != nil {
		if elevatesOnlyThroughAProvider {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		slog.WarnContext(ctx, "could not list the account's linked OAuth providers",
			"user_id", user.ID, "err", err)
	}

	userProto := userToProto(user, passkeyCount)
	userProto.OauthProviders = linkedProviders
	userProto.MayElevateThroughAProvider = elevatesOnlyThroughAProvider
	return connect.NewResponse(&leapmuxv1.GetCurrentUserResponse{
		User: userProto,
		// Reported, never sent: this runs on every page load.
		EmailVerification: emailVerificationToProto(s.verificationStatusFor(ctx, user)),
		// Evaluated at NOW, not when the UserInfo was cached, so a window
		// that lapsed inside the auth cache's lifetime is reported as
		// absent rather than as a deadline in the past.
		ElevationExpiresAt: elevationExpiresAtProto(userInfo, s.now()),
	}), nil
}

// linkedProvidersFor lists the account's OAuth links, each with whether an
// administrator currently has that provider enabled.
//
// It REPORTS a failure rather than discarding it, and that is the difference
// from the passkey count beside it. The count's zero costs a wasted round
// trip; an empty provider list is indistinguishable from "this account has no
// linked provider", and for an account that holds neither a password nor a
// passkey that is the ONLY step-up option -- so a transient store failure
// told the user their account had nothing to verify with at all, on the
// screen the command-line consent leg just bounced them to. An error the
// client retries is the honest answer.
//
// A disabled provider's link is INCLUDED. See LinkedOAuthProvider.enabled: the
// owner must still be able to detach a link whose provider an administrator
// turned off, and the verification screen filters on the flag instead.
func (s *AuthService) linkedProvidersFor(ctx context.Context, userID userid.UserID) ([]*leapmuxv1.LinkedOAuthProvider, error) {
	links, err := s.store.OAuthUserLinks().ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list the account's OAuth links: %w", err)
	}
	if len(links) == 0 {
		return nil, nil
	}
	// ListAll is acceptable here: the number of configured OAuth providers is
	// typically in the single digits, and adding a GetByIDs method to every
	// backend is not worth the complexity.
	providers, err := s.store.OAuthProviders().ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the configured OAuth providers: %w", err)
	}
	rows := make(map[string]store.OAuthProviderSummary, len(providers))
	for _, p := range providers {
		rows[p.ID] = p
	}
	out := make([]*leapmuxv1.LinkedOAuthProvider, 0, len(links))
	for _, link := range links {
		row, ok := rows[link.ProviderID]
		if !ok {
			// An ABSENT row is different from a disabled one: the provider
			// was removed, so there is no name to render and nothing the
			// link can ever reach again.
			continue
		}
		out = append(out, &leapmuxv1.LinkedOAuthProvider{
			Id:      link.ProviderID,
			Name:    row.Name,
			Enabled: row.Enabled,
		})
	}
	return out, nil
}

func (s *AuthService) SignUp(ctx context.Context, req *connect.Request[leapmuxv1.SignUpRequest]) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "sign-up"); err != nil {
		return nil, err
	}

	// The hub always creates the first user as an admin, regardless of whether
	// signup is enabled globally.
	hasUser, err := s.checkHasAnyUser(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
	}
	isSetupMode := !hasUser
	if !isSetupMode && !s.signupEnabled(ctx) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}

	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// This check rejects `solo` in every mode, and `admin` only outside setup
	// mode.
	// usernames.IsReservedForSignup states both rules and their reasons.
	if usernames.IsReservedForSignup(username, isSetupMode) {
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
	if err := s.validateSignupEmail(ctx, req.Msg.GetEmail()); err != nil {
		return nil, err
	}
	email := req.Msg.GetEmail()
	hash, err := pwdhash.Hash(pw)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}

	if isSetupMode {
		return s.signUpSetupMode(ctx, username, displayName, email, hash)
	}

	if s.emailVerificationRequired(ctx) {
		createdUser, storedCode, err := createUserInTx(ctx, s.store, createUserTxParams{
			username:     username,
			displayName:  displayName,
			pendingEmail: email,
			passwordHash: hash,
			passwordSet:  true,
			now:          s.now,
		})
		if err != nil {
			return nil, mapSignupCommitError(err)
		}
		if err := s.deliverSignupVerification(ctx, createdUser.ID, email, storedCode); err != nil {
			return nil, err
		}
		nextResend := pendingResendDeadline(s.now(), createdUser.PendingEmailUnblockedAt)

		uid, mintErr := mintRowUserID(createdUser.ID)
		if mintErr != nil {
			return nil, mintErr
		}
		sessionID, sessionExpires, err := auth.CreateSession(ctx, s.store, uid, s.sessionDuration(ctx))
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
		}
		resp := connect.NewResponse(&leapmuxv1.SignUpResponse{
			// A password sign-up carries no passkeys yet.
			User: userToProto(createdUser, 0),
			EmailVerification: emailVerificationToProto(verificationOutcome{
				Required:              true,
				EmailSent:             true,
				NextResendAvailableAt: &nextResend,
			}),
		})
		s.setSessionCookie(ctx, resp.Header(), sessionID, sessionExpires)
		s.hasAnyUser.Store(true)
		return resp, nil
	}

	// No verification required — email goes directly to email column.
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

// signUpSetupMode creates the initial admin account when no users exist yet.
// The first user is always an administrator, and the address lands
// UNVERIFIED: nobody confirmed it, and the column records only what somebody
// confirmed. The login gate takes its own exemption through
// auth.EmailVerificationFacts.Satisfied, so nothing blocks the administrator
// -- but account recovery stays closed for that address until they verify it
// from Preferences › Account.
func (s *AuthService) signUpSetupMode(ctx context.Context, username, displayName, email, passwordHash string) (*connect.Response[leapmuxv1.SignUpResponse], error) {
	// Re-check to handle the race condition where another request created a
	// user between the initial check and now.
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
		Username:     username,
		PasswordHash: passwordHash,
		DisplayName:  displayName,
		Email:        email,
		PasswordSet:  true,
		IsAdmin:      true,
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
	sessionID, expiresAt, err := auth.CreateSession(ctx, s.store, loginUID, s.sessionDuration(ctx))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}

	resp := connect.NewResponse(&leapmuxv1.SignUpResponse{
		// Both callers are password sign-ups: no passkeys yet.
		User: userToProto(user, 0),
	})
	s.setSessionCookie(ctx, resp.Header(), sessionID, expiresAt)
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

	// A captcha-config read failure reports captcha as ENABLED, matching
	// the interceptor's fail-closed enforcement on the same store: the
	// opposite polarity would unblock a payload-less submit that the hub
	// then denies, so the form would loop for ever on a mislabeled error. The
	// provider is zero (UNSPECIFIED) on that degraded path — never a
	// wrong concrete provider — and clients treat anything but a known
	// enum as altcha. The rest of the system info stays usable, mirroring
	// the providers flag above.
	captchaCfg := s.captcha.Describe(ctx)
	captchaEnabled := captchaCfg.Enabled
	captchaProvider := captchaCfg.Provider
	captchaSiteKey := captchaCfg.SiteKey()
	altchaAlgorithm := captchaCfg.AltchaAlgorithm()

	// Decide what URL workers should target. Precedence:
	//   1. An explicit public_url setting wins (admin's canonical external URL,
	//      typically used when the hub is behind a reverse proxy).
	//   2. If -listen gave no TCP address (desktop's NoTCP mode), the browser
	//      origin is `tauri://localhost`, which is unusable. Such a hub can
	//      still hold a TCP address the extra_listen_addresses setting added,
	//      so prefer that one and fall back to the local unix-socket /
	//      named-pipe address, which workers can always dial locally.
	//   3. Otherwise leave it empty — the frontend falls back to
	//      window.location.origin, which already reflects whatever proxy or
	//      hostname the user connects through.
	//
	// Case 2 tests cfg.Listen and NOT the live address. They differ on exactly
	// the deployment this matters for: a desktop hub that gained an extra
	// address has a live address, so testing that one dropped the case and
	// answered "" -- and the frontend's window.location.origin fallback, which
	// case 3 rests on, is `tauri://localhost` there.
	var workerHubURL string
	snap := s.snap(ctx)
	switch {
	case settings.KeyPublicURL.Of(snap) != "":
		workerHubURL = settings.KeyPublicURL.Of(snap)
	case s.cfg.Listen == "":
		if addr := s.listenAddr(); addr != "" {
			workerHubURL = settings.BaseURL(snap, addr)
		} else if u, err := s.cfg.LocalListenURL(); err == nil {
			workerHubURL = u
		}
	}

	// ONE store read for the three solo facts, and NONE outside solo mode --
	// the GATE answers both, so neither is a conjunct here.
	//
	// Each fact rests on the same row, and the latch that would make a second
	// read free is set only once a password exists, so asking through
	// CredentialFree and PasswordSet separately cost three round trips for one
	// fact on every page load of a hub that has none. And a gate that is not
	// solo refuses without reading anything, so a `leapmux hub` no longer looks
	// up a `solo` row that can never exist.
	credentialFree, soloPasswordSet := s.soloGate.CredentialFreeAndPasswordSet(ctx)

	return connect.NewResponse(&leapmuxv1.GetSystemInfoResponse{
		SignupEnabled:  s.signupEnabled(ctx),
		SoloMode:       s.cfg.SoloMode,
		SetupRequired:  setupRequired,
		Version:        version.Value,
		CommitHash:     version.CommitHash,
		CommitTime:     version.CommitTime,
		BuildTime:      version.BuildTime,
		Branch:         version.Branch,
		OauthEnabled:   len(providers) > 0,
		WorkerHubUrl:   workerHubURL,
		EmailEnabled:   settings.KeySMTP.Of(snap).Enabled(),
		PasskeyEnabled: s.passkeysRunnableForOrigin(ctx, originFromRequest(req)),
		CaptchaEnabled: captchaEnabled,
		// The three solo facts. Each is FALSE outside solo mode, where a
		// multi-user hub authenticates every caller and each account answers
		// for its own password -- and the gate says so itself, so no mode test
		// appears here.
		AutoAuthenticated:     credentialFree,
		PasswordSetupRequired: s.soloPasswordSetupRequired(credentialFree, soloPasswordSet),
		SoloPasswordSet:       soloPasswordSet,
		AltchaAlgorithm:       altchaAlgorithm,
		CaptchaProvider:       captchaProvider,
		CaptchaSiteKey:        captchaSiteKey,
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
	baseURL := s.baseURL(ctx)

	var pbProviders []*leapmuxv1.OAuthProviderInfo
	for _, p := range providers {
		pbProviders = append(pbProviders, &leapmuxv1.OAuthProviderInfo{
			Id:           p.ID,
			Name:         p.Name,
			ProviderType: p.ProviderType,
			LoginUrl:     fmt.Sprintf("%s/auth/idp/%s/login", baseURL, p.ID),
		})
	}

	return connect.NewResponse(&leapmuxv1.GetOAuthProvidersResponse{
		Providers: pbProviders,
	}), nil
}

// loadPendingOAuthSignup fetches and validates a pending OAuth signup by token.
// It returns a connect error on missing/expired tokens.
//
// now is a parameter so both callers pass the service's clock seam rather
// than this function reading a second wall clock beside it.
func loadPendingOAuthSignup(ctx context.Context, st store.Store, token string, now time.Time) (*store.PendingOAuthSignup, error) {
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
	if auth.IsExpired(now, pending.ExpiresAt) {
		_ = st.PendingOAuthSignups().Delete(ctx, token)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("signup token expired"))
	}
	return pending, nil
}

// errSignupStartedElsewhere refuses a pending signup to a browser that did
// not start the OAuth flow.
//
// The signup token specifies a FLOW, not a browser: it travels in the URL the
// callback redirects to, so whoever holds that URL could otherwise finish
// the signup. An attacker who completes their OWN callback and hands the
// resulting link to a victim would sign that victim into an account linked
// to the ATTACKER's identity, which the attacker can return to at any time.
// The pending-signup cookie is what specifies the browser. A row minted without
// one is refused rather than admitted, for the same reason the callback
// refuses a state row with no nonce.
func errSignupStartedElsewhere() error {
	return connect.NewError(connect.CodePermissionDenied,
		fmt.Errorf("a different browser started this sign-up; start again from the sign-in page"))
}

// assertSignupBrowser checks the pending signup's browser binding.
func (s *AuthService) assertSignupBrowser(ctx context.Context, header http.Header, pending *store.PendingOAuthSignup) error {
	presented := auth.OAuthSignupNonceFromHeader(header.Get("Cookie"), pending.Token, s.secureCookies(ctx))
	if !browserSecretMatches(pending.NonceHash, presented) {
		return errSignupStartedElsewhere()
	}
	return nil
}

func (s *AuthService) GetPendingOAuthSignup(ctx context.Context, req *connect.Request[leapmuxv1.GetPendingOAuthSignupRequest]) (*connect.Response[leapmuxv1.GetPendingOAuthSignupResponse], error) {
	pending, err := loadPendingOAuthSignup(ctx, s.store, req.Msg.GetSignupToken(), s.now().UTC())
	if err != nil {
		return nil, err
	}
	// Refuse here as well as at Complete, so the wrong browser sees the
	// refusal before it fills a username in rather than after.
	if err := s.assertSignupBrowser(ctx, req.Header(), pending); err != nil {
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
	pending, err := loadPendingOAuthSignup(ctx, s.store, signupToken, s.now().UTC())
	if err != nil {
		return nil, err
	}
	if err := s.assertSignupBrowser(ctx, req.Header(), pending); err != nil {
		return nil, err
	}

	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// OAuth completion is never setup mode: only an administrator can
	// configure a provider, so an account already exists whenever a pending
	// OAuth signup does. Both reserved rules therefore apply.
	if err := s.validateSignupUsername(ctx, username, false); err != nil {
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
	// at callback time). The request's email field is a FALLBACK only, read
	// below when an untrusted provider supplied nothing; a provider-supplied
	// address always wins, so the caller cannot substitute one.
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

	if trustEmail {
		if email == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trusted OAuth provider returned no email"))
		}
		if err := CheckEmailAvailable(ctx, s.store, email, ""); err != nil {
			return nil, AvailabilityConnectError(err)
		}
		userEmail = email
		emailVerified = true
	} else {
		// Untrusted provider: the hub applies its own signup email policy
		// (required + verified-available when SMTP is on, optional
		// otherwise), exactly like local signup.
		//
		// A provider that omits the email claim leaves the caller to supply
		// one. Without this the sign-up cannot complete whenever SMTP is on:
		// validateSignupEmail refuses an empty address, and no other step in
		// the flow can produce one, so the pending token expires and the
		// account can never be created. The request field is only honored
		// when the provider gave nothing -- a provider-supplied address
		// still wins, so the caller cannot substitute one.
		if email == "" {
			email = req.Msg.GetEmail()
		}
		if err := s.validateSignupEmail(ctx, email); err != nil {
			return nil, err
		}
		if s.emailVerificationRequired(ctx) {
			pendingEmail = email
		} else {
			userEmail = email
		}
	}

	linkUserID := id.Generate()
	link := func(tx store.Store) error {
		linkUID, mintErr := mintRowUserID(linkUserID)
		if mintErr != nil {
			return mintErr
		}
		if err := tx.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          linkUID,
			ProviderID:      pending.ProviderID,
			ProviderSubject: pending.ProviderSubject,
		}); err != nil {
			return fmt.Errorf("create user link: %w", err)
		}
		return nil
	}

	var user *store.User
	emailSent := false
	var storedCode string

	var nextResend *time.Time
	user, storedCode, err = createUserInTx(ctx, s.store, createUserTxParams{
		userID:        linkUserID,
		username:      username,
		displayName:   displayName,
		email:         userEmail,
		emailVerified: emailVerified,
		pendingEmail:  pendingEmail,
		passwordHash:  pwdhash.PlaceholderHash,
		extra:         link,
		now:           s.now,
	})
	if err != nil {
		return nil, mapSignupCommitError(err)
	}
	if pendingEmail != "" {
		if err := s.deliverSignupVerification(ctx, user.ID, pendingEmail, storedCode); err != nil {
			return nil, err
		}
		emailSent = true
		next := pendingResendDeadline(s.now(), user.PendingEmailUnblockedAt)
		nextResend = &next
	}

	// Decrypt and re-store OAuth tokens with the real user ID as AAD.
	if s.keystore != nil {
		if err := reencryptPendingTokens(ctx, s.keystore, s.store, pending, signupToken, user.ID); err != nil {
			slog.Error("oauth: re-encrypt tokens for new user", "error", err, "user_id", user.ID)
		}
	}

	_ = s.store.PendingOAuthSignups().Delete(ctx, signupToken)

	finalUser := user
	finalUID, mintErr := mintRowUserID(finalUser.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, s.store, finalUID, s.sessionDuration(ctx))
	if sessionErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", sessionErr))
	}

	resp := connect.NewResponse(&leapmuxv1.CompleteOAuthSignupResponse{
		// An OAuth sign-up carries no passkeys yet.
		User: userToProto(finalUser, 0),
		EmailVerification: emailVerificationToProto(verificationOutcome{
			Required:              pendingEmail != "",
			EmailSent:             emailSent,
			NextResendAvailableAt: nextResend,
		}),
	})
	s.setSessionCookie(ctx, resp.Header(), sessionID, expiresAt)
	// The pending row is consumed, so its browser binding has nothing left
	// to bind. Add, never Set: setSessionCookie above owns the one
	// Set-Cookie line for the session, and a Set here would delete it.
	for _, c := range auth.ClearOAuthSignupNonceCookie(signupToken) {
		resp.Header().Add("Set-Cookie", c.String())
	}
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

func userToProto(u *store.User, passkeyCount int64) *leapmuxv1.User {
	return &leapmuxv1.User{
		Id:            u.ID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		IsAdmin:       u.IsAdmin,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		PendingEmail:  u.PendingEmail,
		PasswordSet:   u.PasswordSet,
		PasskeyCount:  int32(passkeyCount),
	}
}

// userToProtoWithPasskeys fills the passkey count best-effort. The count is
// display data; a transient failure of this one query must not fail an RPC
// whose main work already committed (a minted session, a rotated password).
// It logs the failure, and the count reports zero.
func userToProtoWithPasskeys(ctx context.Context, st store.Store, u *store.User) *leapmuxv1.User {
	count, _ := countPasskeysBestEffort(ctx, st, u)
	return userToProto(u, count)
}

// countPasskeysBestEffort returns the account's passkey count, and whether
// the query answered at all. A failure logs a warning and reports (0, false).
//
// It is separate from userToProtoWithPasskeys because GetCurrentUser needs
// the same number TWICE -- once to report it, once for the step-up option
// rule -- and running the COUNT again for the second reader is the kind of
// drift a shared value removes.
//
// The second return value is what keeps the two readers honest. A count of
// zero means one of two things, and only one of them is a fact about the
// account: it holds no passkey, or nobody could ask. A display tolerates the
// first reading of both; an authorization-shaped rule must not, so it reads
// this flag and fails closed. The value was a bare int64, and the discarded
// error reached the rule as a genuine zero.
func countPasskeysBestEffort(ctx context.Context, st store.Store, u *store.User) (int64, bool) {
	count, err := st.PasskeyCredentials().CountByUser(ctx, u.ID)
	if err != nil {
		slog.WarnContext(ctx, "count passkeys for user proto", "user_id", u.ID, "err", err)
		return 0, false
	}
	return count, true
}

// mapSignupCommitError maps an account-creation failure onto a connect
// code. A uniqueness conflict is AlreadyExists with a stable message: the
// unique index, not the pre-checks, is the real guard against a race, and
// a permanent conflict reported as a retryable fault with raw driver text
// misleads both the client and the operator. Everything else is Internal.
func mapSignupCommitError(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username or email already taken"))
	}
	return connect.NewError(connect.CodeInternal, err)
}

// validateSignupUsername enforces the username rules (reserved names plus
// availability) shared by the OAuth completion and both passkey sign-up
// halves. `setupMode` says whether this sign-up creates the hub's first
// account, which is what decides if `admin` is claimable; see
// usernames.IsReservedForSignup.
func (s *AuthService) validateSignupUsername(ctx context.Context, username string, setupMode bool) error {
	if usernames.IsReservedForSignup(username, setupMode) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is a reserved username", username))
	}
	if err := CheckUsernameAvailable(ctx, s.store, username); err != nil {
		return AvailabilityConnectError(err)
	}
	return nil
}

// validateSignupEmail enforces the email policy shared by every sign-up
// flavor: the address is required and verified-available when the hub
// requires email verification, validated and availability-checked whenever
// present otherwise. One spelling of the policy, so the password, OAuth,
// and passkey flavors cannot drift.
func (s *AuthService) validateSignupEmail(ctx context.Context, email string) error {
	if email == "" {
		if s.emailVerificationRequired(ctx) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("email is required"))
		}
		return nil
	}
	if err := validate.ValidateEmail(email); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := CheckEmailAvailable(ctx, s.store, email, ""); err != nil {
		return AvailabilityConnectError(err)
	}
	return nil
}

// deliverSignupVerification sends the verification email. A send failure
// fails closed: the account cannot verify its email, so this function rolls
// the just-created account back and the caller sees a generic error. The
// transport error stays in the server log — never in the anonymous client
// response.
//
// The rollback is a best-effort compensation in a SECOND transaction: a
// process death between the create commit and this rollback leaves a
// signed-up account whose code was never delivered. That account
// self-recovers — its credential committed with it, Login and the passkey
// finish are public, and ResendVerificationEmail is allowlisted for
// unverified sessions — so only a double fault strands an account, and this
// needs no outbox. A rollback that itself fails is logged, never surfaced.
func (s *AuthService) deliverSignupVerification(ctx context.Context, userID, email, code string) error {
	if err := s.mail.Send(ctx, s.renderer.VerificationEmail(email, code, pendingEmailExpiry)); err != nil {
		slog.WarnContext(ctx, "verification email send failed; rolling back sign-up",
			"user_id", userID, "err", err)
		if rbErr := rollbackUnusableSignup(ctx, s.store, userID); rbErr != nil {
			slog.ErrorContext(ctx, "sign-up rollback failed", "user_id", userID, "err", rbErr)
		}
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("sign-up failed: verification email could not be sent"))
	}
	return nil
}
