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
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

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
			h := newHarness(t)
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
	h := newHarness(t)
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
	h := newHarness(t)
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
	require.NoError(t, h.agent.Interrupt())
	h.feed(fp, lines[5])
	fp.exit()
	fp.awaitHandled(t)

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	assert.JSONEq(t, lines[5], string(ends[0].Content), "the turn end is Amp's own result")
	count, ok := agent.DefaultTurnEndToolUses(agent.ResolveMessageContent(ampProvider{}, agent.MessageContent{Original: ends[0].Content, Metadata: ends[0].Metadata}))
	require.True(t, ok)
	assert.EqualValues(t, 1, count, "one call ended with a result")

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
	require.NoError(t, h.agent.Interrupt())
	h.feed(fp, errorResult(interruptedMessage))

	ends := h.turnEnds()
	require.Len(t, ends, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, ends[0].Completion)
	assert.False(t, fp.resumeAfterExit.Load(), "an interrupt waits for the next message")
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
	require.NoError(t, h.agent.Interrupt())
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
// build does not know moves nothing. A fresh agent has no turn armed, so an
// assistant message is the one frame that arms one (a turn Amp started from its
// own queue).
func TestTurnFrames(t *testing.T) {
	t.Parallel()
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "init", Line: initLine("T-1"), Moves: false},
		{Name: "user echo", Line: userEchoLine("hi"), Moves: false},
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
