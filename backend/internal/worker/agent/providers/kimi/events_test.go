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

// TestKimiTurnFrames checks native main activity and keeps child activity separate.
func TestKimiTurnFrames(t *testing.T) {
	t.Parallel()
	const header = `"turn_id":"t0","ordinal":0,"kind":"agent","started_at":"2026-11-18T00:00:00Z","steps":[]`
	const start = `{"type":"turn.started","turn":{` + header + `,"status":"running"}}`
	const end = `{"type":"turn.ended","turn":{` + header + `,"status":"ended","outcome":"completed"}}`
	frame := func(agentID, event string) string {
		return string(kimiConversationTestFrame(t, "session_1", agentID, event))
	}
	cases := []agenttest.TurnFrameCase{
		{Name: "main turn started", Line: frame(kimiMainAgentID, start), Moves: true},
		{Name: "main turn ended", Line: frame(kimiMainAgentID, end), Moves: true},
		{Name: "missing native agent", Line: frame("", start)},
		{Name: "child turn started", Line: frame("agent-0", start)},
		{Name: "child turn ended", Line: frame("agent-0", end)},
		{Name: "assistant item", Line: frame(kimiMainAgentID, `{"type":"item.started","turn_id":"t0","item":{"type":"assistant_message","item_id":"answer","status":"running","created_at":"2026-11-18T00:00:00Z","text":"hi"}}`)},
		{Name: "tool opening", Line: frame(kimiMainAgentID, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{}}}`)},
		{Name: "tool result", Line: frame(kimiMainAgentID, `{"type":"item.ended","turn_id":"t0","item":{"type":"tool_call","item_id":"call","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{},"output":"ok"}}`)},
		{Name: "flat usage", Line: `{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":1,"max_context_tokens":10}`},
		{Name: "step interrupted", Line: frame(kimiMainAgentID, `{"type":"turn.updated","turn_id":"t0","step":{"index":1,"outcome":"interrupted","started_at":"2026-11-18T00:00:00Z"}}`)},
		{Name: "unknown frame", Line: frame(kimiMainAgentID, `{"type":"future.event"}`)},
		{Name: "foreign session", Line: string(kimiConversationTestFrame(t, "session_other", kimiMainAgentID, end))},
		{Name: "missing turn", Line: frame(kimiMainAgentID, `{"type":"turn.ended"}`)},
		{Name: "invalid turn", Line: frame(kimiMainAgentID, `{"type":"turn.ended","turn":"not a turn"}`)},
	}
	agenttest.AssertTurnFrames(t, cases, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		rig := newKimiConversationRig(t)
		if tc.Name != "main turn started" {
			rig.conversationFrame(t, start)
		}
		if tc.Name == "child turn started" || tc.Name == "child turn ended" {
			adoptNativeKimiChild(t, rig.agent, "agent-0")
		}
		if tc.Name == "child turn ended" {
			rig.conversationAgentFrame(t, "agent-0", start)
		}
		before := len(rig.sink.TurnActives())
		rig.agent.HandleOutput([]byte(tc.Line))
		return rig.sink.TurnActives()[before:]
	})
}

func TestParseKimiEvent(t *testing.T) {
	t.Parallel()

	event, ok := parseKimiEvent(kimiFrame{Payload: json.RawMessage(`{"type":"warning","message":"A profile warning."}`)})
	require.True(t, ok)
	assert.Equal(t, contracts.KimiEventWarning, event.Type)
	assert.Equal(t, kimiMainAgentID, event.AgentID, "an event that states no agent is the main agent's")
	assert.JSONEq(t, `{"type":"warning","message":"A profile warning."}`, string(event.Raw))

	event, ok = parseKimiEvent(kimiFrame{Payload: json.RawMessage(`{"type":"warning","agentId":"agent-2"}`)})
	require.True(t, ok)
	assert.Equal(t, "agent-2", event.AgentID)

	for _, payload := range []string{``, `{}`, `{"type":""}`, `[]`, `null`} {
		_, ok := parseKimiEvent(kimiFrame{Payload: json.RawMessage(payload)})
		assert.False(t, ok, payload)
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

func TestKimiConfigWarningBeforeSessionAdmissionWaitsForPersistence(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	raw := []byte(`{"type":"event.config.warning","session_id":"__global__","timestamp":"2099-01-01T00:00:00Z","payload":{"type":"event.config.warning","warnings":[{"domain":"config","message":"Startup warning."}]}}`)
	rig.agent.HandleOutput(raw)
	assert.Empty(t, rig.sink.PersistedNotifications(), "startup retains the warning until the sink receives its session")
	assert.Empty(t, rig.agent.storedConfigWarnings)
	rig.sink.UpdateSessionID("session_1")
	rig.agent.flushStartupConfigWarnings()
	rig.agent.HandleOutput(raw)
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Equal(t, "session_1", rig.sink.PersistedNotifications()[0].AgentSessionID)
}

func TestKimiStartupWarningSurvivesWithoutAGlobalSubscription(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	stream := newKimiStreamRig(t)
	rig.agent.stream = stream.stream
	rig.agent.HandleOutput([]byte(`{"type":"event.config.warning","session_id":"__global__","timestamp":"2099-01-01T00:00:00Z","payload":{"type":"event.config.warning","warnings":[{"domain":"config","message":"Startup warning."}]}}`))
	rig.sink.UpdateSessionID("session_1")
	rig.agent.flushStartupConfigWarnings()
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Equal(t, "session_1", rig.sink.PersistedNotifications()[0].AgentSessionID)
	assert.Empty(t, stream.fake.subscribeFrames())
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

func TestKimiNativeServerMetadataDoesNotBecomeConversation(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"session.meta.updated", "event.session.created", "event.session.work_changed", "event.session.status_changed"} {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			payload := map[string]any{"type": typ, "agentId": "main", "sessionId": "session_1", "title": "Native title", "session": map[string]any{"id": "session_1"}}
			raw, err := json.Marshal(map[string]any{"type": typ, "session_id": "session_1", "timestamp": "2099-01-01T00:00:00Z", "payload": payload})
			require.NoError(t, err)
			rig.agent.HandleOutput(raw)
			assert.Empty(t, rig.sink.Messages(), "global publisher records can carry the real session ID")
			assert.Empty(t, rig.sink.PersistedNotifications())
			assert.False(t, rig.agent.PublishTurnActive().Active)
		})
	}
}
