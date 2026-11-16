package kimi

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeTaskOf(t *testing.T, sink *agenttest.ControlSink, taskID string) bgtask.Item {
	t.Helper()
	row, exists := sink.BackgroundTask(kimiTaskRowKey("session_1", "main", taskID))
	require.True(t, exists, "the exact native task retains its own row")
	assert.Equal(t, bgtask.RoleTaskOutcome, row.Role)
	return row
}

type kimiTaskCloseRecorder struct {
	agent.ProviderServices
	mu       sync.Mutex
	statuses []bgtask.Status
}

func (s *kimiTaskCloseRecorder) CloseBackgroundTask(key string, status bgtask.Status) error {
	s.mu.Lock()
	s.statuses = append(s.statuses, status)
	s.mu.Unlock()
	return s.ProviderServices.CloseBackgroundTask(key, status)
}

func TestKimiOlderQueuedRunningTaskRetainsItsOwnRegistryLifetime(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	spy := &kimiTaskCloseRecorder{ProviderServices: rig.agent.sink}
	rig.agent.sink = spy
	rig.agent.HandleOutput([]byte(kimiQueuedSwarmTask))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"newer","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","description":"Newer assignment"}`))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"queued-1","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	spy.mu.Lock()
	for _, status := range spy.statuses {
		assert.True(t, status.IsFinished(), "a running superseded task must not issue a pending-status close")
	}
	spy.mu.Unlock()
	older, exists := rig.sink.BackgroundTask("session_1/task/main/queued-1")
	require.True(t, exists)
	assert.False(t, older.Status.IsFinished())
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"queued-1","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0"}`))
	older, exists = rig.sink.BackgroundTask("session_1/task/main/queued-1")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusFailed, older.Status, "the old queued row must close when its own native task ends")
	newer := nativeTaskOf(t, rig.sink, "newer")
	assert.Equal(t, bgtask.StatusRunning, newer.Status)
	assert.Equal(t, "Newer assignment", newer.Title)
}

const kimiQueuedSwarmTask = `{"type":"task","session_id":"session_1","agent_id":"main","task_id":"queued-1","kind":"subagent","status":"running","detached":false,"parent_tool_call_id":"spawn","swarm_index":1,"description":"Queued reviewer"}`

func TestKimiQueuedSwarmTaskRemainsVisibleUntilItsChildStarts(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.HandleOutput([]byte(kimiQueuedSwarmTask))
	queuedKey := "session_1/task/main/queued-1"
	row, exists := rig.sink.BackgroundTask(queuedKey)
	require.True(t, exists, "a task without a child ID still owns a registry row")
	assert.Equal(t, bgtask.KindWorkflow, row.Kind)
	assert.Equal(t, bgtask.StatusPending, row.Status)
	assert.Equal(t, "Queued reviewer", row.Title)
	assert.Empty(t, row.ChildAgentID)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"queued-1","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	task := nativeTaskOf(t, rig.sink, "queued-1")
	assert.Equal(t, queuedKey, task.RowKey, "child adoption does not change native task identity")
	assert.Equal(t, bgtask.KindWorkflow, task.Kind)
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	assert.Equal(t, "Queued reviewer", task.Title)
	canonical, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleChildActivity, canonical.Role)
	assert.Equal(t, canonical.ChildAgentID, task.ChildAgentID)
	assert.NotEmpty(t, task.ChildAgentID)
	assert.False(t, canonical.Status.IsWorking(), "a task reference alone does not prove child activity")
}

func TestKimiQueuedSwarmTaskCanEndBeforeChildCreation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		outcome string
		status  bgtask.Status
	}{
		{"completed", bgtask.StatusSucceeded},
		{"failed", bgtask.StatusFailed},
		{"timed_out", bgtask.StatusFailed},
		{"killed", bgtask.StatusStopped},
		{"lost", bgtask.StatusFailed},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.agent.HandleOutput([]byte(kimiQueuedSwarmTask))
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"queued-1","kind":"subagent","status":"ended","outcome":%q,"detached":false}`, test.outcome)))
			row, exists := rig.sink.BackgroundTask("session_1/task/main/queued-1")
			require.True(t, exists)
			assert.Equal(t, test.status, row.Status)
			assert.Equal(t, bgtask.KindWorkflow, row.Kind)
			assert.Empty(t, row.ChildAgentID)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"queued-1","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			_, linked := rig.agent.children.get("agent-0")
			assert.False(t, linked, "an ended task must not start from a late replay")
		})
	}
}

func TestKimiQueuedSwarmTaskRecoversAMissingOutcome(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.agent.HandleOutput([]byte(kimiQueuedSwarmTask))
	rig.fake.mu.Lock()
	rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
		assert.Equal(t, "queued-1", command.TaskID)
		return map[string]any{"task_id": "queued-1", "kind": "subagent", "status": "ended", "outcome": "failed", "detached": false, "error": "failed before launch"}
	}
	rig.fake.mu.Unlock()
	rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
	waitFor(t, rig.agent.taskRecoveryWork.Idle, "the queued task's missing outcome is recovered")
	row, exists := rig.sink.BackgroundTask("session_1/task/main/queued-1")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	rig.fake.mu.Lock()
	defer rig.fake.mu.Unlock()
	require.Len(t, rig.fake.taskCommands, 2)
	assert.Equal(t, "task.watch", rig.fake.taskCommands[0].Type)
	assert.Equal(t, "task.unwatch", rig.fake.taskCommands[1].Type)
}

func TestKimiQueuedSwarmTaskLeavesFinalizationToItsRegistryOwner(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.HandleOutput([]byte(kimiQueuedSwarmTask))
	rig.agent.settleAfterExit(agent.MessageCompletionInterrupted)
	row, exists := rig.sink.BackgroundTask("session_1/task/main/queued-1")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusPending, row.Status, "the registry owner chooses the exit status from the shared stop context")
	assert.Empty(t, row.ChildAgentID)
}

func TestKimiQueuedSwarmTaskAdoptsATranscriptFirstCanonicalRow(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.HandleOutput([]byte(kimiQueuedSwarmTask))
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"AgentSwarm","state":"running","input":{},"agentRefs":[{"agentId":"agent-0","role":"member"}]}}`)
	canonical, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"queued-1","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	task := nativeTaskOf(t, rig.sink, "queued-1")
	assert.Equal(t, canonical.ChildAgentID, task.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	row, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	assert.Equal(t, canonical.Status, row.Status, "task linkage preserves canonical activity")
	assert.Equal(t, bgtask.RoleChildActivity, row.Role)
}

func TestKimiQueuedTaskWaitsForItsActualOwnerSink(t *testing.T) {
	t.Parallel()
	for _, ended := range []bool{false, true} {
		t.Run(fmt.Sprintf("ended=%t", ended), func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-owner","task_id":"queued-1","kind":"subagent","status":"running","detached":false,"swarm_index":1,"description":"Nested reviewer"}`))
			if ended {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-owner","task_id":"queued-1","kind":"subagent","status":"ended","outcome":"failed","detached":false}`))
			}
			_, exists := rig.sink.BackgroundTask("session_1/task/agent-owner/queued-1")
			assert.False(t, exists, "an unknown native owner must not be replaced with the root owner")
			rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"owner","toolCallId":"owner","name":"Agent","state":"running","input":{},"agentRefs":[{"agentId":"agent-owner","role":"child"}]}}`)
			owner, linked := rig.agent.children.get("agent-owner")
			require.True(t, linked)
			row, exists := rig.sink.BackgroundTask("session_1/task/agent-owner/queued-1")
			require.True(t, exists)
			assert.Equal(t, owner.childAgentID, row.ParentAgentID)
			want := bgtask.StatusPending
			if ended {
				want = bgtask.StatusFailed
			}
			assert.Equal(t, want, row.Status)
			assert.Equal(t, "Nested reviewer", row.Title)
		})
	}
}

type kimiTaskProjectionFailureSink struct {
	*agenttest.ControlSink
	lookupErr     error
	renameErr     error
	upsertErr     error
	closeErr      error
	diagnosticErr error
	ensureErr     error
}

type kimiTaskDiagnosticFailureSink struct {
	agent.ProviderServices
	owner *kimiTaskProjectionFailureSink
}

func (s *kimiTaskDiagnosticFailureSink) ChildSink(childID string) agent.ProviderServices {
	return &kimiTaskDiagnosticFailureSink{ProviderServices: s.ProviderServices.ChildSink(childID), owner: s.owner}
}

func (s *kimiTaskProjectionFailureSink) ChildSink(childID string) agent.ProviderServices {
	return &kimiTaskDiagnosticFailureSink{ProviderServices: s.ControlSink.ChildSink(childID), owner: s}
}

func kimiDiagnosticChildLease(lease agent.ChildSinkLease, owner *kimiTaskProjectionFailureSink) agent.ChildSinkLease {
	direct, valid := agent.DirectTranscript(lease.Sink).(agent.ProviderServices)
	if !valid {
		return agent.ChildSinkLease{}
	}
	lease.Sink = agent.NewOrderedProviderServices(agent.OutputQueueFor(lease.Sink), &kimiTaskDiagnosticFailureSink{ProviderServices: direct, owner: owner})
	return lease
}

func (s *kimiTaskProjectionFailureSink) AcquireChildSink(childID string) agent.ChildSinkLease {
	return kimiDiagnosticChildLease(s.ControlSink.AcquireChildSink(childID), s)
}

func (s *kimiTaskDiagnosticFailureSink) AcquireChildSink(childID string) agent.ChildSinkLease {
	return kimiDiagnosticChildLease(s.ProviderServices.AcquireChildSink(childID), s.owner)
}

func (s *kimiTaskProjectionFailureSink) AcquireChildStateSink(childID string) agent.ChildSinkLease {
	return kimiDiagnosticChildLease(s.ControlSink.AcquireChildStateSink(childID), s)
}

func (s *kimiTaskDiagnosticFailureSink) AcquireChildStateSink(childID string) agent.ChildSinkLease {
	return kimiDiagnosticChildLease(s.ProviderServices.AcquireChildStateSink(childID), s.owner)
}

func (s *kimiTaskDiagnosticFailureSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.owner.diagnosticErr != nil {
		return s.owner.diagnosticErr
	}
	return s.ProviderServices.PersistMessage(source, content, span)
}

func TestKimiTaskDiagnosticRefusalRetriesAfterItsRowCloses(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	rig.agent.sink = &kimiTaskDiagnosticFailureSink{ProviderServices: rig.agent.sink, owner: sink}
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	queue := agent.OutputQueueFor(rig.agent.runSink(rig.agent.run("agent-0")))
	require.NotNil(t, queue)
	clock := testutil.NewQuartzMock(t)
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	created, stopped := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	sink.diagnosticErr = errors.New("diagnostic write refused")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","error":"The task failed."}`))
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	row := nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.Empty(t, child.Messages())
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, created)
	sink.diagnosticErr = nil
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
	queue.StopRetries()
	list := []byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`)
	rig.agent.HandleOutput(list)
	rig.agent.HandleOutput(list)
	messages := child.Messages()
	require.Len(t, messages, 1)
	_, failure := assembledRow(t, messages[0])
	assert.Equal(t, "The task failed.", failure)
	assert.Equal(t, string(agent.MessageCompletionError), assembledCompletion(t, messages[0]))
}

func TestKimiQueuedTaskDiagnosticRemainsOwnedByTheOutputQueue(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	sink.diagnosticErr = errors.New("child storage unavailable")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","error":"The task failed."}`))
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	row := nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.Empty(t, child.Messages())
	rig.agent.Mu.Lock()
	task := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "task-child"}]
	assert.False(t, task.projectionPending, "the output queue retained the diagnostic")
	assert.True(t, task.childEndRetained)
	rig.agent.Mu.Unlock()
	sink.diagnosticErr = nil
	require.NoError(t, agent.FlushPendingOutput(rig.agent.sink.ChildSink(row.ChildAgentID)))
	rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
	messages := child.Messages()
	require.Len(t, messages, 1)
	_, failure := assembledRow(t, messages[0])
	assert.Equal(t, "The task failed.", failure)
}

func TestKimiPendingRunningTaskCannotReclaimANewerChildAssignment(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"older","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","description":"Older task"}`))
	sink.upsertErr = errors.New("registry metadata write failed")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"older","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","description":"Older update"}`))
	sink.upsertErr = nil
	newer := `{"task_id":"newer","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","description":"Newer task"}`
	rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[` + newer + `]}`))
	row := nativeTaskOf(t, rig.sink, "newer")
	assert.Equal(t, "Newer task", row.Title)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)

	assert.False(t, rig.agent.ActiveChildTurnState(child.rowKey).Active)
}

func TestKimiOlderQueuedTaskRetainsItsLateChildResult(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"completed", "failed"} {
		for _, newerEnded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/newer_ended=%t", outcome, newerEnded), func(t *testing.T) {
				t.Parallel()
				rig := newKimiOutputRig(t)
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"older","kind":"subagent","status":"running","detached":false,"description":"Older task"}`))
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"newer","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","description":"Newer task"}`))
				if newerEnded {
					rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"newer","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","description":"Newer task"}`))
				}
				ended := []byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"older","kind":"subagent","status":"ended","outcome":%q,"detached":false,"child_agent_id":"agent-0","description":"Older task","error":%q,"result_summary":%q}`, outcome, map[string]string{"failed": "The older task failed."}[outcome], map[string]string{"completed": "The older task completed."}[outcome]))
				rig.agent.HandleOutput(ended)
				rig.agent.HandleOutput(ended)
				_, childSink := childOf(t, rig.sink, "session_1/agent-0")
				row := nativeTaskOf(t, rig.sink, "newer")
				assert.Equal(t, "Newer task", row.Title)
				if newerEnded {
					assert.Equal(t, bgtask.StatusSucceeded, row.Status)
				} else {
					assert.Equal(t, bgtask.StatusRunning, row.Status)
				}
				older := nativeTaskOf(t, rig.sink, "older")
				expected, _ := kimiTaskStatus(outcome)
				assert.Equal(t, expected, older.Status)
				assert.Equal(t, row.ChildAgentID, older.ChildAgentID)
				canonical, _ := rig.sink.BackgroundTask("session_1/agent-0")
				assert.False(t, canonical.Status.IsWorking())
				if outcome == "completed" {
					require.Len(t, childSink.LeapMuxNotifications(), 1)
					assert.Contains(t, fmt.Sprint(childSink.LeapMuxNotifications()), "The older task completed.")
				} else {
					messages := childSink.Messages()
					require.Len(t, messages, 1)
					_, failure := assembledRow(t, messages[0])
					assert.Equal(t, "The older task failed.", failure)
				}
			})
		}
	}
}

func (s *kimiTaskProjectionFailureSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	if s.closeErr != nil {
		return s.closeErr
	}
	return s.ControlSink.CloseBackgroundTask(rowKey, status)
}

func TestKimiPendingTaskMetadataCannotOverwriteANewerFinishedTask(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	send := func(id, status, outcome, title string) {
		rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":%q,"kind":"subagent","status":%q,"outcome":%q,"detached":false,"child_agent_id":"agent-0","description":%q}`, id, status, outcome, title)))
	}
	send("older", "running", "", "Older task")
	sink.upsertErr = errors.New("registry metadata write failed")
	send("older", "ended", "completed", "Older finished task")
	sink.upsertErr = nil
	send("newer", "running", "", "Newer task")
	send("newer", "ended", "completed", "Newer finished task")
	rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
	row := nativeTaskOf(t, rig.sink, "newer")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Equal(t, "Newer finished task", row.Title)
}

func TestKimiTaskOutcomeWriteRetriesWithoutRepeatingItsFailure(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	sink.upsertErr = errors.New("registry outcome write failed")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","error":"The task failed."}`))
	before := nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.StatusRunning, before.Status, "the refused write has not reached storage")
	rig.agent.Mu.Lock()
	assert.True(t, rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "task-child"}].childEndRetained, "the output queue owns the final task update")
	rig.agent.Mu.Unlock()
	queue := agent.OutputQueueFor(rig.agent.sink)
	require.NotNil(t, queue)
	assert.True(t, queue.HasPending())
	sink.upsertErr = nil
	require.NoError(t, queue.Flush())
	list := []byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`)
	rig.agent.HandleOutput(list)
	rig.agent.HandleOutput(list)
	_, childSink := childOf(t, rig.sink, "session_1/agent-0")
	row := nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	messages := childSink.Messages()
	require.Len(t, messages, 1)
	_, failure := assembledRow(t, messages[0])
	assert.Equal(t, "The task failed.", failure)
}

func (s *kimiTaskProjectionFailureSink) UpsertBackgroundTask(update bgtask.Upsert) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	return s.ControlSink.UpsertBackgroundTask(update)
}

func TestKimiTaskMetadataRetryRetainsFailureOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","description":"Initial title"}`))
	sink.upsertErr = errors.New("registry metadata write failed")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","description":"Recovered title","error":"The earlier task failed."}`))
	sink.upsertErr = nil
	list := []byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`)
	rig.agent.HandleOutput(list)
	rig.agent.HandleOutput(list)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	row := nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.Equal(t, "Recovered title", row.Title)
	messages := child.Messages()
	require.Len(t, messages, 1, "metadata retries do not repeat task failure text")
	assert.True(t, messages[0].TranscriptOnly, "a task failure has no exact child turn identity")
	_, failure := assembledRow(t, messages[0])
	assert.Equal(t, "The earlier task failed.", failure)
}

func TestKimiTaskMetadataFailureRetainsCompletionAndItsReport(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","description":"Initial title"}`))
	sink.upsertErr = errors.New("registry metadata write failed")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","description":"Recovered title","result_summary":"The earlier review completed."}`))
	row := nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "storage retains its last successful projection")
	queue := agent.OutputQueueFor(rig.agent.sink)
	require.NotNil(t, queue)
	assert.True(t, queue.HasPending(), "the queue retains metadata and completion together")
	sink.upsertErr = nil
	require.NoError(t, queue.Flush())
	row = nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Equal(t, "Recovered title", row.Title)
	waitFor(t, func() bool { return len(rig.sink.Child(row.ChildAgentID).LeapMuxNotifications()) == 1 }, "the task report is retained once")
	rig.agent.Mu.Lock()
	assert.False(t, rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "task-child"}].projectionPending)
	rig.agent.Mu.Unlock()
}

func (s *kimiTaskProjectionFailureSink) LookupBackgroundTask(key string) (string, bgtask.Status, bool, error) {
	if s.lookupErr != nil {
		return "", bgtask.StatusUnspecified, false, s.lookupErr
	}
	return s.ControlSink.LookupBackgroundTask(key)
}

func (s *kimiTaskProjectionFailureSink) RenameBackgroundTask(oldKey, newKey string) error {
	if s.renameErr != nil {
		return s.renameErr
	}
	return s.ControlSink.RenameBackgroundTask(oldKey, newKey)
}

func TestKimiQueuedTaskProjectionRetriesAfterRegistryFailure(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	rig.agent.HandleOutput([]byte(kimiQueuedSwarmTask))
	sink.upsertErr = errors.New("registry link write failed")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"queued-1","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	before := nativeTaskOf(t, rig.sink, "queued-1")
	assert.Equal(t, bgtask.StatusPending, before.Status)
	assert.Empty(t, before.ChildAgentID, "the failed write cannot publish partial task linkage")
	queue := agent.OutputQueueFor(rig.agent.sink)
	require.NotNil(t, queue)
	assert.True(t, queue.HasPending())
	sink.upsertErr = nil
	require.NoError(t, queue.Flush())
	row := nativeTaskOf(t, rig.sink, "queued-1")
	assert.Equal(t, before.RowKey, row.RowKey)
	assert.NotEmpty(t, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	canonical, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	assert.Equal(t, row.ChildAgentID, canonical.ChildAgentID)
	assert.False(t, canonical.Status.IsWorking())
	assert.Len(t, rig.sink.BackgroundTasks(), 2, "task linkage retains one independent activity marker")
}

func (s *kimiTaskProjectionFailureSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	if s.ensureErr != nil {
		return "", s.ensureErr
	}
	return s.ControlSink.EnsureChildAgent(spec)
}

func TestKimiTaskRetainsItsUnpublishedChildLinkAfterAdmissionRefusal(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: rig.sink, ensureErr: errors.New("child admission refused")}
	rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	before := nativeTaskOf(t, rig.sink, "task-child")
	assert.Empty(t, before.ChildAgentID)
	assert.Empty(t, rig.sink.ChildAgentIDs())
	rig.agent.Mu.Lock()
	assert.True(t, rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "task-child"}].projectionPending, "task storage does not acknowledge refused child admission")
	rig.agent.Mu.Unlock()
	sink.ensureErr = nil
	rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
	after := nativeTaskOf(t, rig.sink, "task-child")
	assert.NotEmpty(t, after.ChildAgentID, "retained projection retries without another task update")
	assert.Equal(t, before.RowKey, after.RowKey)
	assert.Len(t, rig.sink.ChildAgentIDs(), 1)
}
