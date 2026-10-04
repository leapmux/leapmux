package cline

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// teamProgressPayload is one team.progress payload for a run event.
func teamProgressPayload(sessionID, eventType, runID, agentID string) map[string]any {
	return map[string]any{
		"type": "team_progress_projection", "version": 1, "sessionId": sessionID,
		"summary": map[string]any{"teamName": "builders"},
		"lastEvent": map[string]any{
			"teamName": "builders", "sessionId": sessionID, "eventType": eventType,
			"runId": runID, "agentId": agentID, "taskId": "task-1",
		},
	}
}

func TestATeammateRunIsAWorkflowRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunQueued, "run_1", "researcher"))
	row := teamRowPrefix + "run_1"
	item, ok := r.sink.BackgroundTask(row)
	require.True(t, ok)
	assert.Equal(t, bgtask.KindWorkflow, item.Kind)
	assert.Equal(t, bgtask.StatusPending, item.Status)
	assert.Equal(t, "researcher", item.Title)
	assert.Equal(t, "builders", item.GroupLabel)
	assert.NotEmpty(t, item.ChildAgentID)

	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
	item, _ = r.sink.BackgroundTask(row)
	assert.Equal(t, bgtask.StatusRunning, item.Status)
	assert.Len(t, r.sink.PersistedNotifications(), 2, "each run event is a notice of the lead")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, r.sink.LastNotification().Source)
}

func TestATeamEventOfNoRunChangesNoRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, "agent_event", "", "researcher"))
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, "teammate_spawned", "", "researcher"))
	assert.Empty(t, r.sink.BackgroundTasks())
	assert.Zero(t, r.sink.NotificationCount())
}

func TestAFinishedTeammateRunTakesItsTranscriptFromClinesStore(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
	stored := root + teamTaskSessionMarker + "researcher__abc123"
	r.hub.setSessions(map[string]any{
		"sessionId": stored, "createdAt": time.Now().UnixMilli(), "status": "completed",
		"metadata": map[string]any{"parentSessionId": root, "agentId": "researcher", "prompt": "Find the bug."},
	})
	r.hub.store(stored, []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Find the bug."}}},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Found it."}}},
	})
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunCompleted, "run_1", "researcher"))
	row := teamRowPrefix + "run_1"
	waitFor(t, func() bool {
		item, _ := r.sink.BackgroundTask(row)
		return item.Status == bgtask.StatusCompleted
	}, "the row closes after the transcript")
	child := r.childSink(t, row)
	messages := child.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, messages[0].Source, "the task opens the transcript")
	assert.Equal(t, contracts.ClineEventAssistantFinished, decode(t, messages[1].Content)["event"])
}

func TestTeamRunStatus(t *testing.T) {
	t.Parallel()
	for event, want := range map[string]bgtask.Status{
		contracts.ClineTeamRunEventRunQueued:      bgtask.StatusPending,
		contracts.ClineTeamRunEventRunStarted:     bgtask.StatusRunning,
		contracts.ClineTeamRunEventRunCompleted:   bgtask.StatusCompleted,
		contracts.ClineTeamRunEventRunFailed:      bgtask.StatusFailed,
		contracts.ClineTeamRunEventRunCancelled:   bgtask.StatusStopped,
		contracts.ClineTeamRunEventRunInterrupted: bgtask.StatusInterrupted,
	} {
		got, ok := teamRunStatus(event)
		assert.True(t, ok, event)
		assert.Equal(t, want, got, event)
	}
	_, ok := teamRunStatus("run_progress")
	assert.False(t, ok)
}

func TestATeammatesOutputWithNoLeadTurnIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
	r.feed(t, eventIterationStarted, map[string]any{"iteration": 1})
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Teammate text."})
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "tm_call", "toolName": "read_files", "input": map[string]any{}})
	r.feed(t, contracts.ClineEventToolFinished, map[string]any{"toolCallId": "tm_call", "toolName": "read_files", "output": []any{}})
	assert.False(t, r.turnActive(), "a teammate's output starts no lead turn")
	assert.Zero(t, r.sink.MessageCount())
}

func TestTheProcessEndClosesTheTeammateRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
	r.exit()
	_ = r.agent.Wait()
	item, _ := r.sink.BackgroundTask(teamRowPrefix + "run_1")
	assert.Equal(t, bgtask.StatusInterrupted, item.Status, "a crash interrupts the run")
}

// A run event that states no teammate or no team still makes a row, with the
// words that stand for them.
func TestATeammateRunWithNoNamesTakesTheDefaults(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	payload := teamProgressPayload(r.sessionID(), contracts.ClineTeamRunEventRunQueued, "run_1", "")
	payload["lastEvent"].(map[string]any)["teamName"] = "  "
	r.feed(t, contracts.ClineEventTeamProgress, payload)
	item, ok := r.sink.BackgroundTask(teamRowPrefix + "run_1")
	require.True(t, ok)
	assert.Equal(t, "Teammate", item.Title)
	assert.Equal(t, "Team", item.GroupLabel)
}

// A run that failed or that the lead cancelled still takes its transcript from
// Cline's store, and its row closes with the run's own status.
func TestAnEndedTeammateRunClosesWithItsStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		event  string
		status bgtask.Status
	}{
		{contracts.ClineTeamRunEventRunFailed, bgtask.StatusFailed},
		{contracts.ClineTeamRunEventRunCancelled, bgtask.StatusStopped},
		{contracts.ClineTeamRunEventRunInterrupted, bgtask.StatusInterrupted},
	} {
		t.Run(tc.event, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			root := r.sessionID()
			r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
			stored := root + teamTaskSessionMarker + "researcher__abc123"
			r.hub.setSessions(map[string]any{
				"sessionId": stored, "createdAt": time.Now().UnixMilli(), "status": "failed",
				"metadata": map[string]any{"parentSessionId": root, "agentId": "researcher"},
			})
			r.hub.store(stored, []any{
				map[string]any{"role": "user", "content": "Find the bug."},
				map[string]any{"role": "assistant", "content": "It broke."},
			})
			r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, tc.event, "run_1", "researcher"))
			waitFor(t, func() bool {
				item, _ := r.sink.BackgroundTask(teamRowPrefix + "run_1")
				return item.Status.IsFinished()
			}, "the row closes after the transcript")
			item, _ := r.sink.BackgroundTask(teamRowPrefix + "run_1")
			assert.Equal(t, tc.status, item.Status)
			assert.Equal(t, []string{contracts.ClineEventAssistantFinished}, rowEvents(t, r.childSink(t, teamRowPrefix+"run_1")))
		})
	}
}

// Cline can state a run's end twice, and the second end changes nothing: the
// row closed and the transcript came from the store once.
func TestARepeatedEndOfATeammateRunChangesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
	stored := root + teamTaskSessionMarker + "researcher__abc123"
	r.hub.setSessions(map[string]any{
		"sessionId": stored, "createdAt": time.Now().UnixMilli(), "status": "completed",
		"metadata": map[string]any{"parentSessionId": root, "agentId": "researcher"},
	})
	r.hub.store(stored, []any{map[string]any{"role": "assistant", "content": "Found it."}})
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunCompleted, "run_1", "researcher"))
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunFailed, "run_1", "researcher"))
	r.agent.background.Wait()
	item, _ := r.sink.BackgroundTask(teamRowPrefix + "run_1")
	assert.Equal(t, bgtask.StatusCompleted, item.Status, "the first end decides")
	assert.Len(t, r.hub.commandsNamed(commandSessionMessages), 1, "the transcript comes from the store once")
	assert.Equal(t, []string{contracts.ClineEventAssistantFinished}, rowEvents(t, r.childSink(t, teamRowPrefix+"run_1")))
}

// A run whose transcript the database refuses closes at its end, with no read
// of Cline's store.
func TestATeammateRunWithNoTranscriptClosesAtItsEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t, withNoChild)
	root := r.sessionID()
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
	item, ok := r.sink.BackgroundTask(teamRowPrefix + "run_1")
	require.True(t, ok)
	assert.Empty(t, item.ChildAgentID)
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunCompleted, "run_1", "researcher"))
	item, _ = r.sink.BackgroundTask(teamRowPrefix + "run_1")
	assert.Equal(t, bgtask.StatusCompleted, item.Status)
	assert.Empty(t, r.hub.commandsNamed(commandSessionList))
}

// A stop ends the teammate runs that still run as stopped, and a crash as
// interrupted (TestTheProcessEndClosesTheTeammateRuns): only a crash can leave
// a run in an unknown state.
func TestTheStopClosesTheTeammateRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunQueued, "run_2", "writer"))
	r.agent.Stop()
	for _, key := range []string{"run_1", "run_2"} {
		item, _ := r.sink.BackgroundTask(teamRowPrefix + key)
		assert.Equal(t, bgtask.StatusStopped, item.Status, key)
	}
}
