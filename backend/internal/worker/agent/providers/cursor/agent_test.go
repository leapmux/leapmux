//go:build unix

package cursor

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func newCursorAgentForRPC(t *testing.T) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPC(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			a.HooksForTest().ModelIDNormalizer = normalizeCursorModelID
			a.HooksForTest().ModelSetter = a.setCursorModel
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
	)
}

func newCursorAgentForRPCWithResponder(t *testing.T, respond func(method string) agenttest.RPCReply) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPCWithResponder(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			a.HooksForTest().ModelIDNormalizer = normalizeCursorModelID
			a.HooksForTest().ModelSetter = a.setCursorModel
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
}

func installFakeCursorCLI(t *testing.T, scenario string) {
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:      "cursor-agent",
		HelperRun:   "TestHelperProcessCursorCLI",
		WantEnv:     "GO_WANT_HELPER_PROCESS_CURSOR",
		Env:         []string{"LEAPMUX_CURSOR_TEST_SCENARIO=" + scenario},
		ForwardArgs: true,
	})
}

func TestHelperProcessCursorCLI(*testing.T) {
	scenario := os.Getenv("LEAPMUX_CURSOR_TEST_SCENARIO")
	agenttest.ServeFakeJSONRPC("GO_WANT_HELPER_PROCESS_CURSOR", func(method string) (string, bool, bool) {
		switch method {
		case acp.MethodInitialize:
			return `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`, false, true
		case acp.MethodSessionNew:
			return `{"sessionId":"cursor-new","models":{"currentModelId":"default","availableModels":[{"modelId":"default","name":"Auto"},{"modelId":"gpt-5.4","name":"GPT-5.4"}]},"modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]},"configOptions":[{"id":"mode","currentValue":"agent","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"},{"value":"ask","name":"Ask"}]},{"id":"model","currentValue":"default","options":[{"value":"default","name":"Auto"},{"value":"gpt-5.4","name":"GPT-5.4"}]}]}`, false, true
		case acp.MethodSessionLoad:
			if scenario == "load" {
				return `{"models":{"currentModelId":"gpt-5.4","availableModels":[{"modelId":"default","name":"Auto"},{"modelId":"gpt-5.4","name":"GPT-5.4"}]},"modes":{"currentModeId":"plan","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]}}`, false, true
			}
			return "", false, false
		case acp.MethodSessionSetConfigOption, acp.MethodSessionSetModel, acp.MethodSessionSetMode, acp.MethodSessionPrompt:
			return `{}`, false, true
		default:
			return "", false, false
		}
	})
}

func TestStartCursorCLI_NewSessionHandshake(t *testing.T) {
	installFakeCursorCLI(t, "new")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:       "cursor-new",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)

	a := provider.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})

	assert.Equal(t, "cursor-new", a.SessionIDForTest())
	assert.Equal(t, "auto", a.ModelForTest())
	assert.Equal(t, ModeAgent, a.PermissionModeForTest())
	require.Len(t, a.AvailableModelsForTest(), 2)
	assert.Equal(t, "auto", a.AvailableModelsForTest()[0].GetId())
	groups := a.OptionGroups()
	assert.Equal(t, "auto", optionids.CurrentValue(groups, agent.OptionIDModel))
	require.NotNil(t, optionids.GroupByID(groups, agent.OptionIDPermissionMode))
}

func TestStartCursorCLI_LoadSessionUsesResumeID(t *testing.T) {
	home := t.TempDir()
	writeCursorACPPolicyFixture(t, home, "cursor-resume", `{"agentId":"cursor-resume","isRunEverything":false}`)
	installFakeCursorCLI(t, "load")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:         "cursor-load",
		HomeDir:         home,
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "cursor-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)

	agent := provider.(*Agent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "cursor-resume", agent.SessionIDForTest())
	assert.Equal(t, "gpt-5.4", agent.ModelForTest())
	assert.Equal(t, ModePlan, agent.PermissionModeForTest())
}

func TestCursorUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	t.Parallel()

	a, requests := newCursorAgentForRPC(t)
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{
		{Id: ModeAgent, Name: "Agent"},
		{Id: ModePlan, Name: "Plan"},
	})

	updated := a.UpdateSettings(map[string]string{
		agent.OptionIDModel:          "auto",
		agent.OptionIDPermissionMode: ModePlan,
	})
	require.True(t, updated.AppliedLive)
	assert.Equal(t, "auto", a.ModelForTest())
	assert.Equal(t, ModePlan, a.PermissionModeForTest())

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acp.ConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, cursorCLIModelAutoWire, recorded[0].Params["value"])
	assert.Equal(t, acp.MethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, ModePlan, recorded[1].Params["modeId"])
}

func TestCursorBypassChangePrecedesLiveSettingsWrites(t *testing.T) {
	t.Parallel()
	a, requests := newCursorAgentForRPC(t)
	a.SetModelForTest("auto")
	result := a.UpdateSettings(map[string]string{"bypassPermissions": "true", agent.OptionIDModel: "gpt-5.4"})
	assert.False(t, result.AppliedLive)
	assert.Empty(t, requests(), "a restart-required policy change must not partially apply a model change")
	assert.Equal(t, "auto", a.ModelForTest())
}

func TestCursorModelParametersUseIndependentNativeSetters(t *testing.T) {
	t.Parallel()
	a, requests := newCursorAgentForRPC(t)
	a.SetModelForTest("mock-sonnet")
	a.Mu.Lock()
	a.ApplyOptionGroupsLockedForTest([]acp.ConfigOption{
		{ID: "thinking", Category: "thought_level", CurrentValue: "false", Options: []acp.ConfigOptionValue{{Value: "false"}, {Value: "true"}}},
		{ID: "effort", Category: "thought_level", CurrentValue: "low", Options: []acp.ConfigOptionValue{{Value: "low"}, {Value: "high"}}},
		{ID: "fast", Category: "model_config", CurrentValue: "false", Options: []acp.ConfigOptionValue{{Value: "false"}, {Value: "true"}}},
	})
	a.Mu.Unlock()
	a.ApplyStartupOptionsForTest(agent.Options{Options: map[string]string{"effort": "high"}})
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, "effort", recorded[0].Params["configId"])
	assert.Equal(t, "high", recorded[0].Params["value"])
	result := a.UpdateSettings(map[string]string{"thinking": "true", "fast": "true"})
	require.True(t, result.AppliedLive)
	recorded = requests()
	require.Len(t, recorded, 3)
	for _, request := range recorded {
		assert.Equal(t, acp.MethodSessionSetConfigOption, request.Method)
		assert.NotEqual(t, "model", request.Params["configId"])
	}
	assert.ElementsMatch(t, []any{"thinking", "fast"}, []any{recorded[1].Params["configId"], recorded[2].Params["configId"]})
	assert.Equal(t, "mock-sonnet", a.ModelForTest())
}

// TestCursorUpdateSettingsSkipsUnchangedModelAndMode verifies the redundant-re-push guard:
// the service hands UpdateSettings the FULL merged options map on every change, so when the
// requested model/mode already match the current selection (e.g. only another axis moved),
// no session/set_config_option (model) or session/set_mode RPC is issued.
func TestCursorUpdateSettingsSkipsUnchangedModelAndMode(t *testing.T) {
	t.Parallel()

	ag, requests := newCursorAgentForRPC(t)
	ag.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{
		{Id: ModeAgent, Name: "Agent"},
		{Id: ModePlan, Name: "Plan"},
	})
	// Seed the current selection as a real apply would: model stored in its normalized
	// form, mode at a concrete value.
	const requestedModel = "gpt-5.4"
	ag.SetModelForTest(normalizeCursorModelID(requestedModel))
	ag.SetPermissionModeForTest(ModePlan)

	// Re-send the SAME model + mode.
	updated := ag.UpdateSettings(map[string]string{
		agent.OptionIDModel:          requestedModel,
		agent.OptionIDPermissionMode: ModePlan,
	})
	require.True(t, updated.AppliedLive)
	assert.Empty(t, requests(), "an unchanged model/mode issues no redundant set_config_option/set_mode RPC")
}

func TestCursorClearContextReappliesModelAndMode(t *testing.T) {
	t.Parallel()

	a, requests := newCursorAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("auto")
	a.SetPermissionModeForTest(ModePlan)
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{
		{Id: ModeAgent, Name: "Agent"},
		{Id: ModePlan, Name: "Plan"},
	})
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)

	sessionID, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "session-2", a.SessionIDForTest())

	// Verify model is preserved with wire format conversion.
	assert.Equal(t, "auto", a.ModelForTest())

	recorded := requests()
	require.Len(t, recorded, 3)
	assert.Equal(t, acp.MethodSessionNew, recorded[0].Method)
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[1].Method)
	assert.Equal(t, acp.ConfigOptionIDModel, recorded[1].Params["configId"])
	assert.Equal(t, cursorCLIModelAutoWire, recorded[1].Params["value"])
	assert.Equal(t, acp.MethodSessionSetMode, recorded[2].Method)
	assert.Equal(t, ModePlan, recorded[2].Params["modeId"])
}

func TestBuildCursorCLIModelsNormalizesAuto(t *testing.T) {
	t.Parallel()

	models := []acp.ModelInfo{
		{ModelID: cursorCLIModelAutoWire, Name: "Auto"},
		{ModelID: "gpt-5.4", Name: "GPT-5.4"},
	}
	result := acp.BuildModelsForTest(models, cursorCLIModelAutoWire, normalizeCursorModelID)
	require.Len(t, result, 2)
	assert.Equal(t, "auto", result[0].Id)
	assert.True(t, result[0].IsDefault)
}

func TestCursorDefaultModelIsAuto(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "auto", Registration().DefaultModel())
}

func TestDecorateCursorModelKeepsNativeNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ id, supplied, want string }{
		{"gpt-5.5", "", "GPT 5.5"},
		{"claude-opus-5", "claude-opus-5", "Claude Opus 5"},
		{"gpt-5.5", "Native model name", "Native model name"},
		{"auto", "Auto", "Auto"},
		{"", "", ""},
	} {
		model := &agent.ModelInfo{Id: tc.id, DisplayName: tc.supplied}
		decorateCursorModel(model)
		assert.Equal(t, tc.want, model.DisplayName)
		assert.Equal(t, tc.id, model.Id)
		assert.Empty(t, model.Description)
	}
}

// TestHumanizeModelID covers the Cursor model ids surfaced by the live ACP server (whose
// `name` is the bare bracket-less id), confirming friendly display names.
func TestHumanizeModelID(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"composer-2.5":      "Composer 2.5",
		"claude-opus-4-8":   "Claude Opus 4.8",
		"claude-fable-5":    "Claude Fable 5",
		"claude-sonnet-4-6": "Claude Sonnet 4.6",
		"gpt-5.5":           "GPT 5.5",
		"gpt-5.3-codex":     "GPT 5.3 Codex",
		"gpt-5.1-codex-max": "GPT 5.1 Codex Max",
		"gpt-5.4-mini":      "GPT 5.4 Mini",
		"gemini-3.1-pro":    "Gemini 3.1 Pro",
		"gemini-3-flash":    "Gemini 3 Flash",
		"grok-build-0.1":    "Grok Build 0.1",
		"kimi-k2.5":         "Kimi K2.5",
		"default":           "Default",
		"":                  "",
	}
	for id, want := range cases {
		assert.Equal(t, want, humanizeModelID(id), "humanizeModelID(%q)", id)
	}
}

// providerkit.EffortLabel is the one place a reasoning-effort id becomes text. Every
// provider reads it, so its fallback matters: a level a CLI adds mid-release
// must still read as a label rather than a raw lowercase token.
func TestEffortLabel(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Extra High", providerkit.EffortLabel("xhigh"))
	assert.Equal(t, "Ultra", providerkit.EffortLabel("ultra"))
	assert.Equal(t, "Ultracode", providerkit.EffortLabel("ultracode"))
	assert.Equal(t, "Auto", providerkit.EffortLabel(agent.EffortAuto))

	// Case-insensitive: a CLI reporting "High" or "XHIGH" still resolves.
	assert.Equal(t, "High", providerkit.EffortLabel("High"))
	assert.Equal(t, "Extra High", providerkit.EffortLabel("XHIGH"))

	// Unknown id: capitalized, never returned raw.
	assert.Equal(t, "Turbo", providerkit.EffortLabel("turbo"))
	assert.Equal(t, "", providerkit.EffortLabel(""))
}

// providerkit.EffortTier is the shorthand for a provider whose catalog carries no
// per-level description. It must never invent one, and never skip the label.
func TestEffortTier(t *testing.T) {
	t.Parallel()

	tier := providerkit.EffortTier("xhigh")
	assert.Equal(t, "xhigh", tier.Id)
	assert.Equal(t, "Extra High", tier.Name)
	assert.Empty(t, tier.Description)
}

// ClearContext replaces the session, so every note keyed by the OUTGOING
// session's tool-call ids must go with it. A surviving note would classify a
// backgrounded shell in the NEW session as a subagent, if that session ever
// reused the id.
//
// Driven through the real ClearContext rather than by calling the hook
// directly, because the wiring -- Cursor registering clearProviderState, and
// Base invoking it -- is the part that can silently go missing.
func TestCursorClearContextDropsTheTaskToolNotes(t *testing.T) {
	t.Parallel()

	ag, _ := newCursorAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId": "session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	ag.HooksForTest().SubagentFromToolCall = ag.spawnObservation
	ag.HooksForTest().SubagentFromToolCallUpdate = ag.finishedObservation
	ag.HooksForTest().ClearProviderState = ag.clearTaskToolCalls

	// A task tool call is in flight when the user clears the context.
	ag.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-task","kind":"other","title":"Task: go","rawInput":{"_toolName":"task"}}`))
	ag.Mu.Lock()
	before := len(ag.taskToolCalls)
	ag.Mu.Unlock()
	require.Equal(t, 1, before, "the in-flight task left a note")

	sessionID, clearErr := ag.ClearContext()
	require.NoError(t, clearErr)
	require.Equal(t, "session-2", sessionID)

	ag.Mu.Lock()
	after := len(ag.taskToolCalls)
	ag.Mu.Unlock()
	assert.Zero(t, after, "the outgoing session's notes went with it")
}

// The same clear must not run when the session was NOT replaced: a failed
// session/new leaves the old session live, and its in-flight task calls still
// report against the notes.
func TestCursorFailedClearContextKeepsTheTaskToolNotes(t *testing.T) {
	t.Parallel()

	ag, _ := newCursorAgentForRPC(t)
	ag.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	ag.HooksForTest().SubagentFromToolCall = ag.spawnObservation
	ag.HooksForTest().ClearProviderState = ag.clearTaskToolCalls
	ag.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-task","kind":"other","title":"Task: go","rawInput":{"_toolName":"task"}}`))

	// A cancelled context makes session/new fail, so the session survives.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ag.SetContextForTest(ctx)
	ag.SimulateExitForTest()

	_, clearErr := ag.ClearContext()
	require.Error(t, clearErr, "session/new must fail without a live agent")

	ag.Mu.Lock()
	after := len(ag.taskToolCalls)
	ag.Mu.Unlock()
	assert.Equal(t, 1, after, "the session was not replaced, so the note still applies")
}

// Cursor's registration hands the registry the same normalizer the live agent
// uses, which maps the wire "default" sentinel to "auto".
func TestCursorNormalizeModelIDFromRegistry(t *testing.T) {
	t.Parallel()
	registry := agenttest.MustNewRegistry(Registration())
	assert.Equal(t, "auto", registry.NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "default"))
}
