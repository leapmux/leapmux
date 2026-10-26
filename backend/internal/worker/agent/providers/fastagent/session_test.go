package fastagent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

func fastagentRootCommandMessages(t *testing.T, text string, isError bool) []fastagentHistoryMessage {
	return []fastagentHistoryMessage{
		{Role: "assistant", Timestamp: "2026-10-26T00:00:00Z", ToolCalls: json.RawMessage(`{"model-call":{"method":"tools/call","params":{"name":"bash","arguments":{"command":"printf computed"}}}}`)},
		{Role: "user", ToolResults: json.RawMessage(`{"model-call":{"content":[{"type":"text","text":` + string(mustJSON(t, text)) + `}],"isError":` + map[bool]string{true: "true", false: "false"}[isError] + `}}`)},
	}
}

func TestFastagentRootToolsPreservesRequestsResultsAndProvenance(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "computed output", strings.Repeat("large output\n", 10000)} {
		for _, failed := range []bool{false, true} {
			tools, err := fastagentRootTools("root", []fastagentRootHistory{{file: "history_agent.json", messages: fastagentRootCommandMessages(t, text, failed)}})
			require.NoError(t, err)
			require.Len(t, tools, 1)
			updates, err := tools[0].updates()
			require.NoError(t, err)
			require.Len(t, updates, 2)
			var request, result map[string]any
			require.NoError(t, json.Unmarshal(updates[0], &request))
			require.NoError(t, json.Unmarshal(updates[1], &result))
			assert.Equal(t, map[string]any{"command": "printf computed"}, request["rawInput"])
			assert.NotEqual(t, "model-call", result["toolCallId"])
			assert.Equal(t, request["toolCallId"], result["toolCallId"])
			assert.Equal(t, map[bool]string{true: "failed", false: "completed"}[failed], result["status"])
			assert.Equal(t, text, result["rawOutput"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
			assert.Equal(t, map[string]any{contracts.FastagentArchiveNamespace: map[string]any{
				contracts.FastagentArchiveSessionID: "root", contracts.FastagentArchiveHistoryFile: "history_agent.json",
				contracts.FastagentArchiveTimestamp: "2026-10-26T00:00:00Z", contracts.FastagentArchiveCallID: "model-call", contracts.FastagentArchiveToolName: "bash",
			}}, result["_meta"])
		}
	}
}

func TestFastagentRootToolsMatchesNativeParallelIDs(t *testing.T) {
	t.Parallel()
	messages := []fastagentHistoryMessage{
		{Role: "assistant", Timestamp: "2026-10-26T00:00:00Z", ToolCalls: json.RawMessage(`{"b":{"method":"tools/call","params":{"name":"bash","arguments":{"command":"one"}}},"p":{"method":"tools/call","params":{"name":"process","arguments":{"process_id":"process-1"}}},"r":{"method":"tools/call","params":{"name":"read_text_file","arguments":{"path":"note"}}}}`)},
		{Role: "user", ToolResults: json.RawMessage(`{"p":{"content":[],"isError":false},"r":{"content":[],"isError":false},"b":{"content":[],"isError":true}}`)},
	}
	tools, err := fastagentRootTools("root", []fastagentRootHistory{{file: "history_agent.json", messages: messages}})
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "b", tools[0].nativeID)
	assert.Contains(t, string(tools[0].result), `"isError":true`)
	assert.Equal(t, "p", tools[1].nativeID)
	assert.Contains(t, string(tools[1].result), `"isError":false`)
	assert.NotEqual(t, tools[0].id, fastagentRootToolID("other", "history_agent.json", tools[0].timestamp, "b"))
	assert.NotEqual(t, tools[0].id, fastagentRootToolID("root", "history_other.json", tools[0].timestamp, "b"))
}

func TestFastagentRootToolsRejectsMalformedAndConflictingRecords(t *testing.T) {
	t.Parallel()
	for _, change := range []func([]fastagentHistoryMessage) []fastagentHistoryMessage{
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage { m[0].Timestamp = ""; return m },
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage { m[0].Role = "user"; return m },
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage {
			m[0].ToolCalls = json.RawMessage(`{"x":null}`)
			return m
		},
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage {
			m[0].ToolCalls = json.RawMessage(`{"x":{"method":"tools/call","params":{"name":"bash","arguments":null}}}`)
			return m
		},
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage {
			m[1].ToolResults = json.RawMessage(`{"model-call":null}`)
			return m
		},
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage {
			m[1].ToolResults = json.RawMessage(`{"model-call":{"content":[],"isError":"no"}}`)
			return m
		},
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage { return append(m, m[0]) },
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage {
			return []fastagentHistoryMessage{m[0], m[0]}
		},
	} {
		tools, err := fastagentRootTools("root", []fastagentRootHistory{{file: "history_agent.json", messages: change(fastagentRootCommandMessages(t, "output", false))}})
		require.Error(t, err)
		assert.Nil(t, tools)
	}
	messages := fastagentRootCommandMessages(t, "", false)[:1]
	tools, err := fastagentRootTools("root", []fastagentRootHistory{{file: "history_agent.json", messages: messages}})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	updates, err := tools[0].updates()
	require.NoError(t, err)
	assert.Len(t, updates, 1, "an interrupted request has no fabricated result")
}

func TestFastagentRootToolsOrdersHistoriesByTheirNativeTimestamp(t *testing.T) {
	t.Parallel()
	later := fastagentRootCommandMessages(t, "later", false)
	later[0].Timestamp = "2026-10-26T00:02:00Z"
	earlier := fastagentRootCommandMessages(t, "earlier", false)
	earlier[0].Timestamp = "2026-10-26T09:01:00+09:00"
	tools, err := fastagentRootTools("root", []fastagentRootHistory{{file: "history_agent.json", messages: later}, {file: "history_other.json", messages: earlier}})
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Contains(t, string(tools[0].result), "earlier")
	assert.Contains(t, string(tools[1].result), "later")
}

func TestFastagentRootToolsRejectsConflictingResultsAndForeignRequests(t *testing.T) {
	t.Parallel()
	for _, change := range []func([]fastagentHistoryMessage) []fastagentHistoryMessage{
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage { return append(m, m[1]) },
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage {
			foreign := m[0]
			foreign.Timestamp = "2026-10-26T00:00:01Z"
			foreign.ToolCalls = json.RawMessage(`{"model-call":{"method":"tools/call","params":{"name":"read_text_file","arguments":{"path":"other.txt"}}}}`)
			return []fastagentHistoryMessage{m[0], foreign, m[1]}
		},
		func(m []fastagentHistoryMessage) []fastagentHistoryMessage {
			m[1].ToolResults = json.RawMessage(`{"model-call":{"content":[null],"isError":false}}`)
			return m
		},
	} {
		tools, err := fastagentRootTools("root", []fastagentRootHistory{{file: "history_agent.json", messages: change(fastagentRootCommandMessages(t, "output", false))}})
		require.Error(t, err)
		assert.Nil(t, tools)
	}
}
