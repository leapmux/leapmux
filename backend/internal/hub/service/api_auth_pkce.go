package service

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

// --- Local-redirect (PKCE) flow ---

// handleStart serves a minimal consent page that the CLI's `auth login`
// instructs the user to open in a browser. The redirect_uri / state /
// code_challenge are echoed in a form the user submits to /auth/cli/authorize.
//
// In production this page should be templated; here we keep it inline to
// avoid adding template files for a one-screen flow.
func (h *APIAuthHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	challenge := q.Get("code_challenge")
	deviceName := q.Get("device_name")

	user := h.requireSession(r)
	if user == nil {
		// Not logged in — bounce to the SPA login screen with a return
		// param so the SPA can come back here after auth.
		next := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/login?next="+next, http.StatusFound)
		return
	}

	if redirectURI == "" || state == "" || challenge == "" {
		http.Error(w, "redirect_uri, state, code_challenge are required", http.StatusBadRequest)
		return
	}
	if !isLoopbackURL(redirectURI) {
		http.Error(w, "redirect_uri must be a loopback URL", http.StatusBadRequest)
		return
	}

	page := fmt.Sprintf(`<!doctype html><html><body style="font-family:sans-serif;max-width:480px;margin:48px auto;">
<h1>Authorize CLI access?</h1>
<p>The leapmux remote CLI on <strong>%s</strong> is requesting access to your account (<strong>%s</strong>).</p>
<form method="POST" action="/auth/cli/authorize">
  <input type="hidden" name="redirect_uri" value="%s"/>
  <input type="hidden" name="state" value="%s"/>
  <input type="hidden" name="code_challenge" value="%s"/>
  <input type="hidden" name="device_name" value="%s"/>
  <button type="submit" style="padding:10px 16px;font-size:14px;">Allow</button>
</form>
</body></html>`,
		html.EscapeString(deviceName),
		html.EscapeString(user.Username),
		html.EscapeString(redirectURI),
		html.EscapeString(state),
		html.EscapeString(challenge),
		html.EscapeString(deviceName),
	)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

// handleAuthorize accepts the consent POST and redirects to the CLI's
// loopback URL with a one-shot authorization code.
func (h *APIAuthHandler) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := h.requireSession(r)
	if user == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")
	challenge := r.FormValue("code_challenge")
	deviceName := r.FormValue("device_name")
	if !isLoopbackURL(redirectURI) || state == "" || challenge == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	code := id.Generate()
	if err := h.store.CLIAuthorizationCodes().Create(r.Context(), store.CreateCLIAuthorizationCodeParams{
		Code:          code,
		UserID:        user.ID,
		CodeChallenge: challenge,
		DeviceName:    deviceName,
		ExpiresAt:     time.Now().Add(CLIAuthCodeTTL),
	}); err != nil {
		http.Error(w, "failed to record authorization", http.StatusInternalServerError)
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
