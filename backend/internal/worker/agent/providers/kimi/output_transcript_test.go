package kimi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

const kimiNativeBashOpening = `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"running","input":{"command":"printf ok"}}}`

const kimiNativeRetryStep = `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running","retry":{"failedAttempt":1,"nextAttempt":2,"maxAttempts":10,"delayMs":545.13,"errorName":"APIStatusError","errorMessage":"The stream failed."}}}`

func (r *kimiOutputRig) transcriptOps(t *testing.T, seq int64, operations ...string) {
	t.Helper()
	r.transcriptAgentOps(t, kimiMainAgentID, seq, operations...)
}

func (r *kimiTestRig) transcriptOps(t *testing.T, seq int64, operations ...string) {
	t.Helper()
	(&kimiOutputRig{agent: r.agent, sink: r.sink}).transcriptOps(t, seq, operations...)
}

func (r *kimiOutputRig) transcriptAgentOps(t *testing.T, agentID string, seq int64, operations ...string) {
	t.Helper()
	ops := make([]json.RawMessage, len(operations))
	for i, operation := range operations {
		ops[i] = json.RawMessage(operation)
	}
	frame, err := json.Marshal(map[string]any{
		"type": "transcript.ops", "session_id": r.sessionID(),
		"payload": map[string]any{"type": "transcript.ops", "agent_id": agentID, "seq": seq, "ops": ops},
	})
	require.NoError(t, err)
	r.agent.HandleOutput(frame)
}

func TestKimiNativeAgentReferencesAdoptChildOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	op := `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`
	rig.transcriptOps(t, 1, op)
	rig.transcriptOps(t, 2, op)
	row, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, "Reviewer", row.Title)
	messages := child.Messages()
	require.Equal(t, 1, len(messages))
	assert.Equal(t, "Review the diff.", userText(t, messages[0]))
}

func TestKimiNativeChildFramesBeforeAdoptionKeepTheirOwner(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"other"},"prompt":"Review the diff."}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.read","toolCallId":"read","name":"Read","state":"done","input":{"path":"child.go"},"output":"package child"}}`)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	messages := child.Messages()
	require.Equal(t, 2, len(messages))
	assert.Contains(t, string(messages[1].Content), "package child")
	for _, message := range rig.sink.Messages() {
		assert.NotContains(t, string(message.Content), "package child")
	}
}

func TestKimiNativeChildPendingOverflowRecoversTranscript(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	for seq := int64(1); seq <= kimiMaxPendingEvents+2; seq++ {
		native.transcriptAgentOps(t, "agent-0", seq)
	}
	var snapshot any
	require.NoError(t, json.Unmarshal([]byte(`{"agent_id":"agent-0","items":[{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"other"},"prompt":"Review the diff.","steps":[{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running","frames":[{"kind":"tool","frameId":"t0.1.read","toolCallId":"read","name":"Read","state":"done","input":{"path":"child.go"},"output":"Recovered child output"}]}]}],"has_more":false,"interactions":[],"attachments":[],"prompts":[],"seq":258}`), &snapshot))
	rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), "/transcript"), fakeKapReply{Data: snapshot})
	native.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	waitFor(t, func() bool { return len(child.Messages()) == 2 }, "the native child snapshot restores its output")
	messages := child.Messages()
	require.Equal(t, 2, len(messages))
	assert.Contains(t, string(messages[1].Content), "Recovered child output")
	assert.Len(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/transcript")), 1)
}

func (r *kimiOutputRig) transcriptReset(t *testing.T, seq int64, items ...string) {
	t.Helper()
	rows := make([]json.RawMessage, len(items))
	for i, item := range items {
		rows[i] = json.RawMessage(item)
	}
	frame, err := json.Marshal(map[string]any{
		"type": "transcript.reset", "session_id": r.sessionID(),
		"payload": map[string]any{"type": "transcript.reset", "agent_id": "main", "seq": seq,
			"snapshot": map[string]any{"items": rows, "interactions": []any{}, "attachments": []any{}, "prompts": []any{}}},
	})
	require.NoError(t, err)
	r.agent.HandleOutput(frame)
}

func TestKimiNativeTranscriptCompletesAuthoritativeText(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`)
	assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, rig.agent.PublishTurnActive())
	rig.transcriptOps(t, 2,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"A🙂"}}`,
		`{"op":"append","target":{"type":"frame","turnId":"t0","stepId":"t0.1","frameId":"t0.1.f1"},"offset":3,"text":"한"}`)
	assert.Zero(t, len(rig.sink.Messages()), "running text remains incomplete")
	rig.transcriptOps(t, 3,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"A🙂한!"}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"completed"}}`)
	messages := rig.sink.Messages()
	require.Len(t, messages, 1)
	kind, text := assembledRow(t, messages[0])
	assert.Equal(t, string(agent.AssembledMessageKindText), kind)
	assert.Equal(t, "A🙂한!", text)
	rig.transcriptOps(t, 4, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"completed","origin":{"kind":"user"}}}`)
	messages = rig.sink.Messages()
	require.Len(t, messages, 2)
	assert.True(t, messages[1].TurnEnd)
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
}

func TestKimiAppendTranscriptText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, local, chunk, want string
		offset                   int64
		ok                       bool
	}{
		{name: "empty", ok: true},
		{name: "first chunk", chunk: "🙂", want: "🙂", ok: true},
		{name: "unicode offset", local: "A🙂", offset: 3, chunk: "한", want: "A🙂한", ok: true},
		{name: "repeated chunk", local: "A🙂한", offset: 1, chunk: "🙂", want: "A🙂한", ok: true},
		{name: "overlapping chunk", local: "A🙂", offset: 1, chunk: "🙂한", want: "A🙂한", ok: true},
		{name: "negative offset", local: "abc", offset: -1, chunk: "d", want: "abc"},
		{name: "missing prefix", local: "abc", offset: 4, chunk: "d", want: "abc"},
		{name: "large offset", local: "abc", offset: 1 << 62, chunk: "d", want: "abc"},
		{name: "conflicting overlap", local: "abc", offset: 1, chunk: "xx", want: "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := kimiAppendTranscriptText(tc.local, tc.offset, tc.chunk)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestKimiNativeTranscriptResetReplacesUnfinishedText(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 10,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"old"}}`)
	rig.agent.HandleOutput([]byte(`{"type":"transcript.reset","session_id":"session_1","payload":{"type":"transcript.reset","agent_id":"main","seq":1,"snapshot":{"items":[{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running","frames":[{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"reset"}]}]}],"interactions":[],"attachments":[],"prompts":[]}}}`))
	rig.transcriptOps(t, 2, `{"op":"append","target":{"type":"frame","turnId":"t0","stepId":"t0.1","frameId":"t0.1.f1"},"offset":5,"text":"!"}`)
	rig.transcriptOps(t, 3, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"completed"}}`)
	messages := rig.sink.Messages()
	require.Equal(t, 1, len(messages))
	_, text := assembledRow(t, messages[0])
	assert.Equal(t, "reset!", text)
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"duplicate"}}`)
	assert.Equal(t, 1, len(rig.sink.Messages()), "repeated positions cannot publish another text row")
}

func TestKimiNativeTranscriptResetDoesNotRepublishSavedHistory(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.attachedAt = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	rig.transcriptReset(t, 9, `{"kind":"turn","turnId":"t0","ordinal":0,"state":"completed","origin":{"kind":"user"},"endedAt":"2026-11-14T00:00:00Z","steps":[{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"completed","frames":[{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"saved history"}]}]}`)
	assert.Zero(t, len(rig.sink.Messages()))
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
}

func TestKimiNativeTranscriptEmptyResetClearsTheRunningTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 10,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"removed"}}`)
	rig.transcriptReset(t, 0)
	assert.Zero(t, len(rig.sink.Messages()))
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
}

func TestKimiNativeTranscriptToolLifecycle(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"done", "error"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`)
			opening := kimiNativeBashOpening
			rig.transcriptOps(t, 2, opening, opening)
			result := fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":%q,"input":{"command":"printf ok"},"output":{"ok":false,"count":0}}}`, state)
			rig.transcriptOps(t, 3, result, result)
			rig.transcriptOps(t, 4, result)
			messages := rig.sink.Messages()
			require.Equal(t, 2, len(messages))
			assert.NotEmpty(t, messages[0].SpanID)
			assert.Equal(t, "Bash", messages[0].SpanType)
			assert.False(t, messages[0].Closing)
			assert.Equal(t, messages[0].SpanID, messages[1].SpanID)
			assert.True(t, messages[1].Closing)
			assert.Contains(t, rig.sink.ClosedSpans(), messages[0].SpanID)
			var stored struct {
				Payload struct {
					AgentID string `json:"agent_id"`
					Ops     []struct {
						Frame struct {
							State  string          `json:"state"`
							Output json.RawMessage `json:"output"`
						} `json:"frame"`
					} `json:"ops"`
				} `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(messages[1].Content, &stored))
			assert.Equal(t, "main", stored.Payload.AgentID)
			require.Len(t, stored.Payload.Ops, 1)
			assert.Equal(t, state, stored.Payload.Ops[0].Frame.State)
			assert.JSONEq(t, `{"ok":false,"count":0}`, string(stored.Payload.Ops[0].Frame.Output))
			rig.agent.Mu.Lock()
			uses := rig.agent.TurnToolUses
			rig.agent.Mu.Unlock()
			assert.EqualValues(t, 1, uses)
		})
	}
}

func TestKimiNativeTranscriptResetKeepsAnOpenToolSpan(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, kimiNativeBashOpening)
	rig.transcriptReset(t, 8, `{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running","frames":[{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"running","input":{"command":"printf ok"}}]}]}`)
	rig.transcriptOps(t, 9, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"done","input":{"command":"printf ok"},"output":"ok"}}`)
	messages := rig.sink.Messages()
	require.Equal(t, 2, len(messages))
	assert.Equal(t, messages[0].SpanID, messages[1].SpanID)
	assert.True(t, messages[1].Closing)
}

func TestKimiNativeTranscriptIgnoresAToolResultAfterItsTurnEnded(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, kimiNativeBashOpening)
	rig.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"cancelled","origin":{"kind":"user"}}}`)
	before := rig.sink.Messages()
	require.Equal(t, 3, len(before))
	assert.True(t, before[1].Closing)
	assert.True(t, before[2].TurnEnd)
	rig.transcriptOps(t, 4, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"done","input":{"command":"printf ok"},"output":"late"}}`)
	assert.Equal(t, 3, len(rig.sink.Messages()))
}

func TestKimiNativeTranscriptPersistsBlockedHookMarkers(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"hook","payload":{"hookEvent":"UserPromptSubmit","blocked":true,"content":"The hook blocked this prompt."},"at":"2026-11-15T00:00:00Z"}}`)
	notifications := rig.sink.PersistedNotifications()
	require.Equal(t, 1, len(notifications))
	var stored struct {
		Payload struct {
			Ops []struct {
				Item struct {
					Marker  string `json:"marker"`
					Payload struct {
						Blocked bool   `json:"blocked"`
						Content string `json:"content"`
					} `json:"payload"`
				} `json:"item"`
			} `json:"ops"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(notifications[0].Content, &stored))
	require.Len(t, stored.Payload.Ops, 1)
	assert.Equal(t, "hook", stored.Payload.Ops[0].Item.Marker)
	assert.True(t, stored.Payload.Ops[0].Item.Payload.Blocked)
	assert.Equal(t, "The hook blocked this prompt.", stored.Payload.Ops[0].Item.Payload.Content)
	rig.transcriptOps(t, 2, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m2","marker":"hook","payload":{"hookEvent":"UserPromptSubmit","blocked":false,"content":"The hook passed."}}}`)
	assert.Equal(t, 1, len(rig.sink.PersistedNotifications()))
}

func TestKimiNativeCompactionMarkersReleaseManualInput(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"completed", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"compaction","payload":{"phase":"started","trigger":"manual"}}}`)
			assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive())
			rig.transcriptOps(t, 2, fmt.Sprintf(`{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m2","marker":"compaction","payload":{"phase":%q,"result":{"tokensBefore":9000,"tokensAfter":1000}}}}`, phase))
			assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
			assert.Equal(t, 1, len(rig.sink.PersistedNotifications()))
		})
	}
}

func TestKimiNativeTranscriptRetrySeparatesAttemptsAndResolvesOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"partial "}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"thinking","frameId":"t0.1.f2","text":"unsigned thought"}}`)
	retry := kimiNativeRetryStep
	rig.transcriptOps(t, 2, retry)
	messages := rig.sink.Messages()
	require.Len(t, messages, 1)
	_, partial := assembledRow(t, messages[0])
	assert.Equal(t, "partial ", partial)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), assembledCompletion(t, messages[0]))
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Contains(t, string(rig.sink.PersistedNotifications()[0].Content), `"retry"`)
	assert.Empty(t, rig.sink.Notifications())
	rig.transcriptOps(t, 3, retry)
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
	rig.transcriptOps(t, 4,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.2","turnId":"t0","ordinal":2,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.2","frame":{"kind":"text","frameId":"t0.2.f1","role":"assistant","text":"RETRIED-ANSWER"}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.2","turnId":"t0","ordinal":2,"state":"completed"}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"completed","origin":{"kind":"user"}}}`)
	messages = rig.sink.Messages()
	require.Len(t, messages, 3)
	_, answer := assembledRow(t, messages[1])
	assert.Equal(t, "RETRIED-ANSWER", answer)
	assert.Equal(t, string(agent.MessageCompletionComplete), assembledCompletion(t, messages[1]))
	assert.Len(t, rig.sink.Notifications(), 1)
}

func TestKimiNativeTranscriptRetryResetKeepsNoticeAndPartialOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"partial "}}`, kimiNativeRetryStep)
	rig.transcriptReset(t, 2, `{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running","retry":{"failedAttempt":1,"nextAttempt":2,"maxAttempts":10,"delayMs":545.13},"frames":[{"kind":"text","frameId":"t0.1.f1","role":"assistant","text":"partial "}]}]}`)
	assert.Len(t, rig.sink.Messages(), 1)
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.True(t, rig.agent.run(kimiMainAgentID).retry.Pending())
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.2","frame":{"kind":"text","frameId":"t0.2.f1","role":"assistant","text":"answer"}}`)
	assert.Len(t, rig.sink.Notifications(), 1)
}

func TestKimiNativeTranscriptRetryValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"absent", "null", false},
		{"missing fields", "{}", false},
		{"zero failed attempt", `{"failedAttempt":0,"nextAttempt":2,"maxAttempts":10,"delayMs":0}`, false},
		{"negative next attempt", `{"failedAttempt":1,"nextAttempt":-1,"maxAttempts":10,"delayMs":0}`, false},
		{"same attempt", `{"failedAttempt":2,"nextAttempt":2,"maxAttempts":10,"delayMs":0}`, false},
		{"past maximum", `{"failedAttempt":1,"nextAttempt":2,"maxAttempts":1,"delayMs":0}`, false},
		{"negative delay", `{"failedAttempt":1,"nextAttempt":2,"maxAttempts":10,"delayMs":-1}`, false},
		{"absent delay", `{"failedAttempt":1,"nextAttempt":2,"maxAttempts":10}`, false},
		{"zero delay", `{"failedAttempt":1,"nextAttempt":2,"maxAttempts":10,"delayMs":0}`, true},
		{"large counters", `{"failedAttempt":2147483648,"nextAttempt":2147483649,"maxAttempts":2147483650,"delayMs":0.5}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var retry *kimiTranscriptRetry
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &retry))
			assert.Equal(t, tc.want, retry.valid())
		})
	}
}

func nativeKimiProgressOperation(kind, text string) string {
	return fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"running","input":{"command":"printf output"},"progress":{"kind":%q,"text":%q}}}`, kind, text)
}

func TestKimiNativeToolProgressRetainsChunksWithoutRepeatingSnapshot(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, kimiNativeBashOpening)
	rig.transcriptOps(t, 2, nativeKimiProgressOperation("stdout", "one"))
	rig.transcriptOps(t, 3, nativeKimiProgressOperation("stdout", "one"))
	rig.transcriptOps(t, 3, nativeKimiProgressOperation("stdout", "one"))
	rig.transcriptReset(t, 3, `{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running","frames":[{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"running","input":{"command":"printf output"},"progress":{"kind":"stdout","text":"one"}}]}]}`)
	rig.transcriptOps(t, 4, nativeKimiProgressOperation("stderr", "!"))
	var total int64
	var tail string
	var exact bool
	for _, update := range rig.sink.ProgressUpdates() {
		switch update.Operation {
		case agent.ProgressOutputTotal:
			total = update.Value
			exact = update.Exact
		case agent.ProgressOutputTail:
			tail = update.Text
		default:
			// This assertion reads output totals and tails, not lifecycle updates.
		}
	}
	assert.EqualValues(t, 7, total)
	assert.True(t, exact)
	assert.Equal(t, "oneone!", tail)
	rig.agent.settleAfterExit(agent.MessageCompletionInterrupted)
	messages := rig.sink.Messages()
	require.Len(t, messages, 2)
	var original map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(messages[1].Content, &original))
	assert.NotContains(t, original, "output", "retention must not rewrite the native envelope")
	require.NotEmpty(t, messages[1].SupplementalContent)
	var supplement map[string]any
	require.NoError(t, json.Unmarshal(messages[1].SupplementalContent, &supplement))
	assert.Equal(t, map[string]any{"sessionId": "session_1", "agentId": "main", "turnId": "t0", "stepId": "t0.1", "frameId": "t0.1.call", "toolCallId": "call", "text": "oneone!", "truncated": false}, supplement["toolOutput"])
}

func nativeKimiProgressSnapshot(t *testing.T, kind, text string) string {
	t.Helper()
	var op struct {
		Frame json.RawMessage `json:"frame"`
	}
	require.NoError(t, json.Unmarshal([]byte(nativeKimiProgressOperation(kind, text)), &op))
	return fmt.Sprintf(`{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","turnId":"t0","ordinal":1,"state":"running","frames":[%s]}]}`, op.Frame)
}

func lastNativeKimiOutputProgress(rig *kimiOutputRig) (total, tail agent.ProgressUpdate) {
	for _, update := range rig.sink.ProgressUpdates() {
		switch update.Operation {
		case agent.ProgressOutputTotal:
			total = update
		case agent.ProgressOutputTail:
			tail = update
		default:
		}
	}
	return total, tail
}

func TestKimiNativeToolProgressResetGapKeepsOnlyAnIncompleteTail(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, kimiNativeBashOpening)
	rig.transcriptOps(t, 2, nativeKimiProgressOperation("stdout", "first"))
	rig.transcriptReset(t, 9, nativeKimiProgressSnapshot(t, "stdout", "last"))
	total, tail := lastNativeKimiOutputProgress(rig)
	assert.False(t, total.Exact)
	assert.True(t, total.Minimum)
	assert.Equal(t, "last", tail.Text)
	assert.True(t, tail.Truncated)
	rig.transcriptOps(t, 10, nativeKimiProgressOperation("stderr", "!"))
	total, tail = lastNativeKimiOutputProgress(rig)
	assert.EqualValues(t, 6, total.Value)
	assert.False(t, total.Exact)
	assert.True(t, total.Minimum)
	assert.Equal(t, "last!", tail.Text)
	assert.True(t, tail.Truncated)
}

func TestKimiNativeToolProgressClipsUTF8WithoutLosingTheByteTotal(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, kimiNativeBashOpening)
	chunk := strings.Repeat("🙂", kimiLiveOutputLimit/4+1)
	rig.transcriptOps(t, 2, nativeKimiProgressOperation("stdout", chunk))
	rig.transcriptOps(t, 3, nativeKimiProgressOperation("stderr", "end"))
	total, tail := lastNativeKimiOutputProgress(rig)
	assert.EqualValues(t, len(chunk)+len("end"), total.Value)
	assert.True(t, total.Exact)
	assert.True(t, tail.Truncated)
	assert.LessOrEqual(t, len(tail.Text), kimiLiveOutputLimit)
	assert.True(t, utf8.ValidString(tail.Text))
	assert.True(t, strings.HasSuffix(tail.Text, "end"))
}
