package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPTurnOutputOwnsTextAndIncompleteToolLifecycles(t *testing.T) {
	t.Parallel()

	var output acpTurnOutput
	output.appendAssistant("answer", "")
	assert.True(t, output.appendThought("first ", ""))
	assert.False(t, output.appendThought("second", ""))
	opener := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tool-1","status":"pending"}`)
	output.rememberIncompleteTool("tool-1", map[string]json.RawMessage{
		"toolCallId": json.RawMessage(`"tool-1"`),
	}, opener)
	output.completeTool("tool-complete")

	turn := output.drainTurn()
	assert.Equal(t, "answer", turn.assistantText)
	assert.Equal(t, "first second", turn.thoughtText)
	require.Len(t, turn.incompleteTools, 1)
	assert.Equal(t, "tool-1", turn.incompleteTools[0].toolCallID)
	assert.NoError(t, turn.incompleteTools[0].encodeErr)
	assert.JSONEq(t, string(opener), string(turn.incompleteTools[0].original),
		"the snapshot carries the agent's own frame, not the merged fields")
	assert.Equal(t, 1, turn.completedToolUses)

	turn = output.drainTurn()
	assert.Empty(t, turn.assistantText)
	assert.Empty(t, turn.thoughtText)
	assert.Empty(t, turn.incompleteTools)
	assert.Zero(t, turn.completedToolUses)
}

func TestSegmentTextPreparationKeepsLaterRowsBehindTheSegment(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	b.sink = agent.NewModelProgressResetSink(b.sink)
	b.hooks.SegmentText = func(_ agent.AssembledMessageKind, _, text string) string {
		// A daemon lookup can let another goroutine submit the native turn end.
		// This reentrant write makes that ordering deterministic without a sleep.
		err := b.sink.PersistTurnEnd(agent.MessageContent{Original: json.RawMessage(`{"stopReason":"end_turn"}`)}, agent.SpanInfo{})
		assert.True(t, agent.OutputRetained(err))
		assert.Empty(t, sink.Messages(), "the native end must wait for text selection")
		return text
	}
	b.appendAssistant("before", "message")
	b.FlushBufferedOutputOffReader(agent.MessageCompletionComplete)
	require.Len(t, sink.Messages(), 2)
	assert.False(t, sink.Messages()[0].TurnEnd)
	assert.Contains(t, string(sink.Messages()[0].Content), "before")
	assert.True(t, sink.Messages()[1].TurnEnd)
}

func TestTurnOutputCleanupRunsAfterTheFinalTextAndDivider(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	b.promptActive = true
	cleared := false
	b.hooks.SegmentText = func(_ agent.AssembledMessageKind, _, text string) string {
		assert.False(t, cleared, "the final segment still needs the provider's state")
		return text
	}
	b.hooks.CaptureTurnOutputCleanup = func() func() {
		return func() {
			cleared = true
			rows := sink.Messages()
			require.Len(t, rows, 2)
			assert.True(t, rows[1].TurnEnd)
		}
	}
	b.appendAssistant("answer", "message")
	b.finishPromptRequest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.True(t, cleared)
}

func TestTurnOutputCleanupKeepsTheQueuedAgentTurn(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, _ := newACPTurnBase(t, agenttest.NopStdin(&output))
	b.promptActive = true
	var clears int
	b.hooks.CaptureTurnOutputCleanup = func() func() { return func() { clears++ } }
	b.appendAssistant("first", "first-message")
	require.True(t, b.BeginAgentTurn())
	b.appendAssistant("second", "second-message")
	b.finishPromptRequest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Zero(t, clears)
	b.EndAgentTurn(json.RawMessage(`{"stopReason":"end_turn"}`))
	assert.Equal(t, 1, clears)
}

func TestTurnOutputCleanupWaitsForAFailedWrite(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	b.sink = agent.NewModelProgressResetSink(b.sink)
	sink.PersistErr = assert.AnError
	cleared := false
	b.hooks.CaptureTurnOutputCleanup = func() func() { return func() { cleared = true } }
	b.appendAssistant("answer", "message")
	b.finishPromptRequest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.False(t, cleared)
	sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(b.sink))
	assert.True(t, cleared)
}

// endToolCall feeds one final update of a call, as the conversation does: the merge
// ends the call's state, then the call counts for its turn.
func endToolCall(output *acpTurnOutput, toolCallID string) {
	output.mergeToolUpdate(toolCallID, map[string]json.RawMessage{
		"toolCallId": json.RawMessage(`"` + toolCallID + `"`),
		"status":     json.RawMessage(`"failed"`),
	}, true, json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"`+toolCallID+`","status":"failed"}`))
	output.completeTool(toolCallID)
}

// Junie sends its own `failed` update for a command that a stop ended, then one more
// `failed` update for each exit report of the client terminal. The turn made one call.
func TestACPTurnOutputCountsACallOnceWhenItsFinalUpdateRepeats(t *testing.T) {
	t.Parallel()

	var output acpTurnOutput
	output.rememberIncompleteTool("tool-1", map[string]json.RawMessage{"toolCallId": json.RawMessage(`"tool-1"`)},
		json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tool-1","status":"in_progress"}`))
	for range 3 {
		endToolCall(&output, "tool-1")
	}
	endToolCall(&output, "tool-2")

	turn := output.drainTurn()
	assert.Equal(t, 2, turn.completedToolUses, "each call counts once, however many final updates it gets")
	assert.Empty(t, turn.incompleteTools)
}

// A repeated final update that arrives after the turn drained counts for no turn: the
// next turn drops the count that no turn owned when it starts.
func TestACPTurnOutputDropsARepeatedFinalUpdateAfterTheTurn(t *testing.T) {
	t.Parallel()

	var output acpTurnOutput
	endToolCall(&output, "tool-1")
	assert.Equal(t, 1, output.drainTurn().completedToolUses)

	endToolCall(&output, "tool-1")
	output.turnMu.Lock()
	output.takeUnownedOutputLocked()
	output.turnMu.Unlock()
	endToolCall(&output, "tool-2")
	assert.Equal(t, 1, output.drainTurn().completedToolUses, "the next turn counts its own call alone")
}

// A call that the prompt left open at a boundary counts for the prompt once, also when
// its final update repeats after the boundary.
func TestACPTurnOutputCountsACallOfThePromptOnceAcrossTheBoundary(t *testing.T) {
	t.Parallel()

	var output acpTurnOutput
	output.rememberIncompleteTool("tool-1", map[string]json.RawMessage{"toolCallId": json.RawMessage(`"tool-1"`)},
		json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tool-1","status":"in_progress"}`))
	output.turnMu.Lock()
	output.markPromptBoundaryLocked()
	output.turnMu.Unlock()
	endToolCall(&output, "tool-1")
	endToolCall(&output, "tool-1")

	prompt, hasPromptBoundary := output.drainPromptTurn()
	require.True(t, hasPromptBoundary)
	assert.Equal(t, 1, prompt.completedToolUses, "the prompt made the call once")
	assert.Zero(t, output.drainTurn().completedToolUses, "the agent turn after the boundary made no call")
}

func TestACPTurnOutputSerializesSessionReplacementWithAppend(t *testing.T) {
	t.Parallel()

	var output acpTurnOutput
	output.appendAssistant("old", "")
	boundaryEntered := make(chan struct{})
	releaseBoundary := make(chan struct{})
	replaced := make(chan acpTurnSnapshot, 1)
	go func() {
		replaced <- output.replaceSession(func() {
			close(boundaryEntered)
			<-releaseBoundary
		})
	}()
	<-boundaryEntered

	var appended sync.WaitGroup
	appended.Add(1)
	go func() {
		defer appended.Done()
		output.appendAssistant("new", "")
	}()
	close(releaseBoundary)
	old := <-replaced
	appended.Wait()

	assert.Equal(t, "old", old.assistantText)
	assert.Equal(t, "new", output.drainTurn().assistantText)
}

func TestACPTurnOutputKeepsThoughtChunksVerbatim(t *testing.T) {
	t.Parallel()

	var output acpTurnOutput
	output.appendThought("**Verifying terminal release synchronization", "")
	output.appendThought("Analyzing lock acquisition order and concurrency**", "")

	assert.Equal(t,
		"**Verifying terminal release synchronizationAnalyzing lock acquisition order and concurrency**",
		output.drainTurn().thoughtText,
	)

	var tokenized acpTurnOutput
	tokenized.appendThought("**Analyzing", "")
	tokenized.appendThought(" synchronization**", "")
	assert.Equal(t, "**Analyzing synchronization**", tokenized.drainTurn().thoughtText)
}

func TestACPTerminalHostReadsTheCurrentServiceFacet(t *testing.T) {
	t.Parallel()

	first := &agenttest.Sink{}
	second := &agenttest.Sink{}
	base := &Base{sink: agent.NewProviderServices(first)}
	base.bind(base)
	base.sink = agent.NewProviderServices(second)

	base.copyTerminalOutput(
		&acpTerminalSession{id: "term-1", byteLimit: 0},
		strings.NewReader("x"),
	)
	assert.Empty(t, first.ProgressUpdates())
	assert.Len(t, second.ProgressUpdates(), 1)
}

// session_info_update carries the runtime's own title and modified time, and one
// arrives for EVERY turn. A flush at that update split each assembled message in two,
// so the reader saw one answer as two rows.
func TestACPSessionInfoUpdateKeepsOneAssembledMessage(t *testing.T) {
	t.Parallel()
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"one "}}`))
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"session_info_update","info":{"title":"A title","modifiedAt":"now"}}`))
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"two"}}`))
	assert.Empty(t, sink.Messages(), "session metadata writes no row and closes no segment")
	b.main().flushAssistantBuffer()
	messages := sink.Messages()
	require.Len(t, messages, 1)
	var assembled struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(messages[0].Content, &assembled))
	assert.Equal(t, "one two", assembled.Text)
}

// chunkIDBase is a turn base whose provider states the message of each chunk in
// `_meta.messageId`.
func chunkIDBase(t *testing.T) (*Base, *agenttest.Sink) {
	t.Helper()
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.hooks.ChunkMessageID = func(_ agent.AssembledMessageKind, metadata map[string]json.RawMessage) string {
		var id string
		_ = json.Unmarshal(metadata["messageId"], &id)
		return id
	}
	return b, sink
}

// chunkUpdate is one text or thought chunk, with the message id when id is not "".
func chunkUpdate(updateType, text, id string) json.RawMessage {
	update := map[string]any{"sessionUpdate": updateType, "content": map[string]any{"type": "text", "text": text}}
	if id != "" {
		update["_meta"] = map[string]any{"messageId": id}
	}
	encoded, _ := json.Marshal(update)
	return encoded
}

func TestACPChunkMessageIDSplitsTwoMessages(t *testing.T) {
	t.Parallel()
	b, sink := chunkIDBase(t)

	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "First ", "m1"))
	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "answer.", "m1"))
	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "Second answer.", "m2"))
	b.main().flushAssistantBuffer()

	assert.Equal(t, []string{"text:First answer.", "text:Second answer."}, assembledTexts(t, sink.Messages()))
}

func TestACPChunkMessageIDSplitsTwoThoughts(t *testing.T) {
	t.Parallel()
	b, sink := chunkIDBase(t)

	b.handleACPUpdate(chunkUpdate("agent_thought_chunk", "First thought.", "m1"))
	b.handleACPUpdate(chunkUpdate("agent_thought_chunk", "Second thought.", "m2"))
	b.main().flushThoughtBuffer()

	assert.Equal(t, []string{"thought:First thought.", "thought:Second thought."}, assembledTexts(t, sink.Messages()))
}

// A chunk that states no id continues the buffered text, and so does a chunk
// of the same message after one that stated none.
func TestACPChunkWithNoMessageIDContinuesTheMessage(t *testing.T) {
	t.Parallel()
	b, sink := chunkIDBase(t)

	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "one ", "m1"))
	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "two ", ""))
	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "three", "m1"))
	b.main().flushAssistantBuffer()

	assert.Equal(t, []string{"text:one two three"}, assembledTexts(t, sink.Messages()))
}

// A message that another update already ended starts no empty row when the
// next message arrives.
func TestACPChunkMessageIDAfterAToolCallWritesNoEmptyRow(t *testing.T) {
	t.Parallel()
	b, sink := chunkIDBase(t)

	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "Before the call.", "m1"))
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"Read","kind":"read","status":"completed"}`))
	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "After the call.", "m2"))
	b.main().flushAssistantBuffer()

	assert.Equal(t, []string{"text:Before the call.", "text:After the call."}, assembledTexts(t, sink.Messages()))
}

// Without a provider hook, _meta.messageId is not the spec field.
func TestACPMetaMessageIDIsNotTheSpecField(t *testing.T) {
	t.Parallel()
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))

	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "one ", "m1"))
	b.handleACPUpdate(chunkUpdate("agent_message_chunk", "two", "m2"))
	b.main().flushAssistantBuffer()

	assert.Equal(t, []string{"text:one two"}, assembledTexts(t, sink.Messages()))
}

// A drained turn forgets the message of its last chunk, so the first chunk of
// the next turn stores nothing before it.
func TestACPChunkMessageIDResetsWithTheTurn(t *testing.T) {
	t.Parallel()

	var output acpTurnOutput
	_, flush := output.switchMessage(agent.AssembledMessageKindText, "m1")
	assert.False(t, flush)
	output.appendAssistant("text", "")
	_ = output.drainTurn()
	output.appendAssistant("carried", "")
	_, flush = output.switchMessage(agent.AssembledMessageKindText, "m2")
	assert.False(t, flush,
		"the drain forgot m1, so m2 starts the buffer rather than a second message")
}

// The reader feeds chunks while the end of a prompt drains the turn on another
// goroutine. The message ids live under turnMu with the text, so the race
// detector finds nothing, and no chunk is lost or stored twice.
func TestACPChunkMessageIDIsSafeAgainstATurnDrain(t *testing.T) {
	t.Parallel()
	b, sink := chunkIDBase(t)

	const chunks = 300
	var drained strings.Builder
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range chunks {
			id := []string{"m1", "m2", "m3"}[i%3]
			b.handleACPUpdate(chunkUpdate("agent_message_chunk", id, id))
		}
	}()
	go func() {
		defer wg.Done()
		for range chunks {
			turn := b.drainTurn()
			if turn.assistantText != "" {
				assert.Contains(t, []string{"m1", "m2", "m3"}, turn.assistantMessageID)
				assert.Equal(t, strings.Repeat(turn.assistantMessageID, len(turn.assistantText)/2), turn.assistantText)
			}
			mu.Lock()
			drained.WriteString(turn.assistantText)
			mu.Unlock()
		}
	}()
	wg.Wait()
	b.main().flushAssistantBuffer()

	// "m" appears once per chunk and in no prefix that assembledTexts adds.
	stored := strings.Join(assembledTexts(t, sink.Messages()), "")
	total := strings.Count(stored, "m") + strings.Count(drained.String(), "m")
	assert.Equal(t, chunks, total, "each chunk is stored once or drained once")
}

func specChunkUpdate(t *testing.T, kind, text, id string) json.RawMessage {
	t.Helper()
	update := map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}}
	if id != "" {
		update["messageId"] = id
	}
	raw, err := json.Marshal(update)
	require.NoError(t, err)
	return raw
}

func TestACPSpecMessageIDSplitsTwoMessages(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "First ", "m1"))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "answer.", "m1"))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "Second.", "m2"))
	b.main().flushAssistantBuffer()
	assert.Equal(t, []string{"text:First answer.", "text:Second."}, assembledTexts(t, sink.Messages()))
}

func TestACPSpecMessageIDOneMessageIsOneRow(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "First ", "m1"))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "answer.", "m1"))
	b.main().flushAssistantBuffer()
	assert.Equal(t, []string{"text:First answer."}, assembledTexts(t, sink.Messages()))
}

func TestACPSpecMessageIDAbsentIsOneRow(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "First ", ""))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "answer.", ""))
	b.main().flushAssistantBuffer()
	assert.Equal(t, []string{"text:First answer."}, assembledTexts(t, sink.Messages()))
}

func TestACPHookMessageIDWinsOverSpecID(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.hooks.ChunkMessageID = func(agent.AssembledMessageKind, map[string]json.RawMessage) string { return "hook-message" }
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "First ", "m1"))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "answer.", "m2"))
	b.main().flushAssistantBuffer()
	assert.Equal(t, []string{"text:First answer."}, assembledTexts(t, sink.Messages()))
}

func TestACPHookMessageIDFallsBackToSpecID(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.hooks.ChunkMessageID = func(agent.AssembledMessageKind, map[string]json.RawMessage) string { return "" }
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "First.", "m1"))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "Second.", "m2"))
	b.main().flushAssistantBuffer()
	assert.Equal(t, []string{"text:First.", "text:Second."}, assembledTexts(t, sink.Messages()))
}

func TestACPChunkMessageIDStillIdentifiesTheBufferedText(t *testing.T) {
	var output acpTurnOutput
	output.switchMessage(agent.AssembledMessageKindText, "first")
	output.appendAssistant("old text", "")
	output.switchMessage(agent.AssembledMessageKindText, "second")
	assert.Equal(t, "first", output.assistantMessageID)
}

func TestACPSpecMessageIDsSurviveEveryTextSnapshot(t *testing.T) {
	for _, path := range []string{"turn", "unowned", "prompt boundary"} {
		t.Run(path, func(t *testing.T) {
			var output acpTurnOutput
			output.appendAssistant("answer", "message")
			output.appendThought("thought", "reasoning")
			output.turnMu.Lock()
			var snapshot acpTurnSnapshot
			switch path {
			case "turn":
				snapshot = output.drainTurnLocked()
			case "unowned":
				snapshot = output.takeUnownedOutputLocked()
			case "prompt boundary":
				snapshot = output.markPromptBoundaryLocked()
			}
			output.turnMu.Unlock()
			assert.Equal(t, "message", snapshot.assistantMessageID)
			assert.Equal(t, "reasoning", snapshot.thoughtMessageID)
			assert.Equal(t, "answer", snapshot.assistantText)
			assert.Equal(t, "thought", snapshot.thoughtText)
			next := output.drainTurn()
			assert.Empty(t, next.assistantMessageID)
			assert.Empty(t, next.thoughtMessageID)
			assert.Empty(t, next.assistantText)
			assert.Empty(t, next.thoughtText)
		})
	}
}

func TestACPSpecMessageIDStaysWithAnAppendAfterADrain(t *testing.T) {
	var output acpTurnOutput
	output.switchMessage(agent.AssembledMessageKindText, "message")
	output.drainTurn()
	output.appendAssistant("answer", "message")
	text, id := output.takeText(agent.AssembledMessageKindText)
	assert.Equal(t, "answer", text)
	assert.Equal(t, "message", id)
	text, id = output.takeText(agent.AssembledMessageKindText)
	assert.Empty(t, text)
	assert.Empty(t, id)
}

func TestACPSpecMessageIDSeparatesTheRecordedOpenCodeAnswers(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	// Trimmed content chunks from .tmp/probe/opencode/logs/steer-1.log.
	for _, text := range []string{"A1 ", "A2 ", "A3 ", "A4 ", "A5 ", "A6 ", "A7 ", "A8 "} {
		b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", text, "msg_149c67a12001t48rip1tEoX2iW"))
	}
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "ANSWER-B", "msg_149c68d0f001doo1F1XqoiM6aj"))
	b.main().flushAssistantBuffer()
	assert.Equal(t, []string{"text:A1 A2 A3 A4 A5 A6 A7 A8 ", "text:ANSWER-B"}, assembledTexts(t, sink.Messages()))
}

func TestSegmentTextReplacesAFlushedSegment(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	var ids []string
	b.hooks.SegmentText = func(kind agent.AssembledMessageKind, id, text string) string {
		assert.Equal(t, agent.AssembledMessageKindText, kind)
		ids = append(ids, id)
		if id == "m1" {
			return "X"
		}
		return text
	}
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "old text", "m1"))
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "new text", "m2"))
	b.main().flushAssistantBuffer()
	assert.Equal(t, []string{"m1", "m2"}, ids)
	assert.Equal(t, []string{"text:X", "text:new text"}, assembledTexts(t, sink.Messages()))
}

func TestSegmentTextDropsASegment(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.hooks.SegmentText = func(agent.AssembledMessageKind, string, string) string { return "" }
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "summary", "summary-id"))
	b.main().flushAssistantBuffer()
	assert.Empty(t, sink.Messages())
	assert.Contains(t, sink.ProgressUpdates(), agent.CompleteModelProgress("acp:agent_message_chunk"))
}

func TestSegmentTextSeesTheTurnEndSegment(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	var ids []string
	b.hooks.SegmentText = func(_ agent.AssembledMessageKind, id, text string) string {
		ids = append(ids, id)
		return "stored " + text
	}
	b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "answer", "message"))
	b.persistFinishedTurn(json.RawMessage(`{"stopReason":"end_turn"}`))
	assert.Equal(t, []string{"message"}, ids)
	assert.Equal(t, []string{"text:stored answer"}, assembledTexts(t, sink.Messages()))
}

func TestSegmentTextNotCalledForAChild(t *testing.T) {
	var stdin bytes.Buffer
	b, root := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	calls := 0
	b.hooks.SegmentText = func(agent.AssembledMessageKind, string, string) string { calls++; return "wrong owner" }
	child := &agenttest.Sink{}
	conversation := &conversation{b: b, childSink: agent.NewProviderServices(child), childAgentID: "child", out: &acpTurnOutput{}}
	raw := specChunkUpdate(t, "agent_message_chunk", "child answer", "child-message")
	var header acpUpdateHeader
	require.NoError(t, json.Unmarshal(raw, &header))
	assert.True(t, conversation.handleUpdate(header, raw))
	conversation.flushAssistantBuffer()
	assert.Zero(t, calls)
	assert.Empty(t, root.Messages())
	assert.Equal(t, []string{"text:child answer"}, assembledTexts(t, child.Messages()))
}

func TestSegmentTextSeesEveryMainSnapshotPath(t *testing.T) {
	for _, path := range []string{"unowned", "admitted", "prompt boundary", "work end", "process end"} {
		t.Run(path, func(t *testing.T) {
			var stdin bytes.Buffer
			b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
			var ids []string
			b.hooks.SegmentText = func(_ agent.AssembledMessageKind, id, text string) string {
				ids = append(ids, id)
				return "stored " + text
			}
			b.handleACPUpdate(specChunkUpdate(t, "agent_message_chunk", "answer", "message"))
			switch path {
			case "unowned":
				require.True(t, b.BeginAgentTurn())
			case "admitted":
				require.True(t, b.AdmitAgentTurn())
			case "prompt boundary":
				b.promptActive = true
				require.True(t, b.BeginAgentTurn())
			case "work end":
				b.storeWorkOutput(b.drainTurn())
			case "process end":
				b.finishAllTurnOutput(agent.MessageCompletionError)
			}
			assert.Equal(t, []string{"message"}, ids)
			assert.Equal(t, []string{"text:stored answer"}, assembledTexts(t, sink.Messages()))
		})
	}
}
