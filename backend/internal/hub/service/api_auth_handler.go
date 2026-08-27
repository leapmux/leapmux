// Package service: API auth handler exposes the leapmux control CLI auth
// flows (local-redirect with PKCE, RFC 8628 device-code) and the bearer
// refresh / revoke endpoints. Endpoints live at /auth/cli/*.
package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/verifycode"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	// CLIAuthCodeTTL is how long a one-shot local-redirect code lives.
	CLIAuthCodeTTL = 10 * time.Minute
	// DeviceCodeTTL is how long a device-code grant lives before expiring.
	DeviceCodeTTL = 10 * time.Minute
	// DeviceCodePollInterval is the recommended polling cadence the CLI
	// honours; the hub returns slow_down to throttle pollers exceeding it.
	DeviceCodePollInterval = 5 * time.Second
	// RefreshWorkTimeout limits detached singleflight work after the request
	// that became the leader disconnects.
	RefreshWorkTimeout = 15 * time.Second
	// CredentialNoticeTimeout limits the detached "a CLI credential was
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

// APIAuthHandlerDeps carries the handler's collaborators. A struct rather
// than a growing parameter list, matching AuthServiceDeps.
type APIAuthHandlerDeps struct {
	Store     store.Store
	Validator *auth.TokenValidator
	Lifecycle *auth.CredentialLifecycleEffects
	// Settings reports the hub's own secure_cookies setting, which decides
	// which session-cookie spelling the consent legs read. Nil means "this
	// hub does not write the __Host- prefix", which is the safe reading for
	// a handler wired without it: it widens nothing, because the prefixed
	// name is still tried first.
	Settings *settings.Manager
	// HubURL builds the device-code verification URLs returned to the CLI.
	HubURL func() string
	// Mail and Renderer send the "a CLI credential was issued" notice.
	//
	// Mail may be nil, and issuance then sends nothing. In the hub it never
	// is: mail.NewSettingsSender always returns a sender, which routes an
	// unconfigured relay to a disabled one internally. Renderer is a struct
	// value and CANNOT be nil; its zero value is valid and prints links with
	// an empty base URL.
	Mail     mail.Sender
	Renderer mail.Renderer
}

// APIAuthHandler implements /auth/cli/*. It routes credential changes through
// lifecycle so cache, lease, and channel effects remain consistent.
type APIAuthHandler struct {
	store         store.Store
	validator     *auth.TokenValidator
	lifecycle     *auth.CredentialLifecycleEffects
	hubURL        func() string
	mail          mail.Sender
	renderer      mail.Renderer
	set           *settings.Manager
	refreshFlight singleflight.Group

	// The clock every instant the handler mints or compares comes from:
	// grant expiry, the device-code slow_down throttle, token lifetimes,
	// and the elevation gate.
	clockSeam
}

// NewAPIAuthHandler wires the handler.
func NewAPIAuthHandler(deps APIAuthHandlerDeps) *APIAuthHandler {
	if deps.Lifecycle == nil {
		panic("API auth handler requires credential lifecycle effects")
	}
	return &APIAuthHandler{
		store:     deps.Store,
		validator: deps.Validator,
		lifecycle: deps.Lifecycle,
		hubURL:    deps.HubURL,
		mail:      deps.Mail,
		renderer:  deps.Renderer,
		set:       deps.Settings,
	}
}

// RegisterRoutes mounts the handler's routes on the mux.
func (h *APIAuthHandler) RegisterRoutes(mux *http.ServeMux) {
	// The three CONSENT legs mount through consentLeg, so the gate is a
	// property of the route rather than the first line somebody remembered
	// to write. The other four authenticate by grant or by bearer, and no
	// gate applies to them by design.
	mux.HandleFunc("/auth/cli/start", h.consentLeg([]string{http.MethodGet, http.MethodHead}, h.handleStart))
	mux.HandleFunc("/auth/cli/authorize", h.consentLeg([]string{http.MethodPost}, h.handleAuthorize))
	mux.HandleFunc("/auth/cli/device-authorization", h.handleDeviceAuthorization)
	// The step-up leg. It is NOT a consent leg: the caller is a command-line
	// credential rather than a browser, and what it asks for is the right to
	// prove a factor -- which is exactly what it does not have yet.
	mux.HandleFunc("/auth/cli/elevate-authorization", h.handleElevateAuthorization)
	mux.HandleFunc("/auth/cli/activate", h.consentLeg([]string{http.MethodGet, http.MethodHead, http.MethodPost}, h.handleActivate))
	mux.HandleFunc("/auth/cli/token", h.handleToken)
	mux.HandleFunc("/auth/cli/refresh", h.handleRefresh)
	mux.HandleFunc("/auth/cli/revoke", h.handleRevoke)
}

// --- Helpers ---

func (h *APIAuthHandler) requireSession(r *http.Request) *auth.UserInfo {
	// /auth/cli/* endpoints only accept session cookies; leaving
	// Validator/SoloUser nil unwires the bearer and solo rungs. The hub's own
	// secure_cookies setting decides which spelling the handler reads; see
	// AuthenticateHTTP for why the fallback direction is asymmetric.
	user, err := auth.AuthenticateHTTP(r.Context(), r, auth.HTTPAuthOpts{
		Store:         h.store,
		ReadCookie:    true,
		SecureCookies: h.secureCookies(r.Context()),
	})
	if err != nil {
		return nil
	}
	return user
}

// secureCookies reports whether this hub writes __Host- prefixed cookies.
// A handler wired with no settings manager reads false; see
// APIAuthHandlerDeps.Settings.
func (h *APIAuthHandler) secureCookies(ctx context.Context) bool {
	if h.set == nil {
		return false
	}
	return settings.KeySecureCookies.Of(h.set.Snapshot(ctx))
}

// gateMode says how a leg answers a caller that is not signed in or not
// elevated.
type gateMode int

const (
	// gateBounce sends the browser away and back again. Only a request that
	// is replayable from its URL alone may take it.
	gateBounce gateMode = iota
	// gateRefuse writes an error. A request that carries its parameters in
	// the BODY takes it -- handleAuthorize reads the PKCE challenge from the
	// form -- because a redirect destroys them irrecoverably. The user would
	// return to a consent page that forgot what it consented to.
	gateRefuse
)

// gateModeFor derives the mode from the request, because the method IS the
// rule. A GET (and a HEAD, which is a GET without the body) is replayable
// from its own URL; anything else carries parameters the redirect would
// destroy.
//
// DERIVED rather than passed. Each of the four consent legs used to choose
// its own value, and every one of them chose it from its method -- so the
// caller could only ever get the parameter wrong, never vary it usefully. A
// fifth leg now cannot pick the mode that discards its own body.
func gateModeFor(r *http.Request) gateMode {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return gateBounce
	}
	return gateRefuse
}

// consentLeg mounts one CLI consent leg: it restricts the methods the leg
// answers, and it demands an elevated session BEFORE the leg runs.
//
// The gate used to be the first statement of each handler, and nothing made
// it so. user_procedures_internal_test.go's tripwire cannot reach here --
// these are mux routes, not Connect procedures -- so a fifth leg that
// shipped without its gate would mint a credential from an unproven session
// and no suite would report it. A leg that needs the consenting identity
// now cannot compile onto the mux without passing through this.
//
// The METHOD check runs first, and the order is the rule: a wrong method on
// a restricted leg must answer 405 rather than bounce an anonymous caller
// through /elevate for a request the leg would refuse anyway.
//
// It also SLIDES the window, on the same route-level terms. Consenting to a
// command-line credential is the most consequential thing a session can do,
// and it used to be the one restricted action that did not count as use: the
// hub bounced a user who elevated at 11:58 and consented at 11:59 through
// /elevate again at 12:01. Sliding here rather than inside each leg keeps the
// property that a fifth leg cannot mount without it.
//
// The slide runs BEFORE the leg, deliberately. A leg writes its own response
// -- a redirect, a consent page, a rendered form -- so there is no "after"
// on this surface that is still safe to touch, and the slide is best-effort
// anyway: it must never turn a served page into an error. What the ordering
// costs is that a leg which then fails still extended the window, which is
// the same answer the gate already gave by admitting the request.
//
// The slide REPORTS nothing here, although the Connect surface reports on
// every slide (see ElevationExpiresAtHeader). Each route below renders a whole
// HTML document to a top-level navigation, so no client waits on a response
// header to read the new deadline off.
//
// A request ANOTHER DOCUMENT started slides nothing, and the reason is the
// one that guards the OAuth re-authentication leg. Two of these legs answer
// GET, the session cookie is SameSite=Lax, and a top-level cross-site
// navigation carries it -- so without this an off-site page could point a
// signed-in victim's browser at /auth/cli/start and re-arm their elevation
// window for another two hours, as often as it liked, up to the absolute cap.
// "The window closes when I stop using the app" is the property the End now
// button and operating/security.md both rest on, and a third party must not
// be able to extend it.
//
// The leg still RUNS. It only reads the session and renders a page; the POST
// that actually grants is SameSite-protected already, and refusing the render
// would break nothing an attacker can reach but would add a second rule to a
// surface that needs one. What is refused is the WRITE.
//
// A CLI login is unaffected: the terminal opens the address itself, which is
// Sec-Fetch-Site: none, and the bounce back from /elevate is same-origin.
func (h *APIAuthHandler) consentLeg(methods []string, leg func(http.ResponseWriter, *http.Request, *auth.UserInfo)) http.HandlerFunc {
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

// requireElevatedSession is the single gate on the four CLI consent legs.
// It returns the acting user, or nil when it already wrote the response.
//
// Minting a CLI credential is the most consequential thing a session can do:
// what it hands back outlives the session by months, and with --admin it
// administers the hub. So the session alone is not enough -- the hub
// requires a recently proven factor, exactly as it does for a password
// change.
//
// Loop prevention is two INDEPENDENT layers, because a stale cache can defeat
// one of them and cannot defeat the other:
//
//   - /elevate returns immediately when the session is already elevated, so
//     the ordinary round trip ends after one bounce.
//   - elevatedMarkerParam turns the second bounce into an explanatory page.
//     It can only stop redirecting; it never admits, so a caller who writes
//     it by hand still elevates nothing.
func (h *APIAuthHandler) requireElevatedSession(w http.ResponseWriter, r *http.Request) *auth.UserInfo {
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
	if user.Elevated(h.now()) {
		return user
	}
	if mode == gateRefuse || r.URL.Query().Get(elevatedMarkerParam) != "" {
		writeElevationRequiredPage(w, mode)
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
// the OAuth handler uses, because it reaches a Location header by way of the
// SPA. A value that guard refuses becomes no parameter at all, so the user
// lands on the destination page rather than somewhere else.
func (h *APIAuthHandler) redirectTo(w http.ResponseWriter, r *http.Request, path, returnTo string) {
	dest := path
	if safe := sanitizeRedirectURI(returnTo); safe != "" {
		dest = path + "?redirect=" + url.QueryEscape(safe)
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// writeElevationRequiredPage explains the refusal. A bounced GET leg gets an
// HTML page, because a browser reads it; a refused POST leg gets plain text,
// which is what the form post shows.
//
// BOTH answer 403. The status is the machine-readable half of the same
// answer, and the two legs refuse for one reason, so a page that said 200
// told a proxy, a health check or a test that the consent succeeded.
func writeElevationRequiredPage(w http.ResponseWriter, mode gateMode) {
	const explanation = "Verify your identity before authorizing CLI access. " +
		"Open LeapMux in this browser, verify with your password or passkey, then start `leapmux control auth login` again."
	if mode == gateRefuse {
		http.Error(w, explanation, http.StatusForbidden)
		return
	}
	writePage(w, http.StatusForbidden, elevationRequiredPageTmpl, explanation)
}

// deviceNameByteLimit caps a stored device name.
//
// The narrowest column that holds one is MySQL's VARCHAR(255), which counts
// CHARACTERS, so 128 BYTES can never overflow it in any dialect -- and the
// other two spell the column TEXT, so without a cap the same anonymous POST
// succeeded on SQLite and Postgres and failed the INSERT with a 500 on
// MySQL. 128 bytes is also far more than a device label anybody reads.
const deviceNameByteLimit = 128

// normalizeDeviceName cleans a device name at the boundary where it ENTERS
// the hub.
//
// Whoever runs the CLI chooses the value, and on the device-code leg
// that endpoint is anonymous -- so an attacker who persuades somebody to
// activate their user code chooses this text. It then reaches the consent
// page, the activation page, the account's CLI-credential list, the stored
// row, and the plain-text security notice that tells an owner a credential
// was issued.
//
// grantDeviceName is what puts it on the activation page, so a device-code
// consent is about a device the user can identify; cleaning at intake is
// what makes that page safe to render it on.
//
// A newline in it writes arbitrary lines into the security notice,
// including a second signature delimiter and a forged hub address, so
// whoever created the credential could also write the one signal the docs
// call "how you learn about a credential you did not create".
//
// Normalizing at INTAKE rather than in the mail renderer fixes every one of
// those readers at once, and makes the column overflow impossible as well.
// validate.CleanNameTo already strips every control and invisible-format
// character, collapses a whitespace run to one space, and cuts on a rune
// boundary; a device name is exactly the kind of one-line visible label it
// exists for.
func normalizeDeviceName(name string) string {
	return validate.CleanNameTo(name, deviceNameByteLimit)
}

// requestsAdminScope reports whether the caller asked a consent leg for hub
// administration. Accepts the query and the form, because the same flag
// travels as a query parameter into the consent page and as a hidden field
// out of it.
func requestsAdminScope(r *http.Request) bool {
	switch r.FormValue("admin") {
	case "1", "true", "on":
		return true
	}
	return false
}

// resolveAdminScope decides the scope a consent grants. It refuses outright
// a request for administration on an account that is not an administrator,
// rather than downgrade it silently: the CLI would otherwise report a
// successful `--admin` login and then fail on the first admin verb, with
// nothing to point at.
func resolveAdminScope(w http.ResponseWriter, requested bool, user *auth.UserInfo) (bool, bool) {
	if !requested {
		return false, true
	}
	if !user.IsAdmin {
		http.Error(w, "your account is not a hub administrator, so it cannot grant the admin scope", http.StatusForbidden)
		return false, false
	}
	return true, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
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
	writeJSON(w, status, body)
}

type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
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

func generateUserCode() string {
	// Reuse verifycode.Generate which produces a 6-char alphanumeric
	// from an unambiguous alphabet — exactly the user-code shape we
	// want. verifycode.Format adds the display form (XXX-XXX) when we
	// build verification_uri_complete.
	return verifycode.Generate()
}

func isLoopbackURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// Through httpsec.LoopbackSchemes and httpsec.LoopbackHosts, which the
	// CSP's `form-action` derives from too: a scheme or a host this accepts
	// and the policy blocks is a CLI login that hangs.
	//
	// The scheme is part of this check, not a separate concern. The value
	// reaches a Location header, and a hostname test alone accepts
	// "javascript://127.0.0.1/%0aalert(1)": the host IS loopback, and the
	// scheme executes.
	//
	// Both halves go through httpsec, and the host half must: url.Parse
	// lower-cases the scheme but NOT the host, so a raw membership test
	// against the list refuses "http://LOCALHOST:5555/" although the CSP's
	// form-action matches a host-source case-insensitively and admits it --
	// the policy-admits-what-the-redirect-refuses asymmetry the package
	// exists to remove.
	return httpsec.IsLoopbackRedirectScheme(u.Scheme) && httpsec.IsLoopbackHost(u.Hostname())
}
