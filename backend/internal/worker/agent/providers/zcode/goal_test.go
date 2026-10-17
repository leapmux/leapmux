package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The protocol's four status words, one card state for each word. An unknown or
// an absent word maps to BLOCKED: this build cannot interpret that status, so it
// must not offer Pause for it.
//
// The four words of ZCode's desktop projection (`verifying`, `verified`,
// `notSatisfied`, `failed`) are unknown words here. The app-server never sends
// them: `session.target` and `target_changed` carry the protocol's four only, and
// the desktop application derives its own words from the goal check events.
func TestZCodeGoal_StatusMapping(t *testing.T) {
	t.Parallel()

	for wire, want := range map[string]agent.GoalStatus{
		zcodeGoalStatusActive:        agent.GoalStatusActive,
		zcodeGoalStatusPaused:        agent.GoalStatusPaused,
		zcodeGoalStatusComplete:      agent.GoalStatusDone,
		zcodeGoalStatusBudgetLimited: agent.GoalStatusBlocked,
		"verifying":                  agent.GoalStatusBlocked,
		"verified":                   agent.GoalStatusBlocked,
		"notSatisfied":               agent.GoalStatusBlocked,
		"failed":                     agent.GoalStatusBlocked,
		"somethingNew":               agent.GoalStatusBlocked,
		"":                           agent.GoalStatusBlocked,
	} {
		assert.Equal(t, want, zcodeGoalStatus(wire), "status %q", wire)
	}
}

// A status word that is not a JSON string lies outside the vocabulary. The
// decode refuses the whole state then, so nothing is reported: a number or an
// object must never guess a card state.
func TestZCodeGoal_StatusOutsideTheWordVocabularyReportsNothing(t *testing.T) {
	t.Parallel()

	for name, state := range map[string]string{
		"a numeric status": `{"objective":"Ship it","status":5}`,
		"a status object":  `{"objective":"Ship it","status":{"state":"active"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

			a.reportZCodeGoal(json.RawMessage(state), false)

			assert.Empty(t, sink.Goals())
			assert.Equal(t, 0, sink.GoalClears())
		})
	}
}

// An absent status word is a status this build cannot interpret. Both wire
// shapes of the absence -- a missing key and JSON null -- report BLOCKED with no
// phrase on the card.
func TestZCodeGoal_AnAbsentStatusWordReportsBlocked(t *testing.T) {
	t.Parallel()

	for name, state := range map[string]string{
		"a missing key": `{"objective":"Ship it"}`,
		"a null word":   `{"objective":"Ship it","status":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

			a.reportZCodeGoal(json.RawMessage(state), false)

			goals := sink.Goals()
			require.Len(t, goals, 1)
			assert.Equal(t, agent.GoalStatusBlocked, goals[0].Status)
			assert.Empty(t, goals[0].StatusDetail)
		})
	}
}

// The goal check lifecycle has its own status vocabulary beside the goal's. A
// status outside it -- absent, unknown, or a word of the goal vocabulary --
// reaches neither a row nor the goal card.
func TestZCodeGoal_VerificationStatusOutsideItsVocabularyChangesNothing(t *testing.T) {
	t.Parallel()

	for name, payload := range map[string]string{
		"an absent status":  `{"verificationId":"v1","targetId":"g1","goalIteration":1}`,
		"an unknown status": `{"verificationId":"v1","targetId":"g1","goalIteration":1,"status":"settled"}`,
		"a goal word":       `{"verificationId":"v1","targetId":"g1","goalIteration":1,"status":"complete"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.reportZCodeGoal(json.RawMessage(`{"targetID":"g1","objective":"Ship it","status":"active"}`), false)

			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated, payload))

			assert.Equal(t, 0, sink.NotificationCount())
			goals := sink.Goals()
			require.Len(t, goals, 1, "the goal keeps the one report it had")
			assert.Empty(t, goals[0].StatusDetail, "no in-range status word, no checking phrase")
		})
	}
}

// A goal check can run before this build has seen its goal. Its `started` then
// has no card to mark, and a final keeps its frame without inventing a goal from
// the empty report.
func TestZCodeGoal_VerificationWithoutAKnownGoal(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		payload string
		rows    int
	}{
		"the start writes nothing":     {`{"verificationId":"v1","targetId":"g1","goalIteration":1,"status":"started"}`, 0},
		"a final keeps only its frame": {`{"verificationId":"v1","targetId":"g1","goalIteration":1,"status":"completed"}`, 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated, tc.payload))

			assert.Equal(t, tc.rows, sink.NotificationCount())
			assert.Empty(t, sink.Goals(), "no goal report, no goal card")
		})
	}
}

// zcodeGoalSetLine is a `target_changed` announcement that sets the goal g1.
func zcodeGoalSetLine(t *testing.T, seq int64) []byte {
	t.Helper()
	return zcodeEventLine(t, seq, contracts.ZCodeEventSessionUpdated,
		`{"action":"set","source":"command","target":{"targetID":"g1","sessionID":"sess-1",`+
			`"objective":"Ship the parity matrix","summaryTitle":"Parity","status":"active",`+
			`"tokenBudget":null,"tokensUsed":0,"timeUsedSeconds":0,"time":{"created":1,"updated":2}}}`)
}

// A clear forgets the goal that the goal check reports again. A check that runs
// after the clear has no goal to mark. If the cache kept the cleared goal, the
// check would put that goal back on the card.
func TestZCodeGoal_AClearForgetsTheGoalTheCheckReportsAgain(t *testing.T) {
	t.Parallel()

	for name, clearGoal := range map[string]func(*testing.T, *Agent){
		"a cleared target change": func(t *testing.T, a *Agent) {
			a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
				`{"action":"cleared","source":"command","target":null}`))
		},
		"a null target in a target change": func(t *testing.T, a *Agent) {
			a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
				`{"action":"status_updated","source":"runtime","target":null}`))
		},
		"a snapshot that states no goal": func(_ *testing.T, a *Agent) {
			a.reportZCodeGoal(json.RawMessage(`null`), true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeGoalSetLine(t, 1))
			require.Len(t, sink.Goals(), 1)

			clearGoal(t, a)
			a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated,
				`{"verificationId":"v1","targetId":"g1","goalIteration":1,"status":"started"}`))
			a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventSessionUpdated,
				`{"verificationId":"v1","targetId":"g1","goalIteration":1,"status":"completed",`+
					`"verification":{"passed":true,"reason":"done"}}`))

			assert.Equal(t, 1, sink.GoalClears())
			assert.Len(t, sink.Goals(), 1, "the check must not report the cleared goal again")
			a.Mu.Lock()
			cached := a.lastGoalUpdate
			a.Mu.Unlock()
			assert.Zero(t, cached, "the clear forgets the cached goal")
		})
	}
}

// A goal check states the target that it checks. A check of another target is
// about a goal that this session replaced. Its verdict does not apply to the
// current goal: ZCode itself skips the continuation when the target changed
// during the check. The check therefore marks nothing and writes no row.
func TestZCodeGoal_AVerificationOfAnotherTargetChangesNothing(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"started", "completed", "failed_closed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeGoalSetLine(t, 1))

			a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
				`{"verificationId":"v1","targetId":"g-replaced","goalIteration":1,"status":"`+status+`",`+
					`"verification":{"passed":true,"reason":"done"}}`))

			assert.Equal(t, 0, sink.NotificationCount(), "a verdict about another goal is no row")
			goals := sink.Goals()
			require.Len(t, goals, 1, "the current goal keeps the one report it had")
			assert.Empty(t, goals[0].StatusDetail)
		})
	}
}

// The session target states its consumed token count beside its budget. A
// resumed session's goal card tracks what the run spent, not only its ceiling.
func TestZCodeGoal_SnapshotReportsTokensUsed(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.reportZCodeGoal(json.RawMessage(`{"targetId":"t-1","objective":"ship it","status":"active",`+
		`"tokenBudget":2000,"tokensUsed":1200,"timeUsedSeconds":90}`), true)

	got, ok := sink.LastGoal()
	require.True(t, ok)
	require.NotNil(t, got.TokensUsed)
	assert.EqualValues(t, 1200, *got.TokensUsed)
	require.NotNil(t, got.TokenBudget)
	assert.EqualValues(t, 2000, *got.TokenBudget)
	assert.True(t, got.Snapshot)
}

// `target_changed` spells the native id `targetID`; the snapshot path spells it
// `targetId`. Whichever spelling the payload carries identifies the goal.
func TestZCodeGoal_NativeIdAcceptsBothSpellings(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.reportZCodeGoal(json.RawMessage(`{"targetID":"upper","objective":"one","status":"active"}`), false)
	a.reportZCodeGoal(json.RawMessage(`{"targetId":"lower","objective":"two","status":"active"}`), false)

	updates := sink.Goals()
	require.Len(t, updates, 2)
	assert.Equal(t, "upper", updates[0].NativeID)
	assert.Equal(t, "lower", updates[1].NativeID)
}

// The shipped app-server emits NO state.updated patch for a goal change.
//
// A live run showed it. LeapMux sent session/goal, the app-server accepted it and
// ZCode started a turn whose input source was `goal-continuation`, so the agent
// held the goal. The worker's debug log for that whole run contains no
// `state.updated` at all.
//
// The reply's goal is applied as a RESTATEMENT. The `target_changed` event
// announces the transition, and the reply only brings the card up to date.
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
	require.Len(t, goals, 1, "the reply states the goal even when no event came before it")
	assert.Equal(t, "Ship the parity matrix", goals[0].Objective)
	assert.Equal(t, "target-1", goals[0].NativeID)
	assert.Equal(t, agent.GoalStatusActive, goals[0].Status)
	assert.True(t, goals[0].Snapshot,
		"the reply RESTATES the goal; only the target_changed event announces the transition")
}

// The app-server appends the `target_changed` event before it writes the
// session/goal reply, so the event line reaches LeapMux first. The goal rows
// depend on that order. The event reports the goal LIVE, and the neutral sink
// writes the `goal_updated` row for that transition. The reply then restates the
// same goal, which writes no row. In the opposite order, the restatement would
// bring the card up to date first, and the live report would find no transition
// to write.
func TestZCodeGoalActionTheEventAnnouncesAndTheReplyRestates(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	go func() {
		req := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
		a.HandleOutput(zcodeGoalSetLine(t, 1))
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(`{
			"response": "",
			"startedTurn": true,
			"snapshot": {
				"runtime": {"stateRevision": 7},
				"session": {"sessionId": "sess-1", "target": {"targetId": "g1", "objective": "Ship the parity matrix", "status": "active"}}
			}
		}`)))
	}()
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Ship the parity matrix")
	require.NoError(t, err)

	goals := sink.Goals()
	require.Len(t, goals, 2)
	assert.False(t, goals[0].Snapshot, "the event comes first and announces the goal")
	assert.True(t, goals[1].Snapshot, "the reply comes second and restates the same goal")
	assert.Equal(t, goals[0].NativeID, goals[1].NativeID)
	assert.Equal(t, goals[0].Objective, goals[1].Objective)
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

// A reply states the goal in `session.target`. ZCode 3.14.5 has no top-level
// `goal` key in its snapshot, so a stray one is ignored, and the card follows
// the target alone.
func TestZCodeGoalActionReadsTheSessionTarget(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{
		"runtime": {"stateRevision": 5},
		"goal": {"targetId": "stray", "objective": "Stray key", "status": "paused"},
		"session": {"sessionId": "sess-1", "target": {"targetID": "from-target", "objective": "Target wins", "status": "complete"}}
	}`)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Target wins")
	require.NoError(t, err)

	goals := sink.Goals()
	require.Len(t, goals, 1)
	assert.Equal(t, "from-target", goals[0].NativeID)
	assert.Equal(t, "Target wins", goals[0].Objective)
	assert.Equal(t, agent.GoalStatusDone, goals[0].Status)
	assert.True(t, goals[0].Snapshot, "a reply restates the goal")
}

// A reply with only a top-level `goal` key states no goal, so it changes nothing.
func TestZCodeGoalActionIgnoresATopLevelGoalKey(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	answerZCodeRequest(t, a, stdin, zcodeMethodSessionGoal, `{
		"runtime": {"stateRevision": 5},
		"goal": {"targetId": "target-2", "objective": "Stray key", "status": "paused"}
	}`)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Stray key")
	require.NoError(t, err)

	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
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
// status loses it, so a reader never sees "active (active)" or "done (complete)".
// `budget_limited` reads as a phrase, so the card shows no raw wire token. An
// unknown word survives, because it is the only statement of why the card shows
// BLOCKED.
func TestZCodeGoalStatusDetailKeepsOnlyWhatTheStatusLoses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		wire   string
		status agent.GoalStatus
		detail string
	}{
		{zcodeGoalStatusActive, agent.GoalStatusActive, ""},
		{zcodeGoalStatusPaused, agent.GoalStatusPaused, ""},
		{zcodeGoalStatusComplete, agent.GoalStatusDone, ""},
		{zcodeGoalStatusBudgetLimited, agent.GoalStatusBlocked, "budget limited"},
		{"blocked", agent.GoalStatusBlocked, ""},
		{"somethingNew", agent.GoalStatusBlocked, "somethingNew"},
		{"", agent.GoalStatusBlocked, ""},
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
