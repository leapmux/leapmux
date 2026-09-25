package cline

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// spawnStart is the tool.started payload of one spawn_agent call.
func spawnStart(id, task string) map[string]any {
	return map[string]any{"toolCallId": id, "toolName": contracts.ClineToolSpawnAgent, "input": map[string]any{"systemPrompt": "You help.", "task": task}}
}

// spawnFinish is the tool.finished payload of one spawn_agent call.
func spawnFinish(id, text, reason string) map[string]any {
	return map[string]any{"toolCallId": id, "toolName": contracts.ClineToolSpawnAgent, "output": map[string]any{"text": text, "iterations": 2, "finishReason": reason}}
}

// childSink returns the recording sink of the child that the row rowKey links.
func (r *rig) childSink(t *testing.T, rowKey string) *agenttest.Sink {
	t.Helper()
	item, ok := r.sink.BackgroundTask(rowKey)
	require.True(t, ok, "the row exists")
	require.NotEmpty(t, item.ChildAgentID, "the row links a child transcript")
	return r.sink.Child(item.ChildAgentID)
}

func TestASubagentsOutputReachesItsOwnTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", "Run one command and report."))

	item, ok := r.sink.BackgroundTask("spawn_1")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindSubagent, item.Kind)
	assert.Equal(t, bgtask.StatusRunning, item.Status)
	assert.Equal(t, "Run one command and report.", item.Title)
	child := r.childSink(t, "spawn_1")
	require.Len(t, child.Messages(), 1, "the task opens the child transcript")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, child.Messages()[0].Source)

	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_sub_1", "toolName": "run_commands", "input": map[string]any{"commands": []string{"echo from-subagent"}}})
	r.feed(t, eventToolUpdated, map[string]any{"toolCallId": "call_sub_1", "update": map[string]any{"chunk": "from-subagent\n"}})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "call_sub_1", "toolName": "run_commands", "output": []any{}})
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 2})
	r.feed(t, eventReasoningDelta, map[string]any{"text": "Subagent reasoning."})
	r.feed(t, eventAssistantDelta, map[string]any{"text": "Subagent result."})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Subagent result."})
	r.feed(t, contracts.ClineEventReasoningFinished, map[string]any{"reasoning": "Subagent reasoning."})
	r.feed(t, eventAgentDone, map[string]any{"reason": "completed"})
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_1", "Subagent result.", "completed"))

	assert.Equal(t, []string{
		contracts.ClineEventToolStarted, contracts.ClineEventToolFinished,
		contracts.ClineEventReasoningFinished, contracts.ClineEventAssistantFinished,
	}, rowEvents(t, child), "the child's calls and messages reach the child")
	assert.Equal(t, []string{contracts.ClineEventToolStarted, contracts.ClineEventToolFinished}, rowEvents(t, &r.sink.Sink),
		"the lead shows its spawn call alone")
	item, _ = r.sink.BackgroundTask("spawn_1")
	assert.Equal(t, bgtask.StatusCompleted, item.Status)
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1, "the child's answer ends its transcript as a report")
	assert.Equal(t, "Subagent result.", reports[0][contracts.NotificationFieldText])
}

// A configured agent (`subagent_<name>_<hash>`, from `.cline/agents/`) runs a
// child that asks nothing, with the same untagged live output as spawn_agent.
// It takes its task as `prompt`.
func TestAConfiguredAgentGetsItsOwnTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	const tool = "subagent_reviewer_1a2b"
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "agent_1", "toolName": tool, "input": map[string]any{"prompt": "Review the change.\nThen report."}})

	item, ok := r.sink.BackgroundTask("agent_1")
	require.True(t, ok, "the configured agent opens a registry row")
	assert.Equal(t, bgtask.KindSubagent, item.Kind)
	assert.Equal(t, "Review the change.", item.Title)
	child := r.childSink(t, "agent_1")

	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, eventAssistantDelta, map[string]any{"text": "Looks right."})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Looks right."})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "agent_1", "toolName": tool, "output": map[string]any{"text": "Looks right.", "iterations": 1, "finishReason": "completed"}})

	assert.Equal(t, []string{contracts.ClineEventAssistantFinished}, rowEvents(t, child), "the child's answer reaches the child")
	assert.Equal(t, []string{contracts.ClineEventToolStarted, contracts.ClineEventToolFinished}, rowEvents(t, &r.sink.Sink),
		"the lead shows its call alone, and not the child's answer as its own")
	item, _ = r.sink.BackgroundTask("agent_1")
	assert.Equal(t, bgtask.StatusCompleted, item.Status)
}

func TestAParallelToolOfTheLeadStaysWithTheLead(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look."))
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "read_1", "toolName": "read_files", "input": map[string]any{}})
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "read_1", "toolName": "read_files", "output": []any{}})
	assert.Equal(t, []string{contracts.ClineEventToolStarted, contracts.ClineEventToolStarted, contracts.ClineEventToolFinished}, rowEvents(t, &r.sink.Sink))
	assert.Empty(t, rowEvents(t, r.childSink(t, "spawn_1")))
}

func TestASubagentOfASubagentGetsItsOwnTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", "Outer."))
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_2", "Inner."))
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Inner answer."})
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_2", "Inner answer.", "completed"))
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Outer answer."})
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_1", "Outer answer.", "completed"))

	outer := r.childSink(t, "spawn_1")
	assert.Equal(t, []string{contracts.ClineEventToolStarted, contracts.ClineEventToolFinished, contracts.ClineEventAssistantFinished}, rowEvents(t, outer))
	// The inner call belongs to the outer child's transcript, and its row to the
	// outer child's sink: production files every row under the root owner.
	inner, ok := outer.BackgroundTask("spawn_2")
	require.True(t, ok)
	outerItem, _ := r.sink.BackgroundTask("spawn_1")
	assert.Equal(t, outerItem.ChildAgentID, inner.ParentAgentID, "the inner subagent is the outer one's child")
	assert.Equal(t, []string{contracts.ClineEventAssistantFinished}, rowEvents(t, outer.Child(inner.ChildAgentID)))
	assert.Equal(t, bgtask.StatusCompleted, inner.Status)
}

func TestAFailedSubagentFailsItsRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look."))
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "spawn_1", "toolName": contracts.ClineToolSpawnAgent, "error": "the model failed"})
	item, _ := r.sink.BackgroundTask("spawn_1")
	assert.Equal(t, bgtask.StatusFailed, item.Status)
}

func TestSpawnStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, bgtask.StatusCompleted, spawnStatus("completed", ""))
	assert.Equal(t, bgtask.StatusCompleted, spawnStatus("max_iterations", ""))
	assert.Equal(t, bgtask.StatusCompleted, spawnStatus("", ""))
	assert.Equal(t, bgtask.StatusStopped, spawnStatus("aborted", "aborted"))
	assert.Equal(t, bgtask.StatusFailed, spawnStatus("error", ""))
	assert.Equal(t, bgtask.StatusFailed, spawnStatus("mistake_limit", ""))
	assert.Equal(t, bgtask.StatusFailed, spawnStatus("completed", "boom"))
}

func TestTheTurnsEndStopsASubagentThatStillRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Delegate.")
	r.emit(contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look."))
	r.emit(eventIterationStarted, map[string]any{"iteration": 1})
	r.emit(contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_sub_1", "toolName": "run_commands", "input": map[string]any{}})
	r.emit(eventAssistantDelta, map[string]any{"text": "Half"})
	r.endRun(t, requestID, contracts.ClineRunReasonAborted)

	item, _ := r.sink.BackgroundTask("spawn_1")
	assert.Equal(t, bgtask.StatusStopped, item.Status)
	child := r.childSink(t, "spawn_1")
	messages := child.Messages()
	events := rowEvents(t, child)
	assert.Equal(t, []string{contracts.ClineEventToolStarted, contracts.ClineEventAssistantFinished, contracts.ClineEventToolStarted}, events)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
	assert.False(t, r.agent.spawnRuns())
}

func TestParallelSubagentsTakeTheirTranscriptsFromClinesStore(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	root := r.sessionID()
	now := time.Now().UnixMilli()
	r.hub.setSessions(
		map[string]any{"sessionId": root + "__agent_b", "createdAt": now + 2, "status": "completed", "metadata": map[string]any{"parentSessionId": root, "agentId": "agent_b", "prompt": "Task B."}},
		map[string]any{"sessionId": root + "__agent_a", "createdAt": now + 1, "status": "completed", "metadata": map[string]any{"parentSessionId": root, "agentId": "agent_a", "prompt": "Task A."}},
		map[string]any{"sessionId": "elsewhere__agent_x", "createdAt": now, "status": "completed", "metadata": map[string]any{"parentSessionId": "elsewhere", "prompt": "Task A."}},
	)
	r.hub.store(root+"__agent_a", []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Task A."}}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "A thinks."},
			map[string]any{"type": "tool_use", "id": "a_call", "name": "run_commands", "input": map[string]any{"commands": []string{"ls"}}},
		}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "a_call", "name": "run_commands", "content": []any{map[string]any{"query": "ls", "result": "x", "success": true}}}}},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "A done."}}},
	})
	r.hub.store(root+"__agent_b", []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Task B."}}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "b_call", "name": "editor", "input": map[string]any{}},
		}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "b_call", "name": "editor", "content": "denied", "is_error": true}}},
		map[string]any{"role": "assistant", "content": "B done."},
	})

	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_a", "Task A."))
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_b", "Task B."))
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	// Interleaved output that nothing attributes.
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "a_call", "toolName": "run_commands", "input": map[string]any{}})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "A done."})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "a_call", "toolName": "run_commands", "output": []any{}})
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_a", "A done.", "completed"))
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_b", "B done.", "completed"))

	assert.Equal(t, []string{
		contracts.ClineEventToolStarted, contracts.ClineEventToolStarted,
		contracts.ClineEventToolFinished, contracts.ClineEventToolFinished,
	}, rowEvents(t, &r.sink.Sink), "no child output reaches the lead")

	waitFor(t, func() bool {
		a, _ := r.sink.BackgroundTask("spawn_a")
		b, _ := r.sink.BackgroundTask("spawn_b")
		return a.Status == bgtask.StatusCompleted && b.Status == bgtask.StatusCompleted
	}, "both rows close once their transcripts are written")

	childA := r.childSink(t, "spawn_a")
	assert.Equal(t, []string{
		contracts.ClineEventReasoningFinished, contracts.ClineEventToolStarted,
		contracts.ClineEventToolFinished, contracts.ClineEventAssistantFinished,
	}, rowEvents(t, childA))
	childB := r.childSink(t, "spawn_b")
	eventsB := rowEvents(t, childB)
	assert.Equal(t, []string{contracts.ClineEventToolStarted, contracts.ClineEventToolFinished, contracts.ClineEventAssistantFinished}, eventsB)
	for _, message := range childB.Messages() {
		if message.Closing {
			assert.Equal(t, "denied", payloadOf(t, message)["error"])
		}
	}
}

func TestABackfillWaitsForTheChildToFinish(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	root := r.sessionID()
	now := time.Now().UnixMilli()
	calls := 0
	r.hub.handle(commandSessionList, func(fakeCommand) fakeReply {
		calls++
		status := "running"
		if calls > 1 {
			status = "completed"
		}
		return fakeReply{Payload: map[string]any{"sessions": []any{
			map[string]any{"sessionId": root + "__agent_a", "createdAt": now, "status": status, "metadata": map[string]any{"parentSessionId": root, "prompt": "Task A."}},
			map[string]any{"sessionId": root + "__agent_b", "createdAt": now, "status": status, "metadata": map[string]any{"parentSessionId": root, "prompt": "Task B."}},
		}}}
	})
	r.hub.store(root+"__agent_a", []any{
		map[string]any{"role": "user", "content": "Task A."},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "A done."}}},
	})
	r.hub.store(root+"__agent_b", []any{})
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_a", "Task A."))
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_b", "Task B."))
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_a", "A done.", "completed"))
	waitFor(t, func() bool {
		item, _ := r.sink.BackgroundTask("spawn_a")
		return item.Status == bgtask.StatusCompleted
	}, "the row closes after the transcript")
	assert.Equal(t, []string{contracts.ClineEventAssistantFinished}, rowEvents(t, r.childSink(t, "spawn_a")))
}

func TestABackfillKeepsWhatReachedTheTranscriptLive(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sink := &agenttest.Sink{}
	childID, err := sink.EnsureChildAgent("span-1", "row-1", "Child")
	require.NoError(t, err)
	child := newChildTranscript(agent.NewProviderServices(sink), childID)
	child.written.texts = 1
	child.written.reasoning = 1
	child.written.tools["t1"] = true
	messages := mustJSON(t, []any{
		map[string]any{"role": "user", "content": "Task."},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "Live thought."},
			map[string]any{"type": "text", "text": "Live text."},
			map[string]any{"type": "tool_use", "id": "t1", "name": "read_files", "input": map[string]any{}},
		}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "name": "read_files", "content": []any{}}}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "Later thought."},
			map[string]any{"type": "tool_use", "id": "t2", "name": "editor", "input": map[string]any{}},
		}},
	})
	r.agent.writeStoredConversation(backfillJob{target: child, label: "test"}, "stored", messages)
	childSink := sink.Child(child.childID)
	assert.Equal(t, []string{contracts.ClineEventReasoningFinished, contracts.ClineEventToolStarted, contracts.ClineEventToolStarted}, rowEvents(t, childSink),
		"the later thought, the unfinished call, and its close")
	last := childSink.Messages()[len(childSink.Messages())-1]
	assert.True(t, last.Closing)
	assert.Equal(t, agent.MessageCompletionError, last.Completion, "a call with no stored result did not finish")
}

func TestAChildSessionIsTakenOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.hub.setSessions(
		map[string]any{"sessionId": root + "__agent_1", "createdAt": 100, "status": "completed", "metadata": map[string]any{"parentSessionId": root, "prompt": "Same."}},
		map[string]any{"sessionId": root + "__agent_2", "createdAt": 200, "status": "completed", "metadata": map[string]any{"parentSessionId": root, "prompt": "Same."}},
	)
	job := backfillJob{match: spawnSessionMatch(&spawnCall{rootSession: root, task: "Same."})}
	first, ok, err := r.agent.findStoredChild(job)
	require.NoError(t, err)
	require.True(t, ok)
	second, ok, err := r.agent.findStoredChild(job)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, root+"__agent_1", first.SessionID, "the earliest child session first")
	assert.Equal(t, root+"__agent_2", second.SessionID)
	_, ok, err = r.agent.findStoredChild(job)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestAChildSessionCreatedBeforeTheCallIsNotItsChild(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.hub.setSessions(map[string]any{"sessionId": root + "__agent_1", "createdAt": 1000, "status": "completed", "metadata": map[string]any{"parentSessionId": root, "prompt": "Same."}})
	job := backfillJob{match: spawnSessionMatch(&spawnCall{rootSession: root, task: "Same."}), notBefore: 1000 + 2*backfillClockSlack}
	_, ok, err := r.agent.findStoredChild(job)
	require.NoError(t, err)
	assert.False(t, ok)
}

// ambiguousSpawns starts two parallel spawns whose output nothing attributes,
// and ends the first, which starts its backfill.
func (r *rig) ambiguousSpawns(t *testing.T) {
	t.Helper()
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_a", "Task A."))
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_b", "Task B."))
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_a", "A done.", "completed"))
}

// Cline can store no finished session for a child, or fail to list them. The
// backfill tries at each of its waits, then gives up, and the row still closes
// with the child's report: a row that stayed Running would never end.
func TestABackfillThatFindsNoChildClosesTheRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	clock := testutil.NewQuartzMock(t)
	r.agent.clock = clock
	trap := clock.Trap().NewTimer("cline", "backfill")
	t.Cleanup(trap.Close)
	lists := 0
	r.hub.handle(commandSessionList, func(fakeCommand) fakeReply {
		lists++
		if lists == 1 {
			return fakeReply{Code: "internal_error", Message: "the index is locked"}
		}
		return fakeReply{Payload: map[string]any{"sessions": []any{}}}
	})
	r.ambiguousSpawns(t)

	ctx := testutil.DeadlineContext(t)
	for _, wait := range backfillWaits[1:] {
		assert.Equal(t, wait, testutil.WaitForTimer(t, ctx, trap))
		item, _ := r.sink.BackgroundTask("spawn_a")
		assert.Equal(t, bgtask.StatusRunning, item.Status, "the row waits while the backfill tries")
		clock.Advance(wait).MustWait(ctx)
	}
	waitFor(t, func() bool {
		item, _ := r.sink.BackgroundTask("spawn_a")
		return item.Status == bgtask.StatusCompleted
	}, "the row closes once the backfill gives up")
	assert.Len(t, r.hub.commandsNamed(commandSessionList), len(backfillWaits), "one read for each wait")
	child := r.childSink(t, "spawn_a")
	assert.Empty(t, rowEvents(t, child), "no stored session, no transcript beyond the task")
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "A done.", reports[0][contracts.NotificationFieldText])
}

// A stored child whose conversation cannot be read writes no transcript, and its
// row still closes.
func TestABackfillThatCannotReadTheChildClosesTheRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.hub.setSessions(map[string]any{
		"sessionId": root + "__agent_a", "createdAt": time.Now().UnixMilli(), "status": "completed",
		"metadata": map[string]any{"parentSessionId": root, "prompt": "Task A."},
	})
	r.hub.handle(commandSessionMessages, func(fakeCommand) fakeReply {
		return fakeReply{Code: "internal_error", Message: "the file is gone"}
	})
	r.ambiguousSpawns(t)
	waitFor(t, func() bool {
		item, _ := r.sink.BackgroundTask("spawn_a")
		return item.Status == bgtask.StatusCompleted
	}, "the row closes")
	assert.Len(t, r.hub.commandsNamed(commandSessionMessages), 1, "the backfill reads once and stops")
	assert.Empty(t, rowEvents(t, r.childSink(t, "spawn_a")))
}

// A stop during a backfill's wait ends the backfill, and its row is final when
// Stop returns.
func TestAStopDuringABackfillFinishesTheRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.hub.setSessions(map[string]any{
		"sessionId": root + "__agent_a", "createdAt": time.Now().UnixMilli(), "status": "running",
		"metadata": map[string]any{"parentSessionId": root, "prompt": "Task A."},
	})
	r.ambiguousSpawns(t)
	_, ok := r.hub.waitCommand(commandSessionList)
	require.True(t, ok, "the backfill reads the store")
	r.agent.Stop()
	for _, key := range []string{"spawn_a", "spawn_b"} {
		item, ok := r.sink.BackgroundTask(key)
		require.True(t, ok)
		assert.True(t, item.Status.IsFinished(), "%s is final when Stop returns", key)
	}
	item, _ := r.sink.BackgroundTask("spawn_a")
	assert.Equal(t, bgtask.StatusCompleted, item.Status, "the call completed before the stop")
}

func TestASubagentWithNoTaskTakesTheFallbackTitle(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", " \n "))
	item, ok := r.sink.BackgroundTask("spawn_1")
	require.True(t, ok)
	assert.Equal(t, spawnTitleFallback, item.Title)
	assert.Empty(t, r.childSink(t, "spawn_1").Messages(), "no task, no prompt row")
}

// A child transcript that the database refuses leaves the row without a link.
// The child's output then reaches no transcript -- never the lead's -- and the
// row still closes with the call.
func TestASubagentWithNoTranscriptStillClosesItsRow(t *testing.T) {
	t.Parallel()
	r := newRig(t, withNoChild)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look."))
	item, ok := r.sink.BackgroundTask("spawn_1")
	require.True(t, ok, "the row exists without a child")
	assert.Empty(t, item.ChildAgentID)
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_sub", "toolName": "run_commands", "input": map[string]any{}})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Child text."})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "call_sub", "toolName": "run_commands", "output": []any{}})
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_1", "Done.", "completed"))
	assert.Equal(t, []string{contracts.ClineEventToolStarted, contracts.ClineEventToolFinished}, rowEvents(t, &r.sink.Sink),
		"the lead shows its call alone")
	item, _ = r.sink.BackgroundTask("spawn_1")
	assert.Equal(t, bgtask.StatusCompleted, item.Status)
	assert.False(t, r.agent.spawnRuns())
}

// A child that completed wrote its text in full, and a call that it still held
// open did not finish.
func TestASubagentThatCompletesClosesWhatItLeftOpen(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look."))
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_sub", "toolName": "run_commands", "input": map[string]any{}})
	r.feed(t, eventAssistantDelta, map[string]any{"text": "Streamed."})
	r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_1", "Done.", "completed"))

	child := r.childSink(t, "spawn_1")
	messages := child.Messages()
	assert.Equal(t, []string{contracts.ClineEventToolStarted, contracts.ClineEventAssistantFinished, contracts.ClineEventToolStarted}, rowEvents(t, child))
	require.Len(t, messages, 4, "the task, the call, the text, and the call's close")
	assert.Equal(t, agent.MessageCompletionComplete, messages[2].Completion)
	assert.True(t, messages[3].Closing)
	assert.Equal(t, agent.MessageCompletionError, messages[3].Completion)
}

// A failed child ends its streamed text with the error, and its report states
// the error when the call states no answer.
func TestAFailedSubagentReportsItsError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", "Look."))
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, eventAssistantDelta, map[string]any{"text": "Half"})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "spawn_1", "toolName": contracts.ClineToolSpawnAgent, "error": " the model failed "})
	item, _ := r.sink.BackgroundTask("spawn_1")
	assert.Equal(t, bgtask.StatusFailed, item.Status)
	child := r.childSink(t, "spawn_1")
	messages := child.Messages()
	assert.Equal(t, agent.MessageCompletionError, messages[len(messages)-1].Completion)
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "the model failed", reports[0][contracts.NotificationFieldText])
}

// A call cannot end before the calls that its child made: one that its parent's
// end cut short stops with it, or fails with it.
func TestANestedSubagentEndsWithItsParent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		outer  bgtask.Status
		inner  bgtask.Status
	}{
		{contracts.ClineRunReasonCompleted, bgtask.StatusCompleted, bgtask.StatusStopped},
		{contracts.ClineRunReasonError, bgtask.StatusFailed, bgtask.StatusFailed},
		{contracts.ClineRunReasonAborted, bgtask.StatusStopped, bgtask.StatusStopped},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.feedTurn(t)
			r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_1", "Outer."))
			r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
			r.feed(t, contracts.ClineEventToolStarted, spawnStart("spawn_2", "Inner."))
			r.feed(t, contracts.ClineEventToolFinished, spawnFinish("spawn_1", "Outer answer.", tc.reason))
			outer, _ := r.sink.BackgroundTask("spawn_1")
			assert.Equal(t, tc.outer, outer.Status)
			inner, ok := r.childSink(t, "spawn_1").BackgroundTask("spawn_2")
			require.True(t, ok)
			assert.Equal(t, tc.inner, inner.Status)
			assert.False(t, r.agent.spawnRuns(), "no call of the ended tree runs")
		})
	}
}

func TestSpawnInputTask(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "T", spawnInput{Task: "T", Prompt: "P"}.task(), "spawn_agent states its task")
	assert.Equal(t, "P", spawnInput{Task: " \n", Prompt: "P"}.task(), "a configured agent states its prompt")
	assert.Empty(t, spawnInput{}.task())
}

func TestSpawnCompletions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status      bgtask.Status
		text, tools agent.MessageCompletion
	}{
		{bgtask.StatusCompleted, agent.MessageCompletionComplete, agent.MessageCompletionError},
		{bgtask.StatusFailed, agent.MessageCompletionError, agent.MessageCompletionError},
		{bgtask.StatusStopped, agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted},
		{bgtask.StatusInterrupted, agent.MessageCompletionInterrupted, agent.MessageCompletionInterrupted},
	} {
		text, tools := spawnCompletions(tc.status)
		assert.Equal(t, tc.text, text, tc.status.String())
		assert.Equal(t, tc.tools, tools, tc.status.String())
	}
	assert.Equal(t, bgtask.StatusFailed, stoppedWith(bgtask.StatusFailed))
	assert.Equal(t, bgtask.StatusStopped, stoppedWith(bgtask.StatusCompleted))
	assert.Equal(t, bgtask.StatusStopped, stoppedWith(bgtask.StatusStopped))
}

// The stored conversation can hold what the transcript cannot show: a call with
// no id, a result of a call that the transcript never opened, and blank text.
// None of it reaches the transcript, and a conversation that is not a list
// writes nothing.
func TestWriteStoredConversationSkipsWhatItCannotShow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sink := &agenttest.Sink{}
	childID, err := sink.EnsureChildAgent("span-1", "row-1", "Child")
	require.NoError(t, err)
	child := newChildTranscript(agent.NewProviderServices(sink), childID)
	r.agent.writeStoredConversation(backfillJob{target: child, label: "test"}, "stored", json.RawMessage(`{"not":"a list"}`))
	assert.Empty(t, sink.Child(childID).Messages())

	messages := mustJSON(t, []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "  "},
			map[string]any{"type": "thinking", "thinking": ""},
			map[string]any{"type": "tool_use", "id": "", "name": "read_files", "input": map[string]any{}},
		}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "never_opened", "content": "x"}}},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Kept."}}},
	})
	r.agent.writeStoredConversation(backfillJob{target: child, label: "test"}, "stored", messages)
	assert.Equal(t, []string{contracts.ClineEventAssistantFinished}, rowEvents(t, sink.Child(childID)))
}
