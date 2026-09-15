package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

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

	first := &testSink{}
	second := &testSink{}
	base := &acpBase{sink: first}
	base.bind(base)
	base.sink = second

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
	b, sink := newACPTurnBase(t, nopWriteCloser{&stdin})
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"one "}}`), nil)
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"session_info_update","info":{"title":"A title","modifiedAt":"now"}}`), nil)
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"two"}}`), nil)
	assert.Empty(t, sink.Messages(), "session metadata writes no row and closes no segment")
	b.flushAssistantBuffer()
	messages := sink.Messages()
	require.Len(t, messages, 1)
	var assembled struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(messages[0].Content, &assembled))
	assert.Equal(t, "one two", assembled.Text)
}
