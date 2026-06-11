//go:build unix

package agent

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
)

func newCursorAgentForRPC(t *testing.T) (*CursorCLIAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *CursorCLIAgent {
			a := &CursorCLIAgent{}
			a.modeChannel = modeChannelPermissionMode
			a.modelIDNormalizer = normalizeCursorModelID
			a.modelSetter = a.setCursorModel
			return a
		},
		func(a *CursorCLIAgent) *acpBase { return &a.acpBase },
	)
}

func newCursorAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*CursorCLIAgent, func() []recordedRequest) {
	return newACPAgentForRPCWithResponder(t,
		func() *CursorCLIAgent {
			a := &CursorCLIAgent{}
			a.modeChannel = modeChannelPermissionMode
			a.modelIDNormalizer = normalizeCursorModelID
			a.modelSetter = a.setCursorModel
			return a
		},
		func(a *CursorCLIAgent) *acpBase { return &a.acpBase },
		respond,
	)
}

func installFakeCursorCLI(t *testing.T, scenario string) {
	installFakeACPCLI(t, fakeACPCLISpec{
		binary:      "cursor-agent",
		helperRun:   "TestHelperProcessCursorCLI",
		wantEnv:     "GO_WANT_HELPER_PROCESS_CURSOR",
		env:         []string{"LEAPMUX_CURSOR_TEST_SCENARIO=" + scenario},
		forwardArgs: true,
	})
}

func TestHelperProcessCursorCLI(*testing.T) {
	scenario := os.Getenv("LEAPMUX_CURSOR_TEST_SCENARIO")
	runFakeACPServer("GO_WANT_HELPER_PROCESS_CURSOR", func(method string) (string, bool, bool) {
		switch method {
		case acpMethodInitialize:
			return `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`, false, true
		case acpMethodSessionNew:
			return `{"sessionId":"cursor-new","models":{"currentModelId":"default[]","availableModels":[{"modelId":"default[]","name":"Auto"},{"modelId":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]},"modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]},"configOptions":[{"id":"mode","currentValue":"agent","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"},{"value":"ask","name":"Ask"}]},{"id":"model","currentValue":"default[]","options":[{"value":"default[]","name":"Auto"},{"value":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]}]}`, false, true
		case acpMethodSessionLoad:
			if scenario == "load" {
				return `{"models":{"currentModelId":"gpt-5.4[reasoning=medium]","availableModels":[{"modelId":"default[]","name":"Auto"},{"modelId":"gpt-5.4[reasoning=medium]","name":"GPT-5.4"}]},"modes":{"currentModeId":"plan","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]}}`, false, true
			}
			return "", false, false
		case acpMethodSessionSetConfigOption, acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
			return `{}`, false, true
		default:
			return "", false, false
		}
	})
}

func TestStartCursorCLI_NewSessionHandshake(t *testing.T) {
	installFakeCursorCLI(t, "new")

	provider, err := StartCursorCLI(context.Background(), Options{
		AgentID:       "cursor-new",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CursorCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "cursor-new", agent.sessionID)
	assert.Equal(t, "auto", agent.model)
	assert.Equal(t, CursorCLIModeAgent, agent.permissionMode)
	require.Len(t, agent.availableModels, 2)
	assert.Equal(t, "auto", agent.availableModels[0].GetId())
	groups := agent.OptionGroups()
	assert.Equal(t, "auto", optionids.CurrentValue(groups, OptionIDModel))
	require.NotNil(t, optionids.GroupByID(groups, OptionIDPermissionMode))
}

func TestStartCursorCLI_LoadSessionUsesResumeID(t *testing.T) {
	installFakeCursorCLI(t, "load")

	provider, err := StartCursorCLI(context.Background(), Options{
		AgentID:         "cursor-load",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "cursor-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CursorCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "cursor-resume", agent.sessionID)
	assert.Equal(t, "gpt-5.4[reasoning=medium]", agent.model)
	assert.Equal(t, CursorCLIModePlan, agent.permissionMode)
}

func TestCursorUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	agent, requests := newCursorAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CursorCLIModeAgent, Name: "Agent"},
		{Id: CursorCLIModePlan, Name: "Plan"},
	}

	updated := agent.UpdateSettings(map[string]string{
		OptionIDModel:          "auto",
		OptionIDPermissionMode: CursorCLIModePlan,
	})
	require.True(t, updated)
	assert.Equal(t, "auto", agent.model)
	assert.Equal(t, CursorCLIModePlan, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acpConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, cursorCLIModelAutoWire, recorded[0].Params["value"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, CursorCLIModePlan, recorded[1].Params["modeId"])
}

// TestCursorUpdateSettingsSkipsUnchangedModelAndMode verifies the redundant-re-push guard:
// the service hands UpdateSettings the FULL merged options map on every change, so when the
// requested model/mode already match the current selection (e.g. only another axis moved),
// no session/set_config_option (model) or session/set_mode RPC is issued.
func TestCursorUpdateSettingsSkipsUnchangedModelAndMode(t *testing.T) {
	agent, requests := newCursorAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CursorCLIModeAgent, Name: "Agent"},
		{Id: CursorCLIModePlan, Name: "Plan"},
	}
	// Seed the current selection as a real apply would: model stored in its normalized
	// form, mode at a concrete value.
	const requestedModel = "gpt-5.4[reasoning=medium]"
	agent.model = normalizeCursorModelID(requestedModel)
	agent.permissionMode = CursorCLIModePlan

	// Re-send the SAME model + mode.
	updated := agent.UpdateSettings(map[string]string{
		OptionIDModel:          requestedModel,
		OptionIDPermissionMode: CursorCLIModePlan,
	})
	require.True(t, updated)
	assert.Empty(t, requests(), "an unchanged model/mode issues no redundant set_config_option/set_mode RPC")
}

func TestCursorClearContextReappliesModelAndMode(t *testing.T) {
	agent, requests := newCursorAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{"sessionId":"session-2"}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "auto"
	agent.permissionMode = CursorCLIModePlan
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CursorCLIModeAgent, Name: "Agent"},
		{Id: CursorCLIModePlan, Name: "Plan"},
	}
	agent.sink = &testSink{}
	agent.reapplySettings = agent.reapplyModelAndSecondary

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "session-2", agent.sessionID)

	// Verify model is preserved with wire format conversion.
	assert.Equal(t, "auto", agent.model)

	recorded := requests()
	require.Len(t, recorded, 3)
	assert.Equal(t, acpMethodSessionNew, recorded[0].Method)
	assert.Equal(t, acpMethodSessionSetConfigOption, recorded[1].Method)
	assert.Equal(t, acpConfigOptionIDModel, recorded[1].Params["configId"])
	assert.Equal(t, cursorCLIModelAutoWire, recorded[1].Params["value"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[2].Method)
	assert.Equal(t, CursorCLIModePlan, recorded[2].Params["modeId"])
}

func TestBuildCursorCLIModelsNormalizesAuto(t *testing.T) {
	models := []acpModelInfo{
		{ModelID: cursorCLIModelAutoWire, Name: "Auto"},
		{ModelID: "gpt-5.4[reasoning=medium]", Name: "GPT-5.4"},
	}
	result := buildACPModels(models, cursorCLIModelAutoWire, normalizeCursorModelID)
	require.Len(t, result, 2)
	assert.Equal(t, "auto", result[0].Id)
	assert.True(t, result[0].IsDefault)
}

func TestCursorDefaultModelIsAuto(t *testing.T) {
	assert.Equal(t, "auto", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR))
}

// decorateCursorModel surfaces the effort/thinking/context metadata Cursor bakes into a
// model id's brackets (which the bare server-reported name omits) as the model's
// ContextWindow and Description, humanizes the bare-id display name, and keeps the wire id.
func TestDecorateCursorModel_ParsesBracketMetadata(t *testing.T) {
	m := &ModelInfo{Id: "claude-fable-5[thinking=true,context=300k,effort=high]", DisplayName: "claude-fable-5"}
	decorateCursorModel(m)

	assert.Equal(t, int64(300000), m.ContextWindow)
	assert.Contains(t, m.Description, "Extended thinking")
	assert.Contains(t, m.Description, "High effort")
	assert.Equal(t, "Claude Fable 5 High", m.DisplayName, "the humanized name carries the effort level")
	assert.Equal(t, "claude-fable-5[thinking=true,context=300k,effort=high]", m.Id, "the wire id keeps its brackets")
}

func TestDecorateCursorModel_ReasoningAndFastFlags(t *testing.T) {
	m := &ModelInfo{Id: "gpt-5.5[context=272k,reasoning=medium,fast=false]"}
	decorateCursorModel(m)

	assert.Equal(t, int64(272000), m.ContextWindow)
	assert.Equal(t, "GPT 5.5 Medium", m.DisplayName, "GPT's reasoning level is appended like effort")
	assert.Contains(t, m.Description, "Medium reasoning")
	assert.NotContains(t, m.Description, "Fast", "fast=false is omitted")

	fast := &ModelInfo{Id: "composer-2.5[fast=true]"}
	decorateCursorModel(fast)
	assert.Contains(t, fast.Description, "Fast")
	assert.Equal(t, "Composer 2.5 Fast", fast.DisplayName, "a fast-only variant is disambiguated by a Fast suffix")
}

// TestDecorateCursorModel_DisambiguatesThinkingFastVariants verifies that two variants of
// the same base model that differ only in their thinking/fast flag (no effort/reasoning)
// get distinct display names instead of colliding -- and that a real server-provided name
// is left untouched rather than double-suffixed.
func TestDecorateCursorModel_DisambiguatesThinkingFastVariants(t *testing.T) {
	thinking := &ModelInfo{Id: "claude-opus-4-8[thinking=true]"}
	decorateCursorModel(thinking)
	assert.Equal(t, "Claude Opus 4.8 Thinking", thinking.DisplayName)

	fast := &ModelInfo{Id: "claude-opus-4-8[fast=true]"}
	decorateCursorModel(fast)
	assert.Equal(t, "Claude Opus 4.8 Fast", fast.DisplayName)
	assert.NotEqual(t, thinking.DisplayName, fast.DisplayName, "variants must not collide")

	// A real server name already disambiguates; it must not gain a second "Fast".
	named := &ModelInfo{Id: "composer-2.5[fast=true]", DisplayName: "Composer 2.5 (Fast)"}
	decorateCursorModel(named)
	assert.Equal(t, "Composer 2.5 (Fast)", named.DisplayName)
}

// TestDecorateCursorModel_EffortLevelInName covers the effort-level suffix across the
// levels Cursor surfaces: xhigh cases as "XHigh", others just capitalize.
func TestDecorateCursorModel_EffortLevelInName(t *testing.T) {
	xhigh := &ModelInfo{Id: "claude-opus-4-7[thinking=true,context=300k,effort=xhigh,fast=false]"}
	decorateCursorModel(xhigh)
	assert.Equal(t, "Claude Opus 4.7 XHigh", xhigh.DisplayName)

	opus := &ModelInfo{Id: "claude-opus-4-8[thinking=true,context=300k,effort=high,fast=false]"}
	decorateCursorModel(opus)
	assert.Equal(t, "Claude Opus 4.8 High", opus.DisplayName)
}

// TestDecorateCursorModel_EffortAndReasoningCollapseToOne is the [G4/S11] guard: "effort"
// (Claude) and "reasoning" (GPT) are the same concept (cursorReasoningLevel), so a model that
// reports BOTH renders the level ONCE in the Description -- effort-preferred -- matching the
// single level the name suffix uses, rather than disagreeing ("High effort · Medium reasoning"
// in the tooltip vs just "High" in the name).
func TestDecorateCursorModel_EffortAndReasoningCollapseToOne(t *testing.T) {
	m := &ModelInfo{Id: "weird-model[effort=high,reasoning=medium]"}
	decorateCursorModel(m)
	assert.Contains(t, m.Description, "High effort")
	assert.NotContains(t, m.Description, "Medium reasoning",
		"the tooltip shows the level once (effort-preferred), matching the name suffix")
	assert.Contains(t, m.DisplayName, "High")
	assert.NotContains(t, m.DisplayName, "Medium", "the name suffix likewise collapses to the single level")
}

func TestDecorateCursorModel_NoBracketOrEmptyAddsNoMetadata(t *testing.T) {
	// A bracketless / empty-bracket id carries no metadata, so ContextWindow and
	// Description are left alone -- but the bare-id display name is still humanized.
	plain := &ModelInfo{Id: "auto", DisplayName: "Auto", Description: "Automatically selects"}
	decorateCursorModel(plain)
	assert.Equal(t, int64(0), plain.ContextWindow)
	assert.Equal(t, "Automatically selects", plain.Description, "a bracketless id adds no metadata")
	assert.Equal(t, "Auto", plain.DisplayName, "a real server name (Auto) is preserved")

	empty := &ModelInfo{Id: "gemini-3.1-pro[]", DisplayName: "gemini-3.1-pro"}
	decorateCursorModel(empty)
	assert.Empty(t, empty.Description, "an empty bracket adds no metadata")
	assert.Equal(t, "Gemini 3.1 Pro", empty.DisplayName, "but the bare-id name is humanized even for empty brackets")
}

// TestDecorateCursorModel_PreservesRealServerName verifies a genuinely friendly server
// name (not equal to the bare id) is kept rather than re-humanized.
func TestDecorateCursorModel_PreservesRealServerName(t *testing.T) {
	m := &ModelInfo{Id: "composer-2.5[fast=true]", DisplayName: "Composer 2.5 (Fast)"}
	decorateCursorModel(m)
	assert.Equal(t, "Composer 2.5 (Fast)", m.DisplayName, "a real server name is left untouched")
}

// TestHumanizeModelID covers the Cursor model ids surfaced by the live ACP server (whose
// `name` is the bare bracket-less id), confirming friendly display names.
func TestHumanizeModelID(t *testing.T) {
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
