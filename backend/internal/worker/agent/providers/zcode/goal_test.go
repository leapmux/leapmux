package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shipped app-server emits NO state.updated patch for a goal change.
//
// A live run settled it. LeapMux sent session/goal, the app-server accepted it and
// ZCode started a turn whose input source was `goal-continuation`, so the agent
// plainly held the goal. The worker's debug log for that whole run contains no
// `state.updated` at all, and the goal card stayed empty.
//
// `sendZCodeGoal` dropped the reply's goal on purpose, because "the app-server also
// emits a state.updated patch for the same change". Where that patch never comes,
// the reply is the only statement of the goal and dropping it loses the goal.
//
// It is applied as a RESTATEMENT rather than an announcement, so a build that does
// send the patch still announces the transition exactly once -- from the patch.
func TestZCodeGoalActionAppliesTheReplyGoal(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	// The shape the shipped app-server actually replies with: a `snapshot`
	// envelope whose goal is `session.target`, beside a `response` string.
	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{
		"response": "Goal active\nObjective: Ship the parity matrix",
		"startedTurn": true,
		"snapshot": {
			"runtime": {"stateRevision": 7},
			"session": {
				"sessionId": "sess-1",
				"target": {
					"targetId": "target-1",
					"objective": "Ship the parity matrix",
					"status": "active",
					"timeUsedSeconds": 0
				}
			}
		}
	}`)

	outcome, err := a.PerformGoalAction(agent.GoalActionSet, "Ship the parity matrix")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput, "session/goal is side-band: nothing is left for the queue")

	goals := sink.Goals()
	require.Len(t, goals, 1, "the reply carries the only statement of the goal this build makes")
	assert.Equal(t, "Ship the parity matrix", goals[0].Objective)
	assert.Equal(t, "target-1", goals[0].NativeID)
	assert.Equal(t, agent.GoalStatusActive, goals[0].Status)
	assert.True(t, goals[0].Snapshot,
		"the reply RESTATES the goal; only a state.updated patch announces the transition")
}

// A reply that carries no goal key must write nothing. The revision still lands,
// because skipping it breaks the next action in a row.
func TestZCodeGoalActionWithoutAGoalInTheReply(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{"snapshot": {"runtime": {"stateRevision": 11}, "session": {"sessionId": "sess-1"}}}`)
	_, err := a.PerformGoalAction(agent.GoalActionPause, "")
	require.NoError(t, err)

	assert.Empty(t, sink.Goals(), "an absent goal key states nothing and must write nothing")
	a.Mu.Lock()
	revision := a.stateRevision
	a.Mu.Unlock()
	assert.Equal(t, int64(11), revision, "the reply's revision still lands, or the next action conflicts")
}

// Clear reports the removal, and as a restatement for the same reason a set does.
func TestZCodeGoalClearAppliesTheReplyNull(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{"snapshot": {"runtime": {"stateRevision": 3}, "session": {"sessionId": "sess-1", "target": null}}}`)
	_, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)

	assert.Equal(t, []bool{true}, sink.GoalClearSnapshots(),
		"the reply RESTATES the absence; only a patch announces the removal")
}

// The request itself must keep its shape: `replace` rather than the positional
// bare-objective form, so an objective beginning with `pause` cannot be read as an
// action.
func TestZCodeGoalSetSendsReplace(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{"snapshot": {"runtime": {"stateRevision": 1}, "session": {"sessionId": "sess-1"}}}`)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "pause the release")
	require.NoError(t, err)

	var found bool
	for _, frame := range stdin.Frames() {
		var sent struct {
			Method string `json:"method"`
			Params struct {
				Action    string `json:"action"`
				Objective string `json:"objective"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(frame), &sent) != nil || sent.Method != zcodeMethodSessionGoal {
			continue
		}
		found = true
		assert.Equal(t, zcodeGoalActionReplace, sent.Params.Action)
		assert.Equal(t, "pause the release", sent.Params.Objective)
	}
	require.True(t, found, "the goal request must reach the wire")
}

// A build that sends the DOCUMENTED `goal` key keeps working, and a bare document
// with no `snapshot` envelope is read as itself.
func TestZCodeGoalActionReadsTheDocumentedGoalKey(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{
		"runtime": {"stateRevision": 5},
		"goal": {"targetId": "target-2", "objective": "Documented shape", "status": "paused"}
	}`)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Documented shape")
	require.NoError(t, err)

	goals := sink.Goals()
	require.Len(t, goals, 1)
	assert.Equal(t, "Documented shape", goals[0].Objective)
	assert.Equal(t, agent.GoalStatusPaused, goals[0].Status)
}

// The envelope also carries the revision the NEXT action needs. Reading the
// envelope as a document left it at zero, so Pause followed by Resume sent a stale
// expectedRevision and the app-server refused the second.
func TestZCodeGoalReplyEnvelopeCarriesTheRevision(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{"snapshot": {"runtime": {"stateRevision": 42}, "session": {"sessionId": "sess-1"}}}`)
	_, err := a.PerformGoalAction(agent.GoalActionPause, "")
	require.NoError(t, err)

	a.Mu.Lock()
	revision := a.stateRevision
	a.Mu.Unlock()
	assert.Equal(t, int64(42), revision, "the revision lives inside the envelope, not beside it")
}

// The detail restates nothing. ZCode's own word survives only where the neutral
// status loses it, so a reader never sees "active (active)".
func TestZCodeGoalStatusDetailKeepsOnlyWhatTheStatusLoses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		wire   string
		status agent.GoalStatus
		detail string
	}{
		{zcodeGoalStatusActive, agent.GoalStatusActive, ""},
		{zcodeGoalStatusPaused, agent.GoalStatusPaused, ""},
		{zcodeGoalStatusVerifying, agent.GoalStatusActive, zcodeGoalStatusVerifying},
		{zcodeGoalStatusVerified, agent.GoalStatusDone, zcodeGoalStatusVerified},
		{zcodeGoalStatusNotSatisfied, agent.GoalStatusBlocked, zcodeGoalStatusNotSatisfied},
		{zcodeGoalStatusFailed, agent.GoalStatusBlocked, zcodeGoalStatusFailed},
	} {
		assert.Equal(t, tc.detail, zcodeGoalStatusDetail(tc.wire, tc.status), tc.wire)
	}
}

// An active goal reaches the sink with no detail beside it.
func TestZCodeGoalActiveCarriesNoRedundantDetail(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{"snapshot": {"session": {"sessionId": "sess-1", "target": {"targetId": "t", "objective": "Ship it", "status": "active"}}}}`)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Ship it")
	require.NoError(t, err)

	goals := sink.Goals()
	require.Len(t, goals, 1)
	assert.Equal(t, agent.GoalStatusActive, goals[0].Status)
	assert.Empty(t, goals[0].StatusDetail, "the card would otherwise read \"active (active)\"")
}
