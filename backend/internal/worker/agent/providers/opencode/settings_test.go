package opencode

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These ACP family tests drive one model switch with the stored effort. The user changes
// only the model, and the worker merges the stored effort into the update, so
// UpdateSettings receives {model: B, effort: X}.
//
// The fake server enforces these ACP family rules:
//
//   - A model write to a different model resets the effort to the first variant of the
//     new model.
//   - The server sends config_option_update before it replies to the model write.
//   - A write of an effort that the current model lacks fails with -32602.
//   - A model without variants has no effort option.

const (
	openCodeModelA = "leapmux/model-a"
	openCodeModelB = "leapmux/model-b"
)

// openCodeSwitchServer models the ACP family with two models. model-a offers low, medium and
// high. The variants of model-b are the caller's.
func openCodeSwitchServer(variantsOfB ...string) *acptest.ModelSwitchServer {
	server := &acptest.ModelSwitchServer{
		EffortID: agent.OptionIDEffort,
		Models:   []string{openCodeModelA, openCodeModelB},
		Levels:   map[string][]string{openCodeModelA: {"low", "medium", "high"}, openCodeModelB: variantsOfB},
	}
	server.Reset = func(model, _ string) string { return server.Levels[model][0] }
	server.Start(openCodeModelA, "high")
	return server
}

func newOpenCodeSwitchAgent(t *testing.T, server *acptest.ModelSwitchServer) (*familyTestAgent, *agenttest.Sink, func() []agenttest.RecordedRequest) {
	t.Helper()
	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *familyTestAgent {
			a := &familyTestAgent{}
			*a.HooksForTest() = FamilyHooks(&a.FamilyBase)
			return a
		},
		func(a *familyTestAgent) *acp.Base { return &a.Base },
		server.Respond,
	)
	return ag, server.Seed(ag, true), requests
}

func TestOpenCodeModelSwitchKeepsTheChosenEffortAfterTheNativeReset(t *testing.T) {
	t.Parallel()
	server := openCodeSwitchServer("none", "low", "medium", "high")
	ag, sink, requests := newOpenCodeSwitchAgent(t, server)
	before := sink.SettingsRefreshCount()

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: openCodeModelB, agent.OptionIDEffort: "medium"})

	require.True(t, result.AppliedLive)
	writes := openCodeEffortWrites(requests())
	require.NotEmpty(t, writes)
	assert.Equal(t, "medium", writes[len(writes)-1].Params["value"],
		"the stored effort is the last write, after the reset to the first variant")
	assert.Equal(t, "medium", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
	require.NotNil(t, result.Settlements[agent.OptionIDEffort].Value)
	assert.Equal(t, "medium", *result.Settlements[agent.OptionIDEffort].Value)
	require.Greater(t, sink.SettingsRefreshCount(), before, "the notification persists the reset effort")
	assert.Equal(t, "medium", sink.LastSettingsRefresh().Effort,
		"the last persisted refresh states the effort that the session runs")
}

// A user who turned the effort off keeps it off. The base raises the effort of a new
// model that starts at none, and then writes the stored level again.
func TestOpenCodeModelSwitchKeepsAnExplicitNoneEffort(t *testing.T) {
	t.Parallel()
	server := openCodeSwitchServer("none", "low", "medium", "high")
	server.Levels[openCodeModelA] = []string{"none", "low", "medium", "high"}
	server.Start(openCodeModelA, "none")
	ag, sink, _ := newOpenCodeSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: openCodeModelB, agent.OptionIDEffort: "none"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, "none", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
	assert.Equal(t, "none", sink.LastSettingsRefresh().Effort, "the persisted refresh states none")
}

// A model without variants has no effort option, and the server refuses a write of
// one. The switch must not write the stored effort, because the refusal would restart
// the agent.
func TestOpenCodeModelSwitchToAModelWithoutVariantsWritesNoEffort(t *testing.T) {
	t.Parallel()
	server := openCodeSwitchServer()
	ag, _, requests := newOpenCodeSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: openCodeModelB, agent.OptionIDEffort: "high"})

	require.True(t, result.AppliedLive, "the server refuses an effort write for a model without variants")
	assert.Empty(t, openCodeEffortWrites(requests()))
	assert.Nil(t, optionids.GroupByID(ag.OptionGroups(), agent.OptionIDEffort))
	_, surfaced := result.SurfacedOptions[agent.OptionIDEffort]
	assert.False(t, surfaced)
}

func TestOpenCodeRuntimeSettingsApplyNativeAgentModelAndVariant(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	var models []openCodeModelRef
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/session/ses_1/model" {
			var body struct {
				Model openCodeModelRef `json:"model"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			models = append(models, body.Model)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	result := runtime.UpdateSettings(optionmap.Map{agent.OptionIDPrimaryAgent: "plan", agent.OptionIDModel: "provider/model", agent.OptionIDEffort: "variant:auto"})
	require.True(t, result.AppliedLive)
	assert.Equal(t, "plan", result.ConfirmedOptions()[agent.OptionIDPrimaryAgent])
	assert.Equal(t, "provider/model", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, "variant:auto", result.ConfirmedOptions()[agent.OptionIDEffort])
	result = runtime.UpdateSettings(optionmap.Map{agent.OptionIDEffort: agent.EffortAuto})
	assert.Equal(t, agent.EffortAuto, result.ConfirmedOptions()[agent.OptionIDEffort])
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/api/session/ses_1/agent", "/api/session/ses_1/model", "/api/session/ses_1/model"}, paths)
	assert.Equal(t, []openCodeModelRef{{ProviderID: "provider", ID: "model", Variant: "auto"}, {ProviderID: "provider", ID: "model"}}, models)
}

func TestOpenCodeRuntimeSettingsRejectInvalidChoicesWithoutHTTP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	result := runtime.UpdateSettings(optionmap.Map{agent.OptionIDPrimaryAgent: "general", agent.OptionIDModel: "missing/model", agent.OptionIDEffort: "variant:missing"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, "build", result.ConfirmedOptions()[agent.OptionIDPrimaryAgent])
	assert.Equal(t, "provider/model", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Zero(t, calls.Load())
}

func TestOpenCodeRuntimeSettingsFailureDoesNotConfirmUnknownValues(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	options := optionmap.Map{agent.OptionIDPrimaryAgent: "plan"}
	assert.Equal(t, agent.RestartRequiredSettings(options), runtime.UpdateSettings(options))
	assert.Equal(t, "build", runtime.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDPrimaryAgent])
}

func TestOpenCodeRuntimeSettingsLateHTTPReplyKeepsNewerNativeSelection(t *testing.T) {
	t.Parallel()
	var runtime *openCodeSessionRuntime
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, runtime.observeSessionInfo(openCodeSessionInfo{ID: "ses_1", Agent: "custom"}))
		w.WriteHeader(http.StatusNoContent)
	})
	runtime = inputRuntime(t, rpc, &agenttest.Sink{})
	result := runtime.UpdateSettings(optionmap.Map{agent.OptionIDPrimaryAgent: "plan"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, "custom", result.ConfirmedOptions()[agent.OptionIDPrimaryAgent])
}

func TestOpenCodeRuntimeEmptySettingsKeepInheritedSelections(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	runtime.selectedAgentID = ""
	result := runtime.UpdateSettings(optionmap.Map{})
	assert.True(t, result.AppliedLive)
	assert.Zero(t, calls.Load())
	assert.Empty(t, runtime.selectedAgentID)
	assert.Nil(t, runtime.selectedModel)
}

func TestOpenCodeRuntimeAgentChangeKeepsTheNativeModelInheritance(t *testing.T) {
	t.Parallel()
	var pathsMu sync.Mutex
	var paths []string
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		pathsMu.Lock()
		paths = append(paths, r.URL.Path)
		pathsMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	runtime.catalog.agents[1].Model = &openCodeModelRef{ProviderID: "other", ID: "model/special"}
	result := runtime.UpdateSettings(optionmap.Map{agent.OptionIDPrimaryAgent: "plan"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, "other/model/special", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Nil(t, runtime.selectedModel)
	pathsMu.Lock()
	defer pathsMu.Unlock()
	assert.Equal(t, []string{"/api/session/ses_1/agent"}, paths)
}

func TestOpenCodeRuntimeSettingsUseEffectiveVariant(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"high", "", "default"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			runtime := inputRuntime(t, rpc, &agenttest.Sink{})
			runtime.selectedAgentID = ""
			runtime.catalog.agents[0].Model = &openCodeModelRef{ProviderID: "provider", ID: "model", Variant: variant}
			expected := agent.EffortAuto
			if variant == "high" {
				expected = "variant:high"
			}
			assert.Equal(t, expected, runtime.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDEffort])
			runtime.catalog.agents[1].Model = &openCodeModelRef{ProviderID: "provider", ID: "model", Variant: "low"}
			result := runtime.UpdateSettings(optionmap.Map{agent.OptionIDPrimaryAgent: "plan"})
			require.True(t, result.AppliedLive)
			assert.Equal(t, "variant:low", result.ConfirmedOptions()[agent.OptionIDEffort])
			assert.Nil(t, runtime.selectedModel)
		})
	}
}

func TestOpenCodeRuntimeSettingsReplaceUnavailableModel(t *testing.T) {
	t.Parallel()
	for _, old := range []string{"missing", "disabled"} {
		t.Run(old, func(t *testing.T) {
			t.Parallel()
			var requested openCodeModelRef
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model openCodeModelRef `json:"model"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				requested = body.Model
				w.WriteHeader(http.StatusNoContent)
			})
			runtime := inputRuntime(t, rpc, &agenttest.Sink{})
			runtime.selectedModel = &openCodeModelRef{ProviderID: "provider", ID: old, Variant: "high"}
			groups := runtime.OptionGroups()
			var mutable bool
			for _, g := range groups {
				if g.Id == agent.OptionIDModel {
					mutable = g.Mutable
				}
			}
			assert.True(t, mutable)
			result := runtime.UpdateSettings(optionmap.Map{agent.OptionIDModel: "provider/model", agent.OptionIDEffort: "variant:low"})
			require.True(t, result.AppliedLive)
			assert.Equal(t, openCodeModelRef{ProviderID: "provider", ID: "model", Variant: "low"}, requested)
			assert.Equal(t, "provider/model", result.ConfirmedOptions()[agent.OptionIDModel])
			assert.Equal(t, "variant:low", result.ConfirmedOptions()[agent.OptionIDEffort])
		})
	}
}
