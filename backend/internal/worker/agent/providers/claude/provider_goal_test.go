package claude

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeGoal_ActiveGoalFrameReportsTheCondition(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"active_goal","value":{` +
		`"condition":"every test passes","iterations":3,"set_at":1700000000000,` +
		`"tokens_at_start":51234,"last_reason":"two suites still fail"},` +
		`"uuid":"u-1","session_id":"s-1"}`))

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "every test passes", got.Objective)
	assert.Equal(t, agent.GoalStatusActive, got.Status)
	assert.Equal(t, "two suites still fail", got.StatusDetail)
	require.NotNil(t, got.Iterations)
	assert.EqualValues(t, 3, *got.Iterations)
	// set_at is Unix MILLISECONDS (Date.now()), unlike Codex's seconds.
	assert.Equal(t, int64(1700000000), got.CreatedAt.Unix())
}

// tokens_at_start is the token balance when the goal was SET -- a starting
// balance, not consumption. Reporting it as usage would print a number meaning
// the opposite of its label, and it would grow with the context rather than
// with the work done toward the goal.
func TestClaudeGoal_NeverReportsTokensAtStartAsUsage(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	agent.HandleOutput([]byte(`{"type":"active_goal","value":{` +
		`"condition":"ship it","iterations":0,"set_at":1700000000000,` +
		`"tokens_at_start":51234},"uuid":"u-1","session_id":"s-1"}`))

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Nil(t, got.TokensUsed, "a starting balance is not usage")
	assert.Nil(t, got.TimeUsedSeconds, "Claude reports no elapsed time here")
}

// A null value is how Claude says the goal is gone -- met, impossible, or
// cleared by the user.
func TestClaudeGoal_NullValueClears(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	agent.HandleOutput([]byte(`{"type":"active_goal","value":null,"uuid":"u-1","session_id":"s-1"}`))

	assert.Equal(t, 1, sink.GoalClears())
	assert.Empty(t, sink.Goals())
}

// Claude Code has no pause and no resume; the feature does not exist in the CLI.
func TestClaudeGoal_SupportsOnlySetAndClear(t *testing.T) {
	t.Parallel()

	ag := newTestAgent(agent.NewProviderServices(&agenttest.Sink{}))
	ag.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["clear","goal","compact"]}`))

	assert.ElementsMatch(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, ag.SupportedGoalActions())
	// One write interface for every provider; the ROUTE is what differs. Claude
	// also implements GoalTextCommander, which is how the Manager knows to
	// report a delivery back to it -- a side-band provider has no use for that.
	var provider any = ag
	_, writer := provider.(agent.GoalWriter)
	_, commander := provider.(agent.GoalTextCommander)
	assert.True(t, writer, "Claude can be told to change its goal")
	assert.True(t, commander, "Claude owns its text command syntax")

	// A text route returns the command instead of performing it, so the queue
	// is what makes it take effect. A side-band provider returns nothing here.
	outcome, err := ag.PerformGoalAction(agent.GoalActionSet, "ship it")
	require.NoError(t, err)
	assert.Equal(t, "/goal ship it", outcome.QueuedInput)
}

// /goal shipped in Claude Code 2.1.139. Against an older build the only effect
// of the control would be sending the literal text "/goal ..." to the model as
// a prompt, so the capability is read from the process's own command list
// rather than assumed from the provider.
func TestClaudeGoal_ReportsNoActionsWhenTheCLILacksTheCommand(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(agent.NewProviderServices(&agenttest.Sink{}))
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["clear","compact"]}`))

	assert.Empty(t, agent.SupportedGoalActions())
}

// Before the init frame arrives nothing is known, and the safe answer is to
// offer nothing rather than a control that may do nothing.
func TestClaudeGoal_ReportsNoActionsBeforeTheInitFrame(t *testing.T) {
	t.Parallel()

	assert.Empty(t, newTestAgent(agent.NewProviderServices(&agenttest.Sink{})).SupportedGoalActions())
}

// A frame with no list at all is a shape this build does not recognize. It must
// leave the answer alone rather than clear it, so a future change degrades to
// "unknown" instead of silently disabling a working feature.
func TestClaudeGoal_AnInitFrameWithNoListLeavesTheAnswerAlone(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(agent.NewProviderServices(&agenttest.Sink{}))
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init"}`))

	assert.NotEmpty(t, agent.SupportedGoalActions())
}

// Claude Code does not report a command-driven goal change. The observer writes
// the row only after Manager.SendInput confirms delivery.
func TestClaudeGoal_ObserveSetWritesTheGoalItself(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newTestAgent(agent.NewProviderServices(sink))
	ag.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	ag.ObserveGoalCommand(agent.GoalDeliverySend, "/goal make the tests pass")

	got, ok := sink.LastGoal()
	require.True(t, ok, "the observer is the only writer here")
	assert.Equal(t, "make the tests pass", got.Objective)
	assert.Equal(t, agent.GoalStatusActive, got.Status)
	assert.False(t, got.CreatedAt.IsZero(), "the observer mints the identity the CLI never sends")
	assert.False(t, got.Snapshot, "the user just did this, so it is a real transition")
}

func TestClaudeGoal_CommandTextFoldsTheObjective(t *testing.T) {
	t.Parallel()

	a := newTestAgent(agent.NewProviderServices(&agenttest.Sink{}))
	outcome, err := a.PerformGoalAction(agent.GoalActionSet, "  every  test\n\tpasses  ")

	require.NoError(t, err)
	assert.Equal(t, "/goal every test passes", outcome.QueuedInput)
}

// A fresh identity on every set, so re-setting the SAME objective reads as a
// restart rather than as no change.
func TestClaudeGoal_ARepeatedObservationMintsAFreshIdentity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newTestAgent(agent.NewProviderServices(sink))
	ag.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	ag.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")
	first, ok := sink.LastGoal()
	require.True(t, ok)

	ag.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")
	second, ok := sink.LastGoal()
	require.True(t, ok)

	assert.False(t, second.CreatedAt.Before(first.CreatedAt),
		"the same objective set again is a restart, not a repeat")
	assert.Len(t, sink.Goals(), 2)
}

func TestClaudeGoal_ObservesEveryClearArgument(t *testing.T) {
	t.Parallel()

	for _, clearArg := range claudeGoalRoute.ClearArgs {
		sink := &agenttest.Sink{}
		a := newTestAgent(agent.NewProviderServices(sink))
		a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
		a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal "+clearArg)
		assert.Equal(t, 1, sink.GoalClears(), clearArg)
	}
}

// A command does not update local state before the queue delivers it.
func TestClaudeGoal_CommandTextWritesNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	_, setErr := a.PerformGoalAction(agent.GoalActionSet, "ship it")
	_, clearErr := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, setErr)
	require.NoError(t, clearErr)
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// An empty objective never reaches the CLI, so it never reaches the row either.
func TestClaudeGoal_CommandTextRefusesAnEmptyObjective(t *testing.T) {
	t.Parallel()

	a := newTestAgent(agent.NewProviderServices(&agenttest.Sink{}))

	_, err := a.PerformGoalAction(agent.GoalActionSet, "   ")
	assert.Error(t, err)
}

// The capability is UNKNOWN before the init frame, and unknown is not absent.
// The queue can cold-start a process and deliver a command before its first
// stdout frame; a refusal there would drop the write for good, because no text
// route reports a command-driven goal change back.
func TestClaudeGoal_ObservesBeforeTheInitFrameArrives(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship the release")

	goal, ok := sink.LastGoal()
	require.True(t, ok, "an unknown capability must not drop the write")
	assert.Equal(t, "ship the release", goal.Objective)
}

// Claude steers by writing the same user message, so a steered command reaches
// the same parser and changes the goal. The card must say so.
func TestClaudeGoal_ObservesASteeredCommand(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	a.ObserveGoalCommand(agent.GoalDeliverySteer, "/goal ship the release")

	goal, ok := sink.LastGoal()
	require.True(t, ok, "Claude's steer channel carries the command")
	assert.Equal(t, "ship the release", goal.Objective)
}

// The refusal must reach the RPC caller, not stay inside the route.
func TestClaudeGoal_CommandTextRefusesAClearWordObjective(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))

	_, err := a.PerformGoalAction(agent.GoalActionSet, "reset")

	assert.ErrorIs(t, err, agent.ErrGoalObjectiveIsCommand)
	assert.Empty(t, sink.Goals())
}

// Claude answers from its init frame, so the ABSENT case is a frame that lists
// other commands. An observed goal command changes nothing while the CLI has not
// advertised the command.
func TestClaudeTextGoalObservationRequiresTheAdvertisedCapability(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["clear","compact"]}`))
	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

func TestClaudeTextGoal_RefusesAnObjectiveThatClears(t *testing.T) {
	t.Parallel()
	providerkittest.AssertRefusesAnObjectiveThatClears(t, claudeGoalRoute)
}
