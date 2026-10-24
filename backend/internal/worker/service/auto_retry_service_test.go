package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// Scheduled retries are queue items, so lifecycle cleanup removes them through
// the queue: the same sweep that clears an agent's runtime state cancels its
// pending retries while ordinary input survives for the next open.

func seedScheduledRetries(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:            agentID, WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, svc.InputQueue.ScheduleAutoRetry(ctx, agentID, retrySchedule(
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, time.Now().Add(time.Hour))))
	require.NoError(t, svc.InputQueue.ScheduleAutoRetry(ctx, agentID, retrySchedule(
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{})))
	_, err := svc.InputQueue.Enqueue(ctx, queueTextItem(agentID, "ordinary"))
	require.NoError(t, err)
}

func retrySchedule(kind leapmuxv1.AgentInputKind, at time.Time) agent.AutoRetrySchedule {
	return agent.AutoRetrySchedule{Kind: kind, ScheduledAt: at, SourcePayload: []byte(`{"source":true}`)}
}

func queueTextItem(agentID, text string) inputqueue.NewItem {
	return inputqueue.NewItem{ID: "ordinary-" + agentID, AgentID: agentID,
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: text}
}

func assertRetriesCancelled(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	snapshot, err := svc.InputQueue.Snapshot(context.Background(), agentID)
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1, "the ordinary input survives")
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, snapshot.Items[0].Kind)
}

func TestCloseAgent_CancelsScheduledRetries(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	seedScheduledRetries(t, svc, "agent-1")

	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"}, w)
	require.Empty(t, w.errors)
	assertRetriesCancelled(t, svc, "agent-1")
}

func TestCleanupWorkspace_CancelsScheduledRetries(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	seedScheduledRetries(t, svc, "agent-1")

	dispatch(d, "CleanupWorkspace", &leapmuxv1.CleanupWorkspaceRequest{
		Tabs: []*leapmuxv1.TabRef{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"}},
	}, w)
	require.Empty(t, w.errors)
	assertRetriesCancelled(t, svc, "agent-1")
}
