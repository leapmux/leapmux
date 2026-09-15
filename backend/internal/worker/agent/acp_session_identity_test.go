package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

func TestACPSessionUpdatesRejectAnotherSession(t *testing.T) {
	for _, sessionID := range []string{"current-session", "previous-session"} {
		t.Run(sessionID, func(t *testing.T) {
			sink := &testSink{}
			base := &acpBase{sink: sink, sessionID: "current-session"}
			base.handleACPSessionUpdate(json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"tool_call","toolCallId":"shared-tool","kind":"read","title":"Read file","rawInput":{"path":"file.txt"}}}`, sessionID)), nil)
			base.handleACPSessionUpdate(json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"goal"}]}}`, sessionID)), nil)
			if sessionID == "current-session" {
				require.Len(t, sink.Messages(), 1)
				require.True(t, base.hasAvailableCommand("goal"))
			} else {
				require.Empty(t, sink.Messages())
				require.False(t, base.hasAvailableCommand("goal"))
			}
		})
	}
}

func TestACPBufferedPrimaryAgentCatalogControlsTheRequestedSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sessionID string
		options   string
		wantRPC   bool
	}{
		{"advertised during startup", "session-1", `[{"value":"build"},{"value":"plan"}]`, true},
		{"identical to fallback", "session-1", `[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]`, true},
		{"requested option absent", "session-1", `[{"value":"plan"}]`, false},
		{"another session", "previous-session", `[{"value":"build"},{"value":"plan"}]`, false},
		{"no advertised catalog", "session-1", `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, requests := newOpenCodeAgentForRPC(t)
			agent.sink = &testSink{}
			agent.beginSessionUpdates()
			agent.handleACPSessionUpdate(json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":%s}]}}`, tc.sessionID, tc.options)), nil)
			fallback := []*leapmuxv1.AvailableOption{{Id: "build", Name: "Build"}, {Id: "plan", Name: "Plan"}}
			require.NoError(t, agent.configurePrimaryAgents(nil, "plan", "build", fallback, "build"))
			if tc.wantRPC {
				require.Equal(t, "build", agent.currentPrimaryAgent)
				recorded := requests()
				require.Len(t, recorded, 1)
				require.Equal(t, acpMethodSessionSetMode, recorded[0].Method)
				require.Equal(t, "build", recorded[0].Params["modeId"])
			} else {
				require.Empty(t, requests(), "a fallback or another session cannot authorize a requested mode")
			}
		})
	}
}

func TestACPBufferedUpdatesKeepTheirSessionAndOriginalBytes(t *testing.T) {
	base := &acpBase{sink: &testSink{}, sessionID: "old-session"}
	base.beginSessionUpdates()
	base.handleACPSessionUpdate(json.RawMessage(`{"sessionId":"old-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"old-goal"}]}}`), nil)
	newUpdate := json.RawMessage(`{"sessionId":"new-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"new-goal"}]}}`)
	base.handleACPSessionUpdate(newUpdate, nil)
	copy(newUpdate, bytes.ReplaceAll(newUpdate, []byte("new-goal"), []byte("bad-goal")))
	require.False(t, base.hasAvailableCommand("old-goal"))
	base.sessionUpdates.mu.Lock()
	base.flushPreviousSessionUpdates()
	require.True(t, base.hasAvailableCommand("old-goal"))
	base.mu.Lock()
	base.sessionID = "new-session"
	base.mu.Unlock()
	base.sessionUpdates.mu.Unlock()
	base.finishSessionUpdates()
	require.True(t, base.hasAvailableCommand("new-goal"))
	require.False(t, base.hasAvailableCommand("old-goal"))
	require.False(t, base.hasAvailableCommand("bad-goal"))
	require.Empty(t, base.sessionUpdates.pending)
}

func TestACPBufferedModeUpdateRemainsNewerThanTheHandshake(t *testing.T) {
	base := &acpBase{sink: &testSink{}, sessionID: "session", modeChannel: modeChannelPermissionMode}
	base.beginSessionUpdates()
	base.handleACPSessionUpdate(json.RawMessage(`{"sessionId":"session","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}`), nil)
	require.NoError(t, base.applyPermissionModeStartup(&acpSessionResult{
		SessionID: "session", CurrentModeID: "agent",
		Modes: []acpModeInfo{{ID: "agent", Name: "Agent"}, {ID: "plan", Name: "Plan"}},
	}, Options{}, "agent", ""))
	require.Equal(t, "plan", base.permissionMode)
}

func TestACPBufferedPrimaryAgentUpdatePrecedesTheRequestedSelection(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPC(t)
	agent.sink = &testSink{}
	agent.beginSessionUpdates()
	agent.handleACPSessionUpdate(json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}`, agent.currentSessionID())), nil)
	require.NoError(t, agent.configurePrimaryAgents([]acpModeInfo{
		{ID: "build", Name: "Build"}, {ID: "plan", Name: "Plan"},
	}, "build", "build", nil, "build"))
	require.Equal(t, "build", agent.currentPrimaryAgent)
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acpMethodSessionSetMode, recorded[0].Method)
	require.Equal(t, "build", recorded[0].Params["modeId"])
}

func TestACPAdvertisedSteeringKeepsItsSessionUntilTheReply(t *testing.T) {
	var agent *OpenCodeAgent
	replaced := false
	agent, _ = newOpenCodeAgentForRPCWithResponder(t, func(method string) jsonrpcResponsePayload {
		if method == "_provider/session/steer" && agent.sessionMu.TryLock() {
			agent.mu.Lock()
			agent.sessionID = "replacement-session"
			agent.mu.Unlock()
			agent.sessionMu.Unlock()
			replaced = true
		}
		return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
	})
	agent.steerMethod = "_provider/session/steer"
	agent.promptActive = true
	require.NoError(t, agent.steerAdvertised("Use the existing session.", nil))
	require.False(t, replaced, "session replacement must wait for the steering reply")
	require.Equal(t, "session-1", agent.currentSessionID())
}
