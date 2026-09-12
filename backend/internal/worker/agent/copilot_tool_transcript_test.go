package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopilotToolTranscriptPreservesOriginalAndSupplementIdentity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	native := "{\"type\":\"tool.execution_start\",\"data\":{\"toolCallId\":\"call\",\"toolName\":\"task\",\"arguments\":{\"counter\":9007199254740993}}}\n{\"type\":\"tool.execution_complete\",\"data\":{\"toolCallId\":\"call\",\"success\":true,\"result\":{\"content\":\"Done\"}}}\n"
	require.NoError(t, os.WriteFile(path, []byte(native), 0o600))
	sink := &testSink{}
	transcript := newCopilotToolTranscript(t.Context(), sink, func() string { return path })
	request := []byte(`{ "sessionUpdate": "tool_call", "toolCallId": "call", "status": "pending" }`)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: request}, SpanInfo{SpanID: "call", NoSpan: true}))
	result := []byte(`{ "sessionUpdate": "tool_call_update", "toolCallId": "call" }`)
	retained := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","protocol":{"status":"in_progress","rawInput":{"prompt":"Original prompt"}}}`)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: result, Supplemental: retained, Completion: MessageCompletionInterrupted}, SpanInfo{SpanID: "call", Closing: true}))
	require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"stopReason":"cancelled"}`)}, SpanInfo{}))
	messages := sink.Messages()
	require.GreaterOrEqual(t, len(messages), 2)
	assert.Equal(t, request, messages[0].Content)
	assert.Equal(t, result, messages[1].Content)
	assert.Equal(t, MessageCompletionInterrupted, messages[1].Completion)
	for _, message := range messages[:2] {
		assert.Contains(t, string(message.SupplementalContent), "9007199254740993")
	}
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(messages[1].SupplementalContent, &supplement))
	assert.NotContains(t, supplement, "status", "the original event has no status field")
	assert.Contains(t, string(supplement["protocol"]), "Original prompt")
}
