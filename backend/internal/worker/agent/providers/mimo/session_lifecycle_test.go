package mimo

import (
	"net/http"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "ses_created", sessionID)
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1, "the old session's running turn is aborted")
	assert.Equal(t, "ses_created", sink.LastSessionID())
	assert.Equal(t, []string{"mimo-question:que_1"}, sink.CanceledControls())
	waitFor(t, func() bool { return len(server.requestsTo("POST /question/que_1/reject")) == 1 }, "the old question is rejected")
	assert.Equal(t, bgtask.StatusStopped, backgroundTask(t, &sink.Sink, spawnCallID).Status)
	assert.Equal(t, bgtask.StatusStopped, backgroundTask(t, &sink.Sink, "mimo-workflow:wf_1").Status)
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots(), "MiMo holds a goal per session")
	last, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.False(t, last)
	assert.False(t, a.turnActive)

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

// A subagent of the old session can still stream when the context clears. Its
// unfinished text and its open command stay in its own transcript, marked as
// interrupted, as the main agent's do.
func TestClearContextFinishesTheSubagentsUnfinishedOutput(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feedUnfinishedTurn(t, a)

	_, err := a.ClearContext()
	require.NoError(t, err)
	childRows := sink.Child("child-of-" + spawnCallID).Messages()
	require.Len(t, childRows, 4, "the prompt, the command's opener, the unfinished text, the command's closer")
	_, text, completion := assembledText(t, childRows[2].Content)
	assert.Equal(t, "Sub half", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.True(t, childRows[3].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, childRows[3].Completion)
	assert.Equal(t, bgtask.StatusStopped, backgroundTask(t, sink, spawnCallID).Status)
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
		assert.Nil(t, a.unattributed)
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

func TestClearContextThatCannotCreateASessionKeepsTheOldOne(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("POST /session", http.StatusInternalServerError, `{}`)

	_, err := a.ClearContext()
	assert.ErrorContains(t, err, "create a MiMo session")
	assert.Equal(t, testSessionID, a.sessionID)

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

func TestCompactContextThatTheServerRefuses(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("POST /session/ses_test/summarize", http.StatusBadRequest, `{"name":"BadRequest"}`)

	err := a.CompactContext()
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Nil(t, a.compactionAck, "a refused compaction can be asked for again")
}

// A route that answers before any compaction part arrives finished its work,
// so the compaction happened.
func TestCompactContextThatFinishesBeforeItsPart(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	require.NoError(t, a.CompactContext())
	assert.Nil(t, a.compactionAck)
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
	assert.Equal(t, 1, sink.Child("child-of-"+spawnCallID).NotificationCount(), "the notice goes to the subagent's transcript")
}
