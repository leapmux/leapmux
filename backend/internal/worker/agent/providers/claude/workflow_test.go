package claude

import (
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

type workflowRegistrySink struct {
	*outputTestSink
	reject     atomic.Bool
	mu         sync.Mutex
	attempts   []bgtask.Upsert
	onAccepted func(bgtask.Upsert)
}

func (sink *workflowRegistrySink) UpsertBackgroundTask(row bgtask.Upsert) error {
	if strings.HasPrefix(row.RowKey, "wf-1/wf/") {
		sink.mu.Lock()
		sink.attempts = append(sink.attempts, row)
		sink.mu.Unlock()
		if sink.reject.Load() {
			return assert.AnError
		}
		if sink.onAccepted != nil {
			sink.onAccepted(row)
		}
	}
	return sink.outputTestSink.UpsertBackgroundTask(row)
}

func (sink *workflowRegistrySink) rowAttempts() []bgtask.Upsert {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]bgtask.Upsert(nil), sink.attempts...)
}

func newWorkflowRegistryTestAgent(t *testing.T) (*Agent, *workflowRegistrySink) {
	t.Helper()
	sink := &workflowRegistrySink{outputTestSink: &outputTestSink{}}
	return newTestAgent(agent.NewProviderServices(sink)), sink
}

func TestClaudeWorkflowIdenticalSnapshotRecoversAnUnretainedRegistryRefusal(t *testing.T) {
	t.Parallel()
	a, sink := newWorkflowRegistryTestAgent(t)
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	sink.reject.Store(true)
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	_, exists := sink.BackgroundTask("wf-1/wf/1")
	require.False(t, exists)
	sink.reject.Store(false)
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	row, exists := sink.BackgroundTask("wf-1/wf/1")
	require.True(t, exists, "the identical native snapshot must recover the refused row")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "Reply with WORKFLOW_CHILD_OK.", row.Description)
	before := len(sink.rowAttempts())
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	assert.Len(t, sink.rowAttempts(), before, "an admitted snapshot submits no duplicate row")
}

func TestClaudeWorkflowFinalRegistryWritesRetryWithoutAnotherNativeFrame(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"run completion", "native success", "native failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, sink := newWorkflowRegistryTestAgent(t)
			clock := testutil.NewQuartzMock(t)
			queue := agent.OutputQueueFor(a.sink)
			require.NotNil(t, queue)
			queue.ConfigureRetry(t.Context(), clock, nil)
			t.Cleanup(queue.StopRetries)
			trap := clock.Trap().NewTimer(agent.OutputRetryTimerTag)
			defer trap.Close()
			a.HandleOutput([]byte(claudeWorkflowStartFrame))
			a.HandleOutput([]byte(claudeWorkflowProgressStart))
			sink.reject.Store(true)
			wantStatus, wantDescription := bgtask.StatusSucceeded, "Reply with WORKFLOW_CHILD_OK."
			switch scenario {
			case "run completion":
				a.HandleOutput([]byte(claudeWorkflowNotificationFrame))
			case "native success":
				a.HandleOutput([]byte(claudeWorkflowProgressDone))
				a.HandleOutput([]byte(strings.Replace(claudeWorkflowNotificationFrame, `"status":"completed"`, `"status":"failed"`, 1)))
				wantDescription = "WORKFLOW_CHILD_OK"
			case "native failure":
				a.HandleOutput([]byte(strings.Replace(claudeWorkflowProgressDone, `"state":"done"`, `"state":"error","error":"Native child failed."`, 1)))
				a.HandleOutput([]byte(claudeWorkflowNotificationFrame))
				wantStatus, wantDescription = bgtask.StatusFailed, "Native child failed."
			}
			require.True(t, queue.HasPending(), "the shared queue must own the refused final write")
			ctx := testutil.DeadlineContext(t)
			testutil.WaitForTimer(t, ctx, trap)
			sink.reject.Store(false)
			clock.Advance(time.Second).MustWait(ctx)
			testutil.RequireEventually(t, func() bool {
				row, exists := sink.BackgroundTask("wf-1/wf/1")
				return exists && row.Status == wantStatus && row.Description == wantDescription && !queue.HasPending()
			})
			for _, row := range sink.rowAttempts() {
				if row.Status.IsFinished() {
					assert.Equal(t, wantStatus, row.Status, "the run outcome cannot replace a native child's final outcome")
				}
			}
		})
	}
}

func TestClaudeWorkflowReservesTheWholeSnapshotBeforeAReentrantSinkRuns(t *testing.T) {
	t.Parallel()
	a, sink := newWorkflowRegistryTestAgent(t)
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	var entered atomic.Bool
	sink.onAccepted = func(row bgtask.Upsert) {
		if row.RowKey != "wf-1/wf/1" || !entered.CompareAndSwap(false, true) {
			return
		}
		if !a.workflows.mu.TryLock() {
			t.Error("the registry sink ran under the workflow tracker mutex")
			return
		}
		a.workflows.mu.Unlock()
		a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"wf-1","workflow_progress":[{"type":"workflow_agent","index":2,"label":"second","state":"done","resultPreview":"SECOND DONE"}]}`))
	}
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"wf-1","workflow_progress":[{"type":"workflow_agent","index":1,"label":"first","state":"start","promptPreview":"First prompt"},{"type":"workflow_agent","index":2,"label":"second","state":"start","promptPreview":"Second prompt"}]}`))
	require.True(t, entered.Load())
	rows := sink.rowAttempts()
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"wf-1/wf/1", "wf-1/wf/2", "wf-1/wf/2"}, []string{rows[0].RowKey, rows[1].RowKey, rows[2].RowKey})
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusRunning, bgtask.StatusSucceeded}, []bgtask.Status{rows[0].Status, rows[1].Status, rows[2].Status})
	row, exists := sink.BackgroundTask("wf-1/wf/2")
	require.True(t, exists)
	assert.Equal(t, "SECOND DONE", row.Description)
}

func TestClaudeWorkflowConcurrentFinalSnapshotWaitsForTheAdmittedSnapshot(t *testing.T) {
	t.Parallel()
	a, sink := newWorkflowRegistryTestAgent(t)
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	entered, release := make(chan struct{}), make(chan struct{})
	releaseWrite := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseWrite)
	sink.onAccepted = func(row bgtask.Upsert) {
		if row.Status == bgtask.StatusRunning {
			close(entered)
			<-release
		}
	}
	completed := make(chan struct{})
	go func() { a.HandleOutput([]byte(claudeWorkflowProgressStart)); close(completed) }()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the initial workflow row did not reach the registry")
	}
	a.HandleOutput([]byte(claudeWorkflowProgressDone))
	_, exists := sink.BackgroundTask("wf-1/wf/1")
	assert.False(t, exists, "a later snapshot cannot publish before the earlier row finishes")
	releaseWrite()
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("the admitted workflow snapshot did not settle")
	}
	row, exists := sink.BackgroundTask("wf-1/wf/1")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Equal(t, "WORKFLOW_CHILD_OK", row.Description)
	attempts := sink.rowAttempts()
	require.Len(t, attempts, 2)
	assert.Equal(t, bgtask.StatusRunning, attempts[0].Status)
	assert.Equal(t, bgtask.StatusSucceeded, attempts[1].Status)
}

func TestClaudeWorkflowMissingOutputQueueDoesNotConsumePublicationState(t *testing.T) {
	t.Parallel()
	var tracker claudeWorkflowRuns
	tracker.started("wf", "workflow:run", "run")
	entries := []claudeWorkflowProgressEntry{{Type: "workflow_agent", Index: 1, Label: "child", State: "start"}}
	require.ErrorContains(t, tracker.progress("wf", entries, nil), "output queue")
	assert.Empty(t, tracker.runs["wf"].agents)
	assert.Empty(t, tracker.runs["wf"].order)
	require.ErrorContains(t, tracker.finished("wf", bgtask.StatusSucceeded, nil), "output queue")
	require.NotNil(t, tracker.runs["wf"])
	sink := &outputTestSink{}
	services := agent.NewProviderServices(sink)
	require.NoError(t, tracker.progress("wf", entries, services))
	require.NoError(t, tracker.finished("wf", bgtask.StatusSucceeded, services))
	row, exists := sink.BackgroundTask("wf/wf/1")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Nil(t, tracker.runs["wf"])
}

// The workflow fixtures quote the frames of the Claude Code 2.1.310 probe
// (.tmp/claude-wf/frames-clwf.jsonl): a `local_workflow` task_started, full
// task_progress snapshots of its agents, and the closing task_notification.

const claudeWorkflowStartFrame = `{"type":"system","subtype":"task_started","task_id":"wf-1","tool_use_id":"workflow-probe",` +
	`"task_type":"local_workflow","workflow_name":"leapmux-e2e-probe","description":"Run one local probe"}`

const claudeWorkflowProgressStart = `{"type":"system","subtype":"task_progress","task_id":"wf-1","description":"Probe: probe",` +
	`"workflow_progress":[{"type":"workflow_phase","index":1,"title":"Probe"},` +
	`{"type":"workflow_agent","index":1,"label":"probe","phaseTitle":"Probe","agentId":"ac590a535ee34c6f5",` +
	`"state":"start","promptPreview":"Reply with WORKFLOW_CHILD_OK.","promptFramed":true}]}`

const claudeWorkflowProgressDone = `{"type":"system","subtype":"task_progress","task_id":"wf-1","description":"Probe: probe",` +
	`"workflow_progress":[{"type":"workflow_phase","index":1,"title":"Probe"},` +
	`{"type":"workflow_agent","index":1,"label":"probe","phaseTitle":"Probe","agentId":"ac590a535ee34c6f5",` +
	`"state":"done","promptPreview":"Reply with WORKFLOW_CHILD_OK.","resultPreview":"WORKFLOW_CHILD_OK"}]}`

const claudeWorkflowNotificationFrame = `{"type":"system","subtype":"task_notification","task_id":"wf-1",` +
	`"tool_use_id":"workflow-probe","status":"completed","summary":"Dynamic workflow \"Run one local probe\" completed"}`

// The first snapshot of a run draws one agent row in the run's group.
func TestClaudeWorkflowProgressDrawsAgentRowInRunGroup(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 2)
	run, agentRow := tasks[0], tasks[1]
	require.Equal(t, "wf-1", run.RowKey)
	require.Equal(t, "wf-1/wf/1", agentRow.RowKey)
	assert.Equal(t, bgtask.KindWorkflow, run.Kind)
	assert.Equal(t, bgtask.KindSubagent, agentRow.Kind)
	assert.Equal(t, "workflow:leapmux-e2e-probe", agentRow.GroupKey)
	assert.Equal(t, "leapmux-e2e-probe", agentRow.GroupLabel)
	assert.Equal(t, run.GroupKey, agentRow.GroupKey, "the agent row joins the run row's group")
	assert.Equal(t, "probe", agentRow.Title)
	assert.Equal(t, "Reply with WORKFLOW_CHILD_OK.", agentRow.Description)
	assert.Equal(t, bgtask.StatusRunning, agentRow.Status)
	assert.Equal(t, bgtask.StatusRunning, run.Status)
	assert.Empty(t, agentRow.ChildAgentID, "a workflow agent owns no transcript")
}

// A snapshot that settles an agent ends its row with its own outcome, and a
// repeated unchanged snapshot writes nothing new.
func TestClaudeWorkflowProgressSettlesAgentRow(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	a.HandleOutput([]byte(claudeWorkflowProgressDone))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 2)
	agentRow := tasks[1]
	assert.Equal(t, bgtask.StatusSucceeded, agentRow.Status)
	assert.Equal(t, "WORKFLOW_CHILD_OK", agentRow.Description)
	assert.False(t, agentRow.EndedAt.IsZero(), "a final snapshot closes the agent row")
}

// The run's notification ends every agent row that a snapshot did not settle.
func TestClaudeWorkflowNotificationClosesUnsettledAgents(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	a.HandleOutput([]byte(claudeWorkflowNotificationFrame))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 2)
	agentRow := tasks[1]
	assert.Equal(t, bgtask.StatusSucceeded, agentRow.Status, "the run's outcome ends the unsettled agent")
	assert.False(t, agentRow.EndedAt.IsZero())
}

// A progress snapshot without a task_started draws nothing: no workflow name,
// no group to join.
func TestClaudeWorkflowProgressWithoutStartDrawsNothing(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	assert.Empty(t, sink.BackgroundTasks())
}

// A progress frame of a non-workflow task carries no workflow snapshot, so it
// draws no agent rows.
func TestClaudeWorkflowProgressOfSubagentDrawsNothing(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Reviewer"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"task-1","description":"Reviewing","last_tool_name":"Grep"}`))
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "task-1", tasks[0].RowKey)
}

// A run of two agents keeps one row per agent, keyed by the stable index.
func TestClaudeWorkflowTwoAgentsDrawTwoRows(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"wf-1","workflow_progress":[` +
		`{"type":"workflow_agent","index":1,"label":"first","state":"start","promptPreview":"First prompt."},` +
		`{"type":"workflow_agent","index":2,"label":"second","state":"start","promptPreview":"Second prompt."}]}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"wf-1","workflow_progress":[` +
		`{"type":"workflow_agent","index":1,"label":"first","state":"done","resultPreview":"ONE"},` +
		`{"type":"workflow_agent","index":2,"label":"second","state":"error","error":"API Error: 500"}]}`))
	a.HandleOutput([]byte(claudeWorkflowNotificationFrame))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 3)
	first, second := tasks[1], tasks[2]
	require.Equal(t, "wf-1/wf/1", first.RowKey)
	require.Equal(t, "wf-1/wf/2", second.RowKey)
	assert.Equal(t, bgtask.StatusSucceeded, first.Status)
	assert.Equal(t, "ONE", first.Description)
	assert.Equal(t, bgtask.StatusFailed, second.Status)
	assert.Equal(t, "API Error: 500", second.Description)
	assert.False(t, first.EndedAt.IsZero())
	assert.False(t, second.EndedAt.IsZero())
}

// A notification without a start forgets nothing and fails nothing.
func TestClaudeWorkflowNotificationWithoutStartIsNoop(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowNotificationFrame))
	assert.Empty(t, sink.BackgroundTasks())
}

// An agent without a label takes the head of its prompt as the title.
func TestClaudeWorkflowUnlabelledAgentUsesPromptTitle(t *testing.T) {
	t.Parallel()

	w := &claudeWorkflowRuns{}
	sink := &outputTestSink{}
	services := agent.NewProviderServices(sink)
	w.started("wf-1", "workflow:run", "run")
	require.NoError(t, w.progress("wf-1", []claudeWorkflowProgressEntry{{
		Type: "workflow_agent", Index: 3, State: "start",
		PromptPreview: "Inspect the parser.\nSecond line.",
	}}, services))
	upserts := sink.BackgroundTasks()
	require.Len(t, upserts, 1)
	assert.Equal(t, "Inspect the parser.", upserts[0].Title)
	assert.Equal(t, "wf-1/wf/3", upserts[0].RowKey)
}

// A repeated task_started keeps the recorded group, and a settled agent row
// keeps its own outcome when the run ends.
func TestClaudeWorkflowRunsStartedIsIdempotent(t *testing.T) {
	t.Parallel()

	w := &claudeWorkflowRuns{}
	sink := &outputTestSink{}
	services := agent.NewProviderServices(sink)
	w.started("wf-1", "workflow:one", "one")
	w.started("wf-1", "workflow:two", "two")
	require.NoError(t, w.progress("wf-1", []claudeWorkflowProgressEntry{
		{Type: "workflow_agent", Index: 1, Label: "a", State: "done", ResultPreview: "ok"},
	}, services))
	upserts := sink.BackgroundTasks()
	require.Len(t, upserts, 1)
	assert.Equal(t, "workflow:one", upserts[0].GroupKey)

	require.NoError(t, w.finished("wf-1", bgtask.StatusStopped, services))
	assert.Equal(t, upserts, sink.BackgroundTasks(), "a settled agent row keeps its own outcome")
	assert.Nil(t, w.runs["wf-1"], "finished forgets the run")
}
