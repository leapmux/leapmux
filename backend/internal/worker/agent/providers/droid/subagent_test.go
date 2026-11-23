package droid

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const droidTaskCall = `{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"task-1","name":"Task","input":{"subagent_type":"explorer","description":"Inspect the note","prompt":"Read the child note."}}}}}`
const droidChildAvailable = `{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"child_session_available","childSessionId":"child-1","toolUseId":"task-1","subagentType":"explorer","description":"Inspect the note"}}}`

func TestChildSessionAvailableCreatesRegistryAndPrompt(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidChildAvailable))
	a.HandleOutput([]byte(droidChildAvailable))

	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok, "the native child session appears in the task registry")
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "worker-agent-1", row.ParentAgentID)
	assert.Equal(t, "Inspect the note", row.Title)
	require.NotEmpty(t, row.ChildAgentID)
	childRows := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, childRows, 1, "a replay does not duplicate the child's prompt")
	var prompt map[string]string
	require.NoError(t, json.Unmarshal(childRows[0].Content, &prompt))
	assert.Equal(t, "Read the child note.", prompt["content"])
}

func TestChildSessionMessagesAndTurnEndStayInChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidChildAvailable))
	// Isolate output routing from the separate missing-registration failure.
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "droid-tool-task-1", ProviderChildKey: "child-1", Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role: bgtask.RoleChildTask,

		RowKey: "child-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.PersistChildPrompt(childID, "Read the child note."))
	rootCount := len(sink.Messages())

	for _, frame := range []string{
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"child-1","notification":{"type":"assistant_text_delta","messageId":"msg-1","blockIndex":0,"textDelta":"CHILD_EARLY"}}}`,
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"child-1","notification":{"type":"assistant_text_complete","messageId":"msg-1","blockIndex":0}}}`,
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"child-1","notification":{"type":"create_message","message":{"id":"msg-1","role":"assistant","content":[{"type":"text","text":"CHILD_EARLY"}]}}}}`,
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"child-1","notification":{"type":"tool_call","toolUse":{"id":"read-1","name":"Read","input":{"file_path":"/note.txt"}}}}}`,
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"child-1","notification":{"type":"tool_result","toolUseId":"read-1","content":"CHILD_READ_MARKER","isError":false}}}`,
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"child-1","notification":{"type":"agent_turn_completed","turnId":"turn-1","reason":"completed"}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	childRows := sink.Child(childID).Messages()
	require.GreaterOrEqual(t, len(childRows), 5, "the child keeps its prompt, answer, tool call, result, and turn end")
	assert.Equal(t, rootCount, len(sink.Messages()), "child output does not enter the root transcript")
	assert.Contains(t, string(childRows[1].Content), "CHILD_EARLY")
	assert.Contains(t, string(childRows[4].Content), "CHILD_READ_MARKER")
	assert.True(t, childRows[len(childRows)-1].TurnEnd)
	assert.False(t, a.ActiveChildTurnState("child-1").Active)
	finished, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, finished.Status)
}

func TestUnknownNativeSessionCannotWriteIntoTheRoot(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, frame := range []string{
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"unknown-session","notification":{"type":"create_message","message":{"id":"msg-1","role":"assistant"}}}}`,
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"unknown-session","notification":{"type":"droid_working_state_changed","newState":"thinking"}}}`,
		`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"unknown-session","notification":{"type":"agent_turn_completed","turnId":"turn-1","reason":"completed"}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.TurnActives(), "an unknown session cannot move the root turn")
}

func TestDroidTaskResumeRevivesChildAndResumesTailFromEOF(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"job1-msg","message":{"role":"assistant","content":[{"type":"text","text":"TASK_JOB1_ANSWER"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		row, ok := sink.BackgroundTask(archiveChildID)
		return ok && row.Status == bgtask.StatusSucceeded && archiveRowsContain(childArchiveRows(sink, archiveChildID), "TASK_JOB1_ANSWER")
	}, "Job 1 completes and closes its row")

	// Deliver Task({resume: archiveChildID}) and matching child_session_available.
	resumeCall := fmt.Sprintf(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"task-2","name":"Task","input":{"subagent_type":"explorer","description":"Inspect again","prompt":"Continue with Job 2","resume":%q}}}}}`, archiveChildID)
	a.HandleOutput([]byte(resumeCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-2")))

	row, ok := sink.BackgroundTask(archiveChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status, "Task({resume}) must revive the finished child row")
	assert.True(t, a.ActiveChildTurnState(archiveChildID).Active, "Task({resume}) must re-arm the child turn")
	assert.True(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "Continue with Job 2"),
		"Task({resume}) must persist the Job 2 follow-up prompt to the child transcript")

	appendDroidArchive(t, path,
		`{"type":"message","id":"job2-msg","message":{"role":"assistant","content":[{"type":"text","text":"TASK_JOB2_ANSWER"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"completed","resultKind":"text"}`,
	)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		r, exists := sink.BackgroundTask(archiveChildID)
		return exists && r.Status == bgtask.StatusSucceeded && archiveRowsContain(childArchiveRows(sink, archiveChildID), "TASK_JOB2_ANSWER")
	}, "resumed tail must read Job 2 and close on Job 2 outcome")
}

func TestDroidTaskResumeAfterRestartResolvesChildFromRegistry(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"job1-msg","message":{"role":"assistant","content":[{"type":"text","text":"RESTART_JOB1_TEXT"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
	)
	childAgentID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "droid-tool-task-1", ProviderChildKey: archiveChildID, Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role: bgtask.RoleChildTask, RowKey: archiveChildID, Kind: bgtask.KindSubagent,
		ChildAgentID: childAgentID, ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusSucceeded,
	}))
	require.NoError(t, sink.CloseBackgroundTask(archiveChildID, bgtask.StatusSucceeded))

	resumeCall := fmt.Sprintf(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"task-2","name":"Task","input":{"subagent_type":"explorer","description":"Inspect again","prompt":"Resume after restart","resume":%q}}}}}`, archiveChildID)
	a.HandleOutput([]byte(resumeCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-2")))

	row, ok := sink.BackgroundTask(archiveChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "Resume after restart"))

	appendDroidArchive(t, path,
		`{"type":"message","id":"job2-msg","message":{"role":"assistant","content":[{"type":"text","text":"RESTART_JOB2_TEXT"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"completed","resultKind":"text"}`,
	)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		r, exists := sink.BackgroundTask(archiveChildID)
		return exists && r.Status == bgtask.StatusSucceeded && archiveRowsContain(childArchiveRows(sink, archiveChildID), "RESTART_JOB2_TEXT")
	}, "resumed tail after restart must read Job 2 and close")
	assert.False(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "RESTART_JOB1_TEXT"),
		"resumed tail after restart must start at EOF and skip Job 1")
}

func TestDroidTaskResumeAfterRestartCapturesJob2FlushedBeforeChildAvailable(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"job1-msg","message":{"role":"assistant","content":[{"type":"text","text":"RESTART_JOB1_TEXT"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
	)
	childAgentID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "droid-tool-task-1", ProviderChildKey: archiveChildID, Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role: bgtask.RoleChildTask, RowKey: archiveChildID, Kind: bgtask.KindSubagent,
		ChildAgentID: childAgentID, ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusSucceeded,
	}))
	require.NoError(t, sink.CloseBackgroundTask(archiveChildID, bgtask.StatusSucceeded))

	resumeCall := fmt.Sprintf(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"task-2","name":"Task","input":{"subagent_type":"explorer","description":"Inspect again","prompt":"Resume after restart","resume":%q}}}}}`, archiveChildID)
	a.HandleOutput([]byte(resumeCall))

	// Job 2 flushes its output and outcome to the archive before child_session_available arrives.
	appendDroidArchive(t, path,
		`{"type":"message","id":"job2-msg","message":{"role":"assistant","content":[{"type":"text","text":"RESTART_JOB2_EARLY_FLUSH"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"completed","resultKind":"text"}`,
	)

	// A repeated tool_call update for the same Task must keep the pre-probed offset.
	resumeCallUpdate := fmt.Sprintf(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"task-2","name":"Task","input":{"subagent_type":"explorer","description":"Inspect again (updated)","prompt":"Resume after restart","resume":%q}}}}}`, archiveChildID)
	a.HandleOutput([]byte(resumeCallUpdate))

	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-2")))

	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		r, exists := sink.BackgroundTask(archiveChildID)
		return exists && r.Status == bgtask.StatusSucceeded && archiveRowsContain(childArchiveRows(sink, archiveChildID), "RESTART_JOB2_EARLY_FLUSH")
	}, "resumed tail must capture Job 2 flushed before child_session_available and close")
	assert.True(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "Resume after restart"))
	assert.False(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "RESTART_JOB1_TEXT"),
		"resumed tail must skip Job 1 written before Task({resume})")
	assert.False(t, a.ActiveChildTurnState(archiveChildID).Active,
		"resumed child turn must disarm after Job 2 outcome")
}

func TestDroidTaskResumeDoesNotReviveOrArmWhenTailCannotStart(t *testing.T) {
	t.Run("invalid existing tail", func(t *testing.T) {
		a, sink, clock, dir, _ := droidArchiveHarness(t)
		path := filepath.Join(dir, archiveChildID+".jsonl")
		appendDroidArchive(t, path, `{not valid json`)

		a.HandleOutput([]byte(droidTaskCall))
		a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
		advanceDroidArchive(t, clock)
		testutil.RequireEventually(t, func() bool {
			row, ok := sink.BackgroundTask(archiveChildID)
			return ok && row.Status == bgtask.StatusEndedWithUnknownOutcome && !a.ActiveChildTurnState(archiveChildID).Active
		}, "corrupt archive closes child row and marks tail invalid")

		resumeCall := fmt.Sprintf(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"task-2","name":"Task","input":{"subagent_type":"explorer","description":"Inspect again","prompt":"Retry job 2","resume":%q}}}}}`, archiveChildID)
		a.HandleOutput([]byte(resumeCall))
		a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-2")))

		row, ok := sink.BackgroundTask(archiveChildID)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status, "failed tail resume must not revive the child row")
		assert.Empty(t, sink.RevivedTasks())
		assert.False(t, a.ActiveChildTurnState(archiveChildID).Active, "failed tail resume must not arm childTurns")
	})

	t.Run("missing working dir", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		childAgentID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
			RegistryRole: bgtask.RoleChildTask,
			SpawnSpanID:  "droid-tool-task-1", ProviderChildKey: archiveChildID, Title: "Inspect the note"})
		require.NoError(t, err)
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			Role: bgtask.RoleChildTask, RowKey: archiveChildID, Kind: bgtask.KindSubagent,
			ChildAgentID: childAgentID, ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusSucceeded,
		}))
		require.NoError(t, sink.CloseBackgroundTask(archiveChildID, bgtask.StatusSucceeded))

		resumeCall := fmt.Sprintf(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"task-2","name":"Task","input":{"subagent_type":"explorer","description":"Inspect again","prompt":"Retry without working dir","resume":%q}}}}}`, archiveChildID)
		a.HandleOutput([]byte(resumeCall))
		a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-2")))

		row, ok := sink.BackgroundTask(archiveChildID)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status, "missing workingDir must not revive the child row")
		assert.Empty(t, sink.RevivedTasks())
		assert.False(t, a.ActiveChildTurnState(archiveChildID).Active, "missing workingDir must not arm childTurns")
	})
}
