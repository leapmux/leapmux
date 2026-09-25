package mimo

import (
	"net/http"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func goalEvent(t *testing.T, sessionID, condition string, verdict map[string]any) []byte {
	t.Helper()
	properties := map[string]any{"sessionID": sessionID}
	if condition != "" {
		properties["goal"] = map[string]any{"condition": condition}
	}
	if verdict != nil {
		properties["lastVerdict"] = verdict
	}
	return eventJSON(t, eventSessionGoal, properties)
}

func TestSessionGoalLifecycle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	feed(a, goalEvent(t, testSessionID, "the tests pass", nil))
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "the tests pass", goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Nil(t, goal.Iterations)
	createdAt := goal.CreatedAt
	assert.False(t, createdAt.IsZero())

	feed(a, goalEvent(t, testSessionID, "the tests pass", map[string]any{"ok": false, "reason": "one test fails", "attempt": 1}))
	goal, _ = sink.LastGoal()
	assert.Equal(t, agent.GoalStatusActive, goal.Status, "a verdict that keeps the goal re-enters the loop")
	assert.Equal(t, "one test fails", goal.StatusDetail, "the judge's reason is what the next pass works on")
	require.NotNil(t, goal.Iterations)
	assert.Equal(t, int32(1), *goal.Iterations)
	assert.Equal(t, createdAt, goal.CreatedAt, "the same goal keeps its start")

	feed(a, goalEvent(t, testSessionID, "", map[string]any{"ok": true, "reason": "all pass", "attempt": 2}))
	goal, _ = sink.LastGoal()
	assert.Equal(t, agent.GoalStatusDone, goal.Status)
	assert.Equal(t, "all pass", goal.StatusDetail)
	assert.Equal(t, "the tests pass", goal.Objective)
	require.NotNil(t, goal.Iterations)
	assert.Equal(t, int32(2), *goal.Iterations)

	feed(a, goalEvent(t, testSessionID, "", nil))
	assert.Zero(t, sink.GoalClears(), "MiMo clears the goal it just ended, and the card keeps the verdict")

	feed(a, goalEvent(t, testSessionID, "the build is green", nil))
	goal, _ = sink.LastGoal()
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	feed(a, goalEvent(t, testSessionID, "", nil))
	assert.Equal(t, 1, sink.GoalClears(), "a clear with no verdict is the user's")
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots())
}

func TestSessionGoalEndsThatAreNotASuccess(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		verdict map[string]any
		detail  string
	}{
		{name: "the judge finds it impossible", verdict: map[string]any{"ok": false, "impossible": true, "reason": "no network", "attempt": 1},
			detail: "impossible: no network"},
		{name: "MiMo reaches its re-entry cap", verdict: map[string]any{"ok": false, "reason": "still failing", "attempt": 21},
			detail: "retry limit reached: still failing"},
		{name: "the judge fails", verdict: map[string]any{"ok": true, "reason": "judge error", "attempt": 0, "error": true},
			detail: "the judge failed: judge error"},
		{name: "a verdict with no reason", verdict: map[string]any{"ok": false, "impossible": true, "reason": " ", "attempt": 1},
			detail: goalDetailImpossible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a, goalEvent(t, testSessionID, "ship it", nil), goalEvent(t, testSessionID, "", tc.verdict))
			goal, _ := sink.LastGoal()
			assert.Equal(t, agent.GoalStatusBlocked, goal.Status)
			assert.Equal(t, tc.detail, goal.StatusDetail)
		})
	}
}

func TestSessionGoalOfAnotherSessionIsIgnored(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, goalEvent(t, "ses_left", "old goal", nil), goalEvent(t, "ses_left", "", nil))
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

func TestSessionGoalVerdictWithoutAKnownGoal(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, goalEvent(t, testSessionID, "", map[string]any{"ok": true, "reason": "done", "attempt": 1}))
	assert.Empty(t, sink.Goals(), "a verdict names no goal, and the worker knows none to end")
}

// A clear that arrives while a set waits for its confirmation removes the old
// goal and keeps the wait. The goal that the set sent confirms it afterwards.
func TestSessionGoalClearKeepsAWaitingSet(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, goalEvent(t, testSessionID, "the old goal", nil))
	waiter := &goalWaiter{done: make(chan struct{})}
	a.goal.setWaiter = waiter

	feed(a, goalEvent(t, testSessionID, "", nil))
	assert.Equal(t, 1, sink.GoalClears())
	assert.Same(t, waiter, a.goal.setWaiter, "the clear keeps the set that waits")
	assert.Empty(t, a.goal.condition)

	feed(a, goalEvent(t, testSessionID, "the new goal", nil))
	select {
	case <-waiter.done:
	default:
		t.Fatal("the new goal confirms the set that waits")
	}
	assert.Nil(t, a.goal.setWaiter)
	goal, _ := sink.LastGoal()
	assert.Equal(t, "the new goal", goal.Objective)
}

// An event with a blank condition and no verdict states neither a goal nor a
// clear, and an event that cannot be read states nothing.
func TestSessionGoalEventsThatStateNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, goalEvent(t, testSessionID, "ship it", nil))
	before := len(sink.Goals())

	feed(a,
		eventJSON(t, eventSessionGoal, map[string]any{"sessionID": testSessionID, "goal": map[string]any{"condition": "  "}}),
		eventJSON(t, eventSessionGoal, "not an object"),
	)
	assert.Len(t, sink.Goals(), before)
	assert.Zero(t, sink.GoalClears())
	assert.Equal(t, "ship it", a.goal.condition)
}

func TestSupportedGoalActions(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, a.SupportedGoalActions(),
		"MiMo has no pause and no resume")

	a.SetStoppedForTest(true)
	assert.Empty(t, a.SupportedGoalActions())

	withoutSession, _ := newTestAgent(t, nil)
	withoutSession.sessionID = ""
	assert.Empty(t, withoutSession.SupportedGoalActions(), "a goal belongs to a session")
}

func TestPerformGoalActionWithoutASession(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.sessionID = ""

	_, err := a.PerformGoalAction(agent.GoalActionSet, "ship it")
	assert.ErrorContains(t, err, "no MiMo session")
	assert.Nil(t, a.goal.setWaiter, "a refused set leaves no waiter behind")
	_, err = a.PerformGoalAction(agent.GoalActionClear, "")
	assert.ErrorContains(t, err, "no MiMo session")
	assert.Empty(t, server.allRequests())
}

// blockGoalCommand makes the command route hold its answer until the test
// ends, as MiMo does until the turn that the goal starts ends. No event
// confirms the goal.
func blockGoalCommand(t *testing.T, server *fakeServer) {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server.handle("POST /session/ses_test/command", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `{}`)
	})
}

// A goal that MiMo never confirms ends the wait at the API timeout, and the
// failed set leaves no waiter that a later event could confirm.
func TestPerformGoalActionSetTimesOutWithoutAConfirmation(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, server := newTestAgent(t, nil)
	clock := useMockClock(t, a)
	blockGoalCommand(t, server)
	confirm := clock.Trap().NewTimer(mimoGoalConfirmTimerTag)
	defer confirm.Close()

	result := make(chan error, 1)
	go func() {
		_, err := a.PerformGoalAction(agent.GoalActionSet, "ship it")
		result <- err
	}()
	assert.Equal(t, a.APITimeout(), testutil.WaitForTimer(t, ctx, confirm), "the wait is the API timeout")
	waitFor(t, func() bool { return len(server.requestsTo("POST /session/ses_test/command")) == 1 }, "the command reached MiMo")
	clock.Advance(a.APITimeout()).MustWait(ctx)
	select {
	case err := <-result:
		assert.ErrorContains(t, err, "did not confirm the goal within 30s")
	case <-ctx.Done():
		t.Fatal("the set did not end at the API timeout")
	}
	assert.Nil(t, a.goal.setWaiter)
}

// A goal starts at the time the agent's clock states when MiMo first reports
// it. A verdict that keeps the goal keeps its start. A new condition, and a
// condition that MiMo sets again after a verdict ended it, start again.
func TestSessionGoalStartsAtTheClocksTime(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	first := time.Date(2026, 9, 25, 18, 30, 0, 0, time.FixedZone("KST", 9*60*60))
	clock.Set(first).MustWait(ctx)
	createdAt := func() time.Time {
		t.Helper()
		goal, ok := sink.LastGoal()
		require.True(t, ok)
		return goal.CreatedAt
	}

	feed(a, goalEvent(t, testSessionID, "ship it", nil))
	assert.Equal(t, first.UTC(), createdAt())
	assert.Equal(t, time.UTC, createdAt().Location(), "the time is stated in UTC")

	clock.Advance(time.Minute).MustWait(ctx)
	feed(a, goalEvent(t, testSessionID, "ship it", map[string]any{"ok": false, "reason": "not yet", "attempt": 1}))
	assert.Equal(t, first.UTC(), createdAt(), "a verdict that keeps the goal keeps its start")

	feed(a, goalEvent(t, testSessionID, "ship it faster", nil))
	assert.Equal(t, first.Add(time.Minute).UTC(), createdAt(), "a new condition is a new goal")

	clock.Advance(time.Minute).MustWait(ctx)
	feed(a, goalEvent(t, testSessionID, "", map[string]any{"ok": true, "reason": "shipped", "attempt": 2}))
	assert.Equal(t, first.Add(time.Minute).UTC(), createdAt(), "the verdict that ends the goal keeps its start")
	feed(a, goalEvent(t, testSessionID, "ship it faster", nil))
	assert.Equal(t, first.Add(2*time.Minute).UTC(), createdAt(), "a goal that a verdict ended starts again")
}

func TestPerformGoalActionSetEndsWhenTheProcessExits(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	blockGoalCommand(t, server)
	a.SimulateExitForTest()

	_, err := a.PerformGoalAction(agent.GoalActionSet, "ship it")
	assert.ErrorContains(t, err, "exited")
	assert.Nil(t, a.goal.setWaiter)
}

func TestPerformGoalActionSet(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.effort = "high"
	// The command route answers after the turn the goal starts. The event that
	// confirms the goal arrives first, while the route still blocks.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server.handle("POST /session/ses_test/command", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		a.HandleOutput(goalEvent(t, testSessionID, "all tests pass", nil))
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, http.StatusOK, `{}`)
	})

	_, err := a.PerformGoalAction(agent.GoalActionSet, "  all tests pass  ")
	require.NoError(t, err)
	requests := server.requestsTo("POST /session/ses_test/command")
	require.Len(t, requests, 1)
	assert.JSONEq(t, `{"command":"goal","arguments":"all tests pass","agent":"build","model":"mock/alpha","variant":"high"}`,
		string(requests[0].Body))
}

// The command can finish before the event that confirms it is read. A command
// that succeeded set the goal.
func TestPerformGoalActionSetWhenTheCommandFinishesFirst(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "ship it")
	require.NoError(t, err)
	assert.Len(t, server.requestsTo("POST /session/ses_test/command"), 1)
}

func TestPerformGoalActionSetRefused(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("POST /session/ses_test/command", http.StatusBadRequest, `{"name":"BadRequest"}`)

	_, err := a.PerformGoalAction(agent.GoalActionSet, "ship it")
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Nil(t, a.goal.setWaiter, "a failed set leaves no waiter behind")
}

func TestPerformGoalActionRefusesAClearWord(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	for _, objective := range []string{"", "  ", "clear", " reset "} {
		_, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		assert.ErrorIs(t, err, agent.ErrGoalObjectiveIsCommand, "objective %q", objective)
	}
	assert.Empty(t, server.allRequests(), "MiMo would read each of these as a clear")

	_, err := a.PerformGoalAction(agent.GoalActionSet, "Clear")
	require.NoError(t, err, "MiMo compares the clear words exactly")
}

func TestPerformGoalActionClear(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	_, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	body := decodeBody(t, server.requestsTo("POST /session/ses_test/command")[0])
	assert.Equal(t, goalCommand, body["command"])
	assert.Equal(t, goalClearArgument, body["arguments"])

	server.respond("POST /session/ses_test/command", http.StatusInternalServerError, `{}`)
	_, err = a.PerformGoalAction(agent.GoalActionClear, "")
	assert.Error(t, err)
}

func TestPerformGoalActionUnsupported(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	for _, action := range []agent.GoalAction{agent.GoalActionPause, agent.GoalActionResume} {
		_, err := a.PerformGoalAction(action, "")
		assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	}
	assert.Empty(t, server.allRequests())
}

func TestPerformGoalActionOnAStoppedAgent(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.SetStoppedForTest(true)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "ship it")
	assert.ErrorContains(t, err, "stopped")
	_, err = a.PerformGoalAction(agent.GoalActionClear, "")
	assert.ErrorContains(t, err, "stopped")
	assert.Empty(t, server.allRequests())
}
