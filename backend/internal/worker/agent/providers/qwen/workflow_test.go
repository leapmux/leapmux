package qwen

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qwenWorkflowTaskList answers each task list with one workflow run of the
// tool call call_wf. The entry quotes the shape of the 0.26.0 probe
// (.tmp/qwen-wf-probe/tasks-final.json).
func qwenWorkflowTaskList(entry string) func(agenttest.RecordedRequest) agenttest.RPCReply {
	return func(request agenttest.RecordedRequest) agenttest.RPCReply {
		switch request.Method {
		case qwenTaskListMethod:
			return agenttest.RPCReply{Result: json.RawMessage(`{"v":1,"sessionId":"` + qwenTestSession + `","now":1,"tasks":[` + entry + `]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

const qwenWorkflowRunningEntry = `{"kind":"workflow","id":"wf_1","toolUseId":"call_wf","label":"triage",` +
	`"status":"running","currentPhase":"Two native assignments","dispatches":[` +
	`{"id":"dispatch-1","label":"First native unit","prompt":"First task.\nSecond line.","status":"running"},` +
	`{"id":"dispatch-2","label":"Second native unit","prompt":"Second task.","status":"completed"}]}`

const qwenWorkflowCompletedEntry = `{"kind":"workflow","id":"wf_1","toolUseId":"call_wf","label":"triage",` +
	`"status":"completed","currentPhase":"Two native assignments","dispatches":[` +
	`{"id":"dispatch-1","label":"First native unit","prompt":"First task.","status":"completed"},` +
	`{"id":"dispatch-2","label":"Second native unit","prompt":"Second task.","status":"failed"}]}`

const qwenWorkflowOpenFrame = `{"sessionUpdate":"tool_call","toolCallId":"call_wf","status":"pending","title":"Workflow","kind":"other","rawInput":{"name":"triage"},"_meta":{"toolName":"workflow"}}`

// One poll of a running run draws the run row's activity and one row per
// dispatch, in the run row's group.
func TestQwenWorkflowPollerDrawsDispatchRows(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTicker("qwen", "workflow")
	defer trap.Close()
	a, sink, _ := newQwenAgent(t, clock, qwenWorkflowTaskList(qwenWorkflowRunningEntry))

	// The trap holds the poller's ticker until the test releases it, and the
	// poller starts on the goroutine that reads the tool call, so that read
	// runs beside the release.
	opened := make(chan struct{})
	go func() {
		defer close(opened)
		a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
	}()
	ctx := testutil.DeadlineContext(t)
	testutil.WaitForTimer(t, ctx, trap)
	<-opened
	clock.Advance(workflowPollInterval).MustWait(ctx)
	testutil.RequireEventually(t, func() bool {
		_, ok := sink.BackgroundTask("call_wf/dispatch-2")
		return ok
	})

	run, ok := sink.BackgroundTask("call_wf")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindWorkflow, run.Kind, "the run keeps its workflow kind")
	assert.Equal(t, bgtask.StatusRunning, run.Status)
	assert.Equal(t, "Two native assignments", run.ActiveForm)

	first, ok := sink.BackgroundTask("call_wf/dispatch-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindSubagent, first.Kind)
	assert.Equal(t, "First native unit", first.Title)
	assert.Equal(t, "First task.", first.ActiveForm, "the dispatch row's secondary line comes from the prompt head")
	assert.Equal(t, "call_wf", first.GroupKey)
	assert.Equal(t, "triage", first.GroupLabel)
	assert.Equal(t, bgtask.StatusRunning, first.Status)
	assert.Empty(t, first.ChildAgentID, "a workflow agent owns no transcript")

	second, ok := sink.BackgroundTask("call_wf/dispatch-2")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, second.Status)
	assert.False(t, second.EndedAt.IsZero(), "a settled dispatch closes its row")
}

// The run's final tool-call frame stops the poller and ends every dispatch row
// that no snapshot settled, with the run's own outcome.
func TestQwenWorkflowFinalFrameClosesUnsettledDispatches(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTicker("qwen", "workflow")
	defer trap.Close()
	a, sink, _ := newQwenAgent(t, clock, qwenWorkflowTaskList(qwenWorkflowRunningEntry))

	// The trap holds the poller's ticker until the test releases it, and the
	// poller starts on the goroutine that reads the tool call, so that read
	// runs beside the release.
	opened := make(chan struct{})
	go func() {
		defer close(opened)
		a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
	}()
	ctx := testutil.DeadlineContext(t)
	testutil.WaitForTimer(t, ctx, trap)
	<-opened
	clock.Advance(workflowPollInterval).MustWait(ctx)
	testutil.RequireEventually(t, func() bool {
		_, ok := sink.BackgroundTask("call_wf/dispatch-1")
		return ok
	})

	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_wf","status":"completed","_meta":{"toolName":"workflow"}}`))
	// The owned poll loop performs the final read, so the settled row arrives
	// asynchronously after the final frame.
	testutil.RequireEventually(t, func() bool {
		first, ok := sink.BackgroundTask("call_wf/dispatch-1")
		if !ok || first.Status != bgtask.StatusSucceeded {
			return false
		}
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		return len(a.children.workflows) == 0
	})
	first, ok := sink.BackgroundTask("call_wf/dispatch-1")
	require.True(t, ok)
	assert.False(t, first.EndedAt.IsZero())
	a.stateMu.Lock()
	assert.Empty(t, a.children.workflows, "the final frame forgets the poller")
	a.stateMu.Unlock()
}

// A run whose entry reports a final status needs no further poll: the loop
// ends after the snapshot that settled it.
func TestQwenWorkflowPollerStopsAtFinalEntry(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTicker("qwen", "workflow")
	defer trap.Close()
	requests := 0
	a, sink, _ := newQwenAgent(t, clock, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == qwenTaskListMethod {
			requests++
			return agenttest.RPCReply{Result: json.RawMessage(`{"v":1,"sessionId":"` + qwenTestSession + `","now":1,"tasks":[` + qwenWorkflowCompletedEntry + `]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	// The trap holds the poller's ticker until the test releases it, and the
	// poller starts on the goroutine that reads the tool call, so that read
	// runs beside the release.
	opened := make(chan struct{})
	go func() {
		defer close(opened)
		a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
	}()
	ctx := testutil.DeadlineContext(t)
	testutil.WaitForTimer(t, ctx, trap)
	<-opened
	clock.Advance(workflowPollInterval).MustWait(ctx)
	testutil.RequireEventually(t, func() bool {
		first, ok := sink.BackgroundTask("call_wf/dispatch-1")
		return ok && first.Status == bgtask.StatusSucceeded
	})
	second, ok := sink.BackgroundTask("call_wf/dispatch-2")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, second.Status, "a dispatch keeps its own settled outcome")

	clock.Advance(workflowPollInterval).MustWait(ctx)
	assert.Equal(t, 1, requests, "a settled run polls no further")
}

// A list that states no entry of this tool call ends the poller without rows.
func TestQwenWorkflowPollerEndsOnAnAbsentEntry(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTicker("qwen", "workflow")
	defer trap.Close()
	a, sink, _ := newQwenAgent(t, clock, qwenWorkflowTaskList(`{"kind":"agent","id":"other","toolUseId":"call_9","status":"running"}`))

	// The trap holds the poller's ticker until the test releases it, and the
	// poller starts on the goroutine that reads the tool call, so that read
	// runs beside the release.
	opened := make(chan struct{})
	go func() {
		defer close(opened)
		a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
	}()
	ctx := testutil.DeadlineContext(t)
	testutil.WaitForTimer(t, ctx, trap)
	<-opened
	clock.Advance(workflowPollInterval).MustWait(ctx)
	testutil.RequireEventually(t, func() bool {
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		return len(a.children.workflows) == 0
	})
	_, ok := sink.BackgroundTask("call_wf/dispatch-1")
	assert.False(t, ok, "a run the list does not state draws no dispatch row")
}

// The status words of a run and a dispatch map onto row statuses.
func TestQwenWorkflowRowStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, bgtask.StatusSucceeded, qwenWorkflowRowStatus("completed"))
	assert.Equal(t, bgtask.StatusFailed, qwenWorkflowRowStatus("failed"))
	assert.Equal(t, bgtask.StatusStopped, qwenWorkflowRowStatus("cancelled"))
	assert.Equal(t, bgtask.StatusPending, qwenWorkflowRowStatus("queued"))
	assert.Equal(t, bgtask.StatusRunning, qwenWorkflowRowStatus("running"))
	assert.Equal(t, bgtask.StatusRunning, qwenWorkflowRowStatus("whatever-comes-next"))
}

// stopAllWorkflowPollers ends each poller without inventing outcomes.
func TestQwenStopAllWorkflowPollersLeavesRowsAtTheirState(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTicker("qwen", "workflow")
	defer trap.Close()
	a, sink, _ := newQwenAgent(t, clock, qwenWorkflowTaskList(qwenWorkflowRunningEntry))

	// The trap holds the poller's ticker until the test releases it, and the
	// poller starts on the goroutine that reads the tool call, so that read
	// runs beside the release.
	opened := make(chan struct{})
	go func() {
		defer close(opened)
		a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
	}()
	ctx := testutil.DeadlineContext(t)
	testutil.WaitForTimer(t, ctx, trap)
	<-opened
	clock.Advance(workflowPollInterval).MustWait(ctx)
	testutil.RequireEventually(t, func() bool {
		_, ok := sink.BackgroundTask("call_wf/dispatch-1")
		return ok
	})

	a.stopAllBackgroundTranscripts()
	first, ok := sink.BackgroundTask("call_wf/dispatch-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, first.Status, "a teardown states no outcome of its own")
}

var _ agent.Agent = (*Agent)(nil)

func TestQwenWorkflowPollerDoesNotReadAReplacementSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, owner, current string }{
		{"replacement", qwenTestSession, "session-2"},
		{"cleared", qwenTestSession, ""},
		{"no owner", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink, requests := newQwenAgent(t, nil, qwenWorkflowTaskList(qwenWorkflowRunningEntry))
			a.SetSessionIDForTest(tc.owner)
			poller := newWorkflowPoller(a, "call_wf", "triage")
			a.SetSessionIDForTest(tc.current)

			assert.True(t, poller.poll(), "a missing or replaced session retires its workflow reader")
			syncPeer(t, a)
			assert.Empty(t, requestsFor(requests(), qwenTaskListMethod), "the old workflow cannot query the replacement session")
			assert.Empty(t, sink.BackgroundTasks(), "the old workflow cannot publish rows in the replacement session")
		})
	}
}

func heldQwenWorkflowTaskList(t *testing.T, entry string) (func(agenttest.RecordedRequest) agenttest.RPCReply, <-chan struct{}, func()) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	entered, release := make(chan struct{}), make(chan struct{})
	noteEntered := sync.OnceFunc(func() { close(entered) })
	releaseReply := sync.OnceFunc(func() { close(release) })
	respond := qwenWorkflowTaskList(entry)
	return func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == qwenTaskListMethod {
			noteEntered()
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return respond(request)
	}, entered, releaseReply
}

func TestQwenWorkflowFinalReadStaysInTheTeardownInventory(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	respond, entered, releaseReply := heldQwenWorkflowTaskList(t, qwenWorkflowCompletedEntry)
	a, _, requests := newQwenAgent(t, clock, respond)
	t.Cleanup(releaseReply)
	poller := newWorkflowPoller(a, "call_wf", "triage")
	a.stateMu.Lock()
	a.children.workflows = map[string]*workflowPoller{"call_wf": poller}
	a.stateMu.Unlock()
	poller.start(a.clock)
	a.stopWorkflowPoller("call_wf", bgtask.StatusSucceeded)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the final workflow read did not start")
	}
	a.stateMu.Lock()
	tracked := a.children.workflows["call_wf"]
	a.stateMu.Unlock()
	assert.Same(t, poller, tracked, "teardown must retain the final reader until it finishes")
	select {
	case <-poller.stopped:
		t.Error("the workflow loop ended while its final read was still active")
	default:
	}
	stopped := make(chan struct{})
	go func() {
		a.stopAllWorkflowPollers()
		close(stopped)
	}()
	testutil.RequireEventually(t, func() bool {
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		return a.children.workflows == nil
	})
	select {
	case <-poller.done:
	case <-ctx.Done():
		t.Fatal("workflow teardown did not request the poller to stop")
	}
	releaseReply()
	poller.mu.Lock()
	assert.Empty(t, poller.rows, "teardown must discard the held snapshot before it can publish dispatches")
	poller.mu.Unlock()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("workflow teardown did not join its final read")
	}
	select {
	case <-poller.stopped:
	case <-ctx.Done():
		t.Fatal("the workflow loop did not finish")
	}
	syncPeer(t, a)
	assert.Len(t, requestsFor(requests(), qwenTaskListMethod), 1)
}

func TestQwenWorkflowMissingSnapshotKeepsUnfinishedDispatchesTracked(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, sink, _ := newQwenAgent(t, clock, qwenWorkflowTaskList(""))
	poller := newWorkflowPoller(a, "call_wf", "triage")
	var entry qwenWorkflowEntry
	require.NoError(t, json.Unmarshal([]byte(qwenWorkflowRunningEntry), &entry))
	poller.apply(&entry)

	assert.False(t, poller.poll(), "an absent snapshot must retain the final-frame owner of unfinished rows")
	row, ok := sink.BackgroundTask("call_wf/dispatch-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status, "an absent snapshot states no final outcome")
	a.stateMu.Lock()
	a.children.workflows = map[string]*workflowPoller{"call_wf": poller}
	a.stateMu.Unlock()
	poller.start(clock)
	a.stopWorkflowPoller("call_wf", bgtask.StatusSucceeded)
	testutil.RequireEventually(t, func() bool {
		row, ok := sink.BackgroundTask("call_wf/dispatch-1")
		return ok && row.Status == bgtask.StatusSucceeded && !row.EndedAt.IsZero()
	})
}

func TestQwenWorkflowFinalSnapshotClosesUnfinishedDispatches(t *testing.T) {
	t.Parallel()
	entry := strings.Replace(qwenWorkflowRunningEntry, `"status":"running"`, `"status":"completed"`, 1)
	a, sink, _ := newQwenAgent(t, nil, qwenWorkflowTaskList(entry))
	poller := newWorkflowPoller(a, "call_wf", "triage")

	assert.True(t, poller.poll(), "the completed workflow needs no more reads")
	row, ok := sink.BackgroundTask("call_wf/dispatch-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "the final run status settles an unfinished dispatch")
	assert.False(t, row.EndedAt.IsZero())
}

func TestQwenWorkflowFinalFrameCannotReopenFromAnOlderSnapshot(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	respond, entered, releaseReply := heldQwenWorkflowTaskList(t, qwenWorkflowRunningEntry)
	a, sink, requests := newQwenAgent(t, clock, respond)
	t.Cleanup(releaseReply)
	a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
	a.stateMu.Lock()
	poller := a.children.workflows["call_wf"]
	a.stateMu.Unlock()
	require.NotNil(t, poller)
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call_update","toolCallId":"call_wf","status":"completed","_meta":{"toolName":"workflow"}}`))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the final workflow read did not start")
	}
	before, ok := sink.BackgroundTask("call_wf")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, before.Status, "the native final frame closes the run before the held snapshot")
	releaseReply()
	poller.mu.Lock()
	assert.Equal(t, bgtask.StatusSucceeded, poller.rows["dispatch-1"], "the final frame settles the older snapshot before the loop retires")
	poller.mu.Unlock()
	testutil.RequireEventually(t, func() bool {
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		return len(a.children.workflows) == 0
	})
	after, ok := sink.BackgroundTask("call_wf")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, after.Status, "an older running snapshot cannot reopen the completed run")
	first, ok := sink.BackgroundTask("call_wf/dispatch-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, first.Status)
	syncPeer(t, a)
	assert.Len(t, requestsFor(requests(), qwenTaskListMethod), 1)
}

func TestQwenStopDoesNotWaitForAWorkflowReplyBeforeStoppingTheProcess(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	respond, entered, releaseReply := heldQwenWorkflowTaskList(t, qwenWorkflowRunningEntry)
	a, _, _ := newQwenAgent(t, clock, respond)
	t.Cleanup(releaseReply)
	a.HandleOutput(rawUpdate(t, qwenWorkflowOpenFrame))
	clock.Advance(workflowPollInterval).MustWait(ctx)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the workflow read did not start")
	}
	stopped := make(chan struct{})
	go func() {
		a.Stop()
		close(stopped)
	}()
	testutil.RequireEventually(t, func() bool {
		if a.IsStopped() {
			return true
		}
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		return len(a.children.workflows) == 0
	})
	assert.True(t, a.IsStopped(), "native process shutdown must precede waiting for a workflow reply")
	releaseReply()
	a.SimulateExitForTest()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("stopping Qwen did not join its workflow reader")
	}
	a.startWorkflowPoller("late-workflow", json.RawMessage(`{"name":"late"}`))
	a.stateMu.Lock()
	assert.Empty(t, a.children.workflows)
	a.stateMu.Unlock()
}
