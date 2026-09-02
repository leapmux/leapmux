package agent

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- turn lifecycle ---

func TestHandleZCodeOutput_TurnStarted_ArmsTheUsersTurn(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.turnToolUses = 4

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"hi"}`))

	a.mu.Lock()
	turnActive, background, toolUses, seq := a.turnActive, a.backgroundTurn, a.turnToolUses, a.lastSeq
	a.mu.Unlock()
	assert.True(t, turnActive)
	assert.False(t, background)
	assert.Equal(t, 0, toolUses, "a fresh user turn resets the tool-use count")
	assert.Equal(t, int64(1), seq, "the sequence must advance so a re-subscribe resumes from it")
	assert.Equal(t, 0, sink.MessageCount(), "turn.started persists nothing")
}

// A turn the RUNTIME started (a background task reporting back, a subagent's reply)
// must not reset or end the user's turn.
func TestHandleZCodeOutput_TurnStarted_WithAnInputSourceIsABackgroundTurn(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		ZCodeInputSourceBackgroundTask,
		ZCodeInputSourceSubagent,
		ZCodeInputSourceTodoReminder,
		ZCodeInputSourceGoalContinuation,
	} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, &recordingControlSink{})
			a.turnToolUses = 3

			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputSource":"`+source+`"}`))

			a.mu.Lock()
			background, toolUses := a.backgroundTurn, a.turnToolUses
			a.mu.Unlock()
			assert.True(t, background)
			assert.Equal(t, 3, toolUses, "a background turn must not clear the user turn's count")
		})
	}
}

func TestHandleZCodeOutput_TurnCompleted_PersistsTheDividerAndResetsSpans(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.turnActive = true
	a.toolCalls["call-1"] = &zcodeToolCall{final: true}

	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventTurnCompleted,
		`{"response":"done","toolCallCount":3,"resultType":"success","duration":1200,
		  "usage":{"inputTokens":100,"outputTokens":20,"totalTokens":120}}`))

	a.mu.Lock()
	turnActive, toolUses, final := a.turnActive, a.turnToolUses, len(a.toolCalls)
	a.mu.Unlock()
	assert.False(t, turnActive)
	assert.Equal(t, 3, toolUses, "the count comes from the turn's own toolCallCount")
	assert.Equal(t, 0, final, "the per-call side table is cleared with the turn")

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.True(t, msg.TurnEnd, "turn.completed must route through PersistTurnEnd")
	assert.Equal(t, 1, sink.ResetSpanCount())

	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Content, &env))
	assert.Equal(t, contracts.ZCodeEventTurnCompleted, env["type"])
	assert.Contains(t, env, "context_usage", "the persisted divider carries the usage a reconnect rehydrates from")
}

// A background turn's completion must leave the user's turn and its open spans
// alone, and record its own outcome as a notification instead.
func TestHandleZCodeOutput_TurnCompleted_OfABackgroundTurnDoesNotEndTheUsersTurn(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.turnActive = true
	a.backgroundTurn = true

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{"resultType":"success","toolCallCount":1}`))

	a.mu.Lock()
	turnActive, background := a.turnActive, a.backgroundTurn
	a.mu.Unlock()
	// The TRANSCRIPT is what a background turn must not touch: no divider, no span
	// reset, no closing of the user's open tool cards.
	assert.Equal(t, 0, sink.MessageCount(), "no turn-end divider for a background turn")
	assert.Equal(t, 0, sink.ResetSpanCount(), "the user's open tool cards must survive")
	assert.Equal(t, 1, sink.NotificationCount(), "the background outcome is recorded as a notification")
	assert.False(t, background, "the background flag is consumed")
	// The FLAG clears either way. A turn ended, and leaving it set made Interrupt and
	// Stop fire a session/stop RPC at an idle session for the rest of the agent's life.
	assert.False(t, turnActive, "a turn ended, whichever kind it was")
}

// The app-server refuses two turns on one session, so a background turn runs alone --
// and it must leave the agent idle when it ends, not permanently mid-turn.
func TestHandleZCodeOutput_TurnCompleted_OfABackgroundTurnLeavesTheAgentIdle(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputSource":"background_task"}`))
	a.mu.Lock()
	startedActive, startedBackground := a.turnActive, a.backgroundTurn
	a.mu.Unlock()
	assert.True(t, startedActive)
	assert.True(t, startedBackground)

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	a.mu.Lock()
	turnActive := a.turnActive
	a.mu.Unlock()
	assert.False(t, turnActive, "nothing is running, so Interrupt must not send a session/stop")
}

func TestHandleZCodeOutput_TurnFailed_SchedulesAutoContinueOnlyWhenRetryable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		payload       string
		wantScheduled bool
	}{
		{"retryable true", `{"error":{"type":"api_error","message":"503","retryable":true}}`, true},
		{"retryable false", `{"error":{"type":"api_error","message":"400","retryable":false}}`, false},
		{"retryable absent", `{"error":{"type":"api_error","message":"?"}}`, false},
		{
			"provider not configured is never retryable",
			`{"error":{"type":"provider_not_configured","message":"no key","retryable":true}}`,
			false,
		},
		{
			"provider not configured by code is never retryable",
			`{"error":{"type":"api_error","code":"provider_not_configured","retryable":true}}`,
			false,
		},
		{"no error object", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &recordingControlSink{}
			a := newZCodeTestAgent(t, sink)
			a.turnActive = true

			a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventTurnFailed, tc.payload))

			a.mu.Lock()
			turnActive := a.turnActive
			a.mu.Unlock()
			assert.False(t, turnActive, "a failed turn always ends the turn")
			require.Equal(t, 1, sink.MessageCount())
			assert.True(t, sink.Messages()[0].TurnEnd)

			if tc.wantScheduled {
				assert.Positive(t, sink.AutoScheduleCount(), "a retryable failure must schedule an auto-continue")
			} else {
				assert.Equal(t, 0, sink.AutoScheduleCount())
			}
		})
	}
}

// --- streaming ---

func TestHandleZCodeOutput_ModelStreaming_TextAndReasoningDeltas(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","delta":"Hello "}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventModelStreaming, `{"kind":"reasoning_delta","delta":"thinking"}`))

	require.Equal(t, 2, sink.StreamChunkCount())
	chunks := sink.StreamChunks()
	assert.Equal(t, "Hello ", string(chunks[0].Content))
	assert.Equal(t, ZCodeStreamTextDelta, chunks[0].Method)
	assert.Equal(t, "", chunks[0].SpanID, "assistant text belongs to no tool span")
	assert.Equal(t, "thinking", string(chunks[1].Content))
	assert.Equal(t, ZCodeStreamReasoningDelta, chunks[1].Method)
	assert.Equal(t, 0, sink.MessageCount(), "a delta persists nothing")
}

func TestHandleZCodeOutput_ModelStreaming_EmptyDeltasAndUnknownKindsAreDropped(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	for _, payload := range []string{
		`{"kind":"text_delta","delta":""}`,
		`{"kind":"reasoning_delta","delta":""}`,
		`{"kind":"start"}`,
		`{"kind":"finish"}`,
		`{"kind":"error"}`,
		`{"kind":"text_start"}`,
		`{"kind":"text_end"}`,
		`{"kind":"reasoning_start"}`,
		`{"kind":"reasoning_end"}`,
		`{"kind":"a_kind_this_build_invented"}`,
	} {
		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventModelStreaming, payload))
	}

	assert.Equal(t, 0, sink.StreamChunkCount())
	assert.Equal(t, 0, sink.MessageCount())
}

// The scheduled tool.updated reports `inputOmitted` and carries no input, so the
// model stream is the ONLY copy of it that ever exists.
func TestHandleZCodeOutput_ToolInputIsCachedFromTheStreamAndConsumedByScheduled(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventModelStreaming,
		`{"kind":"tool_input_start","toolCallId":"call-1","toolName":"Bash"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventModelStreaming,
		`{"kind":"tool_input_delta","toolCallId":"call-1","delta":"{\"command\":\"ls"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventModelStreaming,
		`{"kind":"tool_input_delta","toolCallId":"call-1","delta":" -1\"}"}`))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventModelStreaming,
		`{"kind":"tool_input_end","toolCallId":"call-1"}`))
	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"call-1","toolName":"Bash","inputOmitted":true,"inputRef":"model_stream"}`))

	require.Equal(t, 1, sink.MessageCount())
	var env struct {
		Type    string `json:"type"`
		Payload struct {
			Input        json.RawMessage `json:"input"`
			InputOmitted *bool           `json:"inputOmitted"`
			InputRef     *string         `json:"inputRef"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &env))
	assert.Equal(t, contracts.ZCodeEventToolUpdated, env.Type)
	assert.JSONEq(t, `{"command":"ls -1"}`, string(env.Payload.Input))
	assert.Nil(t, env.Payload.InputOmitted, "the omission marker must go once the input is filled in")
	assert.Nil(t, env.Payload.InputRef)

	assert.Equal(t, 1, len(sink.OpenSpans()))
	assert.Equal(t, "Bash", sink.GetSpanType("call-1"))
}

// The parsed `tool_call` supersedes the concatenated fragments, which can be
// truncated when the model's stream was cut.
func TestHandleZCodeOutput_ToolCallInputSupersedesTheFragments(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventModelStreaming,
		`{"kind":"tool_input_delta","toolCallId":"c1","delta":"{\"command\":\"tru"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventModelStreaming,
		`{"kind":"tool_call","toolCallId":"c1","toolName":"Bash","input":{"command":"echo ok"}}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"c1","inputOmitted":true}`))

	require.Equal(t, 1, sink.MessageCount())
	var env struct {
		Payload struct {
			Input    json.RawMessage `json:"input"`
			ToolName string          `json:"toolName"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &env))
	assert.JSONEq(t, `{"command":"echo ok"}`, string(env.Payload.Input))
	assert.Equal(t, "Bash", sink.GetSpanType("c1"), "the tool name is recovered from the stream too")
}

// A restarted tool call must not inherit the abandoned attempt's fragment.
func TestHandleZCodeOutput_ToolInputStartDropsAPartialFragment(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventModelStreaming,
		`{"kind":"tool_input_delta","toolCallId":"c1","delta":"{\"partial\":"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventModelStreaming,
		`{"kind":"tool_input_start","toolCallId":"c1","toolName":"Read"}`))

	a.mu.Lock()
	cached := a.toolCalls["c1"].input
	name := a.toolCalls["c1"].name
	a.mu.Unlock()
	assert.Empty(t, cached)
	assert.Equal(t, "Read", name)
}

func TestZCodeCompleteToolInput_LeavesAnExistingInputAlone(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{"kind":"scheduled","input":{"command":"real"}}`)
	got := zcodeCompleteToolInput(payload, json.RawMessage(`{"command":"cached"}`))
	assert.JSONEq(t, string(payload), string(got), "the update's own input wins over the cache")

	// Nothing to fill in, or nothing valid: the payload passes through untouched, so
	// the common path takes no decode/encode round trip.
	assert.Equal(t, string(payload), string(zcodeCompleteToolInput(payload, nil)))
	assert.Equal(t, string(payload), string(zcodeCompleteToolInput(payload, json.RawMessage(`{not json`))))

	// A `null` input counts as absent.
	filled := zcodeCompleteToolInput(json.RawMessage(`{"kind":"scheduled","input":null}`), json.RawMessage(`{"a":1}`))
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(filled, &body))
	assert.JSONEq(t, `{"a":1}`, string(body["input"]))

	// A payload that is not an object cannot be completed and must not be corrupted.
	assert.Equal(t, `[1,2]`, string(zcodeCompleteToolInput(json.RawMessage(`[1,2]`), json.RawMessage(`{"a":1}`))))
}

// --- tool progress ---

// The app-server sends a byte TOTAL plus a size-limited tail, not a delta, so the growth
// is computed from the total and cut from the end of the tail.
func TestHandleZCodeOutput_ToolProgress_StreamsOnlyTheGrowth(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, `{"kind":"started","toolCallId":"c1"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","outputBytes":5,"stdoutTail":"line1"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","outputBytes":11,"stdoutTail":"line1\nline2"}`))

	chunks := sink.StreamChunks()
	require.Len(t, chunks, 2)
	assert.Equal(t, "line1", string(chunks[0].Content))
	assert.Equal(t, "\nline2", string(chunks[1].Content), "only the six new bytes ship the second time")
	for _, c := range chunks {
		assert.Equal(t, "c1", c.SpanID)
		assert.Equal(t, contracts.ZCodeToolKindProgress, c.Method)
	}
}

// When the output grew by more than the tail holds, the tail ships whole. The middle
// is lost at the source too -- the app-server keeps only a tail.
func TestHandleZCodeOutput_ToolProgress_GrowthLargerThanTheTailShipsTheWholeTail(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","outputBytes":4,"stdoutTail":"abcd"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","outputBytes":10000,"stdoutTail":"wxyz"}`))

	chunks := sink.StreamChunks()
	require.Len(t, chunks, 2)
	assert.Equal(t, "wxyz", string(chunks[1].Content))
}

func TestHandleZCodeOutput_ToolProgress_NoCounterFallsBackToTheTailsOwnGrowth(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, `{"kind":"progress","toolCallId":"c1","stdoutTail":"abc"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, `{"kind":"progress","toolCallId":"c1","stdoutTail":"abcdef"}`))
	// A tail that did not grow ships nothing.
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, `{"kind":"progress","toolCallId":"c1","stdoutTail":"abcdef"}`))

	chunks := sink.StreamChunks()
	require.Len(t, chunks, 2)
	assert.Equal(t, "abc", string(chunks[0].Content))
	assert.Equal(t, "def", string(chunks[1].Content))
}

func TestHandleZCodeOutput_ToolProgress_UsesStderrWhenStdoutIsEmpty(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","stderrTail":"boom","stdoutBytes":4}`))

	require.Equal(t, 1, sink.StreamChunkCount())
	assert.Equal(t, "boom", string(sink.LastStreamChunk().Content))
}

// Each stream is measured against its OWN counter. Measuring one tail against the
// other's total -- or against the combined `outputBytes` -- makes either stream's growth
// look like both: the quiet stream's tail is re-broadcast while the busy stream's new
// bytes never appear. Every compiler, `git` and `npm` invocation writes on both.
func TestHandleZCodeOutput_ToolProgress_StreamsStdoutAndStderrIndependently(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, `{"kind":"started","toolCallId":"c1"}`))
	// stdout writes 10 bytes.
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","stdoutBytes":10,"stderrBytes":0,
		  "outputBytes":10,"stdoutTail":"0123456789"}`))
	// stderr writes 5 while stdout stays quiet.
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","stdoutBytes":10,"stderrBytes":5,
		  "outputBytes":15,"stdoutTail":"0123456789","stderrTail":"boom!"}`))

	chunks := sink.StreamChunks()
	require.Len(t, chunks, 2)
	assert.Equal(t, "0123456789", string(chunks[0].Content))
	assert.Equal(t, "boom!", string(chunks[1].Content),
		"the stderr bytes ship, and the quiet stdout tail is not re-broadcast")
}

// A build that sends only the COMBINED counter is still served, and the attribution is
// unambiguous while exactly one tail is present.
func TestHandleZCodeOutput_ToolProgress_TheCombinedCounterServesASingleStream(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","outputBytes":5,"stderrTail":"line1"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","outputBytes":11,"stderrTail":"line1\nline2"}`))

	chunks := sink.StreamChunks()
	require.Len(t, chunks, 2)
	assert.Equal(t, "line1", string(chunks[0].Content))
	assert.Equal(t, "\nline2", string(chunks[1].Content))
}

// The counter is a BYTE count and the tail is UTF-8, so the cut can land inside a
// multi-byte rune. The browser decodes each chunk on its own, so an orphan continuation
// byte renders as U+FFFD at the seam of every update on a non-ASCII line.
func TestHandleZCodeOutput_ToolProgress_CutsOnlyAtARuneBoundary(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	// "한글" is six bytes. The second update grows the total by 4 -- a cut two bytes into
	// the first rune.
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","stdoutBytes":2,"stdoutTail":"ab"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","stdoutBytes":6,"stdoutTail":"한글"}`))

	chunks := sink.StreamChunks()
	require.Len(t, chunks, 2)
	assert.True(t, utf8.Valid(chunks[1].Content), "a chunk must never start mid-rune")
	assert.Equal(t, "글", string(chunks[1].Content),
		"the cut moves FORWARD to the next rune start; moving back would repeat shipped bytes")
}

// A subagent's tool call opened its span in a CHILD transcript, so every chunk of that
// call must go there -- including on the counterless fallback path, which used to
// broadcast on the parent and orphan the output in a transcript with no such span.
func TestHandleZCodeOutput_ToolProgress_ASubagentsOutputStaysInItsChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"sub-1","toolName":"Bash","source":"subagent",
		  "parentToolCallId":"spawn-1","input":{"command":"ls"}}`))
	// No counter of any kind: the fallback path.
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"sub-1","source":"subagent",
		  "parentToolCallId":"spawn-1","stdoutTail":"one"}`))

	assert.Equal(t, 0, sink.StreamChunkCount(),
		"the parent transcript never opened this span, so no chunk may land there")
	childIDs := sink.ChildAgentIDs()
	require.Len(t, childIDs, 1)
	child, ok := sink.ChildSink(childIDs[0]).(*testSink)
	require.True(t, ok)
	require.Equal(t, 1, child.StreamChunkCount())
	assert.Equal(t, "one", string(child.LastStreamChunk().Content))
	assert.Equal(t, "sub-1", child.LastStreamChunk().SpanID)
}

func TestHandleZCodeOutput_ToolProgress_IgnoresAnIdlessOrEmptyUpdate(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, `{"kind":"progress","stdoutTail":"orphan"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, `{"kind":"progress","toolCallId":"c1"}`))

	assert.Equal(t, 0, sink.StreamChunkCount())
}

// A re-started call must not read its predecessor's byte total as already broadcast.
func TestHandleZCodeOutput_ToolStarted_ResetsTheProgressCounter(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.toolCalls["c1"] = &zcodeToolCall{name: "Bash"}

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","outputBytes":100,"stdoutTail":"old"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, `{"kind":"started","toolCallId":"c1"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"progress","toolCallId":"c1","outputBytes":3,"stdoutTail":"new"}`))

	chunks := sink.StreamChunks()
	require.Len(t, chunks, 2)
	assert.Equal(t, "new", string(chunks[1].Content))

	// `started` broadcasts no session info AT ALL. It used to ship a
	// zcode_running_tool key that no browser code read; recordZCodeToolStarted
	// says what ZCode must report before it broadcasts the shared running_tool key
	// instead. Asserting the whole payload is absent (not just the key) is the
	// stronger statement, and it does not pass vacuously the way NotContains does
	// against a nil map.
	assert.Nil(t, sink.LastSessionInfo())
}

// --- tool completion ---

func TestHandleZCodeOutput_ToolResult_PersistsClosesAndCountsTheCall(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.toolCalls["c1"] = &zcodeToolCall{name: "Bash", input: json.RawMessage(`{"command":"ls"}`)}

	a.HandleOutput(zcodeEventLine(t, 9, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"c1","result":{"content":"a\nb","truncated":false}}`))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.Equal(t, "c1", msg.SpanID)
	assert.Equal(t, "Bash", msg.SpanType, "the span carries the tool name a result payload omits")
	assert.True(t, msg.Closing)
	assert.Equal(t, 1, sink.StreamEndCount())
	assert.Equal(t, "c1", sink.LastStreamEnd())
	assert.Equal(t, 1, sink.ClosedSpanCount())

	a.mu.Lock()
	tc := a.toolCalls["c1"]
	toolUses, final, names, inputs := a.turnToolUses, tc.final, len(tc.name), len(tc.input)
	a.mu.Unlock()
	assert.Equal(t, 1, toolUses)
	assert.True(t, final)
	assert.Equal(t, 0, names, "the side tables are released with the call")
	assert.Equal(t, 0, inputs)

	// The close broadcasts no running-tool clear -- no session info at all. The
	// frontend drops a span's running_tool entry when the result row above lands,
	// so a provider never sends an end message; see closeZCodeToolCall. Asserted
	// as a nil payload for the reason the `started` test above gives: NotContains
	// passes vacuously against a nil map, so it would state nothing here.
	assert.Nil(t, sink.LastSessionInfo())
}

func TestHandleZCodeOutput_ToolError_ClosesTheCallToo(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.toolCalls["c1"] = &zcodeToolCall{name: "Edit"}

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"error","toolCallId":"c1","error":{"message":"file not found"}}`))

	require.Equal(t, 1, sink.MessageCount())
	assert.True(t, sink.Messages()[0].Closing)
	assert.Equal(t, 1, sink.ClosedSpanCount())
}

// A background turn's tool call must not be counted against the user's turn.
func TestHandleZCodeOutput_ToolResult_DuringABackgroundTurnDoesNotCount(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.backgroundTurn = true
	a.toolCalls["c1"] = &zcodeToolCall{name: "Bash"}

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, `{"kind":"result","toolCallId":"c1","result":{}}`))

	a.mu.Lock()
	toolUses := a.turnToolUses
	a.mu.Unlock()
	assert.Equal(t, 0, toolUses)
}

// The batch summary arrives AFTER the per-call results and only summarizes them, so
// it must close only a call that was opened and never reached a final state.
func TestHandleZCodeOutput_ToolBatch_BackfillsOnlyTheLostCalls(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.toolCalls["finished"] = &zcodeToolCall{name: "Bash", final: true}
	a.toolCalls["lost"] = &zcodeToolCall{name: "Read"}

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"batch","toolCallIds":["finished","lost","never-seen",""],"successCount":2}`))

	require.Equal(t, 1, sink.MessageCount(), "only the lost call is backfilled")
	msg := sink.Messages()[0]
	assert.Equal(t, "lost", msg.SpanID)
	assert.Equal(t, "Read", msg.SpanType)
	assert.True(t, msg.Closing)
}

func TestHandleZCodeOutput_ToolUpdated_IgnoresAnIdlessOrUnknownKind(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, `{"kind":"scheduled"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated, `{"kind":"result"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, `{"kind":"a_kind_this_build_invented","toolCallId":"c1"}`))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventToolUpdated, `{"toolCallId":"c1"}`))

	assert.Equal(t, 0, sink.MessageCount())
	assert.Equal(t, 0, len(sink.OpenSpans()))
}

// --- subagents ---

// The two questions are opposites and the wire fields that answer them are
// different: the Agent tool STARTS a subagent, while `source`/`agentId`/
// `childSessionId` mark an update a subagent PRODUCED.
func TestZCodeToolSpawnsSubagent(t *testing.T) {
	t.Parallel()

	assert.True(t, zcodeToolSpawnsSubagent(zcodeToolUpdated{ToolName: contracts.ZCodeToolNameAgent}),
		"the Agent tool spawns one even before a child session exists")
	assert.False(t, zcodeToolSpawnsSubagent(zcodeToolUpdated{ToolName: contracts.ZCodeToolNameBash}))
	assert.False(t, zcodeToolSpawnsSubagent(zcodeToolUpdated{
		ToolName: contracts.ZCodeToolNameBash, Source: ZCodeToolSourceSubagent, ChildSessionID: "child-1",
	}), "a subagent's OWN Bash call does not spawn anything")
}

func TestZCodeToolFromSubagent(t *testing.T) {
	t.Parallel()

	assert.True(t, zcodeToolFromSubagent(zcodeToolUpdated{Source: ZCodeToolSourceSubagent}))
	assert.True(t, zcodeToolFromSubagent(zcodeToolUpdated{ParentToolCallID: "spawn-1"}))
	assert.False(t, zcodeToolFromSubagent(zcodeToolUpdated{ToolName: contracts.ZCodeToolNameAgent}),
		"the spawn itself belongs to the main conversation")
	assert.False(t, zcodeToolFromSubagent(zcodeToolUpdated{ToolName: contracts.ZCodeToolNameBash}))
}

func TestHandleZCodeOutput_SubagentSpawnOpensNoSpanAndRemembersThePrompt(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"spawn-1","toolName":"Agent",
		  "input":{"prompt":"investigate the flake","description":"flake hunt"}}`))

	require.Equal(t, 1, sink.MessageCount())
	assert.Equal(t, 0, len(sink.OpenSpans()),
		"a subagent spawn holds no rail: its output lands in a child transcript")
	assert.Equal(t, "investigate the flake", a.toolCallPrompts.take("spawn-1"))
}

// The background-task path creates the row (and the child transcript). The tool
// result closes THAT row -- both paths key by the tool-call id, so one subagent has
// exactly one row.
func TestHandleZCodeOutput_SubagentEndClosesTheRowTheBackgroundPathCreated(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.toolCalls["spawn-1"] = &zcodeToolCall{name: contracts.ZCodeToolNameAgent}

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"task-1","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent",
		  "childSessionId":"child-1","status":"running"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","toolName":"Agent","childSessionId":"child-1",
		  "description":"flake hunt","result":{"content":"done"}}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "one subagent must produce exactly one registry row")
	assert.Equal(t, "spawn-1", tasks[0].RowKey)
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind)
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status)
	assert.NotEmpty(t, tasks[0].ChildAgentID, "the row the lifecycle closed is the one that carries the child linkage")
}

func TestHandleZCodeOutput_SubagentEndWithAnErrorFailsTheRow(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"task-1","toolCallId":"spawn-1","toolName":"Agent","taskKind":"subagent","status":"running"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"error","toolCallId":"spawn-1","toolName":"Agent","agentType":"reviewer"}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusFailed, tasks[0].Status)
}

// An Agent tool call the app-server never reported as a background task has no child
// transcript, so a registry row for it would open nothing. Its result is persisted in
// this transcript either way.
func TestHandleZCodeOutput_SubagentEndWithNoRegistryRowCreatesNone(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","toolName":"Agent","result":{"content":"done"}}`))

	assert.Empty(t, sink.BackgroundTasks())
	assert.Equal(t, 1, sink.MessageCount(), "the result still lands in the parent transcript")
}

// --- session.updated, the overloaded event ---

func TestHandleZCodeOutput_SessionUpdated_ModelResponsePersistsTheAssistantText(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventSessionUpdated,
		`{"content":"Here is the answer.","stopReason":"end_turn","contextWindow":200000,
		  "usage":{"inputTokens":50,"outputTokens":10,"totalTokens":60,"cacheReadTokens":5}}`))

	require.Equal(t, 1, sink.MessageCount())
	var env struct {
		Type    string `json:"type"`
		Payload struct {
			Content string `json:"content"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &env))
	assert.Equal(t, contracts.ZCodeEventSessionUpdated, env.Type)
	assert.Equal(t, "Here is the answer.", env.Payload.Content)

	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(50), usage["input_tokens"])
	assert.Equal(t, int64(200000), usage["context_window"])
}

// A tool-only step reports an EMPTY content with a stop reason. Persisting it would
// put a blank assistant bubble between the tool cards.
func TestHandleZCodeOutput_SessionUpdated_EmptyContentPersistsNothingButKeepsTheUsage(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"content":"","stopReason":"tool_calls","usage":{"inputTokens":7,"totalTokens":7}}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"content":"   ","stopReason":"tool_calls"}`))

	assert.Equal(t, 0, sink.MessageCount())
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]any)
	require.True(t, ok, "the usage of a tool-only step still counts")
	assert.Equal(t, int64(7), usage["input_tokens"])
}

func TestHandleZCodeOutput_SessionUpdated_TelemetryVariantsAreIgnored(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	for _, payload := range []string{
		`{"messageCount":12,"modelRef":{"providerId":"p","modelId":"m"},"iteration":3}`,
		`{"baseURL":"https://api.example","requestId":"r-1","maxAttempts":3}`,
		`{}`,
		`{"stopReason":"end_turn"}`,
	} {
		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated, payload))
	}

	assert.Equal(t, 0, sink.MessageCount())
	assert.Equal(t, 0, sink.NotificationCount())
	assert.Equal(t, 0, len(sink.BackgroundTasks()))
}

// --- background tasks ---

func TestHandleZCodeOutput_BackgroundShellTaskReusesItsLaunchCard(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"task-1","toolCallId":"c1","toolName":"Bash","taskKind":"bash",
		  "command":"npm run dev\n# second line","status":"running"}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "c1", tasks[0].RowKey,
		"the row is keyed by the tool call, which is the span the launch card already shows")
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind)
	assert.Equal(t, "npm run dev", tasks[0].Title, "the command's first line labels the row")
	assert.True(t, tasks[0].TitleIsCommand, "a verbatim command renders as code, prose does not")
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	assert.Empty(t, sink.ChildAgentIDs(), "a shell task mints no child transcript")
}

func TestHandleZCodeOutput_BackgroundSubagentTaskMintsAChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.toolCallPrompts.remember("c1", "review the diff")

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"task-1","toolCallId":"c1","toolName":"Agent","taskKind":"subagent",
		  "childSessionId":"child-1","status":"running"}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind)
	assert.Equal(t, "Agent", tasks[0].Title)
	assert.False(t, tasks[0].TitleIsCommand)
	require.NotEmpty(t, tasks[0].ChildAgentID)

	child, ok := sink.ChildSink(tasks[0].ChildAgentID).(*testSink)
	require.True(t, ok)
	messages := child.Messages()
	require.Len(t, messages, 1, "the spawn instruction opens the child transcript")
	var opening struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(messages[0].Content, &opening))
	assert.Equal(t, "review the diff", opening.Content)
}

func TestZCodeBackgroundStatus(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		want  bgtask.Status
		final bool
	}{
		"completed":   {bgtask.StatusCompleted, true},
		"failed":      {bgtask.StatusFailed, true},
		"spawn_error": {bgtask.StatusFailed, true},
		"timed_out":   {bgtask.StatusFailed, true},
		"cancelled":   {bgtask.StatusStopped, true},
		"lost":        {bgtask.StatusFailed, true},
		"running":     {bgtask.StatusRunning, false},
		"":            {bgtask.StatusRunning, false},
		"queued":      {bgtask.StatusRunning, false},
	}
	for status, want := range cases {
		got, final := zcodeBackgroundStatus(status)
		assert.Equalf(t, want.want, got, "status %q", status)
		assert.Equalf(t, want.final, final, "status %q finality", status)
	}
}

// A blocked task reports WHY it is idle, but only while it is still running: an
// active form on a finished row would leave a stale explanation on screen.
func TestHandleZCodeOutput_BackgroundTaskBlockedReasonOnlyWhileRunning(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"t1","taskKind":"bash","command":"sleep 5","status":"running",
		  "blocked":true,"blockedReason":"waiting for the port"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"t2","taskKind":"bash","command":"sleep 5","status":"completed",
		  "blocked":true,"blockedReason":"waiting for the port"}`))

	byKey := map[string]bgtask.Item{}
	for _, task := range sink.BackgroundTasks() {
		byKey[task.RowKey] = task
	}
	assert.Equal(t, "waiting for the port", byKey["t1"].ActiveForm)
	assert.Equal(t, "", byKey["t2"].ActiveForm)
}

func TestHandleZCodeOutput_BackgroundTaskWithoutAnIDIsIgnored(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	// No taskId, so the shape reads as telemetry rather than as a task.
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated, `{"taskKind":"bash","status":"running"}`))

	assert.Equal(t, 0, len(sink.BackgroundTasks()))
}

// --- permission.resolved ---

func TestHandleZCodeOutput_PermissionResolved_RecordsOnlyDecisionsLeapMuxDidNotMake(t *testing.T) {
	t.Parallel()

	t.Run("an automatic denial is worth reporting", func(t *testing.T) {
		t.Parallel()
		sink := &recordingControlSink{}
		a := newZCodeTestAgent(t, sink)

		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventPermissionResolved,
			`{"requestId":"r-1","toolName":"Bash","decision":"deny","reason":"plan mode forbids commands"}`))

		assert.Equal(t, 1, sink.NotificationCount())
	})

	t.Run("an automatic allow is not", func(t *testing.T) {
		t.Parallel()
		sink := &recordingControlSink{}
		a := newZCodeTestAgent(t, sink)

		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventPermissionResolved,
			`{"requestId":"r-1","toolName":"Read","decision":"allow"}`))

		assert.Equal(t, 0, sink.NotificationCount(),
			"an allow in build mode is the normal case; a row per call would bury the transcript")
	})

	t.Run("the echo of the user's own answer is not", func(t *testing.T) {
		t.Parallel()
		sink := &recordingControlSink{}
		a := newZCodeTestAgent(t, sink)
		a.rememberZCodeControlRequest("r-1", json.RawMessage(`{"type":"control_request"}`))

		a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventPermissionResolved,
			`{"requestId":"r-1","toolName":"Bash","decision":"deny","reason":"the user refused"}`))

		assert.Equal(t, 0, sink.NotificationCount(), "the structured answer row already records it")

		// The id is consumed, so a SECOND resolution for it is an automatic one.
		a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventPermissionResolved,
			`{"requestId":"r-1","toolName":"Bash","decision":"deny"}`))
		assert.Equal(t, 1, sink.NotificationCount())
	})
}

func TestForgetZCodePermissionRequest(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	assert.False(t, a.forgetZCodeControlRequest(""), "an empty id was never remembered")
	assert.False(t, a.forgetZCodeControlRequest("unknown"))

	a.rememberZCodeControlRequest("r-1", json.RawMessage(`{"type":"control_request"}`))
	assert.True(t, a.forgetZCodeControlRequest("r-1"))
	assert.False(t, a.forgetZCodeControlRequest("r-1"), "the id is consumed")
}

// --- notifications and ignored events ---

func TestHandleZCodeOutput_SteeringAndCloseArePersistedAsNotifications(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{contracts.ZCodeEventTurnSteerQueued, contracts.ZCodeEventTurnSteerDrained, contracts.ZCodeEventSessionClosed} {
		t.Run(eventType, func(t *testing.T) {
			t.Parallel()
			sink := &recordingControlSink{}
			a := newZCodeTestAgent(t, sink)

			a.HandleOutput(zcodeEventLine(t, 1, eventType, `{}`))

			require.Equal(t, 1, sink.NotificationCount())
			var env map[string]any
			require.NoError(t, json.Unmarshal(sink.LastNotification().Content, &env))
			assert.Equal(t, eventType, env["type"])
		})
	}
}

// Every event the provider deliberately ignores must produce NOTHING, and must not
// panic on an absent or malformed payload.
func TestHandleZCodeOutput_IgnoredEventTypesProduceNothing(t *testing.T) {
	t.Parallel()

	ignored := []string{
		contracts.ZCodeEventSessionCreated,
		contracts.ZCodeEventSessionResumed,
		contracts.ZCodeEventSessionTitleUpdated,
		contracts.ZCodeEventMessageUpserted,
		contracts.ZCodeEventMessageRemoved,
		contracts.ZCodeEventPartStarted,
		contracts.ZCodeEventPartDelta,
		contracts.ZCodeEventPartUpserted,
		contracts.ZCodeEventPartRemoved,
		contracts.ZCodeEventPermissionRequested,
		contracts.ZCodeEventUserInputRequested,
		contracts.ZCodeEventUserInputResolved,
		contracts.ZCodeEventCheckpointCreated,
		contracts.ZCodeEventRewindTriggered,
	}
	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	for _, eventType := range ignored {
		a.HandleOutput(zcodeEventLine(t, 1, eventType, `{"anything":true}`))
		a.HandleOutput(zcodeEventLine(t, 1, eventType, ""))
	}

	assert.Equal(t, 0, sink.MessageCount())
	assert.Equal(t, 0, sink.NotificationCount())
	assert.Equal(t, 0, sink.StreamChunkCount())
	assert.Equal(t, 0, len(sink.BackgroundTasks()))
}

// An event type this build does not know must be dropped rather than crash or reach a
// branch that looks deliberate.
func TestHandleZCodeOutput_UnknownEventTypeIsIgnoredWithoutPanic(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 7, "a.type.a.later.build.invented", `{"x":1}`))

	assert.Equal(t, 0, sink.MessageCount())
	a.mu.Lock()
	seq := a.lastSeq
	a.mu.Unlock()
	assert.Equal(t, int64(7), seq, "an unknown event still advances the sequence, so a replay does not repeat it")
}

func TestHandleZCodeOutput_MalformedAndUnrelatedLinesAreSurvivable(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	for _, line := range []string{
		`{"method":"session/event"}`,
		`{"method":"session/event","params":{}}`,
		`{"method":"session/event","params":{"type":"tool.updated","payload":"not an object"}}`,
		`{"method":"process/resourceSample","params":{"rss":1}}`,
		`{"method":"v4/telemetry/event","params":{}}`,
		`{"id":99,"result":{}}`,
		`{"nonsense":true}`,
		`not json at all`,
	} {
		a.HandleOutput([]byte(line))
	}

	assert.Equal(t, 0, sink.MessageCount())
	assert.Equal(t, 0, sink.NotificationCount())
}

// The sequence must never move BACKWARDS: a replay that re-delivers an older event
// would otherwise make the next re-subscribe ask for events already persisted.
func TestDispatchZCodeEvent_SequenceOnlyAdvances(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})

	a.HandleOutput(zcodeEventLine(t, 10, contracts.ZCodeEventSessionTitleUpdated, `{}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionTitleUpdated, `{}`))
	a.HandleOutput(zcodeEventLine(t, 0, contracts.ZCodeEventSessionTitleUpdated, `{}`))

	a.mu.Lock()
	seq := a.lastSeq
	a.mu.Unlock()
	assert.Equal(t, int64(10), seq)
}

func TestZCodeEventEnvelope_PersistBytesNormalizesBothArrivalPaths(t *testing.T) {
	t.Parallel()

	// A replayed event (from the subscribe reply) and a notification event must
	// persist as the SAME envelope, so the frontend has one shape to classify.
	replayed := zcodeEventEnvelope{Type: contracts.ZCodeEventTurnCompleted, Seq: 4, Payload: json.RawMessage(`{"toolCallCount":1}`)}
	notified, ok := parseZCodeEvent([]byte(`{"seq":4,"type":"turn.completed","payload":{"toolCallCount":1}}`))
	require.True(t, ok)

	assert.JSONEq(t, string(replayed.persistBytes()), string(notified.persistBytes()))

	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(replayed.persistBytes(), &env))
	assert.Equal(t, contracts.ZCodeEventTurnCompleted, env.Type)
	assert.JSONEq(t, `{"toolCallCount":1}`, string(env.Payload))
}

func TestZCodeEventEnvelope_WithPayloadDoesNotMutateTheOriginal(t *testing.T) {
	t.Parallel()

	original := zcodeEventEnvelope{Type: contracts.ZCodeEventToolUpdated, Payload: json.RawMessage(`{"a":1}`)}
	copied := original.withPayload(json.RawMessage(`{"b":2}`))

	assert.JSONEq(t, `{"a":1}`, string(original.Payload))
	assert.JSONEq(t, `{"b":2}`, string(copied.Payload))
	assert.Equal(t, original.Type, copied.Type)
}

func TestZCodeBackgroundTitle(t *testing.T) {
	t.Parallel()

	title, isCommand := zcodeBackgroundTitle(zcodeBackgroundTask{Command: "  ls -1\nrm -rf /  "})
	assert.Equal(t, "ls -1", title)
	assert.True(t, isCommand)

	title, isCommand = zcodeBackgroundTitle(zcodeBackgroundTask{ToolName: " Agent "})
	assert.Equal(t, "Agent", title)
	assert.False(t, isCommand, "prose must not render as code")

	title, isCommand = zcodeBackgroundTitle(zcodeBackgroundTask{})
	assert.Equal(t, "", title)
	assert.False(t, isCommand)
}

// userInput.resolved is bookkeeping only: it re-arms the re-announcement guard and
// renders nothing. A payload it cannot read must not panic and must not persist a
// row of raw JSON.
func TestHandleZCodeOutput_UserInputResolvedRendersNothing(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventUserInputResolved, ""))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventUserInputResolved, `"not an object"`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventUserInputResolved, `{}`))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventUserInputResolved, `{"requestId":"never-forwarded"}`))

	assert.Equal(t, 0, sink.MessageCount())
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.PersistedControls())
}

// --- event dispatch ---

// Two subscriptions can ask from the same sequence, and the app-server replays the gap
// in full to each. Without a watermark check every row in that gap is persisted twice:
// each tool card, each assistant bubble and each turn divider appears twice.
func TestDispatchZCodeEvent_ADuplicateSequenceIsDropped(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	line := zcodeEventLine(t, 7, contracts.ZCodeEventSessionUpdated, `{"content":"the answer","stopReason":"stop"}`)
	a.HandleOutput(line)
	a.HandleOutput(line)
	// An event BELOW the watermark is a replay too.
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated, `{"content":"older","stopReason":"stop"}`))

	assert.Equal(t, 1, sink.MessageCount(), "a replayed event must not persist a second row")
	a.mu.Lock()
	seq := a.lastSeq
	a.mu.Unlock()
	assert.Equal(t, int64(7), seq, "an older replay must not lower the watermark")
}

// An event with no sequence at all still dispatches: the watermark only suppresses a
// replay of something already seen.
func TestDispatchZCodeEvent_AnUnsequencedEventStillDispatches(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 5, contracts.ZCodeEventSessionUpdated, `{"content":"one","stopReason":"stop"}`))
	a.HandleOutput(zcodeEventLine(t, 0, contracts.ZCodeEventSessionUpdated, `{"content":"two","stopReason":"stop"}`))

	assert.Equal(t, 2, sink.MessageCount())
}

// The BATCH payload is addressed to a LIST, so it carries no `toolCallId` and every
// frontend extractor refuses it: the recovered row closed the span and rendered an
// empty bubble. A per-call result is synthesized instead.
func TestHandleZCodeOutput_ToolBatch_PersistsAPerCallResultNotTheBatchEnvelope(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"lost","toolName":"Bash","input":{"command":"ls"}}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"batch","toolCallIds":["lost"],"successCount":1,"errorCount":0}`))

	messages := sink.Messages()
	require.Len(t, messages, 2)
	closing := messages[1]
	assert.True(t, closing.Closing)
	assert.Equal(t, "lost", closing.SpanID)

	var env struct {
		Payload struct {
			Kind       string `json:"kind"`
			ToolCallID string `json:"toolCallId"`
			ToolName   string `json:"toolName"`
			Result     struct {
				Success bool   `json:"success"`
				Content string `json:"content"`
			} `json:"result"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(closing.Content, &env))
	assert.Equal(t, contracts.ZCodeToolKindResult, env.Payload.Kind)
	assert.Equal(t, "lost", env.Payload.ToolCallID, "without this the frontend renders an empty bubble")
	assert.Equal(t, contracts.ZCodeToolNameBash, env.Payload.ToolName)
	assert.True(t, env.Payload.Result.Success)
	assert.NotEmpty(t, env.Payload.Result.Content)
}

// The batch states aggregate counts only, so it cannot say WHICH call failed. Reporting
// the outcome as unknown-but-not-success is honest; claiming success is not.
func TestHandleZCodeOutput_ToolBatch_AnErrorInTheBatchIsNotReportedAsSuccess(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"lost","toolName":"Bash","input":{"command":"ls"}}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventToolUpdated,
		`{"kind":"batch","toolCallIds":["lost"],"successCount":0,"errorCount":1}`))

	messages := sink.Messages()
	require.Len(t, messages, 2)
	var env struct {
		Payload struct {
			Result struct {
				Success bool `json:"success"`
			} `json:"result"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(messages[1].Content, &env))
	assert.False(t, env.Payload.Result.Success)
}

// --- stream recovery ---

// A stream recovery reports a retry of the MODEL PROVIDER's SSE stream, not a lapse in
// LeapMux's event subscription -- so it takes no action at all. It used to fire a
// re-subscribe, which would spend an RPC on every model retry to replay nothing, and
// whose three trigger literals the shipped app-server never sends anyway.
func TestHandleZCodeOutput_StreamRecoveryTakesNoAction(t *testing.T) {
	t.Parallel()

	for name, payload := range map[string]string{
		"an anchor":               `{"kind":"tool_result","anchorId":"a-1"}`,
		"a retry with no kind":    `{"attemptId":"x","retryNumber":1,"maxRetries":3,"streamMode":"sse"}`,
		"discarded output":        `{"discardedTextBytes":420,"streamMode":"sse"}`,
		"one of the old triggers": `{"kind":"retry_started"}`,
		"an empty payload":        `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &recordingControlSink{}
			a := newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})

			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventStreamRecoveryUpdated, payload))

			assert.Empty(t, a.stdin.(*zcodeRecordedStdin).Frames(),
				"no RPC: a re-subscribe would replay nothing, because the subscription never lapsed")
			assert.Equal(t, 0, sink.MessageCount(), "the retry's own outcome arrives on turn.failed or turn.completed")
			assert.Equal(t, 0, sink.NotificationCount())
		})
	}
}
