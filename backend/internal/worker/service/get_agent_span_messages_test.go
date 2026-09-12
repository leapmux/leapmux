package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func getAgentSpanMessages(t *testing.T, dispatcher *channel.Dispatcher, agentID, spanID string) (*leapmuxv1.GetAgentSpanMessagesResponse, *testResponseWriter) {
	t.Helper()
	writer := newTestWriter()
	dispatch(dispatcher, contracts.RPCMethodGetAgentSpanMessages, &leapmuxv1.GetAgentSpanMessagesRequest{AgentId: agentID, SpanId: spanID}, writer)
	if len(writer.responses) == 0 {
		return nil, writer
	}
	var response leapmuxv1.GetAgentSpanMessagesResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
	return &response, writer
}

func TestGetAgentSpanMessagesScopesAndOrdersRelatedMessages(t *testing.T) {
	t.Parallel()
	service, dispatcher, _ := setupTestService(t)
	for _, agentID := range []string{"agent-1", "agent-2"} {
		require.NoError(t, service.Queries.CreateAgent(t.Context(), db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir()}))
		for _, suffix := range []string{"request", "result", "unrelated"} {
			spanID := "call\nwith-provider-suffix"
			if suffix == "unrelated" {
				spanID = "other-call"
			}
			_, err := createMessageRow(t.Context(), service.Queries, db.CreateMessageParams{
				ID: agentID + "-" + suffix, AgentID: agentID,
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
				Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				Content:       []byte(`{"type":"tool"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
				SpanID: spanID, CreatedAt: sqltime.NewSQLiteTime(time.Now()),
			})
			require.NoError(t, err)
		}
	}
	response, writer := getAgentSpanMessages(t, dispatcher, "agent-1", "call\nwith-provider-suffix")
	require.Empty(t, writer.errors)
	require.Len(t, response.GetMessages(), 2)
	assert.Equal(t, "agent-1-request", response.Messages[0].Id)
	assert.Equal(t, "agent-1-result", response.Messages[1].Id)
	assert.Less(t, response.Messages[0].Seq, response.Messages[1].Seq)

	response, writer = getAgentSpanMessages(t, dispatcher, "agent-1", "missing")
	require.Empty(t, writer.errors)
	assert.Empty(t, response.GetMessages())
	response, writer = getAgentSpanMessages(t, dispatcher, "agent-1", "' OR 1=1 --")
	require.Empty(t, writer.errors)
	assert.Empty(t, response.GetMessages())
}

func TestGetAgentSpanMessagesRejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	service, dispatcher, _ := setupTestService(t)
	require.NoError(t, service.Queries.CreateAgent(t.Context(), db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir()}))
	_, writer := getAgentSpanMessages(t, dispatcher, "agent-1", "")
	assert.NotEmpty(t, writer.errors)
	_, writer = getAgentSpanMessages(t, dispatcher, "unknown-agent", "span")
	assert.NotEmpty(t, writer.errors)
}
