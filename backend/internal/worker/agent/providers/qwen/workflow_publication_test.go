package qwen

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const qwenWorkflowPublicationFinalEntry = `{"kind":"workflow","id":"wf_1","toolUseId":"call_wf","label":"final triage",` +
	`"status":"completed","currentPhase":"All native assignments ended","dispatches":[` +
	`{"id":"dispatch-1","label":"First unit final title","prompt":"The final native assignment.","status":"completed"},` +
	`{"id":"dispatch-2","label":"Second native unit","prompt":"Second task.","status":"failed"},` +
	`{"id":"dispatch-3","label":"Only the final snapshot reports this unit","prompt":"The last native assignment.","status":"completed"}]}`

// qwenWorkflowPublicationSink refuses one final registry operation before storage.
// The inherited output queue remains the real root queue used by transcript writes.
type qwenWorkflowPublicationSink struct {
	*agenttest.ControlSink
	failure string
	blocked atomic.Bool

	rejections            atomic.Int64
	acceptedFinalUpserts  atomic.Int64
	acceptedFinalClosures atomic.Int64
}

type qwenWorkflowAdmissionSink struct {
	*agenttest.ControlSink
	available atomic.Bool
}

func (sink *qwenWorkflowAdmissionSink) OutputQueue() *agent.OutputQueue {
	if !sink.available.Load() {
		return nil
	}
	return sink.ControlSink.OutputQueue()
}

func (sink *qwenWorkflowPublicationSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	target := task.RowKey == qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", "dispatch-1") && task.Status.IsFinished()
	if target && sink.failure == "upsert" && sink.blocked.Load() {
		sink.rejections.Add(1)
		return errors.New("the final workflow upsert storage is unavailable")
	}
	err := sink.ControlSink.UpsertBackgroundTask(task)
	if err == nil && target {
		sink.acceptedFinalUpserts.Add(1)
	}
	return err
}

func (sink *qwenWorkflowPublicationSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	target := rowKey == qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", "dispatch-1") && status.IsFinished()
	if target && sink.failure == "close" && sink.blocked.Load() {
		sink.rejections.Add(1)
		return errors.New("the final workflow close storage is unavailable")
	}
	err := sink.ControlSink.CloseBackgroundTask(rowKey, status)
	if err == nil && target {
		sink.acceptedFinalClosures.Add(1)
	}
	return err
}

func TestQwenWorkflowFinalPublicationRecoversAfterItsNativeReaderRetires(t *testing.T) {
	for _, failure := range []string{"upsert", "close"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			clock := testutil.NewQuartzMock(t)
			var reads atomic.Int64
			a, recorded, _ := newQwenAgent(t, clock, func(request agenttest.RecordedRequest) agenttest.RPCReply {
				entry := qwenWorkflowRunningEntry
				if request.Method == qwenTaskListMethod && reads.Add(1) > 1 {
					entry = qwenWorkflowPublicationFinalEntry
				}
				return qwenWorkflowTaskList(entry)(request)
			})
			sink := &qwenWorkflowPublicationSink{ControlSink: recorded, failure: failure}
			services := agent.NewProviderServices(sink)
			queue := agent.OutputQueueFor(services)
			require.Same(t, recorded.OutputQueue(), queue, "registry retries must use the actual root output queue")
			queue.ConfigureRetry(t.Context(), clock, nil)
			t.Cleanup(queue.StopRetries)
			created, stopped := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
			a.SetSinkForTest(agent.NewOrderedProviderServices(queue, services))

			a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
			a.stateMu.Lock()
			poller := a.children.workflows["call_wf"]
			a.stateMu.Unlock()
			require.NotNil(t, poller)
			clock.Advance(workflowPollInterval).MustWait(ctx)
			testutil.RequireEventually(t, func() bool {
				poller.mu.Lock()
				defer poller.mu.Unlock()
				return poller.rows["dispatch-1"] == bgtask.StatusRunning &&
					poller.rows["dispatch-2"] == bgtask.StatusSucceeded
			})
			waitForQwenRegistryStorage(t, a)
			initial, found := recorded.BackgroundTask(qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", "dispatch-1"))
			require.True(t, found)
			require.Equal(t, "First native unit", initial.Title)
			require.Equal(t, int64(1), reads.Load())
			require.False(t, queue.HasPending())

			sink.blocked.Store(true)
			a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_wf","status":"completed","_meta":{"toolName":"workflow"}}`))
			select {
			case <-poller.stopped:
			case <-ctx.Done():
				t.Fatal("the native workflow reader did not retire after its final snapshot")
			}
			testutil.RequireEventually(t, func() bool {
				a.stateMu.Lock()
				defer a.stateMu.Unlock()
				return len(a.children.workflows) == 0 && a.workflowReader == nil && sink.rejections.Load() > 0
			})
			require.Equal(t, int64(2), reads.Load(), "the native final frame requires exactly one final read")
			require.True(t, queue.HasPending(), "the root queue must retain a final registry refusal after the native reader retires")
			if failure == "upsert" {
				interim, found := recorded.BackgroundTask(qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", "dispatch-1"))
				require.True(t, found)
				assert.Equal(t, initial.Title, interim.Title, "a successful Close cannot recover the refused final metadata")
				assert.Zero(t, sink.acceptedFinalUpserts.Load())
			} else {
				assert.Equal(t, int64(1), sink.acceptedFinalUpserts.Load(), "the final upsert succeeded before its close failed")
				assert.Zero(t, sink.acceptedFinalClosures.Load())
			}

			delay := testutil.WaitForTimer(t, ctx, created)
			sink.blocked.Store(false)
			testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
			queue.StopRetries()
			require.False(t, queue.HasPending(), "storage recovery must finish without another native frame")
			assert.Equal(t, int64(2), reads.Load(), "storage retry must not restart the native workflow reader")
			assert.Equal(t, int64(1), sink.acceptedFinalUpserts.Load(), "a close retry must not repeat the accepted final upsert")
			assert.Equal(t, int64(1), sink.acceptedFinalClosures.Load())

			first, found := recorded.BackgroundTask(qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", "dispatch-1"))
			require.True(t, found)
			assert.Equal(t, bgtask.StatusSucceeded, first.Status)
			assert.Equal(t, "First unit final title", first.Title)
			assert.Equal(t, "The final native assignment.", first.ActiveForm)
			assert.Equal(t, "final triage", first.GroupLabel)
			assert.False(t, first.EndedAt.IsZero())
			second, found := recorded.BackgroundTask(qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", "dispatch-2"))
			require.True(t, found)
			assert.Equal(t, bgtask.StatusFailed, second.Status, "the dispatch keeps its native outcome rather than the run's success")
			last, found := recorded.BackgroundTask(qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", "dispatch-3"))
			require.True(t, found, "the last native snapshot is the only source of this dispatch")
			assert.Equal(t, bgtask.StatusSucceeded, last.Status)
			assert.Equal(t, "Only the final snapshot reports this unit", last.Title)
			assert.Equal(t, "The last native assignment.", last.ActiveForm)
			assert.False(t, last.EndedAt.IsZero())
		})
	}
}

func TestQwenWorkflowRetainsItsConsumedFinalStatusUntilPublicationAdmission(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	var reads atomic.Int64
	a, recorded, _ := newQwenAgent(t, clock, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == qwenTaskListMethod {
			reads.Add(1)
		}
		return qwenWorkflowTaskList(qwenWorkflowPublicationFinalEntry)(request)
	})
	sink := &qwenWorkflowAdmissionSink{ControlSink: recorded}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
	a.stateMu.Lock()
	poller := a.children.workflows["call_wf"]
	a.stateMu.Unlock()
	require.NotNil(t, poller)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_wf","status":"completed","_meta":{"toolName":"workflow"}}`))
	testutil.RequireEventually(t, func() bool {
		poller.mu.Lock()
		defer poller.mu.Unlock()
		return reads.Load() >= 1 && poller.pendingFinal == bgtask.StatusSucceeded && len(poller.rows) == 0
	})
	select {
	case <-poller.stopped:
		t.Fatal("the reader discarded its final snapshot before output admission succeeded")
	default:
	}
	sink.available.Store(true)
	clock.Advance(workflowPollInterval).MustWait(ctx)
	select {
	case <-poller.stopped:
	case <-ctx.Done():
		t.Fatal("the retained final status did not complete after publication admission recovered")
	}
	testutil.RequireEventually(t, func() bool { return !recorded.OutputQueue().HasPending() })
	assert.Positive(t, reads.Load(), "the recovered publication comes from the native reader")
	last, found := recorded.BackgroundTask(qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", "dispatch-3"))
	require.True(t, found)
	assert.Equal(t, bgtask.StatusSucceeded, last.Status)
	assert.Equal(t, "Only the final snapshot reports this unit", last.Title)
}
