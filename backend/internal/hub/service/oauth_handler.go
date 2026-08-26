package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	oauthStateExpiry         = 5 * time.Minute
	pendingOAuthSignupExpiry = 5 * time.Minute
	defaultTokenExpiry       = 1 * time.Hour
)

// OAuthHandler handles OAuth login/callback HTTP endpoints.
type OAuthHandler struct {
	store store.Store
	cfg   *config.Config
	set   *settings.Manager
	// lifecycle drops the cached UserInfo after the re-authentication leg
	// elevates a session, so this process re-reads the new deadline instead
	// of serving the old one for the rest of the auth cache's life.
	lifecycle *auth.CredentialLifecycleEffects
	keystore  *keystore.Keystore

	// The clock every instant this handler mints or compares comes from.
	// It is the THIRD elevation arm, and grantSessionElevation takes now as
	// a parameter precisely so each arm passes its own seam rather than
	// inventing a fourth notion of the current instant -- this arm had
	// none, so a test that moved the password and passkey arms forward
	// could not move this one, and completeOAuthReauth read the wall clock
	// three separate times inside one request.
	clockSeam

	// providers caches built Provider instances.
	//
	// The key carries the redirect URL as well as the provider ID, because
	// a built Provider bakes BOTH in and only one of them is immutable. The
	// row's own config cannot change after creation -- AdminOAuthService
	// exposes add, remove and enable/disable and no edit verb, and the one
	// writer of client_secret is the offline re-encrypt command, which
	// rewrites the SAME plaintext under a new key version in another process.
	// A future edit verb must call InvalidateProvider, exactly as remove and
	// disable already do. The redirect URL is different: it derives from
	// public_url and
	// secure_cookies, and neither key is restart-class -- an admin changes
	// them live. Keyed on the ID alone, the cache kept serving the old
	// redirect_uri until the process restarted, and every login for that
	// provider then failed with the identity provider's redirect_uri
	// mismatch.
	providers   map[providerCacheKey]huboauth.Provider
	providersMu sync.RWMutex
	// providerFlight collapses concurrent COLD builds of one key onto one
	// discovery leg.
	//
	// The double-check under the write lock made the discarded instances
	// unreachable; it could not stop them being BUILT. An OIDC build is an
	// outbound round trip to the identity provider for
	// .well-known/openid-configuration plus the JWKS, so N cold callers ran N
	// of them and kept one -- on process start, and again whenever an
	// administrator's remove or disable evicts the entry.
	providerFlight singleflight.Group
}

// providerCacheKey identifies one built Provider. See OAuthHandler.providers
// for why the redirect URL is part of the identity.
type providerCacheKey struct {
	providerID  string
	redirectURL string
}

// NewOAuthHandler creates a new OAuth HTTP handler.
func NewOAuthHandler(st store.Store, cfg *config.Config, set *settings.Manager, lifecycle *auth.CredentialLifecycleEffects, ks *keystore.Keystore) *OAuthHandler {
	if lifecycle == nil {
		panic("oauth handler requires credential lifecycle effects")
	}
	return &OAuthHandler{store: st, cfg: cfg, set: set, lifecycle: lifecycle, keystore: ks, providers: make(map[providerCacheKey]huboauth.Provider)}
}

// RegisterRoutes registers OAuth HTTP routes on the given mux.
func (h *OAuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/oauth/", h.handleOAuth)
}

func (h *OAuthHandler) handleOAuth(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/auth/oauth/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid OAuth path", http.StatusBadRequest)
		return
	}

	providerID := parts[0]
	action := parts[1]

	switch action {
	case "login":
		h.handleLogin(w, r, providerID)
	case "reauth":
		h.handleReauth(w, r, providerID)
	case "callback":
		h.handleCallback(w, r, providerID)
	default:
		http.Error(w, "unknown OAuth action", http.StatusBadRequest)
	}
}

// loadEnabledProvider fetches the provider from DB, checks it's enabled, and
// builds the cached Provider instance. Returns the provider, its trust_email
// setting, and whether the load succeeded. Writes an HTTP error on failure.
func (h *OAuthHandler) loadEnabledProvider(w http.ResponseWriter, ctx context.Context, providerID string) (huboauth.Provider, bool, bool) {
	dbProvider, err := h.store.OAuthProviders().GetByID(ctx, providerID)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return nil, false, false
	}
	if !dbProvider.Enabled {
		http.Error(w, "provider disabled", http.StatusForbidden)
		return nil, false, false
	}
	provider, err := h.buildProvider(ctx, dbProvider)
	if err != nil {
		slog.Error("oauth: build provider", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false, false
	}
	return provider, dbProvider.TrustEmail, true
}

func (h *OAuthHandler) handleLogin(w http.ResponseWriter, r *http.Request, providerID string) {
	h.beginOAuthFlow(w, r, providerID, store.OAuthStatePurposeLogin, "")
}

// handleReauth starts an OAuth RE-AUTHENTICATION: the caller is already
// signed in, and completing this leg elevates their session rather than
// creating one.
//
// It is how an account whose only reachable factor is its identity provider
// performs a step-up.
//
// Cookies only, and a session is required: there is nothing to elevate
// otherwise, and a bearer has no session row to stamp.
//
// The account shape must ALSO admit the arm -- see providerMayElevateAccount
// for the one rule. Refusing here keeps the browser from ever leaving;
// completeOAuthReauth checks it again, because it is the leg that grants and
// the shape can move inside the state row's five minutes.
func (h *OAuthHandler) handleReauth(w http.ResponseWriter, r *http.Request, providerID string) {
	ctx := r.Context()
	// A navigation another site started cannot begin a step-up.
	//
	// The session cookie is SameSite=Lax, which a top-level cross-site
	// navigation CARRIES, so without this an attacker page could point the
	// victim's browser here: the hub would authenticate the session, mint a
	// reauth row bound to it, and send the browser to the provider with
	// prompt=login. A victim who signed in at that prompt returned with the
	// session elevated without ever choosing to verify -- and whoever else
	// held a copy of that cookie then passed every gate the window protects,
	// including the command-line credential mint.
	//
	// This leg alone. handleLogin's worst outcome is a sign-in the victim
	// performed as themselves, the nonce binding already stops the
	// attacker-identity variant, and a cross-site link to a hub's sign-in
	// page is a thing that legitimately exists.
	//
	// A same-origin POST is NOT the remedy here, although it is the one the
	// CLI consent leg uses. A browser matches form-action against every hop
	// of a submission's redirect chain, and this leg's next hop is the
	// identity provider -- an origin the policy cannot enumerate, because an
	// administrator configures it. See hub/frontend/csp.go.
	if httpsec.StartedByAnotherDocument(r) {
		http.Error(w, "start verification from inside the app", http.StatusForbidden)
		return
	}
	user, err := auth.AuthenticateHTTP(ctx, r, auth.HTTPAuthOpts{
		Store:   h.store,
		Cookies: []bool{false, true},
	})
	if err != nil || user == nil || user.Credential.SessionID() == "" {
		http.Error(w, "sign in first: there is no session to verify", http.StatusUnauthorized)
		return
	}
	row, err := h.store.Users().GetByID(ctx, user.ID.String())
	if err != nil {
		slog.Error("oauth: load user for reauth", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The provider is loaded before the shape check, and only to REFUSE a
	// disabled or missing one here rather than after the browser has left
	// the app. The rule itself no longer depends on which provider this is.
	// The instance is cached, so beginOAuthFlow's own load costs nothing.
	if _, _, ok := h.loadEnabledProvider(w, ctx, providerID); !ok {
		return
	}
	if !h.providerMayElevateAccount(w, ctx, row) {
		return
	}
	h.beginOAuthFlow(w, r, providerID, store.OAuthStatePurposeReauth, user.Credential.SessionID())
}

// providerMayElevateAccount answers the question both re-authentication legs
// ask, and writes the refusal itself. It reports whether the caller may
// continue.
//
// ONE rule: an account with NO password and NO passkey is signed in BY the
// provider, so the elevation is exactly as strong as the sign-in it stands
// on. Any linked provider qualifies, GitHub included --
// operating/security.md states that limit plainly. Anything else is refused,
// because the account holds a factor it can present directly and "the
// browser can still reach the provider session" is a weaker claim than the
// one that factor makes.
//
// The rule is accountElevatesOnlyThroughAProvider, CALLED rather than spelled
// again: the first-credential branch of passkeyManagementAuth reads the same
// two facts, and a copy meant a change to what counts as "a factor" could
// land in one and miss the other. There is no wrapper between the two either,
// because a name that only forwards is a difference a reader has to look for
// and never finds.
//
// There used to be a second tier for an account whose only factor is a
// passkey THIS HUB CANNOT RUN, bridged by a provider that fills auth_time.
// It is gone, and its absence is the design rather than an omission. A hub
// that cannot run a passkey ceremony has one cause -- it has no usable
// browser origin, because public_url is unset and nothing is listening -- so
// the tier existed only to rescue users from an administrator's
// misconfiguration whose real remedy is to restore the address. Serving it
// cost a second freshness rule, a per-provider capability the code could not
// actually know (Google, Apple, Entra and GitLab-as-OP all report
// provider_type "oidc" and none of them fills auth_time on request), and an
// arm the step-up screen offered and the grant leg then refused. Refusing
// the shape outright is both simpler and stricter: the account waits for the
// hub to be repaired instead of elevating on a weaker claim than the passkey
// it holds.
func (h *OAuthHandler) providerMayElevateAccount(w http.ResponseWriter, ctx context.Context, user *store.User) bool {
	mayElevate, err := accountElevatesOnlyThroughAProvider(ctx, h.store, user)
	if err != nil {
		slog.Error("oauth: read account shape for reauth", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !mayElevate {
		// The refusal names the remedy rather than the rule: the account
		// holds a factor it can present directly, and that is what it must
		// present. It does NOT say which of the two the account holds --
		// this runs before the caller proved it owns the account.
		http.Error(w, "this account holds a password or a passkey; verify with one of those instead", http.StatusForbidden)
		return false
	}
	return true
}

// beginOAuthFlow mints the state row plus its browser-bound nonce and sends
// the user to the provider. Login and re-authentication share it so the two
// cannot drift on the browser binding, the PKCE verifier, or the expiry --
// they differ only in the purpose they record and whether they force the
// provider to prompt.
func (h *OAuthHandler) beginOAuthFlow(w http.ResponseWriter, r *http.Request, providerID, purpose, sessionID string) {
	ctx := r.Context()

	provider, _, ok := h.loadEnabledProvider(w, ctx, providerID)
	if !ok {
		return
	}

	verifier := oauth2.GenerateVerifier()
	state := id.Generate()
	// The nonce binds this flow to THIS browser. The state alone does not: it
	// travels in the callback URL, so whoever holds that URL can complete the
	// flow, and an attacker who starts a login with their own identity and
	// withholds the callback can hand the live (code, state) pair to a victim
	// -- whose browser is then signed in as the attacker. Only the browser
	// that received this cookie can redeem the row. RFC 6749 section 10.12.
	//
	// On the reauth leg the binding matters just as much: the row specifies the
	// session it will elevate, so a state that anybody could redeem would
	// hand an attacker's completed provider flow the victim's elevation.
	nonce := id.Generate()

	redirectURI := sanitizeRedirectURI(r.URL.Query().Get("redirect"))

	if err := h.store.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
		State:        state,
		ProviderID:   providerID,
		PkceVerifier: verifier,
		NonceHash:    hashBrowserSecret(nonce),
		RedirectURI:  redirectURI,
		Purpose:      purpose,
		SessionID:    sessionID,
		ExpiresAt:    h.now().Add(oauthStateExpiry).UTC(),
	}); err != nil {
		slog.Error("oauth: create state", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Set the cookie BEFORE the redirect: http.Redirect writes the header, so
	// a SetCookie after it never reaches the browser.
	http.SetCookie(w, auth.BuildOAuthNonceCookie(state, nonce, oauthStateExpiry, h.secureCookies(ctx)))

	authURL := provider.AuthURL(state, verifier, huboauth.AuthURLOptions{
		// Without this the provider silently reuses its own session, the
		// browser returns in a fraction of a second, and the "step-up" proved
		// nothing.
		ForceReauthentication: purpose == store.OAuthStatePurposeReauth,
	})
	http.Redirect(w, r, authURL, http.StatusFound)
}

// consumeOAuthState turns a callback request into a validated, single-use
// state row, or writes the refusal itself and reports false.
//
// It is separate from handleCallback because it owns ONE thing that the rest
// of the callback does not touch: the order in which a flow's single use is
// spent. That order is the security property, it is invisible from any one
// line, and it was the first seventy lines of a two-hundred-line function
// whose remaining work -- exchange, claims, dispatch -- has nothing to do
// with it. See the comments inside for why the nonce runs before the
// consume.
//
// It also returns the code, so the caller does not read the query again for
// a value this function already validated.
func (h *OAuthHandler) consumeOAuthState(w http.ResponseWriter, r *http.Request, providerID string) (code string, oauthState *store.OAuthState, ok bool) {
	ctx := r.Context()

	code = r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		errMsg := r.URL.Query().Get("error_description")
		if errMsg == "" {
			errMsg = r.URL.Query().Get("error")
		}
		if errMsg == "" {
			errMsg = "missing code or state"
		}
		http.Error(w, "OAuth error: "+errMsg, http.StatusBadRequest)
		return "", nil, false
	}

	oauthState, err := h.store.OAuthStates().Get(ctx, state)
	if err != nil {
		// A missing/unknown state is a genuine client error (expired or forged),
		// but a transient store error should not masquerade as "invalid state"
		// and force the user to restart the whole login -- surface it as
		// retryable, matching the ErrNotFound split the user link uses.
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("oauth: load state", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return "", nil, false
		}
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return "", nil, false
	}
	// Bind the flow to the browser that started it, BEFORE anything is
	// spent. A row minted without a nonce cannot be completed at all:
	// failing closed keeps a stored row from becoming redeemable by anyone
	// who holds its state, which is the whole property this check exists to
	// establish.
	//
	// The refusal leaves the state row ALONE, so consumption is owner-only.
	// Consuming it here instead would let anyone who learns a live state
	// destroy the owner's in-flight login with one request, and the owner's
	// real callback would then report "invalid or expired state" -- the
	// nonce is what makes owner-only consumption expressible, so it must
	// run before the consume rather than after it.
	//
	// The cookie name is built from the LOADED row's state, never from the
	// query parameter, so a caller cannot aim the lookup at a name of its
	// own choosing.
	secureCookies := h.secureCookies(ctx)
	if !browserSecretMatches(oauthState.NonceHash, auth.OAuthNonceFromRequest(r, oauthState.State, secureCookies)) {
		http.Error(w, "this sign-in was started in a different browser; start again from the sign-in page", http.StatusBadRequest)
		return "", nil, false
	}

	// The owner presented the nonce, so consume the single-use row now and
	// clear the nonce on every outcome from here on, success included. A
	// failed delete leaves the row live for the rest of its expiry, which is
	// not worth failing the login over, but it must not be silent: it is the
	// only consumption of a single-use row in the hub.
	if err := h.store.OAuthStates().Delete(ctx, state); err != nil {
		slog.Error("oauth: delete state", "error", err, "provider_id", providerID)
	}
	for _, c := range auth.ClearOAuthNonceCookie(oauthState.State) {
		http.SetCookie(w, c)
	}

	if auth.IsExpired(h.now().UTC(), oauthState.ExpiresAt) {
		http.Error(w, "state expired", http.StatusBadRequest)
		return "", nil, false
	}
	if oauthState.ProviderID != providerID {
		http.Error(w, "state/provider mismatch", http.StatusBadRequest)
		return "", nil, false
	}
	return code, oauthState, true
}

func (h *OAuthHandler) handleCallback(w http.ResponseWriter, r *http.Request, providerID string) {
	ctx := r.Context()

	code, oauthState, ok := h.consumeOAuthState(w, r, providerID)
	if !ok {
		return
	}

	provider, trustEmail, ok := h.loadEnabledProvider(w, ctx, providerID)
	if !ok {
		return
	}

	// Exchange code for tokens, under a deadline of our own.
	//
	// This is a mux route, so it never passes through the RPC interceptor chain
	// that limits every Connect handler -- and the leg it makes is an outbound
	// call to a third party. golang.org/x/oauth2 runs it on http.DefaultClient
	// unless the context carries an oauth2.HTTPClient, and http.DefaultClient
	// has NO timeout, so without this deadline an identity provider that accepts
	// the connection and never answers parks this handler until the hub shuts
	// down (the http.Server's shutdown-scoped BaseContext cancels it then, but a
	// hung IdP during NORMAL operation would otherwise park it for the process's
	// life). This per-leg deadline limits exactly that: a slow or wedged IdP
	// while the hub is healthy.
	exchangeCtx, cancelExchange := context.WithTimeout(ctx, settings.KeyTimeouts.Of(h.set.Snapshot(r.Context())).APITimeout())
	defer cancelExchange()
	tokenSet, claims, err := provider.Exchange(exchangeCtx, code, oauthState.PkceVerifier)
	if err != nil {
		slog.Error("oauth: exchange", "error", err)
		http.Error(w, "OAuth exchange failed", http.StatusBadRequest)
		return
	}

	// Check if this OAuth identity is already linked to a user.
	link, err := h.store.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{
		ProviderID:      providerID,
		ProviderSubject: claims.Subject,
	})
	// A re-authentication ends HERE, whatever the link lookup found. It must
	// never fall through to the auto-link or signup branches below: those
	// create a session or an account, and this leg exists only to elevate a
	// session that already exists.
	if oauthState.Purpose == store.OAuthStatePurposeReauth {
		h.completeOAuthReauth(w, r, oauthState, provider, claims, link, err)
		return
	}

	// Require a valid email from the OAuth provider.
	//
	// AFTER the re-authentication branch, because that leg never reads the
	// address: it identifies the person by claims.Subject, through the link
	// this lookup just made. Demanding one refused every step-up on a
	// provider that stopped reporting a VERIFIED address -- and for the
	// accounts this arm serves, which hold neither a password nor a passkey,
	// it is the only arm they have. The refusal even blamed the `email`
	// scope, which the operator had granted.
	if claims.Email == "" {
		slog.Error("oauth: provider did not return an email", "provider_id", providerID)
		http.Error(w, "OAuth provider did not return an email address; ensure the 'email' scope is granted", http.StatusBadRequest)
		return
	}
	if emailErr := validate.ValidateEmail(claims.Email); emailErr != nil {
		slog.Error("oauth: provider returned invalid email", "email", claims.Email, "provider_id", providerID)
		http.Error(w, "OAuth provider returned an invalid email address", http.StatusBadRequest)
		return
	}
	if err == nil {
		// Existing link — direct login.
		user, userErr := h.store.Users().GetByID(ctx, link.UserID)
		if userErr != nil {
			http.Error(w, "linked user not found", http.StatusInternalServerError)
			return
		}

		h.loginOAuthUser(w, r, user.ID, providerID, tokenSet, oauthState.RedirectURI)
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		slog.Error("oauth: query user link", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Auto-link by verified email: if the OAuth provider is trusted, look for
	// an existing user with the same verified email and link automatically.
	if trustEmail {
		existingUser, emailErr := h.store.Users().GetByEmail(ctx, claims.Email)
		// Distinguish "no such user" (fall through to signup) from a transient
		// store failure: on a DB blip, GetByEmail must NOT be read as absence, or
		// a returning verified user is either turned into a permanent 403 (signup
		// disabled) or duplicated into a fresh account (signup enabled). Mirror the
		// ErrNotFound-vs-internal split the OAuthUserLinks().Get read above uses.
		if emailErr != nil && !errors.Is(emailErr, store.ErrNotFound) {
			slog.Error("oauth: look up user by email for auto-link", "error", emailErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if emailErr == nil && existingUser.EmailVerified {
			linkUID, mintOK := userid.New(existingUser.ID)
			if !mintOK {
				slog.Error("oauth: matched user row has a blank id")
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if err := h.store.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
				UserID:          linkUID,
				ProviderID:      providerID,
				ProviderSubject: claims.Subject,
			}); err != nil {
				slog.Error("oauth: create user link for auto-link", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}

			slog.Info("oauth: auto-linked provider to existing account by verified email",
				"user_id", existingUser.ID, "provider_id", providerID, "email", claims.Email)
			h.loginOAuthUser(w, r, existingUser.ID, providerID, tokenSet, oauthState.RedirectURI)
			return
		}
	}

	// New user — store pending signup for username selection.
	if !settings.SignupEnabledEffective(h.set.Snapshot(r.Context()), h.cfg.DevMode) {
		http.Error(w, "sign-up is disabled; no existing account linked to this identity", http.StatusForbidden)
		return
	}

	// Carry the browser binding ACROSS the hand-off. Without it the binding
	// stops here: the browser receives a signup token in a URL, and
	// CompleteOAuthSignup would create the account and set a session for
	// whoever presents that token -- so an attacker who completes their OWN
	// callback can deliver the token to a victim, whose browser is then
	// signed into an account the attacker's identity owns and can return to
	// at any time. A fresh nonce, because the flow's first one is spent.
	signupNonce := id.Generate()
	signupToken, err := h.storePendingSignup(ctx, providerID, claims, tokenSet, oauthState.RedirectURI, hashBrowserSecret(signupNonce))
	if err != nil {
		slog.Error("oauth: store pending signup", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, auth.BuildOAuthSignupNonceCookie(signupToken, signupNonce, pendingOAuthSignupExpiry, h.secureCookies(ctx)))
	http.Redirect(w, r, "/oauth/complete-signup?token="+signupToken, http.StatusFound)
}

// completeOAuthReauth finishes the step-up leg: it confirms the identity the
// provider just returned is one the ACTING user already holds, then stamps
// the elevation on the acting session.
//
// It creates nothing. No session, no account, and above all no
// oauth_user_links row -- linking here would let a step-up attach a brand new
// identity to the account, which is a credential change disguised as a
// verification of one.
//
// linkErr carries the outcome of the caller's link lookup so this does not
// repeat the query.
func (h *OAuthHandler) completeOAuthReauth(
	w http.ResponseWriter,
	r *http.Request,
	oauthState *store.OAuthState,
	provider huboauth.Provider,
	claims *huboauth.UserClaims,
	link *store.OAuthUserLink,
	linkErr error,
) {
	ctx := r.Context()
	// ONE instant for the whole leg. The freshness check, the session
	// lookup and the stamped window all answer "now", so three separate
	// reads could disagree inside one request.
	now := h.now().UTC()
	if linkErr != nil && !errors.Is(linkErr, store.ErrNotFound) {
		slog.Error("oauth: query user link for reauth", "error", linkErr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The session is loaded, not trusted from the state row alone, because
	// the elevation must land on a session that is still live and because
	// its user_id is what the link is compared against.
	session, err := h.store.Sessions().GetByID(ctx, oauthState.SessionID, now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "your session ended while you were verifying; sign in again", http.StatusUnauthorized)
			return
		}
		slog.Error("oauth: load session for reauth", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The returned identity must ALREADY belong to this user. A different
	// account's identity, or an unlinked one, proves nothing about who is at
	// the keyboard of this session.
	if linkErr != nil || link.UserID != session.UserID {
		http.Error(w, "that account is not linked to the signed-in user", http.StatusForbidden)
		return
	}
	// The account shape is checked HERE as well as at the start leg, because
	// this is where the grant happens and the shape can move in between: the
	// state row lives for oauthStateExpiry, and the first-credential rule
	// lets an account attach a password in another tab inside that window. A
	// row minted while the account held nothing would otherwise buy
	// provider-strength elevation for an account that now holds a password.
	owner, err := h.store.Users().GetByID(ctx, session.UserID)
	if err != nil {
		slog.Error("oauth: load user for reauth grant", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !h.providerMayElevateAccount(w, ctx, owner) {
		return
	}
	uid, ok := userid.New(session.UserID)
	if !ok {
		slog.Error("oauth: session row has a blank user id", "session_id", oauthState.SessionID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Through the shared grant, so the window, the zero-row refusal and the
	// cache invalidation are the same ones the password and passkey arms use.
	if _, err := grantSessionElevation(ctx, h.store, h.lifecycle, oauthState.SessionID, uid, now); err != nil {
		if errors.Is(err, errElevationSessionEnded) {
			http.Error(w, "your session ended while you were verifying; sign in again", http.StatusUnauthorized)
			return
		}
		slog.Error("oauth: record session elevation", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.redirectBack(w, r, oauthState.RedirectURI)
}

// redirectBack sends the browser to the address the flow recorded, or home
// when it recorded none. The value passed through sanitizeRedirectURI when
// the state row was minted, so it needs no second guard here.
func (h *OAuthHandler) redirectBack(w http.ResponseWriter, r *http.Request, redirectURI string) {
	redirectTo := "/"
	if redirectURI != "" {
		redirectTo = redirectURI
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

// secureCookies reports whether this hub writes __Host- prefixed cookies.
func (h *OAuthHandler) secureCookies(ctx context.Context) bool {
	return settings.KeySecureCookies.Of(h.set.Snapshot(ctx))
}

// loginOAuthUser stores tokens, creates a session, and redirects the user.
// Used by both the existing-link and auto-link-by-email paths.
func (h *OAuthHandler) loginOAuthUser(w http.ResponseWriter, r *http.Request, userID, providerID string, tokenSet *huboauth.TokenSet, redirectURI string) {
	ctx := r.Context()

	if err := h.storeTokens(ctx, userID, providerID, tokenSet); err != nil {
		slog.Error("oauth: store tokens", "error", err)
	}

	loginUID, mintOK := userid.New(userID)
	if !mintOK {
		slog.Error("oauth: refusing to create a session for a blank user id")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, h.store, loginUID, settings.SessionDuration(h.set.Snapshot(r.Context())), auth.SessionMeta{
		UserAgent: r.UserAgent(),
		IPAddress: r.RemoteAddr,
	})
	if sessionErr != nil {
		slog.Error("oauth: create session", "error", sessionErr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, auth.BuildSessionCookie(sessionID, expiresAt, h.secureCookies(ctx)))

	h.redirectBack(w, r, redirectURI)
}

// sanitizeRedirectURI ensures the redirect URI is a safe relative path.
// Returns empty string for anything that could be an open redirect.
//
// This is the sink guard: the value it returns reaches a Location header
// through http.Redirect, so it must refuse every spelling a BROWSER reads
// as an authority, not just the ones Go's URL parser does. Two spellings
// beyond the obvious "//host" get through an RFC 3986 parser:
//
//   - "/\host". A WHATWG parser treats a backslash as a slash for a
//     special scheme, so it reads "host" as the authority. Go's net/url
//     does not, so parsing the value and comparing origins ACCEPTS this
//     one -- which is why the guard is an allowlist on the raw bytes.
//   - "/<TAB>/host". url.Parse rejects the control byte, so http.Redirect
//     skips its cleaning branch and writes the value verbatim; the
//     browser then strips the tab and reads "//host". CR and LF do not
//     work (net/http rewrites them to a space), but refusing every
//     control byte costs nothing and removes the dependency on that
//     detail.
//
// frontend/src/lib/safeRedirect.ts applies the same three rules.
func sanitizeRedirectURI(uri string) string {
	if uri == "" || uri[0] != '/' {
		return ""
	}
	if len(uri) > 1 && (uri[1] == '/' || uri[1] == '\\') {
		return ""
	}
	for i := 0; i < len(uri); i++ {
		if uri[i] < 0x20 || uri[i] == 0x7f {
			return ""
		}
	}
	return uri
}

// tokenExpiryTime is the provider token's deadline, or a default measured
// from now when the provider reported none.
//
// now is a parameter so this reads the handler's seam like every other
// instant the handler mints. One wall-clock read left here is the drift
// clockSeam exists to remove: a test that moved the handler forward would
// stamp a provider token against the real clock.
func tokenExpiryTime(tokenSet *huboauth.TokenSet, now time.Time) time.Time {
	if !tokenSet.ExpiresAt.IsZero() {
		return tokenSet.ExpiresAt
	}
	return now.Add(defaultTokenExpiry).UTC()
}

// cleanProviderDisplayName picks the display name for a pending OAuth signup
// and cleans it HERE, at the boundary where an identity provider's value
// enters LeapMux, rather than at the field that reads it later.
//
// validate.SanitizeDisplayName decides its fallback from the RAW value, so it
// treats "" as absent and a lone zero width space as present. That is right
// for a form FIELD, where the user reads the error and corrects it. It is
// wrong for this value: the provider chose it, no field shows it, and
// CompleteOAuthSignup refused the whole signup with "display name: name must
// not be empty" over a name that LOOKS empty, while the username fallback sat
// unused in the same call.
//
// CleanName never fails, so an all-invisible claim becomes "" and takes the
// fallback, and a claim over the byte limit is cut instead of refused. Its
// result is idempotent and already within the limit, so the later
// SanitizeDisplayName passes it through unchanged.
//
// An empty return value means the provider supplied no usable name, and the
// caller that reads the stored row then falls back to the username.
func cleanProviderDisplayName(claims *huboauth.UserClaims) string {
	if name := validate.CleanName(claims.DisplayName); name != "" {
		return name
	}
	return validate.CleanName(claims.Name)
}

func (h *OAuthHandler) storePendingSignup(ctx context.Context, providerID string, claims *huboauth.UserClaims, tokenSet *huboauth.TokenSet, redirectURI, nonceHash string) (string, error) {
	token := id.Generate()

	// Use the signup token as entity ID for AAD since the user doesn't exist yet.
	encAccessToken, encRefreshToken, err := encryptTokenPair(h.keystore, tokenSet.AccessToken, tokenSet.RefreshToken, token, providerID)
	if err != nil {
		return "", err
	}

	tokenExpiresAt := tokenExpiryTime(tokenSet, h.now())

	displayName := cleanProviderDisplayName(claims)

	if err := h.store.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
		Token:           token,
		ProviderID:      providerID,
		NonceHash:       nonceHash,
		ProviderSubject: claims.Subject,
		Email:           claims.Email,
		DisplayName:     displayName,
		AccessToken:     encAccessToken,
		RefreshToken:    encRefreshToken,
		TokenType:       tokenSet.TokenType,
		TokenExpiresAt:  tokenExpiresAt,
		KeyVersion:      int64(h.keystore.ActiveVersion()),
		RedirectURI:     redirectURI,
		ExpiresAt:       h.now().Add(pendingOAuthSignupExpiry).UTC(),
	}); err != nil {
		return "", fmt.Errorf("create pending signup: %w", err)
	}

	return token, nil
}

// encryptTokenPair encrypts an access/refresh token pair. The entityID is used
// as part of the AAD and is typically a user ID or a pending-signup token.
func encryptTokenPair(ks *keystore.Keystore, accessToken, refreshToken string, entityID, providerID string) (encAccess, encRefresh []byte, err error) {
	encAccess, err = ks.Encrypt([]byte(accessToken), keystore.AccessTokenAAD(entityID, providerID))
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt access token: %w", err)
	}
	encRefresh, err = ks.Encrypt([]byte(refreshToken), keystore.RefreshTokenAAD(entityID, providerID))
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt refresh token: %w", err)
	}
	return encAccess, encRefresh, nil
}

func (h *OAuthHandler) storeTokens(ctx context.Context, userID, providerID string, tokenSet *huboauth.TokenSet) error {
	encAccess, encRefresh, err := encryptTokenPair(h.keystore, tokenSet.AccessToken, tokenSet.RefreshToken, userID, providerID)
	if err != nil {
		return err
	}

	expiresAt := tokenExpiryTime(tokenSet, h.now())

	tokUID, mintOK := userid.New(userID)
	if !mintOK {
		return errors.New("oauth: cannot store tokens for a blank user id")
	}
	return h.store.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
		UserID:       tokUID,
		ProviderID:   providerID,
		AccessToken:  encAccess,
		RefreshToken: encRefresh,
		TokenType:    tokenSet.TokenType,
		ExpiresAt:    expiresAt,
		KeyVersion:   int64(h.keystore.ActiveVersion()),
	})
}

func (h *OAuthHandler) buildProvider(ctx context.Context, dbProvider *store.OAuthProvider) (huboauth.Provider, error) {
	snap := h.set.Snapshot(ctx)
	key := providerCacheKey{
		providerID:  dbProvider.ID,
		redirectURL: fmt.Sprintf("%s/auth/oauth/%s/callback", settings.BaseURL(snap, h.cfg.Listen), dbProvider.ID),
	}

	if cached, ok := h.cachedProvider(key); ok {
		return cached, nil
	}

	// ONE build per key, however many cold callers arrive together. The
	// flight key is the WHOLE cache key: two redirect URLs build two
	// different clients, and collapsing them would hand a follower a client
	// baked with somebody else's redirect_uri.
	//
	// The build runs on a context DETACHED from the leader's request.
	// singleflight cancels nothing itself, so the shared leg would otherwise
	// carry the leader's context and one abandoned tab would fail discovery
	// for every waiter -- the trade refresh.go states for its own flight, and
	// settings.Manager takes for the same reason. It keeps its own deadline,
	// so a wedged identity provider still cannot park the work for ever.
	built, err, _ := h.providerFlight.Do(key.providerID+"\x00"+key.redirectURL, func() (any, error) {
		// Re-check INSIDE the flight: a caller that gathered while the leader
		// built shares the leader's result, and a later arrival finds the
		// cache warm. This is what keeps "every caller receives the same
		// instance" true, which the double-check under the write lock used to
		// carry.
		if cached, ok := h.cachedProvider(key); ok {
			return cached, nil
		}
		provider, err := h.newProvider(context.WithoutCancel(ctx), snap, dbProvider, key)
		if err != nil {
			return nil, err
		}
		h.providersMu.Lock()
		defer h.providersMu.Unlock()
		h.cacheProvider(key, provider)
		return provider, nil
	})
	if err != nil {
		return nil, err
	}
	return built.(huboauth.Provider), nil
}

// cachedProvider reads one built instance under the read lock.
func (h *OAuthHandler) cachedProvider(key providerCacheKey) (huboauth.Provider, bool) {
	h.providersMu.RLock()
	defer h.providersMu.RUnlock()
	cached, ok := h.providers[key]
	return cached, ok
}

// newProvider builds one client. It writes nothing and caches nothing; the
// caller owns both, because only the caller knows it holds the flight.
func (h *OAuthHandler) newProvider(
	ctx context.Context,
	snap *settings.Snapshot,
	dbProvider *store.OAuthProvider,
	key providerCacheKey,
) (huboauth.Provider, error) {
	clientSecret, err := h.keystore.Decrypt(dbProvider.ClientSecret, keystore.ProviderAAD(dbProvider.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt client secret: %w", err)
	}

	scopes := strings.Fields(dbProvider.Scopes)

	switch dbProvider.ProviderType {
	case huboauth.ProviderTypeOIDC:
		// OIDC discovery is an outbound call to a third party from an
		// unauthenticated route, so it gets a deadline of its own for the
		// same reason the token exchange does: this is a mux route, so it
		// never passes through the interceptor chain, and
		// http.DefaultClient has no timeout. Without it an identity
		// provider that accepts the connection and never answers parks the
		// handler for the process's life.
		discoverCtx, cancel := context.WithTimeout(ctx, settings.KeyTimeouts.Of(snap).APITimeout())
		defer cancel()
		return huboauth.NewOIDCProvider(discoverCtx, dbProvider.IssuerURL, dbProvider.ClientID, string(clientSecret), key.redirectURL, scopes)
	case huboauth.ProviderTypeGitHub:
		return huboauth.NewGitHubProvider(dbProvider.ClientID, string(clientSecret), key.redirectURL, scopes), nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", dbProvider.ProviderType)
	}
}

// cacheProvider stores one built Provider and drops that provider's entries
// under every OTHER redirect URL. The caller holds providersMu.
//
// The key carries the redirect URL because it derives from public_url and
// secure_cookies, which an administrator changes live -- and a key that
// changes with a setting is a key that accumulates one dead entry per edit,
// each holding its own OIDC client and discovery state for the life of the
// process. Only the current redirect URL can ever be looked up again, so
// the rest are unreachable rather than merely cold.
func (h *OAuthHandler) cacheProvider(key providerCacheKey, provider huboauth.Provider) {
	h.dropProviderLocked(key.providerID)
	h.providers[key] = provider
}

// InvalidateProvider drops every built instance of one provider.
//
// An administrator who removes or disables a provider makes its entry
// UNREACHABLE rather than stale: loadEnabledProvider refuses a deleted row
// with 404 and a disabled row with 403 before buildProvider runs, so no
// later request can evict it through cacheProvider. Without this call the
// built client -- holding the client secret the keystore decrypted -- stays
// in memory for the life of the process.
//
// Best effort by design. A request that read the row before the write can
// still insert an entry after this call. The eviction is hygiene, never
// access control: loadEnabledProvider's own row read is what refuses the
// flow.
func (h *OAuthHandler) InvalidateProvider(providerID string) {
	h.providersMu.Lock()
	defer h.providersMu.Unlock()
	h.dropProviderLocked(providerID)
}

// dropProviderLocked removes every entry for one provider id. The caller
// holds providersMu.
func (h *OAuthHandler) dropProviderLocked(providerID string) {
	for existing := range h.providers {
		if existing.providerID == providerID {
			delete(h.providers, existing)
		}
	}
}
