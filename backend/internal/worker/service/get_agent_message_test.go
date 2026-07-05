package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func getAgentMessage(t *testing.T, d *channel.Dispatcher, agentID string, seq int64) (*leapmuxv1.GetAgentMessageResponse, *testResponseWriter) {
	t.Helper()
	w := newTestWriter()
	dispatch(d, "GetAgentMessage", &leapmuxv1.GetAgentMessageRequest{AgentId: agentID, Seq: seq}, w)
	if len(w.responses) == 0 {
		return nil, w
	}
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.GetAgentMessageResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	return &resp, w
}

// TestGetAgentMessage_ReturnsMessageBySeq asserts the handler returns the row whose
// per-agent seq matches, carrying the fields the rail preview needs (content stays
// compressed for the frontend to decode; mark_type rides along).
func TestGetAgentMessage_ReturnsMessageBySeq(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "m1",
		AgentID:       "agent-1",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte(`{"content":"hello world"}`),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		MarkType:      leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)
	// A second row at a different seq to prove the handler selects by seq, not "first".
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "m2", AgentID: "agent-1", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content: []byte(`{"content":"other"}`), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	resp, _ := getAgentMessage(t, d, "agent-1", seq)
	require.NotNil(t, resp.GetMessage(), "the message at the requested seq must be returned")
	assert.Equal(t, "m1", resp.GetMessage().GetId())
	assert.Equal(t, seq, resp.GetMessage().GetSeq())
	assert.Equal(t, []byte(`{"content":"hello world"}`), resp.GetMessage().GetContent())
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, resp.GetMessage().GetMarkType())
}

// TestGetAgentMessage_MissingSeq_ReturnsUnset asserts a seq with no row yields an
// unset message (not an error): a mark can outlive its message after a delete/reseq.
func TestGetAgentMessage_MissingSeq_ReturnsUnset(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	resp, w := getAgentMessage(t, d, "agent-1", 999)
	require.Empty(t, w.errors, "a missing seq is a normal empty result, not an error")
	require.NotNil(t, resp)
	assert.Nil(t, resp.GetMessage(), "no row at that seq -> unset message")
}

// TestGetAgentMessage_ClosedAgent_ReturnsUnset mirrors ListAgentMessages: a closed
// agent yields an unset message rather than an error.
func TestGetAgentMessage_ClosedAgent_ReturnsUnset(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	seq := seedMark(t, svc, "agent-1", "m1", leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE)
	require.NoError(t, svc.Queries.CloseAgent(ctx, "agent-1"))

	resp, w := getAgentMessage(t, d, "agent-1", seq)
	require.Empty(t, w.errors)
	require.NotNil(t, resp)
	assert.Nil(t, resp.GetMessage())
}

// TestGetAgentMessage_UnknownAgent_Errors asserts an unknown agent id produces an
// error (not a success response), via requireAccessibleAgent.
func TestGetAgentMessage_UnknownAgent_Errors(t *testing.T) {
	_, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	resp, w := getAgentMessage(t, d, "nope", 1)
	assert.Nil(t, resp, "an unknown agent must not produce a success response")
	require.NotEmpty(t, w.errors, "an unknown agent must produce an error")
}

// TestGetAgentMessage_InaccessibleWorkspace_Denied is the security check the RPC
// exists to satisfy: a caller whose channel was NOT granted the agent's workspace
// cannot read the message, even though the row exists and the agent id is valid.
// requireAccessibleAgent must deny before the agent-scoped query ever runs.
func TestGetAgentMessage_InaccessibleWorkspace_Denied(t *testing.T) {
	ctx := context.Background()
	// The caller's channel is granted ws-1 only.
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	// The agent (and its message) live in ws-2, which the caller cannot access.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "other-agent", WorkspaceID: "ws-2", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	seq := seedMark(t, svc, "other-agent", "m1", leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE)

	resp, w := getAgentMessage(t, d, "other-agent", seq)
	assert.Nil(t, resp, "a cross-workspace read must not return the message")
	require.NotEmpty(t, w.errors, "a cross-workspace read must be denied")
}
