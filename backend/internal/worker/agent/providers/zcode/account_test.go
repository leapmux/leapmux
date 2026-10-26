package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// zcodeTestSignedInStore is a credential store with a Z.ai sign-in and a ZCode token.
func zcodeTestSignedInStore(t *testing.T, accessToken string) zcodeCredentialStore {
	t.Helper()
	return zcodeTestCredentialStore(t, map[string]string{
		zcodeOAuthAccessTokenKey("zai"): accessToken,
		zcodeOAuthUserInfoKey("zai"):    zcodeTestUserInfo("user-1"),
		zcodeCredentialZCodeJWT:         "store-jwt",
	})
}

// zcodeAccountTestAgent is an agent whose account state binds the Z.ai plans of
// configJSON (empty for no config.json) against store, and whose coding plans mint
// from a fake project API.
func zcodeAccountTestAgent(t *testing.T, configJSON string, store zcodeCredentialStore) (*Agent, *zcodeFakeProjectAPI) {
	t.Helper()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	if configJSON != "" {
		a.catalog = zcodeTestCatalog(t, configJSON)
	}
	a.catalog.accountSelection = zcodeAccountSelection{domain: "zai", planKinds: map[string]string{"zai": "individual-coding-plan"}}
	a.credentials = store
	a.identity = zcodeTestIdentity
	tokens, api, _ := newZCodeTestProjectTokens(t)
	a.projectTokens = tokens
	seedZCodeAccountStateForTests(t, a)
	return a, api
}

func TestZCodeAccountRequestAuth_MintsACodingPlanTokenFromTheSignIn(t *testing.T) {
	t.Parallel()

	a, api := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	ctx := context.Background()
	auth, err := a.accountRequestAuth(ctx, zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan", Reason: "model-request"})
	require.NoError(t, err)
	assert.Equal(t, "token-1", auth.APIKey)
	assert.Equal(t, "key-1", auth.APIKeyID)
	assert.Regexp(t, `^[0-9a-f]{64}$`, auth.AccountScope)
	assert.Equal(t, 1, api.count("GET /api/biz/customer/getCustomerInfo auth=login "), "the store's OAuth token signs the discovery")

	// The app-server refused the token: it sends the scope and the token's hash back.
	retry, err := a.accountRequestAuth(ctx, zcodeRuntimeHeadersRequest{
		ProviderID:                      "account:zai-individual-coding-plan",
		Reason:                          "model-request",
		ExpectedAccountScope:            auth.AccountScope,
		RejectedProjectTokenFingerprint: zcodeSHA256Hex(auth.APIKey),
	})
	require.NoError(t, err)
	assert.Equal(t, "token-2", retry.APIKey, "a refused token is minted again")
	assert.Equal(t, auth.AccountScope, retry.AccountScope)

	_, err = a.accountRequestAuth(ctx, zcodeRuntimeHeadersRequest{
		ProviderID: "account:zai-individual-coding-plan", ExpectedAccountScope: strings.Repeat("0", 64),
	})
	require.ErrorIs(t, err, errZCodeScopeInvalidated, "a retry from another sign-in must not get a token")

	usage, err := a.accountRequestAuth(ctx, zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan", Reason: "usage"})
	require.NoError(t, err)
	assert.Empty(t, usage.AccountScope, "a usage readout carries no scope")
}

// A mint is a network call, so the answer must not hold the output reader: the
// app-server's other frames, such as the reply to an interrupt, wait behind it.
func TestAnswerProviderRuntimeHeaders_MintsWithoutHoldingTheReader(t *testing.T) {
	t.Parallel()

	a, api := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	release := make(chan struct{})
	api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
	var releaseOnce sync.Once
	releaseMint := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseMint)

	returned := make(chan struct{})
	go func() {
		a.HandleOutput(zcodeRequestLine(t, 21, MethodRequestProviderRuntimeHeaders,
			`{"providerId":"account:zai-individual-coding-plan","reason":"model-request"}`))
		close(returned)
	}()
	require.Eventually(t, func() bool {
		select {
		case <-returned:
			return true
		default:
			return false
		}
	}, 30*time.Second, time.Millisecond, "the reader must return while the mint runs")
	stdin := a.StdinForTest().(*zcodeRecordedStdin)
	assert.Empty(t, stdin.Frames(), "the reply waits for the mint")

	releaseMint()
	reply := zcodeAwaitReplies(t, stdin, 1)[0]
	assert.Equal(t, "21", string(reply.ID))
	var result struct {
		HeadersApplied bool             `json:"headersApplied"`
		RequestAuth    zcodeRequestAuth `json:"requestAuth"`
	}
	require.NoError(t, json.Unmarshal(reply.Result, &result))
	assert.True(t, result.HeadersApplied)
	assert.Equal(t, "token-1", result.RequestAuth.APIKey)
}

// A sign-in can change while a mint runs. The token belongs to the old sign-in, so
// the answer must not hand it out as the credential of the new one.
func TestZCodeAccountRequestAuth_RefusesATokenOfASignInThatChangedDuringTheMint(t *testing.T) {
	t.Parallel()

	store := zcodeTestSignedInStore(t, "login")
	a, api := zcodeAccountTestAgent(t, "", store)
	release := make(chan struct{})
	api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
	var releaseOnce sync.Once
	releaseMint := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseMint)

	errs := make(chan error, 1)
	go func() {
		_, err := a.accountRequestAuth(context.Background(),
			zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan", Reason: "model-request"})
		errs <- err
	}()
	require.Eventually(t, func() bool { return api.count("POST ") == 1 }, 30*time.Second, time.Millisecond)
	renewed := zcodeTestSignedInStore(t, "login-2")
	data, err := os.ReadFile(renewed.path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.path, data, 0o600))
	releaseMint()
	require.ErrorIs(t, <-errs, errZCodeScopeInvalidated)
}

// A new sign-in serves at once, because each request reads the store again.
func TestZCodeAccountRequestAuth_FollowsANewSignIn(t *testing.T) {
	t.Parallel()

	store := zcodeTestSignedInStore(t, "login")
	a, api := zcodeAccountTestAgent(t, "", store)
	first, err := a.accountRequestAuth(context.Background(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan"})
	require.NoError(t, err)

	renewed := zcodeTestSignedInStore(t, "login-2")
	data, err := os.ReadFile(renewed.path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.path, data, 0o600))
	second, err := a.accountRequestAuth(context.Background(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan"})
	require.NoError(t, err)
	assert.NotEqual(t, first.AccountScope, second.AccountScope)
	assert.NotEqual(t, first.APIKey, second.APIKey, "a new sign-in never reuses the previous one's token")
	assert.Equal(t, 1, api.count("POST /api/biz/v1/organization/org-default/projects/p-default/api_keys/key-1/access_tokens auth=Bearer login-2 "),
		"the same account keeps its project, and the new sign-in mints")
	assert.Equal(t, 1, api.count("GET /api/biz/customer/getCustomerInfo "), "the account's project is known")
	_, err = a.accountRequestAuth(context.Background(), zcodeRuntimeHeadersRequest{
		ProviderID: "account:zai-individual-coding-plan", ExpectedAccountScope: first.AccountScope,
	})
	require.ErrorIs(t, err, errZCodeScopeInvalidated)
}

func TestZCodeAccountRequestAuth_StartPlanAndConfigFallbacks(t *testing.T) {
	t.Parallel()

	const configJSON = `{
      "provider": {
        "builtin:zai-coding-plan": {"kind": "anthropic", "enabled": true, "options": {"apiKey": "plan-key"}, "models": {"GLM-5.3": {}}},
        "builtin:zai-start-plan": {"kind": "anthropic", "options": {"apiKey": "start-key"}, "models": {"GLM-5.3-Flash": {}}}
      }
    }`
	for _, test := range []struct {
		name       string
		store      zcodeCredentialStore
		providerID string
		want       string
	}{
		{name: "a Start Plan sends the store's ZCode token", store: zcodeTestSignedInStore(t, "login"), providerID: "account:zai-start-plan", want: "store-jwt"},
		{name: "a Start Plan falls back to config.json", store: zcodeTestCredentialStore(t, nil), providerID: "account:zai-start-plan", want: "start-key"},
		{name: "a coding plan without a sign-in falls back to config.json", store: zcodeTestCredentialStore(t, nil), providerID: "account:zai-individual-coding-plan", want: "plan-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			a, api := zcodeAccountTestAgent(t, configJSON, test.store)
			auth, err := a.accountRequestAuth(context.Background(), zcodeRuntimeHeadersRequest{ProviderID: test.providerID})
			require.NoError(t, err)
			assert.Equal(t, zcodeRequestAuth{APIKey: test.want}, auth)
			assert.Empty(t, api.recorded(), "no project token is minted")
		})
	}
}

func TestZCodeAccountRequestAuth_RefusesWhatItCannotServe(t *testing.T) {
	t.Parallel()

	a, _ := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	_, err := a.accountRequestAuth(context.Background(), zcodeRuntimeHeadersRequest{ProviderID: "account:unknown"})
	require.ErrorContains(t, err, `leapmux holds no credential for provider "account:unknown"`)

	a.Mu.Lock()
	a.account = nil
	a.Mu.Unlock()
	_, err = a.accountRequestAuth(context.Background(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan"})
	require.ErrorContains(t, err, "holds no credential")

	team := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	team.catalog.accountSelection = zcodeAccountSelection{domain: "zai", planKinds: map[string]string{"zai": "team-coding-plan"}}
	team.credentials = zcodeTestSignedInStore(t, "login")
	seedZCodeAccountStateForTests(t, team)
	_, err = team.accountRequestAuth(context.Background(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-team-coding-plan"})
	require.ErrorContains(t, err, "states no team project")
}

func TestZCodeAccountRequestAuth_RefusesAStaleBigModelTeamSignIn(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog.accountSelection = zcodeAccountSelection{
		domain:    "bigmodel",
		planKinds: map[string]string{"bigmodel": "team-coding-plan"},
		teams:     map[string]zcodeTeamProject{"bigmodel": {organizationID: "org", projectID: "proj"}},
	}
	a.credentials = zcodeTestCredentialStore(t, map[string]string{
		zcodeOAuthAccessTokenKey("bigmodel"): "same", zcodeCredentialZCodeJWT: "same", zcodeCredentialActiveProvider: "bigmodel",
	})
	credentials := zcodeTestReadCredentials(t, a.credentials)
	bindings := zcodeTestBindings(t, a.catalog, `{
      "revision": 1,
      "config": {"providerConfigRules": {"providerRules": [
        {"providerId":"account:bigmodel-team-coding-plan","config":{"access":{"type":"zhipu-account","accountType":"bigmodel","mode":"team-coding-plan"}}}
      ]}}
    }`, credentials)
	require.Len(t, bindings, 1)
	a.account = &zcodeAccountState{bindings: bindings}
	_, err := a.accountRequestAuth(context.Background(), zcodeRuntimeHeadersRequest{ProviderID: "account:bigmodel-team-coding-plan"})
	require.ErrorContains(t, err, "stale")
}

// A sign-in in the credential store binds the account plans without config.json,
// which the current desktop no longer writes.
func TestZCodeCatalog_AccountProviderBindingsFromTheStoreAlone(t *testing.T) {
	t.Parallel()

	credentials := zcodeTestReadCredentials(t, zcodeTestSignedInStore(t, "login"))
	bindings := zcodeTestBindings(t, zcodeCatalog{}, `{
      "revision": 33,
      "config": {"providerConfigRules": {"providerRules": [
        {"providerId":"account:zai-individual-coding-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"individual-coding-plan"},"api":{"baseUrl":"https://api.z.ai/api/anthropic"}}},
        {"providerId":"account:zai-start-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"start-plan"},"api":{"baseUrl":"https://zcode.z.ai/api/v1/zcode-plan/anthropic"}}},
        {"providerId":"account:bigmodel-start-plan","config":{"access":{"type":"zhipu-account","accountType":"bigmodel","mode":"start-plan"}}},
        {"providerId":"account:zai-offpeak-idle-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"off-peak"}}}
      ]}}
    }`, credentials)
	require.Len(t, bindings, 2, "BigModel holds no sign-in, and an off-peak plan is not bound")
	assert.Equal(t, "builtin:zai-coding-plan", bindings[0].legacyID)
	assert.Nil(t, bindings[0].provider)
	assert.Equal(t, "builtin:zai-start-plan", bindings[1].legacyID)
	assert.Equal(t, "store-jwt", bindings[1].startPlanCredential())
	assert.Equal(t, "https://zcode.z.ai/api/v1/zcode-plan/anthropic", bindings[1].baseURL(), "the installed rule states the endpoint")
}

func TestZCodeAccountState_TracksWhatTheAppServerHolds(t *testing.T) {
	t.Parallel()

	available := zcodeAccountEntitlement{Availability: zcodeAvailabilityAvailable, Models: []string{"GLM-5.3"}}
	state := &zcodeAccountState{plans: map[string]*zcodeStartPlanState{
		"builtin:zai-start-plan":      {entitlement: zcodeUnknownEntitlement},
		"builtin:bigmodel-start-plan": {entitlement: zcodeUnknownEntitlement},
	}}
	first, learned := state.payload("r")
	assert.Equal(t, "r", first.Revision)
	state.accepted(learned)
	assert.False(t, state.unpushed())

	state.learnForTest(map[string]zcodeAccountEntitlement{"builtin:zai-start-plan": zcodeUnknownEntitlement})
	assert.False(t, state.unpushed(), "a check that learned nothing changes nothing")

	state.learnForTest(map[string]zcodeAccountEntitlement{"builtin:zai-start-plan": available})
	assert.True(t, state.unpushed())
	second, learned := state.payload("r")
	assert.Equal(t, "r#1", second.Revision, "each push carries a new revision")

	// A check that learns while the push runs is not part of the pushed snapshot.
	state.learnForTest(map[string]zcodeAccountEntitlement{"builtin:bigmodel-start-plan": available})
	state.accepted(learned)
	assert.True(t, state.unpushed(), "the app-server does not hold the plan that the push did not carry")

	state.learnForTest(map[string]zcodeAccountEntitlement{"builtin:zai-start-plan": {Availability: zcodeAvailabilityUnavailable}})
	assert.Equal(t, zcodeAvailabilityUnavailable, state.plans["builtin:zai-start-plan"].entitlement.Availability, "a new result replaces an earlier entitlement")
	third, learned := state.payload("r")
	assert.Equal(t, "r#3", third.Revision)
	state.accepted(learned)
	assert.False(t, state.unpushed())
}

// zcodeNthRequest waits until stdin holds n requests for method and returns the n-th.
func zcodeNthRequest(t *testing.T, stdin *zcodeRecordedStdin, method string, n int) zcodeSentRequest {
	t.Helper()
	var found zcodeSentRequest
	require.Eventually(t, func() bool {
		count := 0
		for _, req := range stdin.Requests(t) {
			if req.Method == method {
				count++
				if count == n {
					found = req
					return true
				}
			}
		}
		return false
	}, 30*time.Second, time.Millisecond)
	return found
}

// zcodeRecheckHarness is a started agent whose first Start Plan check failed: the
// billing API answers 503 to the first check and a live plan to each later one.
type zcodeRecheckHarness struct {
	a         *Agent
	stdin     *zcodeRecordedStdin
	clock     *quartz.Mock
	newTimer  *quartz.Trap
	stopTimer *quartz.Trap

	mu sync.Mutex
	// authorizations holds the Authorization header of each check, in order.
	authorizations []string
}

// newZCodeRecheckHarness starts the agent on store and waits for its first snapshot.
func newZCodeRecheckHarness(t *testing.T, store zcodeCredentialStore) *zcodeRecheckHarness {
	t.Helper()
	h := newZCodeRecheckHarnessWithReply(t, store, testutil.NewQuartzMock(t), zcodeBalanceTestReply{status: http.StatusServiceUnavailable})
	var first zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 1).Params, &first))
	require.Equal(t, zcodeAvailabilityUnknown, first.States["account:zai-start-plan"].Availability)
	return h
}

type zcodeBalanceTestReply struct {
	status int
	body   string
}

func newZCodeRecheckHarnessWithReply(t *testing.T, store zcodeCredentialStore, clock *quartz.Mock, initial zcodeBalanceTestReply) *zcodeRecheckHarness {
	t.Helper()
	return newZCodeRecheckHarnessWithHandler(t, store, clock, func(w http.ResponseWriter, _ *http.Request, request int) {
		if request == 1 {
			if initial.status != 0 {
				w.WriteHeader(initial.status)
			}
			_, _ = io.WriteString(w, initial.body)
			return
		}
		_, _ = io.WriteString(w, zcodeLiveBalanceReply)
	})
}

func newZCodeRecheckHarnessWithHandler(t *testing.T, store zcodeCredentialStore, clock *quartz.Mock, respond func(http.ResponseWriter, *http.Request, int)) *zcodeRecheckHarness {
	t.Helper()
	h := &zcodeRecheckHarness{clock: clock, stdin: &zcodeRecordedStdin{}}
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.authorizations = append(h.authorizations, r.Header.Get("Authorization"))
		request := len(h.authorizations)
		h.mu.Unlock()
		respond(w, r, request)
	})
	h.newTimer, h.stopTimer = testutil.NewTimerTraps(t, h.clock, zcodeStartPlanRecheckTimerTag)
	h.a = zcodeStartPlanTestAgentWithClock(t, h.stdin, server.URL, client, h.clock)
	h.a.credentials = store
	refuseZCodeRequest(t, h.a, h.stdin, MethodUpdateProviderRegistry, ErrMethodNotFound, "Method not found")
	answerZCodeRequest(t, h.a, h.stdin, MethodUpdateAccountConfig,
		`{"receivedRevision":"leapmux-test","providerCount":2,"status":"received"}`)
	require.NoError(t, h.a.pushProviderRegistry(zcodeTestRPCTimeout))

	return h
}

// checks returns the Authorization header of each check so far.
func (h *zcodeRecheckHarness) checks() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.authorizations...)
}

// zcodeAwaitRecheck waits for the re-check timer, asserts its delay, and fires it.
func zcodeAwaitRecheck(t *testing.T, clock *quartz.Mock, newTimer *quartz.Trap, delay time.Duration) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, delay, testutil.WaitForTimer(t, ctx, newTimer))
	clock.Advance(delay).MustWait(ctx)
}

// zcodeRequireLearnedSnapshot asserts that req pushes the live Start Plan with revision.
func zcodeRequireLearnedSnapshot(t *testing.T, req zcodeSentRequest, revision string) {
	t.Helper()
	var payload zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(req.Params, &payload))
	assert.Equal(t, revision, payload.Revision)
	assert.True(t, payload.Providers["account:zai-start-plan"].Access.Entitled)
	assert.Equal(t, []string{"GLM-5.3-Flash", "GLM-5.3"}, payload.Providers["account:zai-start-plan"].BuiltinModelIDs)
}

func zcodeAcknowledgeAccountSnapshot(t *testing.T, h *zcodeRecheckHarness, request zcodeSentRequest, readNumber int) {
	t.Helper()
	var payload zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(request.Params, &payload))
	h.a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"receivedRevision":"`+payload.Revision+`","status":"received"}`)))
	zcodeAcknowledgeModelCatalog(t, h, readNumber)
	read := zcodeNthRequest(t, h.stdin, MethodSessionRead, readNumber)
	h.a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, read), json.RawMessage(`{}`)))
}

func zcodeAcknowledgeModelCatalog(t *testing.T, h *zcodeRecheckHarness, n int) {
	t.Helper()
	request := zcodeNthRequest(t, h.stdin, MethodSessionResume, n)
	h.a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"session":{"sessionId":"sess-1"},"settings":{"model":{"available":[]}}}`)))
}

// A check that fails at startup must not hide the plan for the agent's life: the
// agent checks again, pushes a new snapshot, and the open session reads the models.
func TestZCodeProviderSetupRechecksAnUnknownStartPlan(t *testing.T) {
	t.Parallel()

	h := newZCodeRecheckHarness(t, zcodeCredentialStore{})
	zcodeAwaitRecheck(t, h.clock, h.newTimer, zcodeStartPlanRecheckDelays[0])

	second := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 2)
	zcodeRequireLearnedSnapshot(t, second, "leapmux-test#1")
	h.a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second),
		json.RawMessage(`{"receivedRevision":"leapmux-test#1","providerCount":2,"status":"received"}`)))

	zcodeAcknowledgeModelCatalog(t, h, 1)
	read := zcodeNthRequest(t, h.stdin, MethodSessionRead, 1)
	h.a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, read), json.RawMessage(`{}`)))
	assert.Equal(t, []string{"Bearer start-key", "Bearer start-key"}, h.checks())
}

// A learned plan whose push fails is pushed again at the next re-check, without
// another check: the entitlement is known, but the app-server does not hold it.
func TestZCodeProviderSetupPushesALearnedStartPlanAgainAfterAFailedPush(t *testing.T) {
	t.Parallel()

	h := newZCodeRecheckHarness(t, zcodeCredentialStore{})
	zcodeAwaitRecheck(t, h.clock, h.newTimer, zcodeStartPlanRecheckDelays[0])
	second := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 2)
	zcodeRequireLearnedSnapshot(t, second, "leapmux-test#1")
	h.a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, second), -32603, "internal error"))

	zcodeAwaitRecheck(t, h.clock, h.newTimer, zcodeAccountPollInterval)
	third := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 3)
	zcodeRequireLearnedSnapshot(t, third, "leapmux-test#1")
	h.a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, third),
		json.RawMessage(`{"receivedRevision":"leapmux-test#1","providerCount":2,"status":"received"}`)))

	zcodeAcknowledgeModelCatalog(t, h, 1)
	read := zcodeNthRequest(t, h.stdin, MethodSessionRead, 1)
	h.a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, read), json.RawMessage(`{}`)))
	assert.Len(t, h.checks(), 2, "a known entitlement needs no new check")
}

// A re-check reads the credential store again, so a sign-in that happened after
// startup serves the next check.
func TestZCodeProviderSetupRecheckReadsANewSignIn(t *testing.T) {
	t.Parallel()

	store := zcodeTestCredentialStore(t, nil)
	h := newZCodeRecheckHarness(t, store)
	zcodeChangeTestJWTAtNextPoll(t, h, store, "new-jwt")
	invalidated := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 2)
	var payload zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(invalidated.Params, &payload))
	assert.False(t, payload.Providers["account:zai-start-plan"].Access.Entitled)
	zcodeAcknowledgeAccountSnapshot(t, h, invalidated, 1)
	available := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 3)
	zcodeRequireLearnedSnapshot(t, available, "leapmux-test#2")
	zcodeAcknowledgeAccountSnapshot(t, h, available, 2)
	assert.Equal(t, []string{"Bearer start-key", "Bearer new-jwt"}, h.checks(),
		"the first check had only the config.json key, and the re-check sends the store's new token")
}

// A stopped agent stops its re-checks: the loop releases its timer and sends nothing.
func TestZCodeProviderSetupRecheckEndsWhenTheAgentStops(t *testing.T) {
	t.Parallel()

	h := newZCodeRecheckHarness(t, zcodeCredentialStore{})
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, zcodeStartPlanRecheckDelays[0], testutil.WaitForTimer(t, ctx, h.newTimer))
	h.a.CancelForTest()
	h.stopTimer.MustWait(ctx).MustRelease(ctx)

	assert.Len(t, h.checks(), 1, "no check after the agent stops")
	pushes := 0
	for _, req := range h.stdin.Requests(t) {
		if req.Method == MethodUpdateAccountConfig {
			pushes++
		}
	}
	assert.Equal(t, 1, pushes, "no snapshot after the agent stops")
}

func TestZCodeAccountRequestAuth_RefusesScopedFallbackAfterSignOut(t *testing.T) {
	t.Parallel()
	const config = `{"provider":{"builtin:zai-coding-plan":{"kind":"anthropic","options":{"apiKey":"config-key"},"models":{"GLM-5.3":{}}}}}`
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("unreadable=%v", broken), func(t *testing.T) {
			t.Parallel()
			store := zcodeTestSignedInStore(t, "login")
			a, api := zcodeAccountTestAgent(t, config, store)
			first, err := a.accountRequestAuth(t.Context(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan"})
			require.NoError(t, err)
			if broken {
				require.NoError(t, os.WriteFile(store.path, []byte("{"), 0o600))
			} else {
				require.NoError(t, os.Remove(store.path))
			}
			got, err := a.accountRequestAuth(t.Context(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan", ExpectedAccountScope: first.AccountScope})
			require.ErrorIs(t, err, errZCodeScopeInvalidated)
			assert.Empty(t, got)
			fallback, err := a.accountRequestAuth(t.Context(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan"})
			require.NoError(t, err)
			assert.Equal(t, "config-key", fallback.APIKey)
			assert.Equal(t, int32(1), api.mints.Load())
		})
	}
}

func TestZCodeAccountRequestAuth_RefusesUnreadableScopeDuringMint(t *testing.T) {
	t.Parallel()
	for _, material := range []string{"json", "oauth", "jwt"} {
		t.Run(material, func(t *testing.T) {
			t.Parallel()
			store := zcodeTestSignedInStore(t, "login")
			a, api := zcodeAccountTestAgent(t, "", store)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
			result := make(chan error, 1)
			go func() {
				auth, err := a.accountRequestAuth(t.Context(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan"})
				assert.Empty(t, auth)
				result <- err
			}()
			require.Eventually(t, func() bool { return api.count("POST ") == 1 }, 30*time.Second, time.Millisecond)
			data := []byte("{")
			if material != "json" {
				values := map[string]string{zcodeOAuthAccessTokenKey("zai"): "login", zcodeOAuthUserInfoKey("zai"): zcodeTestUserInfo("user-1"), zcodeCredentialZCodeJWT: "store-jwt"}
				values[map[string]string{"oauth": zcodeOAuthAccessTokenKey("zai"), "jwt": zcodeCredentialZCodeJWT}[material]] = "enc:v1:broken"
				var err error
				data, err = json.Marshal(values)
				require.NoError(t, err)
			}
			require.NoError(t, os.WriteFile(store.path, data, 0o600))
			unblock()
			select {
			case err := <-result:
				require.ErrorIs(t, err, errZCodeScopeInvalidated)
			case <-time.After(30 * time.Second):
				t.Fatal("credential request did not finish")
			}
		})
	}
}

func TestZCodeAccountState_ChangesRevisionWhenAnUnacknowledgedPayloadChanges(t *testing.T) {
	t.Parallel()
	state := &zcodeAccountState{plans: map[string]*zcodeStartPlanState{"a": {entitlement: zcodeUnknownEntitlement}, "b": {entitlement: zcodeUnknownEntitlement}}}
	_, generation := state.payload("r")
	state.accepted(generation)
	state.learnForTest(map[string]zcodeAccountEntitlement{"a": zcodeAvailable("a")})
	first, _ := state.payload("r")
	unchanged, _ := state.payload("r")
	assert.Equal(t, first.Revision, unchanged.Revision, "retrying identical content is idempotent")
	state.learnForTest(map[string]zcodeAccountEntitlement{"b": zcodeAvailable("b")})
	second, _ := state.payload("r")
	assert.NotEqual(t, first.Revision, second.Revision, "a lost acknowledgement cannot cause different content to share a revision")
}

func TestZCodeAccountRequestAuth_AcceptsUnchangedIdentityDuringMint(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"reencryption", "profile", "active-provider", "unreadable-jwt"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			store := zcodeTestSignedInStore(t, "login")
			a, api := zcodeAccountTestAgent(t, "", store)
			values := map[string]string{zcodeOAuthAccessTokenKey("zai"): "login", zcodeOAuthUserInfoKey("zai"): zcodeTestUserInfo("user-1"), zcodeCredentialZCodeJWT: "store-jwt"}
			if change == "unreadable-jwt" {
				credentials := zcodeTestReadCredentials(t, store)
				credentials.values[zcodeCredentialZCodeJWT] = "enc:v1:broken"
				data, err := json.Marshal(credentials.values)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(store.path, data, 0o600))
			}
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
			result := make(chan error, 1)
			go func() {
				auth, err := a.accountRequestAuth(t.Context(), zcodeRuntimeHeadersRequest{ProviderID: "account:zai-individual-coding-plan"})
				if err == nil {
					assert.NotEmpty(t, auth.AccountScope)
				}
				result <- err
			}()
			require.Eventually(t, func() bool { return api.count("POST ") == 1 }, 30*time.Second, time.Millisecond)
			if change != "unreadable-jwt" {
				if change == "profile" {
					values[zcodeOAuthUserInfoKey("zai")] = `{"user_id":"user-1","name":"Renamed","email":"new@example.com"}`
				}
				if change == "active-provider" {
					values[zcodeCredentialActiveProvider] = "bigmodel"
				}
				replacement := zcodeTestCredentialStore(t, values)
				data, err := os.ReadFile(replacement.path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(store.path, data, 0o600))
			}
			unblock()
			select {
			case err := <-result:
				require.NoError(t, err)
			case <-time.After(30 * time.Second):
				t.Fatal("credential request did not finish")
			}
		})
	}
}

func TestZCodeProviderSetupRefreshesAPendingStartPlan(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	now := clock.Now().Unix()
	body := fmt.Sprintf(`{"code":0,"data":{"server_time":%d,"plans":[{"plan_id":"zcode-start-plan","status":"active","starts_at":%d}],"balances":[]}}`, now, now+30)
	h := newZCodeRecheckHarnessWithReply(t, zcodeCredentialStore{}, clock, zcodeBalanceTestReply{body: body})
	var first zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 1).Params, &first))
	require.Equal(t, zcodeAvailabilityPending, first.States["account:zai-start-plan"].Availability)
	zcodeAwaitRecheck(t, h.clock, h.newTimer, 30*time.Second)
	second := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 2)
	zcodeRequireLearnedSnapshot(t, second, "leapmux-test#1")
	zcodeAcknowledgeAccountSnapshot(t, h, second, 1)
	assert.Len(t, h.checks(), 2)
}

func TestZCodeProviderSetupRefreshesARefusedCredentialAfterSignIn(t *testing.T) {
	t.Parallel()
	store := zcodeTestCredentialStore(t, map[string]string{zcodeCredentialZCodeJWT: "old-jwt"})
	h := newZCodeRecheckHarnessWithReply(t, store, testutil.NewQuartzMock(t), zcodeBalanceTestReply{status: http.StatusUnauthorized})
	zcodeChangeTestJWTAtNextPoll(t, h, store, "new-jwt")
	second := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 2)
	var invalidated zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(second.Params, &invalidated))
	assert.False(t, invalidated.Providers["account:zai-start-plan"].Access.Entitled)
	zcodeAcknowledgeAccountSnapshot(t, h, second, 1)
	third := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 3)
	zcodeRequireLearnedSnapshot(t, third, "leapmux-test#2")
	zcodeAcknowledgeAccountSnapshot(t, h, third, 2)
	assert.Equal(t, []string{"Bearer old-jwt", "Bearer new-jwt"}, h.checks())
}

// learnForTest isolates generation bookkeeping from transport and scheduling.
func (s *zcodeAccountState) learnForTest(results map[string]zcodeAccountEntitlement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entitlement := range results {
		s.learnLocked(id, entitlement)
	}
}
