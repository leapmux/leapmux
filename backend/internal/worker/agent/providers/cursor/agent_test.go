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
			return `{"sessionId":"cursor-new","models":{"currentModelId":"default[]","availableModels":[{"modelId":"default[]","name":"Auto"},{"modelId":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]},"modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]},"configOptions":[{"id":"mode","currentValue":"agent","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"},{"value":"ask","name":"Ask"}]},{"id":"model","currentValue":"default[]","options":[{"value":"default[]","name":"Auto"},{"value":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]}]}`, false, true
		case acp.MethodSessionLoad:
			if scenario == "load" {
				return `{"models":{"currentModelId":"gpt-5.4[reasoning=medium]","availableModels":[{"modelId":"default[]","name":"Auto"},{"modelId":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]},"modes":{"currentModeId":"plan","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]}}`, false, true
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
	installFakeCursorCLI(t, "load")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:         "cursor-load",
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
	assert.Equal(t, "gpt-5.4[reasoning=medium]", agent.ModelForTest())
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
	const requestedModel = "gpt-5.4[reasoning=medium]"
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
		{ModelID: "gpt-5.4[reasoning=medium]", Name: "GPT-5.4"},
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

// decorateCursorModel surfaces the effort/thinking/context metadata Cursor bakes into a
// model id's brackets (which the bare server-reported name omits) as the model's
// ContextWindow and Description, humanizes the bare-id display name, and keeps the wire id.
func TestDecorateCursorModel_ParsesBracketMetadata(t *testing.T) {
	t.Parallel()

	m := &agent.ModelInfo{Id: "claude-fable-5[thinking=true,context=300k,effort=high]", DisplayName: "claude-fable-5"}
	decorateCursorModel(m)

	assert.Equal(t, int64(300000), m.ContextWindow)
	assert.Contains(t, m.Description, "Extended thinking")
	assert.Contains(t, m.Description, "High effort")
	assert.Equal(t, "Claude Fable 5 High", m.DisplayName, "the humanized name carries the effort level")
	assert.Equal(t, "claude-fable-5[thinking=true,context=300k,effort=high]", m.Id, "the wire id keeps its brackets")
}

func TestDecorateCursorModel_ReasoningAndFastFlags(t *testing.T) {
	t.Parallel()

	m := &agent.ModelInfo{Id: "gpt-5.5[context=272k,reasoning=medium,fast=false]"}
	decorateCursorModel(m)

	assert.Equal(t, int64(272000), m.ContextWindow)
	assert.Equal(t, "GPT 5.5 Medium", m.DisplayName, "GPT's reasoning level is appended like effort")
	assert.Contains(t, m.Description, "Medium reasoning")
	assert.NotContains(t, m.Description, "Fast", "fast=false is omitted")

	fast := &agent.ModelInfo{Id: "composer-2.5[fast=true]"}
	decorateCursorModel(fast)
	assert.Contains(t, fast.Description, "Fast")
	assert.Equal(t, "Composer 2.5 Fast", fast.DisplayName, "a fast-only variant is disambiguated by a Fast suffix")
}

// TestDecorateCursorModel_DisambiguatesThinkingFastVariants verifies that two variants of
// the same base model that differ only in their thinking/fast flag (no effort/reasoning)
// get distinct display names instead of colliding -- and that a real server-provided name
// is left untouched rather than double-suffixed.
func TestDecorateCursorModel_DisambiguatesThinkingFastVariants(t *testing.T) {
	t.Parallel()

	thinking := &agent.ModelInfo{Id: "claude-opus-4-8[thinking=true]"}
	decorateCursorModel(thinking)
	assert.Equal(t, "Claude Opus 4.8 Thinking", thinking.DisplayName)

	fast := &agent.ModelInfo{Id: "claude-opus-4-8[fast=true]"}
	decorateCursorModel(fast)
	assert.Equal(t, "Claude Opus 4.8 Fast", fast.DisplayName)
	assert.NotEqual(t, thinking.DisplayName, fast.DisplayName, "variants must not collide")

	// A real server name already disambiguates; it must not gain a second "Fast".
	named := &agent.ModelInfo{Id: "composer-2.5[fast=true]", DisplayName: "Composer 2.5 (Fast)"}
	decorateCursorModel(named)
	assert.Equal(t, "Composer 2.5 (Fast)", named.DisplayName)
}

// TestDecorateCursorModel_EffortLevelInName covers the effort-level suffix across
// the levels Cursor surfaces. The suffix reads the SHARED label table, so an id
// spells the same here as it does in every other provider's effort picker --
// "xhigh" used to render "XHigh" only in this one place.
func TestDecorateCursorModel_EffortLevelInName(t *testing.T) {
	t.Parallel()

	xhigh := &agent.ModelInfo{Id: "claude-opus-4-7[thinking=true,context=300k,effort=xhigh,fast=false]"}
	decorateCursorModel(xhigh)
	assert.Equal(t, "Claude Opus 4.7 "+providerkit.EffortLabel(agent.EffortXHigh), xhigh.DisplayName)
	assert.Equal(t, "Claude Opus 4.7 Extra High", xhigh.DisplayName)

	opus := &agent.ModelInfo{Id: "claude-opus-4-8[thinking=true,context=300k,effort=high,fast=false]"}
	decorateCursorModel(opus)
	assert.Equal(t, "Claude Opus 4.8 High", opus.DisplayName)

	// An id no provider declares still reads as a label, not a raw token.
	unknown := &agent.ModelInfo{Id: "some-model[thinking=true,context=300k,effort=turbo,fast=false]"}
	decorateCursorModel(unknown)
	assert.Equal(t, "Some Model Turbo", unknown.DisplayName)
}

// TestDecorateCursorModel_EffortAndReasoningCollapseToOne is the [G4/S11] guard: "effort"
// (Claude) and "reasoning" (GPT) are the same concept (cursorReasoningLevel), so a model that
// reports BOTH renders the level ONCE in the Description -- effort-preferred -- matching the
// single level the name suffix uses, rather than disagreeing ("High effort · Medium reasoning"
// in the tooltip vs just "High" in the name).
func TestDecorateCursorModel_EffortAndReasoningCollapseToOne(t *testing.T) {
	t.Parallel()

	m := &agent.ModelInfo{Id: "weird-model[effort=high,reasoning=medium]"}
	decorateCursorModel(m)
	assert.Contains(t, m.Description, "High effort")
	assert.NotContains(t, m.Description, "Medium reasoning",
		"the tooltip shows the level once (effort-preferred), matching the name suffix")
	assert.Contains(t, m.DisplayName, "High")
	assert.NotContains(t, m.DisplayName, "Medium", "the name suffix likewise collapses to the single level")
}

// TestDecorateCursorModel_ReasoningEffortKey covers the THIRD spelling Cursor uses for
// the same concept. Its catalogue reports a level under "effort" (Claude), "reasoning"
// (GPT) and "reasoning_effort" (Grok). The last one used to parse as no level at all, so
// every Grok variant humanized to the same bare name -- "Grok 4.7" four times over, which
// is the collision cursorModelNameSuffix exists to prevent.
func TestDecorateCursorModel_ReasoningEffortKey(t *testing.T) {
	t.Parallel()

	low := &agent.ModelInfo{Id: "grok-4.7[context=256k,reasoning_effort=low,fast=false]"}
	decorateCursorModel(low)
	assert.Equal(t, int64(256000), low.ContextWindow)
	assert.Equal(t, "Grok 4.7 Low", low.DisplayName)
	assert.Contains(t, low.Description, "Low effort", "reasoning_effort reads as an effort level")

	high := &agent.ModelInfo{Id: "grok-4.7[context=500k,reasoning_effort=xhigh,fast=true]"}
	decorateCursorModel(high)
	assert.Equal(t, "Grok 4.7 "+providerkit.EffortLabel(agent.EffortXHigh), high.DisplayName)
	assert.NotEqual(t, low.DisplayName, high.DisplayName, "variants must not collide")
	assert.Contains(t, high.Description, "Fast")

	// The three keys stay one concept: a model that reports more than one renders the
	// level ONCE, and the name suffix agrees with the tooltip.
	both := &agent.ModelInfo{Id: "weird-model[effort=high,reasoning_effort=low]"}
	decorateCursorModel(both)
	assert.Contains(t, both.Description, "High effort")
	assert.NotContains(t, both.Description, "Low effort", "effort wins over reasoning_effort")
	assert.Contains(t, both.DisplayName, "High")
	assert.NotContains(t, both.DisplayName, "Low")
}

func TestDecorateCursorModel_NoBracketOrEmptyAddsNoMetadata(t *testing.T) {
	t.Parallel()

	// A bracketless / empty-bracket id carries no metadata, so ContextWindow and
	// Description are left alone -- but the bare-id display name is still humanized.
	plain := &agent.ModelInfo{Id: "auto", DisplayName: "Auto", Description: "Automatically selects"}
	decorateCursorModel(plain)
	assert.Equal(t, int64(0), plain.ContextWindow)
	assert.Equal(t, "Automatically selects", plain.Description, "a bracketless id adds no metadata")
	assert.Equal(t, "Auto", plain.DisplayName, "a real server name (Auto) is preserved")

	empty := &agent.ModelInfo{Id: "gemini-3.1-pro[]", DisplayName: "gemini-3.1-pro"}
	decorateCursorModel(empty)
	assert.Empty(t, empty.Description, "an empty bracket adds no metadata")
	assert.Equal(t, "Gemini 3.1 Pro", empty.DisplayName, "but the bare-id name is humanized even for empty brackets")
}

// TestDecorateCursorModel_PreservesRealServerName verifies a genuinely friendly server
// name (not equal to the bare id) is kept rather than re-humanized.
func TestDecorateCursorModel_PreservesRealServerName(t *testing.T) {
	t.Parallel()

	m := &agent.ModelInfo{Id: "composer-2.5[fast=true]", DisplayName: "Composer 2.5 (Fast)"}
	decorateCursorModel(m)
	assert.Equal(t, "Composer 2.5 (Fast)", m.DisplayName, "a real server name is left untouched")
}

// TestHumanizeModelID covers the Cursor model ids surfaced by the live ACP server (whose
// `name` is the bare bracket-less id), confirming friendly display names.
func TestHumanizeModelID(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"composer-2.5[fast=true]":                                 "Composer 2.5",
		"claude-opus-4-8[thinking=true,context=300k,effort=high]": "Claude Opus 4.8",
		"claude-fable-5[thinking=true]":                           "Claude Fable 5",
		"claude-sonnet-4-6[context=200k]":                         "Claude Sonnet 4.6",
		"gpt-5.5[context=272k]":                                   "GPT 5.5",
		"gpt-5.3-codex[reasoning=medium]":                         "GPT 5.3 Codex",
		"gpt-5.1-codex-max[reasoning=medium]":                     "GPT 5.1 Codex Max",
		"gpt-5.4-mini[reasoning=medium]":                          "GPT 5.4 Mini",
		"gemini-3.1-pro[]":                                        "Gemini 3.1 Pro",
		"gemini-3-flash[]":                                        "Gemini 3 Flash",
		"grok-build-0.1[context=200k]":                            "Grok Build 0.1",
		"kimi-k2.5[]":                                             "Kimi K2.5",
		"default[]":                                               "Default",
		"":                                                        "",
	}
	for id, want := range cases {
		assert.Equal(t, want, humanizeModelID(id), "humanizeModelID(%q)", id)
	}
}

func TestParseCursorContextWindow(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(300000), parseCursorContextWindow("300k"))
	assert.Equal(t, int64(272000), parseCursorContextWindow("272K"))
	assert.Equal(t, int64(1_000_000), parseCursorContextWindow("1m"))
	assert.Equal(t, int64(200000), parseCursorContextWindow("200000"))
	assert.Equal(t, int64(0), parseCursorContextWindow(""))
	assert.Equal(t, int64(0), parseCursorContextWindow("huge"))
	// strconv.ParseFloat accepts "inf"/"nan"; converting those to int64 is
	// implementation-defined, so they must be rejected (0), not surfaced as a
	// garbage context window. The "k"/"m" suffix strip also routes "infk" -> "inf".
	assert.Equal(t, int64(0), parseCursorContextWindow("inf"))
	assert.Equal(t, int64(0), parseCursorContextWindow("Inf"))
	assert.Equal(t, int64(0), parseCursorContextWindow("nan"))
	assert.Equal(t, int64(0), parseCursorContextWindow("-1"))
	assert.Equal(t, int64(0), parseCursorContextWindow("infk"))
	// A finite but out-of-int64-range value (after the multiplier) converts to an
	// implementation-defined garbage int64 (saturates to MaxInt64 on arm64, wraps to
	// MinInt64 on amd64), so it must be rejected (0) rather than surfacing garbage.
	assert.Equal(t, int64(0), parseCursorContextWindow("99999999999999999999k"))
	assert.Equal(t, int64(0), parseCursorContextWindow("1e30"))
	assert.Equal(t, int64(0), parseCursorContextWindow("99999999999999999999999m"))
	// Just below the int64 ceiling still converts (no overflow).
	assert.Equal(t, int64(9_000_000_000_000_000_000), parseCursorContextWindow("9000000000000000000"))
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

// Cursor reads its effort out of its model ids, and labels it from the shared
// table too.
func TestCursorEffortLabelUsesTheSharedTable(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Extra High", cursorEffortLabel("xhigh"))
}

// Cursor's registration hands the registry the same normalizer the live agent
// uses, which maps the wire "default[]" sentinel to "auto".
func TestCursorNormalizeModelIDFromRegistry(t *testing.T) {
	t.Parallel()
	registry := agenttest.MustNewRegistry(Registration())
	assert.Equal(t, "auto", registry.NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "default[]"))
}
