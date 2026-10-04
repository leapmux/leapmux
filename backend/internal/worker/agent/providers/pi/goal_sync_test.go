package pi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func piGoalSyncRig(t *testing.T) (*piTestRig, *agenttest.Sink, string) {
	t.Helper()
	sink := &agenttest.Sink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"session","id":"session","version":3}`+"\n"+`{"type":"custom","id":"focus","parentId":null,"customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"goal"}}`+"\n"), 0o600))
	goals := filepath.Join(directory, ".pi", "goals")
	require.NoError(t, os.MkdirAll(goals, 0o700))
	goalPath := filepath.Join(goals, "current.md")
	require.NoError(t, os.WriteFile(goalPath, []byte(`{"version":3,"id":"goal","objective":"Objective","status":"paused","usage":{"tokensUsed":3,"activeSeconds":2}}`), 0o600))
	rig.agent.Mu.Lock()
	rig.agent.sessionID, rig.agent.sessionFile, rig.agent.workingDir = "session", path, directory
	rig.agent.Mu.Unlock()
	return rig, sink, goalPath
}

func holdPiGoalPromptReply(t *testing.T, rig *piTestRig) func() {
	t.Helper()
	releasePrompt := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(releasePrompt) }) }
	t.Cleanup(release)
	rig.holdResponse(CommandPrompt, releasePrompt)
	return release
}

func writePiSessionWithoutFocus(t *testing.T, rig *piTestRig) {
	t.Helper()
	require.NoError(t, os.WriteFile(rig.agent.sessionFile, []byte(`{"type":"session","id":"session","version":3}`+"\n"), 0o600))
}

func TestPiGoalCommandsUseOnlyInstalledExtensionCommands(t *testing.T) {
	t.Parallel()
	rig := newPiTestRig(t, agent.NewProviderServices(&agenttest.Sink{}))
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{"commands":[{"name":"goal-direct","source":"extension"},{"name":"goal-clear","source":"extension"},{"name":"goal-pause","source":"prompt"},{"name":"goal-resume","source":"skill"}]}`), true, ""
	})
	rig.agent.refreshPiCommands(time.Second)
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, rig.agent.SupportedGoalActions())
	_, err := rig.agent.PerformGoalAction(agent.GoalActionPause, "")
	require.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	_, err = rig.agent.PerformGoalAction(agent.GoalActionSet, " \n ")
	require.Error(t, err)
	assert.Len(t, rig.requests(), 1)
}

func TestPiGoalSnapshotUsesTheCurrentNativeBranch(t *testing.T) {
	t.Parallel()
	rig, _, _ := piGoalSyncRig(t)
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		if !assert.Equal(t, CommandGetEntries, request.Type) {
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
		case CommandPrompt:
			assert.Equal(t, "/goal-resume", request.Payload["message"])
			err := os.WriteFile(goalPath, []byte(`{"version":3,"id":"goal","objective":"Resumed objective","status":"active"}`), 0o600)
			if err != nil {
				return nil, false, err.Error()
			}
			return nil, true, ""
		case CommandGetEntries:
			return json.RawMessage(`{"entries":[],"leafId":"focus"}`), true, ""
		default:
			return nil, false, "Unexpected command"
		}
	})
	outcome, err := rig.agent.PerformGoalAction(agent.GoalActionResume, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	require.Eventually(t, func() bool {
		goal, ok := sink.LastGoal()
		return ok && goal.Status == agent.GoalStatusActive && goal.Objective == "Resumed objective"
	}, time.Second, time.Millisecond)
}

// A goal command can start a model turn before its prompt reply.
// The first refresh can observe no focused goal.
// A later goal file must reach the sidebar while that reply stays open.
func TestPiGoalRefreshFindsGoalBeforeDetachedCommandReply(t *testing.T) {
	t.Parallel()
	rig, sink, goalPath := piGoalSyncRig(t)
	a := rig.agent
	writePiSessionWithoutFocus(t, rig)
	require.NoError(t, os.Remove(goalPath))
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-direct": true}
	a.goal.retryDelay = time.Millisecond
	a.goal.retryLimit = 5
	a.Mu.Unlock()
	focusReady := make(chan struct{})
	var focusOnce sync.Once
	release := holdPiGoalPromptReply(t, rig)
	reveal := func() { focusOnce.Do(func() { close(focusReady) }) }
	t.Cleanup(reveal)
	var reads atomic.Int32
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		switch request.Type {
		case CommandPrompt:
			return nil, true, ""
		case CommandGetEntries:
			if reads.Add(1) == 1 {
				return json.RawMessage(`{"entries":[],"leafId":null}`), true, ""
			}
			<-focusReady
			return json.RawMessage(`{"entries":[{"type":"custom","id":"focus","parentId":null,"customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"goal"}}],"leafId":"focus"}`), true, ""
		default:
			return nil, false, "Unexpected command"
		}
	})
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Objective")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return reads.Load() > 0 }, 2*time.Second, time.Millisecond)
	require.NoError(t, os.WriteFile(goalPath, []byte(`{"version":3,"id":"goal","objective":"Objective","status":"active"}`), 0o600))
	reveal()
	require.Eventually(t, func() bool {
		goal, ok := sink.LastGoal()
		return ok && goal.Objective == "Objective" && goal.Status == agent.GoalStatusActive
	}, 2*time.Second, time.Millisecond, "the focused goal must appear before the prompt reply")
	release()
}

func TestPiGoalRefreshFindsCustomOnlyGoalBeforeDetachedCommandReply(t *testing.T) {
	t.Parallel()
	rig, sink, goalPath := piGoalSyncRig(t)
	a := rig.agent
	require.NoError(t, os.Remove(a.sessionFile))
	require.NoError(t, os.WriteFile(goalPath, []byte(`{"version":3,"id":"goal","objective":"Fresh objective","status":"active"}`), 0o600))
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-direct": true}
	a.goal.retryDelay = time.Millisecond
	a.goal.retryLimit = 1
	a.Mu.Unlock()
	release := holdPiGoalPromptReply(t, rig)
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		switch request.Type {
		case CommandPrompt:
			return nil, true, ""
		case CommandGetState:
			return json.RawMessage(`{"sessionId":"session","messageCount":2}`), true, ""
		case CommandGetSessionStats:
			data, err := json.Marshal(map[string]any{"sessionId": "session", "sessionFile": a.sessionFile, "userMessages": 0, "assistantMessages": 0, "toolResults": 0, "totalMessages": 1})
			require.NoError(t, err)
			return data, true, ""
		case CommandGetEntries:
			return json.RawMessage(`{"entries":[{"type":"model_change","id":"42de25e8","parentId":null},{"type":"thinking_level_change","id":"3422c726","parentId":"42de25e8"},{"type":"custom","id":"951f641f","parentId":"3422c726","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"goal","reason":"created"}},{"type":"message","id":"0b2fc47f","parentId":"951f641f","message":{"role":"system","content":"Private goal policy"}},{"type":"custom_message","id":"df6944e9","parentId":"0b2fc47f","customType":"pi-goal-event","content":"Private goal continuation"}],"leafId":"df6944e9"}`), true, ""
		default:
			return nil, false, "Unexpected command"
		}
	})
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Fresh objective")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		goal, ok := sink.LastGoal()
		return ok && goal.Objective == "Fresh objective" && goal.Status == agent.GoalStatusActive
	}, 2*time.Second, time.Millisecond, "publish the native goal while its first prompt reply stays held")
	_, err = os.Stat(a.sessionFile)
	assert.ErrorIs(t, err, os.ErrNotExist)
	release()
}

func TestPiGoalRefreshStopsWhenPendingGoalNeverAppears(t *testing.T) {
	t.Parallel()
	rig, sink, _ := piGoalSyncRig(t)
	a := rig.agent
	writePiSessionWithoutFocus(t, rig)
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-direct": true}
	a.goal.retryDelay = time.Nanosecond
	a.goal.retryLimit = 2
	a.Mu.Unlock()
	release := holdPiGoalPromptReply(t, rig)
	var reads atomic.Int32
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		switch request.Type {
		case CommandPrompt:
			return nil, true, ""
		case CommandGetEntries:
			reads.Add(1)
			return json.RawMessage(`{"entries":[],"leafId":null}`), true, ""
		default:
			return nil, false, "Unexpected command"
		}
	})
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Objective")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return reads.Load() == 3 && !a.goal.running
	}, 2*time.Second, time.Millisecond)
	assert.Empty(t, sink.Goals(), "a missing focused goal must not create a sidebar entry")
	assert.Equal(t, int32(3), reads.Load(), "the first read plus two bounded retries")
	release()
}

func TestPiGoalClearDoesNotRetryAnEmptySnapshotWhileReplyIsPending(t *testing.T) {
	t.Parallel()
	rig, sink, _ := piGoalSyncRig(t)
	a := rig.agent
	writePiSessionWithoutFocus(t, rig)
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-clear": true}
	a.goal.retryDelay = time.Nanosecond
	a.goal.retryLimit = 2
	a.Mu.Unlock()
	release := holdPiGoalPromptReply(t, rig)
	var reads atomic.Int32
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		switch request.Type {
		case CommandPrompt:
			return nil, true, ""
		case CommandGetEntries:
			reads.Add(1)
			return json.RawMessage(`{"entries":[],"leafId":null}`), true, ""
		default:
			return nil, false, "Unexpected command"
		}
	})
	_, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return reads.Load() >= 1 && !a.goal.running
	}, 2*time.Second, time.Millisecond)
	assert.Equal(t, int32(1), reads.Load(), "a Clear with no goal is already settled")
	assert.Empty(t, sink.Goals())
	release()
}

func TestPiGoalRefreshIgnoresAnotherSessionPendingActivation(t *testing.T) {
	t.Parallel()
	rig, sink, _ := piGoalSyncRig(t)
	a := rig.agent
	writePiSessionWithoutFocus(t, rig)
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-direct": true}
	a.goal.pendingActivations = map[string]int{"old-session": 1}
	a.goal.retryDelay = time.Nanosecond
	a.goal.retryLimit = 2
	a.Mu.Unlock()
	var reads atomic.Int32
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		if request.Type != CommandGetEntries {
			return nil, false, "Unexpected command"
		}
		reads.Add(1)
		return json.RawMessage(`{"entries":[],"leafId":null}`), true, ""
	})
	a.schedulePiGoalRefresh(false)
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return reads.Load() >= 1 && !a.goal.running
	}, 2*time.Second, time.Millisecond)
	assert.Equal(t, int32(1), reads.Load(), "an old session must not add retries")
	assert.Empty(t, sink.Goals())
}

func TestPiGoalPublicationRejectsStaleReadsAndShutdown(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
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
	rig := newPiTestRig(t, agent.NewProviderServices(&agenttest.Sink{}))
	rig.agent.extensionCommands = map[string]bool{"goal-pause": true}
	rig.agent.stopPiGoalRefresh()
	_, err := rig.agent.PerformGoalAction(agent.GoalActionPause, "")
	require.Error(t, err)
	assert.Empty(t, rig.requests())
}

func TestPiGoalSnapshotSupportsAnUnwrittenNewSession(t *testing.T) {
	t.Parallel()
	rig, _, _ := piGoalSyncRig(t)
	require.NoError(t, os.Remove(rig.agent.sessionFile))
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		switch request.Type {
		case CommandGetState:
			return json.RawMessage(`{"sessionId":"session","messageCount":0}`), true, ""
		case CommandGetEntries:
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

func TestPiGoalSnapshotSupportsAnUnwrittenCustomOnlySession(t *testing.T) {
	t.Parallel()
	for _, count := range []int{2, 32} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			t.Parallel()
			rig, _, _ := piGoalSyncRig(t)
			require.NoError(t, os.Remove(rig.agent.sessionFile))
			rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
				switch request.Type {
				case CommandGetState:
					return json.RawMessage(fmt.Sprintf(`{"sessionId":"session","messageCount":%d}`, count)), true, ""
				case CommandGetSessionStats:
					data, err := json.Marshal(map[string]any{"sessionId": "session", "sessionFile": rig.agent.sessionFile, "userMessages": 0, "assistantMessages": 0, "toolResults": 0, "totalMessages": count - 1})
					require.NoError(t, err)
					return data, true, ""
				case CommandGetEntries:
					return json.RawMessage(`{"entries":[{"type":"model_change","id":"42de25e8","parentId":null},{"type":"thinking_level_change","id":"3422c726","parentId":"42de25e8"},{"type":"custom","id":"951f641f","parentId":"3422c726","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"goal","reason":"created"}},{"type":"message","id":"0b2fc47f","parentId":"951f641f","message":{"role":"system","content":"Private goal policy"}},{"type":"custom_message","id":"df6944e9","parentId":"0b2fc47f","customType":"pi-goal-event","content":"Private goal continuation"}],"leafId":"df6944e9"}`), true, ""
				default:
					return nil, false, "Unexpected command"
				}
			})
			record, err := rig.agent.readPiGoalSnapshot(rig.agent.sessionFile, rig.agent.workingDir, rig.agent.sessionID)
			require.NoError(t, err)
			require.NotNil(t, record)
			assert.Equal(t, "goal", record.ID)
			assert.Equal(t, "paused", record.Status)
			requests := rig.requests()
			require.Len(t, requests, 3)
			assert.Equal(t, CommandGetState, requests[0].Type)
			assert.Equal(t, CommandGetSessionStats, requests[1].Type)
			assert.Equal(t, CommandGetEntries, requests[2].Type)
		})
	}
}

func TestPiGoalSnapshotRefusesUnsafeUnwrittenHistory(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"large live count", "negative live count", "wrong state session", "absent live count", "fractional live count", "wrong session", "wrong file", "absent user count", "absent assistant count", "absent tool count", "absent total count", "negative count", "negative total count", "fractional count", "ordinary user", "ordinary assistant", "tool result", "large total count", "stats failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			rig, _, _ := piGoalSyncRig(t)
			require.NoError(t, os.Remove(rig.agent.sessionFile))
			liveCount := 2
			switch scenario {
			case "large live count":
				liveCount = 33
			case "negative live count":
				liveCount = -1
			}
			stats := map[string]any{"sessionId": "session", "sessionFile": rig.agent.sessionFile, "userMessages": 0, "assistantMessages": 0, "toolResults": 0, "totalMessages": 2}
			switch scenario {
			case "wrong session":
				stats["sessionId"] = "different"
			case "wrong file":
				stats["sessionFile"] = filepath.Join(t.TempDir(), "different.jsonl")
			case "absent user count":
				delete(stats, "userMessages")
			case "absent assistant count":
				delete(stats, "assistantMessages")
			case "absent tool count":
				delete(stats, "toolResults")
			case "absent total count":
				delete(stats, "totalMessages")
			case "negative total count":
				stats["totalMessages"] = -1
			case "negative count":
				stats["userMessages"] = -1
			case "fractional count":
				stats["totalMessages"] = 2.5
			case "ordinary user":
				stats["userMessages"] = 1
			case "ordinary assistant":
				stats["assistantMessages"] = 1
			case "tool result":
				stats["toolResults"] = 1
			case "large total count":
				stats["totalMessages"] = 33
			}
			rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
				switch request.Type {
				case CommandGetState:
					state := map[string]any{"sessionId": "session", "messageCount": liveCount}
					switch scenario {
					case "wrong state session":
						state["sessionId"] = "different"
					case "absent live count":
						delete(state, "messageCount")
					case "fractional live count":
						state["messageCount"] = 2.5
					}
					data, err := json.Marshal(state)
					require.NoError(t, err)
					return data, true, ""
				case CommandGetSessionStats:
					if scenario == "stats failure" {
						return nil, false, "Native statistics unavailable"
					}
					data, err := json.Marshal(stats)
					require.NoError(t, err)
					return data, true, ""
				default:
					return nil, false, "Unsafe history request"
				}
			})
			_, err := rig.agent.readPiGoalSnapshot(rig.agent.sessionFile, rig.agent.workingDir, rig.agent.sessionID)
			require.Error(t, err)
			requests := rig.requests()
			for _, request := range requests {
				assert.NotEqual(t, CommandGetEntries, request.Type)
			}
			switch scenario {
			case "large live count", "negative live count", "wrong state session", "absent live count", "fractional live count":
				assert.Len(t, requests, 1)
			default:
				assert.Len(t, requests, 2, "inspect native role counts before refusing small memory history")
			}
		})
	}
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
	assert.Equal(t, CommandGetState, requests[0].Type)
}

func TestPiGoalScheduleKeepsAPendingSnapshotIntent(t *testing.T) {
	t.Parallel()
	rig, _, _ := piGoalSyncRig(t)
	a := rig.agent
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-pause": true}
	// Hold the refresh loop, so the scheduling fields stay observable.
	a.goal.running = true
	a.Mu.Unlock()
	a.schedulePiGoalRefresh(true)
	a.schedulePiGoalRefresh(false)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.True(t, a.goal.snapshot, "a later hint must not spend the pending snapshot intent")
	assert.True(t, a.goal.pending)
}

func TestPiGoalRefreshRepublishesASnapshotAfterAFailedRead(t *testing.T) {
	t.Parallel()
	rig, sink, _ := piGoalSyncRig(t)
	a := rig.agent
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-pause": true}
	a.Mu.Unlock()
	var failing atomic.Bool
	failing.Store(true)
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		if request.Type != CommandGetEntries {
			return nil, false, "Unexpected command"
		}
		if failing.Load() {
			return nil, false, "the Pi session is busy"
		}
		return json.RawMessage(`{"entries":[],"leafId":"focus"}`), true, ""
	})
	settled := func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.goal.running
	}
	a.schedulePiGoalRefresh(true)
	require.Eventually(t, settled, time.Second, time.Millisecond)
	require.Empty(t, sink.Goals(), "a failed read publishes nothing")

	// The failed startup publication retains its snapshot intent.
	// A later hint without that intent must still use the snapshot.
	// Otherwise, the browser announces Goal set for a goal from an earlier process.
	failing.Store(false)
	a.schedulePiGoalRefresh(false)
	require.Eventually(t, func() bool { _, ok := sink.LastGoal(); return ok }, time.Second, time.Millisecond)
	goal, _ := sink.LastGoal()
	assert.True(t, goal.Snapshot)

	// Publication clears the snapshot intent.
	// The next hint must announce a real change.
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.goal.running && !a.goal.snapshot
	}, time.Second, time.Millisecond)
	a.schedulePiGoalRefresh(false)
	require.Eventually(t, func() bool { return len(sink.Goals()) == 2 }, time.Second, time.Millisecond)
	assert.False(t, sink.Goals()[1].Snapshot)
}

// Pi writes its first session file after a user or assistant message.
// A missing ordinary-history file cannot use the small custom-only fallback.
// No new hint follows a recovery pass.
// Retry so the panel can observe the later file.
func TestPiGoalRefreshWaitsForAnUnwrittenTranscript(t *testing.T) {
	t.Parallel()
	rig, sink, _ := piGoalSyncRig(t)
	a := rig.agent
	path := a.sessionFile
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-pause": true}
	a.goal.retryDelay = time.Millisecond
	a.Mu.Unlock()

	var restored atomic.Bool
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		switch request.Type {
		case CommandGetState:
			// The ordinary-history count exceeds the in-memory limit.
			// Simulate the first conversation flush during this read.
			// The retry must then read the file.
			if os.WriteFile(path, contents, 0o600) == nil {
				restored.Store(true)
			}
			return json.RawMessage(`{"sessionId":"session","messageCount":10000}`), true, ""
		case CommandGetEntries:
			return json.RawMessage(`{"entries":[],"leafId":"focus"}`), true, ""
		default:
			return nil, false, "Unexpected command"
		}
	})

	a.schedulePiGoalRefresh(true)
	require.Eventually(t, func() bool { _, ok := sink.LastGoal(); return ok }, 2*time.Second, time.Millisecond,
		"the refresh must read again once Pi writes the transcript, without a second hint")
	require.True(t, restored.Load(), "the test must restore the transcript the retry reads")
	goal, _ := sink.LastGoal()
	assert.Equal(t, "Objective", goal.Objective)
	assert.True(t, goal.Snapshot, "the recovery pass still carries the snapshot intent")
}

// A session file that never appears must not keep the refresh goroutine alive.
// Stop at the retry limit.
func TestPiGoalRefreshStopsWaitingForATranscriptThatNeverLands(t *testing.T) {
	t.Parallel()
	rig, sink, _ := piGoalSyncRig(t)
	a := rig.agent
	require.NoError(t, os.Remove(a.sessionFile))
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-pause": true}
	a.goal.retryDelay, a.goal.retryLimit = time.Nanosecond, 2
	a.Mu.Unlock()
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		if request.Type != CommandGetState {
			return nil, false, "Unexpected command"
		}
		return json.RawMessage(`{"sessionId":"session","messageCount":10000}`), true, ""
	})

	a.schedulePiGoalRefresh(true)
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.goal.running
	}, 2*time.Second, time.Millisecond)
	assert.Empty(t, sink.Goals(), "a transcript that never lands publishes nothing")
	assert.Len(t, rig.requests(), 3, "the first read plus the two the limit allows")
}

// A stop must end the retry wait.
// It must prevent another read during shutdown.
func TestPiGoalRefreshEndsTheWaitWhenTheAgentStops(t *testing.T) {
	t.Parallel()
	rig, sink, _ := piGoalSyncRig(t)
	a := rig.agent
	require.NoError(t, os.Remove(a.sessionFile))
	a.Mu.Lock()
	a.extensionCommands = map[string]bool{"goal-pause": true}
	// Only cancellation can end this long wait before its deadline.
	a.goal.retryDelay = time.Minute
	a.Mu.Unlock()
	read := make(chan struct{}, 1)
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		select {
		case read <- struct{}{}:
		default:
		}
		return json.RawMessage(`{"sessionId":"session","messageCount":10000}`), true, ""
	})

	a.schedulePiGoalRefresh(true)
	<-read
	a.CancelForTest()
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.goal.running
	}, 2*time.Second, time.Millisecond)
	assert.Len(t, rig.requests(), 1, "the wait ends on the stop, with no second command")
	assert.Empty(t, sink.Goals())
}

func TestPiGoalActionReturnsBeforeTheConfirmationAnswer(t *testing.T) {
	t.Parallel()
	rig, sink, _ := piGoalSyncRig(t)
	rig.agent.Mu.Lock()
	rig.agent.extensionCommands = map[string]bool{"goal-clear": true}
	rig.agent.Mu.Unlock()
	release := make(chan struct{})
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseOnce)
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		if request.Type == CommandPrompt {
			// Pi answers a clear only after the user closes its confirmation dialog.
			<-release
			return nil, false, "the Pi goal clear was refused"
		}
		return json.RawMessage(`{"entries":[],"leafId":"focus"}`), true, ""
	})
	done := make(chan error, 1)
	go func() {
		_, err := rig.agent.PerformGoalAction(agent.GoalActionClear, "")
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err, "the stdin write is the delivery acceptance")
	case <-time.After(2 * time.Second):
		t.Fatal("PerformGoalAction waited for the dialog answer")
	}
	// A late command failure must reach the user.
	// No other path reports it.
	releaseOnce()
	require.Eventually(t, func() bool { return len(sink.LeapMuxNotifications()) > 0 }, time.Second, time.Millisecond)
	note := sink.LeapMuxNotifications()[0]
	assert.Equal(t, contracts.NotificationTypeAgentError, note["type"])
	assert.Contains(t, note["error"], "refused")
}

func TestPiGoalControlRecoversFromAFailedCatalogRead(t *testing.T) {
	t.Parallel()
	rig := newPiTestRig(t, agent.NewProviderServices(&agenttest.Sink{}))
	var ready atomic.Bool
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		if request.Type != CommandGetCommands {
			return nil, false, "Unexpected command"
		}
		if !ready.Load() {
			return nil, false, "the Pi extension host is not ready"
		}
		return json.RawMessage(`{"commands":[{"name":"goal-pause","source":"extension"}]}`), true, ""
	})
	assert.False(t, rig.agent.refreshPiCommands(time.Second), "a failed read holds no catalog")
	assert.Empty(t, rig.agent.SupportedGoalActions())
	ready.Store(true)
	assert.True(t, rig.agent.refreshPiCommands(time.Second))
	assert.Equal(t, []agent.GoalAction{agent.GoalActionPause}, rig.agent.SupportedGoalActions())
}
