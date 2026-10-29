package amp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestUnknownFramesPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "current session", Frame: []byte(`{"type":"probe_future_line","session_id":"T-1"}`)},
		{Name: "implicit session", Frame: []byte(`{"type":"probe_future_line","value":7}`)},
		{Name: "unknown system subtype", Frame: []byte(`{"type":"system","subtype":"future","session_id":"T-1"}`)},
		{Name: "system subtype of another session", Frame: []byte(`{"type":"system","subtype":"future","session_id":"T-other"}`), OtherSession: true},
		{Name: "another session", Frame: []byte(`{"type":"probe_future_line","session_id":"T-other"}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		t.Helper()
		h := newHarness(t, withResume("T-1"))
		h.agent.HandleOutput(tc.Frame)
		return &h.sink.Sink
	})
}

// The probe transcript of a shell call and a Task subagent, read end to end:
// each assistant block takes a row of its own, the worker drops the empty
// thinking block and the user echoes, and each turn ends at its end_turn message.
func TestProbeTranscriptReadsIntoRowsAndTurnEnds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("run ls")
	h.feedFixture(fp, "probe_shell_task.jsonl")

	rows := h.rows()
	var kinds []string
	for _, row := range rows {
		blocks := rowBlocks(t, row.Content)
		require.Len(t, blocks, 1, "each row carries one block")
		block := blocks[0].(map[string]any)
		kinds = append(kinds, block["type"].(string)+":"+row.SpanID)
	}
	assert.Equal(t, []string{
		"thinking:",
		"tool_use:TU-034UC14fL0WVIuQhmDl0qN",
		"tool_result:TU-034UC14fL0WVIuQhmDl0qN",
		"text:",
		"thinking:",
		"tool_use:TU-034UC1Nm0p8vRRJ3Q7oNEO",
		"tool_result:TU-034UC1Nm0p8vRRJ3Q7oNEO",
		"text:",
	}, kinds, "the empty thinking block and the two user echoes reach no row")

	ends := h.turnEnds()
	require.Len(t, ends, 2, "two end_turn messages; the process's own success result after stdin closed ends no turn")
	first := decodeRow(t, ends[0].Content)
	assert.Equal(t, contracts.AmpLineTypeResult, first["type"])
	assert.Equal(t, contracts.AmpResultSubtypeSuccess, first["subtype"])
	assert.Equal(t, "done", first["result"], "the worker's turn end states the turn's last text")
	assert.EqualValues(t, 2, first["num_turns"], "two assistant messages")
	assert.Equal(t, "T-01a0d1c3-e51a-756b-9279-34c1cd441c35", first["session_id"])
	assert.Equal(t, agent.MessageCompletionComplete, ends[0].Completion)
	assert.Equal(t, "T-01a0d1c3-e51a-756b-9279-34c1cd441c35", h.sink.LastSessionID(), "the init line states the thread")
	assert.False(t, h.turnActive())
}

func TestAssistantLineSplitsEveryBlockIntoItsOwnRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, assistantLine(`[`+
		`{"type":"thinking","thinking":"**Plan**"},`+
		`{"type":"text","text":"working"},`+
		toolUseBlock("TU-a", "shell_command", `{"command":"ls"}`)+`,`+
		toolUseBlock("TU-b", "shell_command", `{"command":"pwd"}`)+
		`]`, "tool_use"))

	rows := h.rows()
	require.Len(t, rows, 4)
	for i, row := range rows {
		line := decodeRow(t, row.Content)
		assert.Equal(t, "assistant", line["type"], "row %d keeps Amp's line type", i)
		assert.Equal(t, "T-1", line["session_id"], "row %d keeps the line's other fields", i)
	}
	assert.Equal(t, "TU-a", rows[2].SpanID)
	assert.Equal(t, "shell_command", rows[2].SpanType)
	assert.Equal(t, "TU-b", rows[3].SpanID)
	assert.Empty(t, rows[0].Metadata, "the usage metadata rides on the message's last row alone")
	assert.NotEmpty(t, rows[3].Metadata)
	var metadata map[string]map[string]any
	require.NoError(t, json.Unmarshal(rows[3].Metadata, &metadata))
	usage := metadata[contracts.SessionInfoKeyContextUsage]
	assert.EqualValues(t, 300, usage[contracts.ContextUsageFieldContextTokens], "the context is every input token, cached ones included")
	assert.EqualValues(t, 7, usage[contracts.ContextUsageFieldOutputTokens])
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "TU-a"}, {SpanID: "TU-b"}}, h.sink.OpenSpans(), "two parallel calls open two spans")
	assert.True(t, h.turnActive(), "a tool_use message does not end the turn")
}

func TestToolResultClosesItsSpanAndCountsTheTool(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, assistantLine(`[`+toolUseBlock("TU-a", "shell_command", `{"command":"ls"}`)+`]`, "tool_use"))
	h.feed(fp, toolResultLine("TU-a", `{"output":"README.md\n","exitCode":0}`, false))
	h.feed(fp, textLine("done", stopReasonEndTurn))

	rows := h.rows()
	require.Len(t, rows, 3)
	result := rows[1]
	assert.Equal(t, "TU-a", result.SpanID)
	assert.Equal(t, "shell_command", result.SpanType, "the result takes its call's tool name")
	assert.True(t, result.Closing)
	assert.Equal(t, []string{"TU-a"}, h.sink.ClosedSpans())

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	count, ok := agent.DefaultTurnEndToolUses(agent.ResolveMessageContent(ampProvider{}, agent.MessageContent{Original: ends[0].Content, Metadata: ends[0].Metadata}))
	require.True(t, ok)
	assert.EqualValues(t, 1, count)
}

// The turn end persists BEFORE the flag clears: the clear is the settle edge
// that spends the turn's tool count.
func TestTurnEndPrecedesTheClear_Amp(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, textLine("done", stopReasonEndTurn))
	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, h.sink.TurnLifecycle())
}

func TestUserEchoesAreDropped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("hello")
	h.feed(fp, userEchoLine("hello"))
	assert.Empty(t, h.sink.Messages(), "LeapMux persisted the user's message already")
}

func TestEmptyAndRedactedThinkingReachNoRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, assistantLine(`[{"type":"thinking","thinking":"  "},{"type":"redacted_thinking","data":"xyz"},{"type":"text","text":""}]`, ""))
	assert.Empty(t, h.rows())
}

func TestUnknownBlockAndLineReachTheTranscript(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, assistantLine(`[{"type":"future_block","value":1}]`, ""))
	h.feed(fp, `{"type":"future_line","value":2}`)
	rows := h.rows()
	require.Len(t, rows, 2)
	assert.Equal(t, "future_block", rowBlocks(t, rows[0].Content)[0].(map[string]any)["type"])
	assert.Equal(t, "future_line", decodeRow(t, rows[1].Content)["type"])
}

func TestMalformedAssistantLineIsKeptRaw(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, `{"type":"assistant","message":"not an object"}`)
	rows := h.rows()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"type":"assistant","message":"not an object"}`, string(rows[0].Content))
}

// A steering message that reached Amp's queue after its turn ended runs as a
// turn of its own, which no user line of this worker armed.
func TestAssistantMessageWithNoArmedTurnArmsOne(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, textLine("done", stopReasonEndTurn))
	require.False(t, h.turnActive())

	h.feed(fp, textLine("more", ""))
	assert.True(t, h.turnActive())
	h.feed(fp, textLine("finished", stopReasonEndTurn))
	assert.False(t, h.turnActive())
	assert.Len(t, h.turnEnds(), 2)
}

func TestInitLineStatesTheThreadOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, initLine("T-first"))
	h.feed(fp, initLine("T-first"))
	assert.Equal(t, []string{"T-first"}, h.sink.SessionIDs(), "a repeated init states nothing new")
	assert.True(t, fp.sawInit.Load())

	h.feed(fp, initLine("T-second"))
	assert.Equal(t, []string{"T-first", "T-second"}, h.sink.SessionIDs(), "a different thread replaces the handle")
	assert.Contains(t, h.sink.StatusActives(), "T-second")
}

func TestUnknownSystemLineIsKept(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, `{"type":"system","subtype":"future"}`)
	require.Len(t, h.rows(), 1)
	assert.Empty(t, h.sink.SessionIDs())
}

func TestUsageBroadcastsOnlyAChangedReading(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, textLine("a", ""))
	h.feed(fp, textLine("b", ""))
	assert.Len(t, h.sink.SessionInfoValues(contracts.SessionInfoKeyContextUsage), 1, "the same reading twice broadcasts once")

	h.feed(fp, `{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"c"}],"stop_reason":null,"usage":{"input_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":300,"output_tokens":9}},"session_id":"T-1"}`)
	readings := h.sink.SessionInfoValues(contracts.SessionInfoKeyContextUsage)
	require.Len(t, readings, 2, "a changed reading broadcasts again")
	latest, ok := readings[1].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 405, latest[contracts.ContextUsageFieldContextTokens], "the context is every input token of the request")
}

// An assistant line that states no usage carries no metadata and broadcasts no
// reading: Amp stated nothing to show.
func TestAssistantLineWithNoUsageCarriesNoMetadata(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, `{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":null},"session_id":"T-1"}`)
	rows := h.rows()
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].Metadata)
	assert.Empty(t, h.sink.SessionInfoValues(contracts.SessionInfoKeyContextUsage))
}

// An assistant line with no message cannot be split into blocks, so it reaches
// the transcript whole, and it moves no turn.
func TestAssistantLineWithNoMessageIsKeptRaw(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"a null message":    `{"type":"assistant","message":null,"session_id":"T-1"}`,
		"no message at all": `{"type":"assistant","session_id":"T-1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withResume("T-1"))
			h.feed(nil, line)
			rows := h.rows()
			require.Len(t, rows, 1)
			assert.JSONEq(t, line, string(rows[0].Content))
			assert.Empty(t, h.sink.TurnActives(), "a line the worker cannot read arms no turn")
		})
	}
}

// An assistant message with no content blocks and the end_turn stop reason
// still ends the turn. The turn end then states no text.
func TestEmptyAssistantMessageStillEndsTheTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, assistantLine(`[]`, stopReasonEndTurn))
	assert.Empty(t, h.rows())
	ends := h.turnEnds()
	require.Len(t, ends, 1)
	row := decodeRow(t, ends[0].Content)
	assert.NotContains(t, row, "result", "the turn stated no text")
	assert.EqualValues(t, 1, row["num_turns"])
	assert.False(t, h.turnActive())
}

// A tool call that states no id cannot own a span, so it reaches the transcript
// as a plain row, and no permission request can claim it.
func TestToolCallWithNoIDIsKeptAsAPlainRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	fp := h.send("go")
	h.feed(fp, assistantLine(`[{"type":"tool_use","name":"shell_command","input":{"command":"ls"}}]`, "tool_use"))
	rows := h.rows()
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].SpanID)
	assert.Equal(t, "tool_use", rowBlocks(t, rows[0].Content)[0].(map[string]any)["type"])
	assert.Empty(t, h.sink.OpenSpans())
	h.agent.mu.Lock()
	open := len(h.agent.tools)
	h.agent.mu.Unlock()
	assert.Zero(t, open)
}

// A tool result that states no call id closes no span and counts no tool. A
// result for a call that the worker never saw closes its span and counts, with
// no tool name.
func TestToolResultsThatMatchNoOpenCall(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	fp := h.send("go")
	h.feed(fp, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"orphan","is_error":false}]},"session_id":"T-1"}`)
	h.feed(fp, toolResultLine("TU-never-opened", "late", false))
	h.feed(fp, textLine("done", stopReasonEndTurn))

	rows := h.rows()
	require.Len(t, rows, 3)
	assert.Empty(t, rows[0].SpanID, "a result with no call id owns no span")
	assert.False(t, rows[0].Closing)
	assert.Equal(t, "TU-never-opened", rows[1].SpanID)
	assert.True(t, rows[1].Closing)
	assert.Empty(t, rows[1].SpanType, "the worker never saw the call, so it knows no tool name")
	assert.Equal(t, []string{"TU-never-opened"}, h.sink.ClosedSpans())

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	count, ok := agent.DefaultTurnEndToolUses(agent.ResolveMessageContent(ampProvider{}, agent.MessageContent{Original: ends[0].Content, Metadata: ends[0].Metadata}))
	require.True(t, ok)
	assert.EqualValues(t, 1, count, "only the result with a call id counts")
}

// The turn end carries the turn's duration on the agent's clock, beside the
// last context reading.
func TestTurnEndStatesTheTurnDuration(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.clock.Advance(1500 * time.Millisecond)
	h.feed(fp, textLine("done", stopReasonEndTurn))

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(ends[0].Metadata, &metadata))
	assert.EqualValues(t, 1500, metadata[contracts.MessageMetadataFieldDurationMs])
	usage, ok := metadata[contracts.SessionInfoKeyContextUsage].(map[string]any)
	require.True(t, ok, "the turn end carries the last context reading")
	assert.EqualValues(t, 300, usage[contracts.ContextUsageFieldContextTokens])
}

func TestTurnDurationMs(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	assert.Nil(t, turnDurationMs(turnState{}, start), "a turn with no start has no duration")
	assert.Nil(t, turnDurationMs(turnState{startedAt: start}, start.Add(-time.Millisecond)), "a clock that moved backwards states no duration")
	zero := turnDurationMs(turnState{startedAt: start}, start)
	require.NotNil(t, zero)
	assert.Zero(t, *zero)
	long := turnDurationMs(turnState{startedAt: start}, start.Add(90*time.Minute))
	require.NotNil(t, long)
	assert.EqualValues(t, 90*60*1000, *long)
}

func TestTurnMetadata(t *testing.T) {
	t.Parallel()
	assert.Nil(t, turnMetadata(nil, nil), "nothing to state encodes nothing")
	assert.Nil(t, turnMetadata(map[string]any{}, nil))
	ms := int64(42)
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldDurationMs+`":42}`, string(turnMetadata(nil, &ms)))
}

func TestSplitBlocks(t *testing.T) {
	t.Parallel()
	rows, err := splitBlocks([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"thinking","thinking":"b"}],"stop_reason":"end_turn"},"session_id":"T-1"}`))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "text", rows[0].block.Type)
	assert.Equal(t, "b", rows[1].block.Thinking)
	for i, row := range rows {
		line := decodeRow(t, row.line)
		assert.Equal(t, "T-1", line["session_id"], "row %d keeps the line's fields", i)
		message := line["message"].(map[string]any)
		assert.Equal(t, "end_turn", message["stop_reason"], "row %d keeps the message's fields", i)
		assert.Len(t, message["content"], 1, "row %d holds its own block alone", i)
	}

	for name, line := range map[string]string{
		"an empty content list": `{"message":{"content":[]}}`,
		"no content":            `{"message":{"role":"assistant"}}`,
		"a null content":        `{"message":{"content":null}}`,
	} {
		rows, err := splitBlocks([]byte(line))
		require.NoErrorf(t, err, "%s splits", name)
		assert.Emptyf(t, rows, "%s holds no block", name)
	}

	for name, line := range map[string]string{
		"a line that is not an object":    `[1]`,
		"no message":                      `{"type":"assistant"}`,
		"a message that is not an object": `{"message":"text"}`,
		"a content that is not a list":    `{"message":{"content":"text"}}`,
		"a block that is not an object":   `{"message":{"content":[1]}}`,
	} {
		_, err := splitBlocks([]byte(line))
		assert.Errorf(t, err, "%s does not split", name)
	}
}

// An init line with no thread id still shows that the process opened its
// thread, and states no handle.
func TestInitLineWithNoThreadStatesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	statusBefore := h.sink.StatusActiveCount()
	h.feed(fp, `{"type":"system","subtype":"init","cwd":"/work"}`)
	assert.True(t, fp.sawInit.Load())
	assert.Empty(t, h.sink.SessionIDs())
	assert.Equal(t, statusBefore, h.sink.StatusActiveCount())
	assert.Empty(t, h.rows())
}

// A success `result` while a turn runs ends that turn as complete, with Amp's
// own row, and resumes nothing.
func TestSuccessResultEndsARunningTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, textLine("partial", ""))
	result := `{"type":"result","subtype":"success","duration_ms":9,"is_error":false,"num_turns":1,"result":"partial","session_id":"T-1"}`
	h.feed(fp, result)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionComplete, ends[0].Completion)
	assert.JSONEq(t, result, string(ends[0].Content))
	assert.False(t, fp.resumeAfterExit.Load())
	assert.True(t, fp.ending(), "a process that printed its result takes no new line")
	assert.Empty(t, h.sink.Notifications())
}

// The probe transcript of an interrupt: a call that a rule refused, a second
// call that still runs, and Amp's cancellation. The turn ends as interrupted
// with Amp's own row, the running call closes as interrupted with its own
// row, and nothing resumes.
func TestProbeInterruptTranscript(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("Run printf probe, then sleep 40")
	lines := fixtureLines(t, "probe_interrupt.jsonl")
	require.Len(t, lines, 6)
	for _, line := range lines[:5] {
		h.feed(fp, line)
	}
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	h.feed(fp, lines[5])
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	assert.JSONEq(t, lines[5], string(ends[0].Content), "the turn end is Amp's own result")
	count, ok := agent.DefaultTurnEndToolUses(agent.ResolveMessageContent(ampProvider{}, agent.MessageContent{Original: ends[0].Content, Metadata: ends[0].Metadata}))
	require.True(t, ok)
	assert.EqualValues(t, 2, count, "the turn made two calls: one ended with a result, and the stop ended the other")

	var closing []agenttest.Message
	for _, row := range h.rows() {
		if row.Closing {
			closing = append(closing, row)
		}
	}
	require.Len(t, closing, 2)
	assert.Equal(t, "TU-034UCBQCRDlhpWgZReDyMW", closing[0].SpanID, "the refused call closes with its result")
	assert.Equal(t, agent.MessageCompletion(""), closing[0].Completion)
	assert.Equal(t, "TU-034UCBVxnlUouW7HpAbRoS", closing[1].SpanID, "the running call closes at the turn end")
	assert.Equal(t, agent.MessageCompletionInterrupted, closing[1].Completion)
	assert.Equal(t, "tool_use", rowBlocks(t, closing[1].Content)[0].(map[string]any)["type"])

	assert.Equal(t, "T-01a0d1c6-c614-728f-a585-70ccc160a717", h.sink.LastSessionID())
	assert.Equal(t, []string{""}, h.startedThreads(), "an interrupt resumes nothing at once")
	assert.Empty(t, h.sink.Notifications())
}

// The probe transcript of two turns in one process: a call that the reader
// allowed and one that the reader refused, then a second turn that the reader
// steered while its call ran. Every user echo, the steering one included, stays
// out of the transcript, and the process's own success result after the last
// turn states nothing.
func TestProbeSteerTranscript(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("Run printf a, then printf b")
	lines := fixtureLines(t, "probe_steer_permission.jsonl")
	require.Len(t, lines, 13)
	for _, line := range lines[:7] {
		h.feed(fp, line)
	}
	require.False(t, h.turnActive(), "the first turn ended at its end_turn message")

	require.NoError(t, h.agent.SendInput("Run sleep 8, then reply finished", nil))
	for _, line := range lines[7:10] {
		h.feed(fp, line)
	}
	require.NoError(t, h.agent.SteerInput("Also append the word steered to your final reply.", nil))
	for _, line := range lines[10:] {
		h.feed(fp, line)
	}

	stdin := fp.lines()
	require.Len(t, stdin, 3, "two turns and one steering line, all in one process")
	assert.Nil(t, stdin[1]["steer"])
	assert.Equal(t, true, stdin[2]["steer"])
	assert.Equal(t, []string{""}, h.startedThreads())

	ends := h.turnEnds()
	require.Len(t, ends, 2)
	for i, want := range []struct {
		text     string
		messages int
		tools    int32
	}{
		{text: "done", messages: 3, tools: 2},
		{text: "finished steered", messages: 2, tools: 1},
	} {
		assert.Equal(t, agent.MessageCompletionComplete, ends[i].Completion)
		row := decodeRow(t, ends[i].Content)
		assert.Equal(t, want.text, row["result"])
		assert.EqualValues(t, want.messages, row["num_turns"])
		count, ok := agent.DefaultTurnEndToolUses(agent.ResolveMessageContent(ampProvider{}, agent.MessageContent{Original: ends[i].Content, Metadata: ends[i].Metadata}))
		require.True(t, ok)
		assert.EqualValues(t, want.tools, count, "turn %d", i)
	}

	for _, row := range h.rows() {
		if decodeRow(t, row.Content)["type"] != contracts.AmpLineTypeUser {
			continue
		}
		block := rowBlocks(t, row.Content)[0].(map[string]any)
		assert.Equal(t, "tool_result", block["type"], "no user echo reaches the transcript")
	}
	refused := h.rows()[3]
	assert.Equal(t, "TU-034UC69EBKMhee2GCskCKE", refused.SpanID)
	assert.True(t, refused.Closing, "the refused call closes with the reason that the model saw")
	assert.Empty(t, h.sink.Notifications())
	assert.Empty(t, h.sink.BackgroundTasks(), "a refused call starts no background command")
	assert.False(t, h.turnActive())
}

func TestErrorResultEndsTheTurnAndAsksForAResume(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("retry please")
	h.feedFixture(fp, "probe_retry_error.jsonl")

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	assert.Equal(t, "Model Provider Overloaded Try again in a few seconds.", decodeRow(t, ends[0].Content)["error"], "Amp's own result row is the turn end")
	assert.True(t, fp.resumeAfterExit.Load(), "an error that ended a turn resumes the thread")
	assert.True(t, fp.ending())
}

func TestInterruptedResultEndsTheTurnAsInterrupted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("sleep")
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	h.feed(fp, errorResult(interruptedMessage))

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	assert.False(t, fp.resumeAfterExit.Load(), "an interrupt waits for the next message")
}

// Native frames of a stop, from Amp 0.0.1791074829 (the `probe_interrupt_*`
// fixtures). Amp answers EVERY cancel signal with one `result` that states its
// cancel wording, whether the signal finds the model request held, a tool
// running, the answer printed, or the process idle. The `result` alone cannot
// tell a finished turn from an abort. The stream tells: the end_turn message
// comes first when the turn finished, and Amp prints its own failure instead of
// the cancel wording when a failure wins the race against the signal.

// A stop while the model request waits: Amp prints its init line and the user
// echo late, then the cancel result. The turn ends as interrupted with Amp's
// own row, and nothing resumes.
func TestProbeInterruptDuringAModelRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("Probe the interrupt race.")
	lines := fixtureLines(t, "probe_interrupt_model.jsonl")
	require.Len(t, lines, 3)
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	for _, line := range lines {
		h.feed(fp, line)
	}
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	assert.JSONEq(t, lines[2], string(ends[0].Content), "the turn end is Amp's own result")
	assert.False(t, fp.resumeAfterExit.Load(), "an interrupt waits for the next message")
	assert.Equal(t, []string{""}, h.startedThreads())
	assert.Empty(t, h.sink.Notifications())
}

// The stop reaches the worker while the answer is still in the pipe. The
// end_turn message ends the turn as complete, whatever the stop note says, and
// the cancel result that follows finds no turn. It is Amp's answer to the
// signal that the worker sent, so it is no agent error. The note does not
// reach the next turn.
func TestATurnThatFinishedBeforeTheStopKeepsItsOutcome(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("Probe the interrupt race.")
	lines := fixtureLines(t, "probe_interrupt_finished.jsonl")
	require.Len(t, lines, 4)
	h.feed(fp, lines[0])
	h.feed(fp, lines[1])
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	h.feed(fp, lines[2])
	h.feed(fp, lines[3])
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionComplete, ends[0].Completion, "Amp's end_turn message ended the turn before the stop")
	assert.Empty(t, h.sink.Notifications(), "Amp's cancel result answers the worker's own signal")
	assert.False(t, fp.resumeAfterExit.Load())
	assert.Equal(t, []string{""}, h.startedThreads())

	// The next turn runs in a new process, and it ends as complete.
	require.NoError(t, h.agent.SendInput("next", nil))
	require.Equal(t, []string{"", "T-acd2eb8e-93b8-4de7-926a-c08625532237"}, h.startedThreads())
	next := h.nextProcAfter(fp)
	h.feed(next, textLine("again", stopReasonEndTurn))
	ends = h.turnEnds()
	require.Len(t, ends, 2)
	assert.Equal(t, agent.MessageCompletionComplete, ends[1].Completion)
}

// A model failure that wins the race against the stop. Amp prints its own
// failure, not its cancel wording, so the turn FAILED on its own: the divider
// states the failure, and the thread resumes at once, as for any failure.
func TestAFailureThatEndedTheTurnBeforeTheStopIsAnError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("Probe the interrupt race.")
	lines := fixtureLines(t, "probe_interrupt_failed.jsonl")
	require.Len(t, lines, 3)
	h.feed(fp, lines[0])
	h.feed(fp, lines[1])
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	h.feed(fp, lines[2])

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	assert.JSONEq(t, lines[2], string(ends[0].Content), "the turn end is Amp's own result")
	assert.True(t, fp.resumeAfterExit.Load(), "an error that ended a turn resumes the thread")
	assert.Empty(t, h.sink.Notifications())
}

// The cancel wording ends the turn as an error when the worker sent no signal:
// an outside SIGINT is no stop of the user's.
func TestACancelResultThatNoStopRequestedEndsTheTurnAsAnError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("sleep")
	h.feed(fp, errorResult(interruptedMessage))

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	assert.True(t, fp.resumeAfterExit.Load(), "an error that ended a turn resumes the thread")
}

// The same outside signal with no turn running still reaches the user: it
// ended the process, and the worker did not ask for that.
func TestACancelResultThatNoStopRequestedWithNoTurnIsANotification(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, textLine("done", stopReasonEndTurn))
	h.feed(fp, errorResult(interruptedMessage))

	notifications := h.sink.Notifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, interruptedMessage, notifications[0][contracts.NotificationFieldError])
}

// Stop signals an idle process too, and Amp answers with the same cancel result
// (probed). The agent asked for it, so it is no error.
func TestAStopOfAnIdleAgentStatesNoError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, textLine("done", stopReasonEndTurn))
	fp.stdin.onClose = func() {
		h.feed(fp, errorResult(interruptedMessage))
		fp.exit()
	}

	h.agent.Stop()
	fp.awaitHandled(t)
	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionComplete, ends[0].Completion)
	assert.Empty(t, h.sink.Notifications())
}

// A stop that lost the race against a failure keeps the failure when the
// stop is the agent's own Stop, too.
func TestStopKeepsAFailureThatEndedTheTurnFirst(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	lines := fixtureLines(t, "probe_interrupt_failed.jsonl")
	h.feed(fp, lines[0])
	fp.stdin.onClose = func() {
		h.feed(fp, lines[2])
		fp.exit()
	}

	h.agent.Stop()
	fp.awaitHandled(t)
	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	assert.Equal(t, []string{""}, h.startedThreads(), "a stopped agent resumes nothing")
}

// No probed signal printed a success `result`, so the shape is not known to
// follow a stop. A turn with no end_turn message did not finish, and the stop is
// the only cause that the worker knows, so the note still decides.
func TestASuccessResultAfterAStopRequestEndsTheTurnAsInterrupted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("sleep")
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	h.feed(fp, `{"type":"result","subtype":"success","duration_ms":9,"is_error":false,"num_turns":1,"result":"partial","session_id":"T-1"}`)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	assert.False(t, fp.resumeAfterExit.Load())
}

// The agent records its signal on the process when it sends one, and only then:
// Interrupt and Stop both go through signalInterrupt.
func TestSignalsRecordThemselvesOnTheProcess(t *testing.T) {
	t.Parallel()
	t.Run("interrupt", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		fp := h.send("sleep")
		assert.False(t, fp.interruptSignalled.Load(), "a process the agent never signalled")
		require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
		assert.True(t, fp.interruptSignalled.Load())
	})
	t.Run("stop", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		fp := h.send("sleep")
		h.agent.Stop()
		assert.True(t, fp.interruptSignalled.Load())
	})
	t.Run("a stop request with no turn sends no signal", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		fp := h.send("go")
		h.feed(fp, textLine("done", stopReasonEndTurn))
		require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
		assert.False(t, fp.interruptSignalled.Load())
	})
}

// The two readings of a `result` that states an error, on the native frames. The
// subtype and the error flag are the same for both, so the error wording decides.
func TestResultLineStatesCancelOrFailure(t *testing.T) {
	t.Parallel()
	cancel := fixtureLines(t, "probe_interrupt_model.jsonl")[2]
	failure := fixtureLines(t, "probe_interrupt_failed.jsonl")[2]
	tests := []struct {
		name    string
		line    string
		cancel  bool
		failure bool
	}{
		{name: "the cancel result of a stop", line: cancel, cancel: true},
		{name: "the cancel result after an answer", line: fixtureLines(t, "probe_interrupt_finished.jsonl")[3], cancel: true},
		{name: "a model failure", line: failure, failure: true},
		{name: "an error with no wording", line: `{"type":"result","subtype":"error_during_execution","is_error":true}`, failure: true},
		{name: "a success", line: `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"ok"}`},
		{name: "a cancel wording that is no error", line: `{"type":"result","subtype":"success","is_error":false,"error":"` + interruptedMessage + `"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var line ampLine
			require.NoError(t, json.Unmarshal([]byte(tc.line), &line))
			assert.Equal(t, tc.cancel, line.statesCancel())
			assert.Equal(t, tc.failure, line.statesFailure())
		})
	}
}

func TestAnswersSignal(t *testing.T) {
	t.Parallel()
	cancel := ampLine{IsError: true, Error: interruptedMessage}
	failure := ampLine{IsError: true, Error: "Model Provider Overloaded"}
	signalled := &ampProcess{}
	signalled.interruptSignalled.Store(true)

	assert.True(t, answersSignal(signalled, cancel), "the cancel wording after the agent's signal")
	assert.False(t, answersSignal(&ampProcess{}, cancel), "a cancel wording with no signal of the agent's")
	assert.False(t, answersSignal(nil, cancel), "no process")
	assert.False(t, answersSignal(signalled, failure), "a failure that the signal did not cause")
	assert.False(t, answersSignal(signalled, ampLine{}), "a success")
}

func TestErrorResultWithNoTurnIsANotification(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.feed(nil, errorResult("Thread actor unavailable"))
	assert.Empty(t, h.turnEnds())
	notifications := h.sink.Notifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, contracts.NotificationTypeAgentError, notifications[0][contracts.NotificationFieldType])
	assert.Equal(t, "Thread actor unavailable", notifications[0][contracts.NotificationFieldError])
}

func TestSuccessResultWithNoTurnStatesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.feed(nil, `{"type":"result","subtype":"success","duration_ms":1,"is_error":false,"num_turns":1,"result":"ok","session_id":"T-1"}`)
	assert.Empty(t, h.sink.Messages())
	assert.Empty(t, h.sink.Notifications())
}

// The worker closes a call that the turn outlived with its own opening row and
// a completion that states it did not finish. Its subagent row takes the
// matching status.
func TestTurnEndClosesTheCallsItOutlived(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, assistantLine(`[`+
		toolUseBlock("TU-shell", "shell_command", `{"command":"sleep 40"}`)+`,`+
		toolUseBlock("TU-task", contracts.AmpSubagentToolTask, `{"description":"Say pong","prompt":"Reply pong"}`)+
		`]`, "tool_use"))
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	h.feed(fp, errorResult(interruptedMessage))

	var closing []agenttest.Message
	for _, row := range h.rows() {
		if row.Closing {
			closing = append(closing, row)
		}
	}
	require.Len(t, closing, 2)
	assert.Equal(t, "TU-shell", closing[0].SpanID, "calls close in the order they started")
	assert.Equal(t, agent.MessageCompletionInterrupted, closing[0].Completion)
	assert.Equal(t, "tool_use", rowBlocks(t, closing[0].Content)[0].(map[string]any)["type"], "the closing row is the call's own row")
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusStopped}, h.sink.BackgroundTaskStatuses("TU-task"))
	assert.ElementsMatch(t, []string{"TU-shell", "TU-task"}, h.sink.ClosedSpans())
	// The turn made both calls, so its divider counts both, although neither ended with a result.
	assert.Equal(t, []int{2}, agenttest.TurnToolUseCounts(t, h.turnEnds()))
}

func TestDiscardedOutputPersistsNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.agent.DiscardOutput()
	h.feed(fp, textLine("done", stopReasonEndTurn))
	assert.Empty(t, h.sink.Messages())
}

// Each provider lists the frames that move its turn flag, and a frame that this
// build does not know moves nothing. A fresh agent has no pending steer, so its
// user echo is inert. TestSteerAfterEndTurnIsNotRunTwice covers a matched steer echo.
func TestTurnFrames(t *testing.T) {
	t.Parallel()
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "init", Line: initLine("T-1"), Moves: false},
		{Name: "unmatched user echo", Line: userEchoLine("hi"), Moves: false},
		{Name: "tool result", Line: toolResultLine("TU-1", "ok", false), Moves: false},
		{Name: "assistant text", Line: textLine("hi", ""), Moves: true},
		{Name: "assistant end_turn", Line: textLine("hi", stopReasonEndTurn), Moves: true},
		{Name: "result success", Line: `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"session_id":"T-1"}`, Moves: false},
		{Name: "result error", Line: errorResult("boom"), Moves: false},
		{Name: "a line Amp does not send yet", Line: `{"type":"thread_title","title":"x"}`, Moves: false},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		h := newHarness(t)
		h.feed(nil, tc.Line)
		return h.sink.TurnActives()
	})
}

// --- Automatic retry ---

// ampFreeTierText is the hourly free-tier text of Amp 0.0.1781034998: the title
// and the description of its credits error, joined by a space, as the
// stream-json rewrite joins them. No probe printed it, because that rewrite
// passes freeTierEnabled false. The test composes it from the bundle's strings.
const ampFreeTierText = "Out of Credits Add credits to keep using Amp right now, or wait until the next hour starts for more free usage."

// The classifier reads Amp's `result` lines. The research lines come from
// .tmp/retry-research/amp-droid.md. The passthrough lines are what Amp printed
// for an error that the probe server sent (.tmp/probe/amp/runs). Lines marked
// synthetic test a rule that no capture reaches.
func TestAmpTurnFailure(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 7, 14, 10, 34, 45, 0, time.UTC)
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Amp reported a transient failure"}
	rateLimited := providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Amp reported a rate limit"}
	quota := providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated, Cause: "Amp reported an exhausted usage quota"}
	for _, tc := range []struct {
		name       string
		completion agent.MessageCompletion
		stop       bool
		line       string
		want       providerkit.TurnFailure
	}{
		{
			name: "an overload is transient",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":356,"is_error":true,"num_turns":0,"error":"Model Provider Overloaded Try again in a few seconds.","session_id":"T-cdee64e4-e764-4104-8f4d-d00deb0a4c5e"}`,
			want: transient,
		},
		{
			name: "a stalled stream is transient",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":417,"is_error":true,"num_turns":0,"error":"Model Stream Timed Out The model stopped sending data before finishing. Try again in a few seconds.","session_id":"T-0bdca618-6e7e-4699-8184-0bedf3d5fc4b"}`,
			want: transient,
		},
		{
			name: "out of credits is a billing block",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":326,"is_error":true,"num_turns":0,"error":"Out of Credits Add credits to keep using Amp.","session_id":"T-7e27ec98-d46a-470b-a686-29aa862fd605"}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Amp reported that the account is out of credits"},
		},
		{
			name: "a full context is not retryable",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":334,"is_error":true,"num_turns":0,"error":"Context Limit Reached This conversation has reached the context window limit. Start a new thread to continue.","session_id":"T-c6518ebe-a1c1-4930-a601-3e52bfba9024"}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "the hourly free tier is a usage limit of a week or less before the billing prefix",
			line: errorResult(ampFreeTierText),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Amp reported its hourly free usage limit"},
		},
		{
			// The body is Anthropic's, and the vendor reads its api_error type.
			name: "a passthrough API error 500 is transient",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":327,"is_error":true,"num_turns":0,"error":"API error (500): {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Internal server error\"}}","session_id":"T-1bbd1fc7-61d2-463b-a19e-cf4f21b421dc"}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "the vendor reported a transient failure"},
		},
		{
			name: "a passthrough API error 500 whose body no vendor rule reads is transient (synthetic)",
			line: errorResult(`API error (502): {"error":"bad gateway"}`),
			want: transient,
		},
		{
			// A passthrough status is generic. The vendor's body states a monthly
			// limit, which a retry within the week does not lift.
			name: "a passthrough 5xx body with a monthly vendor limit is a longer usage limit (synthetic)",
			line: errorResult(`API error (500): {"error":{"message":"You have reached your monthly usage limit."}}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"},
		},
		{
			name: "a passthrough 5xx status line with a monthly vendor limit is a longer usage limit (synthetic)",
			line: errorResult(`503 You have reached your monthly usage limit.`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"},
		},
		{
			name: "Amp's own overload title decides before a vendor's wording (synthetic)",
			line: errorResult(`Model Provider Overloaded Try again in a few seconds. You have reached your monthly usage limit.`),
			want: transient,
		},
		{
			name: "a passthrough 503 status line is transient",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":327,"is_error":true,"num_turns":0,"error":"503 Service Unavailable","session_id":"T-b4daf5b6-0645-433f-8fe6-97d5ea447bf7"}`,
			want: transient,
		},
		{
			name: "a failed fetch is transient",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":337,"is_error":true,"num_turns":0,"error":"fetch failed: ECONNRESET","session_id":"T-c6810453-f2af-4d53-acf6-5f228f5ba1c4"}`,
			want: transient,
		},
		{
			name: "a bare connection reset is transient",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":336,"is_error":true,"num_turns":0,"error":"ECONNRESET","session_id":"T-eeed2469-8f91-4cc3-a556-5399cc94e07a"}`,
			want: transient,
		},
		{
			name: "a terminated stream is transient",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":333,"is_error":true,"num_turns":0,"error":"terminated","session_id":"T-04a97878-27c1-4b35-9fa1-dec9575079c4"}`,
			want: transient,
		},
		{
			name: "an incomplete response is transient",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":333,"is_error":true,"num_turns":0,"error":"response incomplete","session_id":"T-1661a524-8a2e-4387-9aa0-9a8d821dbae7"}`,
			want: transient,
		},
		{
			name: "a plain rate limit is rate limited",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":340,"is_error":true,"num_turns":0,"error":"Rate limit exceeded","session_id":"T-5e6c57fb-2341-4bca-9afa-0b4ba3fca437"}`,
			want: rateLimited,
		},
		{
			name: "a 429 body that a vendor rule reads takes the vendor verdict",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":364,"is_error":true,"num_turns":0,"error":"429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"Number of request tokens has exceeded your per-minute rate limit\"}}","session_id":"T-637c99e6-ae88-4f00-b3db-c9beb92dd61e"}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{
			name: "a 429 body that no vendor rule reads is rate limited (synthetic)",
			line: errorResult(`429 {"error":"too many"}`),
			want: rateLimited,
		},
		{
			name: "a 429 body with a monthly vendor limit is a longer usage limit (synthetic)",
			line: errorResult(`429 {"error":{"message":"You have reached your monthly usage limit."}}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"},
		},
		{
			name: "a bare rate_limit_error code decides nothing",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":341,"is_error":true,"num_turns":0,"error":"rate_limit_error: Rate limit exceeded","session_id":"T-080a1072-51ad-4066-a51b-43dfeff32a25"}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "an exceeded usage quota is a usage limit with no stated window",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":805,"is_error":true,"num_turns":0,"error":"You've exceeded your usage quota of $100.00 for this month. Contact your workspace admin.","session_id":"T-15322159-aeab-45a6-a1bd-252a57cb7fb8"}`,
			want: quota,
		},
		{
			// Amp's own quota wording is more specific than the status before it.
			name: "a 429 that states Amp's usage quota is a usage limit with no stated window (synthetic)",
			line: errorResult(`429 You've exceeded your usage quota of $100.00 for this month. Contact your workspace admin.`),
			want: quota,
		},
		{
			name: "a free usage limit is a usage limit with no stated window",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":331,"is_error":true,"num_turns":0,"error":"You've reached your free usage limit. Add credits or wait until the next hour.","session_id":"T-079ced8c-d06f-437e-8c45-b35164477168"}`,
			want: quota,
		},
		{
			name: "a definitive vendor billing code wins over the 500 prefix (synthetic)",
			line: errorResult(`API error (500): {"error":{"code":"insufficient_quota"}}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported insufficient_quota"},
		},
		{
			name: "a Z.AI usage limit in a 429 body schedules at its stamp (synthetic wrapper)",
			line: errorResult(`429 {"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13"}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Date(2026, 7, 14, 11, 6, 14, 0, time.UTC), Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
		{
			name: "an unknown error is not retryable",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":333,"is_error":true,"num_turns":0,"error":"Gemini requests are limited to 100 turns","session_id":"T-a02907d4-498e-4832-b9a2-d04d3654feaa"}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "an empty error is not retryable",
			line: errorResult(""),
			want: providerkit.TurnFailure{},
		},
		{
			name: "an error that is not a string is not retryable",
			line: `{"type":"result","subtype":"error_during_execution","is_error":true,"error":{"message":"Model Provider Overloaded Try again in a few seconds."}}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "a malformed line is not retryable",
			line: `{"type":"result","error":"Model Provider Overloaded `,
			want: providerkit.TurnFailure{},
		},
		{
			name:       "a complete turn is not retryable",
			completion: agent.MessageCompletionComplete,
			line:       errorResult("Model Provider Overloaded Try again in a few seconds."),
			want:       providerkit.TurnFailure{},
		},
		{
			name:       "an interrupted turn is not retryable",
			completion: agent.MessageCompletionInterrupted,
			line:       errorResult(interruptedMessage),
			want:       providerkit.TurnFailure{},
		},
		{
			name: "a failure of a stopped turn is not retryable",
			stop: true,
			line: errorResult("Model Provider Overloaded Try again in a few seconds."),
			want: providerkit.TurnFailure{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			completion := tc.completion
			if completion == "" {
				completion = agent.MessageCompletionError
			}
			assert.Equal(t, tc.want, ampTurnFailure(completion, tc.stop, []byte(tc.line), observedAt))
		})
	}
}

// The probe transcript of an overload ends the turn on Amp's own result row,
// and the server-error retry follows that row.
func TestProbeRetryErrorSchedulesAServerErrorRetryAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("retry please")
	h.feedFixture(fp, "probe_retry_error.jsonl")

	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{
		Kind: providerkit.TurnFailureTransient, Cause: "Amp reported a transient failure",
	}), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
	require.Equal(t, 1, h.sink.AutoScheduleCount())
	assert.JSONEq(t, fixtureLines(t, "probe_retry_error.jsonl")[2], string(h.sink.LastAutoSchedule().SourcePayload),
		"the retry carries the result line that failed")
}

// A failed result line decides the verdict of its turn through the real
// handler. The lines are the research captures of Amp 0.0.1792915237.
func TestFailedResultLinesApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		line string
		want providerkit.TurnFailure
	}{
		{
			name: "an overload schedules a server-error retry",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":356,"is_error":true,"num_turns":0,"error":"Model Provider Overloaded Try again in a few seconds.","session_id":"T-cdee64e4-e764-4104-8f4d-d00deb0a4c5e"}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Amp reported a transient failure"},
		},
		{
			name: "out of credits cancels both retries",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":326,"is_error":true,"num_turns":0,"error":"Out of Credits Add credits to keep using Amp.","session_id":"T-7e27ec98-d46a-470b-a686-29aa862fd605"}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Amp reported that the account is out of credits"},
		},
		{
			name: "a full context cancels the server-error retry",
			line: `{"type":"result","subtype":"error_during_execution","duration_ms":334,"is_error":true,"num_turns":0,"error":"Context Limit Reached This conversation has reached the context window limit. Start a new thread to continue.","session_id":"T-c6518ebe-a1c1-4930-a601-3e52bfba9024"}`,
			want: providerkit.TurnFailure{},
		},
		{
			// The hourly free-tier text starts with the billing title, so only the
			// order of the rules keeps it from reading as a billing block.
			name: "the hourly free tier schedules a server-error retry and not a billing block",
			line: errorResult(ampFreeTierText),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Amp reported its hourly free usage limit"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			fp := h.send("go")
			h.feed(fp, tc.line)
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
		})
	}
}

// A turn that completes cancels the server-error retry once, after its row.
// The line is the research capture of a turn that recovered after the
// server's own retries (.tmp/retry-research/amp-droid.md).
func TestACompletedTurnCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, `{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"recovered"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5,"service_tier":"standard"}},"parent_tool_use_id":null,"session_id":"T-c6a7b118-3c90-4c71-b915-ce522680a120"}`)
	ends := h.turnEnds()
	require.Len(t, ends, 1)
	require.Equal(t, agent.MessageCompletionComplete, ends[0].Completion)
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
}

// A stop whose cancel result ends the turn as interrupted never retries.
func TestAnInterruptedTurnCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("sleep")
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	h.feed(fp, errorResult(interruptedMessage))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
}

// A failure that wins the race against the user's interrupt ends the turn as
// an error (probe_interrupt_failed.jsonl). The user asked to stop, so the
// failure must not schedule a retry.
func TestAFailureThatWinsTheRaceAgainstAnInterruptDoesNotRetry(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("Probe the interrupt race.")
	lines := fixtureLines(t, "probe_interrupt_failed.jsonl")
	h.feed(fp, lines[0])
	h.feed(fp, lines[1])
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	h.feed(fp, lines[2])

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	require.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
}

// The same race against the agent's own Stop also schedules nothing.
func TestAFailureThatWinsTheRaceAgainstStopDoesNotRetry(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	lines := fixtureLines(t, "probe_interrupt_failed.jsonl")
	h.feed(fp, lines[0])
	fp.stdin.onClose = func() {
		h.feed(fp, lines[2])
		fp.exit()
	}

	h.agent.Stop()
	fp.awaitHandled(t)
	ends := h.turnEnds()
	require.Len(t, ends, 1)
	require.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
}

// A process that dies with no result ends the running turn on the worker's own
// error row, and that row decides one verdict too.
func TestAProcessExitWithNoResultAppliesOneVerdict(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	require.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
}

// A process that crashes with no result ends the turn on the row that the
// worker writes. Its error states how the process exited, not a model failure,
// so a connection reset in its stderr must not schedule a retry.
func TestAProcessCrashWithAConnectionResetDoesNotRetry(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.stderrFn = func(*ampProcess) string { return "Error: read ECONNRESET\n" }
	fp := h.send("go")
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	require.Equal(t, agent.MessageCompletionError, ends[0].Completion)
	require.Contains(t, decodeRow(t, ends[0].Content)["error"], "Error: read ECONNRESET", "the worker's row states the stderr")
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
}

// A result that arrives with no turn running ends no turn, so it applies no
// verdict: the earlier turn end already decided.
func TestAResultWithNoTurnRunningAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.feed(fp, textLine("done", stopReasonEndTurn))
	h.feed(fp, errorResult("Model Provider Overloaded Try again in a few seconds."))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
}

// A turn whose output the service discards writes no row, so it applies no
// verdict either.
func TestADiscardedTurnAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("go")
	h.agent.DiscardOutput()
	h.feed(fp, errorResult("Model Provider Overloaded Try again in a few seconds."))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
}
