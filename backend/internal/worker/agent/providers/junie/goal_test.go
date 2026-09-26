package junie

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Junie's `_session/goal` extension is a request/response method whose handler
// accepts ONE action: `clear`. Set, Pause and Resume are not on the wire, so
// this writer must report them unsupported rather than fake them through a
// prompt. The goal state arrives as a `session_info_update` whose `_meta.goal`
// holds the snapshot, or null when the goal is gone.

// newJunieGoalAgent returns a bare agent with a test sink and the start time
// the fold compares createdAt against.
func newJunieGoalAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.goalStartedAt = time.Now()
	return a, sink
}

// Junie's goal handler rejects any action but `clear`.
func TestJunieSupportedGoalActionsOfferOnlyClear(t *testing.T) {
	t.Parallel()
	a, _ := newJunieGoalAgent(t)
	assert.Equal(t, []agent.GoalAction{agent.GoalActionClear}, a.SupportedGoalActions(),
		"the jar's goal handler accepts only the clear action")
}

// A clear sends `_session/goal` with the session id and the one action word.
func TestJuniePerformGoalActionClearSendsTheGoalRequest(t *testing.T) {
	t.Parallel()
	a, requests := newJunieAgentForRPC(t)

	outcome, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err, "the clear action runs")
	assert.Empty(t, outcome.QueuedInput, "the side-band request starts no queued input")

	recorded := requests()
	require.Len(t, recorded, 1, "the clear writes one request")
	assert.Equal(t, "_session/goal", recorded[0].Method)
	assert.Equal(t, "clear", recorded[0].Params["action"], "the action word is the jar's GOAL_ACTION_CLEAR")
	assert.Equal(t, "session-1", recorded[0].Params["sessionId"], "the request addresses the running session")
}

// Set, Pause and Resume have no request shape. The writer reports them
// unsupported instead of sending an action word the handler would reject.
func TestJuniePerformGoalActionRejectsTheUnsupportedActions(t *testing.T) {
	t.Parallel()
	a, requests := newJunieAgentForRPC(t)

	for _, action := range []agent.GoalAction{agent.GoalActionSet, agent.GoalActionPause, agent.GoalActionResume} {
		_, err := a.PerformGoalAction(action, "Ship the feature")
		require.ErrorIs(t, err, agent.ErrGoalControlUnsupported, "action %v has no wire shape", action)
	}
	assert.Empty(t, requests(), "an unsupported action writes no request")
}

// Junie's status words map onto the neutral vocabulary. `limited` is the
// budget/turn limit, the same meaning as Codex's usageLimited, which is
// Blocked. An unrecognized word is Blocked too, so a state this build cannot
// read never offers Pause.
func TestJunieGoalStatusMapping(t *testing.T) {
	t.Parallel()
	assert.Equal(t, agent.GoalStatusActive, junieGoalStatus("active"))
	assert.Equal(t, agent.GoalStatusBlocked, junieGoalStatus("limited"))
	assert.Equal(t, agent.GoalStatusDone, junieGoalStatus("complete"))
	assert.Equal(t, agent.GoalStatusBlocked, junieGoalStatus("brand_new"), "an unknown word is Blocked")
}

// A `_meta.goal` snapshot upserts the goal with its fields and its status.
func TestJunieGoalMetaFoldsASnapshot(t *testing.T) {
	t.Parallel()
	a, sink := newJunieGoalAgent(t)
	now := time.Now().UnixMilli()

	handled := a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{
		"goal": json.RawMessage(`{"objective":"Ship the feature","status":"active","createdAt":` +
			itoa64(now) + `,"updatedAt":` + itoa64(now) + `,"timeUsedSeconds":42,"controlMethod":"_session/goal"}`),
	}, nil)
	assert.True(t, handled, "an update that carries a goal is consumed")

	require.Len(t, sink.Goals(), 1, "the fold writes one goal")
	got := sink.Goals()[0]
	assert.Equal(t, "Ship the feature", got.Objective)
	assert.Equal(t, agent.GoalStatusActive, got.Status)
	assert.Equal(t, "active", got.StatusDetail, "the provider word is retained as the detail")
	assert.Equal(t, 42, int(*got.TimeUsedSeconds), "the progress counter rides the report")
	assert.False(t, got.Snapshot, "a goal created after this process began is a transition")
}

// A `_meta.goal: null` clears the goal.
func TestJunieGoalMetaFoldsANullAsAClear(t *testing.T) {
	t.Parallel()
	a, sink := newJunieGoalAgent(t)

	// A goal first, so the clear follows a real removal.
	a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{
		"goal": json.RawMessage(`{"objective":"Ship","status":"active","createdAt":` +
			itoa64(time.Now().UnixMilli()) + `,"updatedAt":1,"timeUsedSeconds":0}`),
	}, nil)
	handled := a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{
		"goal": json.RawMessage(`null`),
	}, nil)
	assert.True(t, handled, "a null goal is consumed")
	assert.Equal(t, 1, sink.GoalClears(), "the null clears the goal")
	assert.Equal(t, []bool{false}, sink.GoalClearSnapshots(),
		"a clear that follows a goal announces the removal")
}

// The first report of a goal whose createdAt predates this process restates a
// stored goal and writes no transcript row.
func TestJunieGoalMetaMarksAStoredGoalAsARestatement(t *testing.T) {
	t.Parallel()
	a, sink := newJunieGoalAgent(t)
	// The goal was created before this process started.
	old := time.Now().Add(-time.Hour).UnixMilli()
	raw := json.RawMessage(`{"objective":"Stored goal","status":"active","createdAt":` +
		itoa64(old) + `,"updatedAt":` + itoa64(old) + `,"timeUsedSeconds":0}`)

	a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{"goal": raw}, nil)
	require.Len(t, sink.Goals(), 1)
	assert.True(t, sink.Goals()[0].Snapshot,
		"the first report of a stored goal restates it")
}

// An identical repeat restates the goal; a changed status is a transition.
func TestJunieGoalMetaSeparatesARestatementFromATransition(t *testing.T) {
	t.Parallel()
	a, sink := newJunieGoalAgent(t)
	now := time.Now().UnixMilli()
	active := `{"objective":"Ship","status":"active","createdAt":` + itoa64(now) + `,"updatedAt":` + itoa64(now) + `,"timeUsedSeconds":0}`
	done := `{"objective":"Ship","status":"complete","createdAt":` + itoa64(now) + `,"updatedAt":` + itoa64(now+1000) + `,"timeUsedSeconds":0}`

	a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{
		"goal": json.RawMessage(active),
	}, nil)
	a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{
		"goal": json.RawMessage(active),
	}, nil)
	require.Len(t, sink.Goals(), 2)
	assert.False(t, sink.Goals()[0].Snapshot, "the first report of a fresh goal is a transition")
	assert.True(t, sink.Goals()[1].Snapshot, "an identical repeat restates the goal")

	a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{
		"goal": json.RawMessage(done),
	}, nil)
	require.Len(t, sink.Goals(), 3)
	assert.False(t, sink.Goals()[2].Snapshot, "a status change is a transition")
	assert.Equal(t, agent.GoalStatusDone, sink.Goals()[2].Status)
}

// A clear with no goal before it restates the absence and writes no
// "Goal cleared" row.
func TestJunieGoalMetaClearWithNoGoalIsARestatement(t *testing.T) {
	t.Parallel()
	a, sink := newJunieGoalAgent(t)

	a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{
		"goal": json.RawMessage(`null`),
	}, nil)
	assert.Equal(t, 1, sink.GoalClears())
	assert.Equal(t, []bool{true}, sink.GoalClearSnapshots(),
		"a clear with no goal before it restates the absence")
}

// An update without a goal key, or of another type, is not consumed: the base
// keeps its own handling.
func TestJunieGoalMetaLeavesOtherUpdatesAlone(t *testing.T) {
	t.Parallel()
	a, sink := newJunieGoalAgent(t)

	assert.False(t, a.handleGoalMeta(contracts.ACPUpdateAgentMessageChunk,
		map[string]json.RawMessage{"goal": json.RawMessage(`null`)}, nil),
		"an update of another type is not consumed")
	assert.False(t, a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate,
		map[string]json.RawMessage{"title": json.RawMessage(`"New title"`)}, nil),
		"a session_info_update with no goal key is not consumed")
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// an unreadable snapshot logs and moves nothing.
func TestJunieGoalMetaIgnoresAnUnreadableSnapshot(t *testing.T) {
	t.Parallel()
	a, sink := newJunieGoalAgent(t)

	handled := a.handleGoalMeta(contracts.ACPUpdateSessionInfoUpdate, map[string]json.RawMessage{
		"goal": json.RawMessage(`{"objective":`),
	}, nil)
	assert.True(t, handled, "the key is present, so the update is consumed")
	assert.Empty(t, sink.Goals(), "an unreadable snapshot folds nothing")
	assert.Zero(t, sink.GoalClears())
}

// itoa64 formats an int64 for a JSON literal.
func itoa64(v int64) string {
	return fmt.Sprintf("%d", v)
}
