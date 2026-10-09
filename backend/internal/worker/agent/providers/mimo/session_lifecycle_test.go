package mimo

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLateSpawnPromptSurvivesClearAndActorIDReuse(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledChildPromptSink{ProviderServices: a.sink}
	controlled.failures.Store(10)
	a.sink = controlled
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	oldID := a.actors[actorID].childAgentID
	require.NotEmpty(t, oldID)
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	controlled.failures.Store(0)
	feed(a, eventWithSession(t, actorRegisteredEvent(t, actorID, true), sessionID),
		eventWithSession(t, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""), sessionID))
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	rows := sink.Child(oldID).Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"content":"SUBAGENT-WORK please run one command"}`, string(rows[0].Content))
	assert.NotEqual(t, oldID, a.actors[actorID].childAgentID)
	assert.Empty(t, sink.Child(a.actors[actorID].childAgentID).Messages())
}

func TestOpenSession(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)

	session, err := a.openSession(a.Context(), "")
	require.NoError(t, err)
	assert.Equal(t, "ses_created", session.ID)

	session, err = a.openSession(a.Context(), "ses_stored")
	require.NoError(t, err)
	assert.Equal(t, "ses_stored", session.ID)
	assert.Len(t, server.requestsTo("POST /session"), 1, "a resume creates no session")

	server.respond("GET /session/ses_gone", http.StatusNotFound, `{"name":"NotFoundError"}`)
	_, err = a.openSession(a.Context(), "ses_gone")
	assert.ErrorContains(t, err, "ses_gone")
	assert.ErrorContains(t, err, "MiMo holds no such session")

	server.respond("GET /session/ses_broken", http.StatusInternalServerError, `{}`)
	_, err = a.openSession(a.Context(), "ses_broken")
	assert.ErrorContains(t, err, "ses_broken")

	_, err = a.openSession(a.Context(), "../config")
	assert.ErrorContains(t, err, "not a MiMo session id", "a stored handle that is no session id reaches no route")
}

// A context clear replaces the session on the same server. What belonged to the
// old session ends with it, because its events name a session the agent no
// longer reads.
func TestClearContext(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a,
		workflowEvent(t, eventWorkflowStarted, testSessionID, "wf_1", map[string]any{"name": "review"}),
		questionAskedEvent(t, "que_1", testSessionID),
		goalEvent(t, testSessionID, "ship it", nil),
		textPartEvent(t, partTypeText, "prt_t", "msg_p", "", false),
		deltaEvent(t, "prt_t", "msg_p", "Half"),
	)
	a.manualCompactionID = "prt_old"
	a.manualCompactionReady = true
	a.compactionAck = make(chan struct{})
	a.manualFollowupSending = true
	a.manualFollowupBusy = true

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "ses_created", sessionID)
	assert.False(t, a.sessionSwitching)
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1, "the old session's running turn is aborted")
	assert.Equal(t, "ses_created", sink.LastSessionID())
	assert.Equal(t, []string{"mimo-question:que_1"}, sink.CanceledControls())
	waitFor(t, func() bool { return len(server.requestsTo("POST /question/que_1/reject")) == 1 }, "the old question is rejected")
	assert.Equal(t, bgtask.StatusStopped, backgroundTask(t, &sink.Sink, spawnSpanID).Status)
	assert.Equal(t, bgtask.StatusStopped, backgroundTask(t, &sink.Sink, "mimo-workflow:wf_1").Status)
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots(), "MiMo holds a goal per session")
	last, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.False(t, last)
	assert.False(t, a.turnActive)
	assert.Empty(t, a.manualCompactionID)
	assert.Nil(t, a.compactionAck)
	assert.False(t, a.manualCompactionReady)
	assert.False(t, a.manualFollowupSending)
	assert.False(t, a.manualFollowupBusy)

	messages := sink.Messages()
	_, text, completion := assembledText(t, messages[len(messages)-1].Content)
	assert.Equal(t, "Half", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)

	before := len(sink.TurnActives())
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Len(t, sink.TurnActives(), before, "the old session's idle belongs to a session the agent left")

	require.NoError(t, a.SendInput("fresh start", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_created/prompt_async"), 1)
}

func eventWithSession(t *testing.T, raw []byte, sessionID string) []byte {
	t.Helper()
	var event map[string]any
	require.NoError(t, json.Unmarshal(raw, &event))
	properties, ok := event["properties"].(map[string]any)
	require.True(t, ok, "the native fixture requires an event properties object")
	properties["sessionID"] = sessionID
	for _, key := range []string{"info", "part"} {
		if nested, ok := properties[key].(map[string]any); ok {
			nested["sessionID"] = sessionID
		}
	}
	updated, err := json.Marshal(event)
	require.NoError(t, err)
	return updated
}

func TestClearContextKeepsAnUnpersistedNativeFinalPart(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The old full final."}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_old", "part_old", "The old prefix.")
	controlled.reject.Store(1)
	feed(a, nativeAbortMarker(t, "session", "msg_old"),
		textPartEvent(t, partTypeText, "part_old", "msg_old", "The old full final.", true))
	_, err := a.ClearContext()
	require.NoError(t, err)
	controlled.reject.Store(0)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	rows := assembledRows(sink)
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The old full final.", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.Equal(t, testSessionID, rows[0].AgentSessionID, "the retry retains the old native session")
}

func TestFailedPendingMessageDoesNotBlockTheCurrentStreamAtClear(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The old full final."}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_old", "part_old", "The old prefix.")
	controlled.reject.Store(10)
	feed(a, nativeAbortMarker(t, "session", "msg_old"),
		textPartEvent(t, partTypeText, "part_old", "msg_old", "The old full final.", true),
		messageEvent(t, "msg_old", roleAssistant, mainActorID, true),
		messageEvent(t, "msg_new", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "part_new", "msg_new", "", false),
		deltaEvent(t, "part_new", "msg_new", "The new streamed answer."))
	_, err := a.ClearContext()
	require.NoError(t, err)
	rows := assembledRows(sink)
	require.Len(t, rows, 1, "a different failed final must not prevent the current stream's own flush")
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The new streamed answer.", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	controlled.reject.Store(0)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	rows = assembledRows(sink)
	require.Len(t, rows, 2)
	_, text, completion = assembledText(t, rows[1].Content)
	assert.Equal(t, "The old full final.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
}

func TestClearContextKeepsAnUnpersistedStreamedPart(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The old streamed answer."}
	a.sink = controlled
	beginPartialAnswer(t, a, "msg_old_stream", "part_old_stream", "The old streamed answer.")
	controlled.reject.Store(10)
	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Empty(t, assembledRows(sink))
	controlled.reject.Store(0)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	rows := assembledRows(sink)
	require.Len(t, rows, 1, "the failed scope survives its session clear")
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The old streamed answer.", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.Equal(t, testSessionID, rows[0].AgentSessionID)
}

func TestClearContextKeepsTheOldChildTargetForItsRetainedPart(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The old child full final."}
	a.sink = controlled
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a, messageEvent(t, "msg_old_child", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_old_child", "msg_old_child", "", false),
		deltaEvent(t, "part_old_child", "msg_old_child", "The old child prefix."))
	controlled.reject.Store(3)
	feed(a, nativeAbortMarker(t, "session", "msg_old_child"),
		textPartEvent(t, partTypeText, "part_old_child", "msg_old_child", "The old child full final.", true),
		actorAbortMessageEvent(t, "msg_old_child", actorID))
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	controlled.reject.Store(0)
	for _, event := range [][]byte{
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_new_parent", roleAssistant, mainActorID, false),
		actorRegisteredEvent(t, actorID, true),
		toolPartEvent(t, "part_new_spawn", "msg_new_parent", contracts.MiMoToolActor, "call_new_spawn", toolState{
			Status: contracts.MiMoToolStatusRunning, Input: spawnInputOf(contracts.MiMoActorActionSpawn, "New child", "The new task."),
			Metadata: map[string]any{"actorId": actorID, "sessionId": sessionID},
		}), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_new_child", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_new_child", "msg_new_child", "The new child answer.", true),
	} {
		feed(a, eventWithSession(t, event, sessionID))
	}
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	old := assembledRows(sink.Child("child-of-" + spawnSpanID))
	require.Len(t, old, 1)
	_, text, completion := assembledText(t, old[0].Content)
	assert.Equal(t, "The old child full final.", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.Equal(t, testSessionID, old[0].AgentSessionID)
	current := assembledRows(sink.Child("child-of-part_new_spawn"))
	require.Len(t, current, 1)
	_, text, completion = assembledText(t, current[0].Content)
	assert.Equal(t, "The new child answer.", text)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	assert.Equal(t, "The new child answer.", a.actors[actorID].lastText, "the old retry must not change the new actor's report")
}

func TestRetainedToolCannotClaimTheNewSessionsRepeatedCallID(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"opening", "closing"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "part_old_tool"}
			a.sink = controlled
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_old_tool", roleAssistant, mainActorID, false))
			if stage == "opening" {
				controlled.reject.Store(10)
			}
			oldOpening := toolPartEvent(t, "part_old_tool", "msg_old_tool", contracts.MiMoToolBash, "call_repeat", toolState{
				Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf old"},
			})
			feed(a, oldOpening)
			controlled.reject.Store(10)
			oldFinal := toolPartEvent(t, "part_old_tool", "msg_old_tool", contracts.MiMoToolBash, "call_repeat", toolState{
				Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf old"}, Output: "old",
			})
			feed(a, oldFinal)
			sessionID, err := a.ClearContext()
			require.NoError(t, err)
			feed(a, eventWithSession(t, statusEvent(t, contracts.MiMoStatusTypeBusy), sessionID),
				eventWithSession(t, messageEvent(t, "msg_new_tool", roleAssistant, mainActorID, false), sessionID))
			newOpening := eventWithSession(t, toolPartEvent(t, "part_new_tool", "msg_new_tool", contracts.MiMoToolBash, "call_repeat", toolState{
				Status: contracts.MiMoToolStatusRunning, Input: map[string]any{"command": "printf new"},
			}), sessionID)
			feed(a, newOpening)
			var newRows []agenttest.Message
			for _, row := range sink.Messages() {
				if row.AgentSessionID == sessionID && !row.TurnEnd {
					newRows = append(newRows, row)
				}
			}
			require.Len(t, newRows, 1, "the new native part must open despite a repeated model CallID")
			assert.JSONEq(t, string(newOpening), string(newRows[0].Content))
			assert.Equal(t, "part_new_tool", newRows[0].SpanID)
			controlled.reject.Store(0)
			a.flushPendingMessage("msg_old_tool", agent.MessageCompletionComplete)
			a.Mu.Lock()
			newCall := a.tools["part_new_tool"]
			var last []byte
			if newCall != nil {
				last = append([]byte(nil), newCall.lastFrame...)
			}
			a.Mu.Unlock()
			require.NotNil(t, newCall)
			assert.JSONEq(t, string(newOpening), string(last), "an old closing retry cannot clear the new native frame")
			newFinal := eventWithSession(t, toolPartEvent(t, "part_new_tool", "msg_new_tool", contracts.MiMoToolBash, "call_repeat", toolState{
				Status: contracts.MiMoToolStatusCompleted, Input: map[string]any{"command": "printf new"}, Output: "new",
			}), sessionID)
			feed(a, newFinal)
			var oldFinalRows, newFinalRows []agenttest.Message
			for _, row := range sink.Messages() {
				if !row.Closing {
					continue
				}
				switch row.AgentSessionID {
				case testSessionID:
					oldFinalRows = append(oldFinalRows, row)
				case sessionID:
					newFinalRows = append(newFinalRows, row)
				}
			}
			require.Len(t, oldFinalRows, 1)
			require.Len(t, newFinalRows, 1)
			assert.JSONEq(t, string(oldFinal), string(oldFinalRows[0].Content))
			assert.JSONEq(t, string(newFinal), string(newFinalRows[0].Content))
			assert.Equal(t, "part_old_tool", oldFinalRows[0].SpanID)
			assert.Equal(t, "part_new_tool", newFinalRows[0].SpanID)
			assert.Equal(t, []agenttest.SpanOpen{{SpanID: "part_new_tool"}}, newFinalRows[0].SpansOpenAtPersist)
		})
	}
}

// A subagent of the old session can still stream when the context clears. Its
// unfinished text and its open command stay in its own transcript, marked as
// interrupted, as the main agent's do.
func TestClearContextFinishesTheSubagentsUnfinishedOutput(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feedUnfinishedTurn(t, a)

	_, err := a.ClearContext()
	require.NoError(t, err)
	childRows := sink.Child("child-of-" + spawnSpanID).Messages()
	require.Len(t, childRows, 4, "the prompt, the command's opener, the unfinished text, the command's closer")
	_, text, completion := assembledText(t, childRows[2].Content)
	assert.Equal(t, "Sub half", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.True(t, childRows[3].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, childRows[3].Completion)
	assert.Equal(t, bgtask.StatusStopped, backgroundTask(t, sink, spawnSpanID).Status)
}

// An abort that fails leaves the old session's turn running on the server, but
// the agent no longer reads that session. The clear still moves to the new
// session, and the usage readout starts again with it.
func TestClearContextWhoseAbortFailsStillMovesOn(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("POST /session/ses_test/abort", http.StatusInternalServerError, `{}`)
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
			"id": "msg_1", "sessionID": testSessionID, "role": roleAssistant, "agentID": mainActorID, "cost": 0.5,
		}}),
	)
	require.True(t, a.usageSnapshot().hasCost)

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "ses_created", sessionID)
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1)
	assert.Equal(t, "ses_created", sink.LastSessionID())
	assert.False(t, a.turnActive)
	assert.False(t, a.usageSnapshot().hasCost, "the new session has cost nothing yet")
}

// A failure that no turn end persisted has none that can persist it once the
// context clears: the old session's events no longer reach the agent. It stays
// in the transcript as a notification, as it does when the process ends.
func TestClearContextPersistsAFailureThatNoTurnEndReported(t *testing.T) {
	t.Parallel()

	t.Run("a failure held while a subagent runs", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSinkTestAgent(t)
		spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
		feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
		failure := sessionErrorEvent(t, "ProviderModelNotFoundError", "no such model")
		feed(a, failure)
		require.Zero(t, sink.NotificationCount(), "a running subagent could still claim it")

		_, err := a.ClearContext()
		require.NoError(t, err)
		require.Equal(t, 1, sink.NotificationCount())
		assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
		assert.Empty(t, a.pendingFailures)
	})

	t.Run("the failure of the running turn", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSinkTestAgent(t)
		failure := sessionErrorEvent(t, "APIError", "bad request")
		feed(a,
			statusEvent(t, contracts.MiMoStatusTypeBusy),
			failure,
			failedMessageEvent(t, "msg_1", mainActorID, "APIError", "bad request"),
		)
		require.Zero(t, sink.NotificationCount(), "inside a turn, the failure waits for the turn end")

		_, err := a.ClearContext()
		require.NoError(t, err)
		require.Equal(t, 1, sink.NotificationCount())
		assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
		assert.Nil(t, a.turnFailure)

		newStatus := func(statusType string) []byte {
			return eventJSON(t, contracts.MiMoEventSessionStatus, map[string]any{
				"sessionID": "ses_created", "status": map[string]any{"type": statusType},
			})
		}
		feed(a, newStatus(contracts.MiMoStatusTypeBusy), newStatus(contracts.MiMoStatusTypeIdle))
		messages := sink.Messages()
		require.NotEmpty(t, messages)
		turnEnd := messages[len(messages)-1]
		require.True(t, turnEnd.TurnEnd)
		assert.Equal(t, agent.MessageCompletionComplete, turnEnd.Completion, "the next turn does not inherit the old failure")
	})
}

func TestClearContextWithoutATurnAbortsNothing(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/abort"))
}

func TestClearContextAbortsAPendingManualCompaction(t *testing.T) {
	t.Parallel()
	a, _, server := newSinkTestAgent(t)
	a.manualCompactionReady = true

	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1)
	assert.False(t, a.manualCompactionReady)
}

func TestClearContextRefusesOldSessionInputDuringCreation(t *testing.T) {
	t.Parallel()
	a, _, server := newSinkTestAgent(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCreation := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseCreation)
	server.handle("POST /session", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `{"id":"ses_created","directory":"/work","title":"New session"}`)
	})

	result := make(chan error, 1)
	go func() { _, err := a.ClearContext(); result <- err }()
	select {
	case <-entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the new session request did not start")
	}

	assert.ErrorIs(t, a.SendInput("old session input", nil), agent.ErrAgentBusy)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))
	assert.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/summarize"))
	releaseCreation()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the new session request did not answer")
	}
	require.NoError(t, a.SendInput("new session input", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_created/prompt_async"), 1)
}

func TestClearContextCreationFailureReleasesQueuedInput(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCreation := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseCreation)
	server.handle("POST /session", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusInternalServerError, `{"name":"CreateFailed"}`)
	})

	result := make(chan error, 1)
	go func() { _, err := a.ClearContext(); result <- err }()
	select {
	case <-entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the replacement session request did not start")
	}
	assert.ErrorIs(t, a.SendInput("queued while switching", nil), agent.ErrAgentBusy)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))

	releaseCreation()
	select {
	case err := <-result:
		require.ErrorContains(t, err, "create a MiMo session")
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the failed replacement request did not answer")
	}
	assert.Equal(t, []bool{false}, sink.TurnActives(), "the old session releases input that waited for creation")
	require.NoError(t, a.SendInput("retry on old session", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_test/prompt_async"), 1)
}

func TestClearContextCreationFailureKeepsPendingCompactionTurn(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRoute := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRoute)
	server.handle("POST /session/ses_test/summarize", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		feed(a, messageEvent(t, "msg_manual", roleUser, mainActorID, false), eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
			"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
			"type": contracts.MiMoPartTypeCompaction, "auto": false,
		}}))
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `true`)
	})
	require.NoError(t, a.CompactContext())
	assert.Equal(t, "prt_manual", a.manualCompactionID)
	server.respond("POST /session", http.StatusInternalServerError, `{"name":"CreateFailed"}`)
	_, err := a.ClearContext()
	require.ErrorContains(t, err, "create a MiMo session")
	active, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.True(t, active, "the old compaction still owns the queue turn")
	assert.ErrorIs(t, a.SendInput("too early", nil), agent.ErrAgentBusy)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))

	releaseRoute()
	waitFor(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return a.manualCompactionID == ""
	}, "the old compaction route settles")
	require.NoError(t, a.SendInput("after no-op", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_test/prompt_async"), 1)
}

func TestClearContextAbortsAnOrdinaryPromptStillBeingSent(t *testing.T) {
	t.Parallel()
	a, _, server := newSinkTestAgent(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releasePrompt := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releasePrompt)
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	})

	result := make(chan error, 1)
	go func() { result <- a.SendInput("old session input", nil) }()
	select {
	case <-entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the old prompt did not reach MiMo")
	}

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "ses_created", sessionID)
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1, "the old pending prompt needs a native abort")
	releasePrompt()
	select {
	case <-result:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the old prompt did not answer")
	}
	require.NoError(t, a.SendInput("new session input", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_created/prompt_async"), 1)
}

func TestClearContextThatCannotCreateASessionKeepsTheOldOne(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("POST /session", http.StatusInternalServerError, `{}`)

	_, err := a.ClearContext()
	assert.ErrorContains(t, err, "create a MiMo session")
	assert.Equal(t, testSessionID, a.sessionID)
	assert.False(t, a.sessionSwitching)
	require.NoError(t, a.SendInput("the old session still works", nil))

	a.SetStoppedForTest(true)
	_, err = a.ClearContext()
	assert.ErrorContains(t, err, "stopped")
}

func TestCompactContext(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	// The route answers only after the compaction and the turn after it finish.
	// The compaction part that starts it arrives first, while the route blocks.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server.handle("POST /session/ses_test/summarize", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		a.HandleOutput(eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
			"id": "prt_c", "messageID": "msg_u", "sessionID": testSessionID, "type": contracts.MiMoPartTypeCompaction, "auto": false,
		}}))
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `true`)
	})

	require.NoError(t, a.CompactContext())
	requests := server.requestsTo("POST /session/ses_test/summarize")
	require.Len(t, requests, 1)
	assert.JSONEq(t, `{"providerID":"mock","modelID":"alpha"}`, string(requests[0].Body))
	assert.Equal(t, 1, sink.NotificationCount())
	assert.Nil(t, a.compactionAck)
}

func TestManualCompactionCompletionReleasesTheNextInput(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	release := make(chan struct{})
	replied := make(chan struct{})
	var releaseOnce sync.Once
	releaseRoute := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRoute)
	server.handle("POST /session/ses_test/summarize", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		a.HandleOutput(eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
			"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
			"type": contracts.MiMoPartTypeCompaction, "auto": false,
		}}))
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `true`)
		close(replied)
	})

	require.NoError(t, a.CompactContext())
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	assert.ErrorIs(t, a.SendInput("continue", nil), agent.ErrAgentBusy)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))

	ended := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": false,
		"projection": map[string]any{"summary": "The context is ready."},
	}})
	feed(a, messageEvent(t, "msg_manual", roleUser, mainActorID, false), ended, ended)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	assert.Len(t, sink.PersistedNotifications(), 2, "the repeated completed part persists no second notification")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	assert.Equal(t, []bool{true, false}, sink.TurnActives(), "a late busy restatement cannot hold the next input")
	require.NoError(t, a.SendInput("continue", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_test/prompt_async"), 1)
	assert.Equal(t, []bool{true, false}, sink.TurnActives(), "the native busy event starts the accepted turn")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	assert.Equal(t, []bool{true, false, true}, sink.TurnActives())

	releaseRoute()
	select {
	case <-replied:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the native compaction route did not answer")
	}
	assert.Equal(t, []bool{true, false, true}, sink.TurnActives(), "the late route reply changes no turn")
}

func TestManualCompactionIdleThenBusyKeepsTheFollowupReady(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	a.compactionAck = make(chan struct{})
	started := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": false,
	}})
	ended := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": false,
		"projection": map[string]any{"summary": "The context is ready."},
	}})

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_manual", roleUser, mainActorID, false), started, ended)
	assert.True(t, a.manualCompactionReady)
	assert.False(t, a.turnActive)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())

	// The native summarize route can report idle, then restate busy before the
	// queued prompt enters its loop. Neither status owns a new user turn.
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle), statusEvent(t, contracts.MiMoStatusTypeBusy))
	assert.True(t, a.manualCompactionReady)
	assert.False(t, a.turnActive)
	require.NoError(t, a.SendInput("continue", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_test/prompt_async"), 1)
}

func TestManualCompactionIdleBeforeCompletionKeepsTheQueueHeld(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	a.compactionAck = make(chan struct{})
	started := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": false,
	}})
	ended := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": false,
		"projection": map[string]any{"summary": "The context is ready."},
	}})

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "msg_manual", roleUser, mainActorID, false), started)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.True(t, a.turnActive, "an idle before the summary must not release the queued prompt")
	assert.Equal(t, []bool{true}, sink.TurnActives())
	assert.ErrorIs(t, a.SendInput("too early", nil), agent.ErrAgentBusy)
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))

	feed(a, ended, statusEvent(t, contracts.MiMoStatusTypeBusy))
	assert.True(t, a.manualCompactionReady)
	assert.False(t, a.turnActive)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	require.NoError(t, a.SendInput("after summary", nil))
	assert.Len(t, server.requestsTo("POST /session/ses_test/prompt_async"), 1)
}

func TestManualCompactionRouteFailureAfterEarlyIdleReleasesTheQueue(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRoute := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRoute)
	server.handle("POST /session/ses_test/summarize", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		feed(a, messageEvent(t, "msg_manual", roleUser, mainActorID, false), eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
			"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
			"type": contracts.MiMoPartTypeCompaction, "auto": false,
		}}))
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusInternalServerError, `{"name":"CompactionFailed"}`)
	})

	require.NoError(t, a.CompactContext())
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.True(t, a.turnActive)
	releaseRoute()
	waitFor(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.turnActive && a.manualCompactionID == ""
	}, "the failed native route releases the manual compaction turn")
	notices := sink.LeapMuxNotifications()
	require.Len(t, notices, 1)
	assert.Equal(t, contracts.NotificationTypeAgentError, notices[0][contracts.NotificationFieldType])
	assert.Contains(t, notices[0][contracts.NotificationFieldText], "MiMo could not compact the context")
	require.NoError(t, a.SendInput("continue after failure", nil))
}

func TestManualCompactionSuccessfulRouteWithoutCompletedPartReleasesTheQueue(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	release, replied := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseRoute := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRoute)
	server.handle("POST /session/ses_test/summarize", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		feed(a, messageEvent(t, "msg_manual", roleUser, mainActorID, false), eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
			"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
			"type": contracts.MiMoPartTypeCompaction, "auto": false,
		}}))
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `true`)
		close(replied)
	})

	require.NoError(t, a.CompactContext())
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.True(t, a.turnActive, "the early idle waits for native completion")
	releaseRoute()
	select {
	case <-replied:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the native summarize route did not answer")
	}
	waitFor(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return !a.turnActive && a.manualCompactionID == ""
	}, "a successful route without a completed part releases the queue")
	assert.False(t, a.manualCompactionReady)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	require.NoError(t, a.SendInput("continue after no-op", nil))
}

func TestManualCompactionRouteFindsADelayedCompletedPart(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRoute := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRoute)
	server.handle("GET /session/ses_test/message", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeJSON(w, http.StatusOK, `[{"info":{"id":"msg_manual","sessionID":"ses_test","role":"user"},"parts":[{"id":"prt_manual","messageID":"msg_manual","sessionID":"ses_test","type":"compaction","projection":{"summary":"The context is ready."}}]}]`)
	})
	server.handle("POST /session/ses_test/summarize", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		feed(a, messageEvent(t, "msg_manual", roleUser, mainActorID, false), eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
			"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
			"type": contracts.MiMoPartTypeCompaction, "auto": false,
		}}))
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `true`)
	})

	require.NoError(t, a.CompactContext())
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), statusEvent(t, contracts.MiMoStatusTypeIdle))
	releaseRoute()
	waitFor(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return a.manualCompactionReady && !a.turnActive
	}, "the stored summary releases the queue before its event arrives")
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	ended := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "prt_manual", "messageID": "msg_manual", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": false,
		"projection": map[string]any{"summary": "The context is ready."},
	}})
	feed(a, ended)
	assert.Equal(t, []bool{true, false}, sink.TurnActives(), "the delayed event releases no second turn")
	assert.Len(t, sink.PersistedNotifications(), 2, "the native start and completion both reach the transcript")
	require.NoError(t, a.SendInput("continue after summary", nil))
}

func TestManualCompactionFollowupBusyBeforeAcceptance(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	a.manualCompactionReady = true
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
		w.WriteHeader(http.StatusNoContent)
	})

	require.NoError(t, a.SendInput("continue", nil))
	assert.False(t, a.manualCompactionReady)
	assert.False(t, a.manualFollowupSending)
	assert.False(t, a.manualFollowupBusy)
	assert.True(t, a.turnActive, "the accepted prompt owns the native busy event")
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the turn starts after the native route accepts the prompt")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Equal(t, []bool{true, false}, sink.TurnActives(), "native idle releases the accepted input")
}

func TestManualCompactionFollowupIdleBeforeAcceptanceStartsNoTurn(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	a.manualCompactionReady = true
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), statusEvent(t, contracts.MiMoStatusTypeIdle))
		w.WriteHeader(http.StatusNoContent)
	})

	require.NoError(t, a.SendInput("continue", nil))
	assert.False(t, a.manualCompactionReady)
	assert.False(t, a.manualFollowupSending)
	assert.False(t, a.manualFollowupBusy)
	assert.False(t, a.turnActive)
	assert.Equal(t, []bool{false}, sink.TurnActives(), "the native turn ended before its HTTP reply")
	assert.Len(t, server.requestsTo("POST /session/ses_test/prompt_async"), 1)
}

func TestManualCompactionFollowupRefusalKeepsTheReadyState(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	a.manualCompactionReady = true
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
		writeJSON(w, http.StatusBadRequest, `{"name":"BadRequest"}`)
	})

	require.Error(t, a.SendInput("continue", nil))
	assert.True(t, a.manualCompactionReady, "a later input can still join the native loop")
	assert.False(t, a.manualFollowupSending)
	assert.False(t, a.manualFollowupBusy)
	assert.False(t, a.turnActive)
	assert.Empty(t, sink.TurnActives(), "a refused prompt starts no turn")
}

func TestManualCompactionFollowupSessionReplacementAbortsOldAndKeepsNewSend(t *testing.T) {
	t.Parallel()
	a, _, server := newSinkTestAgent(t)
	oldEntered, oldRelease := make(chan struct{}), make(chan struct{})
	newEntered, newRelease := make(chan struct{}), make(chan struct{})
	var oldOnce, newOnce sync.Once
	releaseOld := func() { oldOnce.Do(func() { close(oldRelease) }) }
	releaseNew := func() { newOnce.Do(func() { close(newRelease) }) }
	t.Cleanup(releaseOld)
	t.Cleanup(releaseNew)
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		close(oldEntered)
		select {
		case <-oldRelease:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server.handle("POST /session/ses_created/prompt_async", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		close(newEntered)
		select {
		case <-newRelease:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	})

	a.manualCompactionReady = true
	oldResult := make(chan error, 1)
	go func() { oldResult <- a.SendInput("old follow-up", nil) }()
	select {
	case <-oldEntered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the old prompt did not reach MiMo")
	}

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "ses_created", sessionID)
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1, "the old pending prompt needs a native abort")

	a.Mu.Lock()
	a.manualCompactionReady = true
	a.Mu.Unlock()
	newResult := make(chan error, 1)
	go func() { newResult <- a.SendInput("new follow-up", nil) }()
	select {
	case <-newEntered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the new prompt did not reach MiMo")
	}

	releaseOld()
	select {
	case <-oldResult:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the old prompt did not answer")
	}
	a.Mu.Lock()
	assert.True(t, a.manualCompactionReady)
	assert.True(t, a.manualFollowupSending, "the old reply cannot clear the new session's send")
	a.Mu.Unlock()

	releaseNew()
	select {
	case err := <-newResult:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the new prompt did not answer")
	}
}

func TestCompactContextThatTheServerRefuses(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("POST /session/ses_test/summarize", http.StatusBadRequest, `{"name":"BadRequest"}`)

	err := a.CompactContext()
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Nil(t, a.compactionAck, "a refused compaction can be asked for again")
}

// A route that answers before any compaction part arrives leaves no native turn.
func TestCompactContextThatFinishesBeforeItsPart(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	require.NoError(t, a.CompactContext())
	assert.Nil(t, a.compactionAck)
	assert.Equal(t, []bool{false}, sink.TurnActives())
	require.NoError(t, a.SendInput("continue after no-op", nil))
}

func TestCompactContextRefusals(t *testing.T) {
	t.Parallel()

	a, server := newTestAgent(t, nil)
	a.model = ""
	assert.ErrorContains(t, a.CompactContext(), "has none")

	a.model = "mock/alpha"
	a.compactionAck = make(chan struct{})
	assert.ErrorContains(t, a.CompactContext(), "already pending")

	a.compactionAck = nil
	a.manualCompactionReady = true
	err := a.CompactContext()
	assert.ErrorContains(t, err, "before the next user message")
	assert.NotErrorIs(t, err, agent.ErrAgentBusy, "a second compaction must not hold the queue")
	a.manualCompactionReady = false
	a.sessionID = ""
	assert.ErrorContains(t, a.CompactContext(), "no MiMo session")
	assert.Nil(t, a.compactionAck)

	a.SetStoppedForTest(true)
	assert.ErrorContains(t, a.CompactContext(), "stopped")
	assert.Empty(t, server.allRequests())
}

// blockSummarize makes the compaction route hold its answer until the test
// ends, as MiMo does until the compaction and the turn after it end.
func blockSummarize(t *testing.T, server *fakeServer) {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server.handle("POST /session/ses_test/summarize", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `true`)
	})
}

// compactOnClock runs CompactContext while its start wait is trapped, and
// returns the call's result channel and the wait that the timer asked for.
func compactOnClock(t *testing.T, a *Agent, clock *quartz.Mock) (<-chan error, time.Duration) {
	t.Helper()
	start := clock.Trap().NewTimer(mimoCompactionStartTimerTag)
	defer start.Close()
	result := make(chan error, 1)
	go func() { result <- a.CompactContext() }()
	return result, testutil.WaitForTimer(t, testutil.DeadlineContext(t), start)
}

// The compaction that MiMo never starts ends the wait at its limit, and a later
// compaction can be asked for. A start just inside the limit confirms it.
func TestCompactContextStartWait(t *testing.T) {
	t.Parallel()

	t.Run("a compaction that never starts ends the wait at its limit", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		a, server := newTestAgent(t, nil)
		clock := useMockClock(t, a)
		blockSummarize(t, server)

		result, wait := compactOnClock(t, a, clock)
		assert.Equal(t, mimoCompactionStartWait, wait)
		clock.Advance(mimoCompactionStartWait).MustWait(ctx)
		select {
		case err := <-result:
			assert.ErrorContains(t, err, "did not start the compaction within 30s")
		case <-ctx.Done():
			t.Fatal("the wait did not end at its limit")
		}
		assert.Nil(t, a.compactionAck, "a later compaction can be asked for")
	})

	t.Run("a compaction that starts just inside the limit confirms the call", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		a, sink, server := newSinkTestAgent(t)
		clock := useMockClock(t, a)
		blockSummarize(t, server)

		result, _ := compactOnClock(t, a, clock)
		clock.Advance(mimoCompactionStartWait - time.Nanosecond).MustWait(ctx)
		feed(a, eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
			"id": "prt_c", "messageID": "msg_u", "sessionID": testSessionID, "type": contracts.MiMoPartTypeCompaction,
		}}))
		select {
		case err := <-result:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("the compaction's start did not confirm the call")
		}
		assert.Equal(t, 1, sink.NotificationCount())
		_, pending := clock.Peek()
		assert.False(t, pending, "the call stops the wait's timer")
	})
}

// A process that exits while the compaction waits to start ends the wait, and
// a later compaction can be asked for.
func TestCompactContextEndsWhenTheProcessExits(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	blockSummarize(t, server)
	a.SimulateExitForTest()

	assert.ErrorContains(t, a.CompactContext(), "exited")
	assert.Nil(t, a.compactionAck)
}

// A subagent's compaction is its own. It does not confirm the compaction the
// user asked of the main agent.
func TestSubagentCompactionDoesNotConfirmTheMainOne(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	ack := make(chan struct{})
	a.compactionAck = ack

	feed(a,
		messageEvent(t, "msg_cc", roleUser, actorID, false),
		eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
			"id": "prt_cc", "messageID": "msg_cc", "sessionID": testSessionID, "type": contracts.MiMoPartTypeCompaction,
		}}),
	)
	assert.Equal(t, ack, a.compactionAck)
	assert.Zero(t, sink.NotificationCount())
	assert.Equal(t, 1, sink.Child("child-of-"+spawnSpanID).NotificationCount(), "the notice goes to the subagent's transcript")
}

func TestLateOmittedSessionCannotChangeARetainedOldPart(t *testing.T) {
	t.Parallel()
	for _, update := range []string{"delta", "final"} {
		t.Run(update, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The original retained bytes."}
			a.sink = controlled
			beginPartialAnswer(t, a, "msg_old_omitted", "part_old_omitted", "The original retained bytes.")
			controlled.reject.Store(10)
			_, err := a.ClearContext()
			require.NoError(t, err)
			if update == "delta" {
				feed(a, eventWithSession(t, deltaEvent(t, "part_old_omitted", "msg_old_omitted", " FOREIGN"), ""))
			} else {
				feed(a, eventWithSession(t, textPartEvent(t, partTypeText, "part_old_omitted", "msg_old_omitted", "A foreign current final.", true), ""))
			}
			controlled.reject.Store(0)
			a.flushUnfinishedOutput(agent.MessageCompletionComplete)
			rows := assembledRows(sink)
			require.Len(t, rows, 1)
			_, text, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, "The original retained bytes.", text)
			assert.Equal(t, testSessionID, rows[0].AgentSessionID)
			assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
		})
	}
}
