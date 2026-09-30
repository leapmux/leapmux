package droid

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

func TestThinkingDeltaCreatesAReasoningRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, frame := range []string{
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_delta","messageId":"message-1","blockIndex":1,"textDelta":"Compare "}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_delta","messageId":"message-1","blockIndex":1,"textDelta":"the values."}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_complete","messageId":"message-1","blockIndex":1,"durationMs":8}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	rows := sink.Messages()
	require.Len(t, rows, 1, "the complete native thinking block creates one row")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rows[0].Content, &payload))
	assert.Equal(t, contracts.AssembledMessageKindReasoning, payload[contracts.AssembledMessageFieldKind])
	assert.Equal(t, "Compare the values.", payload[contracts.AssembledMessageFieldText])
	assert.Empty(t, sink.PersistedNotifications(), "thinking frames do not become raw notifications")
}

func TestEmptyThinkingBlockCreatesNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_complete","messageId":"message-1","blockIndex":1}}}`))
	assert.Empty(t, sink.Messages(), "an empty thinking block has no content")
	assert.Empty(t, sink.PersistedNotifications(), "the completion is protocol state")
}
