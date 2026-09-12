package agent

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolTranscriptUsesClosingSpansAcrossProtocols(t *testing.T) {
	t.Parallel()
	for _, original := range []string{
		`{"type":"tool.updated","payload":{"kind":"result","toolCallId":"call","result":{"success":true,"content":"image"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"in_progress"}`,
	} {
		t.Run(original, func(t *testing.T) {
			t.Parallel()
			sink := &testSink{}
			transcript := &toolTranscript{
				ProviderServices: sink,
				toolCallID:       func([]byte) string { return "call" },
				ctx:              t.Context(),
				locate: func(_ string) toolTranscriptLocation {
					return toolTranscriptLocation{sessionKey: "session", path: "store"}
				},
				readSupplements: func(_ context.Context, _ string, pending map[string]MessageContent, _ bool) (map[string][]byte, error) {
					assert.Equal(t, original, string(pending["call"].Original))
					return map[string][]byte{"call": []byte(`{"recovered":true}`)}, nil
				},
			}
			content := MessageContent{Original: []byte(original), Completion: MessageCompletionInterrupted}
			require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, SpanInfo{SpanID: "call", Closing: true}))
			require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"done":true}`)}, SpanInfo{}))
			result := sink.Messages()[0]
			assert.Equal(t, original, string(result.Content))
			assert.Equal(t, MessageCompletionInterrupted, result.Completion)
			assert.JSONEq(t, `{"recovered":true}`, string(result.SupplementalContent))
		})
	}
}
