package kimi

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

func TestKimiBlockedPromptPersistsTheHookResult(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	// Trimmed from .tmp/probe/kimi/logs/ws-frames.jsonl, the blocked prompt after frame 89.
	rig.feed(t, map[string]any{"type": "hook.result", "hookEvent": "UserPromptSubmit", "content": "probe hook blocked this prompt", "blocked": true})
	rig.feed(t, map[string]any{"type": "prompt.completed", "reason": "blocked"})
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Contains(t, string(rig.sink.PersistedNotifications()[0].Content), "probe hook blocked this prompt")
	assert.Empty(t, rig.sink.Messages(), "a blocked prompt starts no turn and writes no divider")
}

func TestKimiPassedHookResultIsDropped(t *testing.T) {
	t.Parallel()
	for _, blocked := range []any{false, nil, "true"} {
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": "hook.result", "hookEvent": "UserPromptSubmit", "content": "a hook detail", "blocked": blocked})
		assert.Empty(t, rig.sink.PersistedNotifications())
		assert.Empty(t, rig.sink.Messages())
	}
}

func TestKimiConfigWarningOfThisRunPersistsOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID("session_1")
	// The global replay in .tmp/probe/kimi/run4/logs/ws-frames-split.jsonl supplies this shape.
	raw, err := json.Marshal(map[string]any{
		"type": "event.config.warning", "session_id": "__global__", "timestamp": rig.agent.clock.Now().Add(time.Second).Format(time.RFC3339Nano),
		"payload": map[string]any{"type": "event.config.warning", "warnings": []map[string]string{{"domain": "loopControl", "message": "A config key is deprecated."}}},
	})
	require.NoError(t, err)
	rig.agent.HandleOutput(raw)
	rig.agent.HandleOutput(raw)
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Contains(t, string(rig.sink.PersistedNotifications()[0].Content), "A config key is deprecated.")
}

func TestKimiConfigWarningOfAnEarlierRunIsDropped(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	launched := clock.Now()
	for _, stamp := range []string{"", "invalid", launched.Add(-time.Nanosecond).Format(time.RFC3339Nano)} {
		rig := newKimiOutputRig(t)
		rig.sink.UpdateSessionID("session_1")
		rig.agent.clock = clock
		rig.agent.serverLaunchedAt = launched
		raw, err := json.Marshal(map[string]any{
			"type": "event.config.warning", "session_id": "__global__", "timestamp": stamp,
			"payload": map[string]any{"type": "event.config.warning", "warnings": []map[string]string{{"domain": "config", "message": "Old warning."}}},
		})
		require.NoError(t, err)
		rig.agent.HandleOutput(raw)
		assert.Empty(t, rig.sink.PersistedNotifications(), stamp)
	}
}

func TestKimiConfigWarningFiltersIndividualDuplicatesAndKeepsNativeFields(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID("session_1")
	rig.agent.serverLaunchedAt = rig.agent.clock.Now()
	feed := func(messages ...string) {
		var warnings []map[string]string
		for _, message := range messages {
			warnings = append(warnings, map[string]string{"domain": "config", "message": message, "nativeField": "kept"})
		}
		raw, err := json.Marshal(map[string]any{
			"type": "event.config.warning", "session_id": "__global__", "timestamp": rig.agent.serverLaunchedAt.Format(time.RFC3339Nano),
			"payload": map[string]any{"type": "event.config.warning", "warnings": warnings, "nativeField": "kept"},
		})
		require.NoError(t, err)
		rig.agent.HandleOutput(raw)
	}
	feed("First warning.", "First warning.", " ")
	feed("First warning.", "Second warning.", "Second warning.")
	rows := rig.sink.PersistedNotifications()
	require.Len(t, rows, 2)
	for i, message := range []string{"First warning.", "Second warning."} {
		var stored struct {
			Warnings    []map[string]string `json:"warnings"`
			NativeField string              `json:"nativeField"`
		}
		require.NoError(t, json.Unmarshal(rows[i].Content, &stored))
		assert.Equal(t, "kept", stored.NativeField)
		assert.Equal(t, []map[string]string{{"domain": "config", "message": message, "nativeField": "kept"}}, stored.Warnings)
	}
}

func TestKimiConfigWarningConcurrentFramesPersistOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID("session_1")
	raw := []byte(`{"type":"event.config.warning","session_id":"__global__","timestamp":"2099-01-01T00:00:00Z","payload":{"type":"event.config.warning","warnings":[{"domain":"config","message":"One warning."}]}}`)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() { rig.agent.HandleOutput(raw) })
	}
	workers.Wait()
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
}

func TestKimiConfigWarningBeforeSessionAdmissionWaitsForReplay(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	raw := []byte(`{"type":"event.config.warning","session_id":"__global__","timestamp":"2099-01-01T00:00:00Z","payload":{"type":"event.config.warning","warnings":[{"domain":"config","message":"Startup warning."}]}}`)
	rig.agent.HandleOutput(raw)
	assert.Empty(t, rig.sink.PersistedNotifications(), "the startup replay supplies the warning after the sink receives its session")
	assert.Empty(t, rig.agent.storedConfigWarnings)
	rig.sink.UpdateSessionID("session_1")
	rig.agent.HandleOutput(raw)
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Equal(t, "session_1", rig.sink.PersistedNotifications()[0].AgentSessionID)
}

type failedConfigWarningSink struct {
	*agenttest.Sink
	failure error
}

func (s *failedConfigWarningSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if s.failure != nil {
		return false, s.failure
	}
	return s.Sink.PersistNotification(source, content)
}

func TestKimiConfigWarningRetriesARefusedWrite(t *testing.T) {
	t.Parallel()
	base := &agenttest.Sink{}
	base.UpdateSessionID("session_1")
	sink := &failedConfigWarningSink{Sink: base, failure: errors.New("the warning write failed")}
	a := newOfflineKimiAgent(t, base)
	a.sink = agent.NewProviderServices(sink)
	raw := []byte(`{"type":"event.config.warning","session_id":"__global__","timestamp":"2099-01-01T00:00:00Z","payload":{"type":"event.config.warning","warnings":[{"domain":"config","message":"One warning."}]}}`)
	a.HandleOutput(raw)
	assert.Empty(t, a.storedConfigWarnings)
	sink.failure = nil
	a.HandleOutput(raw)
	a.HandleOutput(raw)
	assert.Len(t, base.PersistedNotifications(), 1)
}

func TestKimiOtherGlobalEventsAreDropped(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID("session_1")
	for _, event := range []string{"event.workspace.created", "future.event"} {
		raw, err := json.Marshal(map[string]any{"type": event, "session_id": "__global__", "timestamp": "2099-01-01T00:00:00Z", "payload": map[string]string{"type": event}})
		require.NoError(t, err)
		rig.agent.HandleOutput(raw)
	}
	assert.Empty(t, rig.sink.PersistedNotifications())
	assert.Empty(t, rig.sink.Messages())
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
