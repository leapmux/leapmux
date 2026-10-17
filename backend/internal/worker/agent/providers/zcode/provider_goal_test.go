package zcode

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A state patch never states the goal. ZCode 3.14.5 sends a session patch of
// `status` alone or of the settings, and its `target_changed` event announces
// each goal change. So no patch shape reaches the goal: a stray `goal` key that
// the worker read would set, replace or clear the goal with no event behind it.
func TestZCodeGoal_AStatePatchNeverTouchesTheGoal(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		patch string
	}{
		{"a session patch of status", `{"scope":"session","sessionId":"sess-1","revision":13,"patch":{"status":"running"}}`},
		{"a session patch with a goal object", `{"scope":"session","sessionId":"sess-1","revision":12,"patch":{"goal":{"targetId":"t-1","objective":"green build","status":"paused"}}}`},
		{"a session patch with a null goal", `{"scope":"session","sessionId":"sess-1","revision":14,"patch":{"goal":null}}`},
		{"a workspace patch with a null goal", `{"scope":"workspace","patch":{"goal":null}}`},
		{"a patch for a replaced session", `{"scope":"session","sessionId":"sess-old","patch":{"goal":{"objective":"stale goal","status":"active"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			ag := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			ag.reportZCodeGoal(json.RawMessage(`{"targetId":"t-0","objective":"live goal","status":"active"}`), false)
			require.Len(t, sink.Goals(), 1)

			ag.handleZCodeStateUpdated(json.RawMessage(tc.patch))

			assert.Len(t, sink.Goals(), 1, "a patch sets no goal")
			assert.Zero(t, sink.GoalClears(), "a patch clears no goal")
		})
	}
}

func TestZCodeGoalPreservesDistinctNativeIdentities(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.reportZCodeGoal(json.RawMessage(`{"targetId":"first","objective":"Same objective","status":"active"}`), false)
	a.reportZCodeGoal(json.RawMessage(`{"targetId":"second","objective":"Same objective","status":"active"}`), false)
	updates := sink.Goals()
	require.Len(t, updates, 2)
	assert.Equal(t, "first", updates[0].NativeID, "each goal keeps the native id its writer stated")
	assert.Equal(t, "second", updates[1].NativeID, "the same objective under two ids stays two goals")
}

// The revision only moves forward. A stale patch arriving out of order must not
// pull it back, or the next session/goal would send an expectedRevision the
// app-server already passed and the write would be refused as a conflict that
// does not exist.
func TestZCodeGoal_StateRevisionIsMonotonic(t *testing.T) {
	t.Parallel()

	agent := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	agent.applyZCodeRuntimeState(&zcodeRuntimeState{StateRevision: 20})
	agent.applyZCodeRuntimeState(&zcodeRuntimeState{StateRevision: 7})

	agent.Mu.Lock()
	got := agent.stateRevision
	agent.Mu.Unlock()
	assert.EqualValues(t, 20, got)
}

func TestZCodeGoal_SupportsEveryAction(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	assert.ElementsMatch(t,
		[]agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume},
		a.SupportedGoalActions())
}

// A session snapshot RESTATES the goal, so it must be reported as one. Only the
// Codex path asserted the snapshot rule before, and ZCode reaches it through a
// different function.
func TestZCodeGoal_SessionSnapshotIsARestatement(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	agent.reportZCodeGoal(json.RawMessage(`{"objective":"resumed objective","status":"active"}`), true)

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "resumed objective", got.Objective)
	assert.True(t, got.Snapshot, "a resume restates the goal; it does not announce one")
}

// The same call with snapshot=false is what a `target_changed` event uses, and
// that one does announce. The pair is what proves the flag is threaded rather
// than hardcoded.
func TestZCodeGoal_AGoalChangeIsNotARestatement(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	agent.reportZCodeGoal(json.RawMessage(`{"objective":"live change","status":"active"}`), false)

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.False(t, got.Snapshot)
}

// A snapshot that says the session has NO goal clears it as a restatement too.
// Without the flag a resume writes "Goal cleared: X" for a clear nobody made.
func TestZCodeGoal_SnapshotNullClearIsARestatement(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	agent.reportZCodeGoal(json.RawMessage(`null`), true)

	assert.Equal(t, []bool{true}, sink.GoalClearSnapshots())
}

// The session snapshot is the only place a RESUMED session learns its
// revision before a turn ends. Reading eventSeq alone left it at 0, and
// session/goal then sent expectedRevision 0 against a live session whose
// revision was higher -- the app-server refused every goal action.
func TestZCodeGoal_SessionSnapshotSeedsTheStateRevision(t *testing.T) {
	t.Parallel()

	agent := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	snap, ok := agent.parseStateSnapshot(json.RawMessage(
		`{"session":{"sessionId":"sess-1"},"runtime":{"eventSeq":7,"stateRevision":42}}`))
	require.True(t, ok)

	agent.applyParsedStateSnapshot(snap)

	agent.Mu.Lock()
	got := agent.stateRevision
	agent.Mu.Unlock()
	assert.EqualValues(t, 42, got, "a resumed session must know its revision before its first turn ends")
}

// A settings reply folds its state document through applyStateSnapshot, and
// three setters do that. Reporting the goal from inside the fold let a model,
// effort or permission-mode change write the goal columns -- and a reply that
// spelled `goal: null` would DELETE a live goal with no transcript row.
func TestZCodeGoal_ASettingsSnapshotNeverTouchesTheGoal(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	// The shape a setter reply carries, with a goal key that must be ignored.
	_, ok := agent.applyStateSnapshot(json.RawMessage(
		`{"session":{"sessionId":"sess-1"},"runtime":{"eventSeq":3},"goal":null}`))

	require.True(t, ok)
	assert.Zero(t, sink.GoalClears(), "a settings reply must not clear the goal")
	assert.Empty(t, sink.Goals(), "nor set one")
}

// The revision the reply carries must be folded, or a second goal action before
// the next turn end sends the same expectedRevision twice and the app-server
// refuses it for a conflict that does not exist.
func TestZCodeGoal_TracksTheRevisionMonotonically(t *testing.T) {
	t.Parallel()

	agent := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	agent.noteZCodeStateRevision(12)
	agent.noteZCodeStateRevision(7)
	agent.noteZCodeStateRevision(0)

	agent.Mu.Lock()
	got := agent.stateRevision
	agent.Mu.Unlock()
	assert.EqualValues(t, 12, got, "a stale or absent revision never moves it backwards")
}

// A goal replacement starts a turn, which can advance the revision after the
// goal reply. Retry one revision conflict with the server's current revision.
func TestZCodeGoal_RetriesARevisionConflictOnce(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.Mu.Lock()
	a.stateRevision = 3
	a.Mu.Unlock()

	result := make(chan error, 1)
	go func() { _, err := a.PerformGoalAction(agent.GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	firstID := zcodeSentRequestID(t, first)
	conflict, err := json.Marshal(map[string]any{
		"id": firstID,
		"error": map[string]any{
			"code":    -32009,
			"message": "Session state revision mismatch",
			"data":    map[string]any{"actualRevision": 4},
		},
	})
	require.NoError(t, err)
	a.HandleOutput(conflict)

	require.Eventually(t, func() bool { return len(stdin.Requests(t)) == 2 },
		time.Second, 5*time.Millisecond, "the revision conflict must cause one retry")
	second := stdin.Requests(t)[1]
	var params struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	require.NoError(t, json.Unmarshal(second.Params, &params))
	assert.EqualValues(t, 4, params.ExpectedRevision)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second),
		json.RawMessage(`{"runtime":{"stateRevision":5}}`)))
	require.NoError(t, <-result)
}

// ZCode reports its own change. SetGoal must not write local state first.
func TestGoal_ZCodeWritesNothingLocally(t *testing.T) {
	t.Parallel()

	zsink := &agenttest.Sink{}
	stdin := &zcodeRecordedStdin{}
	zagent := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(zsink), stdin)
	// A reply with no goal: the event that states the goal comes separately.
	answerZCodeRequest(t, zagent, stdin, zcodeMethodSessionGoal, `{"response":"Goal active"}`)
	_, err := zagent.PerformGoalAction(agent.GoalActionSet, "ship it")
	require.NoError(t, err)
	assert.Empty(t, zsink.Goals(), "ZCode states the goal in its own target_changed event")
	assert.Zero(t, zsink.GoalClears())
}

// A conflict reply is the app-server's own answer about its own session, so it
// is the one source entitled to move the cached revision DOWN. Routing it
// through the monotonic note would resend the number that just failed.
func TestZCodeGoal_RetriesWithARevisionBelowTheCachedOne(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	ag := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	ag.Mu.Lock()
	ag.stateRevision = 9
	ag.Mu.Unlock()

	result := make(chan error, 1)
	go func() { _, err := ag.PerformGoalAction(agent.GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	ag.HandleOutput(zcodeConflictLine(t, zcodeSentRequestID(t, first), 4))

	require.Eventually(t, func() bool { return len(stdin.Requests(t)) == 2 },
		time.Second, 5*time.Millisecond, "a lower server revision must still retry")
	second := stdin.Requests(t)[1]
	assert.EqualValues(t, 4, zcodeGoalExpectedRevision(t, second),
		"the retry sends the revision the app-server reported, not the higher cached one")
	ag.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second),
		json.RawMessage(`{"runtime":{"stateRevision":5}}`)))
	require.NoError(t, <-result)
}

// The retry is one-shot. A second conflict returns the wire error rather than
// sending a third request, or a wedged app-server would take the goal write
// around for ever.
func TestZCodeGoal_StopsAfterOneRetry(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	ag := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	ag.Mu.Lock()
	ag.stateRevision = 3
	ag.Mu.Unlock()

	result := make(chan error, 1)
	go func() { _, err := ag.PerformGoalAction(agent.GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	ag.HandleOutput(zcodeConflictLine(t, zcodeSentRequestID(t, first), 4))
	require.Eventually(t, func() bool { return len(stdin.Requests(t)) == 2 },
		time.Second, 5*time.Millisecond, "the first conflict retries")
	second := stdin.Requests(t)[1]
	ag.HandleOutput(zcodeConflictLine(t, zcodeSentRequestID(t, second), 7))

	err := <-result
	require.Error(t, err, "a second conflict is reported, never retried again")
	assert.Len(t, stdin.Requests(t), 2, "no third request")
}

// Two requests that carry one inputId and different parameters are the worst
// input for any server-side duplicate check, so each attempt mints its own.
func TestZCodeGoal_MintsAFreshInputIDForTheRetry(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.Mu.Lock()
	a.stateRevision = 3
	a.Mu.Unlock()

	result := make(chan error, 1)
	go func() { _, err := a.PerformGoalAction(agent.GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	a.HandleOutput(zcodeConflictLine(t, zcodeSentRequestID(t, first), 4))
	require.Eventually(t, func() bool { return len(stdin.Requests(t)) == 2 },
		time.Second, 5*time.Millisecond, "the revision conflict must cause one retry")
	second := stdin.Requests(t)[1]

	assert.NotEqual(t, zcodeGoalInputID(t, first), zcodeGoalInputID(t, second))
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second),
		json.RawMessage(`{"runtime":{"stateRevision":5}}`)))
	require.NoError(t, <-result)
}

// A non-conflict error is reported at once. A retry there would send the same
// refused write a second time for no reason.
func TestZCodeGoal_DoesNotRetryANonConflictError(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	ag := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	result := make(chan error, 1)
	go func() { _, err := ag.PerformGoalAction(agent.GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	refusal, err := json.Marshal(map[string]any{
		"id":    zcodeSentRequestID(t, first),
		"error": map[string]any{"code": ErrSessionNotActive, "message": "no session"},
	})
	require.NoError(t, err)
	ag.HandleOutput(refusal)

	require.Error(t, <-result)
	assert.Len(t, stdin.Requests(t), 1, "only a revision mismatch retries")
}

// A conflict reply that states no actual revision cannot steer a retry, so the
// error is reported rather than retried against the same stale number.
func TestZCodeGoal_DoesNotRetryAConflictWithNoRevision(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	result := make(chan error, 1)
	go func() { _, err := a.PerformGoalAction(agent.GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	refusal, err := json.Marshal(map[string]any{
		"id": zcodeSentRequestID(t, first),
		"error": map[string]any{
			"code": ErrRevisionMismatch, "message": "Session state revision mismatch",
		},
	})
	require.NoError(t, err)
	a.HandleOutput(refusal)

	require.Error(t, <-result)
	assert.Len(t, stdin.Requests(t), 1)
}

// zcodeConflictLine builds a session/goal revision-mismatch reply.
func zcodeConflictLine(t *testing.T, id, actualRevision int64) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    ErrRevisionMismatch,
			"message": "Session state revision mismatch",
			"data":    map[string]any{"actualRevision": actualRevision},
		},
	})
	require.NoError(t, err)
	return line
}

func zcodeGoalExpectedRevision(t *testing.T, req zcodeSentRequest) int64 {
	t.Helper()
	var params struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	require.NoError(t, json.Unmarshal(req.Params, &params))
	return params.ExpectedRevision
}

func zcodeGoalInputID(t *testing.T, req zcodeSentRequest) string {
	t.Helper()
	var params struct {
		InputID string `json:"inputId"`
	}
	require.NoError(t, json.Unmarshal(req.Params, &params))
	require.NotEmpty(t, params.InputID)
	return params.InputID
}

// ZCode completes a goal action itself, so the caller has nothing to enqueue. An
// outcome that carried text here would send the objective to the model as a
// prompt on top of the side-band write that already happened.
func TestZCodeSideBandGoal_ReturnsNoQueuedInput(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{"response":"Goal active"}`)
	outcome, err := a.PerformGoalAction(agent.GoalActionSet, "ship it")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput, "ZCode performs the action itself")
}
