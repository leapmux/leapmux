package kimi

import (
	"encoding/json"
	"errors"
	"fmt"
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
	operation := func(agentID, op string) string {
		return fmt.Sprintf(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":%q,"seq":1,"ops":[%s]}}`, agentID, op)
	}
	started := `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`
	ended := `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`
	cases := []agenttest.TurnFrameCase{
		{Name: "main turn started", Line: operation("main", started), Moves: true},
		{Name: "main turn ended", Line: operation("main", ended), Moves: true},
		{Name: "missing native agent", Line: operation("", started)},
		{Name: "child turn started", Line: operation("agent-0", started)},
		{Name: "child turn ended", Line: operation("agent-0", ended)},
		{Name: "assistant frame", Line: operation("main", `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"hi"}}`)},
		{Name: "tool opening", Line: operation("main", `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"c1","toolCallId":"c1","name":"Bash","state":"running","input":{}}}`)},
		{Name: "tool result", Line: operation("main", `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"c1","toolCallId":"c1","name":"Bash","state":"done","input":{},"output":"ok"}}`)},
		{Name: "flat usage", Line: `{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":1,"max_context_tokens":10}`},
		{Name: "prompt update", Line: operation("main", `{"op":"prompt.upsert","prompt":{"promptId":"prompt-1","status":"running"}}`)},
		{Name: "step interrupted", Line: operation("main", `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"interrupted"}}`)},
		{Name: "unknown operation", Line: operation("main", `{"op":"future.operation"}`)},
		{Name: "foreign session", Line: `{"type":"transcript.ops","session_id":"session_other","payload":{"type":"transcript.ops","agent_id":"main","seq":1,"ops":[` + started + `]}}`},
		{Name: "missing payload", Line: `{"type":"transcript.ops","session_id":"session_1"}`},
		{Name: "invalid payload", Line: `{"type":"transcript.ops","session_id":"session_1","payload":"not an operation envelope"}`},
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

func TestKimiBlockedPromptPersistsTheHookResult(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	// The hook marker and prompt admission record describe a refusal without a turn.
	rig.publishMarker(t, kimiMainAgentID, "hook", map[string]any{"hookEvent": "UserPromptSubmit", "content": "probe hook blocked this prompt", "blocked": true})
	rig.transcriptOps(t, 2, `{"op":"prompt.upsert","prompt":{"promptId":"blocked-prompt","status":"blocked","createdAt":"2026-11-16T00:00:00Z"}}`)
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Contains(t, string(rig.sink.PersistedNotifications()[0].Content), "probe hook blocked this prompt")
	assert.Empty(t, rig.sink.Messages(), "a blocked prompt starts no turn and writes no divider")
}

func TestKimiPassedHookResultIsDropped(t *testing.T) {
	t.Parallel()
	for _, blocked := range []any{false, nil, "true"} {
		rig := newKimiOutputRig(t)
		rig.publishMarker(t, kimiMainAgentID, "hook", map[string]any{"hookEvent": "UserPromptSubmit", "content": "a hook detail", "blocked": blocked})
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
