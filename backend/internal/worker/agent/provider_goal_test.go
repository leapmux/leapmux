package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Claude Code ---

func TestClaudeGoal_ActiveGoalFrameReportsTheCondition(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{"type":"active_goal","value":{` +
		`"condition":"every test passes","iterations":3,"set_at":1700000000000,` +
		`"tokens_at_start":51234,"last_reason":"two suites still fail"},` +
		`"uuid":"u-1","session_id":"s-1"}`))

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "every test passes", got.Objective)
	assert.Equal(t, GoalStatusActive, got.Status)
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

	sink := &testSink{}
	agent := newTestAgent(sink)

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

	sink := &testSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{"type":"active_goal","value":null,"uuid":"u-1","session_id":"s-1"}`))

	assert.Equal(t, 1, sink.GoalClears())
	assert.Empty(t, sink.Goals())
}

// Claude Code has no pause and no resume; the feature does not exist in the CLI.
func TestClaudeGoal_SupportsOnlySetAndClear(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(&testSink{})
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["clear","goal","compact"]}`))

	assert.ElementsMatch(t, []GoalAction{GoalActionSet, GoalActionClear}, agent.SupportedGoalActions())
	assert.ErrorIs(t, agent.PauseGoal(), ErrGoalControlUnsupported)
	assert.ErrorIs(t, agent.ResumeGoal(), ErrGoalControlUnsupported)
}

// /goal shipped in Claude Code 2.1.139. Against an older build the only effect
// of the control would be sending the literal text "/goal ..." to the model as
// a prompt, so the capability is read from the process's own command list
// rather than assumed from the provider.
func TestClaudeGoal_ReportsNoActionsWhenTheCLILacksTheCommand(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(&testSink{})
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["clear","compact"]}`))

	assert.Empty(t, agent.SupportedGoalActions())
}

// Before the init frame arrives nothing is known, and the safe answer is to
// offer nothing rather than a control that may do nothing.
func TestClaudeGoal_ReportsNoActionsBeforeTheInitFrame(t *testing.T) {
	t.Parallel()

	assert.Empty(t, newTestAgent(&testSink{}).SupportedGoalActions())
}

// A frame with no list at all is a shape this build does not recognize. It must
// leave the answer alone rather than clear it, so a future change degrades to
// "unknown" instead of silently disabling a working feature.
func TestClaudeGoal_AnInitFrameWithNoListLeavesTheAnswerAlone(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(&testSink{})
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init"}`))

	assert.NotEmpty(t, agent.SupportedGoalActions())
}

// --- ZCode ---

func TestZCodeGoal_StatePatchReportsAChange(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)

	agent.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1",` +
		`"revision":12,"patch":{"goal":{"targetId":"t-1","objective":"green build",` +
		`"status":"verifying","timeUsedSeconds":90,"iteration":4}}}`))

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "green build", got.Objective)
	// `verifying` is still being pursued, so it maps to ACTIVE; the word itself
	// survives in the detail, which is where a reader learns a check is running.
	assert.Equal(t, GoalStatusActive, got.Status)
	assert.Equal(t, "verifying", got.StatusDetail)
	require.NotNil(t, got.Iterations)
	assert.EqualValues(t, 4, *got.Iterations)
	assert.False(t, got.Snapshot, "a patch reports a change as it happens")
	assert.Nil(t, got.TokensUsed, "ZCode reports a budget, never a consumed count")
}

// A patch that changed something else omits `goal` entirely. Treating an absent
// key as "no goal" would clear the goal on every settings change.
func TestZCodeGoal_PatchWithoutAGoalKeyLeavesItAlone(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)

	agent.handleZCodeStateUpdated(json.RawMessage(
		`{"scope":"session","sessionId":"sess-1","revision":13,"patch":{"status":"prompt_started"}}`))

	assert.Empty(t, sink.Goals())
	assert.Equal(t, 0, sink.GoalClears())
}

func TestZCodeGoal_NullGoalClears(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)

	agent.handleZCodeStateUpdated(json.RawMessage(
		`{"scope":"session","sessionId":"sess-1","revision":14,"patch":{"goal":null}}`))

	assert.Equal(t, 1, sink.GoalClears())
}

func TestZCodeGoal_StatusMapping(t *testing.T) {
	t.Parallel()

	for wire, want := range map[string]GoalStatus{
		"active":       GoalStatusActive,
		"verifying":    GoalStatusActive,
		"paused":       GoalStatusPaused,
		"verified":     GoalStatusDone,
		"notSatisfied": GoalStatusBlocked,
		"failed":       GoalStatusBlocked,
		"somethingNew": GoalStatusBlocked,
	} {
		assert.Equal(t, want, zcodeGoalStatus(wire), "status %q", wire)
	}
}

// The revision only moves forward. A stale patch arriving out of order must not
// pull it back, or the next session/goal would send an expectedRevision the
// app-server already passed and the write would be refused as a conflict that
// does not exist.
func TestZCodeGoal_StateRevisionIsMonotonic(t *testing.T) {
	t.Parallel()

	agent := newZCodeTestAgent(t, &testSink{})

	agent.applyZCodeRuntimeState(&zcodeRuntimeState{StateRevision: 20})
	agent.applyZCodeRuntimeState(&zcodeRuntimeState{StateRevision: 7})

	agent.mu.Lock()
	got := agent.stateRevision
	agent.mu.Unlock()
	assert.EqualValues(t, 20, got)
}

func TestZCodeGoal_SupportsEveryAction(t *testing.T) {
	t.Parallel()

	agent := newZCodeTestAgent(t, &testSink{})
	assert.ElementsMatch(t,
		[]GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume},
		agent.SupportedGoalActions())
}

// --- Reasonix ---

func TestReasonixGoal_StatusUpdateReportsTheGoal(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &ReasonixAgent{}
	agent.agentID = "test-agent"
	agent.sink = sink

	handled := agent.handleExtraMethod(&parsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"","status":{"goal":{"status":"running",` +
			`"objective":"land the refactor","runtime":{"turnsUsed":6,"tokensUsed":9000,` +
			`"lastReason":"tests still red"}}}}`),
	})

	require.True(t, handled, "the reasonix namespace must be claimed, not left unknown")
	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "land the refactor", got.Objective)
	assert.Equal(t, GoalStatusActive, got.Status)
	assert.Equal(t, "tests still red", got.StatusDetail)
	require.NotNil(t, got.Iterations)
	assert.EqualValues(t, 6, *got.Iterations)
	require.NotNil(t, got.TokensUsed)
	assert.EqualValues(t, 9000, *got.TokensUsed)
}

// A stop cause says more than the status word: goal_stuck and budget_spend are
// both reported as `stopped`.
func TestReasonixGoal_StopCauseWinsOverTheStatusWord(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &ReasonixAgent{}
	agent.agentID = "test-agent"
	agent.sink = sink

	agent.handleExtraMethod(&parsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"status":{"goal":{"status":"stopped","objective":"x",` +
			`"runtime":{"stopCause":"budget_spend","lastReason":"out of budget"}}}}`),
	})

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, GoalStatusBlocked, got.Status)
	assert.Equal(t, "budget_spend", got.StatusDetail)
}

func TestReasonixGoal_NoneClears(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &ReasonixAgent{}
	agent.agentID = "test-agent"
	agent.sink = sink

	agent.handleExtraMethod(&parsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"status":{"goal":{"status":"none"}}}`),
	})

	assert.Equal(t, 1, sink.GoalClears())
}

// ClearContext mints a NEW ACP sessionId. A status notification still in flight
// for the OLD session must not be applied, or a goal the user just cleared
// comes back.
func TestReasonixGoal_IgnoresAnotherSession(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &ReasonixAgent{}
	agent.agentID = "test-agent"
	agent.sink = sink
	agent.sessionID = "session-new"

	agent.handleExtraMethod(&parsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"session-old","status":{"goal":` +
			`{"status":"running","objective":"stale objective"}}}`),
	})

	assert.Empty(t, sink.Goals(), "a notification for a replaced session is dropped")
}

// Reasonix is READ-ONLY: setting means switching to its goal mode and hijacking
// the next prompt, and clearing means switching back to a mode LeapMux never
// tracked. It must therefore implement no GoalController at all, so the browser
// disables every action.
func TestReasonixGoal_ImplementsNoGoalController(t *testing.T) {
	t.Parallel()

	var a any = &ReasonixAgent{}
	_, ok := a.(GoalController)
	assert.False(t, ok, "Reasonix cannot honestly perform a goal action")
}
