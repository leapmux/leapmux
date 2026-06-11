//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

func newCopilotAgentForRPC(t *testing.T) (*CopilotCLIAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *CopilotCLIAgent {
			a := &CopilotCLIAgent{}
			a.modeChannel = modeChannelPermissionMode
			return a
		},
		func(a *CopilotCLIAgent) *acpBase { return &a.acpBase },
	)
}

func newCopilotAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*CopilotCLIAgent, func() []recordedRequest) {
	return newACPAgentForRPCWithResponder(t,
		func() *CopilotCLIAgent {
			a := &CopilotCLIAgent{}
			a.modeChannel = modeChannelPermissionMode
			return a
		},
		func(a *CopilotCLIAgent) *acpBase { return &a.acpBase },
		respond,
	)
}

func installFakeCopilotCLI(t *testing.T, scenario string) {
	installFakeACPCLI(t, fakeACPCLISpec{
		binary:    "copilot",
		helperRun: "TestHelperProcessCopilotCLI",
		wantEnv:   "GO_WANT_HELPER_PROCESS_COPILOT",
		env:       []string{"LEAPMUX_COPILOT_TEST_SCENARIO=" + scenario},
	})
}

func TestHelperProcessCopilotCLI(*testing.T) {
	scenario := os.Getenv("LEAPMUX_COPILOT_TEST_SCENARIO")
	runFakeACPServer("GO_WANT_HELPER_PROCESS_COPILOT", func(method string) (string, bool, bool) {
		switch method {
		case acpMethodInitialize:
			return `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`, false, true
		case acpMethodSessionNew:
			if scenario == "generic-option" {
				// A spec-compliant agent reporting a third axis (thought_level) the
				// model/mode channels do not claim. It must surface as a mutable
				// option group alongside the mapped permission-mode group.
				return `{"sessionId":"copilot-new","models":{"currentModelId":"gpt-5.4","availableModels":[{"modelId":"gpt-5.4","name":"GPT-5.4"}]},"modes":{"currentModeId":"https://agentclientprotocol.com/protocol/session-modes#agent","availableModes":[{"id":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"id":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#agent","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`, false, true
			}
			return `{"sessionId":"copilot-new","models":{"currentModelId":"gpt-5.4","availableModels":[{"modelId":"gpt-5.4","name":"GPT-5.4","description":"Full"},{"modelId":"gpt-5.4-mini","name":"GPT-5.4 mini","description":"Mini"}]},"modes":{"currentModeId":"https://agentclientprotocol.com/protocol/session-modes#agent","availableModes":[{"id":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"id":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#agent","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"model","currentValue":"gpt-5.4","options":[{"value":"gpt-5.4","name":"GPT-5.4"},{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}`, false, true
		case acpMethodSessionLoad:
			if scenario == "load" {
				return `{"models":{"currentModelId":"gpt-5.4-mini","availableModels":[{"modelId":"gpt-5.4-mini","name":"GPT-5.4 mini"}]},"modes":{"currentModeId":"https://agentclientprotocol.com/protocol/session-modes#plan","availableModes":[{"id":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"id":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]}}`, false, true
			}
			return "", false, false
		case acpMethodSessionSetConfigOption, acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
			return `{}`, false, true
		default:
			return "", false, false
		}
	})
}

func TestBuildCopilotSessionRequest_NewSession(t *testing.T) {
	method, params := buildACPSessionRequest("", "/workspace", acpMethodSessionNew, acpMethodSessionLoad)
	assert.Equal(t, acpMethodSessionNew, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestBuildCopilotSessionRequest_LoadSession(t *testing.T) {
	method, params := buildACPSessionRequest("session-123", "/workspace", acpMethodSessionNew, acpMethodSessionLoad)
	assert.Equal(t, acpMethodSessionLoad, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestStartCopilotCLI_NewSessionHandshake(t *testing.T) {
	installFakeCopilotCLI(t, "new")

	provider, err := StartCopilotCLI(context.Background(), Options{
		AgentID:       "copilot-new",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CopilotCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "copilot-new", agent.sessionID)
	assert.Equal(t, "gpt-5.4", agent.model)
	assert.Equal(t, CopilotCLIModeAgent, agent.permissionMode)
	require.Len(t, agent.availableModels, 2)
	assert.Equal(t, "gpt-5.4", agent.availableModels[0].GetId())
	groups := agent.OptionGroups()
	assert.Equal(t, "gpt-5.4", optionids.CurrentValue(groups, OptionIDModel))
	require.NotNil(t, optionids.GroupByID(groups, OptionIDPermissionMode))
}

// End-to-end: a handshake reporting an unmapped config option (a third axis the
// model/mode channels don't claim) surfaces it as a mutable option group after
// the mapped permission-mode group, and its current value rides in CurrentSettings
// extras. This exercises the permission-mode AvailableOptionGroups path; OpenCode
// covers the primary-agent path.
func TestStartCopilotCLI_HandshakeSurfacesConfigOption(t *testing.T) {
	installFakeCopilotCLI(t, "generic-option")

	provider, err := StartCopilotCLI(context.Background(), Options{
		AgentID:       "copilot-generic",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CopilotCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	groups := agent.OptionGroups()
	assert.NotNil(t, optionids.GroupByID(groups, OptionIDPermissionMode))
	generic := optionids.GroupByID(groups, "thoughtLevel")
	require.NotNil(t, generic)
	assert.Equal(t, "Thought Level", generic.GetLabel())
	require.Len(t, generic.GetOptions(), 2)

	// The current option value rides in CurrentOptions.
	assert.Equal(t, "high", CurrentOptions(groups)["thoughtLevel"])
}

// The base UpdateSettings (permission-mode providers) reads only model and
// permissionMode, so an unknown extras key (a future config-option axis) is structurally
// ignored: model + mode RPCs fire, nothing else, and the write succeeds.
func TestUpdateSettings_IgnoresUnknownExtraKey(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent"},
		{Id: CopilotCLIModePlan, Name: "Plan"},
	}

	ok := agent.UpdateSettings(map[string]string{
		OptionIDModel:          "gpt-5.4",
		OptionIDPermissionMode: CopilotCLIModePlan,
		"thoughtLevel":         "high",
	})

	require.True(t, ok)
	recorded := requests()
	require.Len(t, recorded, 2, "model + mode RPCs fire; the unknown extra key sends nothing")
	assert.Equal(t, acpMethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acpConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
}

// End-to-end S1: the handshake reports models through the SessionModelState
// `models` field. A later runtime config_option_update carries only the
// configOptions `model` select (a subset). The models-field-only entry must
// survive -- applyConfigOptionModelsLocked re-unions the remembered catalog.
func TestStartCopilotCLI_RuntimeConfigOptionPreservesModelsFieldCatalog(t *testing.T) {
	installFakeCopilotCLI(t, "new")

	provider, err := StartCopilotCLI(context.Background(), Options{
		AgentID:       "copilot-reunion",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CopilotCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	// Handshake surfaced both models (gpt-5.4 lives in the models field).
	require.Len(t, agent.availableModels, 2)

	// A runtime update whose model select lists only gpt-5.4-mini.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"copilot-reunion","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"gpt-5.4-mini","options":[{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, "gpt-5.4-mini", agent.model, "the config-option current becomes the model")
	ids := map[string]bool{}
	for _, m := range agent.availableModels {
		ids[m.GetId()] = true
	}
	assert.True(t, ids["gpt-5.4"], "the models-field-only model survives the runtime config-option update")
	assert.True(t, ids["gpt-5.4-mini"])
}

func TestStartCopilotCLI_LoadSessionUsesResumeID(t *testing.T) {
	installFakeCopilotCLI(t, "load")

	provider, err := StartCopilotCLI(context.Background(), Options{
		AgentID:         "copilot-load",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "copilot-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*CopilotCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "copilot-resume", agent.sessionID)
	assert.Equal(t, "gpt-5.4-mini", agent.model)
	assert.Equal(t, CopilotCLIModePlan, agent.permissionMode)
}

func TestCopilotUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent"},
		{Id: CopilotCLIModePlan, Name: "Plan"},
	}

	updated := agent.UpdateSettings(map[string]string{
		OptionIDModel:          "gpt-5.4-mini",
		OptionIDPermissionMode: CopilotCLIModePlan,
	})
	require.True(t, updated)
	assert.Equal(t, "gpt-5.4-mini", agent.model)
	assert.Equal(t, CopilotCLIModePlan, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acpConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, "gpt-5.4-mini", recorded[0].Params["value"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, CopilotCLIModePlan, recorded[1].Params["modeId"])
}

func TestCopilotCancelSessionSendsACPMethod(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)

	require.NoError(t, agent.cancelSession())
	testutil.AssertEventually(t, func() bool {
		recorded := requests()
		return len(recorded) == 1 && recorded[0].Method == acpMethodSessionCancel
	}, "expected session/cancel notification to be recorded")
}

func TestCopilotAvailableOptionGroupsFallsBack(t *testing.T) {
	// configure sets both the channel and the static fallback list; OptionGroups serves
	// that fallback before the session reports a permission-mode catalog.
	agent := &CopilotCLIAgent{acpBase: acpBase{
		modeChannel:       modeChannelPermissionMode,
		secondaryFallback: fallbackCopilotCLIModes(),
	}}

	groups := agent.OptionGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "permissionMode", groups[0].GetId())
	assert.Equal(t, CopilotCLIModeAgent, groups[0].GetOptions()[0].GetId())
}

func TestDefaultModel_CopilotUsesEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_COPILOT_DEFAULT_MODEL", "gpt-5.4-mini")
	assert.Equal(t, "gpt-5.4-mini", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT))
}

// applyStartupPermissionMode pushes a requested mode that differs from the
// server's current, is a no-op when empty or already matching, and propagates a
// rejection as a fatal error. The current mode is read under the lock (it shares
// the field the reader goroutine writes), mirroring trySetStartupModel.

func TestApplyStartupPermissionMode_PushesWhenDiffers(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)
	agent.permissionMode = CopilotCLIModeAgent

	require.NoError(t, agent.applyStartupPermissionMode(CopilotCLIModePlan))

	assert.Equal(t, CopilotCLIModePlan, agent.permissionMode)
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, acpMethodSessionSetMode, recorded[0].Method)
	assert.Equal(t, CopilotCLIModePlan, recorded[0].Params["modeId"])
}

func TestApplyStartupPermissionMode_NoopWhenMatchesCurrent(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)
	agent.permissionMode = CopilotCLIModePlan

	require.NoError(t, agent.applyStartupPermissionMode(CopilotCLIModePlan))

	assert.Empty(t, requests(), "no set_mode when the request already matches the server's current")
}

func TestApplyStartupPermissionMode_NoopWhenEmpty(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)
	agent.permissionMode = CopilotCLIModeAgent

	require.NoError(t, agent.applyStartupPermissionMode(""))

	assert.Empty(t, requests(), "an empty requested mode is a no-op")
}

func TestApplyStartupPermissionMode_RejectionIsFatal(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionSetMode {
			return json.RawMessage(`{"code":-32602,"message":"unknown mode"}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.permissionMode = CopilotCLIModeAgent

	err := agent.applyStartupPermissionMode(CopilotCLIModePlan)
	require.Error(t, err, "a rejected mode must surface so the caller aborts startup")
}

// seedReasoningEffort surfaces a Copilot-shaped reasoning_effort config option, as a
// handshake would, returning the agent ready for a settings write.
func seedReasoningEffort(agent *CopilotCLIAgent, current string) {
	agent.mu.Lock()
	agent.applyOptionGroupsLocked([]acpConfigOption{{
		ID:           "reasoning_effort",
		Category:     "thought_level",
		Name:         "Reasoning Effort",
		CurrentValue: current,
		Options: []acpConfigOptionValue{
			{Value: "low", Name: "low"}, {Value: "medium", Name: "medium"}, {Value: "high", Name: "high"},
		},
	}})
	agent.mu.Unlock()
}

// TestSetConfigOptionGuarded_PreconditionGatesWrite is the [S2] guard for the tightened
// raiseEffortOffNone race window: the optional precondition is evaluated under the WRITE's own
// b.mu acquisition (the latest point before the send), so a write predicated on stale state can be
// skipped without holding the lock across the RPC. A false precondition must NOT send a
// session/set_config_option RPC (no-op success); a true one must.
func TestSetConfigOptionGuarded_PreconditionGatesWrite(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionSetConfigOption {
			return json.RawMessage(`{"configOptions":[{"id":"reasoning_effort","category":"thought_level","currentValue":"low","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.sink = &testSink{}
	seedReasoningEffort(agent, "high") // marks reasoning_effort known + offered (low/medium/high)

	effortWrites := func() int {
		n := 0
		for _, r := range requests() {
			if r.Method == acpMethodSessionSetConfigOption && r.Params["configId"] == "reasoning_effort" {
				n++
			}
		}
		return n
	}

	// A false precondition skips the write entirely.
	require.NoError(t, agent.setConfigOptionGuarded("reasoning_effort", "low", func() bool { return false }))
	assert.Equal(t, 0, effortWrites(), "a write predicated on a false precondition is skipped")

	// A true precondition lets the write through.
	require.NoError(t, agent.setConfigOptionGuarded("reasoning_effort", "low", func() bool { return true }))
	assert.Equal(t, 1, effortWrites(), "a write predicated on a true precondition is sent")
}

// TestACPConfigOption_MutableUpdateRoundTrips verifies a config option
// (Copilot's reasoning_effort) is surfaced mutable, and a settings change is written
// via session/set_config_option with the new value adopted from the response.
func TestACPConfigOption_MutableUpdateRoundTrips(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionSetConfigOption {
			return json.RawMessage(`{"configOptions":[
				{"id":"reasoning_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"medium","name":"medium"},{"value":"high","name":"high"}]}
			]}`)
		}
		return json.RawMessage(`{}`)
	})
	sink := &testSink{}
	agent.sink = sink
	seedReasoningEffort(agent, "medium")

	// Surfaced mutable, at its server-reported current.
	g := optionids.GroupByID(agent.OptionGroups(), "reasoning_effort")
	require.NotNil(t, g)
	assert.True(t, g.GetMutable(), "a config option is now writable")
	assert.Equal(t, "medium", g.GetCurrentValue())

	// Changing it routes through session/set_config_option and adopts the response value.
	ok := agent.UpdateSettings(map[string]string{"reasoning_effort": "high"})
	assert.True(t, ok)

	var setReq *recordedRequest
	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "UpdateSettings sent session/set_config_option")
	assert.Equal(t, "reasoning_effort", setReq.Params["configId"])
	assert.Equal(t, "high", setReq.Params["value"])

	assert.Equal(t, "high", optionids.GroupByID(agent.OptionGroups(), "reasoning_effort").GetCurrentValue(),
		"the new value is adopted from the set_config_option response")

	// A pure current-value change keeps the same option set, so it must NOT broadcast a catalog
	// refresh -- the new value rides the settings reply; broadcasting here would fire a redundant
	// statusChange on every effort/mode edit.
	assert.Equal(t, 0, sink.StatusActiveCount(),
		"a value-only change must not broadcast a catalog refresh")
}

// TestACPConfigOption_SkipsUnofferedValue is the regression guard for the stale-tier push: a
// settings write whose value the current option list does NOT offer (e.g. an effort tier
// inherited from a prior model that the new model dropped) is SKIPPED rather than force-pushed,
// so the daemon never sees a value it would reject and bounce UpdateSettings into a relaunch.
func TestACPConfigOption_SkipsUnofferedValue(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})
	agent.sink = &testSink{}
	// The current model offers only low/medium/high.
	seedReasoningEffort(agent, "medium")

	// A change to "xhigh" -- a tier the current list does not offer.
	ok := agent.UpdateSettings(map[string]string{"reasoning_effort": "xhigh"})
	assert.True(t, ok, "skipping an unoffered value is a no-op success, not a failure")

	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption && r.Params["configId"] == "reasoning_effort" {
			t.Fatalf("an unoffered value must not be pushed to the daemon, got value=%v", r.Params["value"])
		}
	}
	assert.Equal(t, "medium", optionids.GroupByID(agent.OptionGroups(), "reasoning_effort").GetCurrentValue(),
		"the running session keeps its actual value when an unoffered write is skipped")
}

// TestCopilotModelChangeSurfacesReasoningEffort is the Copilot parity for
// TestStartOpenCode_ModelChangeSurfacesEffort: switching to a reasoning-capable model must
// surface the daemon's reasoning_effort group. Copilot, like OpenCode, returns the refreshed
// configOptions from session/set_config_option (configId "model") but emits no
// config_option_update notification, so the live model write must BOTH fold the response (so the
// group exists in agent state) AND broadcast a status refresh (so the new group reaches the
// settings panel, which rebuilds its catalog only from statusChange events).
func TestCopilotModelChangeSurfacesReasoningEffort(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionSetConfigOption {
			// The new model supports reasoning, so its refreshed configOptions now carry the
			// reasoning_effort axis (the prior model offered none).
			return json.RawMessage(`{"configOptions":[
				{"id":"reasoning_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"medium","options":[{"value":"low","name":"low"},{"value":"medium","name":"medium"},{"value":"high","name":"high"}]}
			]}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-5.4-mini"
	sink := &testSink{}
	agent.sink = sink

	// The prior model surfaced no reasoning_effort axis.
	require.Nil(t, optionids.GroupByID(agent.OptionGroups(), "reasoning_effort"),
		"the prior model surfaced no reasoning_effort group")

	// Switch to the reasoning-capable model.
	require.True(t, agent.UpdateSettings(map[string]string{OptionIDModel: "gpt-5.4"}))

	// The model write went through session/set_config_option (configId "model"), not set_model.
	var setReq *recordedRequest
	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "the model write goes through session/set_config_option")
	assert.Equal(t, acpConfigOptionIDModel, setReq.Params["configId"])
	assert.Equal(t, "gpt-5.4", setReq.Params["value"])

	// The refreshed configOptions surfaced reasoning_effort at its server-reported current.
	groups := agent.OptionGroups()
	effort := optionids.GroupByID(groups, "reasoning_effort")
	require.NotNil(t, effort, "reasoning_effort must surface after switching to a reasoning-capable model")
	require.Len(t, effort.GetOptions(), 3)
	assert.Equal(t, "medium", CurrentOptions(groups)["reasoning_effort"])

	// The option-group set changed, so a status refresh must broadcast the new catalog -- the
	// settings panel rebuilds its option groups only from statusChange events.
	assert.Equal(t, 1, sink.StatusActiveCount(),
		"a status refresh must broadcast the new reasoning_effort group to the frontend")
}

// TestACPConfigOption_EmptyResponseAdoptsWrittenValue is the regression guard for
// [E10]: a server that accepts session/set_config_option but echoes no refreshed
// configOptions (off-spec, but possible) must not leave the option at its stale prior value.
// The write succeeded, so the value we wrote is authoritative and is recorded optimistically
// -- otherwise applySettingsLive's readback would persist the stale value and revert the
// user's choice.
func TestACPConfigOption_EmptyResponseAdoptsWrittenValue(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(string) json.RawMessage {
		// Every method (incl. set_config_option) succeeds but returns no configOptions.
		return json.RawMessage(`{}`)
	})
	seedReasoningEffort(agent, "medium")

	ok := agent.UpdateSettings(map[string]string{"reasoning_effort": "high"})
	assert.True(t, ok)
	assert.Equal(t, "high", optionids.GroupByID(agent.OptionGroups(), "reasoning_effort").GetCurrentValue(),
		"the written value is adopted even when the response carries no configOptions")
}

// TestACPConfigOption_EffortSortedStrongestFirst verifies a thought_level
// (reasoning-effort) config option is reordered strongest-first, regardless of the
// weakest-first order the server reports.
func TestACPConfigOption_EffortSortedStrongestFirst(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(string) json.RawMessage { return json.RawMessage(`{}`) })
	agent.mu.Lock()
	agent.applyOptionGroupsLocked([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "medium",
		Options: []acpConfigOptionValue{
			{Value: "none"}, {Value: "low"}, {Value: "medium"}, {Value: "high"}, {Value: "xhigh"},
		},
	}})
	agent.mu.Unlock()

	g := optionids.GroupByID(agent.OptionGroups(), "reasoning_effort")
	require.NotNil(t, g)
	var ids []string
	for _, o := range g.GetOptions() {
		ids = append(ids, o.GetId())
	}
	assert.Equal(t, []string{"xhigh", "high", "medium", "low", "none"}, ids)
}

// TestSortEffortOptionsDescending_RanksUnrankedLast guards the degenerate path: a
// provider-specific variant the rank table doesn't know sorts after every ranked
// value, and -- crucially -- does NOT act as a barrier that leaves the ranked entries
// mis-ordered. With the old "incomparable" comparator [low, default, high] stayed put
// (low ahead of high); the strict-weak-ordering comparator now sorts the ranked pair.
func TestSortEffortOptionsDescending_RanksUnrankedLast(t *testing.T) {
	opts := []*leapmuxv1.AvailableOption{{Id: "low"}, {Id: "default"}, {Id: "high"}}
	sortEffortOptionsDescending(opts)
	ids := func() []string {
		out := make([]string, len(opts))
		for i, o := range opts {
			out[i] = o.GetId()
		}
		return out
	}()
	assert.Equal(t, []string{"high", "low", "default"}, ids,
		"ranked values sort strongest-first; the unranked value sorts last")
}

// TestSortEffortOptionsDescending_StableAmongUnranked verifies multiple unranked
// values keep their relative (server-reported) order while sorting after ranked ones.
func TestSortEffortOptionsDescending_StableAmongUnranked(t *testing.T) {
	opts := []*leapmuxv1.AvailableOption{{Id: "alpha"}, {Id: "high"}, {Id: "beta"}, {Id: "low"}}
	sortEffortOptionsDescending(opts)
	ids := make([]string, len(opts))
	for i, o := range opts {
		ids[i] = o.GetId()
	}
	assert.Equal(t, []string{"high", "low", "alpha", "beta"}, ids,
		"ranked strongest-first, then unranked in their original order")
}

// TestSortEffortOptionsDescending_CaseInsensitive verifies a server reporting effort ids
// in mixed case (e.g. "High"/"LOW") is still ranked rather than dumped into the unranked
// tail -- effortRankOf lowercases before the lookup.
func TestSortEffortOptionsDescending_CaseInsensitive(t *testing.T) {
	opts := []*leapmuxv1.AvailableOption{{Id: "LOW"}, {Id: "High"}, {Id: "Medium"}}
	sortEffortOptionsDescending(opts)
	ids := make([]string, len(opts))
	for i, o := range opts {
		ids[i] = o.GetId()
	}
	assert.Equal(t, []string{"High", "Medium", "LOW"}, ids,
		"mixed-case effort ids rank by intensity, preserving their original spelling")
}

// TestSortEffortOptionsDescending_KnownSynonyms verifies the common separator/spelling
// variants share a rank with their canonical name (e.g. "very_high" == "xhigh"), so a
// mid-tier synonym is not stranded after every ranked value.
func TestSortEffortOptionsDescending_KnownSynonyms(t *testing.T) {
	opts := []*leapmuxv1.AvailableOption{{Id: "moderate"}, {Id: "very_high"}, {Id: "minimal"}}
	sortEffortOptionsDescending(opts)
	ids := make([]string, len(opts))
	for i, o := range opts {
		ids[i] = o.GetId()
	}
	assert.Equal(t, []string{"very_high", "moderate", "minimal"}, ids,
		"synonyms (very_high~xhigh, moderate~medium) rank by intensity, not as unknowns")
}

// TestChooseDefaultEffort covers the value installed in place of a daemon's "none" default:
// "high" when offered, otherwise the offered level CLOSEST TO high with ties broken toward the
// stronger level. none/off and unranked provider-specific values are never chosen.
func TestChooseDefaultEffort(t *testing.T) {
	option := func(values ...string) acpConfigOption {
		o := acpConfigOption{Category: acpConfigOptionCategoryThoughtLevel}
		for _, v := range values {
			o.Options = append(o.Options, acpConfigOptionValue{Value: v})
		}
		return o
	}
	cases := []struct {
		name   string
		option acpConfigOption
		want   string
	}{
		{"high offered wins outright", option("low", "medium", "high"), "high"},
		{"no high: medium is closest", option("low", "medium"), "medium"},
		{"no high/medium: low is closest", option("minimal", "low"), "low"},
		{"none and off are never chosen", option("none", "off", "low", "medium", "high"), "high"},
		{"closest-to-high beats a farther level", option("low", "xhigh"), "xhigh"},                // low d2, xhigh d1
		{"equidistant ties toward the stronger level", option("low", "max"), "max"},               // both d2 -> higher
		{"synonyms rank like their canonical name", option("moderate", "very-high"), "very-high"}, // d1 each -> stronger
		{"only none/off yields no choice", option("none", "off"), ""},
		{"only unranked values yields no choice", option("default", "custom"), ""},
		{"empty option list yields no choice", option(), ""},
		{"case-insensitive", option("Low", "High"), "High"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, chooseDefaultEffort(tc.option))
		})
	}
}

// TestACPConfigOption_DedupsDuplicateIDs verifies a (non-conforming) server that
// reports the same config-option id twice surfaces a SINGLE group, not two sharing a key
// -- two groups with the same id corrupt the frontend's id-keyed <For> reconciliation.
func TestACPConfigOption_DedupsDuplicateIDs(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(string) json.RawMessage { return json.RawMessage(`{}`) })
	agent.mu.Lock()
	agent.applyOptionGroupsLocked([]acpConfigOption{
		{ID: "allow_all", Name: "Allow All", CurrentValue: "off", Options: []acpConfigOptionValue{{Value: "off"}, {Value: "on"}}},
		{ID: "allow_all", Name: "Allow All (dup)", CurrentValue: "on", Options: []acpConfigOptionValue{{Value: "off"}, {Value: "on"}}},
	})
	agent.mu.Unlock()

	groups := agent.OptionGroups()
	count := 0
	for _, g := range groups {
		if g.GetId() == "allow_all" {
			count++
		}
	}
	assert.Equal(t, 1, count, "a duplicate config-option id surfaces a single group")
	assert.Equal(t, "off", optionids.GroupByID(groups, "allow_all").GetCurrentValue(),
		"the first sighting wins")
}

// TestACPConfigOption_EmptyCurrentRecoverable verifies the empty-current recovery
// fix: an option the server advertises with an EMPTY current at handshake is not surfaced
// yet but IS recorded as known, so re-pushing its persisted preference via
// set_config_option is accepted (not rejected as "unknown config option") and surfaces it.
func TestACPConfigOption_EmptyCurrentRecoverable(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionSetConfigOption {
			return json.RawMessage(`{"configOptions":[{"id":"reasoning_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"high","name":"high"}]}]}`)
		}
		return json.RawMessage(`{}`)
	})
	// Handshake reports reasoning_effort with an empty current and nothing stored.
	agent.mu.Lock()
	agent.applyOptionGroupsLocked([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}})
	agent.mu.Unlock()

	assert.Nil(t, optionids.GroupByID(agent.OptionGroups(), "reasoning_effort"),
		"an option with an empty current is not surfaced until a concrete value is known")

	// Re-pushing the persisted preference is accepted (recorded as known) and surfaces it.
	require.NoError(t, agent.setConfigOption("reasoning_effort", "high"),
		"an empty-current option must be pushable so its persisted preference can be recovered")
	assert.Equal(t, "high", optionids.GroupByID(agent.OptionGroups(), "reasoning_effort").GetCurrentValue())
}

// TestStaticSecondaryGroup_CarriesDefault verifies the static fallback secondary group
// (served before the session reports its catalog) carries a DefaultValue (default-or-first
// option), matching the live secondaryOptionGroupLocked -- so a fresh tab shows a default
// badge instead of none until the handshake lands.
func TestStaticSecondaryGroup_CarriesDefault(t *testing.T) {
	groups := staticSecondaryGroup(modeChannelPermissionMode, []*leapmuxv1.AvailableOption{
		{Id: "default"}, {Id: "plan"},
	})
	require.Len(t, groups, 1)
	assert.Equal(t, OptionIDPermissionMode, groups[0].GetId())
	assert.Equal(t, "Mode", groups[0].GetLabel())
	assert.Equal(t, "default", groups[0].GetDefaultValue(),
		"the static fallback marks the default-or-first option as default")
}

// TestSecondaryOptionGroupLocked_DefaultIsProviderDefaultNotCurrent is the regression guard
// for the live secondary group's DefaultValue: it must mark the provider default (default-or-
// first option), NOT the user's current selection -- so the picker's default badge stays put
// instead of following the selection around, matching the static fallback
// (TestStaticSecondaryGroup_CarriesDefault).
func TestSecondaryOptionGroupLocked_DefaultIsProviderDefaultNotCurrent(t *testing.T) {
	agent, _ := newCopilotAgentForRPC(t)
	agent.mu.Lock()
	agent.availableModes = []*leapmuxv1.AvailableOption{{Id: "default"}, {Id: "plan"}}
	agent.permissionMode = "plan" // current selection differs from the default-or-first option
	g := agent.secondaryOptionGroupLocked()
	agent.mu.Unlock()

	require.NotNil(t, g)
	assert.Equal(t, "plan", g.GetCurrentValue(), "current reflects the live selection")
	assert.Equal(t, "default", g.GetDefaultValue(),
		"default marks the provider default (first option), not the current selection")
}

// TestACPConfigOption_NoopWhenUnchanged verifies UpdateSettings does not write a
// config option whose value already matches the current selection.
func TestACPConfigOption_NoopWhenUnchanged(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})
	seedReasoningEffort(agent, "medium")

	require.True(t, agent.UpdateSettings(map[string]string{"reasoning_effort": "medium"}))
	for _, r := range requests() {
		assert.NotEqual(t, acpMethodSessionSetConfigOption, r.Method, "no write when the value is unchanged")
	}
}

// TestACPConfigOption_PreservesValueOnTransientEmptyCurrent verifies a select
// always has a value, so a server-reported empty current (a transient/partial
// config_option_update) keeps the prior selection rather than wiping it -- which
// mergeOptionValues would otherwise propagate as a delete.
func TestACPConfigOption_PreservesValueOnTransientEmptyCurrent(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})
	seedReasoningEffort(agent, "high")

	agent.mu.Lock()
	// The server re-reports the same option with an empty current value.
	agent.applyOptionGroupsLocked([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}})
	got := agent.options.values["reasoning_effort"]
	agent.mu.Unlock()

	assert.Equal(t, "high", got, "a transient empty current must not wipe the stored selection")
	assert.Equal(t, "high", optionids.GroupByID(agent.OptionGroups(), "reasoning_effort").GetCurrentValue(),
		"the projected group keeps the preserved current too")
}

// TestACPConfigOption_ClearContextReconcilesStoredValueNotInList is the regression guard for
// the resolveCurrent membership check: on a ClearContext refresh (preferStoredValue), a stored
// value the new session no longer offers must NOT be surfaced as the group's current -- doing
// so would render a selection absent from its own option list. The payload's authoritative
// CurrentValue wins instead, mirroring reconcileCurrentOptionID on the model/mode channels.
func TestACPConfigOption_ClearContextReconcilesStoredValueNotInList(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(string) json.RawMessage { return json.RawMessage(`{}`) })
	agent.mu.Lock()
	// A prior session stored "xhigh"; a re-push the new session rejected left it lingering.
	agent.options.values = map[string]string{"reasoning_effort": "xhigh"}
	agent.options.markKnown(acpConfigOption{ID: "reasoning_effort"})
	agent.options.markSurfaced("reasoning_effort")
	// ClearContext refresh: the new session offers only [low, high] and reports low as current.
	agent.applyOptionGroupsKeepingStoredLocked([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "low",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}})
	got := agent.options.values["reasoning_effort"]
	agent.mu.Unlock()

	assert.Equal(t, "low", got, "a stored value absent from the new option list reconciles to the payload current")
	g := optionids.GroupByID(agent.OptionGroups(), "reasoning_effort")
	require.NotNil(t, g)
	assert.Equal(t, "low", g.GetCurrentValue(), "the surfaced current is selectable from the group's own options")
	assert.True(t, hasACPOption(g.GetOptions(), g.GetCurrentValue()),
		"the surfaced current must be one of the group's listed options")
}

// TestACPConfigOption_StartupReappliesPersistedValue verifies a persisted
// option preference is re-pushed after a (relaunch) handshake whose server reports a
// different default, so the user's choice survives a fresh process.
func TestACPConfigOption_StartupReappliesPersistedValue(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionSetConfigOption {
			return json.RawMessage(`{"configOptions":[
				{"id":"reasoning_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"low","options":[{"value":"low","name":"low"},{"value":"medium","name":"medium"},{"value":"high","name":"high"}]}
			]}`)
		}
		return json.RawMessage(`{}`)
	})
	// The handshake surfaced the server default (medium); the launch options carry the
	// user's persisted preference (low).
	seedReasoningEffort(agent, "medium")
	agent.applyStartupOptions(Options{Options: map[string]string{"reasoning_effort": "low"}})

	var setReq *recordedRequest
	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "the persisted preference is re-pushed on startup")
	assert.Equal(t, "low", setReq.Params["value"])
}

// TestACPConfigOption_StartupMapsEnvEffortOntoDeclaredID verifies the operator env-effort override
// (resolveProviderDefaults stores it under the well-known "effort" id) is re-pushed onto the
// provider's DECLARED effort config id at startup, even though the override never carries the
// daemon's own id ("reasoning_effort"). This is the provider-declaration replacement for the old
// live well-known-id scan: Copilot declares effortConfigID = "reasoning_effort" in configure.
func TestACPConfigOption_StartupMapsEnvEffortOntoDeclaredID(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionSetConfigOption {
			return json.RawMessage(`{"configOptions":[
				{"id":"reasoning_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"medium","name":"medium"},{"value":"high","name":"high"}]}
			]}`)
		}
		return json.RawMessage(`{}`)
	})
	// configure sets this in production; the test constructor builds the agent directly.
	agent.effortConfigID = CopilotConfigReasoningEffort
	// The handshake surfaced the server default (medium); the env override (high) is stored under
	// the well-known "effort" id, NOT under "reasoning_effort".
	seedReasoningEffort(agent, "medium")
	agent.applyStartupOptions(Options{Options: map[string]string{OptionIDEffort: "high"}})

	var setReq *recordedRequest
	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "the env-effort override is re-pushed onto the declared effort id")
	assert.Equal(t, CopilotConfigReasoningEffort, setReq.Params["configId"],
		"the well-known \"effort\" override maps onto Copilot's declared \"reasoning_effort\" axis")
	assert.Equal(t, "high", setReq.Params["value"])
}

// TestACPConfigOption_StartupEnvEffortNotDoubledOntoCoincidentalAxis is the S1 regression: a daemon
// advertising BOTH a real "effort" axis and a coincidental, category-less "reasoning_effort" axis
// must receive the env-effort override on "effort" ONLY. The old live well-known-id scan
// (effortConfigOptionID, matching acpEffortConfigOptionIDs) would mis-claim "reasoning_effort" as
// the effort axis and double-push the override onto it. With provider declaration, OpenCode wires
// no effortConfigID (its axis IS "effort"), and the category-only fallback ignores the bare
// "reasoning_effort", so the override lands on "effort" exactly once.
func TestACPConfigOption_StartupEnvEffortNotDoubledOntoCoincidentalAxis(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req recordedRequest) json.RawMessage {
		return json.RawMessage(`{}`)
	})
	require.Empty(t, agent.effortConfigID, "OpenCode's effort axis IS \"effort\"; it declares no convention id")
	// The daemon advertised both axes at handshake (known), neither yet surfaced with a value.
	agent.mu.Lock()
	agent.options.markKnown(acpConfigOption{ID: OptionIDEffort})
	agent.options.markKnown(acpConfigOption{ID: "reasoning_effort"})
	agent.mu.Unlock()

	agent.applyStartupOptions(Options{Options: map[string]string{OptionIDEffort: "high"}})

	var writes []recordedRequest
	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption {
			writes = append(writes, r)
		}
	}
	require.Len(t, writes, 1, "the env-effort override is pushed once, not doubled onto the coincidental axis")
	assert.Equal(t, OptionIDEffort, writes[0].Params["configId"])
	assert.Equal(t, "high", writes[0].Params["value"])
}

// TestACPConfigOption_LiveUpdateAppliesKnownButUnsurfacedOption is the [V8] regression guard:
// a live settings edit must reach a config option that is KNOWN (advertised at handshake) but
// not yet surfaced with a value -- the same advertised-with-empty-current case applyStartupOptions
// handles. forEachOption iterating only the surfaced values would silently skip it, yet
// UpdateSettings would still return true, so the service would persist/broadcast a value the live
// session never applied until the next relaunch. The fix iterates the union of known + valued ids.
func TestACPConfigOption_LiveUpdateAppliesKnownButUnsurfacedOption(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionSetConfigOption {
			return json.RawMessage(`{"configOptions":[
				{"id":"reasoning_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"high","name":"high"}]}
			]}`)
		}
		return json.RawMessage(`{}`)
	})
	// Advertised at handshake (known) but never surfaced with a value (no seedReasoningEffort).
	agent.mu.Lock()
	agent.options.markKnown(acpConfigOption{ID: "reasoning_effort"})
	agent.mu.Unlock()

	require.True(t, agent.UpdateSettings(map[string]string{"reasoning_effort": "high"}),
		"a known config option is applied live")

	var setReq *recordedRequest
	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption {
			r := r
			setReq = &r
			break
		}
	}
	require.NotNil(t, setReq, "the known-but-unsurfaced option is written via session/set_config_option, not silently skipped")
	assert.Equal(t, "reasoning_effort", setReq.Params["configId"])
	assert.Equal(t, "high", setReq.Params["value"])
}

// TestACPSessionRPCs_ConcurrentWithClearContext stresses the S3 coordination: ClearContext
// (session/new + the sessionID swap, under sessionMu.Lock via newSessionLocked) and the
// session/* RPCs (under sessionMu.RLock via withSessionID) run concurrently. Under -race
// this catches a data race or a deadlock, and every recorded session-scoped request must
// have carried a concrete (non-empty) sessionId -- never one observed mid-swap.
func TestACPSessionRPCs_ConcurrentWithClearContext(t *testing.T) {
	var sessionSeq atomic.Int64
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		switch method {
		case acpMethodSessionNew:
			return json.RawMessage(fmt.Sprintf(`{"sessionId":"session-%d"}`, sessionSeq.Add(1)))
		case acpMethodSessionSetConfigOption:
			return json.RawMessage(`{"configOptions":[{"id":"reasoning_effort","category":"thought_level","name":"Reasoning Effort","currentValue":"high","options":[{"value":"low","name":"low"},{"value":"high","name":"high"}]}]}`)
		default:
			return json.RawMessage(`{}`)
		}
	})
	agent.sink = &testSink{} // ClearContext broadcasts the new session id through the sink
	// Surface reasoning_effort so setConfigOption's known-id gate accepts it.
	seedReasoningEffort(agent, "high")

	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Add(1)
		go func() { defer wg.Done(); fn() }()
	}
	for range 8 {
		run(func() { agent.ClearContext() })
		run(func() { _ = agent.setConfigOption("reasoning_effort", "low") })
		run(func() { _ = agent.setModelViaConfigOption("gpt-5") })
		run(func() { _ = agent.cancelSession() })
	}
	wg.Wait()

	for _, r := range requests() {
		if sid, ok := r.Params["sessionId"].(string); ok {
			assert.NotEmpty(t, sid, "%s carried an empty sessionId", r.Method)
		}
	}
}
