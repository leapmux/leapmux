package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Unit tests for parseSessionModelAndMode ---

func TestParseSessionModelAndMode(t *testing.T) {
	resp := json.RawMessage(`{
		"sessionId": "session-1",
		"models": {"currentModelId": "gemini-2.5-pro", "availableModels": []},
		"modes":  {"currentModeId": "plan", "availableModes": []}
	}`)
	model, mode := parseSessionModelAndMode(resp)
	assert.Equal(t, "gemini-2.5-pro", model)
	assert.Equal(t, "plan", mode)
}

func TestParseSessionModelAndMode_NoModes(t *testing.T) {
	resp := json.RawMessage(`{
		"sessionId": "session-1",
		"models": {"currentModelId": "gpt-5.4"}
	}`)
	model, mode := parseSessionModelAndMode(resp)
	assert.Equal(t, "gpt-5.4", model)
	assert.Equal(t, "", mode)
}

func TestParseSessionModelAndMode_InvalidJSON(t *testing.T) {
	model, mode := parseSessionModelAndMode(json.RawMessage(`invalid`))
	assert.Equal(t, "", model)
	assert.Equal(t, "", mode)
}

// --- Tests for refreshFromSession via ClearContext ---

func TestGeminiClearContextRefreshesFromSession(t *testing.T) {
	agent, _ := newGeminiAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "gemini-2.5-flash"},
				"modes":  {"currentModeId": "plan"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gemini-2.5-pro"
	agent.permissionMode = "default"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPermissionMode
	agent.refreshFromSession = agent.refreshModelAndPermissionModeFromSession

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "gemini-2.5-flash", agent.model)
	assert.Equal(t, "plan", agent.permissionMode)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gemini-2.5-flash", refresh.Model)
	assert.Equal(t, "plan", refresh.PermissionMode)
}

func TestCopilotClearContextRefreshesFromSession(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "gpt-5.4"},
				"modes":  {"currentModeId": "plan"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-4o"
	agent.permissionMode = "agent"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPermissionMode
	agent.refreshFromSession = agent.refreshModelAndPermissionModeFromSession

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "gpt-5.4", agent.model)
	assert.Equal(t, "plan", agent.permissionMode)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.4", refresh.Model)
	assert.Equal(t, "plan", refresh.PermissionMode)
}

func TestCursorClearContextRefreshesWithNormalization(t *testing.T) {
	agent, _ := newCursorAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			// Cursor returns the wire format "default[]" for auto model.
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "default[]"},
				"modes":  {"currentModeId": "agent"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "some-model"
	agent.permissionMode = "plan"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndMode
	agent.refreshFromSession = agent.refreshCursorFromSession

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "auto", agent.model)
	assert.Equal(t, "agent", agent.permissionMode)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "auto", refresh.Model)
	assert.Equal(t, "agent", refresh.PermissionMode)
}

func TestOpenCodeClearContextRefreshesPrimaryAgent(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "plan"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "openai/gpt-4o"
	agent.currentPrimaryAgent = "build"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "openai/gpt-5", agent.model)
	assert.Equal(t, "plan", agent.currentPrimaryAgent)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "openai/gpt-5", refresh.Model)
	assert.Equal(t, "plan", refresh.ExtraSettings[OptionGroupKeyPrimaryAgent])
}

func TestKiloClearContextRefreshesPrimaryAgent(t *testing.T) {
	agent, _ := newKiloAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "anthropic/claude-sonnet-4"},
				"modes":  {"currentModeId": "code"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "anthropic/claude-opus-4"
	agent.currentPrimaryAgent = "plan"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.model)
	assert.Equal(t, "code", agent.currentPrimaryAgent)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "anthropic/claude-sonnet-4", refresh.Model)
	assert.Equal(t, "code", refresh.ExtraSettings[OptionGroupKeyPrimaryAgent])
}

func TestGooseClearContextRefreshesFromSession(t *testing.T) {
	agent, _ := newGooseAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "claude-sonnet-4"},
				"modes":  {"currentModeId": "approve"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-5.4"
	agent.permissionMode = "auto"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPermissionMode
	agent.refreshFromSession = agent.refreshModelAndPermissionModeFromSession

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "claude-sonnet-4", agent.model)
	assert.Equal(t, "approve", agent.permissionMode)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "claude-sonnet-4", refresh.Model)
	assert.Equal(t, "approve", refresh.PermissionMode)
}
