package service

import (
	"net/http"
	"net/url"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

// --- Local-redirect (PKCE) flow ---

// handleStart serves a minimal consent page that the CLI's `auth login`
// instructs the user to open in a browser. The page echoes the redirect_uri,
// the state and the code_challenge in a form the user submits to
// /auth/cli/authorize.
//
// In production this page should use a template; here we keep it inline to
// avoid adding template files for a one-screen flow.
//
// The page cannot run script: the enforced Content-Security-Policy has no
// hash or nonce for it, which is why a passkey ceremony is impossible HERE
// and the elevation gate bounces to the SPA's /elevate route instead.
//
// A GET is replayable from its own URL, so the gate sends away a caller that
// is not signed in or not elevated, and that caller comes back to exactly
// this address. consentLeg runs that gate before this leg does, and
// gateModeFor reads the method to decide between the bounce and the refusal.
func (h *APIAuthHandler) handleStart(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	challenge := q.Get("code_challenge")
	deviceName := normalizeDeviceName(q.Get("device_name"))

	if redirectURI == "" || state == "" || challenge == "" {
		http.Error(w, "redirect_uri, state, code_challenge are required", http.StatusBadRequest)
		return
	}
	if !isLoopbackURL(redirectURI) {
		http.Error(w, "redirect_uri must be a loopback URL", http.StatusBadRequest)
		return
	}
	// The handler resolves the scope AFTER the request shape, so a malformed
	// consent URL answers about the malformed part rather than about the scope.
	adminScope, ok := resolveAdminScope(w, requestsAdminScope(r), user)
	if !ok {
		return
	}

	writePage(w, http.StatusOK, consentPageTmpl, consentPageData{
		DeviceName:    deviceName,
		Username:      user.Username,
		RedirectURI:   redirectURI,
		State:         state,
		CodeChallenge: challenge,
		AdminScope:    adminScope,
	})
}

// handleAuthorize accepts the consent POST and redirects to the CLI's
// loopback URL with a one-shot authorization code.
//
// The session alone does NOT admit this: what it mints outlives the session
// by months, so requireElevatedSession demands a recently proven factor.
// This leg REFUSES rather than bounces, and the gate derives that from the
// method: the PKCE challenge, the state and the redirect URI arrive in the
// form body, so a redirect would drop them and the user would return to a
// consent page that forgot what it consented to. Cross-site forgery is not
// the exposure here either way -- the session cookie is SameSite=Lax, so a
// cross-site POST carries no cookie.
//
// consentLeg runs the method check and the gate BEFORE this leg, and before
// ParseForm. The gate reads only the URL, the headers and the cookies, so
// parsing first made the hub read and buffer an anonymous caller's body --
// up to Go's 10 MB form limit -- to decide it had no session. Every
// FormValue below still runs after the parse.
func (h *APIAuthHandler) handleAuthorize(w http.ResponseWriter, r *http.Request, user *auth.UserInfo) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")
	challenge := r.FormValue("code_challenge")
	deviceName := normalizeDeviceName(r.FormValue("device_name"))
	if !isLoopbackURL(redirectURI) || state == "" || challenge == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	adminScope, ok := resolveAdminScope(w, requestsAdminScope(r), user)
	if !ok {
		return
	}

	code := id.Generate()
	// The scope binds HERE, on the grant row. /auth/cli/token reads it back
	// from the row; if it read the exchange form instead, any holder of an
	// authorization code could upgrade the grant to hub administration.
	if err := h.store.CLIAuthorizationCodes().Create(r.Context(), store.CreateCLIAuthorizationCodeParams{
		Code:          code,
		UserID:        user.ID,
		CodeChallenge: challenge,
		DeviceName:    deviceName,
		AdminScope:    adminScope,
		ExpiresAt:     h.now().Add(CLIAuthCodeTTL),
	}); err != nil {
		writeInternalError(w, "authorization code creation failed", err)
		return
	}

	dest, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect", http.StatusBadRequest)
		return
	}
	q := dest.Query()
	q.Set("code", code)
	q.Set("state", state)
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}
