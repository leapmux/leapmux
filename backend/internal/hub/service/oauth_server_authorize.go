package service

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

// --- RFC 6749 section 4.1: authorization code with PKCE ---

// authorizeRequest is one validated /oauth/authorize request: everything the
// consent page renders and everything the POST must carry forward unchanged.
type authorizeRequest struct {
	app *store.OAuthClient
	// redirectURI is what the CLIENT presented, not what the app registered.
	// RFC 6749 section 4.1.3 makes the token leg compare the presented value,
	// so the presented value is what the grant row must store.
	redirectURI string
	// registeredURI is the entry redirectURI matched. The consent page derives
	// its LABEL from this, never from the presented value, because the label
	// is the one thing a person reads to decide.
	registeredURI string
	state         string
	codeChallenge string
	scopes        authscope.ScopeSet
	installation  string
}

// parseAuthorizeRequest validates a request and reports what to answer when it
// is invalid.
//
// The ORDER is the security property, and it is what RFC 6749 section 4.1.2.1
// requires: the client and the redirect URI are settled FIRST, and a failure
// in either renders a page and REDIRECTS NOWHERE. Only after both are known
// good may an error travel back to the client's address -- otherwise an
// unregistered redirect_uri would be a functioning open redirect, aimed by
// whoever wrote the link.
//
// `redirectable` reports which of the two answers the caller must give.
//
// req is filled FIELD BY FIELD as each value is settled, so a redirectable
// failure carries the address to redirect to and the state to echo. Returning
// the zero req instead sent every such failure to an empty redirect_uri, which
// http.Redirect resolved against the current path: the app got no callback at
// all and the browser landed on the hub with a bare error string.
func (h *OAuthServerHandler) parseAuthorizeRequest(
	r *http.Request, user *auth.UserInfo, values url.Values,
) (req authorizeRequest, body oauthErrorResponse, redirectable bool, ok bool) {
	app, err := h.resolveApp(r.Context(), values.Get("client_id"), user)
	if err != nil {
		if errors.Is(err, errAppUnavailable) {
			return req, oauthErrorBody("invalid_client", "unknown or unavailable client_id"), false, false
		}
		return req, oauthErrorBody("server_error", ""), false, false
	}
	req.app = app

	presented := values.Get("redirect_uri")
	registered, matched := MatchRedirectURI(ParseRedirectURIs(app.RedirectURIs), presented)
	if !matched {
		return req, oauthErrorBody("invalid_request",
			"redirect_uri does not match an address this app registered"), false, false
	}
	req.redirectURI, req.registeredURI = presented, registered

	// The state is read BEFORE the checks that redirect, although it is
	// validated among them. RFC 6749 section 4.1.2.1 requires the error
	// response to echo it, and a client that cannot match the callback to its
	// own request must discard it -- so a refusal that dropped the state would
	// reach the app as an unattributable callback it is right to ignore.
	req.state = values.Get("state")

	// From here on a failure MAY redirect: both the client and its address are
	// registered, so the client is entitled to the answer.
	if !appAllowsGrantType(app, GrantTypeAuthorizationCode) {
		return req, oauthErrorBody("unauthorized_client",
			"this app is not registered for the authorization_code grant"), true, false
	}
	// OAuth 2.1 removed the implicit and password grants, so response_type has
	// exactly one legal value. Requiring it rather than defaulting keeps a
	// client that asks for `token` from being silently served a code.
	if rt := values.Get("response_type"); rt != "code" {
		return req, oauthErrorBody("unsupported_response_type",
			"response_type must be code"), true, false
	}
	// PKCE is REQUIRED, and S256 is the only method. OAuth 2.1 mandates PKCE
	// for every client, and `plain` is not a challenge at all.
	if method := values.Get("code_challenge_method"); method != "S256" {
		return req, oauthErrorBody("invalid_request",
			"code_challenge_method must be S256"), true, false
	}
	req.codeChallenge = values.Get("code_challenge")
	if req.codeChallenge == "" {
		return req, oauthErrorBody("invalid_request", "code_challenge is required"), true, false
	}
	if req.state == "" {
		// RFC 6749 calls state RECOMMENDED; OAuth 2.1 requires either it or
		// PKCE for CSRF protection, and PKCE is already required above. It is
		// still demanded here, because a client that omits it cannot tell its
		// own callback apart from one an attacker triggered.
		return req, oauthErrorBody("invalid_request", "state is required"), true, false
	}
	scopes, scopeErr := resolveRequestedScopes(values.Get("scope"), app, user)
	if scopeErr != nil {
		return req, *scopeErr, true, false
	}
	req.scopes = scopes
	req.installation = normalizeInstallationName(values.Get("installation_name"))
	return req, oauthErrorResponse{}, false, true
}

// handleAuthorize serves the consent page.
//
// A GET is replayable from its own URL, so the gate sends away a caller that
// is not signed in or not elevated, and that caller comes back to exactly this
// address. consentLeg runs that gate before this leg does.
//
// The page cannot run script: its enforced Content-Security-Policy has no hash
// and no nonce, which is why a passkey ceremony is impossible HERE and the
// elevation gate bounces to the SPA's /elevate route instead.
func (h *OAuthServerHandler) handleAuthorize(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	req, body, redirectable, ok := h.parseAuthorizeRequest(r, user, r.URL.Query())
	if !ok {
		h.writeAuthorizationFailure(w, r, req, body, redirectable)
		return
	}
	writePage(w, http.StatusOK, consentPageTmpl, consentPageData{
		App:              appDisplay(req.app),
		Username:         user.Username,
		RedirectLabel:    redirectLabel(req.registeredURI, req.redirectURI),
		Permissions:      describeScopes(req.scopes),
		RedirectURI:      req.redirectURI,
		ClientID:         req.app.ClientID,
		State:            req.state,
		CodeChallenge:    req.codeChallenge,
		Scope:            req.scopes.String(),
		InstallationName: req.installation,
	}, redirectFormActionSource(req.redirectURI))
}

// writeAuthorizationFailure answers a rejected authorization.
//
// It REDIRECTS only once the client and its address are both registered. Until
// then the answer is a page on the hub's own origin, because RFC 6749 section
// 4.1.2.1 forbids redirecting to an unregistered address -- and because that
// page is the last place attacker-chosen text could reach a browser, so it
// draws from hub-authored sentences alone.
func (h *OAuthServerHandler) writeAuthorizationFailure(
	w http.ResponseWriter, r *http.Request, req authorizeRequest, body oauthErrorResponse, redirectable bool,
) {
	if !redirectable {
		status := http.StatusBadRequest
		if body.Error == "server_error" {
			status = http.StatusInternalServerError
		}
		writePage(w, status, invalidRequestPageTmpl, invalidRequestPageData{
			Reason: invalidRequestSentence(body.Error),
		}, "")
		return
	}
	dest, err := url.Parse(req.redirectURI)
	if err != nil {
		writePage(w, http.StatusBadRequest, invalidRequestPageTmpl, invalidRequestPageData{
			Reason: invalidRequestSentence("invalid_request"),
		}, "")
		return
	}
	q := dest.Query()
	q.Set("error", body.Error)
	if body.ErrorDescription != "" {
		q.Set("error_description", body.ErrorDescription)
	}
	if req.state != "" {
		q.Set("state", req.state)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// handleConsent accepts the consent POST and redirects to the app's registered
// address with a one-shot authorization code.
//
// This leg REFUSES rather than bounces when the session is not elevated, and
// the gate derives that from the method: the PKCE challenge, the state, the
// scope and the redirect URI arrive in the form body, so a redirect would drop
// them and the user would return to a consent page that forgot what it
// consented to. Cross-site forgery is not the exposure here either way -- the
// session cookie is SameSite=Lax, so a cross-site POST carries no cookie.
//
// consentLeg runs the method check and the gate BEFORE this leg, and before
// ParseForm. The gate reads only the URL, the headers and the cookies, so
// parsing first made the hub read and buffer an anonymous caller's body -- up
// to Go's 10 MB form limit -- to decide it had no session.
//
// It RE-VALIDATES everything the page carried forward. The form is
// attacker-writable: nothing about having rendered a page makes the values
// that come back trustworthy, so this leg parses them exactly as the GET did.
func (h *OAuthServerHandler) handleConsent(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	req, body, redirectable, ok := h.parseAuthorizeRequest(r, user, r.PostForm)
	if !ok {
		h.writeAuthorizationFailure(w, r, req, body, redirectable)
		return
	}
	// DENY actually returns. The code flow redirects with access_denied, so
	// the app learns immediately instead of waiting out the code's TTL.
	if r.PostFormValue("decision") != consentDecisionAllow {
		h.writeAuthorizationFailure(w, r, req, oauthErrorBody("access_denied", "the account owner refused"), true)
		return
	}

	code := id.Generate()
	granted, err := req.scopes.Storable()
	if err != nil {
		writeInternalError(w, "consent produced an unstorable grant", err)
		return
	}
	// The grant binds HERE, on the row: the client, the address, the scope and
	// the account. /oauth/token reads every one of them back from the row. Read
	// from the exchange form instead, any holder of an authorization code could
	// change what the user consented to.
	if err := h.store.OAuthAuthorizationCodes().Create(r.Context(), store.CreateOAuthAuthorizationCodeParams{
		Code:             code,
		UserID:           user.ID,
		ClientID:         req.app.ClientID,
		CodeChallenge:    req.codeChallenge,
		RedirectURI:      req.redirectURI,
		GrantedScopes:    granted,
		InstallationName: req.installation,
		ExpiresAt:        h.now().Add(AuthorizationCodeTTL),
	}); err != nil {
		writeInternalError(w, "authorization code creation failed", err)
		return
	}

	dest, err := url.Parse(req.redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect", http.StatusBadRequest)
		return
	}
	q := dest.Query()
	q.Set("code", code)
	q.Set("state", req.state)
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// consentDecisionAllow is the form value that grants. Deny is EVERY other
// value, including an absent one, so a submission the hub cannot read is a
// refusal rather than a grant.
const consentDecisionAllow = "allow"

// invalidRequestSentence maps an OAuth error code to a sentence the hub wrote.
//
// A CLOSED set, and that is the point. This page renders on the hub's own
// origin for a request whose client or redirect URI could not be verified, so
// it is the last place attacker-chosen text could reach a browser. Echoing the
// error_description would put the caller's words on the hub's page.
func invalidRequestSentence(code string) string {
	switch code {
	case "invalid_client":
		return "That app is not registered on this hub, or it is not available to your account."
	case "server_error":
		return "The hub could not read this app's registration."
	default:
		return "The address this app asked the hub to return to is not one it registered."
	}
}

// redirectLabel describes WHERE a grant sends the browser back to, in words.
//
// It is never the URI. A full URI is itself a phishing surface -- a person
// scanning "https://leapmux.example.com.evil.io/callback" reads the first
// familiar-looking run of characters and stops -- so the page renders a label
// derived from the REGISTERED entry instead.
//
// A loopback address names the port the client actually presented, because
// that is the fact a person can check against the program in front of them.
// Anything else names the registered host alone, with no path and no query.
func redirectLabel(registeredURI, presentedURI string) string {
	u, err := url.Parse(registeredURI)
	if err != nil {
		return "an address this app registered"
	}
	host := u.Hostname()
	if host == "" {
		// A private-use scheme (com.example.app:/cb): the operating system
		// picks which program answers, and no host exists to state.
		return "an app installed on this device"
	}
	if isLoopbackLabelHost(host) {
		if p, perr := url.Parse(presentedURI); perr == nil && p.Port() != "" {
			return "a program on this computer (" + host + ":" + p.Port() + ")"
		}
		return "a program on this computer (" + host + ")"
	}
	return host
}

func isLoopbackLabelHost(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}
