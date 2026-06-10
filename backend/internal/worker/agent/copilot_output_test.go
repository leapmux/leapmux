package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

func newCopilotAgentWithSink(sink OutputSink) *CopilotCLIAgent {
	a := &CopilotCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:      "test-agent",
				providerName: "copilot",
			}},
			sink:      sink,
			sessionID: "test-session",
		},
	}
	a.modeChannel = modeChannelPermissionMode
	a.sink = newThinkingResetSink(a.sink, &a.thinkingTokens)
	return a
}

func TestHandleCopilotOutput_AgentMessageChunk(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello Copilot"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "agent_message_chunk", got.Method)
	require.Equal(t, "Hello Copilot", string(got.Content))
}

func TestHandleCopilotOutput_RequestPermission(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCopilotAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s1","options":[{"optionId":"proceed_once","name":"Allow","kind":"allow_once"}],"toolCall":{"toolCallId":"tc-1","title":"shell","kind":"execute"}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PersistedControlCount())
	require.Equal(t, "7", sink.LastPersistedControl().RequestID)
}

func TestHandleCopilotOutput_ConfigOptionUpdateBroadcastsPermissionMode(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)
	agent.permissionMode = CopilotCLIModeAgent

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#plan","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"model","currentValue":"gpt-5.4-mini","options":[{"value":"gpt-5.4","name":"GPT-5.4"},{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, CopilotCLIModePlan, agent.permissionMode)
	require.Equal(t, "gpt-5.4-mini", agent.model)
	require.Len(t, agent.availableModels, 2)
	// A combined model+mode update persists & broadcasts the full settings exactly once
	// -- the StatusChange carries the new mode -- then emits only the chat
	// settings_changed notification. It must NOT also fire UpdatePermissionMode, which
	// would broadcast a second StatusChange carrying a transiently stale model.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	require.Equal(t, "gpt-5.4-mini", refresh.Model)
	require.Equal(t, CopilotCLIModePlan, refresh.PermissionMode)
	require.Equal(t, []testSinkModeChange{{Old: CopilotCLIModeAgent, New: CopilotCLIModePlan}}, sink.ModeChanges())
	require.Empty(t, sink.PermissionMode(), "combined change must not also fire UpdatePermissionMode")
}

// A config_option_update that changes ONLY the permission mode (no model) goes
// through UpdatePermissionMode -- one StatusChange carrying the live model list plus
// the chat notification -- and triggers no settings refresh.
func TestHandleCopilotOutput_ConfigOptionUpdateModeOnly(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)
	agent.permissionMode = CopilotCLIModeAgent
	agent.model = "gpt-5.4"

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#plan","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, CopilotCLIModePlan, agent.permissionMode)
	require.Equal(t, CopilotCLIModePlan, sink.PermissionMode(), "mode-only change uses UpdatePermissionMode")
	require.Equal(t, 0, sink.SettingsRefreshCount(), "no model change means no settings refresh")
}

// A config_option_update that changes ONLY the model (the `mode` select is present
// but unchanged) broadcasts the settings once and fires NO permission-mode chat
// notification -- the mode-change notification must not piggyback on a model switch.
func TestHandleCopilotOutput_ConfigOptionUpdateModelOnly(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)
	agent.permissionMode = CopilotCLIModeAgent
	agent.model = "gpt-5.4"

	// model: gpt-5.4 -> gpt-5.4-mini; mode: still "agent" (unchanged).
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#agent","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"model","currentValue":"gpt-5.4-mini","options":[{"value":"gpt-5.4","name":"GPT-5.4"},{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, "gpt-5.4-mini", agent.model)
	require.Equal(t, CopilotCLIModeAgent, agent.permissionMode, "mode unchanged")
	require.Equal(t, 1, sink.SettingsRefreshCount())
	require.Equal(t, "gpt-5.4-mini", sink.LastSettingsRefresh().Model)
	require.Empty(t, sink.ModeChanges(), "mode unchanged -> no settings_changed notification")
	require.Empty(t, sink.PermissionMode(), "mode unchanged -> no UpdatePermissionMode")
}

// A config_option_update that changes ONLY the available mode list (same currentValue,
// no model) broadcasts a status refresh so the picker updates -- the mode-channel
// analogue of the model channel's list-change broadcast. Without it the new mode
// option would never reach the frontend until an unrelated change fired one.
func TestHandleCopilotOutput_ConfigOptionUpdateModeListOnlyBroadcasts(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)
	agent.permissionMode = CopilotCLIModeAgent
	agent.model = "gpt-5.4"
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CopilotCLIModePlan, Name: "Plan"},
	}

	// Same currentValue ("agent"), but Autopilot is added; no model option.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#agent","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"},{"value":"https://agentclientprotocol.com/protocol/session-modes#autopilot","name":"Autopilot"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Len(t, agent.availableModes, 3)
	require.Equal(t, CopilotCLIModeAgent, agent.permissionMode, "current mode unchanged")
	require.Equal(t, 1, sink.StatusActiveCount(), "the new mode option is broadcast via a status refresh")
	require.Equal(t, 0, sink.SettingsRefreshCount(), "no settings DB write when only the list changed")
	require.Empty(t, sink.ModeChanges(), "current mode unchanged -> no settings_changed notification")
	require.Empty(t, sink.PermissionMode(), "list-only change does not call UpdatePermissionMode")
}

// A runtime config_option_update surfaces an unmapped option for a permission-mode
// provider too, persisting its value in extras while the permission mode rides in its
// own field (not extras, and never the empty primaryAgent base). This is the
// permission-mode-provider runtime analogue of the OpenCode generic-surfacing test.
func TestHandleCopilotOutput_ConfigOptionUpdateSurfacesGenericGroup(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)
	agent.permissionMode = CopilotCLIModeAgent
	agent.model = "gpt-5.4"

	// A new thought_level axis appears; no model/mode change.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}}}`
	agent.HandleOutput([]byte(input))

	// The generic group is surfaced after the mapped permission-mode group.
	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 2)
	require.Equal(t, OptionGroupKeyPermissionMode, groups[0].GetKey())
	require.Equal(t, "thoughtLevel", groups[1].GetKey())

	// The value persists via a settings refresh carrying the live mode (in its field)
	// and the generic value (in extras); no primaryAgent key for a permission-mode
	// provider.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	require.Equal(t, CopilotCLIModeAgent, refresh.PermissionMode)
	require.Equal(t, "high", refresh.ExtraSettings["thoughtLevel"])
	require.NotContains(t, refresh.ExtraSettings, OptionGroupKeyPrimaryAgent)
}
