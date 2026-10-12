package fastagent

import (
	"context"
	"encoding/json"
	"os"
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

type fastagentCloseSink struct {
	*agenttest.Sink
	closed chan bgtask.Status
}

func (s *fastagentCloseSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	if err := s.Sink.CloseBackgroundTask(rowKey, status); err != nil {
		return err
	}
	s.closed <- status
	return nil
}

func newFastagentSubagentAgentForRPC(t *testing.T, home string) (*Agent, *fastagentCloseSink, *quartz.Mock) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	sink := &fastagentCloseSink{Sink: &agenttest.Sink{}, closed: make(chan bgtask.Status, 16)}
	services := agent.NewProviderServices(sink)
	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{home: home, clock: clock} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == acp.MethodSessionNew {
				return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.hooks("", services)
	return a, sink, clock
}

func TestSubagentFromToolCallUpdateOpensStreamingCallWhenInputArrives(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	initial := a.subagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "call-1", Title: "local/subagent", Status: "pending",
	})
	assert.Nil(t, initial)

	update := acp.ToolCallUpdateEnvelope{
		ToolCallID: "call-1", Title: "local/subagent", Status: "in_progress",
		RawInput: json.RawMessage(`{"message":"Count files","label":"Count files"}`),
	}
	spawn := a.subagentFromToolCallUpdate(update)
	require.NotNil(t, spawn)
	assert.Equal(t, "call-1", spawn.RowKey)
	assert.Equal(t, "call-1", spawn.ChildAgentKey)
	assert.Equal(t, "Count files", spawn.Prompt)
	assert.Equal(t, bgtask.StatusRunning, spawn.Status)
	assert.True(t, spawn.Spawns)
	assert.Nil(t, a.subagentFromToolCallUpdate(update), "a repeated update must not open a second child")
}

func TestFastagentSubagentClearContextSeparatesReusedCallIDs(t *testing.T) {
	t.Parallel()
	a, sink, _ := newFastagentSubagentAgentForRPC(t, t.TempDir())
	first := a.observeSubagentCall("call-1", "subagent", json.RawMessage(`{"message":"Old child task","label":"First child"}`))
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

	second := a.observeSubagentCall("call-1", "subagent", json.RawMessage(`{"message":"New child task","label":"Second child"}`))
	require.NotNil(t, second, "the new native session may reuse a call id")
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

func TestFastagentChildClaimRejectsAReplacedSessionBeforeClearHook(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	a, sink, _ := newFastagentSubagentAgentForRPC(t, home)
	spawn := a.observeSubagentCall("acp-old", "subagent", json.RawMessage(`{"message":"Count files","label":"Count files"}`))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	writeFastagentChildArchive(t, home, "session-1", "child-old", "model-old", "history_agent.json")
	state := a.childState["acp-old"]
	require.NotNil(t, state)
	archive, err := readFastagentChildArchive(home, "session-1", state)
	require.NoError(t, err)
	require.Equal(t, "child-old", archive.childID)

	// ACP changes its current session before it calls ClearProviderState.
	a.SetSessionIDForTest("session-2")
	assert.False(t, a.claimFastagentChild(state, archive.childID), "the old archive cannot enter the new session")
	assert.Empty(t, a.childClaims)
	a.HooksForTest().ClearProviderState()
	assert.Empty(t, a.childState)
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status, "ACP closes the outgoing row after the provider hook")
}

func TestFastagentChildFinishRejectsAReplacedSessionBeforeClearHook(t *testing.T) {
	t.Parallel()
	a, sink, _ := newFastagentSubagentAgentForRPC(t, t.TempDir())
	spawn := a.observeSubagentCall("acp-old", "subagent", json.RawMessage(`{"message":"Count files","label":"Count files"}`))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	state := a.childState["acp-old"]
	require.NotNil(t, state)
	// The archive read and the last update can finish before ACP clears state.
	a.SetSessionIDForTest("session-2")
	assert.False(t, a.finishFastagentChild(state), "the old child cannot close as completed in the new session")
	assert.Same(t, state, a.childState["acp-old"])
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestFastagentSubagentArchiveWaitsForAFileAfterItsFinalUpdate(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	a, sink, _ := newFastagentSubagentAgentForRPC(t, home)
	spawn := a.observeSubagentCall("call-archive", "subagent", json.RawMessage(`{"message":"Count files","label":"Count files"}`))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)

	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{ToolCallID: "call-archive", Status: "completed"})
	require.NotNil(t, final)
	require.False(t, final.CloseRow, "a missing archive must keep the child route open")
	a.ApplySubagentObservation(final)
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	writeFastagentChildArchive(t, home, "session-1", "child-archive", "native-call", "history_agent.json")
	require.NotNil(t, a.HooksForTest().PromptEnded)
	a.HooksForTest().PromptEnded(nil, false)
	row, ok = sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1].Content), "FAST_CHILD_ARCHIVED_TEXT")
}

func TestFastagentSubagentArchiveSeparatesIdenticalPromptsByNativeResult(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	const parentID = "session-1"
	for _, child := range []struct {
		id, modelCallID, result string
	}{
		{id: "child-first", modelCallID: "model-first", result: "FIRST_CHILD_RESULT"},
		{id: "child-second", modelCallID: "model-second", result: "SECOND_CHILD_RESULT"},
	} {
		writeFastagentChildArchive(t, home, parentID, child.id, child.modelCallID, "history_agent.json")
		history := map[string]any{"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "Count files"}}},
			map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": child.result}}},
		}}
		raw, err := json.Marshal(history)
		require.NoError(t, err)
		path := filepath.Join(home, "sessions", parentID, "children", child.id, "history_agent.json")
		require.NoError(t, os.WriteFile(path, raw, 0o644))
	}
	writeFastagentParentResultHistory(t, home, parentID,
		fastagentParentResultFixture{modelCallID: "model-first", childID: "child-first", text: "FIRST_CHILD_RESULT"},
		fastagentParentResultFixture{modelCallID: "model-second", childID: "child-second", text: "SECOND_CHILD_RESULT"},
	)

	a, sink, _ := newFastagentSubagentAgentForRPC(t, home)
	children := []struct {
		acpID, result, other string
	}{
		{acpID: "acp-first", result: "FIRST_CHILD_RESULT", other: "SECOND_CHILD_RESULT"},
		{acpID: "acp-second", result: "SECOND_CHILD_RESULT", other: "FIRST_CHILD_RESULT"},
	}
	for _, child := range children {
		spawn := a.observeSubagentCall(child.acpID, "subagent", json.RawMessage(`{"message":"Count files","label":"Count files"}`))
		require.NotNil(t, spawn)
		a.ApplySubagentObservation(spawn)
	}
	for _, child := range children {
		output, err := json.Marshal(child.result)
		require.NoError(t, err)
		final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
			ToolCallID: child.acpID, Status: "completed", RawOutput: output,
		})
		require.NotNil(t, final)
		require.True(t, final.CloseRow, "a native result identifies one of two identical prompts")
		a.ApplySubagentObservation(final)
		row, ok := sink.BackgroundTask(fastagentChildRowKey(parentID, child.acpID))
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status)
		messages := sink.Child(row.ChildAgentID).Messages()
		require.Len(t, messages, 2)
		assert.Contains(t, string(messages[1].Content), child.result)
		assert.NotContains(t, string(messages[1].Content), child.other)
	}
}

func TestFastagentSubagentArchiveDoesNotTreatAnEmptyNativeResultAsAbsent(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "session-1", "child-old", "model-old", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "session-1",
		fastagentParentResultFixture{modelCallID: "model-old", childID: "child-old", text: "OLD_RESULT"},
		fastagentParentResultFixture{modelCallID: "model-new", childID: "child-new", text: ""},
	)
	a, sink, _ := newFastagentSubagentAgentForRPC(t, home)
	spawn := a.observeSubagentCall("acp-new", "subagent", json.RawMessage(`{"message":"Count files","label":"Count files"}`))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "acp-new", Status: "completed", RawOutput: json.RawMessage(`""`),
	})
	require.NotNil(t, final)
	require.False(t, final.CloseRow, "an empty native result still identifies a different child")
	a.ApplySubagentObservation(final)
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	writeFastagentChildArchive(t, home, "session-1", "child-new", "model-new", "history_agent.json")
	newHistory := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "Count files"}}},
		map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": "NEW_EMPTY_RESULT_CHILD_ARCHIVE"}}},
	}}
	raw, err := json.Marshal(newHistory)
	require.NoError(t, err)
	path := filepath.Join(home, "sessions", "session-1", "children", "child-new", "history_agent.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	a.HooksForTest().PromptEnded(nil, false)
	row, ok = sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1].Content), "NEW_EMPTY_RESULT_CHILD_ARCHIVE")
	assert.NotContains(t, string(messages[1].Content), "FAST_CHILD_ARCHIVED_TEXT")
}

func TestFastagentSubagentArchiveNeverReusesAClaimedIdenticalResult(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	a, sink, clock := newFastagentSubagentAgentForRPC(t, home)
	ctx := testutil.DeadlineContext(t)
	input := json.RawMessage(`{"message":"Count files","label":"Count files"}`)
	result := json.RawMessage(`"SAME_RESULT"`)
	writeFastagentChildArchive(t, home, "session-1", "child-old", "model-old", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "session-1", fastagentParentResultFixture{
		modelCallID: "model-old", childID: "child-old", text: "SAME_RESULT",
	})
	old := a.observeSubagentCall("acp-old", "subagent", input)
	require.NotNil(t, old)
	a.ApplySubagentObservation(old)
	oldFinal := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "acp-old", Status: "completed", RawOutput: result,
	})
	require.NotNil(t, oldFinal)
	require.True(t, oldFinal.CloseRow)
	a.ApplySubagentObservation(oldFinal)
	assert.Equal(t, bgtask.StatusSucceeded, waitFastagentChildClose(t, ctx, sink))

	newTimer, _ := testutil.NewTimerTraps(t, clock, fastagentArchiveTimerTag)
	newChild := a.observeSubagentCall("acp-new", "subagent", input)
	require.NotNil(t, newChild)
	a.ApplySubagentObservation(newChild)
	newFinal := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{
		ToolCallID: "acp-new", Status: "completed", RawOutput: result,
	})
	require.NotNil(t, newFinal)
	require.False(t, newFinal.CloseRow, "the old native child already belongs to another ACP row")
	a.ApplySubagentObservation(newFinal)
	call := newTimer.MustWait(ctx)
	writeFastagentChildArchive(t, home, "session-1", "child-new", "model-new", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "session-1",
		fastagentParentResultFixture{modelCallID: "model-old", childID: "child-old", text: "SAME_RESULT"},
		fastagentParentResultFixture{modelCallID: "model-new", childID: "child-new", text: "SAME_RESULT"},
	)
	a.HooksForTest().PromptEnded(nil, false)
	row, ok := sink.BackgroundTask(newChild.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	state := a.childState["acp-new"]
	require.NotNil(t, state)
	state.deadline = clock.Now().Add(fastagentArchiveRetryInterval)
	call.MustRelease(ctx)
	clock.Advance(fastagentArchiveRetryInterval).MustWait(ctx)
	assert.Equal(t, bgtask.StatusFailed, waitFastagentChildClose(t, ctx, sink))
	row, ok = sink.BackgroundTask(newChild.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.Len(t, sink.Child(row.ChildAgentID).Messages(), 1, "the ambiguous archive reaches neither child transcript")
}

func TestFastagentSubagentArchiveKeepsTheRouteAfterAFirstDeliveryFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	a, sink, _ := newFastagentSubagentAgentForRPC(t, home)
	spawn := a.observeSubagentCall("call-route", "subagent", json.RawMessage(`{"message":"Count files","label":"Count files"}`))
	require.NotNil(t, spawn)
	writeFastagentChildArchive(t, home, "session-1", "child-route", "native-call", "history_agent.json")

	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{ToolCallID: "call-route", Status: "completed"})
	require.NotNil(t, final)
	require.False(t, final.CloseRow, "a refused child route must remain available for replay")
	a.ApplySubagentObservation(spawn)
	require.NotNil(t, a.HooksForTest().PromptEnded)
	a.HooksForTest().PromptEnded(nil, false)
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1].Content), "FAST_CHILD_ARCHIVED_TEXT")
}

func waitFastagentChildClose(t *testing.T, ctx context.Context, sink *fastagentCloseSink) bgtask.Status {
	t.Helper()
	select {
	case status := <-sink.closed:
		return status
	case <-ctx.Done():
		t.Fatal("the Fast Agent child row did not close")
		return bgtask.StatusUnspecified
	}
}

func TestFastagentSubagentArchiveRetriesOnTheMockClock(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	a, sink, clock := newFastagentSubagentAgentForRPC(t, home)
	newTimer, _ := testutil.NewTimerTraps(t, clock, fastagentArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := a.observeSubagentCall("call-retry", "subagent", json.RawMessage("{\"message\":\"Count files\",\"label\":\"Count files\"}"))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{ToolCallID: "call-retry", Status: "completed"})
	require.NotNil(t, final)
	require.False(t, final.CloseRow)
	a.ApplySubagentObservation(final)
	assert.Equal(t, fastagentArchiveRetryInterval, testutil.WaitForTimer(t, ctx, newTimer))

	writeFastagentChildArchive(t, home, "session-1", "child-retry", "native-call", "history_agent.json")
	clock.Advance(fastagentArchiveRetryInterval).MustWait(ctx)
	assert.Equal(t, bgtask.StatusSucceeded, waitFastagentChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1].Content), "FAST_CHILD_ARCHIVED_TEXT")
}

func TestFastagentSubagentArchiveDeadlineClosesFailed(t *testing.T) {
	t.Parallel()
	a, sink, clock := newFastagentSubagentAgentForRPC(t, t.TempDir())
	newTimer, _ := testutil.NewTimerTraps(t, clock, fastagentArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	started := clock.Now()
	spawn := a.observeSubagentCall("call-deadline", "subagent", json.RawMessage("{\"message\":\"Count files\",\"label\":\"Count files\"}"))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{ToolCallID: "call-deadline", Status: "completed"})
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)
	call := newTimer.MustWait(ctx)
	assert.Equal(t, fastagentArchiveRetryInterval, call.Duration)
	state := a.childState["call-deadline"]
	require.NotNil(t, state)
	assert.Equal(t, started.Add(fastagentArchiveDeadline), state.deadline)
	// The retry goroutine waits inside NewTimer until this test shortens the deadline.
	state.deadline = started.Add(fastagentArchiveRetryInterval)
	call.MustRelease(ctx)

	clock.Advance(fastagentArchiveRetryInterval).MustWait(ctx)
	assert.Equal(t, bgtask.StatusFailed, waitFastagentChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	notifications := sink.Child(row.ChildAgentID).LeapMuxNotifications()
	encoded, err := json.Marshal(notifications)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "Child transcript unavailable after two minutes")
}

func TestFastagentSubagentArchiveClearCancelsThePendingTimer(t *testing.T) {
	t.Parallel()
	a, sink, clock := newFastagentSubagentAgentForRPC(t, t.TempDir())
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, fastagentArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := a.observeSubagentCall("call-clear", "subagent", json.RawMessage("{\"message\":\"Count files\",\"label\":\"Count files\"}"))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{ToolCallID: "call-clear", Status: "completed"})
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)
	testutil.WaitForTimer(t, ctx, newTimer)

	_, err := a.ClearContext()
	require.NoError(t, err)
	stopTimer.MustWait(ctx).MustRelease(ctx)
	assert.Equal(t, bgtask.StatusStopped, waitFastagentChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.Empty(t, a.childState)
}

func TestFastagentSubagentArchiveProcessExitCancelsThePendingTimer(t *testing.T) {
	t.Parallel()
	a, sink, clock := newFastagentSubagentAgentForRPC(t, t.TempDir())
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, fastagentArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := a.observeSubagentCall("call-exit", "subagent", json.RawMessage("{\"message\":\"Count files\",\"label\":\"Count files\"}"))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{ToolCallID: "call-exit", Status: "completed"})
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)
	testutil.WaitForTimer(t, ctx, newTimer)

	a.CancelForTest()
	stopTimer.MustWait(ctx).MustRelease(ctx)
	assert.Equal(t, bgtask.StatusFailed, waitFastagentChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	notifications := sink.Child(row.ChildAgentID).LeapMuxNotifications()
	encoded, err := json.Marshal(notifications)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "process exited before the child archive was ready")
}

func TestFastagentSubagentWaitKeepsArchiveCloseBeforeExitCleanup(t *testing.T) {
	t.Parallel()
	a, sink, clock := newFastagentSubagentAgentForRPC(t, t.TempDir())
	beforeWait := a.HooksForTest().BeforeWaitCleanup
	require.NotNil(t, beforeWait)
	waitEntered := make(chan struct{})
	a.HooksForTest().BeforeWaitCleanup = func() {
		close(waitEntered)
		beforeWait()
	}
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, fastagentArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := a.observeSubagentCall("call-wait", "subagent", json.RawMessage("{\"message\":\"Count files\",\"label\":\"Count files\"}"))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{ToolCallID: "call-wait", Status: "completed"})
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)
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
	assert.Equal(t, bgtask.StatusFailed, waitFastagentChildClose(t, ctx, sink))
	row, ok := sink.BackgroundTask(spawn.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestFastagentSubagentArchiveBacksOffAfterRepeatedReadFailures(t *testing.T) {
	t.Parallel()
	a, sink, clock := newFastagentSubagentAgentForRPC(t, t.TempDir())
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, fastagentArchiveTimerTag)
	ctx := testutil.DeadlineContext(t)
	spawn := a.observeSubagentCall("call-backoff", "subagent", json.RawMessage("{\"message\":\"Count files\",\"label\":\"Count files\"}"))
	require.NotNil(t, spawn)
	a.ApplySubagentObservation(spawn)
	final := a.subagentFromToolCallUpdate(acp.ToolCallUpdateEnvelope{ToolCallID: "call-backoff", Status: "completed"})
	require.NotNil(t, final)
	a.ApplySubagentObservation(final)

	first := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, fastagentArchiveRetryInterval, first)
	clock.Advance(first).MustWait(ctx)
	second := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, 2*fastagentArchiveRetryInterval, second)
	clock.Advance(second).MustWait(ctx)
	third := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, time.Second, third)
	a.CancelForTest()
	stopTimer.MustWait(ctx).MustRelease(ctx)
	assert.Equal(t, bgtask.StatusFailed, waitFastagentChildClose(t, ctx, sink))
}
