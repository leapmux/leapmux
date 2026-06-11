//go:build unix

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "copilot")
	script := fmt.Sprintf("#!/bin/sh\nLEAPMUX_COPILOT_TEST_SCENARIO=%q exec %q -test.run=TestHelperProcessCopilotCLI --\n", scenario, os.Args[0])
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_WANT_HELPER_PROCESS_COPILOT", "1")
}

func TestHelperProcessCopilotCLI(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_COPILOT") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	scenario := os.Getenv("LEAPMUX_COPILOT_TEST_SCENARIO")

	writeResponse := func(id json.RawMessage, body string, isError bool) {
		field := "result"
		if isError {
			field = "error"
		}
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"%s":%s}`+"\n", string(id), field, body)
		_ = writer.Flush()
	}

	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}

		switch req.Method {
		case acpMethodInitialize:
			writeResponse(req.ID, `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`, false)
		case acpMethodSessionNew:
			if scenario == "generic-option" {
				// A spec-compliant agent reporting a third axis (thought_level) the
				// model/mode channels do not claim. It must surface as a read-only
				// generic group alongside the mapped permission-mode group.
				writeResponse(req.ID, `{"sessionId":"copilot-new","models":{"currentModelId":"gpt-5.4","availableModels":[{"modelId":"gpt-5.4","name":"GPT-5.4"}]},"modes":{"currentModeId":"https://agentclientprotocol.com/protocol/session-modes#agent","availableModes":[{"id":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"id":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#agent","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`, false)
				continue
			}
			writeResponse(req.ID, `{"sessionId":"copilot-new","models":{"currentModelId":"gpt-5.4","availableModels":[{"modelId":"gpt-5.4","name":"GPT-5.4","description":"Full"},{"modelId":"gpt-5.4-mini","name":"GPT-5.4 mini","description":"Mini"}]},"modes":{"currentModeId":"https://agentclientprotocol.com/protocol/session-modes#agent","availableModes":[{"id":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"id":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#agent","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"model","currentValue":"gpt-5.4","options":[{"value":"gpt-5.4","name":"GPT-5.4"},{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}`, false)
		case acpMethodSessionLoad:
			if scenario == "load" {
				writeResponse(req.ID, `{"models":{"currentModelId":"gpt-5.4-mini","availableModels":[{"modelId":"gpt-5.4-mini","name":"GPT-5.4 mini"}]},"modes":{"currentModeId":"https://agentclientprotocol.com/protocol/session-modes#plan","availableModes":[{"id":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"id":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]}}`, false)
			}
		case acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
			writeResponse(req.ID, `{}`, false)
		}
	}
	os.Exit(0)
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
	require.Len(t, agent.AvailableModels(), 2)
	assert.Equal(t, "gpt-5.4", agent.AvailableModels()[0].GetId())
	require.Len(t, agent.AvailableOptionGroups(), 1)
	assert.Equal(t, "permissionMode", agent.AvailableOptionGroups()[0].GetKey())
}

// End-to-end: a handshake reporting an unmapped config option (a third axis the
// model/mode channels don't claim) surfaces it as a read-only generic group after
// the mapped permission-mode group, and its current value rides in CurrentSettings
// extras. This exercises the permission-mode AvailableOptionGroups path; OpenCode
// covers the primary-agent path.
func TestStartCopilotCLI_HandshakeSurfacesGenericConfigOption(t *testing.T) {
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

	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 2, "the mapped permission-mode group plus one generic group")
	assert.Equal(t, "permissionMode", groups[0].GetKey())
	assert.Equal(t, "thoughtLevel", groups[1].GetKey())
	assert.Equal(t, "Thought Level", groups[1].GetLabel())
	require.Len(t, groups[1].GetOptions(), 2)

	// The current generic value rides in CurrentSettings extras.
	assert.Equal(t, "high", agent.CurrentSettings().GetExtraSettings()["thoughtLevel"])
}

// The base UpdateSettings (permission-mode providers) reads only model and
// permissionMode, so an unknown extras key (a future generic axis) is structurally
// ignored: model + mode RPCs fire, nothing else, and the write succeeds.
func TestUpdateSettings_IgnoresUnknownExtraKey(t *testing.T) {
	agent, requests := newCopilotAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CopilotCLIModePlan, Name: "Plan"},
	}

	ok := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "gpt-5.4",
		PermissionMode: CopilotCLIModePlan,
		ExtraSettings:  map[string]string{"thoughtLevel": "high"},
	})

	require.True(t, ok)
	recorded := requests()
	require.Len(t, recorded, 2, "model + mode RPCs fire; the unknown extra key sends nothing")
	assert.Equal(t, acpMethodSessionSetModel, recorded[0].Method)
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
	require.Len(t, agent.AvailableModels(), 2)

	// A runtime update whose model select lists only gpt-5.4-mini.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"copilot-reunion","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"gpt-5.4-mini","options":[{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, "gpt-5.4-mini", agent.model, "the config-option current becomes the model")
	ids := map[string]bool{}
	for _, m := range agent.AvailableModels() {
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
		{Id: CopilotCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CopilotCLIModePlan, Name: "Plan"},
	}

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:          "gpt-5.4-mini",
		PermissionMode: CopilotCLIModePlan,
	})
	require.True(t, updated)
	assert.Equal(t, "gpt-5.4-mini", agent.model)
	assert.Equal(t, CopilotCLIModePlan, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetModel, recorded[0].Method)
	assert.Equal(t, "gpt-5.4-mini", recorded[0].Params["modelId"])
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
	agent := &CopilotCLIAgent{}

	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "permissionMode", groups[0].GetKey())
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
