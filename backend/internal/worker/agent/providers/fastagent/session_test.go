package fastagent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rootNativeMessages(text string) []json.RawMessage {
	encoded, _ := json.Marshal(text)
	return []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"current input"}]}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"Before"}],"tool_calls":{"model-call":{"method":"tools/call","params":{"name":"bash","arguments":{"command":"printf computed"}}}},"stop_reason":"toolUse"}`),
		json.RawMessage(`{"role":"user","tool_results":{"model-call":{"content":[{"type":"text","text":` + string(encoded) + `}],"isError":false}}}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"After"}],"stop_reason":"endTurn"}`),
	}
}

func rootInputProof(t *testing.T) [32]byte {
	t.Helper()
	digest, err := fastagentInputDigest([]json.RawMessage{json.RawMessage(`{"type":"text","text":"current input"}`)}, true)
	require.NoError(t, err)
	return digest
}

func rootNativeArchive(messages []json.RawMessage) *fastagentRootArchive {
	return &fastagentRootArchive{sessionID: "root", activeActor: "agent", histories: []fastagentRootHistory{{actor: "agent", file: "history_agent.json", messages: messages}}}
}

func TestFastagentRootProjectionPreservesNativeElementsAndProvenance(t *testing.T) {
	for _, text := range []string{"", "complete output", strings.Repeat("large output\n", 10000)} {
		before, err := freshFastagentRootReceipt("root", "agent")
		require.NoError(t, err)
		messages := rootNativeMessages(text)
		projection, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "capture-1", rootInputProof(t), agent.MessageCompletionComplete)
		require.NoError(t, err)
		require.Len(t, projection.rows, 4)
		assert.JSONEq(t, `{"type":"text","text":"Before"}`, string(projection.rows[0].content.Original))
		assert.JSONEq(t, `{"method":"tools/call","params":{"name":"bash","arguments":{"command":"printf computed"}}}`, string(projection.rows[1].content.Original))
		var result struct{ Content []struct{ Text string } }
		require.NoError(t, json.Unmarshal(projection.rows[2].content.Original, &result))
		require.Len(t, result.Content, 1)
		assert.Equal(t, text, result.Content[0].Text)
		assert.Equal(t, projection.rows[1].span.SpanID, projection.rows[2].span.SpanID)
		assert.NotEqual(t, "model-call", projection.rows[1].span.SpanID)
		assert.True(t, projection.rows[2].span.Closing)
		var supplement map[string]map[string]any
		require.NoError(t, json.Unmarshal(projection.rows[2].content.Supplemental, &supplement))
		source := supplement[contracts.FastagentArchiveNamespace]
		assert.Equal(t, "capture-1", source[contracts.FastagentArchiveCaptureID])
		assert.Equal(t, float64(2), source[contracts.FastagentArchiveMessageIndex])
		assert.NotContains(t, source, contracts.FastagentArchiveTimestamp, "timestamps are optional and never order the transcript")
		second, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "capture-2", rootInputProof(t), agent.MessageCompletionComplete)
		require.NoError(t, err)
		assert.NotEqual(t, projection.rows[1].span.SpanID, second.rows[1].span.SpanID)
	}
}

func TestFastagentRootProjectionKeepsNativeParallelMapOrder(t *testing.T) {
	messages := rootNativeMessages("")
	messages[1] = json.RawMessage(`{"role":"assistant","tool_calls":{"b":{"method":"tools/call","params":{"name":"bash","arguments":{"command":"one"}}},"p":{"method":"tools/call","params":{"name":"process","arguments":{"process_id":"p"}}},"r":{"method":"tools/call","params":{"name":"read_text_file","arguments":{"path":"note"}}}}}`)
	messages[2] = json.RawMessage(`{"role":"user","tool_results":{"p":{"content":[]},"r":{"content":[{"type":"image","data":"native","mimeType":"image/png"}]},"b":{"content":[],"isError":true}}}`)
	before, err := freshFastagentRootReceipt("root", "agent")
	require.NoError(t, err)
	projection, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "parallel", rootInputProof(t), agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.Len(t, projection.rows, 7)
	var calls []string
	for _, row := range projection.rows[:6] {
		var supplement map[string]map[string]any
		require.NoError(t, json.Unmarshal(row.content.Supplemental, &supplement))
		calls = append(calls, supplement[contracts.FastagentArchiveNamespace][contracts.FastagentArchiveCallID].(string))
	}
	assert.Equal(t, []string{"b", "p", "r", "p", "r", "b"}, calls)
	assert.Contains(t, string(projection.rows[4].content.Original), `"data":"native"`)
}

func TestFastagentRootProjectionKeepsSubagentCallsWithoutASpan(t *testing.T) {
	messages := rootNativeMessages("child result")
	messages[1] = json.RawMessage(`{"role":"assistant","tool_calls":{"model-call":{"method":"tools/call","params":{"name":"subagent","arguments":{"message":"the child task"}}}}}`)
	before, err := freshFastagentRootReceipt("root", "agent")
	require.NoError(t, err)
	projection, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "spawn", rootInputProof(t), agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.Len(t, projection.rows, 3)
	assert.True(t, projection.rows[0].span.NoSpan)
	assert.True(t, projection.rows[1].span.NoSpan)
}

func TestFastagentRootProjectionRejectsTheWholeIncompleteOrAmbiguousSuffix(t *testing.T) {
	cases := map[string]func([]json.RawMessage) []json.RawMessage{
		"unfinished request": func(m []json.RawMessage) []json.RawMessage { return m[:2] },
		"missing final stop": func(m []json.RawMessage) []json.RawMessage {
			m[3] = json.RawMessage(`{"role":"assistant","content":[]}`)
			return m
		},
		"duplicate input":        func(m []json.RawMessage) []json.RawMessage { return append(m, m[0]) },
		"duplicate result":       func(m []json.RawMessage) []json.RawMessage { return slices.Insert(m, 3, m[2]) },
		"duplicate pending call": func(m []json.RawMessage) []json.RawMessage { return slices.Insert(m, 2, m[1]) },
		"foreign result": func(m []json.RawMessage) []json.RawMessage {
			m[2] = json.RawMessage(`{"role":"user","tool_results":{"foreign":{"content":[]}}}`)
			return m
		},
		"null result": func(m []json.RawMessage) []json.RawMessage {
			m[2] = json.RawMessage(`{"role":"user","tool_results":{"model-call":null}}`)
			return m
		},
		"missing result content": func(m []json.RawMessage) []json.RawMessage {
			m[2] = json.RawMessage(`{"role":"user","tool_results":{"model-call":{"isError":false}}}`)
			return m
		},
		"null block": func(m []json.RawMessage) []json.RawMessage {
			m[2] = json.RawMessage(`{"role":"user","tool_results":{"model-call":{"content":[null]}}}`)
			return m
		},
		"invalid error flag": func(m []json.RawMessage) []json.RawMessage {
			m[2] = json.RawMessage(`{"role":"user","tool_results":{"model-call":{"content":[],"isError":"no"}}}`)
			return m
		},
		"unknown channel": func(m []json.RawMessage) []json.RawMessage {
			m[3] = json.RawMessage(`{"role":"assistant","channels":{"future":[{"type":"text","text":"unread"}]},"stop_reason":"endTurn"}`)
			return m
		},
		"unrepresented body": func(m []json.RawMessage) []json.RawMessage {
			m[3] = json.RawMessage(`{"role":"assistant","content":[{"type":"image","data":"unrepresented"}],"stop_reason":"endTurn"}`)
			return m
		},
		"wrong request role": func(m []json.RawMessage) []json.RawMessage {
			m[1] = json.RawMessage(strings.Replace(string(m[1]), `"assistant"`, `"user"`, 1))
			return m
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			before, err := freshFastagentRootReceipt("root", "agent")
			require.NoError(t, err)
			projection, err := projectFastagentRootArchive(before, rootNativeArchive(change(rootNativeMessages("output"))), "capture", rootInputProof(t), agent.MessageCompletionComplete)
			require.Error(t, err)
			assert.Nil(t, projection, "no valid prefix can escape a later validation failure")
		})
	}
}

func TestFastagentRootProjectionRequiresAnUnchangedOwnedPrefix(t *testing.T) {
	old := rootNativeMessages("old output")
	before, err := captureFastagentRootReceipt(rootNativeArchive(old))
	require.NoError(t, err)
	after := rootNativeArchive(append(slices.Clone(old), rootNativeMessages("new output")...))
	projection, err := projectFastagentRootArchive(before, after, "new", rootInputProof(t), agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.Len(t, projection.rows, 4)
	assert.Contains(t, string(projection.rows[2].content.Original), "new output")
	after.histories[0].messages[2] = json.RawMessage(strings.Replace(string(old[2]), "old output", "changed output", 1))
	projection, err = projectFastagentRootArchive(before, after, "invalid", rootInputProof(t), agent.MessageCompletionComplete)
	require.Error(t, err)
	assert.Nil(t, projection)
	after = rootNativeArchive(append(slices.Clone(old), rootNativeMessages("new output")...))
	after.histories = append(after.histories, fastagentRootHistory{actor: "other", file: "history_other.json", messages: rootNativeMessages("other output")})
	projection, err = projectFastagentRootArchive(before, after, "ambiguous", rootInputProof(t), agent.MessageCompletionComplete)
	require.Error(t, err)
	assert.Nil(t, projection, "timestamps cannot prove order across histories")
	after = rootNativeArchive(old)
	after.activeActor = "other"
	_, err = projectFastagentRootArchive(before, after, "foreign", rootInputProof(t), agent.MessageCompletionComplete)
	require.Error(t, err)
}

func TestFastagentRootProjectionRetainsReasoningAndMetadataWithoutInventingACP(t *testing.T) {
	messages := rootNativeMessages("output")
	messages[3] = json.RawMessage(`{"role":"assistant","channels":{"reasoning":[{"type":"text","text":"reason"}],"fast-agent-timing":[{"type":"text","text":"time"}],"fast-agent-error":[{"type":"text","text":"error"}]},"content":[{"type":"text","text":""}],"stop_reason":"endTurn"}`)
	before, err := freshFastagentRootReceipt("root", "agent")
	require.NoError(t, err)
	projection, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "channels", rootInputProof(t), agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.Len(t, projection.rows, 7)
	for i, text := range []string{"reason", "", "time", "error"} {
		assert.JSONEq(t, `{"type":"text","text":`+string(mustJSON(t, text))+`}`, string(projection.rows[i+3].content.Original))
	}
}

func TestFastagentOrderedCollectionsRejectInvalidAndDuplicateMembers(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"a":1,"a":2}`, `{"":1}`, `{"a":1} {}`} {
		_, err := fastagentArchiveEntries(json.RawMessage(raw))
		if raw == "null" {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
	for _, raw := range []string{`null`, `{}`, `[1] 2`, `[`} {
		_, err := fastagentArchiveArray(json.RawMessage(raw))
		require.Error(t, err)
	}
	left, err := fastagentMessageDigest(json.RawMessage(`{"x":0,"y":1}`))
	require.NoError(t, err)
	right, err := fastagentMessageDigest(json.RawMessage("{\n\"x\":0, \"y\":1}"))
	require.NoError(t, err)
	assert.Equal(t, left, right)
	reordered, err := fastagentMessageDigest(json.RawMessage(`{"y":1,"x":0}`))
	require.NoError(t, err)
	assert.NotEqual(t, left, reordered)
}

func TestFastagentRootProjectionPreservesNativeShellProcessMetadata(t *testing.T) {
	messages := rootNativeMessages("native shell output")
	messages[2] = json.RawMessage(`{"role":"user","tool_results":{"model-call":{"content":[{"type":"text","text":"native shell output"}],"isError":false}},"channels":{"fast-agent-shell-process-metadata":[{"type":"text","text":"{\"model-call\":{\"exit_code\":0}}"}]}}`)
	before, err := freshFastagentRootReceipt("root", "agent")
	require.NoError(t, err)
	projection, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "native-shell", rootInputProof(t), agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.Len(t, projection.rows, 5)
	assert.JSONEq(t, `{"type":"text","text":"{\"model-call\":{\"exit_code\":0}}"}`, string(projection.rows[3].content.Original))
	assert.Contains(t, string(projection.rows[3].content.Supplemental), "fast-agent-shell-process-metadata")
}

func TestFastagentRootProjectionPreservesNativeToolErrorChannel(t *testing.T) {
	messages := rootNativeMessages("fatal native tool error")
	messages[2] = json.RawMessage(`{"role":"user","content":[{"type":"text","text":"fatal native tool error"}],"tool_results":{"model-call":{"content":[{"type":"text","text":"fatal native tool error"}],"isError":true}},"channels":{"fast-agent-error":[{"type":"text","text":"fatal native tool error"}]}}`)
	before, err := freshFastagentRootReceipt("root", "agent")
	require.NoError(t, err)
	projection, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "native-error", rootInputProof(t), agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.Len(t, projection.rows, 6)
	assert.JSONEq(t, `{"type":"text","text":"fatal native tool error"}`, string(projection.rows[4].content.Original))
}
func TestFastagentRootProjectionRequiresCancellationAcknowledgementForCheckpoint(t *testing.T) {
	messages := rootNativeMessages("native completed result")[:3]
	before, err := freshFastagentRootReceipt("root", "agent")
	require.NoError(t, err)
	_, err = projectFastagentRootArchive(before, rootNativeArchive(messages), "not-ended", rootInputProof(t), agent.MessageCompletionComplete)
	require.Error(t, err)
	projection, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "cancelled", rootInputProof(t), agent.MessageCompletionInterrupted)
	require.NoError(t, err)
	require.Len(t, projection.rows, 3)
	assert.Empty(t, projection.rows[2].content.Completion, "a completed native result keeps its own outcome even when later generation was canceled")
	_, err = projectFastagentRootArchive(before, rootNativeArchive(messages[:2]), "unfinished", rootInputProof(t), agent.MessageCompletionInterrupted)
	require.Error(t, err, "the cancellation acknowledgement cannot replace an absent native result")
}

func TestFastagentRootProjectionDoesNotInventHumanInputFromToolResultContext(t *testing.T) {
	messages := rootNativeMessages("native interruption")
	messages[2] = json.RawMessage(`{"role":"user","content":[{"type":"text","text":"Native tool context, not human input."}],"tool_results":{"model-call":{"content":[{"type":"text","text":"native interruption"}],"isError":true}}}`)
	before, err := freshFastagentRootReceipt("root", "agent")
	require.NoError(t, err)
	projection, err := projectFastagentRootArchive(before, rootNativeArchive(messages), "native-context", rootInputProof(t), agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.Len(t, projection.rows, 5)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, projection.rows[3].source)
	assert.JSONEq(t, `{"type":"text","text":"Native tool context, not human input."}`, string(projection.rows[3].content.Original))
}
