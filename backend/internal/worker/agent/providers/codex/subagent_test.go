package codex

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The `turn/started` notification confirms that Codex accepted a turn.
// sendTurnStart uses it because response timing differs across Codex versions.
func TestCodex_TurnStartedIsTheAckThatReleasesASend(t *testing.T) {
	t.Parallel()
	pending := newCodexTurnStart("t-1", nil, turnSettings{})
	ack := pending.done
	a := &Agent{pendingStart: pending}
	a.threadID = "t-1"
	a.sink = agenttest.Nop()

	a.handleTurnStarted(json.RawMessage(`{"threadId":"t-1","turn":{"id":"turn-1"}}`))

	select {
	case <-ack:
	default:
		t.Fatal("turn/started must release the waiting send")
	}
	assert.Nil(t, a.pendingStart, "the waiter is cleared, so a later turn cannot close it twice")
	assert.Equal(t, "turn-1", a.mainTurn.id, "and the turn id it carries is what makes the next send steer")
}

// A CHILD's turn/started must not release the primary send: it belongs to a
// collab subagent thread, and the main thread has not accepted anything yet.
func TestCodex_AChildTurnStartedDoesNotReleaseTheSend(t *testing.T) {
	t.Parallel()
	pending := newCodexTurnStart("t-main", nil, turnSettings{})
	ack := pending.done
	a := &Agent{pendingStart: pending}
	a.threadID = "t-main"
	a.sink = agenttest.Nop()

	a.handleTurnStarted(json.RawMessage(`{"threadId":"t-child","turn":{"id":"turn-child"}}`))

	select {
	case <-ack:
		t.Fatal("a child's turn must not acknowledge the main thread's send")
	default:
	}
	assert.NotNil(t, a.pendingStart, "the main send is still waiting")
}

func TestCodex_MultiAgentV2ChildDoesNotAdvertiseDirectInput(t *testing.T) {
	t.Parallel()

	assert.False(t, codexProvider{}.ChildCapabilities(nil).AcceptsMessages,
		"Codex rejects direct app-server input for Multi-Agent V2 children")
	_, sendsDirectInput := any(&Agent{}).(agent.ChildSteerer)
	assert.False(t, sendsDirectInput)
	_, interruptsChild := any(&Agent{}).(agent.ChildInterrupter)
	assert.True(t, interruptsChild, "the app server still permits direct child interruption")
}

// TestCodex_InterruptChildUnknownThreadReturnsRetryable mirrors the send path
// for interrupt: a NotFound misclassification would make the frontend drop the
// child tab as unavailable even though the root process is running.
func TestCodex_InterruptChildUnknownThreadReturnsRetryable(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	err := a.InterruptChild("unknown-thread", agent.StopContext{})
	assert.ErrorIs(t, err, agent.ErrChildRouteNotReady)
}

// Codex declares `prompt` on the collabAgentToolCall thread item
// (app-server-protocol schema/typescript/v2/ThreadItem.ts). The spawn item identifies the receiver threads.
// The worker creates the child transcript from agentsStates later. The index retains the prompt until then.
func TestCodex_CollabPromptHeldUntilTheChildExists(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	a.rememberCollabChildPrompt("thread-1", "Write the essay.")

	// Spent once, so a second child creation cannot repeat it.
	assert.Equal(t, "Write the essay.", a.takeCollabChildPrompt("thread-1"))
	assert.Empty(t, a.takeCollabChildPrompt("thread-1"))
}

// The spawn's own prompt wins; the later collab tools (send/wait) on the same
// thread carry none and must not clear or replace it.
func TestCodex_CollabPromptFirstWriteWins(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	a.rememberCollabChildPrompt("thread-1", "first")
	a.rememberCollabChildPrompt("thread-1", "second")
	a.rememberCollabChildPrompt("thread-1", "")
	assert.Equal(t, "first", a.takeCollabChildPrompt("thread-1"))
}

func TestCodex_CollabPromptIgnoresEmptyInput(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	a.rememberCollabChildPrompt("", "x")
	a.rememberCollabChildPrompt("thread-1", "")
	assert.Empty(t, a.collabChildren)
}

// A completed run drops its spent prompt but keeps the route that a follow-up
// turn needs.
func TestCodex_CollabPromptDroppedOnFinalChild(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	a.rememberCollabChildPrompt("thread-1", "Write the essay.")
	a.finishCollabChildRun("thread-1")
	assert.Empty(t, a.takeCollabChildPrompt("thread-1"))
}

// The spawn item's prompt reaches the index through the parse.
func TestCodex_ParseCollabToolCallCarriesThePrompt(t *testing.T) {
	t.Parallel()

	collab := parseCollabToolCall(json.RawMessage(
		`{"type":"collabAgentToolCall","tool":"spawnAgent","status":"inProgress",` +
			`"receiverThreadIds":["thread-1"],"prompt":"Write the essay.","model":null}`))
	require.NotNil(t, collab)
	assert.Equal(t, "Write the essay.", collab.Prompt)
	assert.Equal(t, []string{"thread-1"}, collab.ReceiverThreadIds)
}

// `prompt` is nullable on the wire for the non-spawn collab tools.
func TestCodex_ParseCollabToolCallToleratesNullPrompt(t *testing.T) {
	t.Parallel()

	collab := parseCollabToolCall(json.RawMessage(
		`{"type":"collabAgentToolCall","tool":"waitForAgent","receiverThreadIds":["thread-1"],"prompt":null}`))
	require.NotNil(t, collab)
	assert.Empty(t, collab.Prompt)
}

// A wrongly typed field costs that FIELD, never the whole spawn.
//
// encoding/json fills every field it can read and then reports the one it could not,
// so the id and the receiver list are already correct when the error arrives. Dropping
// the item left the reader with a live subagent card the chat drew and no background
// task row and no child transcript route behind it, because one agent state carried a
// number where a word belongs.
func TestCodex_ParseCollabToolCallKeepsTheFieldsThatDecoded(t *testing.T) {
	t.Parallel()

	collab := parseCollabToolCall(json.RawMessage(
		`{"type":"collabAgentToolCall","tool":"spawnAgent","status":"inProgress",` +
			`"receiverThreadIds":["thread-1"],"prompt":"Write the essay.",` +
			`"agentsStates":{"thread-1":{"status":7}}}`))
	require.NotNil(t, collab, "a wrongly typed agent state must not discard the item")
	assert.Equal(t, []string{"thread-1"}, collab.ReceiverThreadIds, "the receiver list decoded before the bad field")
	assert.Equal(t, "spawnAgent", collab.Tool)
	assert.Equal(t, "Write the essay.", collab.Prompt)
}

// A SYNTAX error is different: no field was read, so there is nothing to keep.
func TestCodex_ParseCollabToolCallRefusesBrokenJSON(t *testing.T) {
	t.Parallel()

	assert.Nil(t, parseCollabToolCall(json.RawMessage(`{"tool":"spawnAgent",`)))
	assert.Nil(t, parseCollabToolCall(json.RawMessage(`["not an object"]`)))
}

func TestCodex_AgentPathTitleUsesTheLastNonEmptySegment(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "probe_child", codexAgentPathTitle("/root/probe_child"))
	assert.Equal(t, "probe_child", codexAgentPathTitle(" /root/probe_child/// "))
	assert.Equal(t, "probe_child", codexAgentPathTitle("probe_child"))
	assert.Empty(t, codexAgentPathTitle("///"))
	assert.Empty(t, codexAgentPathTitle(""))
}

func TestCodex_RootPathClassificationUsesTheCompleteCanonicalPath(t *testing.T) {
	t.Parallel()

	assert.True(t, codexAgentPathIsRoot("/root"))
	assert.True(t, codexAgentPathIsRoot(" /root/// "))
	assert.False(t, codexAgentPathIsRoot("/root/root"), "a child can use root as its task name")
	assert.False(t, codexAgentPathIsRoot("root"))
	assert.False(t, codexAgentPathIsRoot(""))
}

func TestCodex_StaleSendInputAndResumeCompletedDoesNotReviveFinishedChild(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"sendInput", "resumeAgent"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agent.NewProviderServices(sink))

			// Job 1 spawns and completes.
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"collabAgentToolCall","id":"spawn-1","tool":"spawnAgent","receiverThreadIds":["child-1"],"prompt":"Job 1"}}}`)))
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"ct-1","status":"completed"}}}`)))
			row, ok := sink.BackgroundTask("child-1")
			require.True(t, ok)
			require.Equal(t, bgtask.StatusSucceeded, row.Status)

			// Job 2 starts via sendInput/resumeAgent item/started, then finishes before item/completed lands.
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t2","item":{"type":"collabAgentToolCall","id":"wake-1","tool":"`+tool+`","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"ct-2"}}}`)))
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"ct-2","status":"completed"}}}`)))
			row, _ = sink.BackgroundTask("child-1")
			require.Equal(t, bgtask.StatusSucceeded, row.Status)
			revivesBefore := len(sink.RevivedTasks())

			// Stale item/completed for the same wake-1 carrying running snapshot must NOT revive child-1.
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t2","item":{"type":"collabAgentToolCall","id":"wake-1","tool":"`+tool+`","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
			row, _ = sink.BackgroundTask("child-1")
			assert.Equal(t, bgtask.StatusSucceeded, row.Status, "stale %s item/completed must not revive finished child", tool)
			assert.Len(t, sink.RevivedTasks(), revivesBefore)

			// A fresh wake item with no prior item/started (item/completed only) must still revive.
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t3","item":{"type":"collabAgentToolCall","id":"wake-2","tool":"`+tool+`","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
			row, _ = sink.BackgroundTask("child-1")
			assert.Equal(t, bgtask.StatusRunning, row.Status, "fresh %s item/completed without prior item/started must revive child", tool)
		})
	}
}

func TestCodex_StaleCollabCompletedDoesNotOverwritePausedChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	// Spawn child-1 and pause it via an interrupted child turn.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"collabAgentToolCall","id":"spawn-1","tool":"spawnAgent","receiverThreadIds":["child-1"],"prompt":"Job 1","agentsStates":{"child-1":{"status":"running"}}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"ct-1"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"ct-1","status":"interrupted"}}}`)))
	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusPaused, row.Status)

	// Stale item/completed for spawnAgent, wait, and closeAgent must not overwrite StatusPaused.
	for _, item := range []string{
		`{"type":"collabAgentToolCall","id":"spawn-1","tool":"spawnAgent","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"pendingInit"}}}`,
		`{"type":"collabAgentToolCall","id":"wait-1","tool":"wait","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}`,
		`{"type":"collabAgentToolCall","id":"close-1","tool":"closeAgent","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}`,
	} {
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t1","item":`+item+`}}`)))
		row, _ = sink.BackgroundTask("child-1")
		assert.Equal(t, bgtask.StatusPaused, row.Status)
	}

	// For sendInput and resumeAgent: a stale item/completed whose item/started already ran
	// and then paused must keep StatusPaused; a fresh item/completed-only wake must resume.
	for _, tool := range []string{"sendInput", "resumeAgent"} {
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t2","item":{"type":"collabAgentToolCall","id":"wake-`+tool+`","tool":"`+tool+`","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"ct-`+tool+`"}}}`)))
		row, _ = sink.BackgroundTask("child-1")
		require.Equal(t, bgtask.StatusRunning, row.Status)

		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"ct-`+tool+`","status":"interrupted"}}}`)))
		row, _ = sink.BackgroundTask("child-1")
		require.Equal(t, bgtask.StatusPaused, row.Status)

		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t2","item":{"type":"collabAgentToolCall","id":"wake-`+tool+`","tool":"`+tool+`","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
		row, _ = sink.BackgroundTask("child-1")
		assert.Equal(t, bgtask.StatusPaused, row.Status, "stale %s item/completed must not overwrite StatusPaused", tool)

		// Fresh item/completed-only wake resumes from StatusPaused to StatusRunning.
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t3","item":{"type":"collabAgentToolCall","id":"wake-fresh-`+tool+`","tool":"`+tool+`","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
		row, _ = sink.BackgroundTask("child-1")
		assert.Equal(t, bgtask.StatusRunning, row.Status, "fresh %s item/completed must resume paused child", tool)

		// Pause again for the next iteration.
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"ct-reset-`+tool+`","status":"interrupted"}}}`)))
		row, _ = sink.BackgroundTask("child-1")
		require.Equal(t, bgtask.StatusPaused, row.Status)
	}

	// Pause via V1 collabAgentToolCall agentsStates "interrupted" must also guard against stale running snapshots.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t4","item":{"type":"collabAgentToolCall","id":"wake-v1-pause","tool":"resumeAgent","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
	row, _ = sink.BackgroundTask("child-1")
	require.Equal(t, bgtask.StatusRunning, row.Status)

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t4","item":{"type":"collabAgentToolCall","id":"wait-pause","tool":"wait","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"interrupted"}}}}}`)))
	row, _ = sink.BackgroundTask("child-1")
	require.Equal(t, bgtask.StatusPaused, row.Status)

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t4","item":{"type":"collabAgentToolCall","id":"wait-stale","tool":"wait","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
	row, _ = sink.BackgroundTask("child-1")
	assert.Equal(t, bgtask.StatusPaused, row.Status, "stale wait item/completed must not overwrite V1 collab-paused child")
}

func TestCodex_SubAgentActivityInteractedDoesNotReviveFinishedChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"spawn-1","agentThreadId":"child-1","agentPath":"/root/reviewer","kind":"started"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"done-1","agentThreadId":"child-1","agentPath":"/root/reviewer","kind":"completed"}}}`)))
	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Duplicate interacted frames (started + completed) must not revive a finished child.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t2","item":{"type":"subAgentActivity","id":"int-1","agentThreadId":"child-1","agentPath":"/root/reviewer","kind":"interacted"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t2","item":{"type":"subAgentActivity","id":"int-1","agentThreadId":"child-1","agentPath":"/root/reviewer","kind":"interacted"}}}`)))
	assert.Empty(t, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("child-1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Job 2 starts via child turn/started, which revives the row.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn-2"}}}`)))
	assert.Equal(t, []string{"child-1"}, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("child-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	// Interacted while running updates ActiveForm without duplicating revival.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t3","item":{"type":"subAgentActivity","id":"int-2","agentThreadId":"child-1","agentPath":"/root/reviewer","kind":"interacted"}}}`)))
	row, _ = sink.BackgroundTask("child-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "received input", row.ActiveForm)

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"child-turn-2","status":"completed"}}}`)))
	row, _ = sink.BackgroundTask("child-1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestCodex_SubAgentActivityPausedSurvivesDuplicateStarted(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"spawn-1","agentThreadId":"child-1","agentPath":"/root/reviewer","kind":"started"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"intr-1","agentThreadId":"child-1","agentPath":"/root/reviewer","kind":"interrupted"}}}`)))
	// Duplicate delivery of the same started activity item on item/completed.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"spawn-1","agentThreadId":"child-1","agentPath":"/root/reviewer","kind":"started"}}}`)))

	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusPaused, row.Status)
}

func TestCodex_ResumedChildLookupUnroutedCloseAndUnseenBuffering(t *testing.T) {
	t.Parallel()
	t.Run("resumed child resolves durable child ID from registry and closes", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		services := agent.NewProviderServices(sink)
		childID, err := services.EnsureChildAgent(agent.ChildAgentSpec{
			RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "spawn-old", ProviderChildKey: "child-resumed", Title: "Resumed",
		})
		require.NoError(t, err)
		require.NoError(t, services.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: "child-resumed", Kind: bgtask.KindSubagent, ChildAgentID: childID, Title: "Resumed", Status: bgtask.StatusRunning,
		}))

		a := newCodexAgentWithSink(services)
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-resumed","turn":{"id":"turn-1","status":"completed"}}}`)))
		row, ok := sink.BackgroundTask("child-resumed")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status)
		assert.NotEmpty(t, sink.Child(childID).Messages())
	})

	t.Run("unrouted completeCodexChildRun still closes registry row", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		services := agent.NewProviderServices(sink)
		require.NoError(t, services.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: "child-orphan", Kind: bgtask.KindSubagent, Title: "Orphan", Status: bgtask.StatusRunning,
		}))
		a := newCodexAgentWithSink(services)
		a.completeCodexChildRun("child-orphan", codexChildTransition{
			status: bgtask.StatusSucceeded, completion: agent.MessageCompletionComplete,
		})
		row, ok := sink.BackgroundTask("child-orphan")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	})

	t.Run("unseen brand-new child buffers turn/started until started arrives", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newCodexAgentWithSink(agent.NewProviderServices(sink))

		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-new","turn":{"id":"turn-1"}}}`)))
		a.Mu.Lock()
		state := a.collabChildren["child-new"]
		require.NotNil(t, state)
		assert.Len(t, state.pendingEvents, 1)
		a.Mu.Unlock()

		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"spawn-new","agentThreadId":"child-new","agentPath":"/root/new","kind":"started"}}}`)))
		assert.Equal(t, "turn-1", a.childTurnID("child-new"))
		row, ok := sink.BackgroundTask("child-new")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
	})
}

func TestCodex_V1CollabStaleSnapshotsDoNotReviveWhileSendInputAndResumeDo(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","receiverThreadIds":["child-1"],"prompt":"work","agentsStates":{}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"ct-1"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"ct-1","status":"completed"}}}`)))
	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Stale item/completed snapshots for spawnAgent, wait, and closeAgent must not revive.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","receiverThreadIds":["child-1"],"prompt":"work","agentsStates":{"child-1":{"status":"pendingInit"}}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"collabAgentToolCall","id":"call-w","tool":"wait","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"collabAgentToolCall","id":"call-c","tool":"closeAgent","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
	assert.Empty(t, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("child-1")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	// Genuine sendInput revives finished child.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t2","item":{"type":"collabAgentToolCall","id":"call-s","tool":"sendInput","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
	assert.Equal(t, []string{"child-1"}, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("child-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	// Complete again, then genuine resumeAgent revives finished child.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"ct-2","status":"completed"}}}`)))
	row, _ = sink.BackgroundTask("child-1")
	require.Equal(t, bgtask.StatusSucceeded, row.Status)

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"t3","item":{"type":"collabAgentToolCall","id":"call-r","tool":"resumeAgent","status":"completed","receiverThreadIds":["child-1"],"agentsStates":{"child-1":{"status":"running"}}}}}`)))
	assert.Equal(t, []string{"child-1", "child-1"}, sink.RevivedTasks())
	row, _ = sink.BackgroundTask("child-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestCodex_ThreadClosedUnknownTurnStatusAndThreadStatusChanged(t *testing.T) {
	t.Parallel()
	t.Run("child thread/closed closes working child as stopped", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newCodexAgentWithSink(agent.NewProviderServices(sink))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"s1","agentThreadId":"child-1","agentPath":"/root/a","kind":"started"}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/closed","params":{"threadId":"child-1"}}`)))
		row, ok := sink.BackgroundTask("child-1")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusStopped, row.Status)
	})

	t.Run("unknown child turn status closes as ended with unknown outcome", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newCodexAgentWithSink(agent.NewProviderServices(sink))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"s1","agentThreadId":"child-1","agentPath":"/root/a","kind":"started"}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"ct-1","status":"unknown_status"}}}`)))
		row, ok := sink.BackgroundTask("child-1")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
	})

	t.Run("thread/status/changed handles child transitions and ignores main thread", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newCodexAgentWithSink(agent.NewProviderServices(sink))

		// Main thread status change is a no-op with zero persisted messages.
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/status/changed","params":{"threadId":"main-thread","status":{"type":"idle"}}}`)))
		assert.Empty(t, sink.Messages())

		// systemError closes child as failed.
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"s-err","agentThreadId":"child-err","agentPath":"/root/err","kind":"started"}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/status/changed","params":{"threadId":"child-err","status":{"type":"systemError"}}}`)))
		row, ok := sink.BackgroundTask("child-err")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusFailed, row.Status)

		// Newly spawned child without sawActiveStatus stays Running on idle.
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"s-idle","agentThreadId":"child-idle","agentPath":"/root/idle","kind":"started"}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/status/changed","params":{"threadId":"child-idle","status":{"type":"idle"}}}`)))
		row, _ = sink.BackgroundTask("child-idle")
		assert.Equal(t, bgtask.StatusRunning, row.Status)

		// After active -> idle (with turnID == ""), child closes as StatusEndedWithUnknownOutcome.
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/status/changed","params":{"threadId":"child-idle","status":{"type":"active"}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/status/changed","params":{"threadId":"child-idle","status":{"type":"idle"}}}`)))
		row, _ = sink.BackgroundTask("child-idle")
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)

		// notLoaded closes even while turnID != "".
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"t1","item":{"type":"subAgentActivity","id":"s-nl","agentThreadId":"child-nl","agentPath":"/root/nl","kind":"started"}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-nl","turn":{"id":"ct-nl"}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/status/changed","params":{"threadId":"child-nl","status":{"type":"notLoaded"}}}`)))
		row, _ = sink.BackgroundTask("child-nl")
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
	})
}
