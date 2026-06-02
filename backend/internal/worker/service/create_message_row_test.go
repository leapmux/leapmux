package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// createMessageRow is the single guarded chokepoint for persisting chat-message
// rows: it refuses an UNSPECIFIED agent provider so a row the frontend cannot
// attribute to a provider is never written (the frontend would otherwise render
// it as `unsupported_provider`).
func TestCreateMessageRow_RejectsUnspecifiedProvider(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		Model:         "opus",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	params := func(id string, provider leapmuxv1.AgentProvider) db.CreateMessageParams {
		return db.CreateMessageParams{
			ID:            id,
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			Content:       []byte(`{"type":"result"}`),
			AgentProvider: provider,
			CreatedAt:     time.Now(),
		}
	}

	// An UNSPECIFIED provider is refused before any row is written.
	_, err := createMessageRow(ctx, svc.Queries, params("msg-bad", leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNSPECIFIED")

	msgs, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	assert.Empty(t, msgs, "a rejected message must not be persisted")

	// A real provider is persisted normally.
	seq, err := createMessageRow(ctx, svc.Queries, params("msg-ok", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	assert.Positive(t, seq)

	msgs, err = svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs[0].AgentProvider)
}

// createAgentRecord enforces the same real-provider invariant at the point agent
// rows are born, so a misconfigured caller fails at creation with a clear error
// rather than later when createMessageRow rejects the agent's first message. The
// production SendAgentMessage path defaults an UNSPECIFIED request to a real
// provider before reaching here, so this guard is a backstop.
func TestCreateAgentRecord_RejectsUnspecifiedProvider(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	base := func(id string, provider leapmuxv1.AgentProvider) db.CreateAgentParams {
		return db.CreateAgentParams{
			ID:            id,
			WorkspaceID:   "ws-1",
			WorkingDir:    t.TempDir(),
			HomeDir:       t.TempDir(),
			Model:         "opus",
			AgentProvider: provider,
		}
	}

	// An UNSPECIFIED provider is refused before any row is written.
	err := svc.createAgentRecord(ctx, base("agent-bad", leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNSPECIFIED")

	_, err = svc.Queries.GetAgentByID(ctx, "agent-bad")
	require.Error(t, err, "a rejected agent must not be persisted")

	// A real provider is created normally.
	require.NoError(t, svc.createAgentRecord(ctx, base("agent-ok", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)))
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-ok")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, dbAgent.AgentProvider)
}
