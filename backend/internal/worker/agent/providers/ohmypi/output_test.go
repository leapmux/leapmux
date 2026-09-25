package ohmypi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real frames from `omp 18.2.11` in rpc-ui mode (probe s1 and s2), shortened
// where a field is irrelevant to the case.
const (
	frameAssistantText = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"The user wants a greeting.","thinkingSignature":"reasoning_content"},{"type":"text","text":"Hello from the mock."}],"api":"openai-completions","provider":"mock","model":"mock-model","usage":{"input":1200,"output":30,"cacheRead":0,"cacheWrite":0,"totalTokens":1230,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0.0021}},"stopReason":"stop","timestamp":1790187118735}}`
	frameUserEcho      = `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"say hello"}],"attribution":"user","timestamp":1790187225212}}`
	frameToolResult    = `{"type":"message_end","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"probe-output\n"}],"details":{},"isError":false}}`
	frameBashStart     = `{"type":"tool_execution_start","toolCallId":"call_1","toolName":"bash","args":{"command":"echo probe-output"}}`
	frameBashUpdate    = `{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{"command":"echo probe-output"},"partialResult":{"content":[{"type":"text","text":"probe-output\n"}],"details":{}}}`
	frameBashEnd       = `{"type":"tool_execution_end","toolCallId":"call_1","toolName":"bash","result":{"content":[{"type":"text","text":"probe-output\n\n\nWall time: 0.05 seconds"}],"details":{"timeoutSeconds":300,"wallTimeMs":49.99}},"isError":false}`
	frameAgentEnd      = `{"type":"agent_end","isTerminal":true,"messages":[{"role":"user","content":[{"type":"text","text":"say hello"}]},{"role":"assistant","content":[{"type":"text","text":"Hello."}],"stopReason":"stop"}]}`
)

// TestTurnFrames lists the frames that move the turn flag. Every other frame of
// omp's vocabulary -- and one that does not exist yet -- must publish nothing.
func TestTurnFrames(t *testing.T) {
	t.Parallel()
	cases := []agenttest.TurnFrameCase{
		{Name: "agent_start", Line: `{"type":"agent_start"}`, Moves: true},
		{Name: "agent_end", Line: frameAgentEnd, Moves: true},
		// omp's report that a prompt started no run releases the arm that prompt
		// took, and it publishes so the input queue settles the dispatch.
		{Name: "prompt_result", Line: `{"type":"prompt_result","id":"leapmux-1","agentInvoked":false}`, Moves: true},
		{Name: "an unclaimed prompt failure", Line: `{"type":"response","id":"leapmux-1","command":"prompt","success":false,"error":"late"}`, Moves: true},
		{Name: "turn_start", Line: `{"type":"turn_start"}`},
		{Name: "turn_end", Line: `{"type":"turn_end","message":{"role":"assistant","content":[]}}`},
		{Name: "message_start", Line: `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
		{Name: "message_update", Line: `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"x"}}`},
		{Name: "message_end", Line: frameAssistantText},
		{Name: "tool_execution_start", Line: frameBashStart},
		{Name: "tool_execution_end", Line: frameBashEnd},
		{Name: "auto_retry_start", Line: `{"type":"auto_retry_start","attempt":1,"maxAttempts":10,"delayMs":92,"errorMessage":"400"}`},
		{Name: "auto_compaction_start", Line: `{"type":"auto_compaction_start","reason":"threshold","action":"snapcompact"}`},
		{Name: "todo_reminder", Line: `{"type":"todo_reminder","todos":[{"content":"Write code","status":"in_progress"}],"attempt":1,"maxAttempts":3}`},
		{Name: "notice", Line: `{"type":"notice","level":"info","message":"hi"}`},
		{Name: "extension_ui_request setWidget", Line: `{"type":"extension_ui_request","id":"w1","method":"setWidget","widgetKey":"autoresearch"}`},
		{Name: "advisor_cost_changed", Line: `{"type":"advisor_cost_changed"}`},
		{Name: "a frame a later omp adds", Line: `{"type":"turn_spinning_up"}`},
	}
	agenttest.AssertTurnFrames(t, cases, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		r := newRig(t)
		r.emit(tc.Line)
		return r.sink.TurnActives()
	})
}

func TestTheTurnSpansRetriesAndEndsAtAgentEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := testutil.DeadlineContext(t)
	start := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	r.clock.Set(start).MustWait(ctx)

	r.emit(`{"type":"agent_start"}`)
	r.awaitStatsRead()
	r.clock.Set(start.Add(2 * time.Second)).MustWait(ctx)
	// A retry starts a second run with no agent_end between.
	r.emit(`{"type":"auto_retry_start","attempt":1,"maxAttempts":10,"delayMs":92,"errorMessage":"400"}`, `{"type":"agent_start"}`)
	r.emit(frameBashStart, frameBashEnd)
	r.clock.Set(start.Add(5 * time.Second)).MustWait(ctx)
	r.emit(frameAgentEnd)

	messages := r.sink.Messages()
	end := messages[len(messages)-1]
	require.True(t, end.TurnEnd)
	var metadata map[string]json.Number
	require.NoError(t, json.Unmarshal(end.Metadata, &metadata))
	assert.Equal(t, json.Number("5000"), metadata[contracts.MessageMetadataFieldDurationMs], "the turn is timed from its FIRST run")
	assert.Equal(t, json.Number("1"), metadata[contracts.MessageMetadataFieldToolUses])
	lifecycle := r.sink.TurnLifecycle()
	require.GreaterOrEqual(t, len(lifecycle), 3)
	assert.Equal(t, []string{"turn_end", "reset_spans", "turn_active:false"}, lifecycle[len(lifecycle)-3:],
		"the turn end and the span reset reach the sink before the flag clears")
	assert.Equal(t, 1, r.sink.ResetSpanCount())
}

func TestARunOmpStartedByItselfIsATurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)

	// A background job finished: omp starts a run with no prompt.
	r.emit(`{"type":"agent_start"}`)
	active, _ := r.sink.LastTurnActive()
	assert.True(t, active)
	r.emit(`{"type":"agent_end","isTerminal":true,"messages":[]}`)
	active, _ = r.sink.LastTurnActive()
	assert.False(t, active)
}

func TestAnAgentEndThatOmpContinuesStillEndsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`, `{"type":"agent_end","isTerminal":false,"messages":[]}`)

	active, _ := r.sink.LastTurnActive()
	assert.False(t, active, "omp reports the session idle; the scheduled work starts a turn of its own")
	messages := r.sink.Messages()
	require.NotEmpty(t, messages)
	assert.True(t, messages[len(messages)-1].TurnEnd)
}

func TestAnErrorEndsTheTurnAsAnError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"partial"}}`,
		`{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"error","errorMessage":"400 bad request"}]}`)

	var kept *agenttest.Message
	for i, message := range r.sink.Messages() {
		if !message.TurnEnd && message.SpanID == "" {
			kept = &r.sink.Messages()[i]
		}
	}
	require.NotNil(t, kept, "text no message_end completed is kept")
	assert.Contains(t, string(kept.Content), string(agent.MessageCompletionError))
}

func TestMessageEndRoles(t *testing.T) {
	t.Parallel()

	t.Run("an assistant message carries the session usage", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameAssistantText)
		messages := r.sink.Messages()
		// The first row is the reply's thinking (see
		// TestAnAssistantMessagePersistsItsThinkingAsARowOfItsOwn).
		require.Len(t, messages, 2)
		assert.JSONEq(t, frameAssistantText, string(messages[1].Content), "omp's own frame is the row")
		var metadata map[string]any
		require.NoError(t, json.Unmarshal(messages[1].Metadata, &metadata))
		assert.InDelta(t, 0.0021, metadata[contracts.SessionInfoKeyTotalCostUsd], 1e-9)
		assert.NotNil(t, metadata[contracts.SessionInfoKeyContextUsage])
		assert.NotZero(t, r.sink.SessionInfoCount(), "the usage is broadcast")
	})

	t.Run("the user echo and a tool result are dropped", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameUserEcho, frameToolResult, `{"type":"message_end","message":{"role":"bashExecution","command":"ls"}}`)
		assert.Empty(t, r.sink.Messages())
	})

	t.Run("a displayed custom message is kept and a hidden one is dropped", func(t *testing.T) {
		r := newRig(t)
		r.emit(
			`{"type":"message_end","message":{"role":"custom","customType":"async-result","content":"Background job done.","display":true,"details":{"jobs":[]}}}`,
			`{"type":"message_end","message":{"role":"custom","customType":"magic-keyword","content":"hidden","display":false}}`,
		)
		messages := r.sink.Messages()
		require.Len(t, messages, 1)
		assert.Contains(t, string(messages[0].Content), "Background job done.")
	})

	t.Run("an unknown role reaches the transcript", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"message_end","message":{"role":"hologram","content":"?"}}`)
		assert.Len(t, r.sink.Messages(), 1)
	})

	t.Run("a message_end resets the streamed text", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"Hello from the mock."}}`, frameAssistantText)
		r.emit(`{"type":"agent_start"}`, frameAgentEnd)
		var rows []agenttest.Message
		for _, message := range r.sink.Messages() {
			if !message.TurnEnd {
				rows = append(rows, message)
			}
		}
		require.Len(t, rows, 2, "no second copy of the streamed text")
		assert.Equal(t, reasoningRow("The user wants a greeting."), assembledRow(t, rows[0]))
		assert.JSONEq(t, frameAssistantText, string(rows[1].Content))
	})
}

// assembledRow decodes one row that the worker wrote in LeapMux's assembled-message
// envelope.
func assembledRow(t *testing.T, message agenttest.Message) map[string]string {
	t.Helper()
	var row map[string]string
	require.NoError(t, json.Unmarshal(message.Content, &row))
	return row
}

// reasoningRow is the assembled-message envelope of one completed thinking row.
func reasoningRow(text string) map[string]string {
	return map[string]string{
		contracts.AssembledMessageFieldType:       contracts.AssembledMessageType,
		contracts.AssembledMessageFieldKind:       contracts.AssembledMessageKindReasoning,
		contracts.AssembledMessageFieldText:       text,
		contracts.AssembledMessageFieldCompletion: contracts.AssembledMessageCompletionComplete,
	}
}

// assembledTextRow is the assembled-message envelope of one row of streamed text
// that no message_end completed.
func assembledTextRow(text string, completion agent.MessageCompletion) map[string]string {
	return map[string]string{
		contracts.AssembledMessageFieldType:       contracts.AssembledMessageType,
		contracts.AssembledMessageFieldKind:       contracts.AssembledMessageKindText,
		contracts.AssembledMessageFieldText:       text,
		contracts.AssembledMessageFieldCompletion: string(completion),
	}
}

// omp states a reasoning model's reply as ONE message that holds a thinking block
// and a text block. One transcript row draws one kind of text, so the worker
// persists the thinking as a row of its own, before the reply's own row.
func TestAnAssistantMessagePersistsItsThinkingAsARowOfItsOwn(t *testing.T) {
	t.Parallel()

	t.Run("the thinking row comes before the reply, which keeps the usage", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameAssistantText)
		messages := r.sink.Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, reasoningRow("The user wants a greeting."), assembledRow(t, messages[0]))
		assert.Empty(t, messages[0].Metadata, "the usage rides on the reply's own row")
		assert.JSONEq(t, frameAssistantText, string(messages[1].Content), "omp's own frame is still the reply's row")
		assert.NotEmpty(t, messages[1].Metadata)
	})

	t.Run("several thinking blocks join into one row", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"First."},{"type":"text","text":"Hi."},{"type":"thinking","thinking":"Second."}],"stopReason":"stop"}}`)
		messages := r.sink.Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, reasoningRow("First.\n\nSecond."), assembledRow(t, messages[0]))
	})

	t.Run("thinking beside a tool call is a row of its own too", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Run it."},{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"ls"}}],"stopReason":"toolUse"}}`)
		messages := r.sink.Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, reasoningRow("Run it."), assembledRow(t, messages[0]))
	})

	t.Run("a thinking block with no visible text adds no row", func(t *testing.T) {
		r := newRig(t)
		frame := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"  ","thinkingSignature":"sig"},{"type":"redactedThinking","data":"opaque"},{"type":"text","text":"Hi."}],"stopReason":"stop"}}`
		r.emit(frame)
		messages := r.sink.Messages()
		require.Len(t, messages, 1)
		assert.JSONEq(t, frame, string(messages[0].Content))
	})

	t.Run("a subagent's thinking reaches the child transcript", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameTaskStart, frameStarted)
		childID := subagentRow(t, r, "Probe").ChildAgentID
		frame := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Count the fruit."},{"type":"text","text":"Three."}],"stopReason":"stop"}}`
		r.emit(subagentEvent("Probe", frame))
		messages := r.sink.Child(childID).Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, reasoningRow("Count the fruit."), assembledRow(t, messages[0]))
		assert.JSONEq(t, frame, string(messages[1].Content))
	})
}

func TestMessageUpdateReportsProgress(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"abcd"},"message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"efgh"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":2,"delta":"{\"command\":"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":""}}`,
	)
	assert.Equal(t, int64(2), r.sink.LastThinkingTokens(), "text and thinking deltas count; a tool-call delta does not")
}

func TestToolCallsOpenAndCloseTheirSpan(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart, frameBashUpdate, frameBashEnd)

	messages := r.sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, "call_1", messages[0].SpanID)
	assert.False(t, messages[0].Closing)
	assert.NotZero(t, messages[0].SpanColor, "a tool call reserves a rail color")
	assert.Equal(t, "call_1", messages[1].SpanID)
	assert.True(t, messages[1].Closing)
	assert.Equal(t, "bash", messages[1].SpanType)
	assert.Contains(t, r.sink.ClosedSpans(), "call_1")
	assert.False(t, r.agent.HasCumulativeOutputForTest("call_1"), "the live output of an ended call is released")

	var sawTail bool
	for _, update := range r.sink.ProgressUpdates() {
		if update.Operation == agent.ProgressOutputTail && update.ScopeID == "call_1" && update.Text == "probe-output\n" {
			sawTail = true
		}
	}
	assert.True(t, sawTail, "a running call's output streams as a live tail")
}

func TestATaskCallOwnsNoSpan(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"task","args":{"context":"c","tasks":[{"name":"ScoutOne","agent":"task","task":"Say hello."}]}}`)
	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.True(t, messages[0].NoSpan, "a spawn draws no rail")
	assert.Zero(t, messages[0].SpanColor)
	assert.Empty(t, r.sink.OpenSpans())
}

func TestAnUpdateAfterItsCallEndedIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"task","args":{"tasks":[{"name":"ScoutOne","task":"t"}]}}`,
		`{"type":"tool_execution_end","toolCallId":"call_1","toolName":"task","result":{"content":[{"type":"text","text":"Spawned agent ScoutOne"}],"details":{"async":{"state":"running","jobId":"ScoutOne","type":"task"}}},"isError":false}`,
		// omp keeps updating a background task's call after its end.
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"task","args":{},"partialResult":{"content":[{"type":"text","text":"Running agent ScoutOne..."}],"details":{}}}`,
		frameAgentEnd,
	)
	for _, message := range r.sink.Messages() {
		if message.SpanID == "call_1" {
			assert.NotEqual(t, agent.MessageCompletionInterrupted, message.Completion,
				"the ended call is not persisted again as an incomplete call")
		}
	}
	closing := 0
	for _, message := range r.sink.Messages() {
		if message.SpanID == "call_1" && message.Closing {
			closing++
		}
	}
	assert.Equal(t, 1, closing)
}

func TestIncompleteToolsKeepTheirOrderAndPartialResult(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"tool_execution_start","toolCallId":"b","toolName":"bash","args":{"command":"one"}}`,
		`{"type":"tool_execution_start","toolCallId":"a","toolName":"read","args":{"path":"x"}}`,
		`{"type":"tool_execution_update","toolCallId":"b","toolName":"bash","args":{},"partialResult":{"content":[{"type":"text","text":"so far"}],"details":{}}}`,
		`{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"aborted"}]}`,
	)
	var closing []agenttest.Message
	for _, message := range r.sink.Messages() {
		if message.Closing {
			closing = append(closing, message)
		}
	}
	require.Len(t, closing, 2)
	assert.Equal(t, "b", closing[0].SpanID, "the call that started first closes first")
	assert.Equal(t, "a", closing[1].SpanID)
	assert.Equal(t, agent.MessageCompletionInterrupted, closing[0].Completion)
	var supplement contracts.OhMyPiIncompleteToolSupplement
	require.NoError(t, json.Unmarshal(closing[0].SupplementalContent, &supplement))
	assert.Equal(t, "b", supplement.ToolCallID)
	assert.Equal(t, "bash", supplement.ToolName)
	assert.Contains(t, string(supplement.PartialResult), "so far")
	assert.Empty(t, closing[1].SupplementalContent, "a call that reported nothing keeps no supplement")
}

// outputUpdates returns the live-output updates of one call, in order.
func outputUpdates(r *rig, toolCallID string, operation agent.ProgressOperation) []agent.ProgressUpdate {
	var out []agent.ProgressUpdate
	for _, update := range r.sink.ProgressUpdates() {
		if update.Operation == operation && update.ScopeID == toolCallID {
			out = append(out, update)
		}
	}
	return out
}

// partialUpdate is a tool_execution_update of call_1 whose partial result holds
// the text and the details given.
func partialUpdate(t *testing.T, text string, details map[string]any) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"type": "tool_execution_update", "toolCallId": "call_1", "toolName": "bash", "args": map[string]any{},
		"partialResult": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
			"details": details,
		},
	})
}

func TestOutputReportingWithATruncatedResult(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart, partialUpdate(t, "tail only", map[string]any{"meta": map[string]any{"truncation": map[string]any{"totalBytes": 90000}}}))

	totals := outputUpdates(r, "call_1", agent.ProgressOutputTotal)
	require.Len(t, totals, 1, "only omp's own total is reported: the text is not the whole output")
	assert.True(t, totals[0].Exact)
	assert.Equal(t, int64(90000), totals[0].Value, "omp's own total counts the whole output")
	tails := outputUpdates(r, "call_1", agent.ProgressOutputTail)
	require.Len(t, tails, 1)
	assert.Equal(t, "tail only", tails[0].Text)
	assert.True(t, tails[0].Truncated, "the tail states that earlier output is missing")
}

// A truncation that states no total still states that output is missing, and
// reports no total at all rather than the length of the tail.
func TestOutputReportingWithATruncationThatStatesNoTotal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart, partialUpdate(t, "tail only", map[string]any{"meta": map[string]any{"truncation": map[string]any{}}}))

	assert.Empty(t, outputUpdates(r, "call_1", agent.ProgressOutputTotal))
	tails := outputUpdates(r, "call_1", agent.ProgressOutputTail)
	require.Len(t, tails, 1)
	assert.True(t, tails[0].Truncated)
}

// A result that kept its head grows by appends, so its length is the total. The
// live tail keeps only the last liveOutputLimit bytes of it.
func TestOutputReportingOfALongResultKeepsTheTail(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	text := strings.Repeat("a", liveOutputLimit) + strings.Repeat("b", 100)
	r.emit(frameBashStart, partialUpdate(t, text, map[string]any{}))

	totals := outputUpdates(r, "call_1", agent.ProgressOutputTotal)
	require.Len(t, totals, 1)
	assert.Equal(t, int64(len(text)), totals[0].Value)
	assert.False(t, totals[0].Exact)
	tails := outputUpdates(r, "call_1", agent.ProgressOutputTail)
	require.Len(t, tails, 1)
	assert.Len(t, tails[0].Text, liveOutputLimit)
	assert.True(t, strings.HasSuffix(tails[0].Text, strings.Repeat("b", 100)), "the tail is the LAST bytes")
	assert.True(t, tails[0].Truncated, "the tail states that earlier output is missing")
}

func TestOutputReportingOfAResultWithNoText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart,
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{},"partialResult":{"content":[{"type":"image","data":"x"}],"details":{}}}`,
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{},"partialResult":"garbled"}`,
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{}}`)

	assert.Empty(t, outputUpdates(r, "call_1", agent.ProgressOutputTotal))
	assert.Empty(t, outputUpdates(r, "call_1", agent.ProgressOutputTail))
}

// A tool frame without a call id cannot be matched to its call, so the worker
// drops it rather than open a span that nothing closes.
func TestAToolFrameWithNoCallIDIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"tool_execution_start","toolName":"bash","args":{"command":"ls"}}`,
		`{"type":"tool_execution_update","toolName":"bash","partialResult":{"content":[{"type":"text","text":"x"}]}}`,
		`{"type":"tool_execution_end","toolName":"bash","result":{"content":[]},"isError":false}`,
		`{"type":"tool_execution_start","toolCallId":7}`,
	)
	assert.Empty(t, r.sink.Messages())
	assert.Empty(t, r.sink.OpenSpans())
	assert.Empty(t, r.sink.ProgressUpdates())
}

func TestAMalformedMessageEndReachesTheTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	frame := `{"type":"message_end","message":7}`
	r.emit(frame)
	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.JSONEq(t, frame, string(messages[0].Content), "the reader can still inspect it")
}

// The run's text is complete at an end that a pending steer continues: only the
// turn goes on. The text is persisted once, as complete, and not again when the
// turn ends.
func TestAnAgentEndThatAQueuedSteerContinuesPersistsTheRunsText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"First run's text"}}`)
	require.NoError(t, r.agent.SteerInput("also check the tests", nil))
	r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)

	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, assembledTextRow("First run's text", agent.MessageCompletionComplete), assembledRow(t, messages[0]))

	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"also check the tests"}]}}`,
		frameAgentEnd)
	rows := 0
	for _, message := range r.sink.Messages() {
		if strings.Contains(string(message.Content), "First run's text") {
			rows++
		}
	}
	assert.Equal(t, 1, rows)
}

func TestAgentEndCompletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame string
		want  agent.MessageCompletion
	}{
		{name: "an error stop", frame: `{"messages":[{"role":"assistant","stopReason":"error"}]}`, want: agent.MessageCompletionError},
		{name: "an abort", frame: `{"messages":[{"role":"assistant","stopReason":"aborted"}]}`, want: agent.MessageCompletionInterrupted},
		{name: "the last assistant message decides", frame: `{"messages":[{"role":"assistant","stopReason":"error"},{"role":"user"},{"role":"assistant","stopReason":"stop"}]}`, want: agent.MessageCompletionInterrupted},
		{name: "a message after the last assistant message is skipped", frame: `{"messages":[{"role":"assistant","stopReason":"error"},{"role":"toolResult"},{"role":"user"}]}`, want: agent.MessageCompletionError},
		{name: "no messages", frame: `{}`, want: agent.MessageCompletionInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var env agentEndEnvelope
			require.NoError(t, json.Unmarshal([]byte(tc.frame), &env))
			assert.Equal(t, tc.want, env.completion())
		})
	}
}

func TestTurnDurationMs(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	assert.Nil(t, turnDurationMs(time.Time{}, start), "a turn whose start this worker never saw")
	assert.Nil(t, turnDurationMs(start, start.Add(-time.Millisecond)), "a clock that moved backwards")
	zero := turnDurationMs(start, start)
	require.NotNil(t, zero, "a real zero is stated, not left out")
	assert.Equal(t, int64(0), *zero)
	long := turnDurationMs(start, start.Add(90*time.Minute+1500*time.Millisecond))
	require.NotNil(t, long)
	assert.Equal(t, int64(5401500), *long)
}

func TestMessageText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "a string", content: `"Do the work."`, want: "Do the work."},
		{name: "text blocks", content: `[{"type":"text","text":"First."},{"type":"image","data":"x"},{"type":"text","text":""},{"type":"text","text":"Second."}]`, want: "First.\nSecond."},
		{name: "no blocks", content: `[]`, want: ""},
		{name: "no content", content: ``, want: ""},
		{name: "another shape", content: `7`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, messageText(json.RawMessage(tc.content)))
		})
	}
}

func TestJoinOutputTail(t *testing.T) {
	t.Parallel()
	blocks := []contentBlock{{Type: "text", Text: "héllo "}, {Type: "image"}, {Type: "text", Text: "world"}}
	total := len("héllo ") + len("world")

	whole, clipped := joinOutputTail(blocks, total, 0)
	assert.Equal(t, "héllo world", whole)
	assert.False(t, clipped)

	tail, clipped := joinOutputTail(blocks, total, 9)
	assert.True(t, clipped)
	assert.Equal(t, "llo world", tail, "the cut moves to a rune boundary")

	empty, clipped := joinOutputTail(nil, 0, 8)
	assert.Empty(t, empty)
	assert.False(t, clipped)
}

// turnEndRows returns the turn-end rows, in order.
func turnEndRows(messages []agenttest.Message) []agenttest.Message {
	var ends []agenttest.Message
	for _, message := range messages {
		if message.TurnEnd {
			ends = append(ends, message)
		}
	}
	return ends
}

// omp ends a run with isTerminal:false when a steer that arrived at its tail
// continues the work at once. The steer is LeapMux's own input to the running
// turn, so the turn stays open across the boundary: no divider, no idle state,
// and the next run belongs to the same turn.
func TestAnAgentEndThatAQueuedSteerContinuesKeepsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`, frameBashStart, frameBashEnd)
	require.NoError(t, r.agent.SteerInput("also check the tests", nil))
	r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)

	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "the queued steer continues the turn")
	assert.Empty(t, turnEndRows(r.sink.Messages()), "no divider between the two runs")

	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"also check the tests"}]}}`,
		frameAgentEnd)

	active, _ = r.sink.LastTurnActive()
	assert.False(t, active)
	ends := turnEndRows(r.sink.Messages())
	require.Len(t, ends, 1, "one turn, one divider")
	var metadata map[string]json.Number
	require.NoError(t, json.Unmarshal(ends[0].Metadata, &metadata))
	assert.Equal(t, json.Number("1"), metadata[contracts.MessageMetadataFieldToolUses], "the turn counts the tool of its first run")
	for _, published := range r.sink.TurnActives()[:len(r.sink.TurnActives())-1] {
		assert.True(t, published, "no idle state is published before the turn ends")
	}
}

// A steer that never enters a run continues nothing. An end with `isTerminal:
// false` then comes from other scheduled work, which can start much later, so
// the end must end the turn. Otherwise the tab stays busy until that work runs.
//
// omp runs a built-in slash command BEFORE it reads `streamingBehavior`, and
// answers `agentInvoked:false` (rpc-mode.ts, the `prompt` case). An extension
// command resolves local-only after the acknowledgement, with a prompt_result.
// A steer can also fail after omp acknowledged it.
func TestASteerThatNeverEntersARunContinuesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// reply answers the steer's prompt command.
		reply func(t *testing.T, command recordedCommand) *rigReply
		// after returns the frames that omp sends after the acknowledgement.
		after func(t *testing.T, id string) []string
	}{
		{
			name: "a slash command omp answered itself",
			reply: func(*testing.T, recordedCommand) *rigReply {
				return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`)}
			},
		},
		{
			name: "a steer that resolved local-only after the acknowledgement",
			after: func(t *testing.T, id string) []string {
				return []string{mustJSON(t, map[string]any{"type": "prompt_result", "id": id, "agentInvoked": false})}
			},
		},
		{
			name: "a steer that failed after the acknowledgement",
			after: func(t *testing.T, id string) []string {
				return []string{mustJSON(t, map[string]any{"type": "response", "id": id, "command": "prompt", "success": false, "error": "preflight failed"})}
			},
		},
		{
			// The worker reads the acknowledgement on another goroutine than the
			// read loop, so the read loop can handle a later frame of the same
			// steer first.
			name: "a local-only result that the read loop handled before the acknowledgement",
			reply: func(t *testing.T, command recordedCommand) *rigReply {
				return &rigReply{Before: []string{mustJSON(t, map[string]any{"type": "prompt_result", "id": command.ID, "agentInvoked": false})}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.respond(func(command recordedCommand) *rigReply {
				if command.Type == CommandPrompt && tc.reply != nil {
					return tc.reply(t, command)
				}
				return nil
			})
			r.emit(`{"type":"agent_start"}`)
			require.NoError(t, r.agent.SteerInput("/session info", nil))
			if tc.after != nil {
				r.emit(tc.after(t, r.commandsOfType(CommandPrompt)[0].ID)...)
			}

			r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)
			active, _ := r.sink.LastTurnActive()
			assert.False(t, active, "no steer continues the turn")
			assert.Len(t, turnEndRows(r.sink.Messages()), 1)
		})
	}
}

// A steer that omp took, and a second steer that it answered itself: the first
// one still continues the turn.
func TestASteerThatOmpTookStillContinuesTheTurnBesideALocalOne(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt && command.Payload["message"] == "/session info" {
			return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`)}
		}
		return nil
	})
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, r.agent.SteerInput("also check the tests", nil))
	require.NoError(t, r.agent.SteerInput("/session info", nil))

	r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)
	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "the steer that omp queued continues the turn")
	assert.Empty(t, turnEndRows(r.sink.Messages()))

	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"also check the tests"}]}}`,
		`{"type":"agent_end","isTerminal":false,"messages":[]}`)
	active, _ = r.sink.LastTurnActive()
	assert.False(t, active, "the queued steer was taken, and nothing else continues the turn")
	assert.Len(t, turnEndRows(r.sink.Messages()), 1)
}

// A steer that omp already took into the run continues nothing, so a later
// end with `isTerminal: false` ends the turn, as any other one does.
func TestAnAgentEndThatOmpContinuesAfterTheSteerWasTakenEndsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, r.agent.SteerInput("also check the tests", nil))
	r.emit(`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"also check the tests"}]}}`,
		`{"type":"agent_end","isTerminal":false,"messages":[]}`)

	active, _ := r.sink.LastTurnActive()
	assert.False(t, active)
	assert.Len(t, turnEndRows(r.sink.Messages()), 1)
}
