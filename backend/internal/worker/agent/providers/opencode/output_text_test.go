package opencode

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeRecoveredRetryDiscardsFailedText(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_failed","type":"assistant","content":[{"type":"text","text":"partial failed attempt"}],"time":{"completed":1},"error":{"type":"api"},"retry":{"attempt":1,"at":0}}`)))
	require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_good","type":"assistant","content":[{"type":"text","text":"full answer"}],"time":{"completed":2}}`)))
	require.NoError(t, streams.finish(agent.MessageCompletionComplete))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), `"text":"full answer"`)
}

func TestOpenCodeRecoveredAbortedTextKeepsItsInterruptionMarker(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"interrupted answer"}],"time":{"completed":1},"error":{"type":"aborted","message":"Step interrupted"}}`)))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), `"completion":"interrupted"`)
}

func TestOpenCodeTextFlushesBeforeTheNextContentStarts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"text.started", "reasoning.started", "tool.input.started", "inbox.delivered"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			sink.UpdateSessionID("ses_1")
			streams := newOpenCodeTextStreams("ses_1", sink)
			_, err := streams.observe(openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_1","ordinal":0,"text":"before the next content"`))
			require.NoError(t, err)
			_, err = streams.observe(openCodeSessionEventForTest(t, kind, `"assistantMessageID":"msg_1","ordinal":1,"id":"call_1","name":"read","inboxID":"input_1"`))
			require.NoError(t, err)
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.Contains(t, string(rows[0].Content), `"completion":"complete"`)
		})
	}
}

func storedAssistant(t *testing.T, raw string) openCodeStoredMessage {
	t.Helper()
	var message openCodeStoredMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &message))
	return message
}

func TestOpenCodeRecoveredReasoningAndTextKeepContentOrder(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"reasoning","text":"think"},{"type":"text","text":"answer"}],"time":{"created":0,"completed":0}}`)))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[0].Content), `"kind":"reasoning"`)
	assert.Contains(t, string(rows[1].Content), `"text":"answer"`)
}

func TestOpenCodeTextRecoveryRejectsMissingContentAndText(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"id":"msg_1","type":"assistant"}`, `{"id":"msg_1","type":"assistant","content":null}`, `{"id":"msg_1","type":"assistant","content":[{"type":"text"}]}`, `{"id":"msg_1","type":"assistant","content":[{"type":"reasoning","text":null}]}`} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			streams := newOpenCodeTextStreams("ses_1", sink)
			require.Error(t, streams.recover(storedAssistant(t, raw)))
			assert.Empty(t, sink.Messages())
		})
	}
}

func TestOpenCodeRecoveredTextDistinguishesMissingAndNullErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ field, completion string }{
		{"", "complete"}, {`,"error":null`, "complete"}, {`,"error": null `, "complete"}, {`,"error":{"name":"Error","message":"native failure"}`, "error"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			sink.UpdateSessionID("ses_1")
			streams := newOpenCodeTextStreams("ses_1", sink)
			require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"kept"}],"time":{"completed":0}`+tc.field+`}`)))
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.JSONEq(t, fmt.Sprintf(`{"type":"assembled_message","kind":"text","text":"kept","completion":%q}`, tc.completion), string(rows[0].Content))
		})
	}
}
