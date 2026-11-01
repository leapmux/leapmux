package service

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
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

	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-1")}, w)
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

	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-1")}, w)
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

	dispatch(d, leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, &leapmuxv1.CleanupWorkspaceRequest{
		Tabs: []*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"}},
	}, w)
	require.Empty(t, w.errors)
	assertRetriesCancelled(t, svc, "agent-1")
}

// A provider report can wait in the worker's retained output queue while its
// process is replaced. Its original authority must survive that wait.
func TestAutoRetryRejectsAnOldProviderMutationAfterCleanup(t *testing.T) {
	t.Parallel()
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "schedule", true: "cancel"}[cancel], func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			svc, sink, agentID, _ := setupBgTaskTestWithService(t)
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseMutation := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseMutation()
			if cancel {
				original := svc.Output.cancelAutoRetry
				svc.Output.cancelAutoRetry = func(id string, kind leapmuxv1.AgentInputKind, admission agent.AutoRetryAdmission) {
					close(entered)
					<-release
					original(id, kind, admission)
				}
			} else {
				original := svc.Output.scheduleAutoRetry
				svc.Output.scheduleAutoRetry = func(id string, schedule agent.AutoRetrySchedule) {
					close(entered)
					<-release
					original(id, schedule)
				}
			}
			ordered := agent.NewOrderedProviderServices(&agent.OutputQueue{}, sink)
			go func() {
				if cancel {
					ordered.CancelUsageLimitRetry()
				} else {
					ordered.ScheduleUsageLimitRetry(time.Now().Add(time.Hour), []byte(`{"old":true}`))
				}
				close(finished)
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("the old provider mutation did not reach queue admission")
			}
			svc.Output.CleanupAgent(agentID)
			// A new process can now report a retry for the same open agent.
			if cancel {
				require.NoError(t, svc.InputQueue.ScheduleAutoRetry(t.Context(), agentID, retrySchedule(
					leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, time.Now().Add(time.Hour))))
			}
			before, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			releaseMutation()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("the old provider mutation did not finish")
			}
			after, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, before, after, "a retired provider must not change the replacement queue")
		})
	}
}

func TestCapturedAutoRetryRefusesReplacedOwnersWithoutResettingBackoff(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"session", "process", "cleanup"} {
		t.Run(replacement, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			svc, _, _ := setupTestService(t, withClock(clock))
			agentID := "agent-1"
			seedAgent(t, svc, agentID)
			sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			writer := sink.CaptureAutoRetry()
			switch replacement {
			case "session":
				sink.UpdateSessionID("replacement-session")
			case "process":
				newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			case "cleanup":
				svc.Output.CleanupAgent(agentID)
			}
			kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
			require.NoError(t, svc.InputQueue.ScheduleAutoRetry(t.Context(), agentID, retrySchedule(kind, time.Time{})))
			before, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			writer.CancelServerErrorRetry()
			writer.ScheduleServerErrorRetry([]byte(`{"old":true}`))
			after, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			var delay int64
			require.NoError(t, svc.DB.QueryRowContext(t.Context(), `SELECT auto_retry_server_error_delay_ms FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&delay))
			assert.Equal(t, int64(20000), delay, "a stale cancellation must not reset the replacement backoff")
		})
	}
}

func TestCapturedAutoRetryLeaseProtectsTheOwnerUntilRelease(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupBgTaskTestWithService(t)
	writer := sink.CaptureAutoRetry().(*capturedAutoRetryWriter)
	lease := writer.Acquire()
	require.NotNil(t, lease)
	mutation := svc.Output.transcriptMutationMutex(agentID)
	locked := mutation.TryLock()
	if locked {
		mutation.Unlock()
	}
	assert.False(t, locked, "context cleanup must wait while the admitted retry mutates its queue")
	lease.Release()
	lease.Release()
	locked = mutation.TryLock()
	require.True(t, locked, "the lease must release the context mutation lock")
	mutation.Unlock()
	sink.UpdateSessionID("replacement-session")
	assert.Nil(t, writer.Acquire(), "a released writer cannot renew an expired owner")
}
