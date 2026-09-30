package fastagent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFastagentHistoryUpdatesPreservesRolesToolsAndOrder(t *testing.T) {
	t.Parallel()
	messages := []fastagentHistoryMessage{
		{Role: "user", Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"Count files"}`)}},
		{Role: "assistant", Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"first answer"}`)},
			ToolCalls: json.RawMessage(`{"call-z":{"method":"tools/call","params":{"name":"read_text_file","arguments":{"path":"note.txt"}}},"call-a":{"method":"tools/call","params":{"name":"execute","arguments":{"command":"pwd"}}}}`)},
		{Role: "user", ToolResults: json.RawMessage(`{"call-z":{"content":[{"type":"text","text":"note content"}],"isError":false},"call-a":{"content":[{"type":"text","text":"no"}],"isError":true}}`)},
		{Role: "assistant", Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"final answer"}`)}},
	}
	var updates []map[string]any
	for index, message := range messages {
		frames, err := fastagentHistoryUpdates(message, index == 0, "Count files")
		require.NoError(t, err)
		for _, frame := range frames {
			var update map[string]any
			require.NoError(t, json.Unmarshal(frame, &update))
			updates = append(updates, update)
		}
	}
	require.Len(t, updates, 6)
	assert.Equal(t, []any{"agent_message_chunk", "tool_call", "tool_call", "tool_call_update", "tool_call_update", "agent_message_chunk"}, []any{
		updates[0]["sessionUpdate"], updates[1]["sessionUpdate"], updates[2]["sessionUpdate"],
		updates[3]["sessionUpdate"], updates[4]["sessionUpdate"], updates[5]["sessionUpdate"],
	})
	assert.Equal(t, "call-z", updates[1]["toolCallId"])
	assert.Equal(t, "call-a", updates[2]["toolCallId"])
	assert.Equal(t, "call-z", updates[3]["toolCallId"])
	assert.Equal(t, "call-a", updates[4]["toolCallId"])
	assert.Equal(t, "failed", updates[4]["status"])
	resultContent, ok := updates[3]["content"].([]any)
	require.True(t, ok)
	require.Len(t, resultContent, 1)
	assert.Contains(t, resultContent[0], "content")
	assert.Contains(t, string(mustJSON(t, resultContent[0])), "note content")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func TestFastagentHistoryUpdatesKeepsReasoningAndErrorChannels(t *testing.T) {
	t.Parallel()
	frames, err := fastagentHistoryUpdates(fastagentHistoryMessage{
		Role: "assistant",
		Channels: map[string][]json.RawMessage{
			"reasoning":        {json.RawMessage(`{"type":"text","text":"considered files"}`)},
			"fast-agent-error": {json.RawMessage(`{"type":"text","text":"read failed"}`)},
		},
	}, false, "")
	require.NoError(t, err)
	require.Len(t, frames, 2)
	assert.Contains(t, string(frames[0]), `"agent_thought_chunk"`)
	assert.Contains(t, string(frames[1]), `"agent_message_chunk"`)
}

func TestFastagentHistoryUpdatesKeepsLaterUserMessage(t *testing.T) {
	t.Parallel()
	frames, err := fastagentHistoryUpdates(fastagentHistoryMessage{
		Role: "user", Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"another task"}`)},
	}, false, "first task")
	require.NoError(t, err)
	require.Len(t, frames, 1)
	assert.Contains(t, string(frames[0]), `"user_message_chunk"`)
	assert.Contains(t, string(frames[0]), "another task")
}

func TestFastagentObjectEntriesRejectsDuplicateIds(t *testing.T) {
	t.Parallel()
	_, err := fastagentObjectEntries(json.RawMessage(`{"call-1":{},"call-1":{}}`))
	require.ErrorContains(t, err, "repeats")
}
