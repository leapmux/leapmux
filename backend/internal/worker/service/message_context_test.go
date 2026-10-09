package service

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

func TestToolRequestLookupSeparatesProviderSessions(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	span := agent.SpanInfo{SpanID: "reused-tool", SpanType: "Read"}
	oldRequest := []byte(`{"sessionUpdate":"tool_call","toolCallId":"reused-tool","kind":"read","rawInput":{"path":"old.txt"}}`)
	newRequest := []byte(`{"sessionUpdate":"tool_call","toolCallId":"reused-tool","kind":"read","rawInput":{"path":"new.txt"}}`)
	sink.UpdateSessionID("old-session")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: oldRequest}, span))
	sink.UpdateSessionID("new-session")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: newRequest}, span))
	request, err := sink.ReadToolRequest(span.SpanID)
	require.NoError(t, err)
	require.NotNil(t, request)
	require.Equal(t, newRequest, request.Content.Original)
	require.Equal(t, "new-session", request.Content.AgentSessionID)
	late := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"reused-tool","status":"completed","rawOutput":"Late old output"}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: late, AgentSessionID: "old-session"}, span))
	request, err = sink.ReadToolRequest(span.SpanID)
	require.NoError(t, err)
	require.Equal(t, newRequest, request.Content.Original)
	reloaded := sink.h.NewSink(sink.agentID, sink.agentProvider)
	request, err = reloaded.ReadToolRequest(span.SpanID)
	require.NoError(t, err)
	require.Equal(t, newRequest, request.Content.Original)
}

func TestToolResultExactReadsKeepStoredSequenceAndNativeSession(t *testing.T) {
	t.Parallel()
	_, sink := setupRootSink(t, "exact-result-reader")
	span := agent.SpanInfo{SpanID: "reused-span", Closing: true}
	original := []byte(`{"same":"bytes"}`)
	sink.UpdateSessionID("first")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, Supplemental: []byte(`{"native":0}`), Metadata: []byte(`{"duration_ms":0}`)}, span))
	sink.UpdateSessionID("second")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, span))
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: original}, span))
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, agent.SpanInfo{}))
	for _, sequence := range []int64{-1, 0, 3, 4, 9223372036854775807} {
		row, err := sink.ReadToolResultBySeq(sequence)
		require.NoError(t, err)
		require.Nil(t, row)
	}
	row, err := sink.ReadToolResultBySeq(1)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, int64(1), row.Seq)
	require.Equal(t, "first", row.Content.AgentSessionID)
	require.Equal(t, original, row.Content.Original)
	require.JSONEq(t, `{"native":0}`, string(row.Content.Supplemental))
	require.JSONEq(t, `{"duration_ms":0}`, string(row.Content.Metadata))
	row.Content.Original[0] = 'x'
	row.Content.Supplemental[0] = 'x'
	row.Content.Metadata[0] = 'x'
	again, err := sink.ReadToolResultForSession(span.SpanID, "first")
	require.NoError(t, err)
	require.NotNil(t, again)
	require.Equal(t, int64(1), again.Seq)
	require.Equal(t, original, again.Content.Original)
	require.JSONEq(t, `{"native":0}`, string(again.Content.Supplemental))
	require.JSONEq(t, `{"duration_ms":0}`, string(again.Content.Metadata))
	missing, err := sink.ReadToolResultForSession("", "first")
	require.NoError(t, err)
	require.Nil(t, missing)
	missing, err = sink.ReadToolResultForSession(span.SpanID, "")
	require.NoError(t, err)
	require.Nil(t, missing, "an explicit empty session must not read the current session")
}

func TestToolResultExactReadKeepsOriginalBytesWhenTheSupplementIsUnreadable(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "corrupt-result-reader")
	original := []byte(`{"tool":"result"}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "call", Closing: true}))
	_, err := svc.DB.ExecContext(t.Context(), `UPDATE messages SET supplemental_content = X'FF', supplemental_content_compression = ? WHERE agent_id = ?`,
		leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD, "corrupt-result-reader")
	require.NoError(t, err)
	row, err := sink.ReadToolResultBySeq(1)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, original, row.Content.Original)
	require.Empty(t, row.Content.Supplemental)
}
