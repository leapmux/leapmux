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

	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
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
	req, err := http.NewRequestWithContext(testutil.DeadlineContext(t), http.MethodGet, redirectURI+"?"+query, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
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

func TestFlow_DuplicateCallbacksNeverBlock(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	redirectURI, state := callbackParts(t, flow)

	valid := url.Values{"state": {state}, "code": {"C-1"}}.Encode()
	status, _ := getCallback(t, redirectURI, valid)
	require.Equal(t, http.StatusOK, status)

	// The first outcome is pending when the duplicates arrive.
	status, _ = getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"different"}}.Encode())
	require.Equal(t, http.StatusOK, status, "a duplicate success callback must answer, not block")
	status, _ = getCallback(t, redirectURI, url.Values{"state": {"wrong"}, "code": {"evil"}}.Encode())
	require.Equal(t, http.StatusBadRequest, status, "a duplicate bad-state callback must answer, not block")
	status, _ = getCallback(t, redirectURI, url.Values{"state": {state}, "error": {"access_denied"}}.Encode())
	require.Equal(t, http.StatusBadRequest, status, "a later refusal must still answer")

	guard(t, "Close after a duplicate callback", flow.Close)

	code, err := flow.Wait(testutil.DeadlineContext(t))
	require.NoError(t, err)
	assert.Equal(t, "C-1", code)
}

func TestClose_AllowsTheAcceptedCallbackToFinishItsReply(t *testing.T) {
	flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI})
	redirectURI, state := callbackParts(t, flow)
	ctx := testutil.DeadlineContext(t)
	type result struct {
		code string
		err  error
	}
	finished := make(chan result, 1)
	go func() {
		code, err := flow.Wait(ctx)
		flow.Close()
		finished <- result{code: code, err: err}
	}()

	status, body := getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"C-1"}}.Encode())
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "Authorization received.")
	select {
	case got := <-finished:
		assert.NoError(t, got.err)
		assert.Equal(t, "C-1", got.code)
	case <-ctx.Done():
		t.Fatal("waiting for the flow to close")
	}
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

func TestWait_RejectsCallbacksAtAndAfterTheDeadline(t *testing.T) {
	for name, delay := range map[string]time.Duration{"at deadline": 0, "after deadline": time.Second} {
		t.Run(name, func(t *testing.T) {
			clock := testutil.NewQuartzMock(t)
			flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI, Clock: clock})
			redirectURI, state := callbackParts(t, flow)
			ctx := testutil.DeadlineContext(t)
			clock.Advance(loopbackauth.Timeout).MustWait(ctx)
			if delay > 0 {
				clock.Advance(delay).MustWait(ctx)
			}

			status, _ := getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"late"}}.Encode())
			assert.Equal(t, http.StatusGone, status)
			code, err := flow.Wait(ctx)
			assert.ErrorIs(t, err, loopbackauth.ErrTimeout)
			assert.Empty(t, code)
		})
	}
}

func TestWait_KeepsTheCallbackThatArrivedBeforeTheDeadline(t *testing.T) {
	for name, query := range map[string]url.Values{
		"code":  {"code": {"early"}},
		"error": {"error": {"access_denied"}},
	} {
		t.Run(name, func(t *testing.T) {
			clock := testutil.NewQuartzMock(t)
			flow := startFlow(t, loopbackauth.Request{HubBaseURL: hubURL, App: loopbackauth.ControlCLI, Clock: clock})
			redirectURI, state := callbackParts(t, flow)
			ctx := testutil.DeadlineContext(t)
			clock.Advance(loopbackauth.Timeout - time.Nanosecond).MustWait(ctx)
			query.Set("state", state)
			status, _ := getCallback(t, redirectURI, query.Encode())
			clock.Advance(time.Nanosecond).MustWait(ctx)

			code, err := flow.Wait(ctx)
			if query.Has("code") {
				assert.Equal(t, http.StatusOK, status)
				assert.NoError(t, err)
				assert.Equal(t, "early", code)
			} else {
				assert.Equal(t, http.StatusBadRequest, status)
				assert.ErrorContains(t, err, "access_denied")
				assert.Empty(t, code)
			}
		})
	}
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
	assert.Equal(t, "token:abc", qWith.Get("step_up"), "a step-up identifies its target")
	assert.False(t, qWithout.Has("step_up"), "a plain sign-in carries no step_up")

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

// TestOAuthErrorBody_Message pins the one line the callers report: the
// error code alone, or the hub's description after it when it sent one.
func TestOAuthErrorBody_Message(t *testing.T) {
	assert.Equal(t, "invalid_grant", loopbackauth.OAuthErrorBody{Error: "invalid_grant"}.Message())
	assert.Equal(t, "invalid_grant: token revoked",
		loopbackauth.OAuthErrorBody{Error: "invalid_grant", ErrorDescription: "token revoked"}.Message())
}

// TestOAuthErrorBody_RoundTripsThroughPostForm pins the error-body wire
// format end to end: PostForm sends the request, a refusal decodes with
// ReadOAuthError, and Message renders it back to the one line the callers
// report. RFC 6749 section 5.2 fixes the body's field names, so a rename
// here changes what every user reads on a refused stage.
func TestOAuthErrorBody_RoundTripsThroughPostForm(t *testing.T) {
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"token revoked"}`))
	}))
	t.Cleanup(srv.Close)

	resp, err := loopbackauth.PostForm(context.Background(), srv.Client(), srv.URL,
		url.Values{"grant_type": {"refresh_token"}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body := loopbackauth.ReadOAuthError(resp)
	assert.Equal(t, "invalid_grant", body.Error)
	assert.Equal(t, "token revoked", body.ErrorDescription)
	assert.Equal(t, "invalid_grant: token revoked", body.Message())
}

// TestReadOAuthError_ReportsTheZeroValueOnABodyItCannotRead pins the
// fallback the callers depend on: a proxy's HTML page or a truncated answer
// decodes to the zero value, so the caller falls back to the HTTP status
// instead of formatting an empty body as ": ".
func TestReadOAuthError_ReportsTheZeroValueOnABodyItCannotRead(t *testing.T) {
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	t.Cleanup(srv.Close)

	resp, err := loopbackauth.PostForm(context.Background(), srv.Client(), srv.URL, url.Values{})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, loopbackauth.OAuthErrorBody{}, loopbackauth.ReadOAuthError(resp))
}

// TestPostForm_SendsAFormBodyAndTheCallersHeaders pins the one request shape
// every path on the Hub's auth surface uses.
func TestPostForm_SendsAFormBodyAndTheCallersHeaders(t *testing.T) {
	type received struct {
		method      string
		contentType string
		authz       string
		field       string
	}
	got := make(chan received, 1)
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got <- received{
			method:      r.Method,
			contentType: r.Header.Get("Content-Type"),
			authz:       r.Header.Get("Authorization"),
			field:       r.FormValue("device_code"),
		}
	}))
	t.Cleanup(srv.Close)

	resp, err := loopbackauth.PostForm(context.Background(), srv.Client(), srv.URL,
		url.Values{"device_code": {"dev-1"}},
		func(h http.Header) { h.Set("Authorization", "Bearer lmx_a_1") })
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	r := <-got
	assert.Equal(t, http.MethodPost, r.method)
	assert.Equal(t, "application/x-www-form-urlencoded", r.contentType)
	assert.Equal(t, "Bearer lmx_a_1", r.authz)
	assert.Equal(t, "dev-1", r.field)
}

// TestPostForm_ObeysTheCallersContext keeps a hung hub from outliving the
// deadline of the call that opened the request.
func TestPostForm_ObeysTheCallersContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := loopbackauth.PostForm(ctx, http.DefaultClient, "http://127.0.0.1:1/anything", url.Values{})
	assert.ErrorIs(t, err, context.Canceled)
}
