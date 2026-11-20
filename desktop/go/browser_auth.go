package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/quartz"
	"github.com/google/uuid"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/loopbackauth"
)

// browserAuthTokenTimeout limits the /oauth/token request of one ceremony.
// proxy.client carries no timeout of its own (it must serve streams), so the
// ceremony ends the request on this clock instead.
const browserAuthTokenTimeout = 30 * time.Second

// browserAuthTokenTimerTag labels the token-request timer for the clock's
// traps.
const browserAuthTokenTimerTag = "browserAuth.TokenTimeout"

// loopbackauth reports a callback that carried an OAuth error as
// "authorization failed: <error>: <description>". The result event carries
// the OAuth message itself -- the same shape a refused token request takes --
// so the strip is what keeps the one failure format the contract states.
const loopbackAuthCallbackErrorPrefix = "authorization failed: "

// browserAuthCeremony is one browser ceremony, from its start to its result
// event. Its lifetime is a child of the connection that started it.
type browserAuthCeremony struct {
	id   string
	kind desktoppb.BrowserAuthKind
	flow *loopbackauth.Flow
	// proxy is the connection's proxy, snapshotted at the start: the token
	// request must ride the jar and the redirect pin of the connection that
	// began the ceremony, never one installed later.
	proxy      *HubProxy
	connection *desktopConnection
	ctx        context.Context
	cancel     context.CancelFunc
}

// StartBrowserAuth begins a browser ceremony for the connected remote Hub and
// returns the id its result event will carry and the address the system
// browser opens. A ceremony already in progress ends.
//
// The webview never sees the code, the verifier or the cookie: the sidecar
// redeems the code itself and keeps the session in the proxy's jar.
func (a *App) StartBrowserAuth(kind desktoppb.BrowserAuthKind) (string, string, error) {
	done, err := a.beginOperation()
	if err != nil {
		return "", "", err
	}
	defer done()
	if kind != desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN &&
		kind != desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP {
		return "", "", fmt.Errorf("unknown browser ceremony kind %d", kind)
	}
	unlock, err := a.acquireLifecycleRLock()
	if err != nil {
		return "", "", err
	}
	connection := a.connection
	unlock()
	if connection == nil {
		return "", "", errors.New("not connected")
	}
	if connection.solo != nil {
		return "", "", errors.New("browser ceremonies require a distributed connection")
	}
	proxy := connection.proxy

	var stepUp string
	if kind == desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP {
		stepUp, err = loopbackauth.StepUpForSessionCookieHeader(proxy.cookieHeader().Get("Cookie"))
		if err != nil {
			slog.Warn("browser ceremony: no session to step up", "error", err)
			return "", "", errors.New("sign in first")
		}
	}

	flow, err := loopbackauth.Start(loopbackauth.Request{
		HubBaseURL:       proxy.baseURL,
		App:              loopbackauth.Desktop,
		InstallationName: loopbackauth.InstallationName(),
		StepUp:           stepUp,
		Clock:            a.browserAuthClock,
	})
	if err != nil {
		return "", "", err
	}
	ceremonyCtx, ceremonyCancel := context.WithCancel(connection.ctx)
	c := &browserAuthCeremony{
		id:         uuid.NewString(),
		kind:       kind,
		flow:       flow,
		proxy:      proxy,
		connection: connection,
		ctx:        ceremonyCtx,
		cancel:     ceremonyCancel,
	}
	if err := a.installBrowserAuth(c); err != nil {
		flow.Close()
		ceremonyCancel()
		return "", "", err
	}
	go a.runBrowserAuth(c)
	return c.id, flow.AuthorizeURL(), nil
}

// installBrowserAuth claims the ceremony slot: it ends the ceremony in
// progress and adopts c, or refuses when the connection changed while the
// loopback listener bound (the post-unlocked-work guard of
// reacquireConnectionForInstall). The claim is one locked step, so a failed
// start can never leave two ceremonies or none.
func (a *App) installBrowserAuth(c *browserAuthCeremony) error {
	unlock, err := a.acquireLifecycleLock()
	if err != nil {
		return err
	}
	defer unlock()
	if a.connection != c.connection {
		return errors.New("connection changed during browser ceremony setup")
	}
	a.endBrowserAuthLocked()
	a.browserAuth = c
	return nil
}

// endBrowserAuthLocked ends the ceremony in progress: it drops the claim,
// ends the ceremony's lifetime and releases the loopback listener. The
// goroutine that runs the ceremony then delivers the result event. Caller
// holds lifecycleMu for writing.
func (a *App) endBrowserAuthLocked() {
	c := a.browserAuth
	if c == nil {
		return
	}
	a.browserAuth = nil
	c.cancel()
	c.flow.Close()
}

// CancelBrowserAuth ends the ceremony in progress when ceremonyID names it.
// Any other id -- including one whose ceremony already ended -- does nothing.
func (a *App) CancelBrowserAuth(ceremonyID string) {
	unlock, err := a.acquireLifecycleLock()
	if err != nil {
		return
	}
	defer unlock()
	if a.browserAuth != nil && a.browserAuth.id == ceremonyID {
		a.endBrowserAuthLocked()
	}
}

// runBrowserAuth waits out the ceremony and emits its one result event.
// Every ending -- a code the Hub accepted, a refusal, a timeout, a cancel, a
// newer start, a disconnect or a mode switch -- reaches the webview as this
// single event, so the Frontend settles on it and on nothing else.
func (a *App) runBrowserAuth(c *browserAuthCeremony) {
	// Close is idempotent: an ending call already released the listener, and a
	// ceremony that ran to its result releases it here.
	defer c.flow.Close()
	ok, failure := a.browserAuthResult(c)
	a.clearBrowserAuth(c)
	a.EmitEvent(&desktoppb.Event{
		Payload: &desktoppb.Event_BrowserAuthResult{
			BrowserAuthResult: &desktoppb.BrowserAuthResultEvent{
				CeremonyId: c.id,
				Ok:         ok,
				Failure:    failure,
			},
		},
	})
}

// clearBrowserAuth drops the ceremony from the slot it still holds, if any. A
// cancel for a ceremony that already ended then does nothing, as the contract
// for CancelBrowserAuth requires.
func (a *App) clearBrowserAuth(c *browserAuthCeremony) {
	unlock, err := a.acquireLifecycleLock()
	if err != nil {
		return
	}
	defer unlock()
	if a.browserAuth == c {
		a.browserAuth = nil
	}
}

// browserAuthResult is the one result of one ceremony. See the outcome table
// of the plan: an empty failure means the ceremony was cancelled, and only a
// confirmation the Hub gave carries ok true.
func (a *App) browserAuthResult(c *browserAuthCeremony) (bool, string) {
	confirmed, failure := a.awaitBrowserAuth(c)
	switch {
	case a.browserAuthConnectionLost(c):
		// The token request can return after the user switched mode. The
		// connection that started the ceremony is gone, so the ceremony was
		// ended and its answer settles nothing.
		return false, ""
	case confirmed:
		// The Hub accepted the code. That answer wins over an end that raced
		// it: a cancel that arrives after the acceptance settles with ok
		// true, so the Frontend never reports a failure while the jar holds
		// the session the acceptance stored.
		return true, ""
	case c.ctx.Err() != nil:
		// A cancel, a newer start, a disconnect or a mode switch ended it.
		return false, ""
	default:
		return false, failure
	}
}

// awaitBrowserAuth runs the two blocking steps of the ceremony and reports
// the confirmation, or the text of whatever ended it. The ended/cancelled
// cases carry a text too; browserAuthResult discards it in favour of the
// empty cancellation reason.
func (a *App) awaitBrowserAuth(c *browserAuthCeremony) (confirmed bool, failure string) {
	code, err := c.flow.Wait(c.ctx)
	if err != nil {
		if errors.Is(err, loopbackauth.ErrTimeout) {
			return false, err.Error()
		}
		return false, browserAuthCallbackFailure(err)
	}
	return a.redeemBrowserAuthCode(c, code)
}

// browserAuthCallbackFailure is the failure text of a callback that ended
// without a code: the OAuth message the redirect carried, in the one format
// the result event uses.
func browserAuthCallbackFailure(err error) string {
	return strings.TrimPrefix(err.Error(), loopbackAuthCallbackErrorPrefix)
}

// browserAuthOAuthMessage renders an OAuth error body the way the result
// event carries it: the code, and the Hub's description after it when it sent
// one.
func browserAuthOAuthMessage(code, description string) string {
	if description == "" {
		return code
	}
	return code + ": " + description
}

// browserAuthUnconfirmedMessage names the confirmation the Hub did not give.
// The wording is kind-aware: "signed in" for a sign-in ceremony, "elevated"
// for a step-up.
func browserAuthUnconfirmedMessage(kind desktoppb.BrowserAuthKind) string {
	if kind == desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP {
		return "the Hub did not confirm that your session is elevated"
	}
	return "the Hub did not confirm that you are signed in"
}

// redeemBrowserAuthCode exchanges the code and reports the confirmation, or
// why there is none. The request runs on the ceremony's own lifetime and ends
// at browserAuthTokenTimeout on browserAuthClock.
func (a *App) redeemBrowserAuthCode(c *browserAuthCeremony, code string) (bool, string) {
	requestCtx, cancelRequest := context.WithCancel(c.ctx)
	defer cancelRequest()
	clock := a.browserClock()
	timer := clock.NewTimer(browserAuthTokenTimeout, browserAuthTokenTimerTag)
	defer timer.Stop(browserAuthTokenTimerTag)

	settled := make(chan tokenSettlement, 1)
	go func() {
		ok, failure, err := a.postBrowserAuthToken(requestCtx, c, code)
		settled <- tokenSettlement{ok: ok, failure: failure, err: err}
	}()
	select {
	case s := <-settled:
		if s.err != nil {
			return false, s.err.Error()
		}
		return s.ok, s.failure
	case <-timer.C:
		cancelRequest()
		return false, "the Hub did not answer in time"
	}
}

// tokenSettlement is one answer out of the token request: a confirmation, a
// refusal with its text, or a transport error and no answer at all.
type tokenSettlement struct {
	ok      bool
	failure string
	err     error
}

// postBrowserAuthToken performs the /oauth/token exchange on the proxy's
// client, so the jar stores the session the Hub answers with and the redirect
// pin applies. It requires the confirmation the contract states for the
// ceremony's kind.
func (a *App) postBrowserAuthToken(ctx context.Context, c *browserAuthCeremony, code string) (bool, string, error) {
	form := c.flow.TokenForm(code)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		locallisten.JoinPath(c.proxy.baseURL, "/oauth/token"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.proxy.client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if json.NewDecoder(resp.Body).Decode(&body) == nil && body.Error != "" {
			return false, browserAuthOAuthMessage(body.Error, body.ErrorDescription), nil
		}
		return false, fmt.Sprintf("the Hub answered %d", resp.StatusCode), nil
	}
	var out struct {
		SignedIn bool `json:"signed_in"`
		Elevated bool `json:"elevated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "the Hub's answer could not be read", nil
	}
	confirmed := out.SignedIn
	if c.kind == desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP {
		confirmed = out.Elevated
	}
	if !confirmed {
		return false, browserAuthUnconfirmedMessage(c.kind), nil
	}
	return true, "", nil
}

// browserAuthConnectionLost reports whether the connection that started the
// ceremony is no longer the app's -- a mode switch or a disconnect swapped it
// out while the ceremony ran.
func (a *App) browserAuthConnectionLost(c *browserAuthCeremony) bool {
	unlock, err := a.acquireLifecycleRLock()
	if err != nil {
		return true
	}
	defer unlock()
	return a.connection != c.connection
}

// browserClock is the clock the ceremony schedules on. A bare-struct App
// (focused tests) carries none, and a nil clock means the real one.
func (a *App) browserClock() quartz.Clock {
	if a.browserAuthClock == nil {
		return quartz.NewReal()
	}
	return a.browserAuthClock
}
