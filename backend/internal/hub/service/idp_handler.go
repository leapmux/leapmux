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

// IdPHandler serves the INBOUND direction of OAuth: the hub is a client of an
// identity provider (GitHub, Google, Apple, a generic OIDC issuer), and this
// handler runs the login, the re-authentication and the callback legs at
// /auth/idp/<provider>/*.
//
// The OUTBOUND direction -- the hub as an OAuth 2.1 authorization server that a
// third-party app asks for access -- lives in the oauth_server_* family under
// /oauth/. The two prefixes state the direction, so neither name has to be read
// twice.
type IdPHandler struct {
	store store.Store
	cfg   *config.Config
	set   *settings.Manager
	// lifecycle drops the cached UserInfo after the re-authentication leg
	// elevates a session, so this process re-reads the new deadline instead
	// of serving the old one for the rest of the auth cache's life.
	lifecycle *auth.CredentialLifecycleEffects
	keystore  *keystore.Keystore

	// The clock every instant this handler mints or compares comes from.
	// It is the THIRD elevation path, and grantSessionElevation takes now as
	// a parameter precisely so each path passes its own seam rather than
	// inventing a fourth notion of the current instant -- this path had
	// none, so a test that moved the password and passkey paths forward
	// could not move this one, and completeOAuthReauth read the wall clock
	// three separate times inside one request.
	clockSeam

	// providers caches built Provider instances.
	//
	// The key carries the redirect URL as well as the provider ID, because
	// a built Provider includes BOTH and only one of them is immutable. The
	// row's own config cannot change after creation -- AdminIdPService
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
	// providerGen counts the invalidations of one provider id, under
	// providersMu.
	//
	// It is what stops a build that races an administrator's DELETE from
	// re-populating the cache after InvalidateProvider swept it. A caller
	// reads the count BEFORE it reads the provider row, and cacheProvider
	// compares that snapshot against the current count, so an invalidation
	// that lands anywhere between the row read and the insert makes the
	// insert a no-op. See buildProvider and cacheProvider.
	//
	// An entry is never removed. The key set is the provider ids an
	// administrator invalidated in this process, so it grows by one per
	// remove or disable and by nothing per request. Removing an entry would
	// re-open the race it exists to close: a build holding the old count
	// would then read a missing key as the same count and insert.
	providerGen map[string]uint64
	// providerFlight collapses concurrent COLD builds of one key onto one
	// discovery leg.
	//
	// The double-check under the write lock made the discarded instances
	// unreachable; it could not stop the code from BUILDING them. An OIDC
	// build is an
	// outbound round trip to the identity provider for
	// .well-known/openid-configuration plus the JWKS, so N cold callers ran N
	// of them and kept one -- on process start, and again whenever an
	// administrator's remove or disable evicts the entry.
	providerFlight singleflight.Group
}

// providerCacheKey identifies one built Provider. See IdPHandler.providers
// for why the redirect URL is part of the identity.
type providerCacheKey struct {
	providerID  string
	redirectURL string
}

// NewIdPHandler creates a new OAuth HTTP handler.
func NewIdPHandler(st store.Store, cfg *config.Config, set *settings.Manager, lifecycle *auth.CredentialLifecycleEffects, ks *keystore.Keystore) *IdPHandler {
	if lifecycle == nil {
		panic("oauth handler requires credential lifecycle effects")
	}
	return &IdPHandler{
		store:       st,
		cfg:         cfg,
		set:         set,
		lifecycle:   lifecycle,
		keystore:    ks,
		providers:   make(map[providerCacheKey]huboauth.Provider),
		providerGen: make(map[string]uint64),
	}
}

// IdPCompleteSignupPath is the SPA page a new identity-provider user lands on
// to choose a username, and the ONE application route that lives under a Go
// subtree.
//
// It is a constant because two places must agree on it and they are in
// different packages: the callback below redirects to it, and NewServer mounts
// the frontend handler at it. The mount is not optional -- RegisterRoutes owns
// /auth/idp/ as a subtree and answers 400 for any path that is not
// <provider>/<action>, so without an EXACT pattern outranking that subtree
// this address is swallowed and every provider sign-up dies on a 400.
//
// TestSPACompleteSignupOutranksTheIdPSubtree is the tripwire, because the
// failure is invisible until somebody signs up through a provider.
const IdPCompleteSignupPath = "/auth/idp/complete-signup"

// RegisterRoutes mounts the inbound identity-provider routes on the mux.
//
// A SUBTREE, so `/auth/idp/<provider>/<action>` reaches handleIdP. See
// IdPCompleteSignupPath for the one address underneath it that this handler
// must not answer.
func (h *IdPHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/idp/", h.handleIdP)
}

func (h *IdPHandler) handleIdP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/auth/idp/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid identity-provider path", http.StatusBadRequest)
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
		http.Error(w, "unknown identity-provider action", http.StatusBadRequest)
	}
}

// loadEnabledProvider fetches the provider from DB, checks that it is
// enabled, and
// builds the cached Provider instance. Returns the provider, its trust_email
// setting, and whether the load succeeded. Writes an HTTP error on failure.
//
// It reads the row on EVERY call: the provider cache holds the built client,
// not the row. A caller that needs the provider twice in one request passes
// the instance along instead of calling this again.
func (h *IdPHandler) loadEnabledProvider(w http.ResponseWriter, ctx context.Context, providerID string) (huboauth.Provider, bool, bool) {
	// Before the row read, never after. See buildProvider.
	gen := h.providerGeneration(providerID)
	dbProvider, err := h.store.OAuthProviders().GetByID(ctx, providerID)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return nil, false, false
	}
	if !dbProvider.Enabled {
		http.Error(w, "provider disabled", http.StatusForbidden)
		return nil, false, false
	}
	provider, err := h.buildProvider(ctx, dbProvider, gen)
	if err != nil {
		slog.Error("oauth: build provider", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false, false
	}
	return provider, dbProvider.TrustEmail, true
}

func (h *IdPHandler) handleLogin(w http.ResponseWriter, r *http.Request, providerID string) {
	provider, _, ok := h.loadEnabledProvider(w, r.Context(), providerID)
	if !ok {
		return
	}
	h.beginOAuthFlow(w, r, providerID, provider, store.OAuthStatePurposeLogin, "")
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
// The account shape must ALSO admit the path -- see providerMayElevateAccount
// for the one rule. Refusing here keeps the browser from ever leaving;
// completeOAuthReauth checks it again, because it is the leg that grants and
// the shape can move inside the state row's five minutes.
func (h *IdPHandler) handleReauth(w http.ResponseWriter, r *http.Request, providerID string) {
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
		Store:         h.store,
		ReadCookie:    true,
		SecureCookies: h.secureCookies(ctx),
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
	// This loads the provider before the shape check, and only to REFUSE a
	// disabled or missing one here rather than after the browser leaves
	// the app. The rule itself no longer depends on which provider this is.
	//
	// The instance travels on to beginOAuthFlow, so the row is read ONCE.
	// loadEnabledProvider always reads oauth_providers before it consults
	// the provider cache -- the cache holds the built client, not the row --
	// so calling it twice was a second read, and the two reads were not one
	// snapshot either.
	provider, _, ok := h.loadEnabledProvider(w, ctx, providerID)
	if !ok {
		return
	}
	if !h.providerMayElevateAccount(w, ctx, row) {
		return
	}
	h.beginOAuthFlow(w, r, providerID, provider, store.OAuthStatePurposeReauth, user.Credential.SessionID())
}

// providerMayElevateAccount answers the question both re-authentication legs
// ask, and writes the refusal itself. It reports whether the caller may
// continue.
//
// ONE rule: an account with NO password and NO passkey is signed in BY the
// provider, so the elevation is exactly as strong as the sign-in it stands
// on. Any linked provider qualifies, GitHub included --
// operating/security.md states that limit plainly. This refuses anything
// else, because the account holds a factor it can present directly and "the
// browser can still reach the provider session" is a weaker claim than the
// one that factor makes.
//
// The rule is accountElevatesOnlyThroughAProvider, CALLED rather than spelled
// again: the first-credential branch of stepUpMutationAuth reads the same
// two facts, and a copy meant a change to what counts as "a factor" could
// land in one and miss the other. There is no wrapper between the two either,
// because a name that only forwards is a difference a reader has to look for
// and never finds.
//
// There used to be a second tier for an account whose only factor is a
// passkey THIS HUB CANNOT RUN, bridged by a provider that fills auth_time.
// It is gone, and its absence is the design rather than an omission. A hub
// that cannot run a passkey ceremony has one cause -- it has no usable
// browser origin, because public_url is unset and nothing listens -- so
// the tier existed only to rescue users from an administrator's
// misconfiguration whose real remedy is to restore the address. Serving it
// cost a second freshness rule, a per-provider capability the code could not
// actually know (Google, Apple, Entra and GitLab-as-OP all report
// provider_type "oidc" and none of them fills auth_time on request), and an
// option the step-up screen offered and the grant leg then refused. Refusing
// the shape outright is both simpler and stricter: the account waits for an
// administrator to repair the hub instead of elevating on a weaker claim than
// the passkey it holds.
func (h *IdPHandler) providerMayElevateAccount(w http.ResponseWriter, ctx context.Context, user *store.User) bool {
	mayElevate, err := accountElevatesOnlyThroughAProvider(ctx, h.store, user)
	if err != nil {
		slog.Error("oauth: read account shape for reauth", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !mayElevate {
		// The refusal states the remedy rather than the rule: the account
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
//
// The CALLER loads the provider and passes the built instance in. The reauth
// leg has to load it anyway, to refuse a disabled or missing provider before
// the browser leaves the app, and loadEnabledProvider reads the
// oauth_providers row on every call -- so a load here as well made that leg
// read the row twice, off two different snapshots.
func (h *IdPHandler) beginOAuthFlow(w http.ResponseWriter, r *http.Request, providerID string, provider huboauth.Provider, purpose, sessionID string) {
	ctx := r.Context()

	verifier := oauth2.GenerateVerifier()
	state := id.Generate()
	// The nonce binds this flow to THIS browser. The state alone does not: it
	// travels in the callback URL, so whoever holds that URL can complete the
	// flow, and an attacker who starts a login with their own identity and
	// withholds the callback can give the live (code, state) pair to a victim
	// -- whose browser is then signed in as the attacker. Only the browser
	// that received this cookie can redeem the row. RFC 6749 section 10.12.
	//
	// On the reauth leg the binding matters just as much: the row specifies the
	// session it will elevate, so a state that anybody could redeem would
	// give an attacker's completed provider flow the victim's elevation.
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
func (h *IdPHandler) consumeOAuthState(w http.ResponseWriter, r *http.Request, providerID string) (code string, oauthState *store.OAuthState, ok bool) {
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
		// but this must not report a transient store error as "invalid state"
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
	// This builds the cookie name from the LOADED row's state, never from the
	// query parameter, so a caller cannot point the lookup at a name of its
	// own choosing.
	secureCookies := h.secureCookies(ctx)
	if !browserSecretMatches(oauthState.NonceHash, auth.OAuthNonceFromRequest(r, oauthState.State, secureCookies)) {
		http.Error(w, "a different browser started this sign-in; start again from the sign-in page", http.StatusBadRequest)
		return "", nil, false
	}

	// The owner presented the nonce, so consume the single-use row now and
	// clear the nonce on every outcome from here on, success included. A
	// failed delete leaves the row live for the rest of its expiry, which is
	// not worth failing the login over, but it must not be silent: it is the
	// only consumption of a single-use row in the hub.
	consumed, err := h.store.OAuthStates().Delete(ctx, state)
	if err != nil {
		slog.Error("oauth: delete state", "error", err, "provider_id", providerID)
	}
	for _, c := range auth.ClearOAuthNonceCookie(oauthState.State) {
		http.SetCookie(w, c)
	}
	// The delete's row count is what makes the single use the HUB's
	// property. Two callbacks carrying the same state and the same nonce
	// cookie -- a double-clicked callback, a browser prefetch of the
	// Location header, a retried navigation -- both reach this line, and
	// exactly one of them removes the row. This refuses the loser here
	// rather than at provider.Exchange, so the property no longer rests on
	// the identity provider rejecting the second use of its authorization
	// code. It matters most on the reauth purpose, whose completion grants
	// an elevation.
	//
	// A delete that FAILED is a different case and keeps its own trade
	// above: the row is still live, the count is 0 for a reason that is not
	// a second use, and refusing here would fail a legitimate login on a
	// transient store failure.
	if err == nil && consumed == 0 {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return "", nil, false
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

func (h *IdPHandler) handleCallback(w http.ResponseWriter, r *http.Request, providerID string) {
	ctx := r.Context()

	code, oauthState, ok := h.consumeOAuthState(w, r, providerID)
	if !ok {
		return
	}

	provider, trustEmail, ok := h.loadEnabledProvider(w, ctx, providerID)
	if !ok {
		return
	}

	// Exchange code for tokens, under a deadline this handler sets.
	//
	// This is a mux route, so it never passes through the RPC interceptor chain
	// that limits every Connect handler -- and the leg it makes is an outbound
	// call to a third party. golang.org/x/oauth2 runs it on http.DefaultClient
	// unless the context carries an oauth2.HTTPClient, and http.DefaultClient
	// has NO timeout, so without this deadline an identity provider that accepts
	// the connection and never answers blocks this handler until the hub shuts
	// down (the http.Server's shutdown-scoped BaseContext cancels it then, but a
	// stuck IdP during NORMAL operation would otherwise block it for the
	// process's life). This per-leg deadline limits exactly that: a slow or
	// stuck IdP while the hub is healthy.
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
	//
	// This switch spells out both purposes, and REFUSES an unrecognised one.
	// The login leg is the one that creates, so it must never be the branch a
	// value reaches by not matching something else -- and the Go zero value
	// "" is exactly such a value. oauth_states.purpose carries
	// CHECK (purpose IN ('login','reauth')) in all three dialects, so a row
	// cannot hold anything else; this branch keeps the two statements of the
	// rule in agreement rather than trusting one of them alone.
	switch oauthState.Purpose {
	case store.OAuthStatePurposeReauth:
		h.completeOAuthReauth(w, r, oauthState, link, err)
		return
	case store.OAuthStatePurposeLogin:
		// The rest of this function is the login leg.
	default:
		slog.Error("oauth: state row carries an unknown purpose",
			"purpose", oauthState.Purpose, "provider_id", providerID)
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}

	// Require a valid email from the OAuth provider.
	//
	// AFTER the re-authentication branch, because that leg never reads the
	// address: it identifies the person by claims.Subject, through the link
	// this lookup just made. Demanding one refused every step-up on a
	// provider that stopped reporting a VERIFIED address -- and for the
	// accounts this path serves, which hold neither a password nor a passkey,
	// it is the only path they have. The refusal even blamed the `email`
	// scope, which the operator already granted.
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
		// store failure: on such a failure, GetByEmail must NOT be read as
		// absence, or a returning verified user is either turned into a permanent
		// 403 (signup disabled) or duplicated into a fresh account (signup
		// enabled). Mirror the ErrNotFound-vs-internal split the
		// OAuthUserLinks().Get read above uses.
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
	http.Redirect(w, r, IdPCompleteSignupPath+"?token="+signupToken, http.StatusFound)
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
//
// It reads NO claim the provider returned, and takes none: the identity is
// the oauth_user_links row the caller already found, and every other claim
// (the address, the display name, auth_time) is a value this leg refuses to
// act on. A parameter it never reads would advertise the opposite.
func (h *IdPHandler) completeOAuthReauth(
	w http.ResponseWriter,
	r *http.Request,
	oauthState *store.OAuthState,
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
	// This loads the session rather than trusting the state row alone, because
	// the elevation must land on a session that is still live and because
	// its user_id is what this compares the link against.
	session, err := h.store.Sessions().GetByID(ctx, oauthState.SessionID, now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "your session ended during the verification; sign in again", http.StatusUnauthorized)
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
	// This checks the account shape HERE as well as at the start leg, because
	// this is where the grant happens and the shape can move in between: the
	// state row lives for oauthStateExpiry, and the first-credential rule
	// lets an account attach a password in another tab inside that window. A
	// row minted while the account held nothing would otherwise gain
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
	// cache invalidation are the same ones the password and passkey paths use.
	if _, err := grantSessionElevation(ctx, h.store, h.lifecycle, oauthState.SessionID, uid, now); err != nil {
		if errors.Is(err, errElevationSessionEnded) {
			http.Error(w, "your session ended during the verification; sign in again", http.StatusUnauthorized)
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
// beginOAuthFlow minted the state row, so it needs no second guard here.
func (h *IdPHandler) redirectBack(w http.ResponseWriter, r *http.Request, redirectURI string) {
	redirectTo := "/"
	if redirectURI != "" {
		redirectTo = redirectURI
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

// secureCookies reports whether this hub writes __Host- prefixed cookies.
func (h *IdPHandler) secureCookies(ctx context.Context) bool {
	return settings.KeySecureCookies.Of(h.set.Snapshot(ctx))
}

// loginOAuthUser stores tokens, creates a session, and redirects the user.
// Both the existing-link and auto-link-by-email paths call it.
func (h *IdPHandler) loginOAuthUser(w http.ResponseWriter, r *http.Request, userID, providerID string, tokenSet *huboauth.TokenSet, redirectURI string) {
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
// beyond the obvious "//host" pass an RFC 3986 parser:
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
// fallback, and CleanName cuts a claim over the byte limit instead of
// refusing it. Its result is idempotent and already within the limit, so the
// later SanitizeDisplayName passes it through unchanged.
//
// An empty return value means the provider supplied no usable name, and the
// caller that reads the stored row then falls back to the username.
func cleanProviderDisplayName(claims *huboauth.UserClaims) string {
	if name := validate.CleanName(claims.DisplayName); name != "" {
		return name
	}
	return validate.CleanName(claims.Name)
}

func (h *IdPHandler) storePendingSignup(ctx context.Context, providerID string, claims *huboauth.UserClaims, tokenSet *huboauth.TokenSet, redirectURI, nonceHash string) (string, error) {
	token := id.Generate()

	// Use the signup token as entity ID for AAD since the user does not exist yet.
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

// encryptTokenPair encrypts an access/refresh token pair. The AAD includes
// the entityID, which is typically a user ID or a pending-signup token.
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

func (h *IdPHandler) storeTokens(ctx context.Context, userID, providerID string, tokenSet *huboauth.TokenSet) error {
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

// buildProvider returns the built client for one provider row, from the
// cache when it is warm and through one collapsed build when it is not.
//
// builtAtGen is the invalidation count the caller read BEFORE it read
// dbProvider, and the order is the whole point. The row read is the caller's
// last evidence that the provider still exists, so a count taken before it
// covers every instant from that evidence to the insert: an administrator's
// remove or disable that lands anywhere in between moves the count and
// cacheProvider refuses. A count taken AFTER the row read would leave the
// gap between the two, and a count taken by this function could not see the
// caller's read at all.
func (h *IdPHandler) buildProvider(ctx context.Context, dbProvider *store.OAuthProvider, builtAtGen uint64) (huboauth.Provider, error) {
	snap := h.set.Snapshot(ctx)
	key := providerCacheKey{
		providerID:  dbProvider.ID,
		redirectURL: fmt.Sprintf("%s/auth/idp/%s/callback", settings.BaseURL(snap, h.cfg.Listen), dbProvider.ID),
	}

	if cached, ok := h.cachedProvider(key); ok {
		return cached, nil
	}

	// ONE build per key, however many cold callers arrive together. The
	// flight key is the WHOLE cache key: two redirect URLs build two
	// different clients, and collapsing them would give a follower a client
	// built with somebody else's redirect_uri.
	//
	// The build runs on a context DETACHED from the leader's request.
	// singleflight cancels nothing itself, so the shared leg would otherwise
	// carry the leader's context and one abandoned tab would fail discovery
	// for every waiter -- the trade refresh.go states for its own flight, and
	// settings.Manager takes for the same reason. It keeps its own deadline,
	// so a stuck identity provider still cannot block the work for ever.
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
		// The LEADER's builtAtGen decides the insert, because this closure
		// is the leader's. A follower supplied its own count and its own row
		// read, and it shares the instance rather than the insert -- the
		// cached entry stands on one caller's evidence pair, and this is
		// that caller.
		h.cacheProvider(key, provider, builtAtGen)
		return provider, nil
	})
	if err != nil {
		return nil, err
	}
	return built.(huboauth.Provider), nil
}

// cachedProvider reads one built instance under the read lock.
func (h *IdPHandler) cachedProvider(key providerCacheKey) (huboauth.Provider, bool) {
	h.providersMu.RLock()
	defer h.providersMu.RUnlock()
	cached, ok := h.providers[key]
	return cached, ok
}

// newProvider builds one client. It writes nothing and caches nothing; the
// caller owns both, because only the caller knows it holds the flight.
func (h *IdPHandler) newProvider(
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
		// provider that accepts the connection and never answers blocks the
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
//
// builtAtGen is the invalidation count the build snapshotted before it
// started. A mismatch means an administrator invalidated this provider id
// while the build ran, so this refuses the insert and drops the built
// client.
//
// The refusal exists because remove and disable are NOT the same case.
// After a DISABLE, a later request re-reads the row and cacheProvider
// evicts the resurrected entry, so a lost eviction repairs itself. After a
// REMOVE there is no later call at all: loadEnabledProvider answers 404 on
// the missing row before buildProvider runs, re-adding the provider mints a
// fresh id, and nothing calls dropProviderLocked for the old id again --
// so an entry inserted after InvalidateProvider swept the map holds the
// keystore-decrypted client secret for the life of the process.
func (h *IdPHandler) cacheProvider(key providerCacheKey, provider huboauth.Provider, builtAtGen uint64) {
	if h.providerGen[key.providerID] != builtAtGen {
		return
	}
	h.dropProviderLocked(key.providerID)
	h.providers[key] = provider
}

// providerGeneration reads one provider id's invalidation count under the
// read lock. A build takes it before it starts and passes it to
// cacheProvider.
func (h *IdPHandler) providerGeneration(providerID string) uint64 {
	h.providersMu.RLock()
	defer h.providersMu.RUnlock()
	return h.providerGen[providerID]
}

// InvalidateProvider drops every built instance of one provider, and bars
// an in-flight build of the same provider from inserting a new one.
//
// An administrator who removes or disables a provider makes its entry
// UNREACHABLE rather than stale: loadEnabledProvider refuses a deleted row
// with 404 and a disabled row with 403 before buildProvider runs, so no
// later request can evict it through cacheProvider. Without this call the
// built client -- holding the client secret the keystore decrypted -- stays
// in memory for the life of the process.
//
// The eviction is hygiene, never access control: loadEnabledProvider's own
// row read is what refuses the flow.
func (h *IdPHandler) InvalidateProvider(providerID string) {
	h.providersMu.Lock()
	defer h.providersMu.Unlock()
	h.providerGen[providerID]++
	h.dropProviderLocked(providerID)
}

// dropProviderLocked removes every entry for one provider id. The caller
// holds providersMu.
func (h *IdPHandler) dropProviderLocked(providerID string) {
	for existing := range h.providers {
		if existing.providerID == providerID {
			delete(h.providers, existing)
		}
	}
}
