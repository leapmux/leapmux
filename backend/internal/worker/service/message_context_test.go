package service

import (
	"database/sql"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/require"
)

func TestMessageKeyLookupSeparatesAgentsAndExactNativeSessions(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "native-key-reader")
	for index, sessionID := range []string{"", "first", "second"} {
		sink.UpdateSessionID(sessionID)
		original := []byte(`{"record":"` + strings.Repeat("native ", index+1) + `"}`)
		content := sink.CaptureMessage(agent.MessageContent{Original: original, IdempotencyKey: "reused", Supplemental: []byte(`{"value":0}`), Metadata: []byte(`{"duration_ms":0}`)}, agent.SpanInfo{})
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}))
	}
	sink.UpdateSessionID("current")
	for index, sessionID := range []string{"", "first", "second"} {
		original := []byte(`{"record":"` + strings.Repeat("native ", index+1) + `"}`)
		row, err := sink.ReadMessageByIdempotencyKey(sessionID, "reused")
		require.NoError(t, err)
		require.NotNil(t, row)
		require.Equal(t, int64(index+1), row.Seq)
		require.Equal(t, sessionID, row.Content.AgentSessionID)
		require.Equal(t, "reused", row.Content.IdempotencyKey)
		require.Equal(t, original, row.Content.Original)
		require.JSONEq(t, `{"value":0}`, string(row.Content.Supplemental))
		require.JSONEq(t, `{"duration_ms":0}`, string(row.Content.Metadata))
		row.Content.Original[0] = 'x'
		row.Content.Supplemental[0] = 'x'
		row.Content.Metadata[0] = 'x'
		again, err := sink.ReadMessageByIdempotencyKey(sessionID, "reused")
		require.NoError(t, err)
		require.Equal(t, original, again.Content.Original)
		require.JSONEq(t, `{"value":0}`, string(again.Content.Supplemental))
		require.JSONEq(t, `{"duration_ms":0}`, string(again.Content.Metadata))
	}
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: "another-key-owner", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	other := newTestRootOutputSink(t, svc.Output, "another-key-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	other.UpdateSessionID("first")
	require.NoError(t, other.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"foreign":true}`), IdempotencyKey: "foreign-only"}, agent.SpanInfo{}))
	for _, query := range []struct{ session, key string }{{"first", ""}, {"first", "missing"}, {"missing", "reused"}, {"first", "foreign-only"}} {
		row, err := sink.ReadMessageByIdempotencyKey(query.session, query.key)
		require.NoError(t, err)
		require.Nil(t, row)
	}
	row, err := (&agentOutputSink{}).ReadMessageByIdempotencyKey("native", "")
	require.NoError(t, err)
	require.Nil(t, row, "an empty key never reaches the store")
}

func TestStoredProviderRecordReportsReadAndOriginalDecodeErrors(t *testing.T) {
	t.Parallel()
	row, err := storedMessage("provider record", db.Message{}, sql.ErrNoRows)
	require.NoError(t, err)
	require.Nil(t, row)
	_, err = storedMessage("provider record", db.Message{}, sql.ErrConnDone)
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.ErrorContains(t, err, "read provider record")
	_, err = storedMessage("provider record", db.Message{Content: []byte("invalid compression"), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "decode provider record")
}

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
	reloaded := newTestRootOutputSink(t, sink.h, sink.agentID, sink.agentProvider)
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
