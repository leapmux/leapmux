package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func piGoalSyncRig(t *testing.T) (*piTestRig, *testSink, string) {
	t.Helper()
	sink := &testSink{}
	rig := newPiTestRig(t, sink)
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"session","id":"session","version":3}`+"\n"+`{"type":"custom","id":"focus","parentId":null,"customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"goal"}}`+"\n"), 0o600))
	goals := filepath.Join(directory, ".pi", "goals")
	require.NoError(t, os.MkdirAll(goals, 0o700))
	goalPath := filepath.Join(goals, "current.md")
	require.NoError(t, os.WriteFile(goalPath, []byte(`{"version":3,"id":"goal","objective":"Objective","status":"paused","usage":{"tokensUsed":3,"activeSeconds":2}}`), 0o600))
	rig.agent.mu.Lock()
	rig.agent.sessionID, rig.agent.sessionFile, rig.agent.workingDir = "session", path, directory
	rig.agent.mu.Unlock()
	return rig, sink, goalPath
}

func TestPiGoalCommandsUseOnlyInstalledExtensionCommands(t *testing.T) {
	t.Parallel()
	rig := newPiTestRig(t, &testSink{})
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{"commands":[{"name":"goal-direct","source":"extension"},{"name":"goal-clear","source":"extension"},{"name":"goal-pause","source":"prompt"},{"name":"goal-resume","source":"skill"}]}`), true, ""
	})
	rig.agent.refreshPiCommands(time.Second)
	assert.Equal(t, []GoalAction{GoalActionSet, GoalActionClear}, rig.agent.SupportedGoalActions())
	_, err := rig.agent.PerformGoalAction(GoalActionPause, "")
	require.ErrorIs(t, err, ErrGoalControlUnsupported)
	_, err = rig.agent.PerformGoalAction(GoalActionSet, " \n ")
	require.Error(t, err)
	assert.Len(t, rig.requests(), 1)
}

func TestPiGoalSnapshotUsesTheCurrentNativeBranch(t *testing.T) {
	t.Parallel()
	rig, _, _ := piGoalSyncRig(t)
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		if !assert.Equal(t, PiCommandGetEntries, request.Type) {
			return nil, false, "Unexpected command"
		}
		assert.Equal(t, "focus", request.Payload["since"])
		return json.RawMessage(`{"entries":[{"type":"custom","id":"clear","parentId":"focus","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":null}}],"leafId":"focus"}`), true, ""
	})
	record, err := rig.agent.readPiGoalSnapshot(rig.agent.sessionFile, rig.agent.workingDir, rig.agent.sessionID)
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, "goal", record.ID)
	assert.Equal(t, "paused", record.Status)
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{"entries":[{"type":"custom","id":"clear","parentId":"focus","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":null}}],"leafId":"clear"}`), true, ""
	})
	record, err = rig.agent.readPiGoalSnapshot(rig.agent.sessionFile, rig.agent.workingDir, rig.agent.sessionID)
	require.NoError(t, err)
	assert.Nil(t, record)
}

func TestPiGoalCommandRefreshesConfirmedState(t *testing.T) {
	t.Parallel()
	rig, sink, goalPath := piGoalSyncRig(t)
	rig.agent.extensionCommands = map[string]bool{"goal-resume": true}
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		switch request.Type {
		case PiCommandPrompt:
			assert.Equal(t, "/goal-resume", request.Payload["message"])
			err := os.WriteFile(goalPath, []byte(`{"version":3,"id":"goal","objective":"Resumed objective","status":"active"}`), 0o600)
			if err != nil {
				return nil, false, err.Error()
			}
			return nil, true, ""
		case PiCommandGetEntries:
			return json.RawMessage(`{"entries":[],"leafId":"focus"}`), true, ""
		default:
			return nil, false, "Unexpected command"
		}
	})
	outcome, err := rig.agent.PerformGoalAction(GoalActionResume, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	require.Eventually(t, func() bool {
		goal, ok := sink.LastGoal()
		return ok && goal.Status == GoalStatusActive && goal.Objective == "Resumed objective"
	}, time.Second, time.Millisecond)
}

func TestPiGoalPublicationRejectsStaleReadsAndShutdown(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	a := &PiAgent{sink: sink}
	record := &piGoalRecord{ID: "goal", Objective: "Old objective", Status: "active"}
	before := a.goal.revision
	a.publishPiGoal(&piGoalRecord{ID: "goal", Objective: "New objective", Status: "paused"}, false, nil)
	a.publishPiGoal(record, false, &before)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "New objective", goal.Objective)
	a.stopPiGoalRefresh()
	a.publishPiGoal(record, false, nil)
	assert.Len(t, sink.Goals(), 1)
}

func TestPiGoalActionDoesNotWriteAfterShutdownStarts(t *testing.T) {
	t.Parallel()
	rig := newPiTestRig(t, &testSink{})
	rig.agent.extensionCommands = map[string]bool{"goal-pause": true}
	rig.agent.stopPiGoalRefresh()
	_, err := rig.agent.PerformGoalAction(GoalActionPause, "")
	require.Error(t, err)
	assert.Empty(t, rig.requests())
}

func TestPiGoalSnapshotSupportsAnUnwrittenNewSession(t *testing.T) {
	t.Parallel()
	rig, _, _ := piGoalSyncRig(t)
	require.NoError(t, os.Remove(rig.agent.sessionFile))
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		switch request.Type {
		case PiCommandGetState:
			return json.RawMessage(`{"sessionId":"session","messageCount":0}`), true, ""
		case PiCommandGetEntries:
			return json.RawMessage(`{"entries":[{"type":"custom","id":"focus","parentId":null,"customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"goal"}}],"leafId":"focus"}`), true, ""
		default:
			return nil, false, "Unexpected command"
		}
	})
	record, err := rig.agent.readPiGoalSnapshot(rig.agent.sessionFile, rig.agent.workingDir, rig.agent.sessionID)
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, "goal", record.ID)
}

func TestPiGoalSnapshotDoesNotRequestMissingNonemptyHistory(t *testing.T) {
	t.Parallel()
	rig, _, _ := piGoalSyncRig(t)
	require.NoError(t, os.Remove(rig.agent.sessionFile))
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{"sessionId":"session","messageCount":10000}`), true, ""
	})
	_, err := rig.agent.readPiGoalSnapshot(rig.agent.sessionFile, rig.agent.workingDir, rig.agent.sessionID)
	require.ErrorIs(t, err, os.ErrNotExist)
	requests := rig.requests()
	require.Len(t, requests, 1)
	assert.Equal(t, PiCommandGetState, requests[0].Type)
}
