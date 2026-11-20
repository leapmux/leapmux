// Package loopbackauth runs the browser side of an OAuth 2.0 authorization
// code flow with PKCE against a loopback redirect (RFC 8252 section 7.3).
//
// It binds an ephemeral 127.0.0.1 port, hands out the authorize URL, and
// answers the Hub's registered callback until one request carries the
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
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
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

// ErrClosed reports a flow that closed before the browser answered.
var ErrClosed = errors.New("the browser authorization closed")

// timeoutTag labels the timeout timer for the clock's traps.
const timeoutTag = "loopbackauth.Timeout"

// closeGracePeriod gives an active callback time to finish its browser reply.
const closeGracePeriod = time.Second

type callbackResult struct {
	code string
	err  error
}

// Flow is one ceremony, from Start to Close. Its zero value is not usable.
type Flow struct {
	app          App
	verifier     string
	state        string
	redirectURI  string
	callbackPath string
	authorizeURL string

	clock    quartz.Clock
	deadline time.Time
	// callbackMu orders publication by the callback, timeout, and listener.
	callbackMu sync.Mutex
	result     callbackResult
	ready      chan struct{}

	srv       *http.Server
	timer     *quartz.Timer
	closeOnce sync.Once
}

// Start serves the registered callback on an ephemeral loopback port and
// prepares the OAuth authorization URL. Close releases the listener.
func Start(req Request) (*Flow, error) {
	redirect, err := url.Parse(req.App.RedirectURI)
	if err != nil {
		return nil, fmt.Errorf("parse the registered callback: %w", err)
	}
	if redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" ||
		redirect.User != nil || strings.Contains(req.App.RedirectURI, "#") {
		return nil, errors.New("the registered callback must use HTTP on 127.0.0.1 without credentials or a fragment")
	}
	callbackPath := redirect.EscapedPath()
	if callbackPath == "" {
		callbackPath = "/"
	}
	clock := req.Clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	verifier := oauth2.GenerateVerifier()
	state := id.Generate()

	ln, err := net.Listen("tcp", net.JoinHostPort(redirect.Hostname(), "0"))
	if err != nil {
		return nil, err
	}
	redirect.Host = net.JoinHostPort(redirect.Hostname(), strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	redirectURI := redirect.String()

	f := &Flow{
		app:          req.App,
		verifier:     verifier,
		state:        state,
		redirectURI:  redirectURI,
		callbackPath: callbackPath,
		clock:        clock,
		ready:        make(chan struct{}),
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

	// The callback and the waiter share the deadline established at Start.
	f.deadline = clock.Now().Add(Timeout)
	f.timer = clock.AfterFunc(clock.Until(f.deadline), func() {
		f.finish(callbackResult{err: ErrTimeout})
	}, timeoutTag)
	f.srv = &http.Server{Handler: f.callbackHandler()}
	go func() {
		if err := f.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			f.finish(callbackResult{err: fmt.Errorf("serve the browser callback: %w", err)})
		}
	}()
	return f, nil
}

// AuthorizeURL is the address the person at the browser opens.
func (f *Flow) AuthorizeURL() string { return f.authorizeURL }

// Wait returns the authorization code the callback delivered.
//
// It returns ErrTimeout when no matching callback arrives within Timeout.
// It returns ErrClosed when the flow closes before a result is available.
// It returns ctx.Err() when the caller cancels the wait.
func (f *Flow) Wait(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-f.ready:
		return f.result.code, f.result.err
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

const maxOAuthErrorBodyBytes = 64 * 1024

// Message renders the body as one line: the error code, and the hub's
// description after it when it sent one.
func (b OAuthErrorBody) Message() string {
	if b.ErrorDescription == "" {
		return b.Error
	}
	return b.Error + ": " + b.ErrorDescription
}

// CallbackError carries the OAuth refusal sent to the loopback listener.
type CallbackError struct {
	OAuthErrorBody
}

func (e *CallbackError) Error() string {
	return "authorization failed: " + e.Message()
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
	if json.NewDecoder(io.LimitReader(resp.Body, maxOAuthErrorBodyBytes)).Decode(&body) != nil {
		return OAuthErrorBody{}
	}
	return body
}

// Close releases the listener and the timeout timer. It gives active HTTP
// requests time to finish, then closes their connections. It is idempotent.
func (f *Flow) Close() {
	f.closeOnce.Do(func() {
		f.finish(callbackResult{err: ErrClosed})
		f.timer.Stop(timeoutTag)
		ctx, cancel := context.WithTimeout(context.Background(), closeGracePeriod)
		defer cancel()
		if err := f.srv.Shutdown(ctx); err != nil {
			_ = f.srv.Close()
		}
	})
}

// recordCallback publishes the first validated callback received before the
// deadline. A retry can still receive a browser reply without replacing it.
func (f *Flow) recordCallback(result callbackResult) error {
	f.callbackMu.Lock()
	defer f.callbackMu.Unlock()
	if f.result.err == ErrClosed {
		return ErrClosed
	}
	if !f.clock.Now().Before(f.deadline) {
		return ErrTimeout
	}
	f.publishResultLocked(result)
	return nil
}

func (f *Flow) finish(result callbackResult) {
	f.callbackMu.Lock()
	defer f.callbackMu.Unlock()
	f.publishResultLocked(result)
}

// publishResultLocked wakes all waiters and keeps the first result.
func (f *Flow) publishResultLocked(result callbackResult) {
	select {
	case <-f.ready:
	default:
		f.result = result
		close(f.ready)
	}
}

// callbackHandler serves the registered path and replies after recording
// the outcome, so writing the reply cannot affect the deadline check.
func (f *Flow) callbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != f.callbackPath {
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
		result := callbackResult{code: gotCode}
		var refusal string
		if gotErr := q.Get("error"); gotErr != "" {
			result = callbackResult{err: &CallbackError{OAuthErrorBody{
				Error:            gotErr,
				ErrorDescription: q.Get("error_description"),
			}}}
			refusal = "authorization refused"
		} else if gotCode == "" {
			result.err = errors.New("callback carried neither a code nor an error")
			refusal = "invalid callback"
		}
		if err := f.recordCallback(result); err != nil {
			http.Error(w, err.Error(), http.StatusGone)
			return
		}
		if refusal != "" {
			http.Error(w, refusal, http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintln(w, "Authorization received. You can close this window and return to "+f.app.successName()+".")
	})
}

// successName is the destination the success page tells the person to return to.
// The control CLI uses “the CLI” instead of its full consent screen label.
func (a App) successName() string {
	if a.Name == ControlCLI.Name {
		return "the CLI"
	}
	return a.Name
}

// StepUpForToken builds the step_up value that identifies an app credential.
func StepUpForToken(tokenID string) string {
	return oauthapp.StepUpTokenPrefix + tokenID
}

// StepUpForSessionCookieHeader builds the step_up value that identifies the
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
