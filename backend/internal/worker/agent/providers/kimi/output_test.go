package kimi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
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

func (r *kimiOutputRig) feed(t *testing.T, payload map[string]any) {
	t.Helper()
	r.agent.HandleOutput(kimiEventFrame(t, "session_1", payload))
}

func (r *kimiOutputRig) startTurn(t *testing.T, turnID int, origin string) {
	t.Helper()
	r.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": turnID, "origin": map[string]any{"kind": origin}, "prompt": "Do it."})
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
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventThinkingDelta, "turnId": 0, "delta": "Let me "})
	rig.feed(t, map[string]any{"type": contracts.KimiEventThinkingDelta, "turnId": 0, "delta": "think."})
	rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": "Hello "})
	rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": ""})
	rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": "world."})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStepCompleted, "turnId": 0})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": "completed"})

	messages := rig.sink.Messages()
	require.Len(t, messages, 3)
	kind, text := assembledRow(t, messages[0])
	assert.Equal(t, string(agent.AssembledMessageKindReasoning), kind)
	assert.Equal(t, "Let me think.", text, "the thinking is complete once the answer starts")
	kind, text = assembledRow(t, messages[1])
	assert.Equal(t, string(agent.AssembledMessageKindText), kind)
	assert.Equal(t, "Hello world.", text)
	assert.True(t, messages[2].TurnEnd)
	assert.Equal(t, contracts.KimiEventTurnEnded, eventType(t, messages[2]))

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
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "contextTokens": 1200, "maxContextTokens": 262144,
		"usage": map[string]any{"currentTurn": map[string]any{"inputOther": 10, "output": 5, "inputCacheRead": 100, "inputCacheCreation": 1}}})
	for _, id := range []string{"call_1", "call_2"} {
		rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": id, "name": contracts.KimiToolRead, "args": map[string]any{"path": "a.go"}})
		rig.feed(t, map[string]any{"type": contracts.KimiEventToolResult, "turnId": 0, "toolCallId": id, "output": "package a"})
	}
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": "completed"})

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

	info, ok := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok, "the status update broadcasts the context")
	assert.NotNil(t, info)
}

func TestKimiToolCallLifecycle(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 3, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 3, "delta": "Running it."})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallDelta, "turnId": 3, "toolCallId": "call_bash"})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 3, "toolCallId": "call_bash", "name": contracts.KimiToolBash, "args": map[string]any{"command": "echo hi"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 3, "toolCallId": "call_bash", "name": contracts.KimiToolBash, "args": map[string]any{"command": "echo hi"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolProgress, "turnId": 3, "toolCallId": "call_bash", "update": map[string]any{"kind": "stdout", "text": "hi\n"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolProgress, "turnId": 3, "toolCallId": "call_bash", "update": map[string]any{"kind": "status", "text": "ignored"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolProgress, "turnId": 3, "toolCallId": "call_unknown", "update": map[string]any{"kind": "stdout", "text": "x"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolResult, "turnId": 3, "toolCallId": "call_bash", "output": "hi\n"})

	spanID := kimiSpanID("session_1", kimiMainAgentID, 3, "call_bash")
	messages := rig.sink.Messages()
	require.Len(t, messages, 3, "the text, the call and its result; a repeated start adds nothing")
	_, text := assembledRow(t, messages[0])
	assert.Equal(t, "Running it.", text, "the text before a call is flushed above it")
	assert.Equal(t, spanID, messages[1].SpanID)
	assert.Equal(t, contracts.KimiToolBash, messages[1].SpanType)
	assert.False(t, messages[1].Closing)
	assert.Equal(t, contracts.KimiEventToolCallStarted, eventType(t, messages[1]))
	assert.Equal(t, spanID, messages[2].SpanID)
	assert.True(t, messages[2].Closing)
	assert.Equal(t, contracts.KimiEventToolResult, eventType(t, messages[2]))
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
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "c", "name": contracts.KimiToolBash, "args": map[string]any{}})
	chunk := strings.Repeat("x", kimiLiveOutputLimit)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolProgress, "turnId": 0, "toolCallId": "c", "update": map[string]any{"kind": "stdout", "text": chunk}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolProgress, "turnId": 0, "toolCallId": "c", "update": map[string]any{"kind": "stderr", "text": "tail-end"}})

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
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "first", "name": contracts.KimiToolBash, "args": map[string]any{"command": "sleep 1"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "second", "name": contracts.KimiToolGrep, "args": map[string]any{"pattern": "x"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": "partial"})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": "cancelled"})

	messages := rig.sink.Messages()
	require.Len(t, messages, 6)
	_, text := assembledRow(t, messages[2])
	assert.Equal(t, "partial", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), assembledCompletion(t, messages[2]),
		"the text a cancelled turn left is marked as cut off")
	for i, id := range []string{"first", "second"} {
		closing := messages[3+i]
		assert.Equal(t, kimiSpanID("session_1", kimiMainAgentID, 0, id), closing.SpanID, "the calls close in the order they opened")
		assert.True(t, closing.Closing)
		assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion)
		assert.Equal(t, contracts.KimiEventToolCallStarted, eventType(t, closing), "the call's own opening payload is its last row")
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
		origin string
		notes  bool
	}{
		{contracts.KimiOriginUser, false},
		{contracts.KimiOriginTask, false},
		{contracts.KimiOriginBackgroundTask, false},
		{"", false},
		{contracts.KimiOriginSystemTrigger, true},
		{contracts.KimiOriginCronJob, true},
		{contracts.KimiOriginRetry, true},
		{contracts.KimiOriginHookResult, true},
	} {
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, tc.origin)
		if tc.notes {
			require.Equal(t, 1, rig.sink.NotificationCount(), "origin %q", tc.origin)
			assert.Equal(t, contracts.KimiEventTurnStarted, eventType(t, rig.sink.LastNotification()))
		} else {
			assert.Zero(t, rig.sink.NotificationCount(), "origin %q", tc.origin)
		}
	}
}

func TestKimiRecordsNotices(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	for _, eventName := range []string{
		contracts.KimiEventCompactionStarted, contracts.KimiEventCompactionCompleted,
		contracts.KimiEventTurnStepRetrying, contracts.KimiEventWarning, contracts.KimiEventTaskNotified,
	} {
		rig.feed(t, map[string]any{"type": eventName})
	}
	assert.Equal(t, 5, rig.sink.NotificationCount())

	rig.agent.DiscardOutput()
	rig.feed(t, map[string]any{"type": contracts.KimiEventWarning})
	assert.Equal(t, 5, rig.sink.NotificationCount(), "an agent that discards its output records nothing")
}

func TestKimiFailedTurn(t *testing.T) {
	t.Parallel()

	t.Run("the error that repeats the turn's failure is not recorded twice", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, contracts.KimiOriginUser)
		failure := map[string]any{"code": "provider.rate_limited", "message": "slow down", "retryable": false}
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": "failed", "error": failure})
		rig.feed(t, map[string]any{"type": contracts.KimiEventError, "code": "provider.rate_limited", "message": "slow down"})
		assert.Zero(t, rig.sink.NotificationCount())
		assert.Equal(t, 1, rig.sink.AutoCancelCount(), "a failure that cannot be retried cancels a pending retry")

		rig.feed(t, map[string]any{"type": contracts.KimiEventError, "code": "provider.rate_limited", "message": "slow down"})
		assert.Equal(t, 1, rig.sink.NotificationCount(), "only the one repeat is dropped")
		rig.feed(t, map[string]any{"type": contracts.KimiEventError, "code": "other", "message": "different"})
		assert.Equal(t, 2, rig.sink.NotificationCount())
	})

	t.Run("a retryable failure schedules a retry", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, contracts.KimiOriginUser)
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": "failed",
			"error": map[string]any{"code": "provider.overloaded", "message": "busy", "retryable": true}})
		require.Equal(t, 1, rig.sink.AutoScheduleCount())
		assert.Equal(t, agent.AutoContinueReasonAPIError, rig.sink.LastAutoSchedule().Reason)
	})

	t.Run("an error of a subagent is recorded in its own transcript only", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": contracts.KimiEventError, "agentId": "agent-0", "code": "x", "message": "y"})
		assert.Zero(t, rig.sink.NotificationCount(), "an unlinked subagent's error waits for its transcript")
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
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "c", "name": contracts.KimiToolBash, "args": map[string]any{"command": "make"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolProgress, "turnId": 0, "toolCallId": "c", "update": map[string]any{"kind": "stdout", "text": "building...\n"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": "cancelled"})

	var closing agenttest.Message
	for _, message := range rig.sink.Messages() {
		if message.Closing {
			closing = message
		}
	}
	var row map[string]any
	require.NoError(t, json.Unmarshal(closing.Content, &row))
	assert.Equal(t, contracts.KimiEventToolCallStarted, row["type"], "the row is still the call's own opening payload")
	assert.Equal(t, "building...\n", row["output"])
	assert.NotContains(t, row, "truncated")
}

func TestKimiWithRetainedOutput(t *testing.T) {
	t.Parallel()

	frame := []byte(`{"type":"tool.call.started","toolCallId":"c"}`)
	assert.Equal(t, frame, kimiWithRetainedOutput(frame, "", true), "a call that printed nothing keeps its payload")
	assert.JSONEq(t, `{"type":"tool.call.started","toolCallId":"c","output":"tail","truncated":true}`, string(kimiWithRetainedOutput(frame, "tail", true)))
	assert.JSONEq(t, `{"type":"tool.call.started","toolCallId":"c","output":"tail"}`, string(kimiWithRetainedOutput(frame, "tail", false)))
	assert.Equal(t, []byte(`not json`), kimiWithRetainedOutput([]byte(`not json`), "tail", false))
	assert.Equal(t, []byte(`null`), kimiWithRetainedOutput([]byte(`null`), "tail", false))
}

// A session resumed while a call ran reports the call's result with no start
// that this process saw. The result still closes the call's own span.
func TestKimiResultOfACallThisProcessNeverSawOpen(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 4, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolResult, "turnId": 4, "toolCallId": "call_old", "output": "done"})

	spanID := kimiSpanID("session_1", kimiMainAgentID, 4, "call_old")
	messages := rig.sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, spanID, messages[0].SpanID)
	assert.True(t, messages[0].Closing)
	assert.Equal(t, contracts.KimiEventToolResult, eventType(t, messages[0]))
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
		rig.startTurn(t, turn, contracts.KimiOriginUser)
		rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": turn, "toolCallId": "call_1", "name": contracts.KimiToolRead, "args": map[string]any{}})
		rig.feed(t, map[string]any{"type": contracts.KimiEventToolResult, "turnId": turn, "toolCallId": "call_1", "output": "ok"})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": turn, "reason": contracts.KimiTurnEndCompleted})
	}
	var spans []string
	for _, message := range rig.sink.Messages() {
		if message.SpanID != "" && !message.Closing {
			spans = append(spans, message.SpanID)
		}
	}
	assert.Equal(t, []string{
		kimiSpanID("session_1", kimiMainAgentID, 0, "call_1"),
		kimiSpanID("session_1", kimiMainAgentID, 1, "call_1"),
	}, spans, "the second call is not read as a repeat of the first")
}

// An agent that discards its output writes no row for what the turn left, but
// it still closes each open span, so no card stays running.
func TestKimiTurnEndOfAnAgentThatDiscardsItsOutput(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_1", "name": contracts.KimiToolBash, "args": map[string]any{}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": "never shown"})
	before := rig.sink.MessageCount()

	rig.agent.DiscardOutput()
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": contracts.KimiTurnEndCancelled})

	written := rig.sink.Messages()[before:]
	require.Len(t, written, 1, "no text row and no retained call row")
	assert.True(t, written[0].TurnEnd, "only the turn end is written")
	assert.Contains(t, rig.sink.ClosedSpans(), kimiSpanID("session_1", kimiMainAgentID, 0, "call_1"))
	last, _ := rig.sink.LastTurnActive()
	assert.False(t, last)
}

func TestKimiToolEventWithNoCallIDIsIgnored(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "name": contracts.KimiToolBash, "args": map[string]any{}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolProgress, "turnId": 0, "update": map[string]any{"kind": "stdout", "text": "x"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolResult, "turnId": 0, "output": "ok"})
	assert.Zero(t, rig.sink.MessageCount(), "a call with no id has no span to open or close")
	assert.Empty(t, rig.sink.ClosedSpans())
}
