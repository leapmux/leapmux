// Package loopbackauth runs the browser side of an OAuth 2.0 authorization
// code flow with PKCE against a loopback redirect (RFC 8252 section 7.3).
//
// It binds an ephemeral 127.0.0.1 port, hands out the authorize URL, and
// answers the Hub's redirect on /callback until one request carries the
// state it issued. The control CLI and the desktop sidecar both import it,
// so the ceremony -- including its timeout and its callback discipline --
// has one implementation. The token-surface wire helpers the two clients
// share -- the form-encoded POST and the RFC 6749 section 5.2 error body --
// live here for the same reason.
package loopbackauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"golang.org/x/oauth2"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/pkce"
	"github.com/leapmux/leapmux/locallisten"
)

// App is the identity one ceremony authenticates as: the registered client
// id, its registered loopback redirect, and the name the success page shows.
type App struct {
	ClientID    string
	RedirectURI string
	Name        string
}

// ControlCLI and Desktop are the apps this build ships with, filled from
// internal/hub/oauthapp -- one home for the Hub's seeded registrations and
// every client that drives them.
var (
	ControlCLI = App{
		ClientID:    oauthapp.ControlCLIClientID,
		RedirectURI: oauthapp.ControlCLIRedirectURI,
		Name:        oauthapp.ControlCLIName,
	}
	Desktop = App{
		ClientID:    oauthapp.DesktopClientID,
		RedirectURI: oauthapp.DesktopRedirectURI,
		Name:        oauthapp.DesktopName,
	}
)

// Request is everything one ceremony needs.
type Request struct {
	// HubBaseURL is the Hub address the browser visits and the token
	// request goes to.
	HubBaseURL string
	App        App
	// Scope is the RFC 6749 section 3.3 ask. An empty value asks for the
	// Hub's default grant. A step-up passes empty, because it grants
	// nothing.
	Scope string
	// InstallationName is the label the grant records on the account's
	// Connected-apps list.
	InstallationName string
	// StepUp, when non-empty, makes this ceremony a step-up of an existing
	// credential: the authorize URL carries it as `step_up`. One of
	// StepUpForToken and StepUpForSessionCookieHeader spells it.
	StepUp string
	// Clock drives the timeout. A nil Clock means the real clock.
	Clock quartz.Clock
}

// Timeout is how long a flow waits for the browser before it gives up.
const Timeout = 10 * time.Minute

// ErrTimeout reports a flow whose browser never answered within Timeout.
var ErrTimeout = errors.New("the browser did not answer in time")

// timeoutTag labels the timeout timer for the clock's traps.
const timeoutTag = "loopbackauth.Timeout"

// Flow is one ceremony, from Start to Close. Its zero value is not usable.
type Flow struct {
	app          App
	verifier     string
	state        string
	redirectURI  string
	authorizeURL string

	codeCh chan string
	errCh  chan error

	srv       *http.Server
	timer     *quartz.Timer
	closeOnce sync.Once
}

// Start binds the loopback listener, draws the PKCE verifier and the state,
// and serves /callback. Close releases both.
func Start(req Request) (*Flow, error) {
	clock := req.Clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	verifier := oauth2.GenerateVerifier()
	state := id.Generate()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	redirectURI := registeredRedirectWithPort(req.App.RedirectURI, ln.Addr().(*net.TCPAddr).Port)

	f := &Flow{
		app:         req.App,
		verifier:    verifier,
		state:       state,
		redirectURI: redirectURI,
		codeCh:      make(chan string, 1),
		errCh:       make(chan error, 1),
	}
	params := url.Values{
		"client_id":             {req.App.ClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {pkce.S256(verifier)},
		"installation_name":     {req.InstallationName},
		"scope":                 {req.Scope},
	}
	if req.StepUp != "" {
		params.Set("step_up", req.StepUp)
	}
	f.authorizeURL = locallisten.JoinPath(req.HubBaseURL, "/oauth/authorize?"+params.Encode())

	f.srv = &http.Server{Handler: f.callbackHandler()}
	go func() { _ = f.srv.Serve(ln) }()
	// The timeout runs from Start, not from Wait: a caller that prints the
	// URL and only then waits must still meet the same deadline the person
	// at the browser sees.
	f.timer = clock.NewTimer(Timeout, timeoutTag)
	return f, nil
}

// AuthorizeURL is the address the person at the browser opens.
func (f *Flow) AuthorizeURL() string { return f.authorizeURL }

// Wait returns the authorization code the callback delivered.
//
// It returns ErrTimeout when Timeout passed since Start, and ctx.Err() when
// the caller gives up first. A callback with the wrong state ends nothing:
// see callbackHandler.
func (f *Flow) Wait(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case code := <-f.codeCh:
		return code, nil
	case err := <-f.errCh:
		return "", err
	case <-f.timer.C:
		return "", ErrTimeout
	}
}

// TokenForm is the /oauth/token body that redeems the code this flow
// produced. The verifier and the redirect URI are the ones the authorize URL
// carried, so the Hub can bind the exchange to this ceremony alone.
func (f *Flow) TokenForm(code string) url.Values {
	return url.Values{
		"grant_type":    {oauthapp.GrantTypeAuthorizationCode},
		"client_id":     {f.app.ClientID},
		"code":          {code},
		"code_verifier": {f.verifier},
		"redirect_uri":  {f.redirectURI},
	}
}

// PostForm performs one form-encoded POST against the hub's auth surface.
//
// Every stage of that surface is this request: an
// application/x-www-form-urlencoded body, an optional credential header,
// and a response whose refusals carry an OAuth error body. decorate runs on
// the request header, so a caller that must present a credential states
// which one; a caller that presents none passes nothing.
//
// The CALLER closes the response body.
func PostForm(
	ctx context.Context,
	hc *http.Client,
	endpoint string,
	form url.Values,
	decorate ...func(http.Header),
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, d := range decorate {
		d(req.Header)
	}
	return hc.Do(req)
}

// OAuthErrorBody is the RFC 6749 section 5.2 error body that every refused
// stage of the hub's auth surface returns.
type OAuthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Message renders the body as one line: the error code, and the hub's
// description after it when it sent one.
func (b OAuthErrorBody) Message() string {
	if b.ErrorDescription == "" {
		return b.Error
	}
	return b.Error + ": " + b.ErrorDescription
}

// ReadOAuthError decodes the error body of a refused response.
//
// A body it cannot read reports the ZERO value rather than a decode error,
// because the caller's next step is the same either way: fall back to the
// HTTP status, which is all that a proxy's HTML page or a truncated answer
// leaves to report. Every caller must test Error against "" before it
// formats the fields -- an empty body formatted as "%s: %s" gives the user
// ": " and hides the transport failure that produced it.
func ReadOAuthError(resp *http.Response) OAuthErrorBody {
	var body OAuthErrorBody
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return OAuthErrorBody{}
	}
	return body
}

// Close releases the listener and the timeout timer. It is idempotent.
func (f *Flow) Close() {
	f.closeOnce.Do(func() {
		f.timer.Stop(timeoutTag)
		_ = f.srv.Shutdown(context.Background())
	})
}

// callbackHandler serves /callback. Its channel sends are NON-BLOCKING: the
// success page is a plain 200, and a user reload re-GETs this address with
// the same valid values. The channels hold one outcome each; a duplicate
// that arrives after the caller has its answer is ACKNOWLEDGED here and
// dropped, because a blocking send parks the handler and Close's Shutdown
// waits for it forever -- a ceremony that already succeeded would hang the
// caller.
func (f *Flow) callbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		gotState := q.Get("state")
		gotCode := q.Get("code")
		// RFC 6749 section 4.1.2.1: a redirect the Hub could validate
		// carries either a code or an error, plus the echoed state. The
		// state check runs first: a mismatched state is refused as a
		// mismatch whatever else the query carries -- and refused SILENTLY,
		// keeping the flow waiting. Any local program that finds the port
		// must not be able to end a login with one request.
		if gotState != f.state {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			return
		}
		// Reading the error parameter matters because the Hub redirects
		// EVERY refusal it could validate back here -- a Deny on the consent
		// page, an invalid_scope, a server error -- and folding those into
		// the state-mismatch branch told a user who deliberately refused
		// that the state check had failed, a CSRF suspicion they can neither
		// check nor act on.
		if gotErr := q.Get("error"); gotErr != "" {
			msg := gotErr
			if desc := q.Get("error_description"); desc != "" {
				msg = gotErr + ": " + desc
			}
			http.Error(w, "authorization refused", http.StatusBadRequest)
			select {
			case f.errCh <- fmt.Errorf("authorization failed: %s", msg):
			default:
			}
			return
		}
		if gotCode == "" {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			select {
			case f.errCh <- errors.New("callback carried neither a code nor an error"):
			default:
			}
			return
		}
		_, _ = fmt.Fprintln(w, "Authorization received. You can close this window and return to "+f.app.successName()+".")
		select {
		case f.codeCh <- gotCode:
		default:
		}
	})
}

// successName is what the success page names as the place to return to. The
// control CLI reads "the CLI": the page is a detour of the CLI's own login,
// and its consent-screen name reads like a third party there. Every other
// app is named by App.Name.
func (a App) successName() string {
	if a.Name == ControlCLI.Name {
		return "the CLI"
	}
	return a.Name
}

// registeredRedirectWithPort binds the ephemeral port onto the REGISTERED
// loopback redirect rather than a local literal: the Hub matches the callback
// against the registration (scheme, host and path exact; only the port free
// per RFC 8252 section 7.3), so the path here is whatever the constant says,
// and a future edit to the registration cannot silently break every login.
func registeredRedirectWithPort(registered string, port int) string {
	u, err := url.Parse(registered)
	if err != nil {
		// Unreachable for a registered constant's shape; the fallback keeps
		// the registered host and path rather than inventing a third spelling.
		return fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	}
	u.Host = fmt.Sprintf("127.0.0.1:%d", port)
	return u.String()
}

// StepUpForToken spells the step_up value that names an app credential.
func StepUpForToken(tokenID string) string {
	return oauthapp.StepUpTokenPrefix + tokenID
}

// StepUpForSessionCookieHeader spells the step_up value that names the
// session a Cookie header carries. The reader accepts both spellings of the
// cookie name -- __Host-leapmux-session first and leapmux-session second --
// because the sidecar cannot know the Hub's secure_cookies setting, and the
// Hub validates the cookie in any case.
func StepUpForSessionCookieHeader(cookieHeader string) (string, error) {
	token := auth.SessionTokenFromCookieHeader(cookieHeader, false)
	if token == "" {
		return "", errors.New("the header carries no session cookie")
	}
	sessionID, _, ok := auth.ParseSessionToken(token)
	if !ok {
		return "", errors.New("the session cookie does not hold a session token")
	}
	return oauthapp.StepUpSessionPrefix + sessionID, nil
}
