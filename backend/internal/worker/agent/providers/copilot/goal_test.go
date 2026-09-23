//go:build unix

package copilot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startNativeCopilotForGoal opens an agent against the fake runtime, which models the
// autopilot command verified in CP-002, CP-003 and CP-008.
func startNativeCopilotForGoal(t *testing.T, env ...string) (*Agent, *agenttest.Sink) {
	t.Helper()
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", Env: env,
	})
	sink := &agenttest.Sink{}
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-goal", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: 2 * time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	return provider.(*Agent), sink
}

func TestNativeCopilotGoalExposesEveryVerifiedAction(t *testing.T) {
	a, _ := startNativeCopilotForGoal(t)
	assert.Equal(t,
		[]agent.GoalAction{agent.GoalActionSet, agent.GoalActionPause, agent.GoalActionResume, agent.GoalActionClear},
		a.SupportedGoalActions())
}

// Set records the objective and hands back the runtime's OWN continuation prompt.
// LeapMux never writes that prompt itself.
func TestNativeCopilotGoalSetReturnsTheRuntimeEffect(t *testing.T) {
	a, sink := startNativeCopilotForGoal(t)

	outcome, err := a.PerformGoalAction(agent.GoalActionSet, "Ship the release.\n두 번째 줄")
	require.NoError(t, err)
	assert.Equal(t, "Pursue: Ship the release.\n두 번째 줄", outcome.QueuedInput)

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "Ship the release.\n두 번째 줄", goal.Objective, "the objective keeps its own line breaks")
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Equal(t, "1", goal.NativeID)
	assert.False(t, goal.Snapshot, "a goal the user just set is announced, not restated")
}

// An objective that reads like an option stays an objective: `--` stops the
// runtime's own argument parsing (CP-003).
func TestNativeCopilotGoalSetPreservesALiteralObjective(t *testing.T) {
	for _, objective := range []string{"off", "on", "clear", "--max-ai-credits 1"} {
		t.Run(objective, func(t *testing.T) {
			a, sink := startNativeCopilotForGoal(t)
			_, err := a.PerformGoalAction(agent.GoalActionSet, objective)
			require.NoError(t, err)
			goal, ok := sink.LastGoal()
			require.True(t, ok)
			assert.Equal(t, objective, goal.Objective)
			assert.Equal(t, agent.GoalStatusActive, goal.Status)
		})
	}
}

func TestNativeCopilotGoalSetRefusesAnEmptyObjective(t *testing.T) {
	a, sink := startNativeCopilotForGoal(t)
	for _, objective := range []string{"", "   ", "\n\t"} {
		outcome, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		require.Error(t, err, objective)
		assert.Empty(t, outcome.QueuedInput)
	}
	assert.Empty(t, sink.Goals())
}

// Pause leaves the objective stored, and it queues nothing: the command takes effect
// by itself, so there is no prompt for the input queue to deliver.
func TestNativeCopilotGoalPauseKeepsTheObjective(t *testing.T) {
	a, sink := startNativeCopilotForGoal(t)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Keep this objective")
	require.NoError(t, err)

	outcome, err := a.PerformGoalAction(agent.GoalActionPause, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput, "the pause command needs no queued input")

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "Keep this objective", goal.Objective)
	assert.Zero(t, sink.GoalClears())
}

// Resume needs the explicit credit-limit command, and it returns the runtime's own
// prompt effect. A plain "continue" is never a resume.
func TestNativeCopilotGoalResumeUsesTheCreditLimitCommand(t *testing.T) {
	a, sink := startNativeCopilotForGoal(t)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Keep this objective")
	require.NoError(t, err)
	_, err = a.PerformGoalAction(agent.GoalActionPause, "")
	require.NoError(t, err)

	outcome, err := a.PerformGoalAction(agent.GoalActionResume, "")
	require.NoError(t, err)
	assert.Equal(t, "Continue the objective.", outcome.QueuedInput)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Equal(t, "Keep this objective", goal.Objective)
}

// Resume applies to a PAUSED objective alone. Resuming an active one would spend a
// turn on work that already runs.
func TestNativeCopilotGoalResumeRefusesAnObjectiveThatIsNotPaused(t *testing.T) {
	a, _ := startNativeCopilotForGoal(t)

	outcome, err := a.PerformGoalAction(agent.GoalActionResume, "")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported, "there is no objective to resume")
	assert.Empty(t, outcome.QueuedInput)

	_, err = a.PerformGoalAction(agent.GoalActionSet, "Keep this objective")
	require.NoError(t, err)
	outcome, err = a.PerformGoalAction(agent.GoalActionResume, "")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported, "an active objective is already running")
	assert.Empty(t, outcome.QueuedInput)
}

// Clear runs the ordered session disposal of CP-008 and confirms the removal.
func TestNativeCopilotGoalClearDisposesAndReopensTheSession(t *testing.T) {
	a, sink := startNativeCopilotForGoal(t)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Remove this objective")
	require.NoError(t, err)
	sessionID := a.currentNativeSessionID()

	outcome, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	assert.Positive(t, sink.GoalClears())
	assert.Equal(t, sessionID, a.currentNativeSessionID(), "the transcript keeps its session identity")

	// The reopened session still accepts input and still reports its settings.
	require.NoError(t, a.SendInput("Carry on.", nil))
	assert.NotEmpty(t, a.SettingsSnapshot().SurfacedOptions)

	raw, err := a.SendRequest("probe.interests", json.RawMessage(`{}`), time.Second)
	require.NoError(t, err)
	var probe struct {
		Closed bool `json:"closed"`
	}
	require.NoError(t, json.Unmarshal(raw, &probe))
	assert.True(t, probe.Closed, "the clear sequence closes the session before it opens it again")
}

// A Clear that leaves the objective behind is a failure, not a success. The stored
// objective would otherwise come back on the next read.
func TestNativeCopilotGoalClearReportsASurvivingObjective(t *testing.T) {
	a, _ := startNativeCopilotForGoal(t, "LEAPMUX_TEST_COPILOT_GOAL_CLEAR_SURVIVES=1")
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Remove this objective")
	require.NoError(t, err)

	_, err = a.PerformGoalAction(agent.GoalActionClear, "")
	require.ErrorContains(t, err, "survived the clear sequence")
}

// Clearing an absent objective changes nothing and disposes of no session.
func TestNativeCopilotGoalClearWithNoObjectiveIsANoOperation(t *testing.T) {
	a, sink := startNativeCopilotForGoal(t)

	outcome, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	assert.Zero(t, sink.GoalClears())

	raw, err := a.SendRequest("probe.interests", json.RawMessage(`{}`), time.Second)
	require.NoError(t, err)
	var probe struct {
		Closed bool `json:"closed"`
	}
	require.NoError(t, json.Unmarshal(raw, &probe))
	assert.False(t, probe.Closed)
}

// Every event-log subscription is released with its own handle. The runtime keeps a
// subscription until then, so a replacement that forgot one would leave it registered
// for the life of the process. See CP-009.
func TestNativeCopilotReleasesItsEventSubscriptions(t *testing.T) {
	a, _ := startNativeCopilotForGoal(t)
	interests := func(t *testing.T) int {
		t.Helper()
		raw, err := a.SendRequest("probe.interests", json.RawMessage(`{}`), time.Second)
		require.NoError(t, err)
		var probe struct {
			Released int `json:"released"`
		}
		require.NoError(t, json.Unmarshal(raw, &probe))
		return probe.Released
	}
	require.Zero(t, interests(t))

	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, len(copilotEventInterests), interests(t),
		"a session replacement releases the subscriptions of the session it replaced")
}

func TestNativeCopilotGoalStatusMapping(t *testing.T) {
	t.Parallel()
	for wire, want := range map[string]agent.GoalStatus{
		"active":      agent.GoalStatusActive,
		"paused":      agent.GoalStatusPaused,
		"completed":   agent.GoalStatusDone,
		"cap_reached": agent.GoalStatusBlocked,
		"":            agent.GoalStatusBlocked,
		"future_word": agent.GoalStatusBlocked,
	} {
		assert.Equal(t, want, copilotGoalStatus(wire), wire)
	}
}

// The runtime refuses to resume a session its store never recorded, which is every
// session that ran no model turn. Clearing a goal on one must still reopen it: the
// store held nothing to restore, so opening it under the same identity loses nothing.
// See CP-012.
func TestNativeCopilotGoalClearReopensASessionTheStoreNeverRecorded(t *testing.T) {
	a, sink := startNativeCopilotForGoal(t, "LEAPMUX_TEST_COPILOT_UNRESUMABLE=1")
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Remove this objective")
	require.NoError(t, err)
	sessionID := a.currentNativeSessionID()

	outcome, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	assert.Positive(t, sink.GoalClears())
	assert.Equal(t, sessionID, a.currentNativeSessionID(), "the transcript keeps its session identity")

	require.NoError(t, a.SendInput("Carry on.", nil))
}

// A goal clear disposes of the session, so a turn that ran when it started can never
// end: no idle event reaches a session the runtime no longer has. Without a reset the
// agent stays busy for good, and every later input returns ErrAgentBusy.
func TestNativeCopilotGoalClearEndsTheRunningTurn(t *testing.T) {
	a, sink := startNativeCopilotForGoal(t)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Remove this objective")
	require.NoError(t, err)
	a.HandleOutput(nativeCopilotSessionEvent(t, a.currentNativeSessionID(), "",
		contracts.CopilotEventAssistantTurnStart, map[string]any{"turnId": "0"}))
	require.True(t, a.PublishTurnActive().Active)
	spansBefore := sink.ResetSpanCount()

	_, err = a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)

	assert.False(t, a.PublishTurnActive().Active, "the disposed session's turn ends with it")
	assert.Greater(t, sink.ResetSpanCount(), spansBefore,
		"the spans of the session that went away are released")
	require.NoError(t, a.SendInput("Carry on.", nil), "a reset turn accepts the next input")
}

// The clear closes the session before it opens it again, so a failure to open leaves the
// agent with no session and nothing to roll back to. The process stops there: an agent
// that stayed alive would accept input that can reach nowhere.
func TestNativeCopilotGoalClearStopsWhenTheSessionCannotOpenAgain(t *testing.T) {
	// The runtime refuses the resume, and the create that follows it fails too.
	a, _ := startNativeCopilotForGoal(t,
		"LEAPMUX_TEST_COPILOT_UNRESUMABLE=1", "LEAPMUX_TEST_COPILOT_REPLACEMENT_FAILURE=create")
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Remove this objective")
	require.NoError(t, err)

	_, err = a.PerformGoalAction(agent.GoalActionClear, "")
	require.ErrorContains(t, err, "create the Copilot session again")
	assert.True(t, a.IsStopped(), "an agent with no session does not keep its process")
	assert.False(t, a.PublishTurnActive().Active)
	require.Error(t, a.SendInput("Carry on.", nil), "a stopped agent accepts no input")
}
