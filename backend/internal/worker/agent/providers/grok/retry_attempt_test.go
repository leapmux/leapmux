package grok

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryFrame builds one retry_state session notification line.
func retryFrame(t *testing.T, attempt int, reason string) []byte {
	t.Helper()
	return notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "retry_state", "type": "retrying", "attempt": attempt, "max_retries": 15, "reason": reason,
	})
}

// grok/cut frames 27-34: Grok retries after a partial answer. The failed
// attempt's text is discarded, the native retry frame becomes one folded row,
// and the first content of the successful attempt resolves it.
func TestGrokRetryDiscardsAndFolds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	*a.HooksForTest() = a.configure(agent.Options{})

	a.HandleOutput(streamChunk(t, "PARTIAL ", 100))
	a.HandleOutput(streamChunk(t, "attempt ", 100))
	a.HandleOutput(streamChunk(t, "one text", 100))
	a.HandleOutput(retryFrame(t, 1, "reqwest error stream: stream ended before a finish reason"))
	a.HandleOutput(streamChunk(t, "FULL an", 200))
	a.HandleOutput(streamChunk(t, "swer #3", 200))
	a.FinishPromptRequestForTest(grokTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	texts := assembledTexts(t, sink)
	require.Len(t, texts, 1, "the failed attempt's text is discarded")
	assert.Equal(t, "FULL answer #3", texts[0])

	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1, "the retry frame becomes one native row")
	assert.Equal(t, agent.APIRetryNotification(), grokProvider{}.Classify(rows[0].Content))
	require.NotEmpty(t, sink.Notifications(), "the first content resolves the retry")
	assert.Equal(t, contracts.NotificationTypeApiRetryResolved, sink.Notifications()[0][contracts.NotificationFieldType])
}

// Three retry rounds fold into the one row that the first content resolves.
func TestGrokRetryRoundsFoldIntoOneRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	*a.HooksForTest() = a.configure(agent.Options{})
	for attempt := 1; attempt <= 3; attempt++ {
		a.HandleOutput(streamChunk(t, "PARTIAL ", int64(100*attempt)))
		a.HandleOutput(retryFrame(t, attempt, "API error (status 503 Service Unavailable): stub overloaded"))
	}
	a.HandleOutput(streamChunk(t, "FULL answer #3", 400))
	a.FinishPromptRequestForTest(grokTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	texts := assembledTexts(t, sink)
	require.Len(t, texts, 1)
	assert.Equal(t, "FULL answer #3", texts[0])
	// Each round stores its native frame; the browser folds the rounds into
	// one entry, and each round resolves at the next attempt's first content.
	assert.Len(t, sink.PersistedNotifications(), 3)
	assert.Len(t, sink.Notifications(), 3)
}

// A retry state that is not a retry in progress stores nothing.
func TestGrokRetryIgnoresOtherStates(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "retry_state", "type": "exhausted",
	}))
	assert.Empty(t, sink.Notifications())
}

// streamChunk builds one agent_message_chunk whose metadata names the model
// request that produced it, as Grok's live wire does.
func streamChunk(t *testing.T, text string, streamStart int64) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": grokTestSession,
			"_meta":     map[string]any{"streamStartMs": streamStart},
			"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}},
		},
	})
}

// Grok can flush the partial text of the failed attempt only after it sent its
// retry_state frame. The first chunk of the retried request names a new stream,
// and the text that the failed attempt left buffered is dropped there.
func TestGrokRetryFlushAfterTheFrameIsDropped(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	*a.HooksForTest() = a.configure(agent.Options{})

	a.HandleOutput(retryFrame(t, 1, "reqwest error stream: stream ended before a finish reason"))
	a.HandleOutput(streamChunk(t, "PARTIAL ", 1792238623174))
	a.HandleOutput(streamChunk(t, "FULL answer", 1792238625328))
	a.FinishPromptRequestForTest(grokTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	texts := assembledTexts(t, sink)
	require.Len(t, texts, 1, "the failed attempt's flushed text is dropped at the new stream")
	assert.Equal(t, "FULL answer", texts[0])
	require.NotEmpty(t, sink.Notifications(), "the retried attempt's first content resolves the retry")
	assert.Equal(t, contracts.NotificationTypeApiRetryResolved, sink.Notifications()[0][contracts.NotificationFieldType])
}

// A chunk that carries no stream metadata joins no boundary, so the discard of
// the retry frame itself keeps serving that wire.
func TestGrokStreamBoundaryNeedsMetadata(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	*a.HooksForTest() = a.configure(agent.Options{})

	a.HandleOutput(streamChunk(t, "one ", 1792238623174))
	a.HandleOutput(chunk(t, "two"))
	a.FinishPromptRequestForTest(grokTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	// The chunk without metadata claims no new stream, so the turn keeps both.
	texts := assembledTexts(t, sink)
	require.Len(t, texts, 1)
	assert.Equal(t, "one two", texts[0])
}

// chunk builds one agent_message_chunk session update of the main session.
func chunk(t *testing.T, text string) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": grokTestSession,
			"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}},
		},
	})
}

// assembledTexts returns the assembled assistant text rows of one sink.
func assembledTexts(t *testing.T, sink *agenttest.ControlSink) []string {
	t.Helper()
	var texts []string
	for _, message := range sink.Messages() {
		if kind, text, _, ok := agenttest.DecodeAssembledMessage(message.Content); ok && kind == agent.AssembledMessageKindText {
			texts = append(texts, text)
		}
	}
	return texts
}

func TestGrokRetryAttempt(t *testing.T) {
	t.Parallel()
	// grok/cut frames 27-34: Grok retries after a partial answer, and the
	// failed attempt's text is discarded while the retry row resolves at the
	// successful attempt's first content.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name:       "a retry discards the failed attempt and resolves at the next content",
		FramesPath: "testdata/retry_attempt.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			a, _, _ := newGrokAgent(t, agent.Options{}, nil)
			a.SetSinkForTest(agent.NewProviderServices(sink))
			a.SetSessionIDForTest(grokTestSession)
			a.SetPromptActiveForTest(true)
			*a.HooksForTest() = a.configure(agent.Options{})
			return func(frame []byte) {
				var envelope struct {
					Result *json.RawMessage `json:"result"`
					ID     *int64           `json:"id"`
				}
				if json.Unmarshal(frame, &envelope) == nil && envelope.Result != nil && envelope.ID != nil {
					a.FinishPromptRequestForTest(grokTestSession, *envelope.Result, nil)
					return
				}
				a.HandleOutput(frame)
			}
		},
		Want:      []agenttest.RetryAttemptText{{Text: "FULL answer #3", Completion: agent.MessageCompletionComplete}},
		Discarded: []string{"PARTIAL "},
	})
}
