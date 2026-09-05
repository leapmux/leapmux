// Package service: the OAuth 2.1 authorization server. LeapMux is the SERVER
// here -- an app asks an account for access, and these endpoints run the
// ceremony. They live under /oauth/.
//
// The other direction, where the hub is a CLIENT of GitHub or an OIDC issuer,
// is idp_handler.go under /auth/idp/. The two prefixes state the direction, so
// neither name has to be read twice.
//
// What runs here: RFC 6749 section 4.1 authorization code with PKCE (RFC 7636),
// RFC 8628 device authorization, RFC 6749 section 6 refresh with rotation and
// reuse detection, RFC 7009 revocation, RFC 7591 dynamic registration behind an
// administrator's setting, RFC 8414 and RFC 9728 metadata, and one LeapMux
// extension: /oauth/step-up, which re-arms an app credential's elevation
// window.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	// AuthorizationCodeTTL is how long a one-shot authorization code lives.
	AuthorizationCodeTTL = 10 * time.Minute
	// DeviceCodeTTL is how long a device-code grant lives before expiring.
	DeviceCodeTTL = 10 * time.Minute
	// DeviceCodePollInterval is the recommended polling cadence the client
	// honours; the hub returns slow_down to throttle pollers exceeding it.
	DeviceCodePollInterval = 5 * time.Second
	// RefreshWorkTimeout limits detached singleflight work after the request
	// that became the leader disconnects.
	RefreshWorkTimeout = 15 * time.Second
	// CredentialNoticeTimeout limits the detached "an app credential was
	// issued" mail send. It is generous, because nothing waits for it: the
	// token response went out before the goroutine started. It exists so a
	// relay that accepts a connection and never answers cannot hold a
	// goroutine for the life of the process.
	CredentialNoticeTimeout = 30 * time.Second
	// elevatePath is the SPA route that runs a step-up ceremony and returns
	// the browser to where it came from.
	elevatePath = "/elevate"
	// The hub appends elevatedMarkerParam to the URL it sends the browser
	// back to after /elevate. Its ONLY effect is to stop this handler
	// redirecting a second time: a request carrying it that is still not
	// elevated gets an explanatory page instead of another bounce. It never
	// admits anything, so a hand-written one gains the caller nothing.
	elevatedMarkerParam = "elevated"
)

// OAuth 2.0 grant types accepted by /oauth/token. Values are RFC-defined wire
// identifiers, from oauthapp (the dependency-free constants home both sides
// of the wire already import):
//   - GrantTypeAuthorizationCode: RFC 6749 section 4.1.3
//   - GrantTypeDeviceCode: RFC 8628 section 3.4
//   - GrantTypeRefreshToken: RFC 6749 section 6
const (
	GrantTypeAuthorizationCode = oauthapp.GrantTypeAuthorizationCode
	GrantTypeDeviceCode        = oauthapp.GrantTypeDeviceCode
	GrantTypeRefreshToken      = oauthapp.GrantTypeRefreshToken
)

// OAuthServerDeps carries the handler's collaborators. A struct rather than a
// growing parameter list, matching AuthServiceDeps.
type OAuthServerDeps struct {
	Store     store.Store
	Validator *auth.TokenValidator
	Lifecycle *auth.CredentialLifecycleEffects
	// SoloUser is the account a SOLO hub authenticates its LOCAL IPC callers
	// as. It is nil on an ordinary hub, and that nil is what keeps the rung off
	// there.
	//
	// The consent endpoints need it because the desktop app holds no browser
	// session. The app reaches its own hub over the local IPC socket and has no
	// password to present. Without this rung it could never reach a consent
	// screen and could never authorize an app. The scope model would then be
	// unreachable on the deployment that most wants it: one machine, one
	// account, an agent that should hold file:read and nothing more. A solo TCP
	// caller signs in and reaches the same screens through the cookie rung.
	//
	// The BEARER rung stays off here whatever this is set to; see
	// requireSession. An app credential must not be able to consent on its own
	// behalf, which is the whole reason the step-up endpoint exists beside these.
	SoloUser *auth.UserInfo
	// SoloGate decides which solo callers may skip credentials. Nil is safe:
	// AuthenticateHTTP builds a throwaway gate over the store, which reads the
	// same rule and only loses the shared latch.
	SoloGate *auth.SoloGate
	// Settings reports the hub's own secure_cookies setting, which decides
	// which session-cookie spelling the consent endpoints read, and
	// open_app_registration, which decides whether /oauth/register answers.
	// Nil means "this hub does not write the __Host- prefix", which is the
	// safe reading for a handler wired without it: it widens nothing, because
	// the prefixed name is still tried first.
	Settings *settings.Manager
	// HubURL builds the device-code verification URLs and the metadata
	// documents' issuer.
	HubURL func() string
	// Limiter throttles the authorization server's ANONYMOUS endpoints -- every
	// route mounted through anonymousLeg: device authorization, the token
	// exchange, revocation, dynamic registration, step-up, and the app icons.
	// Nil disables the throttle, which is what a unit test wants and what a
	// hub never has.
	Limiter *ratelimit.Manager
	// Mail and Renderer send the "an app credential was issued" notice.
	//
	// Mail may be nil, and issuance then sends nothing. In the hub it never
	// is: mail.NewSettingsSender always returns a sender, which routes an
	// unconfigured relay to a disabled one internally. Renderer is a struct
	// value and CANNOT be nil; its zero value is valid and prints links with
	// an empty base URL.
	Mail     mail.Sender
	Renderer mail.Renderer
}

// OAuthServerHandler implements /oauth/* and the two metadata documents. It
// routes credential changes through lifecycle so cache, lease, and channel
// effects remain consistent.
type OAuthServerHandler struct {
	store         store.Store
	validator     *auth.TokenValidator
	soloUser      *auth.UserInfo
	soloGate      *auth.SoloGate
	lifecycle     *auth.CredentialLifecycleEffects
	hubURL        func() string
	mail          mail.Sender
	renderer      mail.Renderer
	set           *settings.Manager
	limiter       *ratelimit.Manager
	refreshFlight singleflight.Group

	// The clock every instant the handler mints or compares comes from:
	// grant expiry, the device-code slow_down throttle, token lifetimes,
	// and the elevation window.
	clockSeam
}

// NewOAuthServerHandler wires the handler.
func NewOAuthServerHandler(deps OAuthServerDeps) *OAuthServerHandler {
	if deps.Lifecycle == nil {
		panic("OAuth server requires credential lifecycle effects")
	}
	return &OAuthServerHandler{
		store:     deps.Store,
		validator: deps.Validator,
		soloUser:  deps.SoloUser,
		soloGate:  deps.SoloGate,
		lifecycle: deps.Lifecycle,
		hubURL:    deps.HubURL,
		mail:      deps.Mail,
		renderer:  deps.Renderer,
		set:       deps.Settings,
		limiter:   deps.Limiter,
	}
}

// RegisterRoutes mounts the authorization server on the mux.
func (h *OAuthServerHandler) RegisterRoutes(mux *http.ServeMux) {
	// RFC 8414 and RFC 9728 metadata. Both are anonymous by design: a client
	// library fetches them before it holds anything.
	mux.HandleFunc("/.well-known/oauth-authorization-server", h.handleAuthorizationServerMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", h.handleProtectedResourceMetadata)

	// The three CONSENT endpoints mount through consentLeg, so the check is a
	// property of the route rather than the first line somebody remembered
	// to write. The rest authenticate by grant, by bearer, or not at all.
	mux.HandleFunc("/oauth/authorize", h.consentLeg([]string{http.MethodGet, http.MethodHead}, h.handleAuthorize))
	mux.HandleFunc("/oauth/consent", h.consentLeg([]string{http.MethodPost}, h.handleConsent))
	// Short, because a human types it.
	mux.HandleFunc("/oauth/device", h.consentLeg([]string{http.MethodGet, http.MethodHead, http.MethodPost}, h.handleDevice))

	mux.HandleFunc("/oauth/device-authorization", h.anonymousLeg(h.handleDeviceAuthorization))
	mux.HandleFunc("/oauth/token", h.anonymousLeg(h.handleToken))
	mux.HandleFunc("/oauth/revoke", h.anonymousLeg(h.handleRevoke))
	mux.HandleFunc("/oauth/register", h.anonymousLeg(h.handleRegister))
	// The step-up endpoint. It is NOT a consent endpoint: the caller is an app
	// credential rather than a browser, and what it asks for is the right to
	// prove a factor -- which is exactly what it does not have yet. It mounts
	// through anonymousLeg because every bearer it validates drives store
	// reads an unauthenticated caller can loop.
	mux.HandleFunc("/oauth/step-up", h.anonymousLeg(h.handleStepUpAuthorization))
	// Same-origin app icons, so the consent page's img-src stays 'self'. Each
	// request reads the app row, so this endpoint shares the anonymous budget too.
	mux.HandleFunc("/oauth/apps/", h.anonymousLeg(h.handleAppAsset))
}

// anonymousLeg mounts one endpoint an unauthenticated caller can drive against
// the store, with the shared budget applied.
//
// The budget used to be the first line of exactly three handlers -- and then a
// fourth anonymous endpoint shipped unthrottled, because nothing recorded
// whether a route carries the budget the way consentLeg records the elevation
// check. An endpoint that reaches this mux now cannot mount without the
// throttle, so the fifth anonymous endpoint is protected by construction
// rather than by the author remembering.
//
// It returns true when it already wrote the refusal, matching the helper it
// wraps, so an endpoint that keeps an internal call for a sub-path reads the
// same way.
func (h *OAuthServerHandler) anonymousLeg(leg http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.throttleAnonymous(w, r) {
			return
		}
		leg(w, r)
	}
}

// --- Helpers ---

func (h *OAuthServerHandler) requireSession(r *http.Request) *auth.UserInfo {
	// A session cookie, or the SOLO account -- never a bearer.
	//
	// Leaving Validator nil is what unwires the bearer rung, and it is
	// deliberate: an app credential presenting itself here would be consenting
	// on its own behalf, which is the grant deciding its own width. The step-up
	// endpoint exists beside these precisely because that is not allowed.
	//
	// SoloUser is nil on an ordinary hub, so the rung is off there. On a solo
	// hub it is the only rung that can answer for the LOCAL IPC socket, which
	// carries no cookie and has no password to present. A solo TCP caller
	// holds an ordinary session and reaches these screens through the cookie
	// rung below.
	//
	// The hub's own secure_cookies setting decides which cookie spelling the
	// handler reads; see AuthenticateHTTP for why the fallback direction is
	// asymmetric.
	user, err := auth.AuthenticateHTTP(r.Context(), r, auth.HTTPAuthOpts{
		Store:         h.store,
		SoloUser:      h.soloUser,
		SoloGate:      h.soloGate,
		ReadCookie:    true,
		SecureCookies: h.secureCookies(r.Context()),
	})
	if err != nil {
		return nil
	}
	return user
}

// snapshot reads the hub's settings once. A handler wired with no manager
// reads the zero snapshot, which is the safe reading of every key this file
// consults.
//
// Package-level (settingsSnapshotOf) rather than a method on each service,
// because AppService and OAuthServerHandler answer the same question the same
// way and two copies of the nil rule could drift after an edit to one.
func (h *OAuthServerHandler) snapshot(ctx context.Context) *settings.Snapshot {
	return settingsSnapshotOf(h.set, ctx)
}

func settingsSnapshotOf(m *settings.Manager, ctx context.Context) *settings.Snapshot {
	if m == nil {
		return nil
	}
	return m.Snapshot(ctx)
}

// secureCookies reports whether this hub writes __Host- prefixed cookies.
// A handler wired with no settings manager reads false; see OAuthServerDeps.
func (h *OAuthServerHandler) secureCookies(ctx context.Context) bool {
	return settings.SecureCookiesFor(ctx, h.snapshot(ctx))
}

// gateMode says how an endpoint answers a caller that is not signed in or not
// elevated.
type gateMode int

const (
	// gateBounce sends the browser away and back again. Only a request that
	// is replayable from its URL alone may take it.
	gateBounce gateMode = iota
	// gateRefuse writes an error. A request that carries its parameters in
	// the BODY takes it -- handleConsent reads the PKCE challenge from the
	// form -- because a redirect destroys them irrecoverably. The user would
	// return to a consent page that forgot what it consented to.
	gateRefuse
)

// gateModeFor derives the mode from the request, because the method IS the
// rule. A GET (and a HEAD, which is a GET without the body) is replayable
// from its own URL; anything else carries parameters the redirect would
// destroy.
//
// DERIVED rather than passed. Each of the consent endpoints used to choose its
// own value, and every one of them chose it from its method -- so the caller
// could only ever get the parameter wrong, never vary it usefully. A new
// endpoint now cannot pick the mode that discards its own body.
func gateModeFor(r *http.Request) gateMode {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return gateBounce
	}
	return gateRefuse
}

// consentLeg mounts one consent endpoint: it restricts the methods the
// endpoint answers, and it demands an elevated session BEFORE the endpoint
// runs.
//
// The check used to be the first statement of each handler, and nothing made
// it so. user_procedures_internal_test.go's tripwire cannot reach here --
// these are mux routes, not Connect procedures -- so a new endpoint that
// shipped without its check would mint a credential from an unproven session
// and no suite would report it. An endpoint that needs the consenting
// identity now cannot compile onto the mux without passing through this.
//
// The METHOD check runs first, and the order is the rule: a wrong method on
// a restricted endpoint must answer 405 rather than bounce an anonymous
// caller through /elevate for a request the endpoint would refuse anyway.
//
// Elevation is required for EVERY grant, whatever the scope. Even a read-only
// grant mints a row that outlives the session by months, so the session alone
// is not enough -- the hub requires a recently proven factor, exactly as it
// does for a password change.
//
// It also SLIDES the window, on the same route-level terms. Consenting to an
// app is the most consequential thing a session can do, and it used to be the
// one restricted action that did not count as use: the hub bounced a user who
// elevated at 11:58 and consented at 11:59 through /elevate again at 12:01.
// Sliding here rather than inside each endpoint keeps the property that a new
// endpoint cannot mount without it.
//
// The slide runs BEFORE the endpoint, deliberately. An endpoint writes its own
// response -- a redirect, a consent page, a rendered form -- so there is no
// "after" on this surface that is still safe to touch, and the slide is
// best-effort anyway: it must never turn a served page into an error. What the
// ordering costs is that an endpoint which then fails still extended the
// window, which is the same answer the check already gave by admitting the
// request.
//
// The slide REPORTS nothing here, although the Connect surface reports on
// every slide (see ElevationExpiresAtHeader). Each route below renders a whole
// HTML document to a top-level navigation, so no client waits on a response
// header to read the new deadline off.
//
// A request ANOTHER DOCUMENT started slides nothing, and the reason is the
// one that guards the identity-provider re-authentication endpoint. Two of
// these endpoints answer GET, the session cookie is SameSite=Lax, and a
// top-level
// cross-site navigation carries it -- so without this an off-site page could
// point a signed-in victim's browser at /oauth/authorize and re-arm their
// elevation window for another two hours, as often as it liked, up to the
// absolute cap. "The window closes when I stop using the app" is the property
// the End now button and operating/security.md both rest on, and a third party
// must not be able to extend it.
//
// The endpoint still RUNS. It only reads the session and renders a page; the
// POST
// that actually grants is SameSite-protected already, and refusing the render
// would break nothing an attacker can reach but would add a second rule to a
// surface that needs one. What is refused is the WRITE.
//
// An app login is unaffected: the terminal or the app opens the address
// itself, which is Sec-Fetch-Site: none, and the bounce back from /elevate is
// same-origin.
func (h *OAuthServerHandler) consentLeg(methods []string, leg func(http.ResponseWriter, *http.Request, *auth.UserInfo)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(methods, r.Method) {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user := h.requireElevatedSession(w, r)
		if user == nil {
			return
		}
		if !httpsec.StartedByAnotherDocument(r) {
			slideElevation(r.Context(), h.store, user, h.now())
		}
		leg(w, r, user)
	}
}

// requireElevatedSession is the single check on the consent endpoints.
// It returns the acting user, or nil when it already wrote the response.
//
// Loop prevention is two INDEPENDENT layers, because a stale cache can defeat
// one of them and cannot defeat the other:
//
//   - /elevate returns immediately when the session is already elevated, so
//     the ordinary round trip ends after one bounce.
//   - elevatedMarkerParam turns the second bounce into an explanatory page.
//     It can only stop redirecting; it never admits, so a caller who writes
//     it by hand still elevates nothing.
func (h *OAuthServerHandler) requireElevatedSession(w http.ResponseWriter, r *http.Request) *auth.UserInfo {
	mode := gateModeFor(r)
	user := h.requireSession(r)
	if user == nil {
		if mode == gateBounce {
			h.redirectTo(w, r, "/login", r.URL.RequestURI())
			return nil
		}
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return nil
	}
	// SOLO has no ceremony to prove a factor with, which is the same answer
	// requireElevation gives on every other restricted surface. Its synthetic
	// user carries no credential row, so Elevated is permanently false and the
	// bounce below would send it to a /elevate page that can never succeed --
	// making app authorization impossible on exactly the deployment the scope
	// model most helps, where one agent should hold file:read and nothing more.
	//
	// Authentication is not weakened by this. The exemption reads
	// SoloAuthenticated, which is the LOCAL IPC identity alone: that caller
	// presented no credential, so it has no ceremony to step up from. A solo
	// TCP caller signed in with a password, holds a real session, and meets
	// the elevation check like anybody else.
	if user.SoloAuthenticated() || user.Elevated(h.now()) {
		return user
	}
	if mode == gateRefuse || r.URL.Query().Get(elevatedMarkerParam) != "" {
		h.writeElevationRequiredPage(w, mode)
		return nil
	}
	h.redirectTo(w, r, elevatePath, markElevationAttempted(r.URL))
	return nil
}

// markElevationAttempted returns the request URI with the marker appended,
// which is what the browser comes back to after /elevate.
func markElevationAttempted(u *url.URL) string {
	next := *u
	q := next.Query()
	q.Set(elevatedMarkerParam, "1")
	next.RawQuery = q.Encode()
	return next.RequestURI()
}

// redirectTo bounces the browser to an SPA route with a return address.
//
// The return address goes through sanitizeRedirectURI, the same sink guard
// the identity-provider handler uses, because it reaches a Location header by
// way of the SPA. A value that guard refuses becomes no parameter at all, so
// the user lands on the destination page rather than somewhere else.
func (h *OAuthServerHandler) redirectTo(w http.ResponseWriter, r *http.Request, path, returnTo string) {
	dest := path
	if safe := sanitizeRedirectURI(returnTo); safe != "" {
		dest = path + "?redirect=" + url.QueryEscape(safe)
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// writeElevationRequiredPage explains the refusal. A bounced GET endpoint gets
// an HTML page, because a browser reads it; a refused POST endpoint gets plain
// text, which is what the form post shows.
//
// BOTH answer 403. The status is the machine-readable half of the same
// answer, and the two endpoints refuse for one reason, so a page that said 200
// told a proxy, a health check or a test that the consent succeeded.
func (h *OAuthServerHandler) writeElevationRequiredPage(w http.ResponseWriter, mode gateMode) {
	const explanation = "Verify your identity before you authorize an app. " +
		"Open LeapMux in this browser, verify with your password or passkey, then start the authorization again."
	if mode == gateRefuse {
		http.Error(w, explanation, http.StatusForbidden)
		return
	}
	writePage(w, http.StatusForbidden, elevationRequiredPageTmpl, explanation, "")
}

// installationNameByteLimit caps a stored installation name.
//
// The narrowest column that holds one is MySQL's VARCHAR(255), which counts
// CHARACTERS, so 128 BYTES can never overflow it in any dialect -- and the
// other two spell the column TEXT, so without a cap the same anonymous POST
// succeeded on SQLite and Postgres and failed the INSERT with a 500 on
// MySQL. 128 bytes is also far more than a label anybody reads.
const installationNameByteLimit = 128

// normalizeInstallationName cleans an installation label at the boundary where
// it ENTERS the hub.
//
// Whoever runs the app chooses the value, and on the device-code stage that
// endpoint is anonymous -- so an attacker who persuades somebody to activate
// their user code chooses this text. It then reaches the consent page, the
// activation page, the account's connected-app list, the stored row, and the
// plain-text security notice that tells an owner a credential was issued.
//
// A newline in it writes arbitrary lines into the security notice, including a
// second signature delimiter and a forged hub address, so whoever created the
// credential could also write the one signal the docs call "how you learn
// about a credential you did not create".
//
// Normalizing at INTAKE rather than in the mail renderer fixes every one of
// those readers at once, and makes the column overflow impossible as well.
// validate.CleanNameTo already strips every control and invisible-format
// character, collapses a whitespace run to one space, and cuts on a rune
// boundary; an installation label is exactly the kind of one-line visible
// label it exists for.
func normalizeInstallationName(name string) string {
	return validate.CleanNameTo(name, installationNameByteLimit)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	// A token response and a metadata document must never be cached: one
	// carries a secret, and the other is what a client re-reads after an
	// operator changes the hub.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeOAuthErrorBody(w, status, oauthErrorBody(code, description))
}

// writeOAuthErrorBody writes a body a caller already built. The poll guards
// RETURN their refusal rather than writing it, because one of them must
// update last_polled_at between deciding and answering.
func writeOAuthErrorBody(w http.ResponseWriter, status int, body oauthErrorResponse) {
	// RFC 6750 section 3: a 401 from a protected resource states the scheme the
	// client should retry with. Without it a conformant client library has no
	// way to tell "your token is wrong" from "this endpoint wants something
	// else entirely".
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer error="`+body.Error+`"`)
	}
	writeJSON(w, status, body)
}

type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func oauthErrorBody(code, description string) oauthErrorResponse {
	return oauthErrorResponse{
		Error:            code,
		ErrorDescription: description,
	}
}

func writeInternalError(w http.ResponseWriter, operation string, err error) {
	slog.Error(operation, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// throttleAnonymous applies the shared anonymous-endpoint budget, keyed by
// client address.
//
// EVERY endpoint mounted through anonymousLeg draws on it: device
// authorization, the token exchange, revocation, dynamic registration,
// step-up, and the app icons. All of them read the store for a caller that
// carries no session, and no Connect interceptor sees them --
// ratelimit.NewInterceptor is a Connect interceptor, and none of these is a
// Connect procedure.
//
// It returns true when it already wrote the refusal. anonymousLeg calls it at
// mounting, so no endpoint reaches the mux without it.
func (h *OAuthServerHandler) throttleAnonymous(w http.ResponseWriter, r *http.Request) bool {
	if h.limiter == nil {
		return false
	}
	if ratelimit.AllowHTTP(r.Context(), h.limiter, ratelimit.OpOAuthAnonymous, r) {
		return false
	}
	// RFC 6749 defines no code for this, and RFC 6585 section 4 defines the
	// status. `slow_down` is the closest OAuth vocabulary and RFC 8628 already
	// gives it the meaning a client acts on: wait, then retry.
	writeOAuthError(w, http.StatusTooManyRequests, "slow_down",
		"too many requests from this address; wait and try again")
	return true
}

// --- App resolution -------------------------------------------------------

// errAppUnavailable reports a client_id that identifies no app this request
// may use: unknown, revoked, or a private app belonging to somebody else.
//
// ONE error for all three, because the caller must not disclose which. An app
// list that answers "unknown" for one id and "not yours" for another lets any
// anonymous caller enumerate the private registrations on the hub.
var errAppUnavailable = errors.New("unknown or unavailable client_id")

// appUnavailableBody is the refusal an unresolvable client_id answers with.
// The sentinel's text is the one spelling: a call site that quoted the
// sentence by hand could drift from it, and the anti-enumeration rule the
// sentinel states is only as strong as every endpoint meaning the same thing
// by it.
func appUnavailableBody() oauthErrorResponse {
	return oauthErrorBody("invalid_client", errAppUnavailable.Error())
}

// resolveApp loads the app a request identifies, and refuses one this request
// may not use.
//
// `viewer` is the account the request acts for, or the zero id on an anonymous
// endpoint. A HUB-WIDE app is available to everyone including an anonymous
// caller;
// a PRIVATE one is available only to its owner, which is the whole of the
// visibility rule and it lives here rather than at each call site.
func (h *OAuthServerHandler) resolveApp(ctx context.Context, clientID string, viewer *auth.UserInfo) (*store.OAuthClient, error) {
	if clientID == "" {
		return nil, errAppUnavailable
	}
	app, err := h.store.OAuthClients().Get(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errAppUnavailable
		}
		return nil, err
	}
	if app.RevokedAt != nil {
		return nil, errAppUnavailable
	}
	if app.IsHubWide() {
		return app, nil
	}
	if viewer == nil || !viewer.ID.Matches(app.OwnerUserID) {
		return nil, errAppUnavailable
	}
	return app, nil
}

// appAllowsGrantType reports whether an app may run one flow.
//
// The service-account registration lists NO grant type, which is what makes it
// unable to run any flow at all -- it exists so api_tokens.client_id can stay
// NOT NULL, not so an administrator can log in through it.
func appAllowsGrantType(app *store.OAuthClient, grantType string) bool {
	return slices.Contains(strings.Fields(app.GrantTypes), grantType)
}

// appScopeCeiling is the widest grant any consent for this app may produce.
//
// An unreadable ceiling is the EMPTY set, not the whole vocabulary: a
// registration whose scope string drifted must grant nothing rather than
// everything.
func appScopeCeiling(app *store.OAuthClient) authscope.ScopeSet {
	set, err := authscope.Parse(app.Scopes)
	if err != nil {
		slog.Error("app registration carries an unreadable scope ceiling",
			"client_id", app.ClientID, "err", err)
		return authscope.ScopeSet{}
	}
	return set
}

// resolveRequestedScopes decides what a consent may grant.
//
// It refuses rather than silently narrows, in each of the three ways it can
// fail, and the reason is the same one every time: a client told "you are
// authorized" and then refused on its first call has nothing to point at.
//
//   - An UNKNOWN scope token answers invalid_scope. This must happen BEFORE
//     anything renders, or `scope=read+Also+give+them+your+password` writes an
//     extra bullet into the consent list.
//   - A scope outside the app's registered ceiling answers invalid_scope.
//   - An ADMIN scope on an account that is not an administrator answers
//     access_denied. Downgrading instead would report a successful login and
//     then fail on the first admin verb.
//
// An EMPTY request takes the app's whole ceiling, which is what RFC 6749
// section 3.3 allows a server to do and what every client that omits `scope`
// expects.
func resolveRequestedScopes(raw string, app *store.OAuthClient, user *auth.UserInfo) (authscope.ScopeSet, *oauthErrorResponse) {
	// Closed, so the ceiling states every IMPLIED permission of the ones the
	// registration lists: a row that lists git:write carries git:read, and an
	// ask checked against the unclosed string could be closed PAST the check.
	ceiling := appScopeCeiling(app).Close()
	// RFC 6749 section 3.3 lets the server pick a default for an omitted
	// scope, and the default is NOT the ceiling.
	//
	// The ceiling is what the registration ALLOWS the app to ask for. Reading
	// silence as "all of it" made an omitted form field the widest possible
	// ask: a consent form that lost its scope input asked an administrator to
	// grant hub administration, and the page said so in a list nobody expects
	// to read. So an admin permission must be NAMED, and an app that wants one
	// asks for it.
	requested := ceiling.Without(adminScopeList...)
	if strings.TrimSpace(raw) != "" {
		parsed, err := authscope.Parse(raw)
		if err != nil {
			body := oauthErrorBody("invalid_scope", err.Error())
			return authscope.ScopeSet{}, &body
		}
		if !ceiling.Contains(parsed) {
			body := oauthErrorBody("invalid_scope",
				"this app is not registered for every permission it asked for")
			return authscope.ScopeSet{}, &body
		}
		requested = parsed.Close()
	}
	if user != nil && !user.IsAdmin {
		if _, found := firstAdminScope(requested); found {
			body := oauthErrorBody("access_denied",
				"your account is not a hub administrator, so it cannot grant an admin permission")
			return authscope.ScopeSet{}, &body
		}
	}
	// The closure runs at the MINT, so the stored set, the consent screen and
	// the token response all show the same thing.
	return requested, nil
}

// adminScopeList is the hub-administration family, read from the one place
// that owns it. Two refusals in this package ask the same question -- a
// consent endpoint refusing a non-administrator, and the admin mint refusing
// to
// issue an admin credential for somebody else -- and a local literal would be
// a third copy of a list that must never disagree.
var adminScopeList = authscope.AdminScopes()
