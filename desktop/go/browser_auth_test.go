package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	return testSessionTokenFor('i', 'k')
}

func testSessionTokenFor(identity, secret byte) string {
	return strings.Repeat(string(identity), 48) + "_" + strings.Repeat(string(secret), 48)
}

// testSessionID is the identity half of testSessionToken: what a step-up
// identifies as session:<id>.
func testSessionID() string {
	return strings.Repeat("i", 48)
}

// browserAuthTestContext limits every wait these tests make. A passing test
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
	issued := testSessionTokenFor('a', 'b')
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: issued, Path: "/"})
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	assert.Contains(t, authorizeURL, "/oauth/authorize?")
	app.lifecycleMu.RLock()
	ceremony := app.browserAuth
	app.lifecycleMu.RUnlock()
	require.NotNil(t, ceremony)
	completeBrowserCallback(t, authorizeURL, "the-code")
	select {
	case <-ceremony.ctx.Done():
	case <-browserAuthTestContext(t).Done():
		t.Fatal("the ceremony did not finish")
	}

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.Empty(t, events, "the completed ceremony emitted exactly one result")
	assert.True(t, evt.Ok)
	assert.Empty(t, evt.Failure)
	assert.Contains(t, proxy.cookieHeader().Get("Cookie"), testSessionCookieName+"="+issued,
		"the jar holds the cookie")
}

func TestBrowserAuthSignInRequiresANewSessionCookie(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")
	event := requireBrowserAuthEvent(t, events)

	assert.Equal(t, id, event.CeremonyId)
	assert.False(t, event.Ok)
	assert.Equal(t, "the Hub did not provide a new session", event.Failure)
	assert.Empty(t, proxy.cookieHeader())
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

func TestBrowserAuthHubAnswerDoesNotDecode(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Equal(t, "the Hub's answer could not be read", evt.Failure)
}

func TestBrowserAuthHubDoesNotConfirmSignIn(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"signed_in": false}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Equal(t, "the Hub did not confirm that you are signed in", evt.Failure)
}

func TestBrowserAuthHubDoesNotConfirmStepUp(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"elevated": false}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)
	seedSessionCookie(proxy, testSecureSessionCookieName)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Equal(t, "the Hub did not confirm that your session is elevated", evt.Failure)
}

// The three failure classes of the token exchange must carry three distinct
// messages. Each describes its own failure:
//   - a non-200 with no readable error body
//   - a 200 whose body does not decode
//   - a 200 without the confirmation
func TestBrowserAuthTokenFailureClassesAreDistinct(t *testing.T) {
	responders := []http.HandlerFunc{
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		},
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		},
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"signed_in": false}`))
		},
	}
	failures := make([]string, 0, len(responders))
	for _, respond := range responders {
		hub := browserAuthHub(t, func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Path: "/", MaxAge: -1})
			respond(w, r)
		})
		proxy := mustNewProxy(t, hub.URL)
		app, events := newBrowserAuthApp(t, proxy, hub.URL)
		previousToken := seedSessionCookie(proxy, testSessionCookieName)
		_, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
		require.NoError(t, err)
		completeBrowserCallback(t, authorizeURL, "the-code")
		evt := requireBrowserAuthEvent(t, events)
		require.False(t, evt.Ok)
		require.NotEmpty(t, evt.Failure, "each failure class carries a message")
		assert.Equal(t, testSessionCookieName+"="+previousToken, proxy.cookieHeader().Get("Cookie"),
			"an unconfirmed reply must not clear the existing session")
		failures = append(failures, evt.Failure)
	}
	for i := range failures {
		for j := i + 1; j < len(failures); j++ {
			assert.NotEqual(t, failures[i], failures[j],
				"each failure class carries its own message")
		}
	}
}

func TestBrowserAuthTokenRedirectStaysOnTheHub(t *testing.T) {
	var reachedOtherHub atomic.Bool
	otherHub := browserAuthHub(t, func(http.ResponseWriter, *http.Request) {
		reachedOtherHub.Store(true)
	})
	hub := browserAuthHub(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: "unconfirmed-session", Path: "/"})
		http.Redirect(w, r, otherHub.URL+"/oauth/token", http.StatusTemporaryRedirect)
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)
	previousToken := seedSessionCookie(proxy, testSessionCookieName)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Contains(t, evt.Failure, "leaves the origin")
	assert.False(t, reachedOtherHub.Load())
	assert.Equal(t, testSessionCookieName+"="+previousToken, proxy.cookieHeader().Get("Cookie"))
}

func TestBrowserAuthTokenRedirectReadsPendingCookieChanges(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			rotated := testSessionTokenFor('r', 's')
			nextCookie := make(chan string, 1)
			server := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/token":
					http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: rotated, Path: "/"})
					http.SetCookie(w, &http.Cookie{Name: "removed", Path: "/", MaxAge: -1})
					http.Redirect(w, r, "/oauth/confirmation", status)
				case "/oauth/confirmation":
					nextCookie <- r.Header.Get("Cookie")
					http.SetCookie(w, &http.Cookie{Name: "confirmed", Value: "yes", Path: "/"})
					_, _ = io.WriteString(w, `{"signed_in": true}`)
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			t.Cleanup(server.Close)
			setTestHome(t)
			app := NewApp("")
			t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
			require.NoError(t, app.ConnectDistributed(t.Context(), server.URL))
			proxy := app.connection.proxy
			seedSessionCookie(proxy, testSessionCookieName)
			proxy.client.Jar.SetCookies(mustParseURL(server.URL), []*http.Cookie{
				{Name: "removed", Value: "old", Path: "/"},
			})
			events := recordBrowserAuthEvents(app)

			id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
			require.NoError(t, err)
			completeBrowserCallback(t, authorizeURL, "the-code")
			evt := requireBrowserAuthEvent(t, events)
			require.Equal(t, id, evt.GetCeremonyId())
			require.True(t, evt.GetOk(), evt.GetFailure())
			select {
			case cookies := <-nextCookie:
				assert.Equal(t, testSessionCookieName+"="+rotated, cookies)
			default:
				t.Fatal("the exchange did not reach the confirmation endpoint")
			}
			assert.Contains(t, proxy.cookieHeader().Get("Cookie"), "confirmed=yes")
		})
	}
}

type pausedSessionCookieJar struct {
	http.CookieJar
	applying chan struct{}
	resume   <-chan struct{}
}

func (j *pausedSessionCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	close(j.applying)
	<-j.resume
	j.CookieJar.SetCookies(u, cookies)
}

func TestBrowserAuthCommitsCookiesOutsideTheLifecycleLock(t *testing.T) {
	confirmed := testSessionTokenFor('c', 'd')
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: confirmed, Path: "/"})
		_, _ = io.WriteString(w, `{"signed_in": true}`)
	})
	endpoint, err := hubtransport.New(hub.URL)
	require.NoError(t, err)
	jar := openPersistentJar(mustParseURL(hub.URL), filepath.Join(t.TempDir(), "sessions"))
	applying, resume := make(chan struct{}), make(chan struct{})
	jar.inner = &pausedSessionCookieJar{CookieJar: jar.inner, applying: applying, resume: resume}
	app, events := newBrowserAuthApp(t, newProxyForEndpoint(endpoint, jar), hub.URL)
	release := sync.OnceFunc(func() { close(resume) })
	defer release()

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")
	select {
	case <-applying:
	case <-browserAuthTestContext(t).Done():
		t.Fatal("the accepted session did not start applying cookies")
	}
	if assert.True(t, app.lifecycleMu.TryRLock(), "cookie I/O must run outside lifecycleMu") {
		app.lifecycleMu.RUnlock()
	}
	release()
	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.GetCeremonyId())
	assert.True(t, evt.GetOk())
	assert.FileExists(t, jar.path)
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
	issued := testSessionTokenFor('a', 'b')
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: issued, Path: "/"})
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

func TestBrowserAuthCancelReleasesTheLifecycleLockBeforeClosingTheListener(t *testing.T) {
	clock := newBrowserAuthClock(t)
	hub := browserAuthHub(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a cancelled ceremony must not redeem a code")
	})
	app, events := newBrowserAuthApp(t, mustNewProxy(t, hub.URL), hub.URL)
	app.browserAuthClock = clock

	id, _, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	stops := clock.Trap().TimerStop()
	defer stops.Close()
	cancelled := make(chan struct{})
	go func() {
		defer close(cancelled)
		app.CancelBrowserAuth(id)
	}()

	stopping := stops.MustWait(browserAuthTestContext(t))
	if assert.True(t, app.lifecycleMu.TryRLock(), "listener cleanup must run outside lifecycleMu") {
		app.lifecycleMu.RUnlock()
	}
	stopping.MustRelease(browserAuthTestContext(t))
	select {
	case <-cancelled:
	case <-browserAuthTestContext(t).Done():
		t.Fatal("CancelBrowserAuth did not finish")
	}
	event := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, event.CeremonyId)
	assert.False(t, event.Ok)
	assert.Empty(t, event.Failure)
}

type browserAuthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f browserAuthRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type browserAuthBlockedBody struct {
	io.ReadCloser
	ctx     context.Context
	reading chan<- context.Context
	release <-chan struct{}
	once    sync.Once
}

func (b *browserAuthBlockedBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		b.reading <- b.ctx
		<-b.release
	})
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}

// blockNextBrowserAuthTokenBody pauses the first token response after the
// HTTP client has read its headers. Release allows decoding to proceed.
func blockNextBrowserAuthTokenBody(t *testing.T, proxy *HubProxy) (<-chan context.Context, func()) {
	t.Helper()
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBody := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBody)

	reading := make(chan context.Context, 1)
	transport := proxy.client.Transport
	var tokenStarted atomic.Bool
	proxy.client.Transport = browserAuthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		first := r.URL.Path == "/oauth/token" && tokenStarted.CompareAndSwap(false, true)
		resp, err := transport.RoundTrip(r)
		if err == nil && first {
			resp.Body = &browserAuthBlockedBody{ReadCloser: resp.Body, ctx: r.Context(), reading: reading, release: release}
		}
		return resp, err
	})
	return reading, releaseBody
}

func TestBrowserAuthCancelDuringRedemptionKeepsAcceptedConfirmation(t *testing.T) {
	issued := testSessionTokenFor('a', 'b')
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: issued, Path: "/"})
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)
	reading, releaseBody := blockNextBrowserAuthTokenBody(t, proxy)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")

	var requestCtx context.Context
	select {
	case requestCtx = <-reading:
	case <-browserAuthTestContext(t).Done():
		t.Fatal("the token request did not read the response body")
	}
	assert.Empty(t, proxy.cookieHeader(), "the cookie must wait for confirmation")
	app.CancelBrowserAuth(id)
	assert.NoError(t, requestCtx.Err(), "cancel must leave an already started token request running")
	releaseBody()

	event := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, event.CeremonyId)
	assert.True(t, event.Ok, "the Hub accepted the code")
	assert.Empty(t, event.Failure)
	assert.Contains(t, proxy.cookieHeader().Get("Cookie"), testSessionCookieName+"="+issued)
}

func TestBrowserAuthReplacementSerializesRedemptionOnTheSameJar(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		token := testSessionTokenFor(r.Form.Get("code")[0], 'z')
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: token, Path: "/"})
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)
	reading, releaseBody := blockNextBrowserAuthTokenBody(t, proxy)

	firstID, firstURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, firstURL, "first")
	var requestCtx context.Context
	select {
	case requestCtx = <-reading:
	case <-browserAuthTestContext(t).Done():
		t.Fatal("the first token request did not read the response body")
	}
	secondID, secondURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, secondURL, "second")
	assert.ErrorIs(t, requestCtx.Err(), context.Canceled, "replacement cancels the predecessor's request")
	mu := &app.connection.browserAuthRedemptionMu
	if mu.TryLock() {
		mu.Unlock()
		t.Error("the predecessor must retain ownership until its token request exits")
	}
	releaseBody()

	results := make(map[string]*desktoppb.BrowserAuthResultEvent, 2)
	for range 2 {
		event := requireBrowserAuthEvent(t, events)
		results[event.CeremonyId] = event
	}
	require.Contains(t, results, firstID)
	require.Contains(t, results, secondID)
	assert.False(t, results[firstID].Ok)
	assert.Empty(t, results[firstID].Failure)
	assert.True(t, results[secondID].Ok)
	assert.Empty(t, results[secondID].Failure)
	assert.Contains(t, proxy.cookieHeader().Get("Cookie"), testSessionCookieName+"="+testSessionTokenFor('s', 'z'))
}

func TestBrowserAuthReplacementDiscardsDecodedConfirmation(t *testing.T) {
	clock := newBrowserAuthClock(t)
	superseded := testSessionTokenFor('s', 'u')
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: superseded, Path: "/"})
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)
	app.browserAuthClock = clock
	previousToken := seedSessionCookie(proxy, testSessionCookieName)

	// The timer stops after the exchange has delivered the decoded result and
	// before the ceremony admits it under lifecycleMu.
	stops := clock.Trap().TimerStop(browserAuthTokenTimerTag)
	closeTrap := sync.OnceFunc(stops.Close)
	defer closeTrap()
	firstID, firstURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, firstURL, "first")
	stopping := stops.MustWait(browserAuthTestContext(t))

	secondID, secondURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	require.NotEqual(t, firstID, secondID)
	stopping.MustRelease(browserAuthTestContext(t))

	first := requireBrowserAuthEvent(t, events)
	assert.Equal(t, firstID, first.CeremonyId)
	assert.False(t, first.Ok)
	assert.Empty(t, first.Failure)
	assert.Equal(t, testSessionCookieName+"="+previousToken, proxy.cookieHeader().Get("Cookie"))

	// The replacement keeps the slot and can confirm its own exchange.
	closeTrap()
	completeBrowserCallback(t, secondURL, "second")
	second := requireBrowserAuthEvent(t, events)
	assert.Equal(t, secondID, second.CeremonyId)
	assert.True(t, second.Ok)
	assert.Empty(t, second.Failure)
}

func TestBrowserAuthStepUpWithoutASessionFails(t *testing.T) {
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"elevated": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, _ := newBrowserAuthApp(t, proxy, hub.URL)

	starts := 0
	_, _, err := app.startBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_STEP_UP,
		func(req loopbackauth.Request) (*loopbackauth.Flow, error) {
			starts++
			return loopbackauth.Start(req)
		})
	require.ErrorContains(t, err, "sign in first")
	assert.Zero(t, starts, "a step-up without a session must not start the listener")
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
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: "unconfirmed-session", Path: "/"})
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	setTestHome(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	events := recordBrowserAuthEvents(app)
	ctx := browserAuthTestContext(t)
	require.NoError(t, app.ConnectDistributed(ctx, hub.URL))
	firstProxy := app.connection.proxy
	previousToken := seedSessionCookie(firstProxy, testSessionCookieName)
	reading, releaseBody := blockNextBrowserAuthTokenBody(t, firstProxy)

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")
	select {
	case <-reading:
	case <-ctx.Done():
		t.Fatal("the token request did not read the response body")
	}

	_, err = app.SwitchMode()
	require.NoError(t, err)
	require.NoError(t, app.ConnectDistributed(ctx, hub.URL))
	reconnectedProxy := app.connection.proxy
	require.NotSame(t, firstProxy, reconnectedProxy)
	assert.Equal(t, testSessionCookieName+"="+previousToken, reconnectedProxy.cookieHeader().Get("Cookie"),
		"reconnecting to the same Hub must load only the previously confirmed session")
	releaseBody()

	evt := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, evt.CeremonyId)
	assert.False(t, evt.Ok)
	assert.Empty(t, evt.Failure, "a mode switch ends the ceremony with no reason")
	reopened := openPersistentJar(firstProxy.base, sessionsDir())
	cookies := reopened.Cookies(firstProxy.base)
	require.Len(t, cookies, 1)
	assert.Equal(t, previousToken, cookies[0].Value)
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

// delayedTokenTimerClock lets the clock reach the deadline before the token
// timer wakes its waiter, as can happen when the timer goroutine runs late.
type delayedTokenTimerClock struct {
	*quartz.Mock
}

func (c delayedTokenTimerClock) NewTimer(d time.Duration, tags ...string) *quartz.Timer {
	if slices.Contains(tags, browserAuthTokenTimerTag) {
		d += time.Second
	}
	return c.Mock.NewTimer(d, tags...)
}

func TestBrowserAuthTokenAnswerAtDeadlineLosesBeforeTimerDelivery(t *testing.T) {
	for _, delay := range []time.Duration{0, time.Nanosecond} {
		t.Run(delay.String(), func(t *testing.T) {
			clock := newBrowserAuthClock(t)
			late := testSessionTokenFor('l', 't')
			hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: late, Path: "/"})
				_, _ = w.Write([]byte(`{"signed_in": true}`))
			})
			proxy := mustNewProxy(t, hub.URL)
			app, events := newBrowserAuthApp(t, proxy, hub.URL)
			app.browserAuthClock = delayedTokenTimerClock{clock}
			reading, releaseBody := blockNextBrowserAuthTokenBody(t, proxy)

			id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
			require.NoError(t, err)
			completeBrowserCallback(t, authorizeURL, "the-code")
			select {
			case <-reading:
			case <-browserAuthTestContext(t).Done():
				t.Fatal("the token request did not read the response body")
			}
			advanceBrowserClock(t, clock, browserAuthTokenTimeout+delay)
			releaseBody()

			event := requireBrowserAuthEvent(t, events)
			assert.Equal(t, id, event.CeremonyId)
			assert.False(t, event.Ok)
			assert.Equal(t, "the Hub did not answer in time", event.Failure)
			assert.Empty(t, proxy.cookieHeader())
		})
	}
}

func TestBrowserAuthTokenAnswerBeforeDeadlineSurvivesDelayedSettlement(t *testing.T) {
	clock := newBrowserAuthClock(t)
	accepted := testSessionTokenFor('a', 'c')
	hub := browserAuthHub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: accepted, Path: "/"})
		_, _ = w.Write([]byte(`{"signed_in": true}`))
	})
	proxy := mustNewProxy(t, hub.URL)
	app, events := newBrowserAuthApp(t, proxy, hub.URL)
	app.browserAuthClock = clock
	reading, releaseBody := blockNextBrowserAuthTokenBody(t, proxy)
	stops := clock.Trap().TimerStop(browserAuthTokenTimerTag)
	defer stops.Close()

	id, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "the-code")
	select {
	case <-reading:
	case <-browserAuthTestContext(t).Done():
		t.Fatal("the token request did not read the response body")
	}
	advanceBrowserClock(t, clock, browserAuthTokenTimeout-time.Nanosecond)
	releaseBody()

	stopping := stops.MustWait(browserAuthTestContext(t))
	advanceBrowserClock(t, clock, time.Nanosecond)
	stopping.MustRelease(browserAuthTestContext(t))

	event := requireBrowserAuthEvent(t, events)
	assert.Equal(t, id, event.CeremonyId)
	assert.True(t, event.Ok)
	assert.Empty(t, event.Failure)
	assert.Equal(t, testSessionCookieName+"="+accepted, proxy.cookieHeader().Get("Cookie"))
}

func TestBrowserAuthDoesNotAdmitAnOldProxyResponseAfterSignIn(t *testing.T) {
	oldToken := testSessionToken()
	newToken := strings.Repeat("n", 48) + "_" + strings.Repeat("r", 48)
	reachedHub := make(chan string, 1)
	releaseResponse := make(chan struct{})
	hub := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/delayed":
			reachedHub <- r.Header.Get("Cookie")
			select {
			case <-releaseResponse:
			case <-r.Context().Done():
				return
			}
			http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: oldToken, Path: "/"})
		case "/oauth/token":
			http.SetCookie(w, &http.Cookie{Name: testSessionCookieName, Value: newToken, Path: "/"})
			_, _ = w.Write([]byte("{\"signed_in\":true}"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(hub.Close)

	setTestHome(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	events := recordBrowserAuthEvents(app)
	ctx := browserAuthTestContext(t)
	require.NoError(t, app.ConnectDistributed(ctx, hub.URL))
	proxy := app.connection.proxy
	seedSessionCookie(proxy, testSessionCookieName)

	finished := make(chan error, 1)
	go func() {
		_, _, err := app.ProxyHTTP(ctx, http.MethodGet, "/delayed", nil, nil)
		finished <- err
	}()
	select {
	case header := <-reachedHub:
		require.Equal(t, testSessionCookieName+"="+oldToken, header)
	case <-ctx.Done():
		t.Fatal("the original request did not reach the Hub")
	}

	_, authorizeURL, err := app.StartBrowserAuth(desktoppb.BrowserAuthKind_BROWSER_AUTH_KIND_SIGN_IN)
	require.NoError(t, err)
	completeBrowserCallback(t, authorizeURL, "accepted-code")
	event := requireBrowserAuthEvent(t, events)
	require.True(t, event.Ok, event.Failure)
	assert.Equal(t, testSessionCookieName+"="+newToken, proxy.cookieHeader().Get("Cookie"))
	close(releaseResponse)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the original request did not return")
	}

	assert.Equal(t, testSessionCookieName+"="+newToken, proxy.cookieHeader().Get("Cookie"))
	storedCookies := openPersistentJar(proxy.base, sessionsDir()).Cookies(proxy.base)
	require.Len(t, storedCookies, 1)
	assert.Equal(t, newToken, storedCookies[0].Value)
}
