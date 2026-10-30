package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// Scheduled retries are queue items, so lifecycle cleanup removes them through
// the queue: the same sweep that clears an agent's runtime state cancels its
// pending retries, and it leaves the agent's ordinary input in the queue.

func seedScheduledRetries(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	ctx := context.Background()
	seedAgent(t, svc, agentID)
	// These lifecycle fixtures retain their ordinary input. Pause before any
	// enqueue so no provider startup competes with their explicit output sink.
	_, err := svc.InputQueue.SetPaused(ctx, agentID, true)
	require.NoError(t, err)
	require.NoError(t, svc.InputQueue.ScheduleAutoRetry(ctx, agentID, retrySchedule(
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, time.Now().Add(time.Hour))))
	require.NoError(t, svc.InputQueue.ScheduleAutoRetry(ctx, agentID, retrySchedule(
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{})))
	_, err = svc.InputQueue.Enqueue(ctx, queueTextItem(agentID, "ordinary"))
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

// Each lifecycle path that ends an agent's runtime states its own reason for
// the retries that it removes, so the log separates a tab close from an
// archive, a context clear, and a plan restart.
func TestRuntimeCleanupStatesWhyItRemovedTheRetries(t *testing.T) {
	// Not parallel: it captures the default logger, which is process-global.
	startNothing := func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return map[string]string{}, nil
	}
	for _, test := range []struct {
		name   string
		reason RuntimeCleanupReason
		end    func(t *testing.T, svc *Service, d *channel.Dispatcher, w *testResponseWriter)
	}{
		{"tab close", CleanupTabClosed, func(t *testing.T, _ *Service, d *channel.Dispatcher, w *testResponseWriter) {
			dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"}, w)
			require.Empty(t, w.errors)
		}},
		{"workspace archive", CleanupWorkspaceArchived, func(t *testing.T, svc *Service, _ *channel.Dispatcher, _ *testResponseWriter) {
			applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
				&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"})
		}},
		{"context clear", CleanupContextCleared, func(t *testing.T, svc *Service, _ *channel.Dispatcher, _ *testResponseWriter) {
			svc.startAgentFn = startNothing
			finish, err := svc.prepareClearContext("agent-1")
			require.NoError(t, err)
			finish()
		}},
		{"plan restart", CleanupPlanContextReset, func(t *testing.T, svc *Service, _ *channel.Dispatcher, _ *testResponseWriter) {
			svc.startAgentFn = startNothing
			require.NoError(t, svc.initiatePlanExecutionRestart("agent-1", "acceptEdits", requireAgentRow(t, svc, "agent-1")))
		}},
		{"closed-agent sweep", CleanupClosedAgentSwept, func(t *testing.T, svc *Service, _ *channel.Dispatcher, _ *testResponseWriter) {
			// A close that bypassed every cleanup path leaves the agent's
			// tracker and its retries behind, and the sweep reclaims both.
			svc.Output.rootTracker("agent-1")
			require.NoError(t, closeErr(svc.Queries.CloseAgent(t.Context(), "agent-1")))
			svc.SweepOrphanedAgentState()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			svc, d, w := setupTestService(t)
			defer drainAllInFlight(svc)
			seedScheduledRetries(t, svc, "agent-1")

			test.end(t, svc, d, w)

			removed := retryRemovalLines(logs, "agent-1")
			require.Len(t, removed, 2, "one line for each pending retry")
			for _, line := range removed {
				assert.Contains(t, line, `reason="`+string(test.reason)+`"`)
			}
			assertRetriesCancelled(t, svc, "agent-1")
		})
	}
}

// A root tab close ends each subagent of the root with it, so the cleanup of
// a subagent states the root's tab close as its reason: the subagent itself
// did not end. The queue admits no retry for a subagent, so no log line can
// show the reason, and the test records the cancellation that the cleanup
// asks for instead.
func TestRootTabCloseStatesItsReasonForTheRetriesOfASubagent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	seedAgent(t, svc, "agent-1")
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: "child-1", ParentAgentID: sql.NullString{String: "agent-1", Valid: true},
		SpawnSpanID: "spawn-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	var mu sync.Mutex
	reasons := map[string][]string{}
	original := svc.Output.cancelAllAutoRetry
	svc.Output.cancelAllAutoRetry = func(agentID, reason string) {
		mu.Lock()
		reasons[agentID] = append(reasons[agentID], reason)
		mu.Unlock()
		original(agentID, reason)
	}

	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"}, w)
	require.Empty(t, w.errors)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, map[string][]string{
		"agent-1": {string(CleanupTabClosed)},
		"child-1": {string(cleanupRootTabClosed)},
	}, reasons)
}

// A provider's cancellation reaches the queue with the provider's reason, and
// it removes the retry of its own kind only.
func TestAProviderCancellationStatesItsReasonInTheLog(t *testing.T) {
	// Not parallel: it captures the default logger, which is process-global.
	logs := testutil.CaptureDefaultLogger(t)
	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)
	var starts atomic.Int32
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		starts.Add(1)
		return nil, assert.AnError
	}
	seedScheduledRetries(t, svc, "agent-1")
	sink, err := svc.Output.NewRootSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, err)

	sink.CancelUsageLimitRetry(`Claude reported status "allowed" for the five_hour usage limit`)

	removed := retryRemovalLines(logs, "agent-1")
	require.Len(t, removed, 1)
	assert.Contains(t, removed[0], "kind="+leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT.String()+" ")
	assert.Contains(t, removed[0], `reason="Claude reported status \"allowed\" for the five_hour usage limit"`)
	snapshot, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	var kinds []leapmuxv1.AgentInputKind
	for _, item := range snapshot.Items {
		kinds = append(kinds, item.Kind)
	}
	assert.ElementsMatch(t, []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	}, kinds, "the server-error retry and the ordinary input stay")
	assert.Zero(t, starts.Load(), "the fixture must not start a competing provider")
}

// retryRemovalLines returns each captured "automatic retry removed" line of
// one agent.
func retryRemovalLines(logs *testutil.LogBuffer, agentID string) []string {
	var removed []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, `msg="automatic retry removed"`) && strings.Contains(line, "agent_id="+agentID+" ") {
			removed = append(removed, line)
		}
	}
	return removed
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
				svc.Output.cancelAutoRetry = func(id string, kind leapmuxv1.AgentInputKind, reason string, admission agent.AutoRetryAdmission) {
					close(entered)
					<-release
					original(id, kind, reason, admission)
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
					ordered.CancelUsageLimitRetry("the usage limit lifted")
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
			svc.Output.CleanupAgent(agentID, CleanupContextCleared)
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
				svc.Output.CleanupAgent(agentID, CleanupContextCleared)
			}
			kind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
			require.NoError(t, svc.InputQueue.ScheduleAutoRetry(t.Context(), agentID, retrySchedule(kind, time.Time{})))
			before, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			writer.CancelServerErrorRetry("the turn ended")
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

func TestCapturedKeyedRetryCleanupSurvivesANewerTurn(t *testing.T) {
	t.Parallel()
	for _, pendingKey := range []string{"native:A", "native:B", ""} {
		t.Run("pending="+pendingKey, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			svc, _, _ := setupTestService(t, withClock(clock))
			const agentID = "agent-1"
			seedAgent(t, svc, agentID)
			sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			svc.Output.NoteAgentProcessStarted(agentID)
			sink.UpdateSessionID("session-a")
			sink.SetTurnState(agent.TurnState{Active: true}, 1)
			writer := sink.CaptureAutoRetryThroughTurn(2)
			writer.ScheduleUsageLimitRetryWithKey("native:A", clock.Now().Add(time.Hour), []byte(`{"source":"A"}`))
			sink.SetTurnState(agent.TurnState{Active: true}, 3)
			if pendingKey != "native:A" {
				current := sink.CaptureAutoRetry()
				if pendingKey == "" {
					current.ScheduleUsageLimitRetry(clock.Now().Add(2*time.Hour), []byte(`{"source":"user"}`))
				} else {
					current.ScheduleUsageLimitRetryWithKey(pendingKey, clock.Now().Add(2*time.Hour), []byte(`{"source":"B"}`))
				}
			}
			before, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			require.Len(t, before.Items, 1)
			writer.ScheduleUsageLimitRetryWithKey("native:stale", clock.Now().Add(3*time.Hour), []byte(`{"source":"stale"}`))
			unchanged, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, before, unchanged, "a later turn rejects an old schedule")
			writer.CancelUsageLimitRetryWithKey("native:A", "native A completed")
			after, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			assert.True(t, after.ActiveTurn, "keyed cleanup must not end the newer native turn")
			if pendingKey == "native:A" {
				assert.Empty(t, after.Items)
				assert.Greater(t, after.Revision, before.Revision)
			} else {
				assert.Equal(t, before, after, "B or unkeyed intent survives A cleanup")
			}
		})
	}
}

func TestCapturedKeyedRetryCleanupRejectsReplacedSessionsAndProviders(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"session", "same ID session", "same ID adoption", "provider", "cleanup", "activity"} {
		t.Run(replacement, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			svc, _, _ := setupTestService(t, withClock(clock))
			const agentID = "agent-1"
			seedAgent(t, svc, agentID)
			sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			svc.Output.NoteAgentProcessStarted(agentID)
			sink.UpdateSessionID("session-a")
			sink.SetTurnState(agent.TurnState{Active: true}, 1)
			writer := sink.CaptureAutoRetry()
			switch replacement {
			case "session", "same ID session":
				sink.UpdateSessionID("session-b")
				if replacement == "same ID session" {
					sink.UpdateSessionID("session-a")
				}
			case "same ID adoption":
				sink.AdoptNativeSession("session-a")
			case "provider":
				newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			case "cleanup":
				svc.Output.CleanupAgent(agentID, CleanupContextCleared)
			case "activity":
				svc.Output.ForgetActivity(agentID)
				svc.Output.activityFor(agentID, agentID)
			}
			require.NoError(t, svc.InputQueue.ScheduleAutoRetry(t.Context(), agentID, agent.AutoRetrySchedule{
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, Key: "native:A", ResetAt: clock.Now().Add(time.Hour),
			}))
			before, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			writer.CancelUsageLimitRetryWithKey("native:A", "old native completion")
			after, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestCapturedKeyedRetryCleanupSurvivesAScopeReset(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, _, _ := setupTestService(t, withClock(clock))
	const agentID = "agent-1"
	seedAgent(t, svc, agentID)
	sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	svc.Output.NoteAgentProcessStarted(agentID)
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	writer := sink.CaptureAutoRetry()
	writer.ScheduleUsageLimitRetryWithKey("native:A", clock.Now().Add(time.Hour), []byte(`{"source":"A"}`))
	before, err := svc.InputQueue.Snapshot(t.Context(), agentID)
	require.NoError(t, err)
	require.Len(t, before.Items, 1)
	svc.Output.resetAgentActivity(agentID)
	writer.CancelUsageLimitRetryWithKey("native:A", "native A completed")
	after, err := svc.InputQueue.Snapshot(t.Context(), agentID)
	require.NoError(t, err)
	assert.Empty(t, after.Items)
}

func TestCapturedKeyedRetryLeaseProtectsItsSessionAcrossTurns(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupBgTaskTestWithService(t)
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	writer := sink.CaptureAutoRetry().(*capturedAutoRetryWriter)
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	admission := &capturedAutoRetryKeyAdmission{owner: writer.owner}
	lease := admission.Acquire()
	require.NotNil(t, lease)
	mutation := svc.Output.transcriptMutationMutex(agentID)
	locked := mutation.TryLock()
	if locked {
		mutation.Unlock()
	}
	assert.False(t, locked, "session cleanup must wait for the admitted key mutation")
	lease.Release()
	lease.Release()
	locked = mutation.TryLock()
	require.True(t, locked)
	mutation.Unlock()
	sink.UpdateSessionID("replacement")
	assert.Nil(t, admission.Acquire())
}

func TestCapturedAutoRetryThroughTurnAdmitsOnlyItsExactCompletion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		transition string
		allowed    bool
	}{
		{"active", true}, {"idle", true}, {"new-active-without-idle", false},
		{"next-turn", false}, {"repeated-idle", false}, {"same-publisher-reset", false},
		{"session-round-trip", false}, {"same-id-adoption", false}, {"cleanup", false}, {"sink-replacement", false},
		{"strict-capture", false}, {"zero-sequence", false},
	} {
		t.Run(test.transition, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			svc, _, _ := setupTestService(t, withClock(clock))
			const agentID = "agent-1"
			seedAgent(t, svc, agentID)
			sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			svc.Output.NoteAgentProcessStarted(agentID)
			sink.UpdateSessionID("session-a")
			sink.SetTurnState(agent.TurnState{Active: true}, 1)
			writer := sink.CaptureAutoRetryThroughTurn(2)
			switch test.transition {
			case "active":
			case "idle":
				sink.SetTurnState(agent.TurnState{}, 2)
			case "new-active-without-idle":
				sink.SetTurnState(agent.TurnState{Active: true}, 3)
			case "next-turn":
				sink.SetTurnState(agent.TurnState{}, 2)
				sink.SetTurnState(agent.TurnState{Active: true}, 3)
			case "repeated-idle":
				sink.SetTurnState(agent.TurnState{}, 2)
				sink.SetTurnState(agent.TurnState{}, 3)
			case "same-publisher-reset":
				sink.SetTurnState(agent.TurnState{}, 2)
				svc.Output.resetAgentActivity(agentID)
			case "session-round-trip":
				sink.UpdateSessionID("session-b")
				sink.UpdateSessionID("session-a")
			case "same-id-adoption":
				sink.AdoptNativeSession("session-a")
			case "cleanup":
				svc.Output.CleanupAgent(agentID, CleanupContextCleared)
			case "sink-replacement":
				newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			case "strict-capture":
				writer = sink.CaptureAutoRetry()
				sink.SetTurnState(agent.TurnState{}, 2)
			case "zero-sequence":
				writer = sink.CaptureAutoRetryThroughTurn(0)
				sink.SetTurnState(agent.TurnState{}, 2)
			}
			require.NoError(t, svc.InputQueue.ScheduleAutoRetry(t.Context(), agentID,
				retrySchedule(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{})))
			before, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			writer.CancelServerErrorRetry("the completed turn ended the retry cycle")
			writer.ScheduleUsageLimitRetry(clock.Now().Add(time.Hour), []byte(`{"source":"captured"}`))
			after, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			if !test.allowed {
				assert.Equal(t, before, after)
				assert.Equal(t, int64(20000), storedServerErrorDelay(t, svc, agentID))
				return
			}
			require.Len(t, after.Items, 1)
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, after.Items[0].Kind)
			assert.Equal(t, int64(10000), storedServerErrorDelay(t, svc, agentID))
		})
	}
}

func TestCapturedCompletionRetryLeaseProtectsItsInactiveOwner(t *testing.T) {
	t.Parallel()
	svc, _, agentID, _ := setupBgTaskTestWithService(t)
	sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	svc.Output.NoteAgentProcessStarted(agentID)
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	writer := sink.CaptureAutoRetryThroughTurn(2).(*capturedAutoRetryWriter)
	sink.SetTurnState(agent.TurnState{}, 2)
	lease := writer.Acquire()
	require.NotNil(t, lease)
	mutation := svc.Output.transcriptMutationMutex(agentID)
	locked := mutation.TryLock()
	if locked {
		mutation.Unlock()
	}
	assert.False(t, locked)
	lease.Release()
	lease.Release()
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	assert.Nil(t, writer.Acquire())
}

// serverErrorRetryRows counts the AUTO_RETRY_SERVER_ERROR items of the queue.
func serverErrorRetryRows(t *testing.T, svc *Service, agentID string) int {
	t.Helper()
	snapshot, err := svc.InputQueue.Snapshot(testutil.DeadlineContext(t), agentID)
	require.NoError(t, err)
	count := 0
	for _, item := range snapshot.Items {
		if item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR {
			count++
		}
	}
	return count
}

// messageRows counts the transcript rows of the agent in the database.
func messageRows(t *testing.T, svc *Service, agentID string) int {
	t.Helper()
	var count int
	require.NoError(t, svc.DB.QueryRowContext(testutil.DeadlineContext(t),
		`SELECT COUNT(*) FROM messages WHERE agent_id = ?`, agentID).Scan(&count))
	return count
}

// storedServerErrorDelay reads the server-error backoff that the queue stores.
func storedServerErrorDelay(t *testing.T, svc *Service, agentID string) int64 {
	t.Helper()
	var delay int64
	require.NoError(t, svc.DB.QueryRowContext(testutil.DeadlineContext(t),
		`SELECT auto_retry_server_error_delay_ms FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&delay))
	return delay
}

// A provider turn failure schedules one durable server-error retry after the
// turn-end row, through the same ordered sink that production wraps around
// each provider. Each turn end doubles the stored backoff once.
func TestAProviderTurnFailureSchedulesOneDurableRetryAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	svc, _, agentID, _ := setupBgTaskTestWithService(t)
	root, err := svc.Output.NewRootSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, err)
	sink := agent.NewModelProgressResetSink(root)
	turnEnd := agent.MessageContent{Original: []byte(`{"type":"turn_end"}`)}
	messagesBefore := messageRows(t, svc, agentID)

	require.NoError(t, sink.PersistTurnEnd(turnEnd, agent.SpanInfo{}))
	sink.ScheduleServerErrorRetry([]byte(`{"error":"503"}`))

	assert.Equal(t, messagesBefore+1, messageRows(t, svc, agentID), "the turn-end row is stored")
	assert.Equal(t, 1, serverErrorRetryRows(t, svc, agentID), "exactly one server-error retry row")
	assert.Equal(t, int64(20000), storedServerErrorDelay(t, svc, agentID), "the delay doubled once, from 10 s to 20 s")

	require.NoError(t, sink.PersistTurnEnd(turnEnd, agent.SpanInfo{}))
	sink.ScheduleServerErrorRetry([]byte(`{"error":"503"}`))

	assert.Equal(t, 1, serverErrorRetryRows(t, svc, agentID), "the second turn end replaces the row")
	assert.Equal(t, int64(40000), storedServerErrorDelay(t, svc, agentID), "the delay doubled again, to 40 s")
}

// The ordered sink holds a retry behind every transcript write that precedes
// it. While an earlier write cannot finish, neither the turn-end row nor the
// retry row exists; once it finishes, both are stored, in order.
func TestAProviderRetryWaitsForTheTurnEndThatPrecedesIt(t *testing.T) {
	t.Parallel()
	svc, _, agentID, _ := setupBgTaskTestWithService(t)
	root, err := svc.Output.NewRootSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, err)
	sink := agent.NewModelProgressResetSink(root)
	queue := agent.OutputQueueFor(sink)
	require.NotNil(t, queue)
	var released atomic.Bool
	queue.Append(agent.OutputWrite{Label: "an earlier transcript write", Run: func() error {
		if !released.Load() {
			return errors.New("the earlier write is held")
		}
		return nil
	}})
	messagesBefore := messageRows(t, svc, agentID)

	var pending *agent.PendingOutputError
	require.ErrorAs(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"turn_end"}`)}, agent.SpanInfo{}), &pending)
	sink.ScheduleServerErrorRetry([]byte(`{"error":"503"}`))

	assert.Equal(t, messagesBefore, messageRows(t, svc, agentID), "the turn-end row waits for the earlier write")
	assert.Zero(t, serverErrorRetryRows(t, svc, agentID), "the retry waits for the turn-end row")

	released.Store(true)
	require.NoError(t, queue.Flush())

	assert.Equal(t, messagesBefore+1, messageRows(t, svc, agentID))
	assert.Equal(t, 1, serverErrorRetryRows(t, svc, agentID))
	assert.Equal(t, int64(20000), storedServerErrorDelay(t, svc, agentID), "the delay doubled exactly once")
}

func TestCapturedAutoRetryOperationKeepsItsOriginalAuthority(t *testing.T) {
	t.Parallel()
	for _, transition := range []string{"active", "own idle", "new turn", "new session"} {
		t.Run(transition, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			svc, _, _ := setupTestService(t, withClock(clock))
			const agentID = "agent-1"
			seedAgent(t, svc, agentID)
			sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			svc.Output.NoteAgentProcessStarted(agentID)
			sink.UpdateSessionID("original")
			sink.SetTurnState(agent.TurnState{Active: true}, 1)
			resetAt := clock.Now().Add(time.Hour)
			operation := sink.CaptureAutoRetryOperation(2, func(writer agent.AutoRetryWriter) {
				writer.CancelServerErrorRetry("the original operation completed")
				writer.ScheduleUsageLimitRetry(resetAt, []byte(`{"source":"operation"}`))
			})
			require.NotNil(t, operation)
			switch transition {
			case "active":
			case "own idle":
				sink.SetTurnState(agent.TurnState{}, 2)
			case "new turn":
				sink.SetTurnState(agent.TurnState{Active: true}, 3)
			case "new session":
				sink.UpdateSessionID("replacement")
			}
			require.NoError(t, svc.InputQueue.ScheduleAutoRetry(t.Context(), agentID, retrySchedule(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, time.Time{})))
			before, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			require.NoError(t, operation())
			after, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			if transition == "new turn" || transition == "new session" {
				assert.Equal(t, before, after)
				return
			}
			require.Len(t, after.Items, 1)
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, after.Items[0].Kind)
			assert.Equal(t, int64(10000), storedServerErrorDelay(t, svc, agentID))
		})
	}
}
