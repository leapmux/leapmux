package codewhale

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodewhalePendingCancellationObservesItsLaterNativeOutcome(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, runtime, clock)
	receipt := func(status string) map[string]any {
		return map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": status}
	}
	runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": []any{receipt("running")}})
	runtime.respondJSON(http.MethodPost, routeAgentRuns+"/"+testChildID+"/cancel", 202, receipt("running"))
	poll := clock.Trap().NewTimer(childRunTimerTag)
	t.Cleanup(poll.Close)
	startChildRun(a)
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), poll)
	require.NoError(t, a.InterruptChild(testChildID, agent.StopContext{}))
	row, _ := sink.BackgroundTask(testChildID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	require.NotNil(t, a.children.lookup(testChildID))
	runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": []any{receipt("cancelled")}})
	clock.Advance(childTailInterval).MustWait(testutil.DeadlineContext(t))
	require.Eventually(t, func() bool { row, _ := sink.BackgroundTask(testChildID); return row.Status == bgtask.StatusStopped }, 30*time.Second, time.Millisecond)
	assert.Nil(t, a.children.lookup(testChildID))
}

func TestCodewhaleLateSnapshotCannotReplaceANewerExplicitChildObservation(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, runtime, clock)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	runtime.handle(http.MethodGet, routeAgentRuns, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeFakeJSON(w, 200, map[string]any{"runs": []any{map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": "completed"}}})
	})
	poll := clock.Trap().NewTimer(childRunTimerTag)
	t.Cleanup(poll.Close)
	startChildRun(a)
	select {
	case <-entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the native snapshot did not start")
	}
	a.settleChild(testChildID, bgtask.StatusPaused)
	require.Eventually(t, func() bool { row, _ := sink.BackgroundTask(testChildID); return row.Status == bgtask.StatusPaused }, 30*time.Second, time.Millisecond)
	unblock()
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), poll)
	row, _ := sink.BackgroundTask(testChildID)
	assert.Equal(t, bgtask.StatusPaused, row.Status)
	require.NotNil(t, a.children.lookup(testChildID))
}

func TestCodewhaleLateCancellationCannotNotifyAReplacementChild(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, runtime, clock)
	runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": []any{}})
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	runtime.handle(http.MethodPost, routeAgentRuns+"/"+testChildID+"/cancel", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeFakeJSON(w, 200, map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": "cancelled"})
	})
	startChildRun(a)
	done := make(chan error, 1)
	go func() { done <- a.InterruptChild(testChildID, agent.StopContext{}) }()
	select {
	case <-entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the cancellation did not start")
	}
	replacement := &codewhaleChild{agentID: testChildID, childID: "replacement", nudge: make(chan struct{}, 1)}
	a.children.mu.Lock()
	a.children.byID[testChildID] = replacement
	a.children.mu.Unlock()
	unblock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the cancellation did not return")
	}
	status, _, current := a.children.observation(replacement)
	assert.True(t, current)
	assert.Zero(t, status)
	row, _ := sink.BackgroundTask(testChildID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestCodewhaleStopCancelsTheSharedChildSnapshotRequest(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWith(t, runtime, testAgentOptions{clock: clock, apiTimeout: time.Hour})
	entered := blockUntilCancelled(t, runtime, http.MethodGet, routeAgentRuns)
	startChildRun(a)
	select {
	case <-entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the native snapshot did not start")
	}
	stopWithin(t, testutil.DeadlineContext(t), a)
	row, found := sink.BackgroundTask(testChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

type codewhaleReportWriteBarrier struct {
	agent.ProviderServices
	released          atomic.Bool
	sealed            atomic.Bool
	admittedAfterSeal atomic.Int32
	notify            sync.Once
	attempted         chan struct{}
}

func (sink *codewhaleReportWriteBarrier) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if source != leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
		return sink.ProviderServices.PersistNotification(source, content)
	}
	sink.notify.Do(func() { close(sink.attempted) })
	if !sink.released.Load() {
		return false, errors.New("the report write is unavailable")
	}
	if sink.sealed.Load() {
		if !agent.IsTranscriptWriteExecuting(content) {
			return false, errors.New("fresh report admission is closed")
		}
		sink.admittedAfterSeal.Add(1)
	}
	return sink.ProviderServices.PersistNotification(source, content)
}

func TestCodewhaleChildReportRetryPreservesItsAcceptedOutputAdmission(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	var barrier *codewhaleReportWriteBarrier
	a, sink := newTestAgentWith(t, runtime, testAgentOptions{clock: clock, services: func(services agent.ProviderServices) agent.ProviderServices {
		barrier = &codewhaleReportWriteBarrier{ProviderServices: services, attempted: make(chan struct{})}
		return barrier
	}})
	queue := a.sink.OutputQueue()
	appendTranscript(t, childTranscriptPath(a.workingDir, testChildID), transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "The accepted child report."}))
	startChildRun(a)
	a.settleChild(testChildID, bgtask.StatusSucceeded)
	select {
	case <-barrier.attempted:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the report write was not attempted")
	}
	// The failed report already owns its ticket. Finish child goroutines before the explicit retry.
	// Automatic retry scheduling is covered separately from this admission check.
	a.children.stopAll()
	require.True(t, queue.HasPending())
	barrier.sealed.Store(true)
	barrier.released.Store(true)
	require.NoError(t, queue.Flush())
	assert.False(t, queue.HasPending())
	assert.GreaterOrEqual(t, int(barrier.admittedAfterSeal.Load()), 1)
	require.Len(t, sink.LeapMuxNotifications(), 1)
}

func TestCodewhaleStopRetainsAFailedChildRegistryClose(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	var barrier *codewhaleChildCloseBarrier
	a, sink := newTestAgentWith(t, runtime, testAgentOptions{clock: clock, services: func(services agent.ProviderServices) agent.ProviderServices {
		barrier = &codewhaleChildCloseBarrier{ProviderServices: services, failed: make(chan struct{})}
		return barrier
	}})
	queue := a.sink.OutputQueue()
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	retry := clock.Trap().NewTimer(agent.OutputRetryTimerTag)
	t.Cleanup(retry.Close)
	startChildRun(a)
	a.Stop()
	require.True(t, queue.HasPending(), "shutdown must retain the failed registry close before releasing the child")
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), retry)
	barrier.released.Store(true)
	clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
	require.Eventually(t, func() bool { return !queue.HasPending() }, 30*time.Second, time.Millisecond)
	row, _ := sink.BackgroundTask(testChildID)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.Empty(t, sink.LeapMuxNotifications(), "shutdown must not invent a completed child report")
}

func TestCodewhaleFinalTranscriptRetryDoesNotPollNativeRuns(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, runtime, clock)
	childID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "final-drain", ProviderChildKey: testChildID, Title: "Finished child"})
	require.NoError(t, err)
	path := childTranscriptPath(a.workingDir, testChildID)
	require.NoError(t, os.MkdirAll(path, 0o700))
	child := &codewhaleChild{agentID: testChildID, childID: childID, spawnSpan: "final-drain", title: "Finished child", path: path, observedStatus: bgtask.StatusSucceeded, nudge: make(chan struct{}, 1)}
	retry := clock.Trap().NewTimer(childTimerTag)
	t.Cleanup(retry.Close)
	require.True(t, a.children.start(child, a.watchChild))
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), retry)
	var requests atomic.Int32
	runtime.handle(http.MethodGet, routeAgentRuns, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFakeJSON(w, 200, map[string]any{"runs": []any{}})
	})
	a.startChildRunPoller()
	require.Eventually(t, func() bool {
		a.children.mu.Lock()
		polling := a.children.polling
		a.children.mu.Unlock()
		return !polling || requests.Load() > 0
	}, 30*time.Second, time.Millisecond)
	require.Zero(t, requests.Load(), "a known final outcome needs no further workspace collection")
	require.Same(t, child, a.children.lookup(testChildID), "the watcher must retain the unreadable transcript")
	require.NoError(t, os.Remove(path))
	appendTranscript(t, path, transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "The recovered final answer."}))
	clock.Advance(childTailInterval).MustWait(testutil.DeadlineContext(t))
	require.Eventually(t, func() bool {
		row, found := sink.BackgroundTask(testChildID)
		return found && row.Status == bgtask.StatusSucceeded
	}, 30*time.Second, time.Millisecond)
	assert.Nil(t, a.children.lookup(testChildID))
	require.Len(t, sink.Child(childID).Messages(), 1)
	assert.Contains(t, string(sink.Child(childID).Messages()[0].Content), "The recovered final answer.")
	assert.Zero(t, requests.Load())
}

func TestCodewhaleFinalOnlyPollingStopsAndRestartsForPausedChildren(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, _ := newTestAgentWithClock(t, runtime, clock)
	add := func(key string, status bgtask.Status) *codewhaleChild {
		id, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{
			RegistryRole: bgtask.RoleChildTask,
			SpawnSpanID:  "spawn-" + key, ProviderChildKey: key, Title: key})
		require.NoError(t, err)
		child := &codewhaleChild{agentID: key, childID: id, observedStatus: status, nudge: make(chan struct{}, 1)}
		require.True(t, a.children.start(child, func(ctx context.Context, _ *codewhaleChild) { <-ctx.Done() }))
		return child
	}
	add("finished", bgtask.StatusSucceeded)
	var requests atomic.Int32
	runtime.handle(http.MethodGet, routeAgentRuns, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFakeJSON(w, 200, map[string]any{"runs": []any{}})
	})
	a.startChildRunPoller()
	require.Eventually(t, func() bool {
		a.children.mu.Lock()
		polling := a.children.polling
		a.children.mu.Unlock()
		return !polling || requests.Load() > 0
	}, 30*time.Second, time.Millisecond)
	require.Zero(t, requests.Load())
	poll := clock.Trap().NewTimer(childRunTimerTag)
	t.Cleanup(poll.Close)
	paused := add("paused", bgtask.StatusPaused)
	a.startChildRunPoller()
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), poll)
	assert.EqualValues(t, 1, requests.Load(), "paused children still need native observations")
	a.children.observe(paused, bgtask.StatusSucceeded)
	clock.Advance(childTailInterval).MustWait(testutil.DeadlineContext(t))
	require.Eventually(t, func() bool { a.children.mu.Lock(); defer a.children.mu.Unlock(); return !a.children.polling }, 30*time.Second, time.Millisecond)
	add("new-child", bgtask.StatusRunning)
	a.startChildRunPoller()
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), poll)
	assert.EqualValues(t, 2, requests.Load(), "new admission restarts the stopped poller")
}

func TestCodewhalePrunedChildClosesAsUnknownOutcome(t *testing.T) {
	t.Parallel()
	t.Run("seen child pruned from agent-runs closes", func(t *testing.T) {
		t.Parallel()
		runtime := newFakeRuntime(t)
		clock := testutil.NewQuartzMock(t)
		a, sink := newTestAgentWithClock(t, runtime, clock)
		runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{
			"runs": []any{map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": "running"}},
		})
		poll := clock.Trap().NewTimer(childRunTimerTag)
		t.Cleanup(poll.Close)
		ctx := testutil.DeadlineContext(t)
		startChildRun(a)
		testutil.WaitForTimer(t, ctx, poll)

		// Child disappears from /v1/agent-runs on next snapshot.
		runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": []any{}})
		clock.Advance(childTailInterval).MustWait(ctx)
		require.Eventually(t, func() bool {
			row, ok := sink.BackgroundTask(testChildID)
			return ok && row.Status == bgtask.StatusEndedWithUnknownOutcome
		}, 30*time.Second, 5*time.Millisecond)
		assert.Nil(t, a.children.lookup(testChildID))
		testutil.WaitForTimer(t, ctx, poll)
		clock.Advance(childTailInterval).MustWait(ctx)
		require.Eventually(t, func() bool {
			a.children.mu.Lock()
			defer a.children.mu.Unlock()
			return !a.children.polling
		}, 30*time.Second, 5*time.Millisecond)
	})

	t.Run("never-seen child absent for three consecutive snapshots closes", func(t *testing.T) {
		t.Parallel()
		runtime := newFakeRuntime(t)
		clock := testutil.NewQuartzMock(t)
		a, sink := newTestAgentWithClock(t, runtime, clock)
		runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": []any{}})
		poll := clock.Trap().NewTimer(childRunTimerTag)
		t.Cleanup(poll.Close)
		ctx := testutil.DeadlineContext(t)
		startChildRun(a)

		// Snapshot 1 (initial): 1 absence -> still running.
		testutil.WaitForTimer(t, ctx, poll)
		row, _ := sink.BackgroundTask(testChildID)
		assert.Equal(t, bgtask.StatusRunning, row.Status)

		// Snapshot 2: 2 absences -> still running.
		clock.Advance(childTailInterval).MustWait(ctx)
		testutil.WaitForTimer(t, ctx, poll)
		row, _ = sink.BackgroundTask(testChildID)
		assert.Equal(t, bgtask.StatusRunning, row.Status)

		// Snapshot 3: 3 consecutive absences -> closes as StatusEndedWithUnknownOutcome.
		clock.Advance(childTailInterval).MustWait(ctx)
		require.Eventually(t, func() bool {
			row, ok := sink.BackgroundTask(testChildID)
			return ok && row.Status == bgtask.StatusEndedWithUnknownOutcome
		}, 30*time.Second, 5*time.Millisecond)
	})
}

func TestCodewhalePausedChildParksAndWakesCleanly(t *testing.T) {
	t.Parallel()
	for _, wakeBy := range []string{"followup", "steer", "wait_settled", "wait_unsettled_target", "interrupt_pending"} {
		t.Run(wakeBy, func(t *testing.T) {
			t.Parallel()
			runtime := newFakeRuntime(t)
			clock := testutil.NewQuartzMock(t)
			a, sink := newTestAgentWithClock(t, runtime, clock)
			var runRequests atomic.Int32
			var statusMu sync.Mutex
			currentStatus := "interrupted"
			runtime.handle(http.MethodGet, routeAgentRuns, func(w http.ResponseWriter, _ *http.Request) {
				runRequests.Add(1)
				statusMu.Lock()
				st := currentStatus
				statusMu.Unlock()
				writeFakeJSON(w, 200, map[string]any{
					"runs": []any{map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": st}},
				})
			})
			runtime.respondJSON(http.MethodPost, routeAgentRuns+"/"+testChildID+"/cancel", 202,
				map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": "running"})

			poll := clock.Trap().NewTimer(childRunTimerTag)
			t.Cleanup(poll.Close)
			ctx := testutil.DeadlineContext(t)
			startChildRun(a)

			// First snapshot reports interrupted -> child pauses and parks.
			testutil.WaitForTimer(t, ctx, poll)
			require.Eventually(t, func() bool {
				row, ok := sink.BackgroundTask(testChildID)
				return ok && row.Status == bgtask.StatusPaused
			}, 30*time.Second, 5*time.Millisecond)

			// Advance once so pollChildRuns re-checks snapshots, sees only paused child, and exits.
			clock.Advance(childTailInterval).MustWait(ctx)
			require.Eventually(t, func() bool {
				a.children.mu.Lock()
				defer a.children.mu.Unlock()
				return !a.children.polling
			}, 30*time.Second, 5*time.Millisecond)
			requestsWhilePaused := runRequests.Load()
			// Advancing further while paused produces zero additional run polls.
			clock.Advance(5 * childTailInterval).MustWait(ctx)
			assert.Equal(t, requestsWhilePaused, runRequests.Load(), "zero timer polls while paused")

			// Switch runtime to completed before waking.
			statusMu.Lock()
			currentStatus = "completed"
			statusMu.Unlock()

			switch wakeBy {
			case "followup", "steer":
				a.HandleOutput(toolEndEvent(30, "item.completed", "item_wake", "call_wake", "agent",
					"resumed", map[string]any{"action": wakeBy, "agent_id": testChildID, "prompt": "continue"}, nil))
			case "wait_settled":
				a.HandleOutput(toolEndEvent(30, "item.completed", "item_wake", "call_wake", "agent",
					`{"settled":[{"agent_id":"`+testChildID+`","status":"running"}]}`, map[string]any{"action": "wait"}, nil))
			case "wait_unsettled_target":
				a.HandleOutput(toolEndEvent(30, "item.completed", "item_wake", "call_wake", "agent",
					`{"settled":[]}`, map[string]any{"action": "wait", "agent_id": testChildID}, nil))
			case "interrupt_pending":
				require.NoError(t, a.InterruptChild(testChildID, agent.StopContext{}))
			}

			// Poller restarts and observes completed status -> closes row.
			require.Eventually(t, func() bool {
				row, ok := sink.BackgroundTask(testChildID)
				return ok && row.Status == bgtask.StatusSucceeded
			}, 30*time.Second, 5*time.Millisecond)
			assert.Nil(t, a.children.lookup(testChildID))
		})
	}

	t.Run("wake during pause status publication does not park", func(t *testing.T) {
		t.Parallel()
		runtime := newFakeRuntime(t)
		clock := testutil.NewQuartzMock(t)
		pauseEntered := make(chan struct{})
		pauseRelease := make(chan struct{})
		var pauseOnce sync.Once
		a, sink := newTestAgentWith(t, runtime, testAgentOptions{clock: clock, services: func(inner agent.ProviderServices) agent.ProviderServices {
			return &pauseGateServices{
				ProviderServices: inner,
				onPaused: func() {
					pauseOnce.Do(func() {
						close(pauseEntered)
						<-pauseRelease
					})
				},
			}
		}})
		t.Cleanup(func() {
			select {
			case <-pauseRelease:
			default:
				close(pauseRelease)
			}
		})
		runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{
			"runs": []any{map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": "running"}},
		})
		startChildRun(a)

		// Deliver StatusPaused and block watchChild inside UpdateBackgroundTaskStatus
		// (between observation and parkPausedChild).
		a.settleChild(testChildID, bgtask.StatusPaused)
		select {
		case <-pauseEntered:
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("watchChild did not publish StatusPaused")
		}

		// Concurrently wake with StatusSucceeded before watchChild reaches parkPausedChild.
		a.settleChild(testChildID, bgtask.StatusSucceeded)
		close(pauseRelease)

		require.Eventually(t, func() bool {
			row, ok := sink.BackgroundTask(testChildID)
			return ok && row.Status == bgtask.StatusSucceeded
		}, 30*time.Second, 5*time.Millisecond)
		assert.Nil(t, a.children.lookup(testChildID))
	})

	t.Run("in-flight poll snapshot wakes paused child on non-paused status and ignores paused", func(t *testing.T) {
		t.Parallel()
		runtime := newFakeRuntime(t)
		clock := testutil.NewQuartzMock(t)
		entered := make(chan struct{})
		release := make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		runtime.handle(http.MethodGet, routeAgentRuns, func(w http.ResponseWriter, r *http.Request) {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			writeFakeJSON(w, 200, map[string]any{
				"runs": []any{map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": "completed"}},
			})
		})
		a, sink := newTestAgentWithClock(t, runtime, clock)
		startChildRun(a)
		select {
		case <-entered:
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("in-flight poll did not start")
		}
		// Simulate child parking with the same observationSequence as the in-flight snapshot.
		child := a.children.lookup(testChildID)
		require.NotNil(t, child)
		a.children.mu.Lock()
		child.paused = true
		a.children.mu.Unlock()
		releaseOnce.Do(func() { close(release) })

		require.Eventually(t, func() bool {
			row, ok := sink.BackgroundTask(testChildID)
			return ok && row.Status == bgtask.StatusSucceeded
		}, 30*time.Second, 5*time.Millisecond)
		assert.Nil(t, a.children.lookup(testChildID))
	})
}

type pauseGateServices struct {
	agent.ProviderServices
	onPaused func()
}

func (s *pauseGateServices) UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error {
	err := s.ProviderServices.UpdateBackgroundTaskStatus(rowKey, status, activeForm)
	if status == bgtask.StatusPaused && s.onPaused != nil {
		s.onPaused()
	}
	return err
}

func TestCodewhaleFailedOrCanceledWaitDoesNotWakePausedChild(t *testing.T) {
	t.Parallel()
	for _, eventName := range []string{contracts.CodewhaleEventItemFailed, contracts.CodewhaleEventItemCanceled} {
		t.Run(eventName, func(t *testing.T) {
			t.Parallel()
			runtime := newFakeRuntime(t)
			clock := testutil.NewQuartzMock(t)
			a, sink := newTestAgentWithClock(t, runtime, clock)
			var runRequests atomic.Int32
			runtime.handle(http.MethodGet, routeAgentRuns, func(w http.ResponseWriter, _ *http.Request) {
				runRequests.Add(1)
				writeFakeJSON(w, 200, map[string]any{
					"runs": []any{map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": "interrupted"}},
				})
			})
			poll := clock.Trap().NewTimer(childRunTimerTag)
			t.Cleanup(poll.Close)
			ctx := testutil.DeadlineContext(t)
			startChildRun(a)

			testutil.WaitForTimer(t, ctx, poll)
			require.Eventually(t, func() bool {
				row, ok := sink.BackgroundTask(testChildID)
				return ok && row.Status == bgtask.StatusPaused
			}, 30*time.Second, 5*time.Millisecond)

			clock.Advance(childTailInterval).MustWait(ctx)
			require.Eventually(t, func() bool {
				a.children.mu.Lock()
				defer a.children.mu.Unlock()
				return !a.children.polling
			}, 30*time.Second, 5*time.Millisecond)

			child := a.children.lookup(testChildID)
			require.NotNil(t, child)
			require.Eventually(t, func() bool {
				a.children.mu.Lock()
				defer a.children.mu.Unlock()
				return child.paused
			}, 30*time.Second, 5*time.Millisecond)

			requestsBefore := runRequests.Load()
			a.HandleOutput(toolEndEvent(30, eventName, "item_wait_err", "call_wait_err", contracts.CodewhaleToolAgent,
				"wait timed out", map[string]any{"action": "wait", "agent_id": testChildID}, nil))

			a.children.mu.Lock()
			paused := child.paused
			polling := a.children.polling
			a.children.mu.Unlock()
			assert.True(t, paused, "a failed or canceled wait must not wake a parked paused child")
			assert.False(t, polling, "a failed or canceled wait must not restart the run poller")
			assert.Equal(t, requestsBefore, runRequests.Load())
			row, ok := sink.BackgroundTask(testChildID)
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusPaused, row.Status)
		})
	}
}
