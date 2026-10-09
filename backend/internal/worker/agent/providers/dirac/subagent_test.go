package dirac

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type diracCloseSink struct {
	*agenttest.Sink
	closed chan bgtask.Status
}

func (s *diracCloseSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	if err := s.Sink.CloseBackgroundTask(rowKey, status); err != nil {
		return err
	}
	s.closed <- status
	return nil
}

func newDiracSubagentAgentForRPC(t *testing.T, root string) (*Agent, *diracCloseSink, *quartz.Mock) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	sink := &diracCloseSink{Sink: &agenttest.Sink{}, closed: make(chan bgtask.Status, 16)}
	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{root: root} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == acp.MethodSessionNew {
				return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = a.configure(clock)
	return a, sink, clock
}

func TestDiracSubagentClearContextSeparatesReusedCallIDs(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDiracSubagentAgentForRPC(t, t.TempDir())
	first := a.observeChildCard("call-1", "First child", json.RawMessage(`{"isSubagent":true,"agentId":7,"agentName":"Ada","prompt":"Old child task"}`), nil, "in_progress")
	require.NotNil(t, first)
	a.ApplySubagentObservation(first)
	oldRow, ok := sink.BackgroundTask(first.RowKey)
	require.True(t, ok)
	require.NotEmpty(t, oldRow.ChildAgentID)

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "session-2", sessionID)
	oldRow, ok = sink.BackgroundTask(first.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, oldRow.Status)

	second := a.observeChildCard("call-1", "Second child", json.RawMessage(`{"isSubagent":true,"agentId":8,"agentName":"Bea","prompt":"New child task"}`), nil, "in_progress")
	require.NotNil(t, second)
	assert.True(t, second.Spawns)
	assert.Equal(t, "New child task", second.Prompt)
	assert.NotEqual(t, first.RowKey, second.RowKey, "the new child needs its own registry row")
	a.ApplySubagentObservation(second)
	newRow, ok := sink.BackgroundTask(second.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, newRow.Status)
	assert.NotEqual(t, oldRow.ChildAgentID, newRow.ChildAgentID)
	messages := sink.Child(newRow.ChildAgentID).Messages()
	require.Len(t, messages, 1)
	assert.Contains(t, string(messages[0].Content), "New child task")
	assert.NotContains(t, string(messages[0].Content), "Old child task")
}

func TestDiracChildFinishRejectsAReplacedSessionBeforeClearHook(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDiracSubagentAgentForRPC(t, t.TempDir())
	spawn := a.observeChildCard("call-old", "Ada", json.RawMessage(`{"isSubagent":true,"agentId":7,"agentName":"Ada","prompt":"Count files"}`), nil, "in_progress")
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	state := a.childState["call-old"]
	require.NotNil(t, state)
	// ACP can change sessions after an archive read and before provider cleanup.
	a.SetSessionIDForTest("session-2")
	assert.False(t, a.finishDiracChild(state), "the old child cannot close as completed in the new session")
	assert.Same(t, state, a.childState["call-old"])
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestDiracSubagentFinalCardReleasesItsCumulativeTrajectory(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDiracSubagentAgentForRPC(t, t.TempDir())
	input := json.RawMessage("{\"isSubagent\":true,\"agentId\":7,\"agentName\":\"Ada\",\"prompt\":\"Count files\"}")
	firstOutput := json.RawMessage("{\"trajectory\":[{\"type\":\"message\",\"text\":\"First update\"}]}")
	spawn := a.observeChildCard("call-trajectory", "Ada", input, firstOutput, "in_progress")
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)

	finalOutput := json.RawMessage("{\"trajectory\":[{\"type\":\"message\",\"text\":\"First update\"},{\"type\":\"message\",\"text\":\"Final update\"}]}")
	final := a.observeChildCard("call-trajectory", "Ada", nil, finalOutput, "completed")
	require.NotNil(t, final)
	assert.Contains(t, string(final.ChildTranscriptPayload), "Final update")
	assert.NotContains(t, string(final.ChildTranscriptPayload), "First update")
	a.ApplySubagentObservation(final)
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 3, "the child keeps its prompt and two distinct live updates")
	assert.Contains(t, string(messages[2].Content), "Final update")

	state := a.childState["call-trajectory"]
	require.NotNil(t, state, "the archive still needs the child's identity")
	assert.Empty(t, state.trajectory, "a final card must release its cumulative live text while the archive waits")
	assert.Nil(t, a.observeChildCard("call-trajectory", "Ada", nil, finalOutput, "completed"),
		"a repeated final card must not append the whole trajectory again")
}

func startDiracPendingArchive(t *testing.T, a *Agent) *acp.SubagentObservation {
	t.Helper()
	spawn := a.observeChildCard("call-pending", "Ada", json.RawMessage("{\"isSubagent\":true,\"agentId\":7,\"agentName\":\"Ada\",\"prompt\":\"Count files\"}"), nil, "in_progress")
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.observeChildCard("call-pending", "Ada", nil, nil, "completed")
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)
	a.hydrateSubagentArchives(nil, false)
	return spawn
}

func waitDiracChildClose(t *testing.T, ctx context.Context, sink *diracCloseSink) bgtask.Status {
	t.Helper()
	select {
	case status := <-sink.closed:
		return status
	case <-ctx.Done():
		t.Fatal("the Dirac child row did not close")
		return bgtask.StatusUnspecified
	}
}

func TestDiracArchiveRetriesOnTheMockClock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, sink, clock := newDiracSubagentAgentForRPC(t, root)
	newTimer, _ := testutil.NewTimerTraps(t, clock, diracArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := startDiracPendingArchive(t, a)
	assert.Equal(t, diracArchiveRetryInterval, testutil.WaitForTimer(t, ctx, newTimer))

	writeDiracHistory(t, root, []diracHistoryRecord{{ID: "my-task", ULID: "session-1", TS: 1}})
	writeDiracArchive(t, root, "my-task", "run-one", filepath.Join("run-one", "transcript.md"), &diracChildState{agentID: 7, agentName: "Ada", prompt: "Count files"})
	clock.Advance(diracArchiveRetryInterval).MustWait(ctx)
	assert.Equal(t, bgtask.StatusSucceeded, waitDiracChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 5)
	assert.Contains(t, string(messages[1].Content), "ARCHIVED_CHILD_TEXT")
}

func TestDiracArchiveDeadlineClosesFailed(t *testing.T) {
	t.Parallel()
	a, sink, clock := newDiracSubagentAgentForRPC(t, t.TempDir())
	newTimer, _ := testutil.NewTimerTraps(t, clock, diracArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	started := clock.Now()
	spawn := startDiracPendingArchive(t, a)
	call := newTimer.MustWait(ctx)
	assert.Equal(t, diracArchiveRetryInterval, call.Duration)
	state := a.childState["call-pending"]
	require.NotNil(t, state)
	assert.Equal(t, started.Add(diracArchiveDeadline), state.deadline)
	// The retry goroutine waits inside NewTimer until this test shortens the deadline.
	state.deadline = started.Add(diracArchiveRetryInterval)
	call.MustRelease(ctx)

	clock.Advance(diracArchiveRetryInterval).MustWait(ctx)
	assert.Equal(t, bgtask.StatusFailed, waitDiracChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	notifications := sink.Child(row.ChildAgentID).LeapMuxNotifications()
	encoded, err := json.Marshal(notifications)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "Child transcript unavailable after two minutes")
}

func TestDiracArchiveClearCancelsThePendingTimer(t *testing.T) {
	t.Parallel()
	a, sink, clock := newDiracSubagentAgentForRPC(t, t.TempDir())
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, diracArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := startDiracPendingArchive(t, a)
	testutil.WaitForTimer(t, ctx, newTimer)

	_, err := a.ClearContext()
	require.NoError(t, err)
	stopTimer.MustWait(ctx).MustRelease(ctx)
	assert.Equal(t, bgtask.StatusStopped, waitDiracChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.Empty(t, a.childState)
}

func TestDiracArchiveProcessExitCancelsThePendingTimer(t *testing.T) {
	t.Parallel()
	a, sink, clock := newDiracSubagentAgentForRPC(t, t.TempDir())
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, diracArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := startDiracPendingArchive(t, a)
	testutil.WaitForTimer(t, ctx, newTimer)

	a.CancelForTest()
	stopTimer.MustWait(ctx).MustRelease(ctx)
	assert.Equal(t, bgtask.StatusFailed, waitDiracChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	notifications := sink.Child(row.ChildAgentID).LeapMuxNotifications()
	encoded, err := json.Marshal(notifications)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "process exited before the child archive was ready")
}

func TestDiracArchiveWaitSettlesAFinalCardWithoutPromptEnd(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDiracSubagentAgentForRPC(t, t.TempDir())
	spawn := a.observeChildCard("call-no-prompt-end", "Ada", json.RawMessage("{\"isSubagent\":true,\"agentId\":7,\"agentName\":\"Ada\",\"prompt\":\"Count files\"}"), nil, "in_progress")
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.observeChildCard("call-no-prompt-end", "Ada", nil, nil, "completed")
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)
	// The native process exits after the final card but before PromptEnded.
	a.SimulateExitForTest()
	a.CancelForTest()
	require.NoError(t, a.Wait())

	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	notifications := sink.Child(row.ChildAgentID).LeapMuxNotifications()
	encoded, err := json.Marshal(notifications)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "process exited before the child archive was ready")
	assert.Empty(t, a.childState)
}

func TestDiracArchiveWaitKeepsArchiveCloseBeforeExitCleanup(t *testing.T) {
	t.Parallel()
	a, sink, clock := newDiracSubagentAgentForRPC(t, t.TempDir())
	beforeWait := a.HooksForTest().BeforeWaitCleanup
	require.NotNil(t, beforeWait)
	waitEntered := make(chan struct{})
	a.HooksForTest().BeforeWaitCleanup = func() {
		close(waitEntered)
		beforeWait()
	}
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, diracArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := startDiracPendingArchive(t, a)
	testutil.WaitForTimer(t, ctx, newTimer)

	waitDone := make(chan error, 1)
	a.SimulateExitForTest()
	a.CancelForTest()
	go func() { waitDone <- a.Wait() }()
	select {
	case <-waitEntered:
	case <-ctx.Done():
		t.Fatal("ACP Wait never entered provider cleanup")
	}
	stopCall := stopTimer.MustWait(ctx)
	select {
	case <-waitDone:
		t.Error("Wait returned before the child archive row closed")
	default:
	}
	stopCall.MustRelease(ctx)
	select {
	case err := <-waitDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("Wait did not finish after the child archive row closed")
	}
	assert.Equal(t, bgtask.StatusFailed, waitDiracChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestDiracArchiveBacksOffAfterRepeatedReadFailures(t *testing.T) {
	t.Parallel()
	a, sink, clock := newDiracSubagentAgentForRPC(t, t.TempDir())
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, diracArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	startDiracPendingArchive(t, a)

	first := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, diracArchiveRetryInterval, first)
	clock.Advance(first).MustWait(ctx)
	second := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, 2*diracArchiveRetryInterval, second)
	clock.Advance(second).MustWait(ctx)
	third := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, time.Second, third)
	a.CancelForTest()
	stopTimer.MustWait(ctx).MustRelease(ctx)
	assert.Equal(t, bgtask.StatusFailed, waitDiracChildClose(t, ctx, sink))
}
