package reasonix

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newReasonixNoticeAgent wires an agent whose metadata hook reads notice
// chunks of the main session.
func newReasonixNoticeAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session-1")
	a.SetPromptActiveForTest(true)
	a.HooksForTest().SessionMetadataHandler = a.observeReasonixNotice
	return a, sink
}

// reasonixFrame builds one agent_message_chunk session/update line of the
// main session.
func reasonixFrame(t *testing.T, text string) []byte {
	t.Helper()
	return json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + jsonString(t, text) + `}}}}`)
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

// reasonixTexts returns the assembled assistant text rows.
func reasonixTexts(sink *agenttest.Sink) []string {
	var texts []string
	for _, message := range sink.Messages() {
		if kind, text, _, ok := agenttest.DecodeAssembledMessage(message.Content); ok && kind == agent.AssembledMessageKindText {
			texts = append(texts, text)
		}
	}
	return texts
}

// A text chunk, a warning and a text chunk give three rows: the notice never
// joins the model's own stream.
func TestReasonixNoticeSplitsTheStream(t *testing.T) {
	t.Parallel()
	a, sink := newReasonixNoticeAgent(t)

	a.HandleOutput(reasonixFrame(t, "The answer."))
	a.HandleOutput(reasonixFrame(t, "\n\n[warning] Guidance was not applied because the turn ended before it could be processed. Send it again if it is still needed:\nSTEER-ONE while turn A runs"))
	a.HandleOutput(reasonixFrame(t, "The rest."))
	a.FinishPromptRequestForTest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	texts := reasonixTexts(sink)
	require.Len(t, texts, 2, "the model's own text keeps its rows")
	assert.Equal(t, "The answer.", texts[0])
	assert.Equal(t, "The rest.", texts[1])
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1, "the warning persists its native frame")
	assert.Contains(t, string(rows[0].Content), "[warning]")
}

// A compaction notice persists its native frame and classifies as a boundary.
func TestReasonixCompactionNoticeIsABoundary(t *testing.T) {
	t.Parallel()
	a, sink := newReasonixNoticeAgent(t)

	a.HandleOutput(reasonixFrame(t, "\n\n[compacted 42 earlier messages to save context]"))

	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), "[compacted 42")
	assert.Equal(t, agent.CompactionBoundaryNotification(), reasonixProvider{}.Classify(rows[0].Content))
}

// A chunk that mentions the marker inside its text does not split.
func TestReasonixNoticeRequiresThePrefix(t *testing.T) {
	t.Parallel()
	a, sink := newReasonixNoticeAgent(t)
	a.HandleOutput(reasonixFrame(t, "A model answer that mentions [warning] inside its text"))
	a.FinishPromptRequestForTest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Len(t, sink.PersistedNotifications(), 0)
	texts := reasonixTexts(sink)
	require.Len(t, texts, 1)
	assert.True(t, strings.HasPrefix(texts[0], "A model answer"))
}

// A compaction notice persists its native frame and classifies as a boundary.
