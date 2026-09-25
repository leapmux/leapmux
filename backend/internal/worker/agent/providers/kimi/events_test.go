package kimi

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// TestKimiTurnFrames states which frames move the main turn flag: the main
// agent's turn.started and turn.ended, and nothing else. A subagent's turn
// moves its own tab's flag, never the main one.
func TestKimiTurnFrames(t *testing.T) {
	t.Parallel()

	event := func(payload string) string {
		return `{"type":"x","session_id":"session_1","seq":1,"payload":` + payload + `}`
	}
	cases := []agenttest.TurnFrameCase{
		{Name: "turn started", Line: event(`{"type":"turn.started","agentId":"main","turnId":0,"origin":{"kind":"user"}}`), Moves: true},
		{Name: "turn ended", Line: event(`{"type":"turn.ended","agentId":"main","turnId":0,"reason":"completed"}`), Moves: true},
		{Name: "turn started with no agent", Line: event(`{"type":"turn.started","turnId":0,"origin":{"kind":"user"}}`), Moves: true},
		{Name: "subagent turn started", Line: event(`{"type":"turn.started","agentId":"agent-0","turnId":0,"origin":{"kind":"system_trigger"}}`)},
		{Name: "subagent turn ended", Line: event(`{"type":"turn.ended","agentId":"agent-0","turnId":0,"reason":"completed"}`)},
		{Name: "assistant delta", Line: event(`{"type":"assistant.delta","agentId":"main","turnId":0,"delta":"hi"}`)},
		{Name: "tool call started", Line: event(`{"type":"tool.call.started","agentId":"main","turnId":0,"toolCallId":"c1","name":"Bash","args":{}}`)},
		{Name: "tool result", Line: event(`{"type":"tool.result","agentId":"main","turnId":0,"toolCallId":"c1","output":"ok"}`)},
		{Name: "status update", Line: event(`{"type":"agent.status.updated","agentId":"main","planMode":true}`)},
		{Name: "prompt started", Line: event(`{"type":"prompt.started","agentId":"main"}`)},
		{Name: "prompt completed", Line: event(`{"type":"prompt.completed","agentId":"main"}`)},
		{Name: "session status changed", Line: event(`{"type":"event.session.status_changed","agentId":"main","busy":false}`)},
		{Name: "step interrupted", Line: event(`{"type":"turn.step.interrupted","agentId":"main","turnId":0}`)},
		{Name: "a type this build does not know", Line: event(`{"type":"turn.paused","agentId":"main","turnId":0}`)},
		{Name: "a frame of another session", Line: `{"type":"turn.started","session_id":"session_other","payload":{"type":"turn.started","agentId":"main","turnId":0}}`},
		{Name: "a frame with no payload", Line: `{"type":"turn.started","session_id":"session_1"}`},
		{Name: "a payload that is not an event", Line: event(`"turn.started"`)},
	}
	agenttest.AssertTurnFrames(t, cases, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.Sink{}
		a := newOfflineKimiAgent(t, sink)
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActives()
	})
}

func TestParseKimiEvent(t *testing.T) {
	t.Parallel()

	event, ok := parseKimiEvent(kimiFrame{Payload: json.RawMessage(`{"type":"turn.started","turnId":3}`)})
	require.True(t, ok)
	assert.Equal(t, contracts.KimiEventTurnStarted, event.Type)
	assert.Equal(t, kimiMainAgentID, event.AgentID, "an event that states no agent is the main agent's")
	assert.JSONEq(t, `{"type":"turn.started","turnId":3}`, string(event.Raw))

	event, ok = parseKimiEvent(kimiFrame{Payload: json.RawMessage(`{"type":"turn.started","agentId":"agent-2"}`)})
	require.True(t, ok)
	assert.Equal(t, "agent-2", event.AgentID)

	for _, payload := range []string{``, `{}`, `{"type":""}`, `[]`, `null`} {
		_, ok := parseKimiEvent(kimiFrame{Payload: json.RawMessage(payload)})
		assert.False(t, ok, payload)
	}
}

// TestKimiDispatchToleratesAMinimalPayloadOfEveryType feeds every event type
// the contract lists, with nothing but its type, to a fresh agent. A handler
// must read an absent field as absent, never panic on it: a later server release
// can drop a field this build reads.
func TestKimiDispatchToleratesAMinimalPayloadOfEveryType(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../../../../../contracts/kimi-protocol.json")
	require.NoError(t, err)
	var contract struct {
		Events map[string]string `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw, &contract))
	require.NotEmpty(t, contract.Events)
	for _, eventType := range contract.Events {
		for _, agentID := range []string{kimiMainAgentID, "agent-0"} {
			sink := &agenttest.ControlSink{}
			a := newOfflineKimiAgent(t, sink)
			payload, err := json.Marshal(map[string]any{"type": eventType, "agentId": agentID})
			require.NoError(t, err)
			assert.NotPanics(t, func() {
				a.HandleOutput([]byte(`{"type":"` + eventType + `","session_id":"session_1","payload":` + string(payload) + `}`))
			}, "%s from %s", eventType, agentID)
		}
	}
}
