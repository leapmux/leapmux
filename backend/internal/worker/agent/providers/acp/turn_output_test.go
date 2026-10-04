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
	output.appendAssistant("answer")
	assert.True(t, output.appendThought("first "))
	assert.False(t, output.appendThought("second"))
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

func TestACPTurnOutputSerializesSessionReplacementWithAppend(t *testing.T) {
	t.Parallel()

	var output acpTurnOutput
	output.appendAssistant("old")
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
		output.appendAssistant("new")
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
	output.appendThought("**Verifying terminal release synchronization")
	output.appendThought("Analyzing lock acquisition order and concurrency**")

	assert.Equal(t,
		"**Verifying terminal release synchronizationAnalyzing lock acquisition order and concurrency**",
		output.drainTurn().thoughtText,
	)

	var tokenized acpTurnOutput
	tokenized.appendThought("**Analyzing")
	tokenized.appendThought(" synchronization**")
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
	b.hooks.ChunkMessageID = func(metadata map[string]json.RawMessage) string {
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

// Without the hook, the protocol states no message boundary, and a run of
// chunks stays one message.
func TestACPWithoutChunkMessageIDARunOfChunksIsOneMessage(t *testing.T) {
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
	assert.False(t, output.switchMessage(agent.AssembledMessageKindText, "m1"))
	output.appendAssistant("text")
	_ = output.drainTurn()
	output.appendAssistant("carried")
	assert.False(t, output.switchMessage(agent.AssembledMessageKindText, "m2"),
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
			b.handleACPUpdate(chunkUpdate("agent_message_chunk", "#", []string{"m1", "m2", "m3"}[i%3]))
		}
	}()
	go func() {
		defer wg.Done()
		for range chunks {
			turn := b.drainTurn()
			mu.Lock()
			drained.WriteString(turn.assistantText)
			mu.Unlock()
		}
	}()
	wg.Wait()
	b.main().flushAssistantBuffer()

	// "#" appears in no prefix that assembledTexts adds.
	stored := strings.Join(assembledTexts(t, sink.Messages()), "")
	total := strings.Count(stored, "#") + strings.Count(drained.String(), "#")
	assert.Equal(t, chunks, total, "each chunk is stored once or drained once")
}
