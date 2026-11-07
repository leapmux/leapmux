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
	replacement := &codewhaleChild{agentID: testChildID, childID: "replacement", nudge: make(chan struct{}, 1), openTools: map[string]string{}}
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
	status, current := a.children.observation(replacement)
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
	childID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "final-drain", ProviderChildKey: testChildID, Title: "Finished child"})
	require.NoError(t, err)
	path := childTranscriptPath(a.workingDir, testChildID)
	require.NoError(t, os.MkdirAll(path, 0o700))
	child := &codewhaleChild{agentID: testChildID, childID: childID, spawnSpan: "final-drain", title: "Finished child", path: path, observedStatus: bgtask.StatusSucceeded, nudge: make(chan struct{}, 1), openTools: make(map[string]string)}
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
		id, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn-" + key, ProviderChildKey: key, Title: key})
		require.NoError(t, err)
		child := &codewhaleChild{agentID: key, childID: id, observedStatus: status, nudge: make(chan struct{}, 1), openTools: make(map[string]string)}
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
