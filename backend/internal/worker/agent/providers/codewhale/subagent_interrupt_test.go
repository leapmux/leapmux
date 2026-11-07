package codewhale

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodewhaleInterruptChildUsesTheNativeRunOutcome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, receipt string
		response      int
		status        bgtask.Status
	}{
		{"cancelled", "cancelled", 200, bgtask.StatusStopped},
		{"already completed", "completed", 200, bgtask.StatusSucceeded},
		{"already failed", "failed", 200, bgtask.StatusFailed},
		{"pending cancellation", "running", 202, bgtask.StatusRunning},
		{"resumable interruption", "interrupted", 200, bgtask.StatusPaused},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime := newFakeRuntime(t)
			clock := testutil.NewQuartzMock(t)
			a, sink := newTestAgentWithClock(t, runtime, clock)
			interrupter, ok := any(a).(agent.ChildInterrupter)
			require.True(t, ok, "the current native runtime supports child cancellation")
			var cancelled atomic.Bool
			route := routeAgentRuns + "/" + testChildID
			runtime.handle(http.MethodGet, routeAgentRuns, func(w http.ResponseWriter, _ *http.Request) {
				status := "running"
				if cancelled.Load() {
					status = test.receipt
				}
				writeFakeJSON(w, 200, map[string]any{"runs": []map[string]any{{"spec": map[string]string{"worker_id": testChildID}, "status": status}}})
			})
			runtime.handle(http.MethodPost, route+"/cancel", func(w http.ResponseWriter, _ *http.Request) {
				cancelled.Store(true)
				writeFakeJSON(w, test.response, map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": test.receipt})
			})
			a.turnID = "parent-turn"
			startChildRun(a)
			require.NoError(t, interrupter.InterruptChild(testChildID, agent.StopContext{}))
			require.Eventually(t, func() bool { row, found := sink.BackgroundTask(testChildID); return found && row.Status == test.status }, 30*time.Second, time.Millisecond)
			assert.Len(t, runtime.requestsTo(http.MethodPost, route+"/cancel"), 1)
			assert.Equal(t, "parent-turn", a.turnID)
		})
	}
}

func TestCodewhaleInterruptChildRejectsMissingRoutesAndBadReceipts(t *testing.T) {
	t.Parallel()
	for _, response := range []int{404, 409, 500, 200} {
		t.Run(http.StatusText(response), func(t *testing.T) {
			t.Parallel()
			runtime := newFakeRuntime(t)
			a, sink := newTestAgent(t, runtime)
			interrupter, ok := any(a).(agent.ChildInterrupter)
			require.True(t, ok)
			runtime.respondJSON(http.MethodPost, routeAgentRuns+"/"+testChildID+"/cancel", response, map[string]any{})
			startChildRun(a)
			require.Error(t, interrupter.InterruptChild(testChildID, agent.StopContext{}))
			row, found := sink.BackgroundTask(testChildID)
			require.True(t, found)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			require.Error(t, interrupter.InterruptChild("", agent.StopContext{}))
			require.Error(t, interrupter.InterruptChild("unknown-child", agent.StopContext{}))
		})
	}
}

func TestCodewhaleChildDrainsItsFinalTranscriptBeforeClosing(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, runtime, clock)
	runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": []map[string]any{{"spec": map[string]string{"worker_id": testChildID}, "status": "completed"}}})
	path := childTranscriptPath(a.workingDir, testChildID)
	appendTranscript(t, path, transcriptLine(t, 0, "user", map[string]any{"type": "text", "text": strings.Repeat("x", 9<<20)})+transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "The final native answer."}))
	startChildRun(a)
	require.Eventually(t, func() bool { row, _ := sink.BackgroundTask(testChildID); return row.Status == bgtask.StatusSucceeded }, 30*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return !a.sink.OutputQueue().HasPending() }, 30*time.Second, time.Millisecond)
	messages := sink.Child("child-of-call_spawn").Messages()
	require.Len(t, messages, 2, "the opening prompt and final native answer must persist before cleanup")
	assert.Contains(t, string(messages[1].Content), "The final native answer.")
	require.Len(t, sink.LeapMuxNotifications(), 1)
}

func TestCodewhaleCancelReceiptClosesTheChildWhenLaterReadsFail(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, runtime, clock)
	route := routeAgentRuns + "/" + testChildID
	runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": []map[string]any{{"spec": map[string]string{"worker_id": testChildID}, "status": "running"}}})
	startChildRun(a)
	runtime.respondStatus(http.MethodGet, routeAgentRuns, 500, "read unavailable")
	runtime.respondJSON(http.MethodPost, route+"/cancel", 200, map[string]any{"spec": map[string]string{"worker_id": testChildID}, "status": "cancelled"})
	require.NoError(t, a.InterruptChild(testChildID, agent.StopContext{}))
	require.Eventually(t, func() bool { row, _ := sink.BackgroundTask(testChildID); return row.Status == bgtask.StatusStopped }, 30*time.Second, time.Millisecond)
	assert.Empty(t, a.children.lookup(testChildID))
}

type codewhaleChildCloseBarrier struct {
	agent.ProviderServices
	attempts atomic.Int32
	released atomic.Bool
	notify   sync.Once
	failed   chan struct{}
}

func (sink *codewhaleChildCloseBarrier) CloseBackgroundTask(key string, status bgtask.Status) error {
	sink.attempts.Add(1)
	if !sink.released.Load() {
		sink.notify.Do(func() { close(sink.failed) })
		return errors.New("the registry write failed")
	}
	return sink.ProviderServices.CloseBackgroundTask(key, status)
}

func TestCodewhaleChildRetriesItsFinalWriteWithoutAnotherNativeEvent(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	var rejecting *codewhaleChildCloseBarrier
	a, sink := newTestAgentWith(t, runtime, testAgentOptions{clock: clock, services: func(services agent.ProviderServices) agent.ProviderServices {
		rejecting = &codewhaleChildCloseBarrier{ProviderServices: services, failed: make(chan struct{})}
		return rejecting
	}})
	queue := a.sink.OutputQueue()
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	retry := clock.Trap().NewTimer(agent.OutputRetryTimerTag)
	t.Cleanup(retry.Close)
	path := childTranscriptPath(a.workingDir, testChildID)
	appendTranscript(t, path, transcriptLine(t, 1, "assistant", map[string]any{"type": "text", "text": "A durable child report."}))
	startChildRun(a)
	a.settleChild(testChildID, bgtask.StatusSucceeded)
	select {
	case <-rejecting.failed:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the child attempted no final registry write")
	}
	require.True(t, queue.HasPending(), "the failed close must retain its cleanup after the report persists")
	require.Len(t, sink.LeapMuxNotifications(), 1, "the child report must already exist when registry close fails")
	remaining := testutil.WaitForTimer(t, testutil.DeadlineContext(t), retry)
	rejecting.released.Store(true)
	// A shared child poll timer can precede the retained output retry.
	for remaining > 0 {
		step, waiter := clock.AdvanceNext()
		require.Positive(t, step)
		require.LessOrEqual(t, step, remaining)
		waiter.MustWait(testutil.DeadlineContext(t))
		remaining -= step
	}
	require.Eventually(t, func() bool { return !queue.HasPending() }, 30*time.Second, time.Millisecond)
	row, _ := sink.BackgroundTask(testChildID)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.GreaterOrEqual(t, int(rejecting.attempts.Load()), 2)
}

func TestCodewhaleChildrenShareOneNativeRunSnapshot(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	clock := testutil.NewQuartzMock(t)
	a, sink := newTestAgentWithClock(t, runtime, clock)
	requests := make(chan string, 4)
	ids := []string{"worker-a", "worker-b"}
	records := []map[string]any{}
	for _, id := range ids {
		records = append(records, map[string]any{"spec": map[string]string{"worker_id": id}, "status": "running"})
		runtime.handle(http.MethodGet, routeAgentRuns+"/"+id, func(w http.ResponseWriter, _ *http.Request) {
			requests <- id
			writeFakeJSON(w, 200, map[string]any{"spec": map[string]string{"worker_id": id}, "status": "running"})
		})
	}
	runtime.handle(http.MethodGet, routeAgentRuns, func(w http.ResponseWriter, _ *http.Request) {
		requests <- "list"
		writeFakeJSON(w, 200, map[string]any{"runs": records})
	})
	poll := clock.Trap().NewTimer(childRunTimerTag)
	t.Cleanup(poll.Close)
	for _, id := range ids {
		a.startChild(id, "spawn-"+id, id, "The actual child task.")
	}
	observed := 0
	for observed < 2 {
		select {
		case request := <-requests:
			if request == "list" {
				observed = 2
			} else {
				observed++
			}
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("the runtime received no child status request")
		}
	}
	require.Len(t, runtime.requestsTo(http.MethodGet, routeAgentRuns), 1, "all children must share one native workspace scan")
	for _, id := range ids {
		assert.Empty(t, runtime.requestsTo(http.MethodGet, routeAgentRuns+"/"+id))
	}
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), poll)
	completed := []map[string]any{}
	for _, id := range ids {
		completed = append(completed, map[string]any{"spec": map[string]string{"worker_id": id}, "status": "completed"})
	}
	runtime.respondJSON(http.MethodGet, routeAgentRuns, 200, map[string]any{"runs": completed})
	clock.Advance(childTailInterval).MustWait(testutil.DeadlineContext(t))
	require.Eventually(t, func() bool {
		for _, id := range ids {
			row, _ := sink.BackgroundTask(id)
			if row.Status != bgtask.StatusSucceeded {
				return false
			}
		}
		return true
	}, 30*time.Second, time.Millisecond)
	assert.Len(t, runtime.requestsTo(http.MethodGet, routeAgentRuns), 2)
}
