package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// elevationHub stands in for the hub's step-up surface: the
// device-authorization leg that opens a ceremony, and the token leg the CLI
// polls until a person approves it.
type elevationHub struct {
	server *httptest.Server

	mu     sync.Mutex
	starts int
	polls  int
	// authorization is the credential the last start request presented.
	authorization string
	// deviceLabel is the installation_name the last start request carried.
	deviceLabel string
	grant       map[string]any
	// startStatus, when non-zero, is the answer instead of the grant.
	startStatus int
	// startBody is the body of that refusal, for a refusal that identifies an
	// OAuth error code.
	startBody string
	// pollAnswer answers one poll. nil means "granted".
	pollAnswer func(w http.ResponseWriter, n int)
	// codeAnswer answers one authorization_code redemption, which is the
	// loopback step-up's token request. nil means "granted".
	codeAnswer func(w http.ResponseWriter, n int)
	// redemptions counts the authorization_code token requests, and
	// redemptionAuth / redemptionForm hold the last one's credential and
	// form.
	redemptions    int
	redemptionAuth string
	redemptionForm url.Values
	// onStart runs inside the start handler, before it answers. A test that
	// needs two ceremonies to OVERLAP blocks the first one here.
	onStart func()
}

// newElevationRoutes builds a ceremony that is approved on the FIRST poll,
// without a server of its own, so a test that also needs a hub RPC or the
// refresh leg can mount it on the same mux.
//
// The interval is one second, the smallest the wire can carry: RFC 8628
// states it in whole seconds, and the CLI waits one before its first poll.
// That second is the floor on any test that completes a ceremony.
func newElevationRoutes() *elevationHub {
	return &elevationHub{
		grant: map[string]any{
			"device_code":               "dev-1",
			"user_code":                 "WDJB-MJHT",
			"verification_uri":          "https://hub.example/oauth/device",
			"verification_uri_complete": "https://hub.example/oauth/device?user_code=WDJB-MJHT",
			"expires_in":                60,
			"interval":                  1,
		},
	}
}

// register mounts the two step-up legs on mux.
func (h *elevationHub) register(mux *http.ServeMux) {
	mux.HandleFunc("/oauth/step-up", h.startLeg())
	mux.HandleFunc("/oauth/token", h.tokenEndpoint(nil))
}

// startLeg answers /oauth/step-up.
func (h *elevationHub) startLeg() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		h.mu.Lock()
		h.starts++
		h.authorization = r.Header.Get("Authorization")
		h.deviceLabel = r.FormValue("installation_name")
		status, grant, hook := h.startStatus, h.grant, h.onStart
		body := h.startBody
		h.mu.Unlock()
		if hook != nil {
			hook()
		}
		if status != 0 {
			if body != "" {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(grant)
	}
}

// tokenEndpoint answers /oauth/token, routing by grant_type.
//
// `other` handles every grant this fixture does not: a nil `other` means the
// fixture serves the device-code poll and the loopback step-up's
// authorization_code redemption alone. A caller that also stands in for the
// refresh grant passes its own handler, and the two share ONE registration --
// which is what the hub does now, and what makes a second mux.HandleFunc for
// the same path a panic rather than a silent shadow.
func (h *elevationHub) tokenEndpoint(other http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.FormValue("grant_type") {
		case GrantTypeDeviceCode:
			h.mu.Lock()
			h.polls++
			n, answer := h.polls, h.pollAnswer
			h.mu.Unlock()
			if answer == nil {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"elevated":true}`))
				return
			}
			answer(w, n)
		case GrantTypeAuthorizationCode:
			h.mu.Lock()
			h.redemptions++
			h.redemptionAuth = r.Header.Get("Authorization")
			h.redemptionForm = r.Form
			n, answer := h.redemptions, h.codeAnswer
			h.mu.Unlock()
			if answer == nil {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"elevated":true}`))
				return
			}
			answer(w, n)
		default:
			if other != nil {
				other(w, r)
				return
			}
			http.Error(w, "unsupported_grant_type", http.StatusBadRequest)
		}
	}
}

// newElevationHub is newElevationRoutes on a server of its own.
func newElevationHub(t *testing.T) *elevationHub {
	t.Helper()
	h := newElevationRoutes()
	mux := http.NewServeMux()
	h.register(mux)
	h.server = hubtransporttest.NewServer(t, mux)
	t.Cleanup(h.server.Close)
	return h
}

func (h *elevationHub) counts() (starts, polls int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts, h.polls
}

func (h *elevationHub) presentedCredential() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.authorization
}

func (h *elevationHub) presentedDeviceName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deviceLabel
}

// redemption records what the loopback step-up's token request carried.
func (h *elevationHub) redemption() (n int, bearer string, form url.Values) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.redemptions, h.redemptionAuth, h.redemptionForm
}

// browserStepUpRefusal is the hub's answer to a device ceremony for a
// browser-issued credential: no device row, and one working alternative.
const browserStepUpRefusal = `{"error":"browser_step_up_required","error_description":"this credential was issued through a browser on its own computer; verify it through that browser"}`

// browserStub stands in for the person at the browser during a loopback
// ceremony: it records the authorize URL and answers its callback.
type browserStub struct {
	mu        sync.Mutex
	calls     int
	authorize string
}

// stubBrowser replaces OpenBrowser for the rest of the test. The replacement
// answers the callback of the authorize URL with callbackQuery of the URL's
// own state -- the test IS the person at the browser, and no real browser
// starts.
func stubBrowser(t *testing.T, callbackQuery func(state string) string) *browserStub {
	t.Helper()
	stub := &browserStub{}
	prev := OpenBrowser
	OpenBrowser = func(authorizeURL string) error {
		stub.mu.Lock()
		stub.calls++
		stub.authorize = authorizeURL
		stub.mu.Unlock()
		u, err := url.Parse(authorizeURL)
		require.NoError(t, err)
		q := u.Query()
		resp, err := http.Get(q.Get("redirect_uri") + "?" + callbackQuery(q.Get("state")))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		return nil
	}
	t.Cleanup(func() { OpenBrowser = prev })
	return stub
}

// approve answers a loopback callback with a code, as a person who approves
// would.
func approve(state string) string {
	return url.Values{"state": {state}, "code": {"C-9"}}.Encode()
}

// deny answers a loopback callback with the consent page's Deny.
func deny(state string) string {
	return url.Values{"state": {state}, "error": {"access_denied"}}.Encode()
}

func (s *browserStub) callsMade() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *browserStub) authorizeURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorize
}

// elevatableBrowserClient is elevatableClient for a credential whose token id
// lets a loopback step-up specify its target: the shape a login through the
// browser leaves on disk.
func elevatableBrowserClient(t *testing.T, hubURL string, expiresAt time.Time) *Client {
	t.Helper()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, SaveCredentials(hubURL, CredentialFile{
		HubURL:       hubURL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		ExpiresAt:    expiresAt,
		UserID:       "usr_1",
		Username:     "alice",
		TokenID:      "tok-1",
	}))
	c, err := NewClient(hubURL)
	require.NoError(t, err)
	c.promptAllowed = true
	return c
}

// elevatableClient builds a client for the stub hub that is allowed to
// prompt, which is what a terminal-attached process would be.
func elevatableClient(t *testing.T, hubURL string) *Client {
	t.Helper()
	seedCredentials(t, hubURL, time.Now().Add(time.Hour))
	c, err := NewClient(hubURL)
	require.NoError(t, err)
	c.promptAllowed = true
	return c
}

// syncBuffer is a buffer that two writers may share.
//
// Out and Err are os.Stdout and os.Stderr in production, where one Write is
// one syscall. A bytes.Buffer in their place is NOT safe for concurrent
// writers, so a test that runs two step-ups at once would report a data race
// in the test's own writer rather than in anything the CLI does.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// captureStreams swaps Out and Err for buffers and returns both.
func captureStreams(t *testing.T) (out, errOut *syncBuffer) {
	t.Helper()
	out, errOut = &syncBuffer{}, &syncBuffer{}
	prevOut, prevErr := Out, Err
	Out, Err = out, errOut
	t.Cleanup(func() { Out, Err = prevOut, prevErr })
	return out, errOut
}

// TestElevate_PrintsThePromptToErrAndNothingToOut is the JSON contract.
//
// Out carries the envelope of whichever verb opened the step-up -- the
// interceptor runs under ANY unary call -- so four lines of prose there stop
// `leapmux control admin settings set … | jq` from parsing on the first run
// after the window lapses.
func TestElevate_PrintsThePromptToErrAndNothingToOut(t *testing.T) {
	hub := newElevationHub(t)
	// A grant that is already expired: the prompt is printed, and the poll
	// loop ends at once. The test then costs no waiting.
	hub.grant["expires_in"] = 0

	c := elevatableClient(t, hub.server.URL)
	out, errOut := captureStreams(t)

	require.Error(t, c.Elevate(context.Background()))

	assert.Empty(t, out.String(), "the JSON envelope stream must carry no prose")
	prompt := errOut.String()
	assert.Contains(t, prompt, "verify your identity")
	assert.Contains(t, prompt, "https://hub.example/oauth/device")
	assert.Contains(t, prompt, "WDJB-MJHT")
	assert.Contains(t, prompt, "user_code=WDJB-MJHT", "the one-click URL must reach the user")
}

// TestElevate_RefusesWhenNobodyCanAnswerThePrompt is the headless case.
//
// The ceremony ends only when a person opens a browser. A CI job or a cron
// run whose window lapsed would print a URL nobody reads and then block for
// the full life of the grant -- ten minutes -- before failing. It must
// refuse at once instead, and post no device-authorization row at all.
func TestElevate_RefusesWhenNobodyCanAnswerThePrompt(t *testing.T) {
	hub := newElevationHub(t)
	c := elevatableClient(t, hub.server.URL)
	c.promptAllowed = false
	out, errOut := captureStreams(t)

	require.ErrorIs(t, c.Elevate(context.Background()), ErrElevationNeedsAPerson)

	starts, polls := hub.counts()
	assert.Zero(t, starts, "a ceremony nobody can finish must never be opened")
	assert.Zero(t, polls)
	assert.Empty(t, out.String())
	assert.Empty(t, errOut.String(), "a prompt nobody can read must not be printed")
}

// TestPromptsAllowed_ObeysTheOptOut pins the explicit refusal.
//
// Detection answers "is somebody probably there". A caller that KNOWS it is
// a script states so, and the variable wins over the detection: a supervisor
// that gives its child a pseudo-terminal is the ordinary case where the
// detection says yes and the truth is no.
func TestPromptsAllowed_ObeysTheOptOut(t *testing.T) {
	t.Setenv(noPromptEnv, "1")
	assert.False(t, promptsAllowed())

	// Presence is the whole test, the way NO_COLOR works. "0" is a value
	// somebody set on purpose.
	t.Setenv(noPromptEnv, "0")
	assert.False(t, promptsAllowed())

	t.Setenv(noPromptEnv, "")
	// With the variable clear the answer is the terminal detection, which
	// under `go test` is false: no test process owns one.
	assert.Equal(t, isInteractive(), promptsAllowed())
}

// TestNewClient_RefusesToPromptWithoutATerminal pins where the constructor
// takes the decision: once, so every call of one command answers alike.
func TestNewClient_RefusesToPromptWithoutATerminal(t *testing.T) {
	seedCredentials(t, "https://hub.example", time.Now().Add(time.Hour))
	c, err := NewClient("https://hub.example")
	require.NoError(t, err)
	assert.False(t, c.promptAllowed, "a test process has no terminal, so it may not prompt")
}

// TestElevate_CollapsesConcurrentCeremonies is why the flight exists. One
// ceremony needs one person, and a CLI command fans its calls out on one
// client -- the entity resolver runs its lookups in an errgroup. Without
// this, the first restricted verb on such a path posts N device-authorization
// rows and prints N interleaved URL and code triples that nobody can answer.
func TestElevate_CollapsesConcurrentCeremonies(t *testing.T) {
	hub := newElevationHub(t)
	c := elevatableClient(t, hub.server.URL)
	captureStreams(t)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.Elevate(context.Background())
		}()
	}
	wg.Wait()

	for _, err := range errs {
		assert.NoError(t, err)
	}
	starts, _ := hub.counts()
	assert.Equal(t, 1, starts, "concurrent callers must collapse onto one ceremony")
}

// TestElevate_ReportsATransportFailureRatherThanAnEmptyOAuthError is the
// answer a proxy gives.
//
// A 502 with an HTML body carries no OAuth error, so formatting the decoded
// fields produced the error ": " -- and the interceptor then reported the
// hub's ORIGINAL refusal, so the transport failure that actually stopped the
// step-up never reached anybody.
func TestElevate_ReportsATransportFailureRatherThanAnEmptyOAuthError(t *testing.T) {
	hub := newElevationHub(t)
	hub.pollAnswer = func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}
	c := elevatableClient(t, hub.server.URL)
	captureStreams(t)

	err := c.Elevate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502", "the status is what is left to report")
	assert.NotEqual(t, ": ", err.Error())
	assert.NotEmpty(t, strings.TrimSpace(strings.TrimPrefix(err.Error(), ":")))
}

// TestElevate_KeepsPollingWhileTheHubSaysPending covers the ordinary wait,
// and the slow_down answer that widens the cadence.
func TestElevate_KeepsPollingWhileTheHubSaysPending(t *testing.T) {
	hub := newElevationHub(t)
	hub.pollAnswer = func(w http.ResponseWriter, n int) {
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		default:
			_, _ = w.Write([]byte(`{"elevated":true}`))
		}
	}
	c := elevatableClient(t, hub.server.URL)
	captureStreams(t)

	require.NoError(t, c.Elevate(context.Background()))
	_, polls := hub.counts()
	assert.Equal(t, 2, polls, "a pending answer must be waited out, not reported")
}

// TestElevate_ReportsACredentialTheHubWillNotElevate keeps a ceremony that
// cannot exist from opening. A worker-minted delegation token has no browser
// leg, so the caller reports the hub's original refusal instead.
func TestElevate_ReportsACredentialTheHubWillNotElevate(t *testing.T) {
	hub := newElevationHub(t)
	hub.startStatus = http.StatusBadRequest
	c := elevatableClient(t, hub.server.URL)
	out, errOut := captureStreams(t)

	assert.ErrorIs(t, c.Elevate(context.Background()), ErrElevationUnsupported)
	assert.Empty(t, out.String())
	assert.Empty(t, errOut.String(), "no prompt for a credential that cannot be elevated")
}

// TestElevate_PresentsTheCredentialItAsksFor: the bearer is the right to
// ASK. What it cannot do is approve, which needs a browser session.
func TestElevate_PresentsTheCredentialItAsksFor(t *testing.T) {
	hub := newElevationHub(t)
	hub.grant["expires_in"] = 0

	c := elevatableClient(t, hub.server.URL)
	captureStreams(t)
	require.Error(t, c.Elevate(context.Background()))

	assert.Equal(t, "Bearer lmx_a_access_0", hub.presentedCredential())
}

// TestElevate_KeepsOneCeremonyForEachCredential is the other half of the
// flight: it collapses the ceremonies of ONE credential, and never those of
// two.
//
// A key that ignored the credential would hand the second hub the first
// hub's answer, so a step-up a person approved on one hub would satisfy a
// restricted call on another -- and the second hub would see no ceremony at
// all. The key is the credential path, so the two never meet.
func TestElevate_KeepsOneCeremonyForEachCredential(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	first, second := newElevationHub(t), newElevationHub(t)

	// The first ceremony BLOCKS inside its start handler, so the second one
	// begins while the first is still in flight. The timeout is what keeps a
	// failure a failure: httptest.Server.Close waits for an outstanding
	// request, so a handler that blocks for ever would hang the cleanup
	// instead of reporting the test.
	firstStarted, secondStarted := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	first.onStart = func() {
		close(firstStarted)
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}
	second.onStart = func() { close(secondStarted) }

	client := func(hubURL string) *Client {
		require.NoError(t, SaveCredentials(hubURL, CredentialFile{
			HubURL:       hubURL,
			AccessToken:  "lmx_a_access_0",
			RefreshToken: "lmx_a_refresh_0",
			ExpiresAt:    time.Now().Add(time.Hour),
		}))
		c, err := NewClient(hubURL)
		require.NoError(t, err)
		c.promptAllowed = true
		return c
	}
	a, b := client(first.server.URL), client(second.server.URL)
	captureStreams(t)

	firstDone := make(chan error, 1)
	go func() { firstDone <- a.Elevate(context.Background()) }()
	<-firstStarted

	secondDone := make(chan error, 1)
	go func() { secondDone <- b.Elevate(context.Background()) }()
	select {
	case <-secondStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("a second credential must open a ceremony of its own, not wait for the first")
	}
	close(release)

	assert.NoError(t, <-firstDone)
	assert.NoError(t, <-secondDone)
	firstStarts, _ := first.counts()
	secondStarts, _ := second.counts()
	assert.Equal(t, 1, firstStarts)
	assert.Equal(t, 1, secondStarts, "each credential asks its own hub for a ceremony")
}

// TestElevate_RecordsTheLabelALoginRecords pins the device label ON THE
// WIRE, which is the only place the two flows can be compared.
//
// The step-up used to send a bare hostname while a login sent "user@host",
// so the approval page asked a person to approve a device that matched
// nothing in their credential list.
func TestElevate_RecordsTheLabelALoginRecords(t *testing.T) {
	t.Setenv("USER", "alice")
	hub := newElevationHub(t)
	// A grant that expired already: the ceremony ends at once, and the start
	// request is what this test reads.
	hub.grant["expires_in"] = 0

	c := elevatableClient(t, hub.server.URL)
	captureStreams(t)
	require.Error(t, c.Elevate(context.Background()))

	assert.Equal(t, DefaultDeviceName(), hub.presentedDeviceName())
	assert.Contains(t, hub.presentedDeviceName(), "alice@",
		"the step-up records the label a login records, not a bare hostname")
}

// TestElevate_LeavesStdoutParseableForTheVerbThatOpenedIt closes the JSON
// contract at the point where the two writers meet.
//
// The step-up interrupts an ORDINARY verb -- any restricted unary call
// opens it, from the interceptor -- and that verb prints its envelope to Out
// in the same invocation. Four lines of prose in that stream stop
// `leapmux control admin settings set ... | jq` from parsing on the first
// run after the window lapses, so Out must hold the envelope and nothing
// else.
func TestElevate_LeavesStdoutParseableForTheVerbThatOpenedIt(t *testing.T) {
	hub := newRepairHub(t, 3600, elevationRequired())
	seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))
	out, errOut := captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = true

	require.NoError(t, hub.call(c))
	// The verb that made the call prints its envelope, which is all that
	// Out may ever hold.
	require.NoError(t, EmitData(map[string]any{"channel_id": "ch-1"}))

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope),
		"stdout must decode as the envelope alone")
	assert.Equal(t, "ch-1", envelope.Data["channel_id"])
	assert.Contains(t, errOut.String(), "verify your identity",
		"the prose the person must read still reaches them, on the other stream")
}

// TestElevate_FallsBackToTheLoopbackCeremonyWhenTheHubDemandsABrowser is the
// fallback: a browser-issued credential cannot prove its factor in the
// device ceremony, and the hub says so. The loopback ceremony runs instead,
// and the redemption presents the bearer this client holds.
func TestElevate_FallsBackToTheLoopbackCeremonyWhenTheHubDemandsABrowser(t *testing.T) {
	hub := newElevationHub(t)
	hub.startStatus = http.StatusBadRequest
	hub.startBody = browserStepUpRefusal
	c := elevatableBrowserClient(t, hub.server.URL, time.Now().Add(time.Hour))
	stub := stubBrowser(t, approve)
	captureStreams(t)

	require.NoError(t, c.Elevate(context.Background()))

	assert.Equal(t, 1, stub.callsMade(), "the person sees exactly one detour")
	u, err := url.Parse(stub.authorizeURL())
	require.NoError(t, err)
	require.Equal(t, "/oauth/authorize", u.Path)
	assert.Equal(t, "token:tok-1", u.Query().Get("step_up"),
		"the ceremony identifies the credential it verifies")

	n, bearer, form := hub.redemption()
	assert.Equal(t, 1, n, "one redemption for one approval")
	assert.Equal(t, "Bearer lmx_a_access_0", bearer, "the token request carries the bearer")
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "C-9", form.Get("code"))
	assert.NotEmpty(t, form.Get("code_verifier"))
	starts, polls := hub.counts()
	assert.Equal(t, 1, starts, "the device ceremony is still the first ask")
	assert.Zero(t, polls, "a refused device ceremony never polls")
}

// TestElevate_RunsTheDeviceCeremonyWhenTheHubGrantsIt is the other half of
// the fallback: a grant answers the device ask, so no loopback detour may
// open beside it.
func TestElevate_RunsTheDeviceCeremonyWhenTheHubGrantsIt(t *testing.T) {
	hub := newElevationHub(t)
	c := elevatableClient(t, hub.server.URL)
	stub := stubBrowser(t, approve)
	captureStreams(t)

	require.NoError(t, c.Elevate(context.Background()))

	assert.Zero(t, stub.callsMade(), "the device ceremony needs no browser of its own")
	starts, polls := hub.counts()
	assert.Equal(t, 1, starts)
	assert.Equal(t, 1, polls, "the approval comes through the device poll")
	n, _, _ := hub.redemption()
	assert.Zero(t, n, "a device step-up redeems no code")
}

// TestElevate_WithoutATokenIDTellsTheUserToSignInAgain keeps a ceremony that
// cannot bind from starting. Without a token id there is no step_up target,
// so the only way forward is a fresh login -- and the failure must say so
// before any listener binds.
func TestElevate_WithoutATokenIDTellsTheUserToSignInAgain(t *testing.T) {
	hub := newElevationHub(t)
	hub.startStatus = http.StatusBadRequest
	hub.startBody = browserStepUpRefusal
	// seedCredentials writes a credential file with no token id -- the shape
	// of a login that predates the field.
	c := elevatableClient(t, hub.server.URL)
	stub := stubBrowser(t, approve)
	captureStreams(t)

	err := c.Elevate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign in again with `leapmux control auth login`")
	assert.Zero(t, stub.callsMade(), "no listener binds, so no detour is ever offered")
	n, _, _ := hub.redemption()
	assert.Zero(t, n)
}

// TestElevate_RefreshesTheBearerAfterAWaitThatExpiresTheToken covers the
// wait itself: the step-up blocks on a person while the access token runs
// out, and the redemption must present the credential the code identifies, not
// the corpse of the one that asked.
func TestElevate_RefreshesTheBearerAfterAWaitThatExpiresTheToken(t *testing.T) {
	hub := newRepairHub(t, 3600)
	hub.elevation.startStatus = http.StatusBadRequest
	hub.elevation.startBody = browserStepUpRefusal
	// An expiry inside the refresh skew: the token dies while the person is
	// at the browser.
	c := elevatableBrowserClient(t, hub.server.URL, time.Now().Add(refreshSkew/2))
	stub := stubBrowser(t, approve)
	captureStreams(t)

	require.NoError(t, c.Elevate(context.Background()))

	assert.Equal(t, 1, stub.callsMade())
	assert.GreaterOrEqual(t, hub.rotations.Load(), int64(1),
		"the wait must refresh the credential it outlived")
	n, bearer, _ := hub.elevation.redemption()
	assert.Equal(t, 1, n)
	assert.Equal(t, "Bearer lmx_a_access_1", bearer,
		"the token request carries the NEW bearer")
}

// TestElevate_RepairsTheBearerAndPostsAgainAfterA401 pins the second post.
// The hub keeps the code valid after a 401, so a repaired bearer is worth
// one more redemption rather than a lost ceremony.
func TestElevate_RepairsTheBearerAndPostsAgainAfterA401(t *testing.T) {
	hub := newRepairHub(t, 3600)
	hub.elevation.startStatus = http.StatusBadRequest
	hub.elevation.startBody = browserStepUpRefusal
	hub.elevation.codeAnswer = func(w http.ResponseWriter, n int) {
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"elevated":true}`))
	}
	c := elevatableBrowserClient(t, hub.server.URL, time.Now().Add(time.Hour))
	stub := stubBrowser(t, approve)
	captureStreams(t)

	require.NoError(t, c.Elevate(context.Background()))

	assert.Equal(t, 1, stub.callsMade(), "the person answers once; the repair is the CLI's own")
	assert.GreaterOrEqual(t, hub.rotations.Load(), int64(1),
		"a 401 must repair the bearer before the replay")
	n, bearer, _ := hub.elevation.redemption()
	assert.Equal(t, 2, n, "one refused redemption, then the replay")
	assert.Equal(t, "Bearer lmx_a_access_1", bearer,
		"the replay carries the repaired bearer")
}

// TestElevate_ReportsTheDenyThePersonChose keeps a deliberate refusal from
// reading as a failure of the ceremony.
func TestElevate_ReportsTheDenyThePersonChose(t *testing.T) {
	hub := newElevationHub(t)
	hub.startStatus = http.StatusBadRequest
	hub.startBody = browserStepUpRefusal
	c := elevatableBrowserClient(t, hub.server.URL, time.Now().Add(time.Hour))
	stub := stubBrowser(t, deny)
	captureStreams(t)

	err := c.Elevate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access_denied")
	assert.Equal(t, 1, stub.callsMade())
	n, _, _ := hub.redemption()
	assert.Zero(t, n, "a Deny redeems nothing")
}

// TestElevate_ReportsTheHubsRefusalOfTheCode keeps the hub's own
// description: it is the only thing that says WHY a valid-looking callback
// did not produce a window.
func TestElevate_ReportsTheHubsRefusalOfTheCode(t *testing.T) {
	hub := newElevationHub(t)
	hub.startStatus = http.StatusBadRequest
	hub.startBody = browserStepUpRefusal
	hub.codeAnswer = func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"code expired or already consumed"}`))
	}
	c := elevatableBrowserClient(t, hub.server.URL, time.Now().Add(time.Hour))
	stub := stubBrowser(t, approve)
	captureStreams(t)

	err := c.Elevate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_grant")
	assert.Contains(t, err.Error(), "code expired or already consumed")
	assert.Equal(t, 1, stub.callsMade())
}

// TestElevateViaLoopback_ReportsACorruptCredentialFileRatherThanASignInPrompt
// separates the two reasons LoadCredentials can fail. A missing file means
// "sign in again", because there is no credential to verify. A file that will
// not parse is a different problem with a different remedy: the credential
// may only need repair, and mapping it to the sign-in message hides that.
func TestElevateViaLoopback_ReportsACorruptCredentialFileRatherThanASignInPrompt(t *testing.T) {
	hubURL := "https://hub.example"
	c := &Client{HubURL: hubURL}

	t.Run("a missing credential file says sign in again", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		err := c.elevateViaLoopback(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sign in again with `leapmux control auth login`")
	})

	t.Run("a corrupt credential file surfaces the parse error", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
		path, err := CredentialsPath(hubURL)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

		err = c.elevateViaLoopback(context.Background())
		require.Error(t, err)
		assert.NotErrorIs(t, err, errStepUpNeedsSignIn,
			"a corrupt file is not a credential without a token id")
		assert.Contains(t, err.Error(), "parse credentials")
	})
}
