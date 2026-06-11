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
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func newCursorAgentForRPC(t *testing.T) (*CursorCLIAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *CursorCLIAgent {
			a := &CursorCLIAgent{}
			a.modeChannel = modeChannelPermissionMode
			a.modelIDNormalizer = normalizeCursorModelID
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
		case acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
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
	require.Len(t, agent.AvailableModels(), 2)
	assert.Equal(t, "auto", agent.AvailableModels()[0].GetId())
	require.Len(t, agent.AvailableOptionGroups(), 1)
	assert.Equal(t, "permissionMode", agent.AvailableOptionGroups()[0].GetKey())
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
		{Id: CursorCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CursorCLIModePlan, Name: "Plan"},
	}

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "auto",
		PermissionMode: CursorCLIModePlan,
	})
	require.True(t, updated)
	assert.Equal(t, "auto", agent.model)
	assert.Equal(t, CursorCLIModePlan, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetModel, recorded[0].Method)
	assert.Equal(t, cursorCLIModelAutoWire, recorded[0].Params["modelId"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, CursorCLIModePlan, recorded[1].Params["modeId"])
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
		{Id: CursorCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CursorCLIModePlan, Name: "Plan"},
	}
	agent.sink = &testSink{}
	agent.reapplySettings = agent.reapplyModelAndMode

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "session-2", agent.sessionID)

	// Verify model is preserved with wire format conversion.
	assert.Equal(t, "auto", agent.model)

	recorded := requests()
	require.Len(t, recorded, 3)
	assert.Equal(t, acpMethodSessionNew, recorded[0].Method)
	assert.Equal(t, acpMethodSessionSetModel, recorded[1].Method)
	assert.Equal(t, cursorCLIModelAutoWire, recorded[1].Params["modelId"])
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
