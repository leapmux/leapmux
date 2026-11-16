package kimi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// kimiOutputRig is an agent with no server, fed events of session_1.
type kimiOutputRig struct {
	agent *Agent
	sink  *agenttest.ControlSink
}

func newKimiOutputRig(t *testing.T) *kimiOutputRig {
	t.Helper()
	sink := &agenttest.ControlSink{}
	return &kimiOutputRig{agent: newOfflineKimiAgent(t, sink), sink: sink}
}

func (r *kimiOutputRig) sessionID() string {
	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	return r.agent.sessionID
}

func (r *kimiOutputRig) feed(t *testing.T, payload map[string]any) {
	t.Helper()
	r.agent.HandleOutput(kimiEventFrame(t, "session_1", payload))
}

// assembledRow decodes a row the worker assembled from deltas.
func assembledRow(t *testing.T, message agenttest.Message) (kind, text string) {
	t.Helper()
	var row map[string]string
	require.NoError(t, json.Unmarshal(message.Content, &row), string(message.Content))
	require.Equal(t, contracts.AssembledMessageType, row[contracts.AssembledMessageFieldType])
	return row[contracts.AssembledMessageFieldKind], row[contracts.AssembledMessageFieldText]
}

// assembledCompletion reads the completion an assembled row states.
func assembledCompletion(t *testing.T, message agenttest.Message) string {
	t.Helper()
	var row map[string]string
	require.NoError(t, json.Unmarshal(message.Content, &row), string(message.Content))
	return row[contracts.AssembledMessageFieldCompletion]
}

// eventType reads the `type` of a persisted event payload.
func eventType(t *testing.T, message agenttest.Message) string {
	t.Helper()
	var head struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(message.Content, &head), string(message.Content))
	return head.Type
}

func metadataField(t *testing.T, message agenttest.Message, key string) (json.RawMessage, bool) {
	t.Helper()
	if len(message.Metadata) == 0 {
		return nil, false
	}
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(message.Metadata, &fields))
	value, ok := fields[key]
	return value, ok
}

func TestKimiAssemblesThinkingAndText(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"thinking","frameId":"thought","text":"Let me "}}`)
	rig.transcriptOps(t, 3, `{"op":"append","target":{"type":"frame","turnId":"t0","stepId":"t0.1","frameId":"thought"},"offset":7,"text":"think."}`)
	rig.transcriptOps(t, 4, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Hello "}}`)
	rig.transcriptOps(t, 5, `{"op":"append","target":{"type":"frame","turnId":"t0","stepId":"t0.1","frameId":"answer"},"offset":6,"text":""}`)
	rig.transcriptOps(t, 6, `{"op":"append","target":{"type":"frame","turnId":"t0","stepId":"t0.1","frameId":"answer"},"offset":6,"text":"world."}`)
	rig.transcriptOps(t, 7, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","state":"completed","ordinal":1}}`)
	rig.transcriptOps(t, 8, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)

	messages := rig.sink.Messages()
	require.Len(t, messages, 3)
	kind, text := assembledRow(t, messages[0])
	assert.Equal(t, string(agent.AssembledMessageKindReasoning), kind)
	assert.Equal(t, "Let me think.", text, "the thinking is complete once the answer starts")
	kind, text = assembledRow(t, messages[1])
	assert.Equal(t, string(agent.AssembledMessageKindText), kind)
	assert.Equal(t, "Hello world.", text)
	assert.True(t, messages[2].TurnEnd)
	assert.Equal(t, contracts.KimiEventTranscriptOps, eventType(t, messages[2]))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, rig.sink.TurnLifecycle(),
		"the turn end is persisted BEFORE the flag clears")

	var modelText int
	for _, update := range rig.sink.ProgressUpdates() {
		if update.Operation == agent.ProgressModelText {
			modelText++
		}
	}
	assert.Equal(t, 4, modelText, "each delta with text moves the progress counter")
}

func TestKimiTurnEndCarriesTheToolCountAndTheContext(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
	rig.agent.HandleOutput([]byte(`{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":1200,"max_context_tokens":262144}`))
	seq := int64(2)
	for _, id := range []string{"call_1", "call_2"} {
		rig.transcriptOps(t, seq,
			fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":%q,"toolCallId":%q,"name":"Read","state":"running","input":{"path":"a.go"}}}`, id, id),
			fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":%q,"toolCallId":%q,"name":"Read","state":"done","input":{"path":"a.go"},"output":"package a"}}`, id, id))
		seq++
	}
	rig.transcriptOps(t, seq, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","state":"completed","ordinal":1,"usage":{"inputOther":10,"output":5,"inputCacheRead":100,"inputCacheCreation":1}}}`)
	rig.transcriptOps(t, seq+1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)

	messages := rig.sink.Messages()
	turnEnd := messages[len(messages)-1]
	require.True(t, turnEnd.TurnEnd)
	count, ok := metadataField(t, turnEnd, contracts.MessageMetadataFieldToolUses)
	require.True(t, ok, "the divider states the turn's tool count")
	assert.JSONEq(t, `2`, string(count))
	usage, ok := metadataField(t, turnEnd, contracts.SessionInfoKeyContextUsage)
	require.True(t, ok, "a reconnecting client reads the context off the turn end")
	var fields map[string]any
	require.NoError(t, json.Unmarshal(usage, &fields))
	assert.EqualValues(t, 1200, fields[contracts.ContextUsageFieldContextTokens])
	assert.EqualValues(t, 262144, fields[contracts.ContextUsageFieldContextWindow])
	assert.EqualValues(t, 10, fields["input_tokens"])
	assert.EqualValues(t, 5, fields["output_tokens"])
	assert.EqualValues(t, 100, fields["cache_read_input_tokens"])
	assert.EqualValues(t, 1, fields["cache_creation_input_tokens"])

	info, ok := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok, "the status update broadcasts the context")
	assert.NotNil(t, info)
}

func TestKimiToolCallLifecycle(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t3","state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t3","stepId":"t3.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Running it."}}`)
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t3","stepId":"t3.1","frame":{"kind":"tool","frameId":"t3.1.call_bash","toolCallId":"call_bash","name":"Bash","state":"running"}}`)
	opening := `{"op":"frame.upsert","turnId":"t3","stepId":"t3.1","frame":{"kind":"tool","frameId":"t3.1.call_bash","toolCallId":"call_bash","name":"Bash","state":"running","input":{"command":"echo hi"}}}`
	rig.transcriptOps(t, 4, opening)
	rig.transcriptOps(t, 5, opening)
	rig.transcriptOps(t, 6, `{"op":"frame.upsert","turnId":"t3","stepId":"t3.1","frame":{"kind":"tool","frameId":"t3.1.call_bash","toolCallId":"call_bash","name":"Bash","state":"running","input":{"command":"echo hi"},"progress":{"kind":"stdout","text":"hi\n"}}}`)
	rig.transcriptOps(t, 7, `{"op":"frame.upsert","turnId":"t3","stepId":"t3.1","frame":{"kind":"tool","frameId":"t3.1.call_bash","toolCallId":"call_bash","name":"Bash","state":"running","input":{"command":"echo hi"},"progress":{"kind":"status","text":"ignored"}}}`)
	rig.transcriptOps(t, 8, `{"op":"frame.upsert","turnId":"t3","stepId":"t3.1","frame":{"kind":"tool","frameId":"unknown","toolCallId":"unknown","state":"running","progress":{"kind":"stdout","text":"x"}}}`)
	rig.transcriptOps(t, 9, `{"op":"frame.upsert","turnId":"t3","stepId":"t3.1","frame":{"kind":"tool","frameId":"t3.1.call_bash","toolCallId":"call_bash","name":"Bash","state":"done","input":{"command":"echo hi"},"output":"hi\n"}}`)

	spanID := `session_1/kimi/"main"/"t3"/"t3.1"/"t3.1.call_bash"`

	messages := rig.sink.Messages()
	require.Len(t, messages, 3, "the text, the call and its result; a repeated start adds nothing")
	_, text := assembledRow(t, messages[0])
	assert.Equal(t, "Running it.", text, "the text before a call is flushed above it")
	assert.Equal(t, spanID, messages[1].SpanID)
	assert.Equal(t, contracts.KimiToolBash, messages[1].SpanType)
	assert.False(t, messages[1].Closing)
	assert.Equal(t, contracts.KimiEventTranscriptOps, eventType(t, messages[1]))
	assert.Equal(t, spanID, messages[2].SpanID)
	assert.True(t, messages[2].Closing)
	assert.Equal(t, contracts.KimiEventTranscriptOps, eventType(t, messages[2]))
	assert.Contains(t, rig.sink.ClosedSpans(), spanID)

	var total, tail bool
	for _, update := range rig.sink.ProgressUpdates() {
		if update.ScopeID != spanID {
			continue
		}
		switch update.Operation {
		case agent.ProgressOutputTotal:
			total = true
			assert.EqualValues(t, 3, update.Value)
			assert.True(t, update.Exact)
		case agent.ProgressOutputTail:
			tail = true
			assert.Equal(t, "hi\n", update.Text)
			assert.False(t, update.Truncated)
		default:
			// The span's reset and completion, which this test does not read.
		}
	}
	assert.True(t, total, "the running output counts toward the progress")
	assert.True(t, tail, "the running output is the card's live tail")

	rig.agent.Mu.Lock()
	uses := rig.agent.TurnToolUses
	rig.agent.Mu.Unlock()
	assert.Equal(t, 1, int(uses))
}

func TestKimiToolProgressKeepsALimitedTail(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"c","toolCallId":"c","name":"Bash","state":"running","input":{}}}`)
	chunk := strings.Repeat("x", kimiLiveOutputLimit)
	for index, progress := range []struct{ kind, text string }{{"stdout", chunk}, {"stderr", "tail-end"}} {
		rig.transcriptOps(t, int64(index+3), fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"c","toolCallId":"c","name":"Bash","state":"running","input":{},"progress":{"kind":%q,"text":%q}}}`, progress.kind, progress.text))
	}

	var last agent.ProgressUpdate
	var lastTotal int64
	for _, update := range rig.sink.ProgressUpdates() {
		switch update.Operation {
		case agent.ProgressOutputTail:
			last = update
		case agent.ProgressOutputTotal:
			lastTotal = update.Value
		default:
			// The model and span resets, which this test does not read.
		}
	}
	assert.True(t, last.Truncated, "the tail states that it lost earlier output")
	assert.LessOrEqual(t, len(last.Text), kimiLiveOutputLimit)
	assert.True(t, strings.HasSuffix(last.Text, "tail-end"))
	assert.EqualValues(t, kimiLiveOutputLimit+len("tail-end"), lastTotal, "the total counts every byte, not the tail")
}

func TestKimiTurnEndClosesTheCallsItLeftOpen(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"first","toolCallId":"first","name":"Bash","state":"running","input":{"command":"sleep 1"}}}`)
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"second","toolCallId":"second","name":"Grep","state":"running","input":{"pattern":"x"}}}`)
	rig.transcriptOps(t, 4, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"partial"}}`)
	rig.transcriptOps(t, 5, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"cancelled","origin":{"kind":"user"}}}`)

	messages := rig.sink.Messages()
	require.Len(t, messages, 6)
	_, text := assembledRow(t, messages[2])
	assert.Equal(t, "partial", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), assembledCompletion(t, messages[2]),
		"the text a cancelled turn left is marked as cut off")
	for i, id := range []string{"first", "second"} {
		closing := messages[3+i]
		assert.Equal(t, fmt.Sprintf(`session_1/kimi/"main"/"t0"/"t0.1"/%q`, id), closing.SpanID, "the calls close in the order they opened")
		assert.True(t, closing.Closing)
		assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion)
		assert.Equal(t, contracts.KimiEventTranscriptOps, eventType(t, closing), "the retained closing row keeps the native operation")
	}
	assert.True(t, messages[5].TurnEnd)
	assert.Equal(t, 1, rig.sink.ResetSpanCount())
}

func TestKimiTurnCompletion(t *testing.T) {
	t.Parallel()

	assert.Equal(t, agent.MessageCompletionComplete, kimiTurnCompletion("completed"))
	assert.Equal(t, agent.MessageCompletionInterrupted, kimiTurnCompletion("cancelled"))
	assert.Equal(t, agent.MessageCompletionError, kimiTurnCompletion("failed"))
	assert.Equal(t, agent.MessageCompletionError, kimiTurnCompletion("blocked"))
	assert.Equal(t, agent.MessageCompletionError, kimiTurnCompletion("paused"), "a reason this build does not know did not complete")
	assert.Equal(t, agent.MessageCompletionError, kimiTurnCompletion(""))
}

func TestKimiAnnouncesATurnTheAgentStartedByItself(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		origin, nativeKind string
		notes              bool
	}{
		{contracts.KimiOriginUser, "user", false},
		{contracts.KimiOriginTask, "task", false},
		{contracts.KimiOriginBackgroundTask, "task", false},
		{"", "", false},
		{contracts.KimiOriginSystemTrigger, "other", true},
		{contracts.KimiOriginCronJob, "cron", true},
		{contracts.KimiOriginRetry, "other", true},
		{contracts.KimiOriginHookResult, "hook", true},
	} {
		rig := newKimiOutputRig(t)
		rig.transcriptOps(t, 1, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":%q,"payload":{"kind":%q}}}}`, tc.nativeKind, tc.origin))
		if tc.notes {
			require.Equal(t, 1, rig.sink.NotificationCount(), "origin %q", tc.origin)
			record, valid := readKimiTranscriptRecord(rig.sink.LastNotification().Content)
			require.True(t, valid)
			assert.Equal(t, contracts.KimiTranscriptOperationTurnUpsert, record.operation.Op)
		} else {
			assert.Zero(t, rig.sink.NotificationCount(), "origin %q", tc.origin)
		}
	}
}

func TestKimiRecordsNotices(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"phase": "started", "trigger": "auto"})
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"phase": "completed"})
	rig.transcriptOps(t, 3, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","state":"running","ordinal":1,"retry":{"failedAttempt":1,"nextAttempt":2,"maxAttempts":5,"delayMs":0}}}`)
	rig.publishMarker(t, kimiMainAgentID, "notice", map[string]any{"level": "warning", "message": "A native warning."})
	rig.transcriptOps(t, 5, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.2","frame":{"kind":"text","frameId":"task-note","role":"user","taskId":"task-1","text":"The background task completed."}}`)
	assert.Equal(t, 4, rig.sink.NotificationCount())

	rig.agent.DiscardOutput()
	rig.publishMarker(t, kimiMainAgentID, "notice", map[string]any{"level": "warning", "message": "Discarded warning."})
	assert.Equal(t, 4, rig.sink.NotificationCount(), "an agent that discards its output records nothing")
}

func TestKimiManualCompactionReleasesTheInputQueue(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"trigger": "manual", "phase": "started"})

	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{

		"result": map[string]any{"summary": "Keep the task state.", "compactedCount": 3, "tokensBefore": 120, "tokensAfter": 20}, "phase": "completed",
	})
	last, published := rig.sink.LastTurnActive()
	assert.True(t, published, "the completed event must release the queue's compact turn")
	assert.False(t, last, "the completed event releases queued input")
}

func TestKimiAutoCompactionKeepsTheUserTurnActive(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"user"}}}`)
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"trigger": "auto", "phase": "started"})
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{

		"result": map[string]any{"summary": "Keep the task state.", "compactedCount": 3, "tokensBefore": 120, "tokensAfter": 20}, "phase": "completed",
	})
	last, published := rig.sink.LastTurnActive()
	assert.True(t, published)
	assert.True(t, last, "auto compaction does not release the user's active turn")
}

func TestKimiRepeatedCompactionCompletionKeepsTheNextTurnActive(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"trigger": "manual", "phase": "started"})
	completed := map[string]any{
		"phase": "completed", "result": map[string]any{"summary": "Keep the task state.", "compactedCount": 3, "tokensBefore": 120, "tokensAfter": 20},
	}
	rig.publishMarker(t, kimiMainAgentID, "compaction", completed)
	rig.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t2","state":"running","origin":{"kind":"user"}}}`)
	rig.publishMarker(t, kimiMainAgentID, "compaction", completed)
	last, published := rig.sink.LastTurnActive()
	assert.True(t, published)
	assert.True(t, last, "a duplicate completion cannot release the next turn")
}

func TestKimiManualCompactionWaitsForAnActualEnd(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"blocked", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"trigger": "manual", "phase": "started"})
			rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"phase": phase})
			last, published := rig.sink.LastTurnActive()
			assert.True(t, published)
			assert.Equal(t, phase == "blocked", last, "a blocked compaction still waits for its end")
		})
	}
}

func TestKimiCompactionStartIsLiveState(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"trigger": "auto", "phase": "started"})
	assert.Empty(t, rig.sink.PersistedNotifications())
	assert.Empty(t, rig.sink.Messages())
	value, exists := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Equal(t, map[string]any{contracts.CompactionFieldTrigger: "auto"}, value)
	rig.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)
	value, _ = rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.NotNil(t, value, "compaction can continue after the model turn ends")
}

func TestKimiBlockedCompactionIsLiveAndKeepsTheManualFlag(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"trigger": "manual", "phase": "started"})
	rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"phase": "blocked"})
	assert.True(t, rig.agent.manualCompactionActive)
	assert.Empty(t, rig.sink.PersistedNotifications())
	value, exists := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Equal(t, map[string]any{contracts.CompactionFieldTrigger: "waiting for the running turn to end"}, value)
}

func TestKimiCompactionEndClearsTheLiveKeyAndStoresTheEnd(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"completed", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			rig := newKimiOutputRig(t)
			rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"trigger": "manual", "phase": "started"})
			rig.publishMarker(t, kimiMainAgentID, "compaction", map[string]any{"phase": phase})
			assert.False(t, rig.agent.manualCompactionActive)
			require.Len(t, rig.sink.PersistedNotifications(), 1)
			record, valid := readKimiTranscriptRecord(rig.sink.PersistedNotifications()[0].Content)
			require.True(t, valid)
			marker, valid := decodeTranscriptMarker(record.operation)
			require.True(t, valid)
			assert.Equal(t, phase, marker.Payload.Phase)
			value, exists := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			assert.True(t, exists)
			assert.Nil(t, value)
		})
	}
}

func TestKimiChildCompactionPublishesOnTheChildSink(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	childID := adoptNativeKimiChild(t, rig.agent, "agent-1")
	for _, phase := range []string{"started", "cancelled"} {
		rig.publishMarker(t, "agent-1", "compaction", map[string]any{"phase": phase, "trigger": "auto"})
	}
	assert.Empty(t, rig.sink.SessionInfos())
	assert.Empty(t, rig.sink.PersistedNotifications())
	child := rig.sink.Child(childID)
	assert.Equal(t, []map[string]any{
		{contracts.SessionInfoKeyCompaction: map[string]any{contracts.CompactionFieldTrigger: "auto"}},
		{contracts.SessionInfoKeyCompaction: nil},
	}, child.SessionInfos())
	assert.Len(t, child.PersistedNotifications(), 1)
}

func TestKimiFailedTurn(t *testing.T) {
	t.Parallel()
	t.Run("the error that repeats the turn's failure is not recorded twice", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
		rig.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"user"},"error":"slow down"}}`)
		failure := map[string]any{"level": "error", "message": "slow down", "event": map[string]any{"agentId": "main", "code": "provider.rate_limited", "message": "slow down", "retryable": false}}
		rig.publishMarker(t, kimiMainAgentID, "notice", failure)
		assert.Zero(t, rig.sink.NotificationCount())
		assert.Equal(t, 2, rig.sink.AutoCancelCount(), "completion cancels older retry state and the notice confirms that this failure cannot retry")
		rig.publishMarker(t, kimiMainAgentID, "notice", failure)
		assert.Equal(t, 1, rig.sink.NotificationCount(), "a later distinct error is not the consumed failure notice")
		rig.publishMarker(t, kimiMainAgentID, "notice", map[string]any{"level": "error", "message": "different", "event": map[string]any{"agentId": "main", "code": "other", "message": "different"}})
		assert.Equal(t, 2, rig.sink.NotificationCount())
	})
	t.Run("a retryable failure schedules a retry", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"user"},"error":"busy"}}`)
		rig.publishMarker(t, kimiMainAgentID, "notice", map[string]any{"level": "error", "message": "busy", "event": map[string]any{"agentId": "main", "code": "provider.overloaded", "message": "busy", "retryable": true}})
		require.Equal(t, 1, rig.sink.AutoScheduleCount())
		assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, rig.sink.LastAutoSchedule().Kind)
	})
	t.Run("an unlinked subagent error waits for its transcript", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.publishMarker(t, "agent-0", "notice", map[string]any{"level": "error", "message": "y", "event": map[string]any{"agentId": "agent-0", "code": "x", "message": "y"}})
		assert.Zero(t, rig.sink.NotificationCount())
	})
}

func TestKimiWithContextUsage(t *testing.T) {
	t.Parallel()

	content := agent.MessageContent{Original: []byte(`{}`), Metadata: []byte(`{"num_tool_uses":2}`)}
	got := kimiWithContextUsage(content, map[string]any{"context_tokens": 5})
	assert.JSONEq(t, `{"num_tool_uses":2,"context_usage":{"context_tokens":5}}`, string(got.Metadata))

	assert.Equal(t, content, kimiWithContextUsage(content, nil), "no readout leaves the metadata alone")

	bad := agent.MessageContent{Metadata: []byte(`not json`)}
	assert.Equal(t, bad, kimiWithContextUsage(bad, map[string]any{"context_tokens": 5}))

	empty := kimiWithContextUsage(agent.MessageContent{}, map[string]any{"context_tokens": 5})
	assert.JSONEq(t, `{"context_usage":{"context_tokens":5}}`, string(empty.Metadata))
}

func TestKimiSpawnFromArgs(t *testing.T) {
	t.Parallel()

	spawn := kimiSpawnFromArgs("span", contracts.KimiToolAgent, json.RawMessage(`{"prompt":" Review it. ","description":" Review "}`))
	assert.Equal(t, kimiSpawn{spanID: "span", name: contracts.KimiToolAgent, prompt: "Review it.", label: "Review"}, spawn)

	swarm := kimiSpawnFromArgs("span", contracts.KimiToolAgentSwarm, json.RawMessage(`{"prompt":"ignored","description":"Audit"}`))
	assert.Empty(t, swarm.prompt, "a swarm's members state their own prompts")
	assert.Equal(t, "Audit", swarm.label)

	assert.Equal(t, kimiSpawn{spanID: "span", name: contracts.KimiToolAgent}, kimiSpawnFromArgs("span", contracts.KimiToolAgent, json.RawMessage(`[1]`)))
	assert.Equal(t, kimiSpawn{spanID: "span", name: contracts.KimiToolAgent}, kimiSpawnFromArgs("span", contracts.KimiToolAgent, nil))
}

func TestKimiRetainedRowCarriesThePrintedOutput(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"c","toolCallId":"c","name":"Bash","state":"running","input":{"command":"make"}}}`)
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"c","toolCallId":"c","name":"Bash","state":"running","input":{"command":"make"},"progress":{"kind":"stdout","text":"building...\n"}}}`)
	rig.transcriptOps(t, 4, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"cancelled","origin":{"kind":"user"}}}`)

	var closing agenttest.Message
	for _, message := range rig.sink.Messages() {
		if message.Closing {
			closing = message
		}
	}
	record, valid := readKimiTranscriptRecord(closing.Content)
	require.True(t, valid)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(record.operation.Frame, &frame))
	assert.Equal(t, "running", frame["state"])
	assert.NotContains(t, frame, "output")
	assert.NotContains(t, frame, "truncated")
	var supplement map[string]map[string]any
	require.NoError(t, json.Unmarshal(closing.SupplementalContent, &supplement))
	assert.Equal(t, "building...\n", supplement["toolOutput"]["text"])
	assert.Equal(t, false, supplement["toolOutput"]["truncated"])

}

func TestKimiRetainedNativeOutputHandlesEmptyAndMalformedRecords(t *testing.T) {
	t.Parallel()
	record := []byte(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","seq":1,"ops":[{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"c","toolCallId":"c","name":"Bash","state":"running","input":{}}}]}}`)
	content, native := kimiRetainedTranscriptToolContent(record, "", true)
	require.True(t, native)
	assert.Equal(t, record, content.Original)
	assert.Empty(t, content.Supplemental)
	for _, truncated := range []bool{false, true} {
		content, native = kimiRetainedTranscriptToolContent(record, "tail", truncated)
		require.True(t, native)
		assert.Equal(t, record, content.Original)
		var supplement map[string]map[string]any
		require.NoError(t, json.Unmarshal(content.Supplemental, &supplement))
		assert.Equal(t, "tail", supplement["toolOutput"]["text"])
		assert.Equal(t, truncated, supplement["toolOutput"]["truncated"])
	}
	for _, invalid := range []string{"not json", "null"} {
		content, native = kimiRetainedTranscriptToolContent([]byte(invalid), "tail", true)
		assert.False(t, native)
		assert.Equal(t, []byte(invalid), content.Original)
		assert.Empty(t, content.Supplemental)
	}
}

// A session resumed while a call ran reports the call's result with no start
// that this process saw. The result still closes the call's own span.
func TestKimiResultOfACallThisProcessNeverSawOpen(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t4","state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t4","stepId":"t4.1","frame":{"kind":"tool","frameId":"call_old","toolCallId":"call_old","name":"Read","state":"done","input":{},"output":"done"}}`)

	spanID := `session_1/kimi/"main"/"t4"/"t4.1"/"call_old"`

	messages := rig.sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, spanID, messages[0].SpanID)
	assert.True(t, messages[0].Closing)
	assert.Equal(t, contracts.KimiEventTranscriptOps, eventType(t, messages[0]))
	assert.Contains(t, rig.sink.ClosedSpans(), spanID)
	rig.agent.Mu.Lock()
	uses := rig.agent.TurnToolUses
	rig.agent.Mu.Unlock()
	assert.EqualValues(t, 1, uses, "the result counts toward the turn's tools")
}

// The server's tool call ids are the model's, and a later turn can repeat one.
func TestKimiRepeatedToolCallIDOfALaterTurnOpensItsOwnSpan(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	for turn := range 2 {
		id, step := fmt.Sprintf("t%d", turn), fmt.Sprintf("t%d.1", turn)
		seq := int64(turn*3 + 1)
		rig.transcriptOps(t, seq, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":%q,"state":"running","origin":{"kind":"user"}}}`, id))
		rig.transcriptOps(t, seq+1,
			fmt.Sprintf(`{"op":"frame.upsert","turnId":%q,"stepId":%q,"frame":{"kind":"tool","frameId":"call_1","toolCallId":"call_1","name":"Read","state":"running","input":{}}}`, id, step),
			fmt.Sprintf(`{"op":"frame.upsert","turnId":%q,"stepId":%q,"frame":{"kind":"tool","frameId":"call_1","toolCallId":"call_1","name":"Read","state":"done","input":{},"output":"ok"}}`, id, step))
		rig.transcriptOps(t, seq+2, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":%q,"state":"completed","origin":{"kind":"user"}}}`, id))
	}

	var spans []string
	for _, message := range rig.sink.Messages() {
		if message.SpanID != "" && !message.Closing {
			spans = append(spans, message.SpanID)
		}
	}
	assert.Equal(t, []string{
		`session_1/kimi/"main"/"t0"/"t0.1"/"call_1"`,
		`session_1/kimi/"main"/"t1"/"t1.1"/"call_1"`,
	}, spans, "the second call is not read as a repeat of the first")
}

// An agent that discards its output writes no row for what the turn left, but
// it still closes each open span, so no card stays running.
func TestKimiTurnEndOfAnAgentThatDiscardsItsOutput(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"call_1","toolCallId":"call_1","name":"Bash","state":"running","input":{}}}`)
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"never shown"}}`)
	before := rig.sink.MessageCount()
	rig.agent.DiscardOutput()
	rig.transcriptOps(t, 4, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"cancelled","origin":{"kind":"user"}}}`)

	written := rig.sink.Messages()[before:]
	require.Len(t, written, 1, "no text row and no retained call row")
	assert.True(t, written[0].TurnEnd, "only the turn end is written")
	assert.Contains(t, rig.sink.ClosedSpans(), `session_1/kimi/"main"/"t0"/"t0.1"/"call_1"`)
	last, _ := rig.sink.LastTurnActive()
	assert.False(t, last)
}

func TestKimiToolEventWithNoCallIDIsIgnored(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
	rig.transcriptOps(t, 2,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"missing","name":"Bash","state":"running","input":{}}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"missing","name":"Bash","state":"running","input":{},"progress":{"kind":"stdout","text":"x"}}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"missing","name":"Bash","state":"done","output":"ok"}}`)
	assert.Zero(t, rig.sink.MessageCount(), "a call with no id has no span to open or close")
	assert.Empty(t, rig.sink.ClosedSpans())
}

func TestKimiRetryKeepsTheFailedTextInterrupted(t *testing.T) {
	t.Parallel()
	// Native operations preserve the failed partial text before the successful retry.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "a retried Kimi step", FramesPath: "testdata/retry_step.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			return newOfflineKimiAgent(t, sink).HandleOutput
		},
		Want: []agenttest.RetryAttemptText{
			{Text: "DISCARDED-ONE DISCARDED-TWO ", Completion: agent.MessageCompletionInterrupted},
			{Text: "RETRIED-ANSWER", Completion: agent.MessageCompletionComplete},
		},
	})
}

func TestKimiRetryDiscardsAThinkingOnlyAttempt(t *testing.T) {
	t.Parallel()
	// Native retry boundaries discard reasoning that never reached an answer.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "a retried thought", FramesPath: "testdata/retry_thinking_only.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			return newOfflineKimiAgent(t, sink).HandleOutput
		},
		Want:      []agenttest.RetryAttemptText{{Text: "RETRIED-ANSWER", Completion: agent.MessageCompletionComplete}},
		Discarded: []string{"DISCARDED-THOUGHT-ONE", "DISCARDED-THOUGHT-TWO"},
	})
}

func TestKimiRetryOfASubagentStaysInItsTranscript(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	childID := adoptNativeKimiChild(t, rig.agent, "agent-1")
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"root-answer","role":"assistant","text":"Root answer."}}`)
	rig.transcriptAgentOps(t, "agent-1", 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"thinking","frameId":"thought","text":"Unsigned thought."}}`)
	rig.transcriptAgentOps(t, "agent-1", 2, kimiNativeRetryStep)
	rig.transcriptAgentOps(t, "agent-1", 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"partial","role":"assistant","text":"Child partial."}}`)
	rig.transcriptAgentOps(t, "agent-1", 4, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running","retry":{"failedAttempt":2,"nextAttempt":3,"maxAttempts":10,"delayMs":0}}}`)
	rig.transcriptAgentOps(t, "agent-1", 5, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Child answer."}}`)
	rig.transcriptAgentOps(t, "agent-1", 6, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"other"}}}`)
	assert.Empty(t, rig.sink.Messages())
	assert.Empty(t, rig.sink.PersistedNotifications())
	child := rig.sink.Child(childID)
	rows := child.Messages()
	require.Len(t, rows, 2)
	_, partial := assembledRow(t, rows[0])
	_, answer := assembledRow(t, rows[1])
	assert.Equal(t, "Child partial.", partial)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), assembledCompletion(t, rows[0]))
	assert.Equal(t, "Child answer.", answer)
	assert.Len(t, child.PersistedNotifications(), 2)
	assert.Len(t, child.LeapMuxNotifications(), 2)
	rig.transcriptOps(t, 2, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"completed"}}`)
	require.Len(t, rig.sink.Messages(), 1)
	_, root := assembledRow(t, rig.sink.Messages()[0])
	assert.Equal(t, "Root answer.", root)
}

func TestKimiRetryResolvesAtTheFirstOutput(t *testing.T) {
	t.Parallel()
	for _, output := range []struct{ name, operation string }{
		{"assistant text", `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Answer."}}`},
		{"reasoning", `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"thinking","frameId":"thought","text":"Thought."}}`},
		{"tool arguments", `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"call-1","toolCallId":"call-1","name":"","state":"running","inputText":""}}`},
		{"complete tool input", `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"call-1","toolCallId":"call-1","name":"Read","state":"running","input":{}}}`},
	} {
		t.Run(output.name, func(t *testing.T) {
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, kimiNativeRetryStep)
			assert.Empty(t, rig.sink.Notifications())
			rig.transcriptOps(t, 2, output.operation)
			rig.transcriptOps(t, 3, output.operation)
			require.Len(t, rig.sink.Notifications(), 1)
			assert.Equal(t, contracts.NotificationTypeApiRetryResolved, rig.sink.Notifications()[0][contracts.NotificationFieldType])
			if output.name == "tool arguments" {
				assert.Empty(t, rig.sink.Messages(), "argument streaming resolves retry without opening an incomplete call")
				assert.Empty(t, rig.sink.ClosedSpans())
			}
		})
	}
	t.Run("a turn end forgets an unresolved retry", func(t *testing.T) {
		rig := newKimiOutputRig(t)
		rig.transcriptOps(t, 1, kimiNativeRetryStep)
		rig.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"user"}}}`)
		rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Another turn."}}`)
		assert.Empty(t, rig.sink.Notifications())
	})
}

func TestKimiRetryDoesNotResolveEmptyOrDiscardedOutput(t *testing.T) {
	t.Parallel()
	for _, discard := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "discarded"}[discard], func(t *testing.T) {
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, kimiNativeRetryStep)
			text := ""
			if discard {
				rig.agent.DiscardOutput()
				text = "Discarded answer."
			}
			rig.transcriptOps(t, 2, fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":%q}}`, text))
			rig.transcriptOps(t, 3, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"completed"}}`)
			assert.Empty(t, rig.sink.Notifications())
			assert.Empty(t, rig.sink.Messages())
		})
	}
}
