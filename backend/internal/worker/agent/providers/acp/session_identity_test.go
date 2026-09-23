package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestACPSessionUpdatesRejectAnotherSession(t *testing.T) {
	for _, sessionID := range []string{"current-session", "previous-session"} {
		t.Run(sessionID, func(t *testing.T) {
			sink := &agenttest.Sink{}
			base := &Base{sink: agent.NewProviderServices(sink), sessionID: "current-session"}
			base.handleACPSessionUpdate(json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"tool_call","toolCallId":"shared-tool","kind":"read","title":"Read file","rawInput":{"path":"file.txt"}}}`, sessionID)))
			base.handleACPSessionUpdate(json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"goal"}]}}`, sessionID)))
			if sessionID == "current-session" {
				require.Len(t, sink.Messages(), 1)
				require.True(t, base.HasAvailableCommand("goal"))
			} else {
				require.Empty(t, sink.Messages())
				require.False(t, base.HasAvailableCommand("goal"))
			}
		})
	}
}

func TestACPBufferedUpdatesKeepTheirSessionAndOriginalBytes(t *testing.T) {
	base := &Base{sink: agent.NewProviderServices(&agenttest.Sink{}), sessionID: "old-session"}
	base.beginSessionUpdates()
	base.handleACPSessionUpdate(json.RawMessage(`{"sessionId":"old-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"old-goal"}]}}`))
	newUpdate := json.RawMessage(`{"sessionId":"new-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"new-goal"}]}}`)
	base.handleACPSessionUpdate(newUpdate)
	copy(newUpdate, bytes.ReplaceAll(newUpdate, []byte("new-goal"), []byte("bad-goal")))
	require.False(t, base.HasAvailableCommand("old-goal"))
	base.sessionUpdates.mu.Lock()
	base.flushPreviousSessionUpdates()
	require.True(t, base.HasAvailableCommand("old-goal"))
	base.Mu.Lock()
	base.sessionID = "new-session"
	base.Mu.Unlock()
	base.sessionUpdates.mu.Unlock()
	base.finishSessionUpdates()
	require.True(t, base.HasAvailableCommand("new-goal"))
	require.False(t, base.HasAvailableCommand("old-goal"))
	require.False(t, base.HasAvailableCommand("bad-goal"))
	require.Empty(t, base.sessionUpdates.pending)
}

func TestACPBufferedModeUpdateRemainsNewerThanTheHandshake(t *testing.T) {
	base := &Base{sink: agent.NewProviderServices(&agenttest.Sink{}), sessionID: "session", hooks: Hooks{ModeChannel: ModeChannelPermissionMode}}
	base.beginSessionUpdates()
	base.handleACPSessionUpdate(json.RawMessage(`{"sessionId":"session","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}`))
	require.NoError(t, base.ApplyPermissionModeStartup(&SessionResult{
		SessionID: "session", CurrentModeID: "agent",
		Modes: []ModeInfo{{ID: "agent", Name: "Agent"}, {ID: "plan", Name: "Plan"}},
	}, agent.Options{}, "agent", ""))
	require.Equal(t, "plan", base.permissionMode)
}
