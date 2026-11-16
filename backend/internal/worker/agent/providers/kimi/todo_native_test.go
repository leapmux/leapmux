package kimi

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeTodoSnapshotUpdatesStateWithoutTranscript(t *testing.T) {
	t.Parallel()
	rig := newKimiServiceTestRig(t)
	before, err := rig.queries.ListMessagesByAgentID(t.Context(), db.ListMessagesByAgentIDParams{AgentID: "test-agent", Limit: 100})
	require.NoError(t, err)
	rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"main","event_created_at":"2026-11-16T00:00:00Z","items":[{"title":"Read the code","status":"done"},{"title":"Write the fix","status":"in_progress"}]}`))
	items, err := rig.output.LoadTodos(t.Context(), "test-agent")
	require.NoError(t, err)
	assert.Equal(t, []todoevents.Item{{Content: "Read the code", Status: todoevents.StatusCompleted}, {Content: "Write the fix", Status: todoevents.StatusInProgress}}, items)
	after, err := rig.queries.ListMessagesByAgentID(t.Context(), db.ListMessagesByAgentIDParams{AgentID: "test-agent", Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, before, after, "a native state snapshot must not create a transcript row")
	rig.agent.Mu.Lock()
	assert.False(t, rig.agent.turnActive)
	rig.agent.Mu.Unlock()
}

func TestKimiNativeTodoBeforeLinkSurvivesCompletedChildReplay(t *testing.T) {
	t.Parallel()
	rig := newKimiServiceTestRig(t)
	queue := agent.OutputQueueFor(rig.sink)
	require.NotNil(t, queue)
	started, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	drained := make(chan error, 1)
	ctx := testutil.DeadlineContext(t)
	held := queue.Append(agent.OutputWrite{Owner: agent.NewOutputOwner(), Label: "hold earlier output while the child links", Run: func() error {
		close(started)
		<-released
		return nil
	}})[0]
	go func() { drained <- queue.Flush() }()
	defer func() {
		release()
		select {
		case err := <-drained:
			if err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
				assert.NoError(t, err)
			}
		case <-ctx.Done():
			t.Error("the held output did not finish after release")
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("the earlier output write did not start")
	}
	rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"agent-0","items":[{"title":"Current child list","status":"done"}]}`))
	native := &kimiOutputRig{agent: rig.agent}
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"child","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"child","state":"completed","origin":{"kind":"other"}}}`)
	seedKimiChildInvocation(t, native)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	assert.False(t, held.Done(), "the earlier write keeps this snapshot queued")
	completed := queue.Append(agent.OutputWrite{Owner: rig.agent.registryOutputOwner(), Label: "observe completed Kimi state writes", Run: func() error { return nil }})[0]
	release()
	if err := queue.Flush(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		require.NoError(t, err)
	}
	require.Eventually(t, completed.Done, 30*time.Second, time.Millisecond,
		"the to-do write and its preceding child replay must finish before reading saved state")
	items, err := rig.output.LoadTodos(t.Context(), child.childAgentID)
	require.NoError(t, err)
	assert.Equal(t, []todoevents.Item{{Content: "Current child list", Status: todoevents.StatusCompleted}}, items)
	rootItems, err := rig.output.LoadTodos(t.Context(), "test-agent")
	require.NoError(t, err)
	assert.Empty(t, rootItems)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiUnlinkedTodoRetainsOriginalPublisher(t *testing.T) {
	for _, change := range []string{"ordinary turn", "fresh same session", "A B A"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			rig := newKimiServiceTestRig(t)
			rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"agent-0","items":[{"title":"Observed before linking","status":"done"}]}`))
			switch change {
			case "ordinary turn":
				rig.sink.SetTurnState(agent.TurnState{Active: true}, 1)
				rig.sink.SetTurnState(agent.TurnState{}, 2)
			case "fresh same session":
				rig.sink.AdoptNativeSession("session_1")
			case "A B A":
				rig.sink.UpdateSessionID("replacement")
				rig.sink.UpdateSessionID("session_1")
			}
			seedKimiChildInvocation(t, &kimiOutputRig{agent: rig.agent})
			child, linked := rig.agent.children.get("agent-0")
			require.True(t, linked)
			items, err := rig.output.LoadTodos(t.Context(), child.childAgentID)
			require.NoError(t, err)
			if change == "ordinary turn" {
				assert.Equal(t, []todoevents.Item{{Content: "Observed before linking", Status: todoevents.StatusCompleted}}, items)
			} else {
				assert.Empty(t, items, "an unlinked snapshot must not acquire the replacement publisher's authority")
			}
		})
	}
}

func TestKimiNativeTodoSurvivesPendingChildCleanup(t *testing.T) {
	t.Parallel()
	rig := newKimiServiceTestRig(t)
	native := &kimiOutputRig{agent: rig.agent}
	seedKimiChildInvocation(t, native)
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"child","state":"running","origin":{"kind":"other"}}}`)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	_, err := rig.database.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER hold_child_close BEFORE UPDATE ON agent_background_tasks WHEN NEW.row_key = 'session_1/agent-0' AND NEW.status <> %d BEGIN SELECT RAISE(ABORT, 'child close held'); END`, bgtask.StatusRunning))
	require.NoError(t, err)
	native.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"child","state":"completed","origin":{"kind":"other"}}}`)
	childServices := rig.sink.ChildSink(child.childAgentID)
	childServices.OpenSpan("held-output", "")
	childServices.SetSpanType("held-output", "existing-tool")
	held := agent.CaptureTranscript(childServices, agent.MessageContent{}, agent.SpanInfo{SpanID: "held-output"})
	require.True(t, held.FrozenContent().Publication.Owner().IsCurrent(), "the held idle source is current before the state feed")
	rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"agent-0","items":[{"title":"Current child list","status":"done"}]}`))
	assert.True(t, held.FrozenContent().Publication.Owner().IsCurrent(), "state acquisition must preserve accepted child output authority")
	assert.Equal(t, "existing-tool", childServices.GetSpanType("held-output"), "state acquisition must preserve existing spans")
	_, err = rig.database.ExecContext(t.Context(), "DROP TRIGGER hold_child_close")
	require.NoError(t, err)
	rig.agent.flushQueuedOutput()
	require.Eventually(t, func() bool {
		items, err := rig.output.LoadTodos(t.Context(), child.childAgentID)
		return err == nil && len(items) == 1 && items[0].Content == "Current child list" && items[0].Status == todoevents.StatusCompleted
	}, 30*time.Second, time.Millisecond)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiNativeTodoSnapshotSurvivesOlderToolReplay(t *testing.T) {
	t.Parallel()
	rig := newKimiServiceTestRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"main","event_created_at":"2026-11-16T00:00:00Z","items":[{"title":"Current list","status":"done"}]}`))
	native := &kimiOutputRig{agent: rig.agent}
	native.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"old-todos","toolCallId":"old-todos","name":"TodoList","state":"done","input":{"todos":[{"title":"Older list","status":"pending"}]},"output":"Todo list updated."}}`)
	items, err := rig.output.LoadTodos(t.Context(), "test-agent")
	require.NoError(t, err)
	assert.Equal(t, []todoevents.Item{{Content: "Current list", Status: todoevents.StatusCompleted}}, items)
	rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"main","items":[]}`))
	native.transcriptOps(t, 2,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"another-old-list","toolCallId":"another-old-list","name":"TodoList","state":"done","input":{"todos":[{"title":"Another older list","status":"pending"}]},"output":"Todo list updated."}}`)
	items, err = rig.output.LoadTodos(t.Context(), "test-agent")
	require.NoError(t, err)
	assert.Empty(t, items, "an authoritative empty snapshot must also survive historical tool results")
}

func TestKimiNativeTodoPreservesFeedOrderAcrossStorageFailure(t *testing.T) {
	t.Parallel()
	rig := newKimiServiceTestRig(t)
	_, err := rig.database.ExecContext(t.Context(), `CREATE TRIGGER hold_first_todo BEFORE INSERT ON agent_todos WHEN NEW.content = 'First snapshot' BEGIN SELECT RAISE(ABORT, 'todo held'); END`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = rig.database.ExecContext(t.Context(), "DROP TRIGGER IF EXISTS hold_first_todo") })
	rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"main","items":[{"title":"First snapshot","status":"pending"}]}`))
	rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"main","items":[{"title":"Latest snapshot","status":"done"}]}`))
	items, err := rig.output.LoadTodos(t.Context(), "test-agent")
	require.NoError(t, err)
	assert.Empty(t, items, "the second accepted snapshot must wait behind the first retained write")
	_, err = rig.database.ExecContext(t.Context(), "DROP TRIGGER hold_first_todo")
	require.NoError(t, err)
	rig.agent.flushQueuedOutput()
	require.Eventually(t, func() bool {
		items, err := rig.output.LoadTodos(t.Context(), "test-agent")
		return err == nil && len(items) == 1 && items[0].Content == "Latest snapshot" && items[0].Status == todoevents.StatusCompleted
	}, 30*time.Second, time.Millisecond)
}

func TestKimiNativeTodoPreservesIdleAncestorAuthority(t *testing.T) {
	t.Parallel()
	rig := newKimiServiceTestRig(t)
	native := &kimiOutputRig{agent: rig.agent}
	seedKimiChildInvocation(t, native)
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"parent","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"step.upsert","turnId":"parent","step":{"kind":"step","stepId":"parent.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"parent","stepId":"parent.1","frame":{"kind":"tool","frameId":"nested","toolCallId":"nested","name":"Agent","state":"running","input":{"prompt":"Review nested work.","description":"Nested child"},"agentRefs":[{"agentId":"agent-1","role":"child"}]}}`)
	native.transcriptAgentOps(t, "agent-1", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"nested","state":"running","origin":{"kind":"other"}}}`)
	parent, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	child, linked := rig.agent.children.get("agent-1")
	require.True(t, linked)
	_, err := rig.database.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER hold_parent_close BEFORE UPDATE ON agent_background_tasks WHEN NEW.row_key = 'session_1/agent-0' AND NEW.status <> %d BEGIN SELECT RAISE(ABORT, 'parent close held'); END`, bgtask.StatusRunning))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = rig.database.ExecContext(t.Context(), "DROP TRIGGER IF EXISTS hold_parent_close") })
	native.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"parent","state":"completed","origin":{"kind":"other"}}}`)
	native.transcriptAgentOps(t, "agent-1", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"nested","state":"completed","origin":{"kind":"other"}}}`)
	parentServices := rig.sink.ChildSink(parent.childAgentID)
	parentServices.OpenSpan("held-parent", "")
	parentServices.SetSpanType("held-parent", "existing-parent-tool")
	held := agent.CaptureTranscript(parentServices, agent.MessageContent{}, agent.SpanInfo{SpanID: "held-parent"})
	require.True(t, held.FrozenContent().Publication.Owner().IsCurrent())
	rig.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"agent-1","items":[{"title":"Nested list","status":"done"}]}`))
	assert.True(t, held.FrozenContent().Publication.Owner().IsCurrent(), "a nested state feed must preserve its idle ancestor's accepted source")
	assert.Equal(t, "existing-parent-tool", parentServices.GetSpanType("held-parent"))
	_, err = rig.database.ExecContext(t.Context(), "DROP TRIGGER hold_parent_close")
	require.NoError(t, err)
	rig.agent.flushQueuedOutput()
	require.Eventually(t, func() bool {
		items, err := rig.output.LoadTodos(t.Context(), child.childAgentID)
		return err == nil && len(items) == 1 && items[0].Content == "Nested list"
	}, 30*time.Second, time.Millisecond)
	parentItems, err := rig.output.LoadTodos(t.Context(), parent.childAgentID)
	require.NoError(t, err)
	assert.Empty(t, parentItems)
}
