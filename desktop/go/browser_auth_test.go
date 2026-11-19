package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/loopbackauth"
	"github.com/leapmux/leapmux/util/atomicfile"
)

// The session cookie names and the token shape come from
// backend/internal/hub/auth, which this module cannot import (internal). A
// session token is two GeneratedLength (48) alphanumeric halves around "_",
// and the two cookie spellings are the ones StepUpForSessionCookieHeader
// accepts. A change to either breaks these tests loudly.
const (
	testSecureSessionCookieName = "__Host-leapmux-session"
	testSessionCookieName       = "leapmux-session"
)

// testSessionToken is a well-formed session token for a jar to hold.
func testSessionToken() string {
	return strings.Repeat("i", 48) + "_" + strings.Repeat("k", 48)
}

// testSessionID is the identity half of testSessionToken: what a step-up
// names as session:<id>.
func testSessionID() string {
	return strings.Repeat("i", 48)
}

// browserAuthTestContext bounds every wait these tests make. A passing test
// never reaches the limit; it exists so a stuck wait fails with its call
// instead of hanging the package.
func browserAuthTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newBrowserAuthClock builds the mock clock the timeout tests drive. The
// logger is off: quartz logs every event through tb.Log and buries the
// failure of a test that advances a clock.
func newBrowserAuthClock(t *testing.T) *quartz.Mock {
	t.Helper()
	return quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
}

// advanceBrowserClock moves the mock clock by d and waits for the timers it
// fired to complete.
func advanceBrowserClock(t *testing.T, clock *quartz.Mock, d time.Duration) {
	t.Helper()
	clock.Advance(d).MustWait(browserAuthTestContext(t))
}

// browserAuthHub starts a fake Hub: the connect probe answers 200, and
// /oauth/token runs onToken. Cleanup closes the server.
func browserAuthHub(t *testing.T, onToken http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			onToken(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

// recordBrowserAuthEvents installs an event sink that collects every browser
// result event the app emits.
func recordBrowserAuthEvents(app *App) <-chan *desktoppb.BrowserAuthResultEvent {
	events := make(chan *desktoppb.BrowserAuthResultEvent, 8)
	app.SetEventSink(func(event *desktoppb.Event) {
		if result := event.GetBrowserAuthResult(); result != nil {
			events <- result
		}
	})
	return events
}

// newBrowserAuthApp builds an app with a distributed connection to hubURL on
// proxy, and collects its result events.
func newBrowserAuthApp(t *testing.T, proxy *HubProxy, hubURL string) (*App, <-chan *desktoppb.BrowserAuthResultEvent) {
	t.Helper()
	setTestHome(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	installTestConnection(app, proxy, nil, hubURL)
	return app, recordBrowserAuthEvents(app)
}

// requireBrowserAuthEvent waits for the next result event.
func requireBrowserAuthEvent(t *testing.T, events <-chan *desktoppb.BrowserAuthResultEvent) *desktoppb.BrowserAuthResultEvent {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-browserAuthTestContext(t).Done():
		t.Fatal("timed out waiting for a browser result event")
		return nil
	}
}

// authorizeURLValues is the query of the address the system browser opens.
func authorizeURLValues(t *testing.T, authorizeURL string) url.Values {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	require.NoError(t, err)
	return u.Query()
}

// browserCallbackQuery is the callback request that carries this ceremony's
// state and the given code.
func browserCallbackQuery(t *testing.T, authorizeURL, code string) url.Values {
	t.Helper()
	values := authorizeURLValues(t, authorizeURL)
	state := values.Get("state")
	require.NotEmpty(t, state, "the authorize URL carries no state")
	return url.Values{"state": {state}, "code": {code}}
}

// browserCallback sends one request to the ceremony's loopback listener and
// returns the response or the transport error.
func browserCallback(t *testing.T, authorizeURL string, query url.Values) (*http.Response, error) {
	t.Helper()
	redirectURI := authorizeURLValues(t, authorizeURL).Get("redirect_uri")
	require.NotEmpty(t, redirectURI, "the authorize URL carries no redirect_uri")
	req, err := http.NewRequestWithContext(browserAuthTestContext(t), http.MethodGet,
		redirectURI+"?"+query.Encode(), nil)
	require.NoError(t, err)
	return http.DefaultClient.Do(req)
}

// completeBrowserCallback delivers a code to the ceremony's listener, as the
// Hub's redirect would.
func completeBrowserCallback(t *testing.T, authorizeURL, code string) {
	t.Helper()
	resp, err := browserCallback(t, authorizeURL, browserCallbackQuery(t, authorizeURL, code))
	require.NoError(t, err, "the loopback callback")
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// seedSessionCookie stores a session cookie in the proxy's jar, as a Hub
// response would, and returns the token it holds.
func seedSessionCookie(proxy *HubProxy, name string) string {
	token := testSessionToken()
	proxy.client.Jar.SetCookies(proxy.base, []*http.Cookie{{Name: name, Value: token, Path: "/"}})
	return token
}

func TestBrowserAuthSignInStoresTheSession(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: "issued-session", Path: "/"})
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	assert.Contains(t, authorizeURL, "/oauth/authorize?")
	completeBrowserCallback(t, authorizeURL, "the-code")

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId, "the one event carries the returned id")
	assert.True(t, evt.Ok)
	assert.Empty(t, evt.Failure)
	assert.Contains(t, proxy.cookieHeader().Get("Cookie"), testSessionCookieName+"=issued-session",
		"the jar holds the cookie")
}

func TestBrowserAuthStepUpCarriesTheSessionIdentity(t *testing.T) {
	var tokenCookie string
	hub := browserAuthHub(t, func(w http.ResponseWriter, r *http.Request) {
		tokenCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{"elevated": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)
	token := seedSessionCookie(proxy, testSecureSessionCookieName)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP)
	require.NoError(t, err)
	assert.Equal(t, "session:"+testSessionID(), authorizeURLValues(t, authorizeURL).Get("step_up"),
		"the authorize URL carries step_up=session:<id> of the cookie")

	completeBrowserCallback(t, authorizeURL, "the-code")
	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.True(t, evt.Ok)
	assert.Empty(t, evt.Failure)
	assert.Contains(t, tokenCookie, testSecureSessionCookieName+"="+token,
		"the token request carries the cookie")
}

func TestBrowserAuthStepUpAfterRestart(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"elevated": true}`))
	})
	dir := privateDir(t)
	base := mustParse(t, hub.URL)
	token := testSessionToken()
	data, err := json.Marshal(sessionFile{
		HubURL:  base.String(),
		Cookies: []persistedCookie{{Name: testSecureSessionCookieName, Value: token, Path: "/"}},
	})
	require.NoError(t, err)
	require.NoError(t, atomicfile.WriteFile(filepath.Join(dir, sessionFileName(base)), data, 0o600))

	endpoint, err := hubtransport.New(hub.URL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	proxy := newProxyForEndpoint(endpoint, openPersistentJar(base, dir))
	app, _ := newBrowserAuthApp(t, proxy, hub.URL)

	_, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP)
	require.NoError(t, err)
	assert.Equal(t, "session:"+testSessionID(), authorizeURLValues(t, authorizeURL).Get("step_up"),
		"a jar loaded from the file steps up the same session")
}

func TestBrowserAuthStepUpWithoutSecureCookies(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"elevated": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, _ := newBrowserAuthApp(t, proxy, hub.URL)
	seedSessionCookie(proxy, testSessionCookieName)

	_, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP)
	require.NoError(t, err)
	assert.Equal(t, "session:"+testSessionID(), authorizeURLValues(t, authorizeURL).Get("step_up"),
		"a Hub without secure cookies names the same session")
}

func TestBrowserAuthHubRefusesTheCode(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"code expired or already consumed"}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Equal(t, "invalid_grant: code expired or already consumed", evt.Failure)
}

func TestBrowserAuthHubAnswersWithoutAnOAuthErrorBody(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Equal(t, "the Hub answered 500", evt.Failure)
}

func TestBrowserAuthHubUnreachableForTheTokenRequest(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)
	hub.Close()

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.NotEmpty(t, evt.Failure, "a request that failed before an answer carries its error")
}

func TestBrowserAuthSecondStartEndsTheFirst(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id1, authorizeURL1, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	id2, authorizeURL2, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	require.NotEqual(t, id1, id2)

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id1, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Empty(t, evt.Failure, "a newer start ends the ceremony with no reason")

	// The first listener is released; the second one still answers.
	resp, err := browserCallback(t, authorizeURL1, browserCallbackQuery(t, authorizeURL1, "the-code"))
	require.Error(t, err, "the first ceremony's listener is closed")
	if resp != nil {
		_ = resp.Body.Close()
	}
	resp2, err := browserCallback(t, authorizeURL2, url.Values{"state": {"wrong"}, "code": {"x"}})
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode, "one listener remains")
}

func TestBrowserAuthCancelOnlyEndsTheMatchingCeremony(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	// A cancel for another id does nothing: the ceremony still completes.
	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	app.CancelBrowserAuth("some-other-ceremony")
	completeBrowserCallback(t, authorizeURL, "the-code")
	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.True(t, evt.Ok, "a cancel for another id ends nothing")

	// A cancel for the id ends it.
	id2, _, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	app.CancelBrowserAuth(id2)
	evt2 := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id2, evt2.CeremonyId)
	assert.False(t, evt2.Ok)
	assert.Empty(t, evt2.Failure)
}

func TestBrowserAuthStepUpWithoutASessionFails(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"elevated": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, _ := newBrowserAuthApp(t, proxy, hub.URL)

	_, _, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP)
	require.ErrorContains(t, err, "sign in first")
	assert.Nil(t, app.browserAuth, "no listener binds")
}

func TestBrowserAuthNotConnectedFails(t *testing.T) {
	setTestHome(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	_ = recordBrowserAuthEvents(app)

	_, _, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.ErrorContains(t, err, "not connected")
}

func TestBrowserAuthSoloModeFails(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	setTestHome(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	installTestConnection(app, mustNewProxy(t, hub.URL), &recordingSoloInstance{}, hub.URL)
	_ = recordBrowserAuthEvents(app)

	_, _, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.ErrorContains(t, err, "distributed connection")
}

func TestBrowserAuthModeSwitchDuringTheTokenRequest(t *testing.T) {
	arrived := make(chan struct{})
	var arrivedOnce sync.Once
	release := make(chan struct{})
	hub := browserAuthHub(t, func(w http.ResponseWriter, r *http.Request) {
		arrivedOnce.Do(func() { close(arrived) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: "leaked-session", Path: "/"})
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	app, events := newBrowserAuthApp(t, mustNewProxy(t, hub.URL), hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")
	select {
	case <-arrived:
	case <-browserAuthTestContext(t).Done():
		t.Fatal("the token request never arrived at the Hub")
	}

	_, err = app.SwitchMode()
	require.NoError(t, err)
	newProxy := mustNewProxy(t, hub.URL)
	installTestConnection(app, newProxy, nil, hub.URL)
	close(release)

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Empty(t, evt.Failure, "a mode switch ends the ceremony with no reason")
	assert.Empty(t, newProxy.cookieHeader(), "the new connection's jar is untouched")
}

func TestBrowserAuthDisconnectDuringTheWait(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	require.NoError(t, app.disconnectAndStopSolo())

	resp, err := browserCallback(t, authorizeURL, browserCallbackQuery(t, authorizeURL, "the-code"))
	require.Error(t, err, "the listener is closed")
	if resp != nil {
		_ = resp.Body.Close()
	}
	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Empty(t, evt.Failure)
}

func TestBrowserAuthBrowserTimeout(t *testing.T) {
	clock := newBrowserAuthClock(t)
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	app, events := newBrowserAuthApp(t, mustNewProxy(t, hub.URL), hub.URL)
	app.browserAuthClock = clock

	id, _, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	advanceBrowserClock(t, clock, loopbackauth.Timeout)

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Equal(t, "the browser did not answer in time", evt.Failure)
}

func TestBrowserAuthTokenRequestTimeout(t *testing.T) {
	clock := newBrowserAuthClock(t)
	arrived := make(chan struct{})
	var arrivedOnce sync.Once
	hub := browserAuthHub(t, func(w http.ResponseWriter, r *http.Request) {
		arrivedOnce.Do(func() { close(arrived) })
		<-r.Context().Done()
	})
	app, events := newBrowserAuthApp(t, mustNewProxy(t, hub.URL), hub.URL)
	app.browserAuthClock = clock

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")
	select {
	case <-arrived:
	case <-browserAuthTestContext(t).Done():
		t.Fatal("the token request never arrived at the Hub")
	}
	// The request arrived, so its deadline timer is armed: end it on the
	// ceremony's clock.
	advanceBrowserClock(t, clock, browserAuthTokenTimeout)

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Equal(t, "the Hub did not answer in time", evt.Failure)
}
