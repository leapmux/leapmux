package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestListAgentMessagesCarriesTheAgentsProvider pins where a stored message's
// provider comes from. No message row stores one: every read takes it from the
// message's agent, which never changes provider. The agent here is a Codex
// agent, so each of the three reads must report CODEX.
func TestListAgentMessagesCarriesTheAgentsProvider(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, d, _ := setupTestService(t)
	const agentID = "codex-agent"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	var seqs []int64
	for _, id := range []string{"first", "second"} {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID: id, AgentID: agentID, SpanID: "span-1",
			Source:  leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			Content: []byte(`{"type":"result"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			CreatedAt: sqltime.NewSQLiteTime(time.Now()),
		})
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}
	call := func(method string, request proto.Message, response proto.Message) {
		t.Helper()
		w := newTestWriter()
		dispatch(d, method, request, w)
		require.Empty(t, w.errors, method)
		require.Len(t, w.responses, 1, method)
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), response))
	}

	var listed leapmuxv1.ListAgentMessagesResponse
	call(leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID, Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, Limit: 10,
	}, &listed)
	require.Len(t, listed.GetMessages(), 2)
	for _, message := range listed.GetMessages() {
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, message.GetAgentProvider(), "ListAgentMessages %s", message.GetId())
	}

	var single leapmuxv1.GetAgentMessageResponse
	call(leapmuxv1connect.WorkerAgentServiceGetAgentMessageProcedure, &leapmuxv1.GetAgentMessageRequest{AgentId: agentID, Seq: seqs[1]}, &single)
	require.NotNil(t, single.GetMessage())
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, single.GetMessage().GetAgentProvider(), "GetAgentMessage")

	var span leapmuxv1.GetAgentSpanMessagesResponse
	call(leapmuxv1connect.WorkerAgentServiceGetAgentSpanMessagesProcedure, &leapmuxv1.GetAgentSpanMessagesRequest{AgentId: agentID, SpanId: "span-1"}, &span)
	require.Len(t, span.GetMessages(), 2)
	for _, message := range span.GetMessages() {
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, message.GetAgentProvider(), "GetAgentSpanMessages %s", message.GetId())
	}
}

// TestReplayAgentCatchUp_ReplaysMessagesWithTheAgentsProvider covers the
// fourth read of a stored message: the catch-up replay a reconnecting watcher
// receives. It too must report the agent's provider, here Pi.
func TestReplayAgentCatchUp_ReplaysMessagesWithTheAgentsProvider(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, _, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
	}))
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "stored", AgentID: "agent-1",
		Source:  leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content: []byte(`{"type":"result"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	svc.replayAgentCatchUp(newReplaySink(w, 0, nil), &leapmuxv1.WatchAgentEntry{AgentId: "agent-1"}, dbAgent, nil)

	var replayed *leapmuxv1.AgentChatMessage
	for _, stream := range w.streamsSnapshot() {
		if message := decodeWatchAgentEvent(t, stream).GetAgentMessage(); message != nil && message.GetId() == "stored" {
			replayed = message
		}
	}
	require.NotNil(t, replayed, "catch-up replay must include the stored message")
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, replayed.GetAgentProvider())
}
