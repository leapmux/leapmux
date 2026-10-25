package service

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// Scheduled retries are queue items, so lifecycle cleanup removes them through
// the queue: the same sweep that clears an agent's runtime state cancels its
// pending retries, and it leaves the agent's ordinary input in the queue.

func seedScheduledRetries(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	ctx := context.Background()
	seedAgent(t, svc, agentID)
	require.NoError(t, svc.InputQueue.ScheduleAutoRetry(ctx, agentID, retrySchedule(
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, time.Now().Add(time.Hour))))
	require.NoError(t, svc.InputQueue.ScheduleAutoRetry(ctx, agentID, retrySchedule(
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{})))
	_, err := svc.InputQueue.Enqueue(ctx, queueTextItem(agentID, "ordinary"))
	require.NoError(t, err)
}

func retrySchedule(kind leapmuxv1.AgentInputKind, at time.Time) agent.AutoRetrySchedule {
	return agent.AutoRetrySchedule{Kind: kind, ResetAt: at, SourcePayload: []byte(`{"source":true}`)}
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

// The teardown cancels the retries before the close stamps closed_at, and a
// provider report that arrives between the two creates a retry that the
// scheduler still admits. The close must remove that retry too, or it
// dispatches into an agent that runs no process.
func TestCloseAgent_RemovesARetryThatALateReportCreatesDuringTheClose(t *testing.T) {
	t.Parallel()

	var svc *Service
	var injected atomic.Bool
	svc, d, w := setupTestService(t, withQueryRewrite(func(query string) string {
		if strings.Contains(query, "-- name: CloseAgent :") && injected.CompareAndSwap(false, true) {
			// The report lands after the teardown and before closed_at. The
			// handler may run off the test goroutine, so assert, not require.
			assert.NoError(t, svc.InputQueue.ScheduleAutoRetry(context.Background(), "agent-1", retrySchedule(
				leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, time.Now().Add(time.Hour))))
		}
		return query
	}))
	seedScheduledRetries(t, svc, "agent-1")

	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"}, w)
	require.Empty(t, w.errors)
	require.True(t, injected.Load(), "the close must run CloseAgent")
	assertRetriesCancelled(t, svc, "agent-1")

	// closed_at is committed, so a report after the close stores nothing.
	err := svc.InputQueue.ScheduleAutoRetry(context.Background(), "agent-1", retrySchedule(
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{}))
	require.ErrorIs(t, err, inputqueue.ErrConflict)
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
