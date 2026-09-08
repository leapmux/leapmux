package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	// One write interface for every provider; the ROUTE is what differs. Claude
	// also implements GoalTextCommander, which is how the Manager knows to
	// report a delivery back to it -- a side-band provider has no use for that.
	var provider any = agent
	_, writer := provider.(GoalWriter)
	_, commander := provider.(GoalTextCommander)
	assert.True(t, writer, "Claude can be told to change its goal")
	assert.True(t, commander, "Claude owns its text command syntax")

	// A text route returns the command instead of performing it, so the queue
	// is what makes it take effect. A side-band provider returns nothing here.
	outcome, err := agent.PerformGoalAction(GoalActionSet, "ship it")
	require.NoError(t, err)
	assert.Equal(t, "/goal ship it", outcome.QueuedInput)
}

// Codex and ZCode complete the action themselves, so the caller has nothing to
// enqueue. An outcome that carried text here would send the objective to the
// model as a prompt on top of the side-band write that already happened.
func TestSideBandGoal_ReturnsNoQueuedInput(t *testing.T) {
	t.Parallel()

	zagent := newZCodeTestAgent(t, &testSink{})
	outcome, _ := zagent.PerformGoalAction(GoalActionSet, "ship it")
	assert.Empty(t, outcome.QueuedInput, "ZCode performs the action itself")

	var codex any = &CodexAgent{}
	_, writer := codex.(GoalWriter)
	_, commander := codex.(GoalTextCommander)
	assert.True(t, writer, "Codex can be told to change its goal")
	assert.False(t, commander, "Codex has no user-message command to observe")
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

func TestTextGoalObservationRequiresTheAdvertisedCapability(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		observe func(*testSink)
	}{
		{
			// Claude answers from its init frame, so the ABSENT case is a frame
			// that lists other commands. See the unknown case below, which is a
			// different answer.
			name: "claude",
			observe: func(sink *testSink) {
				agent := newTestAgent(sink)
				agent.HandleOutput([]byte(
					`{"type":"system","subtype":"init","slash_commands":["clear","compact"]}`))
				agent.ObserveGoalCommand(GoalDeliverySend, "/goal ship it")
			},
		},
		{
			name: "goose",
			observe: func(sink *testSink) {
				agent := &GooseCLIAgent{}
				agent.sink = sink
				agent.ObserveGoalCommand(GoalDeliverySend, "/goal ship it")
			},
		},
		{
			name: "copilot",
			observe: func(sink *testSink) {
				agent := newCopilotCLIAgent("", false)
				agent.sink = sink
				agent.ObserveGoalCommand(GoalDeliverySend, "/goal ship it")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sink := &testSink{}
			test.observe(sink)
			assert.Empty(t, sink.Goals())
			assert.Zero(t, sink.GoalClears())
		})
	}
}

func advertiseACPCommands(t *testing.T, base *acpBase, commands ...string) {
	t.Helper()
	values := make([]map[string]string, len(commands))
	for i, command := range commands {
		values[i] = map[string]string{"name": command}
	}
	update, err := json.Marshal(map[string]any{
		"sessionUpdate":     acpUpdateAvailableCommandsUpdate,
		"availableCommands": values,
	})
	require.NoError(t, err)
	params, err := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	require.NoError(t, err)
	base.handleACPSessionUpdate(params, nil)
}

func TestGooseGoal_UsesTheAdvertisedGoalCommand(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	agent := &GooseCLIAgent{}
	agent.sink = sink

	assert.Empty(t, agent.SupportedGoalActions())
	advertiseACPCommands(t, &agent.acpBase, "compact", "goal")
	assert.Equal(t, []GoalAction{GoalActionSet, GoalActionClear}, agent.SupportedGoalActions())
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())

	setOutcome, err := agent.PerformGoalAction(GoalActionSet, "  ship\n it ")
	require.NoError(t, err)
	assert.Equal(t, "/goal ship it", setOutcome.QueuedInput)
	agent.ObserveGoalCommand(GoalDeliverySend, setOutcome.QueuedInput)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "ship it", goal.Objective)

	for _, clearArg := range gooseGoalRoute.clearArgs {
		agent.ObserveGoalCommand(GoalDeliverySend, "/goal "+clearArg)
	}
	assert.Equal(t, len(gooseGoalRoute.clearArgs), sink.GoalClears())
}

func TestCopilotGoal_UsesTheAdvertisedAutopilotCommand(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	agent := newCopilotCLIAgent("", false)
	agent.sink = sink

	advertiseACPCommands(t, &agent.acpBase, "autopilot")
	assert.Equal(t, []GoalAction{GoalActionSet, GoalActionClear}, agent.SupportedGoalActions())
	setOutcome, err := agent.PerformGoalAction(GoalActionSet, "ship it")
	require.NoError(t, err)
	assert.Equal(t, "/goal ship it", setOutcome.QueuedInput)
	clearOutcome, err := agent.PerformGoalAction(GoalActionClear, "")
	require.NoError(t, err)
	assert.Equal(t, "/goal off", clearOutcome.QueuedInput)

	agent.ObserveGoalCommand(GoalDeliverySend, setOutcome.QueuedInput)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "ship it", goal.Objective)
	agent.ObserveGoalCommand(GoalDeliverySend, clearOutcome.QueuedInput)
	assert.Equal(t, 1, sink.GoalClears())
}

func TestACPGoal_CommandRemovalRepublishesCapabilities(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	agent := &GooseCLIAgent{}
	agent.sink = sink

	advertiseACPCommands(t, &agent.acpBase, "goal")
	advertiseACPCommands(t, &agent.acpBase, "compact")
	assert.Empty(t, agent.SupportedGoalActions())
	assert.Equal(t, 2, sink.GoalCapabilityPublishes())
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
// tracked. It must therefore implement no GoalWriter at all, so the browser
// disables every action.
func TestReasonixGoal_ImplementsNoGoalWriter(t *testing.T) {
	t.Parallel()

	var a any = &ReasonixAgent{}
	_, ok := a.(GoalWriter)
	assert.False(t, ok, "Reasonix cannot honestly perform a goal action")
	_, ok = a.(GoalCapable)
	assert.True(t, ok, "Reasonix reports that its action list is empty")
}

// Reasonix's real wire vocabulary. `cancelled` and `failed` come from a
// per-turn override rather than the goal enum, so they are easy to miss; both
// mean "not progressing, needs the user".
func TestReasonixGoal_StatusMapping(t *testing.T) {
	t.Parallel()

	for wire, want := range map[string]GoalStatus{
		"running":  GoalStatusActive,
		"complete": GoalStatusDone,
		"blocked":  GoalStatusBlocked,
		// Set when the user cancels a turn, and when a turn returns an error.
		"cancelled": GoalStatusBlocked,
		"failed":    GoalStatusBlocked,
		// A word this build does not know must not offer Pause either.
		"somethingNew": GoalStatusBlocked,
	} {
		assert.Equal(t, want, reasonixGoalStatus(wire), "status %q", wire)
	}
}

// The notification is also observed with the status fields HOISTED to the top
// level. The struct declares both shapes because declaring one silently
// reported no goal, and every other Reasonix test here sends the nested shape --
// so without this the hoisted field could be deleted with a green suite.
func TestReasonixGoal_ReadsTheHoistedShape(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &ReasonixAgent{}
	agent.agentID = "test-agent"
	agent.sink = sink

	handled := agent.handleExtraMethod(&parsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"","goal":{"status":"running",` +
			`"objective":"hoisted objective"}}`),
	})

	require.True(t, handled)
	got, ok := sink.LastGoal()
	require.True(t, ok, "the hoisted shape must report a goal, not silence")
	assert.Equal(t, "hoisted objective", got.Objective)
	assert.Equal(t, GoalStatusActive, got.Status)
}

// When BOTH shapes arrive, the nested one wins. Nothing else pins that
// precedence, so inverting it would keep the suite green.
func TestReasonixGoal_NestedStatusWinsOverTheHoistedCopy(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &ReasonixAgent{}
	agent.agentID = "test-agent"
	agent.sink = sink

	agent.handleExtraMethod(&parsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"",` +
			`"goal":{"status":"running","objective":"hoisted"},` +
			`"status":{"goal":{"status":"running","objective":"nested"}}}`),
	})

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "nested", got.Objective)
}

// workDurationMs is MILLISECONDS and TimeUsedSeconds is seconds. A direct
// assignment would print a duration a thousand times too large.
func TestReasonixGoal_ConvertsWorkDurationToSeconds(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &ReasonixAgent{}
	agent.agentID = "test-agent"
	agent.sink = sink

	agent.handleExtraMethod(&parsedLine{
		Method: reasonixMethodStatusUpdate,
		Params: json.RawMessage(`{"sessionId":"","status":{"goal":{"status":"running",` +
			`"objective":"land it","runtime":{"workDurationMs":90000}}}}`),
	})

	got, ok := sink.LastGoal()
	require.True(t, ok)
	require.NotNil(t, got.TimeUsedSeconds)
	assert.EqualValues(t, 90, *got.TimeUsedSeconds)
}

// A status update that reaches the reader goroutine while a session/new round
// trip holds sessionMu must not block. It once did: isCurrentACPSession took
// sessionMu.RLock on the goroutine that has to deliver that round trip's own
// response, so the clear and the whole output stream stalled until the API
// timeout.
func TestReasonixGoal_StatusUpdateDoesNotBlockOnTheSessionLock(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &ReasonixAgent{}
	agent.agentID = "test-agent"
	agent.sink = sink

	agent.sessionMu.Lock()
	defer agent.sessionMu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.handleExtraMethod(&parsedLine{
			Method: reasonixMethodStatusUpdate,
			Params: json.RawMessage(`{"sessionId":"","status":{"goal":{"status":"running","objective":"x"}}}`),
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the status update blocked on sessionMu, which the reader goroutine must never wait for")
	}
}

// A session snapshot RESTATES the goal, so it must be reported as one. Only the
// Codex path asserted the snapshot rule before, and ZCode reaches it through a
// different function.
func TestZCodeGoal_SessionSnapshotIsARestatement(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)

	agent.reportZCodeGoal(json.RawMessage(`{"objective":"resumed objective","status":"active"}`), true)

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "resumed objective", got.Objective)
	assert.True(t, got.Snapshot, "a resume restates the goal; it does not announce one")
}

// The same call with snapshot=false is what a state PATCH uses, and that one
// does announce. The pair is what proves the flag is threaded rather than
// hardcoded.
func TestZCodeGoal_StatePatchIsNotARestatement(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)

	agent.reportZCodeGoal(json.RawMessage(`{"objective":"live change","status":"active"}`), false)

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.False(t, got.Snapshot)
}

// A snapshot that says the session has NO goal clears it as a restatement too.
// Without the flag a resume writes "Goal cleared: X" for a clear nobody made.
func TestZCodeGoal_SnapshotNullClearIsARestatement(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)

	agent.reportZCodeGoal(json.RawMessage(`null`), true)

	assert.Equal(t, []bool{true}, sink.GoalClearSnapshots())
}

// The session snapshot is the only place a RESUMED session learns its
// revision before a turn ends. Reading eventSeq alone left it at 0, and
// session/goal then sent expectedRevision 0 against a live session whose
// revision was higher -- the app-server refused every goal action.
func TestZCodeGoal_SessionSnapshotSeedsTheStateRevision(t *testing.T) {
	t.Parallel()

	agent := newZCodeTestAgent(t, &testSink{})
	snap, ok := agent.parseStateSnapshot(json.RawMessage(
		`{"session":{"sessionId":"sess-1"},"runtime":{"eventSeq":7,"stateRevision":42}}`))
	require.True(t, ok)

	agent.applyParsedStateSnapshot(snap)

	agent.mu.Lock()
	got := agent.stateRevision
	agent.mu.Unlock()
	assert.EqualValues(t, 42, got, "a resumed session must know its revision before its first turn ends")
}

// A workspace-scope patch carries no SESSION state. A `goal` key on one must
// not reach the session goal: a null there would clear a live goal and write a
// "Goal cleared" row for a change the user never made.
func TestZCodeGoal_WorkspaceScopePatchNeverTouchesTheGoal(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)
	agent.reportZCodeGoal(json.RawMessage(`{"objective":"live goal","status":"active"}`), false)
	require.Equal(t, 1, len(sink.Goals()))

	agent.handleZCodeStateUpdated(json.RawMessage(
		`{"scope":"workspace","patch":{"goal":null}}`))

	assert.Equal(t, 0, sink.GoalClears(), "a workspace patch must not clear the session goal")
}

// A settings reply folds its state document through applyStateSnapshot, and
// three setters do that. Reporting the goal from inside the fold let a model,
// effort or permission-mode change write the goal columns -- and a reply that
// spelled `goal: null` would DELETE a live goal with no transcript row.
func TestZCodeGoal_ASettingsSnapshotNeverTouchesTheGoal(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)

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

	agent := newZCodeTestAgent(t, &testSink{})

	agent.noteZCodeStateRevision(12)
	agent.noteZCodeStateRevision(7)
	agent.noteZCodeStateRevision(0)

	agent.mu.Lock()
	got := agent.stateRevision
	agent.mu.Unlock()
	assert.EqualValues(t, 12, got, "a stale or absent revision never moves it backwards")
}

// A goal replacement starts a turn, which can advance the revision after the
// goal reply. Retry one revision conflict with the server's current revision.
func TestZCodeGoal_RetriesARevisionConflictOnce(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)
	agent.mu.Lock()
	agent.stateRevision = 3
	agent.mu.Unlock()

	result := make(chan error, 1)
	go func() { _, err := agent.PerformGoalAction(GoalActionSet, "ship it"); result <- err }()
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
	agent.HandleOutput(conflict)

	require.Eventually(t, func() bool { return len(stdin.Requests(t)) == 2 },
		time.Second, 5*time.Millisecond, "the revision conflict must cause one retry")
	second := stdin.Requests(t)[1]
	var params struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	require.NoError(t, json.Unmarshal(second.Params, &params))
	assert.EqualValues(t, 4, params.ExpectedRevision)
	agent.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second),
		json.RawMessage(`{"runtime":{"stateRevision":5}}`)))
	require.NoError(t, <-result)
}

// A patch for a session ClearContext already replaced must not apply. It would
// resurrect the goal the user just cleared and write a "Goal set" row for it.
func TestZCodeGoal_IgnoresAPatchForAReplacedSession(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newZCodeTestAgent(t, sink)
	agent.mu.Lock()
	agent.sessionID = "sess-new"
	agent.mu.Unlock()

	agent.handleZCodeStateUpdated(json.RawMessage(
		`{"scope":"session","sessionId":"sess-old","patch":{"goal":{"objective":"stale goal","status":"active"}}}`))

	assert.Empty(t, sink.Goals(), "a patch for the replaced session is not this session's goal")
}

// Claude Code does not report a command-driven goal change. The observer writes
// the row only after Manager.SendInput confirms delivery.
func TestClaudeGoal_ObserveSetWritesTheGoalItself(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	agent.ObserveGoalCommand(GoalDeliverySend, "/goal make the tests pass")

	got, ok := sink.LastGoal()
	require.True(t, ok, "the observer is the only writer here")
	assert.Equal(t, "make the tests pass", got.Objective)
	assert.Equal(t, GoalStatusActive, got.Status)
	assert.False(t, got.CreatedAt.IsZero(), "the observer mints the identity the CLI never sends")
	assert.False(t, got.Snapshot, "the user just did this, so it is a real transition")
}

func TestClaudeGoal_CommandTextFoldsTheObjective(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(&testSink{})
	outcome, err := agent.PerformGoalAction(GoalActionSet, "  every  test\n\tpasses  ")

	require.NoError(t, err)
	assert.Equal(t, "/goal every test passes", outcome.QueuedInput)
}

// A fresh identity on every set, so re-setting the SAME objective reads as a
// restart rather than as no change.
func TestClaudeGoal_ARepeatedObservationMintsAFreshIdentity(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	agent.ObserveGoalCommand(GoalDeliverySend, "/goal ship it")
	first, ok := sink.LastGoal()
	require.True(t, ok)

	agent.ObserveGoalCommand(GoalDeliverySend, "/goal ship it")
	second, ok := sink.LastGoal()
	require.True(t, ok)

	assert.False(t, second.CreatedAt.Before(first.CreatedAt),
		"the same objective set again is a restart, not a repeat")
	assert.Len(t, sink.Goals(), 2)
}

func TestClaudeGoal_ObservesEveryClearArgument(t *testing.T) {
	t.Parallel()

	for _, clearArg := range claudeGoalRoute.clearArgs {
		sink := &testSink{}
		agent := newTestAgent(sink)
		agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
		agent.ObserveGoalCommand(GoalDeliverySend, "/goal "+clearArg)
		assert.Equal(t, 1, sink.GoalClears(), clearArg)
	}
}

// A command does not update local state before the queue delivers it.
func TestClaudeGoal_CommandTextWritesNothing(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newTestAgent(sink)

	_, setErr := agent.PerformGoalAction(GoalActionSet, "ship it")
	_, clearErr := agent.PerformGoalAction(GoalActionClear, "")
	require.NoError(t, setErr)
	require.NoError(t, clearErr)
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// An empty objective never reaches the CLI, so it never reaches the row either.
func TestClaudeGoal_CommandTextRefusesAnEmptyObjective(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(&testSink{})

	_, err := agent.PerformGoalAction(GoalActionSet, "   ")
	assert.Error(t, err)
}

// ZCode reports its own change. SetGoal must not write local state first.
func TestGoal_ZCodeWritesNothingLocally(t *testing.T) {
	t.Parallel()

	zsink := &testSink{}
	zagent := newZCodeTestAgent(t, zsink)
	_, _ = zagent.PerformGoalAction(GoalActionSet, "ship it")
	assert.Empty(t, zsink.Goals(), "ZCode answers a session/goal with a state patch")
	assert.Zero(t, zsink.GoalClears())
}

// The capability is UNKNOWN before the init frame, and unknown is not absent.
// The queue can cold-start a process and deliver a command before its first
// stdout frame; a refusal there would drop the write for good, because no text
// route reports a command-driven goal change back.
func TestClaudeGoal_ObservesBeforeTheInitFrameArrives(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newTestAgent(sink)
	agent.ObserveGoalCommand(GoalDeliverySend, "/goal ship the release")

	goal, ok := sink.LastGoal()
	require.True(t, ok, "an unknown capability must not drop the write")
	assert.Equal(t, "ship the release", goal.Objective)
}

// Claude steers by writing the same user message, so a steered command reaches
// the same parser and changes the goal. The card must say so.
func TestClaudeGoal_ObservesASteeredCommand(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	agent.ObserveGoalCommand(GoalDeliverySteer, "/goal ship the release")

	goal, ok := sink.LastGoal()
	require.True(t, ok, "Claude's steer channel carries the command")
	assert.Equal(t, "ship the release", goal.Objective)
}

// Goose steers through a separate ACP method whose command handling LeapMux did
// not verify, so a steered command must claim nothing.
func TestGooseGoal_IgnoresASteeredCommand(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := &GooseCLIAgent{}
	agent.sink = sink
	advertiseACPCommands(t, &agent.acpBase, "goal")

	agent.ObserveGoalCommand(GoalDeliverySteer, "/goal ship it")

	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// A one-word objective that equals a clear word would reach the provider as a
// clear, so every text route refuses it before it is queued. Without the
// refusal the RPC reports success and the card then shows no goal at all.
func TestTextGoal_RefusesAnObjectiveThatClears(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		route goalTextRoute
	}{
		{name: "claude", route: claudeGoalRoute},
		{name: "goose", route: gooseGoalRoute},
		{name: "copilot", route: copilotGoalRoute},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, clearArg := range test.route.clearArgs {
				// Upper case too: parseGoalCommandText folds case, so the
				// provider reads `OFF` as a clear exactly as it reads `off`.
				for _, objective := range []string{clearArg, strings.ToUpper(clearArg), " " + clearArg + " "} {
					_, err := test.route.commandText(GoalActionSet, objective)
					assert.ErrorIs(t, err, ErrGoalObjectiveIsCommand, objective)
				}
			}
			// A clear word that only STARTS the objective is a real objective.
			command, err := test.route.commandText(GoalActionSet, test.route.clearArgs[0]+" the queue")
			require.NoError(t, err)
			assert.Equal(t, test.route.command+" "+test.route.clearArgs[0]+" the queue", command)
		})
	}
}

// The refusal must reach the RPC caller, not stay inside the route.
func TestClaudeGoal_CommandTextRefusesAClearWordObjective(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"]}`))

	_, err := agent.PerformGoalAction(GoalActionSet, "reset")

	assert.ErrorIs(t, err, ErrGoalObjectiveIsCommand)
	assert.Empty(t, sink.Goals())
}

// A conflict reply is the app-server's own answer about its own session, so it
// is the one source entitled to move the cached revision DOWN. Routing it
// through the monotonic note would resend the number that just failed.
func TestZCodeGoal_RetriesWithARevisionBelowTheCachedOne(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)
	agent.mu.Lock()
	agent.stateRevision = 9
	agent.mu.Unlock()

	result := make(chan error, 1)
	go func() { _, err := agent.PerformGoalAction(GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	agent.HandleOutput(zcodeConflictLine(t, zcodeSentRequestID(t, first), 4))

	require.Eventually(t, func() bool { return len(stdin.Requests(t)) == 2 },
		time.Second, 5*time.Millisecond, "a lower server revision must still retry")
	second := stdin.Requests(t)[1]
	assert.EqualValues(t, 4, zcodeGoalExpectedRevision(t, second),
		"the retry sends the revision the app-server reported, not the higher cached one")
	agent.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second),
		json.RawMessage(`{"runtime":{"stateRevision":5}}`)))
	require.NoError(t, <-result)
}

// The retry is one-shot. A second conflict returns the wire error rather than
// sending a third request, or a wedged app-server would take the goal write
// around for ever.
func TestZCodeGoal_StopsAfterOneRetry(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)
	agent.mu.Lock()
	agent.stateRevision = 3
	agent.mu.Unlock()

	result := make(chan error, 1)
	go func() { _, err := agent.PerformGoalAction(GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	agent.HandleOutput(zcodeConflictLine(t, zcodeSentRequestID(t, first), 4))
	require.Eventually(t, func() bool { return len(stdin.Requests(t)) == 2 },
		time.Second, 5*time.Millisecond, "the first conflict retries")
	second := stdin.Requests(t)[1]
	agent.HandleOutput(zcodeConflictLine(t, zcodeSentRequestID(t, second), 7))

	err := <-result
	require.Error(t, err, "a second conflict is reported, never retried again")
	assert.Len(t, stdin.Requests(t), 2, "no third request")
}

// Two requests that carry one inputId and different parameters are the worst
// input for any server-side duplicate check, so each attempt mints its own.
func TestZCodeGoal_MintsAFreshInputIDForTheRetry(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)
	agent.mu.Lock()
	agent.stateRevision = 3
	agent.mu.Unlock()

	result := make(chan error, 1)
	go func() { _, err := agent.PerformGoalAction(GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	agent.HandleOutput(zcodeConflictLine(t, zcodeSentRequestID(t, first), 4))
	require.Eventually(t, func() bool { return len(stdin.Requests(t)) == 2 },
		time.Second, 5*time.Millisecond, "the revision conflict must cause one retry")
	second := stdin.Requests(t)[1]

	assert.NotEqual(t, zcodeGoalInputID(t, first), zcodeGoalInputID(t, second))
	agent.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second),
		json.RawMessage(`{"runtime":{"stateRevision":5}}`)))
	require.NoError(t, <-result)
}

// A non-conflict error is reported at once. A retry there would send the same
// refused write a second time for no reason.
func TestZCodeGoal_DoesNotRetryANonConflictError(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)

	result := make(chan error, 1)
	go func() { _, err := agent.PerformGoalAction(GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	refusal, err := json.Marshal(map[string]any{
		"id":    zcodeSentRequestID(t, first),
		"error": map[string]any{"code": ZCodeErrSessionNotActive, "message": "no session"},
	})
	require.NoError(t, err)
	agent.HandleOutput(refusal)

	require.Error(t, <-result)
	assert.Len(t, stdin.Requests(t), 1, "only a revision mismatch retries")
}

// A conflict reply that states no actual revision cannot steer a retry, so the
// error is reported rather than retried against the same stale number.
func TestZCodeGoal_DoesNotRetryAConflictWithNoRevision(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)

	result := make(chan error, 1)
	go func() { _, err := agent.PerformGoalAction(GoalActionSet, "ship it"); result <- err }()
	first := waitZCodeRequest(t, stdin, zcodeMethodSessionGoal)
	refusal, err := json.Marshal(map[string]any{
		"id": zcodeSentRequestID(t, first),
		"error": map[string]any{
			"code": ZCodeErrRevisionMismatch, "message": "Session state revision mismatch",
		},
	})
	require.NoError(t, err)
	agent.HandleOutput(refusal)

	require.Error(t, <-result)
	assert.Len(t, stdin.Requests(t), 1)
}

// zcodeConflictLine builds a session/goal revision-mismatch reply.
func zcodeConflictLine(t *testing.T, id, actualRevision int64) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    ZCodeErrRevisionMismatch,
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
