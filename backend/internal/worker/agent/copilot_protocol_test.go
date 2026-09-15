package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopilotSessionEventKeepsNativeIdentityAndData(t *testing.T) {
	raw := json.RawMessage(`{"sessionId":"root-session","event":{"id":"event-1","type":"tool.execution_start","agentId":"child-1","parentId":"event-0","data":{ "toolCallId":"tool-1", "counter":9007199254740993,"unknown":false }}}`)
	original := string(raw)
	event, err := decodeCopilotSessionEvent(raw, "root-session")
	require.NoError(t, err)
	require.Equal(t, "event-1", event.ID)
	require.Equal(t, "event-0", event.ParentID)
	require.Equal(t, "child-1", event.AgentID)
	require.Equal(t, `{ "toolCallId":"tool-1", "counter":9007199254740993,"unknown":false }`, string(event.Data))
	require.Equal(t, original, string(raw))
}

func TestCopilotSessionEventRejectsInvalidSessionData(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{`, `{}`,
		`{"sessionId":"old-session","event":{"type":"session.idle"}}`,
		`{"sessionId":"root-session","event":{"type":7}}`,
		`{"sessionId":"root-session","event":{}}`,
	} {
		_, err := decodeCopilotSessionEvent(json.RawMessage(raw), "root-session")
		require.Error(t, err, raw)
	}
	_, err := decodeCopilotSessionEvent(json.RawMessage(`{"sessionId":"","event":{"type":"session.idle"}}`), "")
	require.Error(t, err)
}

func TestCopilotSessionEventPreservesUnknownEventKinds(t *testing.T) {
	event, err := decodeCopilotSessionEvent(json.RawMessage(`{"sessionId":"root-session","event":{"type":"future.event","data":[0,false,""]}}`), "root-session")
	require.NoError(t, err)
	require.Equal(t, "future.event", event.Type)
	require.JSONEq(t, `[0,false,""]`, string(event.Data))
}

func TestNativeCopilotIdleDoesNotInventOrRepeatATurnEnd(t *testing.T) {
	sink := &testSink{}
	agent := &copilotAgent{sink: sink, sessionID: "session"}
	idle := []byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"session.idle","data":{}}}}`)
	agent.HandleOutput(idle)
	agent.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"assistant.turn_start","data":{}}}}`))
	agent.HandleOutput(idle)
	agent.HandleOutput(idle)
	var ends int
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			ends++
		}
	}
	require.Equal(t, 1, ends)
	require.Len(t, sink.Messages(), 4, "every original event must remain available")
}
