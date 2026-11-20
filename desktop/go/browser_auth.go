package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/google/uuid"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/loopbackauth"
	"github.com/leapmux/leapmux/machinelabel"
)

// browserAuthTokenTimeout limits the /oauth/token request of one ceremony.
// proxy.client carries no timeout of its own (it must serve streams), so the
// ceremony ends the request on this clock instead.
const browserAuthTokenTimeout = 30 * time.Second

// browserAuthTokenTimerTag labels the token-request timer for the clock's
// traps.
const browserAuthTokenTimerTag = "browserAuth.TokenTimeout"

const browserAuthTokenTimeoutFailure = "the Hub did not answer in time"

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
	// Guarded by App.lifecycleMu. Once redemption starts, a matching cancel
	// records intent and leaves the exchange running for confirmation.
	redeeming       bool
	cancelRequested bool
}

// StartBrowserAuth begins a browser ceremony for the connected remote Hub and
// returns the id its result event will carry and the address the system
// browser opens. A ceremony already in progress ends.
//
// The webview never sees the code, the verifier or the cookie: the sidecar
// redeems the code itself and keeps the session in the proxy's jar.
func (a *App) StartBrowserAuth(kind desktoppb.BrowserAuthKind) (string, string, error) {
	return a.startBrowserAuth(kind, loopbackauth.Start)
}

func (a *App) startBrowserAuth(kind desktoppb.BrowserAuthKind, start func(loopbackauth.Request) (*loopbackauth.Flow, error)) (string, string, error) {
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

	flow, err := start(loopbackauth.Request{
		HubBaseURL:       proxy.baseURL,
		App:              loopbackauth.Desktop,
		InstallationName: machinelabel.InstallationName(),
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
		c.cancel()
		c.flow.Close()
		return "", "", err
	}
	go a.runBrowserAuth(c)
	return c.id, flow.AuthorizeURL(), nil
}

// installBrowserAuth replaces the current ceremony in one locked step. It
// refuses when the connection changed while the loopback listener bound.
func (a *App) installBrowserAuth(c *browserAuthCeremony) error {
	unlock, err := a.acquireLifecycleLock()
	if err != nil {
		return err
	}
	if a.connection != c.connection {
		unlock()
		return errors.New("connection changed during browser ceremony setup")
	}
	previous := a.detachBrowserAuthLocked()
	a.browserAuth = c
	unlock()
	previous.closeListener()
	return nil
}

// detachBrowserAuthLocked clears the slot and cancels the ceremony's lifetime.
// The caller holds lifecycleMu and closes the returned ceremony's listener
// after releasing the lock.
func (a *App) detachBrowserAuthLocked() *browserAuthCeremony {
	c := a.browserAuth
	if c != nil {
		a.browserAuth = nil
		c.cancel()
	}
	return c
}

func (c *browserAuthCeremony) closeListener() {
	if c != nil {
		c.flow.Close()
	}
}

// CancelBrowserAuth cancels a wait for the matching ceremony ID. Once the
// token exchange has started, the Hub's confirmation can still settle it.
func (a *App) CancelBrowserAuth(ceremonyID string) {
	unlock, err := a.acquireLifecycleLock()
	if err != nil {
		return
	}
	c := a.browserAuth
	if c == nil || c.id != ceremonyID {
		unlock()
		return
	}
	if c.redeeming {
		c.cancelRequested = true
	} else {
		a.detachBrowserAuthLocked()
	}
	unlock()
	c.closeListener()
}

// beginBrowserAuthRedemption and CancelBrowserAuth use the same lock to
// decide whether a matching cancel can prevent the token exchange.
func (a *App) beginBrowserAuthRedemption(c *browserAuthCeremony) error {
	unlock, err := a.acquireLifecycleLock()
	if err != nil {
		return err
	}
	defer unlock()
	if a.browserAuth != c || a.connection != c.connection || c.ctx.Err() != nil {
		return context.Canceled
	}
	c.redeeming = true
	return nil
}

// runBrowserAuth waits out the ceremony and emits its one result event.
// Every ending -- a code the Hub accepted, a refusal, a timeout, a cancel, a
// newer start, a disconnect or a mode switch -- reaches the webview as this
// single event, so the Frontend settles on it and on nothing else.
func (a *App) runBrowserAuth(c *browserAuthCeremony) {
	defer c.cancel()
	defer c.flow.Close()
	ok, failure := a.completeBrowserAuth(c)
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

// completeBrowserAuth admits the outcome and releases its ceremony slot under
// the lifecycle lock. The per-Hub file lock keeps a reconnect behind the
// accepted cookie write without holding lifecycleMu across disk I/O.
func (a *App) completeBrowserAuth(c *browserAuthCeremony) (bool, string) {
	outcome := a.awaitBrowserAuth(c)
	if outcome.cookies != nil {
		release, err := a.sessionFiles.lock(c.ctx, sessionFileName(outcome.cookies.live.base))
		if err != nil {
			return false, ""
		}
		defer release()
	}
	unlock, err := a.acquireLifecycleLock()
	if err != nil {
		return false, ""
	}
	if a.browserAuth != c {
		unlock()
		return false, ""
	}
	a.browserAuth = nil
	ownsConnection := a.connection == c.connection && c.ctx.Err() == nil
	cancelRequested := c.cancelRequested
	unlock()
	if !ownsConnection {
		return false, ""
	}
	if outcome.cookies != nil {
		outcome.cookies.commit()
		return true, ""
	}
	if cancelRequested {
		return false, ""
	}
	return false, outcome.failure
}

// awaitBrowserAuth waits for the callback and exchanges its code. Neither
// blocking step holds lifecycleMu.
func (a *App) awaitBrowserAuth(c *browserAuthCeremony) browserAuthOutcome {
	code, err := c.flow.Wait(c.ctx)
	if err != nil {
		return browserAuthOutcome{failure: browserAuthCallbackFailure(err)}
	}
	return a.redeemBrowserAuthCode(c, code)
}

// browserAuthCallbackFailure is the failure text of a callback that ended
// without a code: the OAuth message the redirect carried, in the one format
// the result event uses.
func browserAuthCallbackFailure(err error) string {
	return strings.TrimPrefix(err.Error(), loopbackAuthCallbackErrorPrefix)
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

// redeemBrowserAuthCode exchanges the code on the ceremony's lifetime and
// publishes one outcome. The clock used to schedule the timer also decides
// whether an answer arrived in time.
func (a *App) redeemBrowserAuthCode(c *browserAuthCeremony, code string) browserAuthOutcome {
	redemptionMu := &c.connection.browserAuthRedemptionMu
	if err := redemptionMu.Lock(c.ctx); err != nil {
		return browserAuthOutcome{failure: err.Error()}
	}
	if err := a.beginBrowserAuthRedemption(c); err != nil {
		redemptionMu.Unlock()
		return browserAuthOutcome{failure: err.Error()}
	}

	requestCtx, cancelRequest := context.WithCancel(c.ctx)
	defer cancelRequest()
	clock := a.browserClock()
	deadline := clock.Now().Add(browserAuthTokenTimeout)
	timer := clock.NewTimer(clock.Until(deadline), browserAuthTokenTimerTag)
	defer timer.Stop(browserAuthTokenTimerTag)

	published := make(chan struct{})
	var outcome browserAuthOutcome
	var publishOnce sync.Once
	publish := func(result browserAuthOutcome) {
		publishOnce.Do(func() {
			if !clock.Now().Before(deadline) {
				result = browserAuthOutcome{failure: browserAuthTokenTimeoutFailure}
			}
			outcome = result
			close(published)
		})
	}
	go func() {
		// A replaced or timed-out request can still be leaving the HTTP
		// client. Keep the next exchange behind it until it exits.
		defer redemptionMu.Unlock()
		publish(postBrowserAuthToken(requestCtx, c, code))
	}()
	select {
	case <-published:
	case <-timer.C:
		publish(browserAuthOutcome{failure: browserAuthTokenTimeoutFailure})
	case <-c.ctx.Done():
		return browserAuthOutcome{}
	}
	return outcome
}

// browserAuthOutcome carries the staged cookie changes after the Hub confirmed
// the exchange. A nil cookies field means the exchange did not confirm.
type browserAuthOutcome struct {
	cookies *browserAuthCookieJar
	failure string
}

// browserAuthCookieJar sends a private snapshot with the exchange. Redirects
// see earlier response cookies in that snapshot, and the live proxy receives
// them only after the ceremony admits the confirmation.
type browserAuthCookieJar struct {
	live    *persistentJar
	view    http.CookieJar
	pending []*http.Cookie
}

func (j *browserAuthCookieJar) Cookies(u *url.URL) []*http.Cookie {
	return j.view.Cookies(u)
}

func (j *browserAuthCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	now := time.Now()
	staged := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		c := *cookie
		c.Path = sessionCookiePath(u, c.Path)
		if c.MaxAge > 0 {
			c.Expires = now.Add(time.Duration(c.MaxAge) * time.Second)
			c.MaxAge = 0
		}
		staged = append(staged, &c)
	}
	j.view.SetCookies(u, staged)
	j.pending = append(j.pending, staged...)
}

func (j *browserAuthCookieJar) commit() {
	if len(j.pending) != 0 {
		// The redirect pin kept every response on the same origin. The
		// cookies already carry their effective paths and absolute expiry.
		j.live.SetCookies(j.live.base, j.pending)
	}
}

// postBrowserAuthToken uses a copy of the proxy's HTTP client to keep its
// transport and redirect pin while staging the cookie updates.
func postBrowserAuthToken(ctx context.Context, c *browserAuthCeremony, code string) browserAuthOutcome {
	client := *c.proxy.client
	live, ok := client.Jar.(*persistentJar)
	if !ok {
		return browserAuthOutcome{failure: "the Hub has no session cookie store"}
	}
	jar := &browserAuthCookieJar{live: live, view: live.snapshot()}
	client.Jar = jar
	resp, err := loopbackauth.PostForm(ctx, &client,
		locallisten.JoinPath(c.proxy.baseURL, "/oauth/token"),
		c.flow.TokenForm(code))
	if err != nil {
		return browserAuthOutcome{failure: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body := loopbackauth.ReadOAuthError(resp)
		if body.Error != "" {
			return browserAuthOutcome{failure: body.Message()}
		}
		return browserAuthOutcome{failure: fmt.Sprintf("the Hub answered %d", resp.StatusCode)}
	}
	var out struct {
		SignedIn bool `json:"signed_in"`
		Elevated bool `json:"elevated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return browserAuthOutcome{failure: "the Hub's answer could not be read"}
	}
	confirmed := out.SignedIn
	if c.kind == desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP {
		confirmed = out.Elevated
	}
	if !confirmed {
		return browserAuthOutcome{failure: browserAuthUnconfirmedMessage(c.kind)}
	}
	return browserAuthOutcome{cookies: jar}
}

// browserClock is the clock the ceremony schedules on. A bare-struct App
// (focused tests) carries none, and a nil clock means the real one.
func (a *App) browserClock() quartz.Clock {
	if a.browserAuthClock == nil {
		return quartz.NewReal()
	}
	return a.browserAuthClock
}
