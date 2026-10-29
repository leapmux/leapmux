package zcode

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func zcodeReplaceTestJWT(t *testing.T, store zcodeCredentialStore, credential string) {
	t.Helper()
	replacement := zcodeTestCredentialStore(t, map[string]string{zcodeCredentialZCodeJWT: credential})
	data, err := os.ReadFile(replacement.path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.path, data, 0o600))
}

// Wait for the owner to read the old credentials before replacing the store.
// An earlier write can trigger a refresh before the first poll timer exists.
func zcodeChangeTestJWTAtNextPoll(t *testing.T, h *zcodeRecheckHarness, store zcodeCredentialStore, credential string) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, zcodeAccountPollInterval, testutil.WaitForTimer(t, ctx, h.newTimer))
	zcodeReplaceTestJWT(t, store, credential)
	h.clock.Advance(zcodeAccountPollInterval).MustWait(ctx)
}

func zcodeBalanceForTestModel(model string) string {
	return zcodeBalanceReplyJSON(zcodeActiveStartPlan, `[{"capabilities":["model:`+model+`"]}]`)
}

func zcodeRequireAccountModels(t *testing.T, request zcodeSentRequest, models ...string) {
	t.Helper()
	var payload zcodeAccountProviderPayload
	require.NoError(t, json.Unmarshal(request.Params, &payload))
	provider := payload.Providers["account:zai-start-plan"]
	assert.Equal(t, len(models) > 0, provider.Access.Entitled)
	assert.Equal(t, models, provider.BuiltinModelIDs)
}

func zcodeRuntimeAuthReply(t *testing.T, h *zcodeRecheckHarness) (zcodeSentRequest, bool) {
	t.Helper()
	for _, frame := range h.stdin.Requests(t) {
		if string(frame.ID) == "99" && frame.Method == "" {
			return frame, true
		}
	}
	return zcodeSentRequest{}, false
}

func zcodeRequestStartPlanAuth(t *testing.T, h *zcodeRecheckHarness) {
	t.Helper()
	h.a.HandleOutput(zcodeRequestLine(t, 99, MethodRequestProviderRuntimeHeaders, `{"providerId":"account:zai-start-plan","reason":"model-request"}`))
	h.a.Mu.Lock()
	state := h.a.account
	h.a.Mu.Unlock()
	require.Eventually(t, func() bool { return len(state.refresh) == 1 }, 30*time.Second, time.Millisecond)
	_, replied := zcodeRuntimeAuthReply(t, h)
	assert.False(t, replied, "the credential must wait for its accepted entitlement")
}

func zcodeRequireStartPlanCredential(t *testing.T, h *zcodeRecheckHarness, credential string) {
	t.Helper()
	var reply zcodeSentRequest
	require.Eventually(t, func() bool {
		var found bool
		reply, found = zcodeRuntimeAuthReply(t, h)
		return found
	}, 30*time.Second, time.Millisecond)
	var result struct {
		HeadersApplied bool             `json:"headersApplied"`
		RequestAuth    zcodeRequestAuth `json:"requestAuth"`
	}
	require.NoError(t, json.Unmarshal(reply.Result, &result))
	assert.True(t, result.HeadersApplied)
	assert.Equal(t, credential, result.RequestAuth.APIKey)
}

func TestZCodeAccountRefresh_WaitsForRetriedSnapshotBeforeAnsweringRuntimeAuth(t *testing.T) {
	t.Parallel()
	store := zcodeTestCredentialStore(t, map[string]string{zcodeCredentialZCodeJWT: "A"})
	h := newZCodeRecheckHarnessWithHandler(t, store, testutil.NewQuartzMock(t), func(w http.ResponseWriter, r *http.Request, _ int) {
		model := "GLM-5.3"
		if r.Header.Get("Authorization") == "Bearer B" {
			model = "GLM-5.3-Flash"
		}
		_, _ = io.WriteString(w, zcodeBalanceForTestModel(model))
	})
	zcodeChangeTestJWTAtNextPoll(t, h, store, "B")
	invalidated := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 2)
	zcodeRequireAccountModels(t, invalidated)
	zcodeRequestStartPlanAuth(t, h)
	zcodeAcknowledgeAccountSnapshot(t, h, invalidated, 1)
	available := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 3)
	zcodeRequireAccountModels(t, available, "GLM-5.3-Flash")
	_, replied := zcodeRuntimeAuthReply(t, h)
	assert.False(t, replied)
	h.a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, available), -32603, "acknowledgement lost"))
	// The waiting runtime request already requested a refresh. Release that
	// notification's timer cancellation, rather than advancing a timing window.
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, zcodeAccountPollInterval, testutil.WaitForTimer(t, ctx, h.newTimer))
	h.stopTimer.MustWait(ctx).MustRelease(ctx)
	retry := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 4)
	assert.JSONEq(t, string(available.Params), string(retry.Params), "an unacknowledged generation retries unchanged")
	_, replied = zcodeRuntimeAuthReply(t, h)
	assert.False(t, replied)
	zcodeAcknowledgeAccountSnapshot(t, h, retry, 2)
	zcodeRequireStartPlanCredential(t, h, "B")
	assert.Equal(t, []string{"Bearer A", "Bearer B"}, h.checks())
}

func TestZCodeAccountRefresh_StartsOwnerWithoutStartPlanRules(t *testing.T) {
	t.Parallel()
	// A catalog with no Start Plan rules must still get a refresh owner.
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-coding-plan": {"kind": "anthropic", "enabled": true, "options": {"apiKey": "plan-key"}, "models": {"GLM-5.3": {}}}
      }
    }`)
	a.catalog.accountSelection = zcodeAccountSelection{domain: "zai", planKinds: map[string]string{"zai": "individual-coding-plan"}}
	a.credentials = zcodeTestCredentialStore(t, nil)
	seedZCodeAccountStateForTests(t, a)

	// The state must track a coding-plan binding.
	binding, ok := a.account.binding("account:zai-individual-coding-plan")
	require.True(t, ok, "the coding plan must be bound")
	assert.Equal(t, zcodeAvailabilityAvailable, binding.entitlement.Availability, "an absent cache preserves the current behavior")
}

func TestZCodeAccountRefresh_ReloadsCodingPlanEntitlement(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cachePath := filepath.Join(append([]string{home}, zcodeCodingPlanCacheRelPath...)...)
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o755))
	require.NoError(t, os.WriteFile(cachePath, []byte(`{"entryStatus":{"items":{"builtin:zai-coding-plan":{"status":"available"}}}}`), 0o600))

	entitlements := loadZCodeCodingPlanEntitlements(home)
	require.Contains(t, entitlements, "builtin:zai-coding-plan")
	assert.Equal(t, zcodeAvailabilityAvailable, entitlements["builtin:zai-coding-plan"].Availability)

	// Flip to unavailable and reload.
	require.NoError(t, os.WriteFile(cachePath, []byte(`{"entryStatus":{"items":{"builtin:zai-coding-plan":{"status":"unavailable"}}}}`), 0o600))
	entitlements = loadZCodeCodingPlanEntitlements(home)
	require.Contains(t, entitlements, "builtin:zai-coding-plan")
	assert.Equal(t, zcodeAvailabilityUnavailable, entitlements["builtin:zai-coding-plan"].Availability)
	assert.Equal(t, zcodeUnavailableNotEntitled, entitlements["builtin:zai-coding-plan"].UnavailableReason)
}

func TestZCodeAccountRefresh_PicksUpConfigKeyRotation(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	configPath := filepath.Join(append([]string{home}, zcodeConfigRelPath...)...)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	writeConfig := func(key string) {
		require.NoError(t, os.WriteFile(configPath, []byte(`{
          "provider": {
            "builtin:zai-coding-plan": {"kind": "anthropic", "enabled": true, "options": {"apiKey": "`+key+`"}, "models": {"GLM-5.3": {}}}
          }
        }`), 0o600))
	}
	writeConfig("old-key")

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.homeDir = home
	a.catalog.accountSelection = zcodeAccountSelection{domain: "zai", planKinds: map[string]string{"zai": "individual-coding-plan"}}
	a.credentials = zcodeTestCredentialStore(t, nil)

	var rule zcodeBuiltinProviderRule
	rule.ProviderID = "account:zai-individual-coding-plan"
	rule.Config.Access.Type = zcodeAccountAccessType
	rule.Config.Access.AccountType = zcodeFamilyZAI
	rule.Config.Access.Mode = "individual-coding-plan"

	bindings := a.readAccountBindings([]zcodeBuiltinProviderRule{rule})
	require.Len(t, bindings, 1)
	assert.Equal(t, "old-key", bindings[0].configKey())

	// Rotate the key in config.json and reload.
	writeConfig("new-key")
	bindings = a.readAccountBindings([]zcodeBuiltinProviderRule{rule})
	require.Len(t, bindings, 1)
	assert.Equal(t, "new-key", bindings[0].configKey(), "a config key rotation must be picked up on reload")
}

// The reload replaces the provider list alone. The live catalog owns Models and
// refs, so a config.json re-read must leave them untouched.
func TestZCodeAccountRefresh_ReloadReplacesOnlyTheProviderList(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	configPath := filepath.Join(append([]string{home}, zcodeConfigRelPath...)...)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{
      "provider": {
        "builtin:zai-coding-plan": {"kind": "anthropic", "enabled": true, "options": {"apiKey": "rotated-key"}, "models": {"GLM-5.3": {}}}
      }
    }`), 0o600))

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.homeDir = home
	a.catalog.accountSelection = zcodeAccountSelection{domain: "zai", planKinds: map[string]string{"zai": "individual-coding-plan"}}
	a.credentials = zcodeTestCredentialStore(t, nil)
	liveModels := []*agent.ModelInfo{{Id: "account:p/live"}}
	a.catalog.Models = liveModels
	a.catalog.refs = map[string]zcodeModelRef{"account:p/live": {ProviderID: "account:p", ModelID: "live"}}

	var rule zcodeBuiltinProviderRule
	rule.ProviderID = "account:zai-individual-coding-plan"
	rule.Config.Access.Type = zcodeAccountAccessType
	rule.Config.Access.AccountType = zcodeFamilyZAI
	rule.Config.Access.Mode = "individual-coding-plan"

	bindings := a.readAccountBindings([]zcodeBuiltinProviderRule{rule})
	require.Len(t, bindings, 1)
	assert.Equal(t, "rotated-key", bindings[0].configKey())
	assert.Equal(t, liveModels, a.catalog.Models, "the reload must not replace the live model list")
	assert.Equal(t, zcodeModelRef{ProviderID: "account:p", ModelID: "live"}, a.catalog.refs["account:p/live"],
		"the reload must not replace the live model refs")
}

// A malformed config.json rewrite keeps the last usable configuration: the
// reload refuses the parse and leaves the previous provider list serving.
func TestZCodeAccountRefresh_MalformedConfigKeepsTheLastUsableProviders(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	configPath := filepath.Join(append([]string{home}, zcodeConfigRelPath...)...)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{
      "provider": {
        "builtin:zai-coding-plan": {"kind": "anthropic", "enabled": true, "options": {"apiKey": "old-key"}, "models": {"GLM-5.3": {}}}
      }
    }`), 0o600))

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.homeDir = home
	a.catalog.accountSelection = zcodeAccountSelection{domain: "zai", planKinds: map[string]string{"zai": "individual-coding-plan"}}
	a.credentials = zcodeTestCredentialStore(t, nil)

	var rule zcodeBuiltinProviderRule
	rule.ProviderID = "account:zai-individual-coding-plan"
	rule.Config.Access.Type = zcodeAccountAccessType
	rule.Config.Access.AccountType = zcodeFamilyZAI
	rule.Config.Access.Mode = "individual-coding-plan"

	bindings := a.readAccountBindings([]zcodeBuiltinProviderRule{rule})
	require.Len(t, bindings, 1)
	require.Equal(t, "old-key", bindings[0].configKey())

	// A torn rewrite leaves a file no parser accepts.
	require.NoError(t, os.WriteFile(configPath, []byte(`{"provider": {`), 0o600))
	bindings = a.readAccountBindings([]zcodeBuiltinProviderRule{rule})
	require.Len(t, bindings, 1, "the last usable configuration keeps serving")
	assert.Equal(t, "old-key", bindings[0].configKey())
}

// The tolerated startup states assign their own verdict on reload, exactly as
// Start adopts them: a missing config.json holds no provider, so the reloaded
// list is empty and no inline key serves any more.
func TestZCodeAccountRefresh_AMissingConfigClearsTheProviderList(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	configPath := filepath.Join(append([]string{home}, zcodeConfigRelPath...)...)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{
      "provider": {
        "builtin:zai-coding-plan": {"kind": "anthropic", "enabled": true, "options": {"apiKey": "old-key"}, "models": {"GLM-5.3": {}}}
      }
    }`), 0o600))

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.homeDir = home
	a.catalog.accountSelection = zcodeAccountSelection{domain: "zai", planKinds: map[string]string{"zai": "individual-coding-plan"}}
	a.credentials = zcodeTestCredentialStore(t, nil)

	var rule zcodeBuiltinProviderRule
	rule.ProviderID = "account:zai-individual-coding-plan"
	rule.Config.Access.Type = zcodeAccountAccessType
	rule.Config.Access.AccountType = zcodeFamilyZAI
	rule.Config.Access.Mode = "individual-coding-plan"

	bindings := a.readAccountBindings([]zcodeBuiltinProviderRule{rule})
	require.Len(t, bindings, 1)
	require.Equal(t, "old-key", bindings[0].configKey())

	require.NoError(t, os.Remove(configPath))
	bindings = a.readAccountBindings([]zcodeBuiltinProviderRule{rule})
	assert.Empty(t, bindings, "a missing config.json serves no inline credential")
}

func TestZCodeAccountRefresh_DiscardsAResultWhenSignInChangesDuringHTTP(t *testing.T) {
	t.Parallel()
	store := zcodeTestCredentialStore(t, map[string]string{zcodeCredentialZCodeJWT: "A"})
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	h := newZCodeRecheckHarnessWithHandler(t, store, testutil.NewQuartzMock(t), func(w http.ResponseWriter, r *http.Request, _ int) {
		model := "GLM-5.3"
		switch r.Header.Get("Authorization") {
		case "Bearer B":
			close(blocked)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			model = "GLM-5.3-Flash"
		case "Bearer C":
			model = "GLM-5.2"
		}
		_, _ = io.WriteString(w, zcodeBalanceForTestModel(model))
	})
	zcodeChangeTestJWTAtNextPoll(t, h, store, "B")
	invalidatedB := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 2)
	zcodeAcknowledgeAccountSnapshot(t, h, invalidatedB, 1)
	select {
	case <-blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("the replacement credential was not checked")
	}
	zcodeReplaceTestJWT(t, store, "C")
	zcodeRequestStartPlanAuth(t, h)
	unblock()
	invalidatedC := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 3)
	zcodeRequireAccountModels(t, invalidatedC)
	zcodeAcknowledgeAccountSnapshot(t, h, invalidatedC, 2)
	availableC := zcodeNthRequest(t, h.stdin, MethodUpdateAccountConfig, 4)
	zcodeRequireAccountModels(t, availableC, "GLM-5.2")
	_, replied := zcodeRuntimeAuthReply(t, h)
	assert.False(t, replied)
	zcodeAcknowledgeAccountSnapshot(t, h, availableC, 3)
	zcodeRequireStartPlanCredential(t, h, "C")
	for _, request := range h.stdin.Requests(t) {
		if request.Method == MethodUpdateAccountConfig {
			var payload zcodeAccountProviderPayload
			require.NoError(t, json.Unmarshal(request.Params, &payload))
			assert.False(t, slices.Contains(payload.Providers["account:zai-start-plan"].BuiltinModelIDs, "GLM-5.3-Flash"), "the obsolete credential's models must never reach the app-server")
		}
	}
	assert.Equal(t, []string{"Bearer A", "Bearer B", "Bearer C"}, h.checks())
}
