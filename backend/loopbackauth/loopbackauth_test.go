package loopbackauth_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/pkce"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/loopbackauth"
)

const hubURL = "https://hub.example"

// startFlow begins a ceremony and closes it with the test. The Request needs
// no Hub reachability: Start only composes the authorize URL from it.
func startFlow(t *testing.T, req loopbackauth.Request) *loopbackauth.Flow {
	t.Helper()
	flow, err := loopbackauth.Start(req)
	require.NoError(t, err)
	t.Cleanup(flow.Close)
	return flow
}

// callbackParts reads the loopback callback address and the state out of the
// authorize URL -- the only values the exported surface hands out.
func callbackParts(t *testing.T, flow *loopbackauth.Flow) (redirectURI, state string) {
	t.Helper()
	u, err := url.Parse(flow.AuthorizeURL())
	require.NoError(t, err)
	q := u.Query()
	return q.Get("redirect_uri"), q.Get("state")
}

// getCallback performs one callback request and reads its answer.
func getCallback(t *testing.T, redirectURI, query string) (status int, body string) {
	t.Helper()
	resp, err := http.Get(redirectURI + "?" + query)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// guard runs fn, failing the test when fn does not return in 30 seconds.
//
// It is a DEADLOCK GUARD, not a timing assumption: a passing test never
// reaches the limit. A blocked callback handler would otherwise hang the
// suite instead of naming the send that parked.
func guard(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not return", what)
	}
}

func TestWait_ReturnsTheCodeOnAValidCallback(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	redirectURI, state := callbackParts(t, flow)

	status, _ := getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"C-1"}}.Encode())
	require.Equal(t, http.StatusOK, status)

	code, err := flow.Wait(testutil.DeadlineContext(t))
	require.NoError(t, err)
	assert.Equal(t, "C-1", code)
}

func TestWait_WrongStateKeepsWaiting(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	redirectURI, state := callbackParts(t, flow)

	status, _ := getCallback(t, redirectURI, url.Values{"state": {"wrong"}, "code": {"evil"}}.Encode())
	require.Equal(t, http.StatusBadRequest, status)

	// The state check runs first: an error riding along with a mismatched
	// state is still refused as a mismatch -- and silently, because a wrong
	// state ends nothing.
	status, _ = getCallback(t, redirectURI, url.Values{"state": {"wrong"}, "error": {"access_denied"}}.Encode())
	require.Equal(t, http.StatusBadRequest, status)

	// The flow is still waiting, so the valid callback is what Wait answers
	// with. Had the mismatch ended the ceremony, the error would surface here
	// instead of the code.
	status, _ = getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"C-1"}}.Encode())
	require.Equal(t, http.StatusOK, status)

	code, err := flow.Wait(testutil.DeadlineContext(t))
	require.NoError(t, err)
	assert.Equal(t, "C-1", code)
}

func TestWait_ReportsTheErrorParameter(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	redirectURI, state := callbackParts(t, flow)

	status, _ := getCallback(t, redirectURI, url.Values{
		"state":             {state},
		"error":             {"access_denied"},
		"error_description": {"the account owner refused"},
	}.Encode())
	require.Equal(t, http.StatusBadRequest, status)

	_, err := flow.Wait(testutil.DeadlineContext(t))
	require.ErrorContains(t, err, "access_denied")
	require.ErrorContains(t, err, "the account owner refused")
}

func TestWait_ReportsACallbackWithoutCodeOrError(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	redirectURI, state := callbackParts(t, flow)

	status, _ := getCallback(t, redirectURI, url.Values{"state": {state}}.Encode())
	require.Equal(t, http.StatusBadRequest, status)

	_, err := flow.Wait(testutil.DeadlineContext(t))
	require.ErrorContains(t, err, "neither a code nor an error")
}

// TestFlow_DuplicateCallbacksNeverBlock is the wedge fix: the browser can
// re-GET the success page (or a stale tab can re-send a bad state) after the
// ceremony already holds its one outcome. A blocking send would park the
// handler goroutine and Close's Shutdown would wait for it forever -- a
// ceremony that already succeeded hanging the caller.
func TestFlow_DuplicateCallbacksNeverBlock(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	redirectURI, state := callbackParts(t, flow)

	valid := url.Values{"state": {state}, "code": {"C-1"}}.Encode()
	status, _ := getCallback(t, redirectURI, valid)
	require.Equal(t, http.StatusOK, status)

	// The outcome is still unconsumed, so the code channel is full -- the
	// exact state a duplicate arrival meets.
	status, _ = getCallback(t, redirectURI, valid)
	require.Equal(t, http.StatusOK, status, "a duplicate success callback must answer, not block")
	status, _ = getCallback(t, redirectURI, url.Values{"state": {"wrong"}, "code": {"evil"}}.Encode())
	require.Equal(t, http.StatusBadRequest, status, "a duplicate bad-state callback must answer, not block")
	status, _ = getCallback(t, redirectURI, valid)
	require.Equal(t, http.StatusOK, status, "a third duplicate must still answer")

	guard(t, "Close after a duplicate callback", flow.Close)

	// The FIRST outcome survives; duplicates were dropped rather than
	// replacing it.
	code, err := flow.Wait(testutil.DeadlineContext(t))
	require.NoError(t, err)
	assert.Equal(t, "C-1", code)
}

func TestClose_IsIdempotent(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	guard(t, "the first Close", flow.Close)
	guard(t, "the second Close", flow.Close)
}

func TestWait_ReturnsTheContextErrorOnCancel(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	cancel()

	_, err := flow.Wait(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestWait_ReturnsErrTimeoutWhenTheClockPassesTheDeadline(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI, Clock: clock})

	// Exactly Timeout reaches the armed timer and no further; the mock
	// refuses an advance past the next event, which keeps a stray timer from
	// hiding a mis-measured deadline.
	waiter := clock.Advance(loopbackauth.Timeout)

	_, err := flow.Wait(testutil.DeadlineContext(t))
	require.ErrorIs(t, err, loopbackauth.ErrTimeout)
	waiter.MustWait(testutil.DeadlineContext(t))
}

func TestAuthorizeURL_CarriesStepUpAndTheBoundPort(t *testing.T) {
	withStepUp := startFlow(t, loopbackauth.Request{
		HubBaseURL: hubURL,
		App:        loopbackauth.Desktop,
		StepUp:     loopbackauth.StepUpForToken("abc"),
	})
	without := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.Desktop})

	parse := func(flow *loopbackauth.Flow) url.Values {
		u, err := url.Parse(flow.AuthorizeURL())
		require.NoError(t, err)
		require.Equal(t, "/oauth/authorize", u.Path)
		require.Equal(t, hubURL, u.Scheme+"://"+u.Host)
		return u.Query()
	}
	qWith, qWithout := parse(withStepUp), parse(without)
	assert.Equal(t, "token:abc", qWith.Get("step_up"), "a step-up names its target")
	assert.Empty(t, qWithout.Get("step_up"), "a plain sign-in carries no step_up")

	// The redirect URI derives from the app's registered form with the BOUND
	// port: a request to it must reach each flow's own listener.
	for i, flow := range []*loopbackauth.Flow{withStepUp, without} {
		redirectURI, _ := callbackParts(t, flow)
		require.NotEmpty(t, redirectURI, "flow %d must publish a redirect", i)
		status, _ := getCallback(t, redirectURI, url.Values{"state": {"wrong"}}.Encode())
		require.Equal(t, http.StatusBadRequest, status, "flow %d must answer on its own bound port", i)
	}
	assert.NotEqual(t, qWith.Get("redirect_uri"), qWithout.Get("redirect_uri"),
		"two live flows must not share a port")
}

func TestTokenForm_MatchesTheAuthorizeURL(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	u, err := url.Parse(flow.AuthorizeURL())
	require.NoError(t, err)
	q := u.Query()

	form := flow.TokenForm("C-1")
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "C-1", form.Get("code"))
	assert.Equal(t, q.Get("code_challenge"), pkce.S256(form.Get("code_verifier")),
		"the verifier must be the one the challenge was drawn from")
	assert.Equal(t, q.Get("client_id"), form.Get("client_id"))
	assert.Equal(t, q.Get("redirect_uri"), form.Get("redirect_uri"))
}

func TestSuccessPage_NamesTheApp(t *testing.T) {
	cases := []struct {
		app  loopbackauth.App
		want string
	}{
		{loopbackauth.Desktop, "Authorization received. You can close this window and return to LeapMux Desktop."},
		{loopbackauth.ControlCLI, "Authorization received. You can close this window and return to the CLI."},
	}
	for _, c := range cases {
		flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: c.app})
		redirectURI, state := callbackParts(t, flow)
		status, body := getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"C-1"}}.Encode())
		require.Equal(t, http.StatusOK, status)
		assert.Contains(t, body, c.want)
	}
}

func TestStepUpForSessionCookieHeader_ReadsBothCookieNames(t *testing.T) {
	sessionID := strings.Repeat("a", id.GeneratedLength)
	token := auth.FormatSessionToken(sessionID, strings.Repeat("b", id.GeneratedLength))

	// A Hub with secure cookies.
	got, err := loopbackauth.StepUpForSessionCookieHeader(auth.SecureCookieName + "=" + token)
	require.NoError(t, err)
	assert.Equal(t, "session:"+sessionID, got)

	// A Hub without secure cookies never writes the __Host- prefix, and the
	// reader accepts its spelling too.
	got, err = loopbackauth.StepUpForSessionCookieHeader(auth.CookieName + "=" + token)
	require.NoError(t, err)
	assert.Equal(t, "session:"+sessionID, got)
}

func TestStepUpForSessionCookieHeader_RefusesAHeaderWithoutASessionToken(t *testing.T) {
	_, err := loopbackauth.StepUpForSessionCookieHeader("other=value; foo=bar")
	require.Error(t, err)

	_, err = loopbackauth.StepUpForSessionCookieHeader(auth.SecureCookieName + "=short")
	require.Error(t, err, "a token of the wrong length is not a session token")
}

func TestStepUpForToken_PrefixesTheTokenID(t *testing.T) {
	assert.Equal(t, "token:abc", loopbackauth.StepUpForToken("abc"))
}
