package kiro

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestKiroGoalSetBuildsTheGoalCommand(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)

	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions(), "only a run can pause, resume or clear")
	outcome, err := a.PerformGoalAction(agent.GoalActionSet, "Ship the\nrelease")
	require.NoError(t, err)
	assert.Equal(t, "/goal Ship the release", outcome.QueuedInput)
}

func TestKiroGoalRefusesATrailingRoundLimit(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)
	// Kiro's command has no verb: a round limit is the only text that it reads
	// as something other than the objective.

	for _, objective := range []string{"ship it --max 5", "ship it  --max\t12", "ship it\n--max 200"} {
		_, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		assert.ErrorIs(t, err, agent.ErrGoalObjectiveIsCommand, objective)
	}
	_, err := a.PerformGoalAction(agent.GoalActionSet, "ship it --max 5")
	assert.ErrorContains(t, err, `Kiro /goal reads the trailing "--max 5" as a round limit`)
	// Kiro splits only a trailing `--max <digits>` that follows other words.
	for _, objective := range []string{"document --max handling", "--max 5", "set --max 5 then more", "cap --max x5", "clear", "status", "pause"} {
		outcome, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		require.NoError(t, err, objective)
		assert.Equal(t, "/goal "+objective, outcome.QueuedInput)
	}
}

func TestKiroGoalRunReportsTheGoal(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(runStart(t, kiroGoalWorkflowName))
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, kiroRunID, goal.NativeID)
	assert.Equal(t, "make hello.txt say bye", goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.False(t, goal.CreatedAt.IsZero())
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "the run's controls become available")
	assert.Len(t, a.SupportedGoalActions(), 4)

	a.HandleOutput(workflowNote(t, kiroWorkflowLoopIterationMethod, map[string]any{"loopId": "goal-loop", "iteration": 0}))
	goal, _ = sink.LastGoal()
	require.NotNil(t, goal.Iterations)
	assert.Equal(t, int32(1), *goal.Iterations)

	a.HandleOutput(workflowNote(t, kiroWorkflowPausedMethod, map[string]any{"pauseReason": "Repeat 'goal-loop' reached maxIterations."}))
	goal, _ = sink.LastGoal()
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "Repeat 'goal-loop' reached maxIterations.", goal.StatusDetail)

	// A resume reports the start again, under the same run.
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))
	goal, _ = sink.LastGoal()
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Empty(t, goal.StatusDetail)
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "the same run changes no capability")

	a.HandleOutput(runComplete(t, kiroRunCompleted, map[string]any{}))
	goal, _ = sink.LastGoal()
	assert.Equal(t, agent.GoalStatusDone, goal.Status)
	assert.Equal(t, 2, sink.GoalCapabilityPublishes())
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions())
}

func TestKiroFailedGoalRunLeavesTheGoalBlocked(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	// A step that sends an error message fails its round, the loop and the run.
	a.HandleOutput(runComplete(t, kiroRunFailed, failedFinalState("Step signaled error via send_message.")))

	goal, _ := sink.LastGoal()
	assert.Equal(t, agent.GoalStatusBlocked, goal.Status)
	assert.Equal(t, "Step signaled error via send_message.", goal.StatusDetail, "the reason that the failed step states")
	assert.Zero(t, sink.GoalClears())
}

// stepNotify is the message that a step of the probe's goal run sends to the
// parent session, as the probe recorded it.
func stepNotify(t *testing.T, workflowID, severity, message string) []byte {
	t.Helper()
	return notification(t, kiroSessionNotifyMethod, map[string]any{
		"sessionId": kiroTestSession, "callerSessionId": kiroStepSession, "message": message,
		"severity": severity, "workflowId": workflowID, "nodeId": "work", "agentName": "wf-coder",
	})
}

// A step that sends an error fails its run, and Kiro states only its own
// phrase for the failure. The card states the step's message, which says why.
func TestKiroBlockedGoalStatesTheErrorOfItsStep(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))
	a.HandleOutput(stepNotify(t, kiroRunID, "warning", "The tests are slow."))
	a.HandleOutput(stepNotify(t, kiroRunID, "error", "The repository is read-only."))
	a.HandleOutput(stepNotify(t, "wf_other", "error", "Another run broke."))

	a.HandleOutput(runComplete(t, kiroRunFailed, failedFinalState("Step signaled error via send_message.")))

	goal, _ := sink.LastGoal()
	assert.Equal(t, agent.GoalStatusBlocked, goal.Status)
	assert.Equal(t, "The repository is read-only.", goal.StatusDetail, "the last error of the goal run's own steps")
}

// The error of an earlier round that the goal survived states nothing about
// a later failure, which a new round starts over.
func TestKiroBlockedGoalForgetsTheErrorOfAnEarlierRound(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))
	a.HandleOutput(stepNotify(t, kiroRunID, "error", "The first round failed to build."))
	// A resume starts the run again.
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	a.HandleOutput(runComplete(t, kiroRunFailed, failedFinalState("The model call failed.")))

	goal, _ := sink.LastGoal()
	assert.Equal(t, "The model call failed.", goal.StatusDetail)
}

func TestKiroAbortedGoalRunClearsTheGoal(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	a.HandleOutput(runComplete(t, kiroRunAborted, map[string]any{}))

	assert.Equal(t, 1, sink.GoalClears())
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions())
}

func TestKiroOtherWorkflowIsNoGoal(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(runStart(t, "review"))
	a.HandleOutput(workflowNote(t, kiroWorkflowLoopIterationMethod, map[string]any{"iteration": 0}))
	a.HandleOutput(runComplete(t, kiroRunCompleted, map[string]any{}))

	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalCapabilityPublishes())
}

// goalResponder answers the workflow requests of a goal with the given
// replies, by method.
func goalResponder(replies map[string]string) func(agenttest.RecordedRequest) agenttest.RPCReply {
	return func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if reply, ok := replies[req.Method]; ok {
			return agenttest.RPCReply{Result: json.RawMessage(reply)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

func TestKiroGoalClearCancelsTheRun(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, goalResponder(map[string]string{kiroWorkflowCancelMethod: `{"cancelled":true}`}))
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	outcome, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput, "the request acts at once, with no prompt")

	cancels := requestsFor(requests(), kiroWorkflowCancelMethod)
	require.Len(t, cancels, 1)
	assert.Equal(t, map[string]any{"workflowId": kiroRunID, "initiator": "user"}, cancels[0].Params)
	assert.Equal(t, 1, sink.GoalClears())

	// Kiro then reports the end of the run, which clears nothing twice.
	a.HandleOutput(runComplete(t, kiroRunAborted, map[string]any{}))
	assert.Equal(t, 1, sink.GoalClears())
}

func TestKiroGoalPauseAndResumeActOnTheRun(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{Options: map[string]string{contracts.KiroOptionPolicyPreset: "dev-shell"}}, goalResponder(map[string]string{
		kiroWorkflowPauseMethod:  `{"paused":true}`,
		kiroWorkflowResumeMethod: `{"resumed":true}`,
	}))
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	_, err := a.PerformGoalAction(agent.GoalActionPause, "")
	require.NoError(t, err)
	_, err = a.PerformGoalAction(agent.GoalActionResume, "")
	require.NoError(t, err)

	pauses := requestsFor(requests(), kiroWorkflowPauseMethod)
	require.Len(t, pauses, 1)
	assert.Equal(t, map[string]any{"workflowId": kiroRunID, "initiator": "user"}, pauses[0].Params)
	resumes := requestsFor(requests(), kiroWorkflowResumeMethod)
	require.Len(t, resumes, 1)
	assert.Equal(t, map[string]any{"workflowId": kiroRunID, "initiator": "user", "policyPreset": []any{"dev-shell"}}, resumes[0].Params,
		"the steps ask for permission as the session's own turns do")
}

func TestKiroGoalResumeUnderTheAskPolicyStatesNoPreset(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	_, err := a.PerformGoalAction(agent.GoalActionResume, "")
	require.NoError(t, err)

	resumes := requestsFor(requests(), kiroWorkflowResumeMethod)
	require.Len(t, resumes, 1)
	assert.NotContains(t, resumes[0].Params, "policyPreset")
}

func TestKiroGoalPauseOfARunThatDoesNotRunFails(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, goalResponder(map[string]string{kiroWorkflowPauseMethod: `{"paused":false}`}))
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	_, err := a.PerformGoalAction(agent.GoalActionPause, "")

	assert.EqualError(t, err, "the goal run was not running, so Kiro did not pause it")
}

func TestKiroGoalRunActionsWithoutARunAreUnsupported(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, nil)

	for _, action := range []agent.GoalAction{agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume} {
		_, err := a.PerformGoalAction(action, "")
		assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported, action)
	}
	syncPeer(t, a)
	for _, method := range []string{kiroWorkflowCancelMethod, kiroWorkflowPauseMethod, kiroWorkflowResumeMethod} {
		assert.Empty(t, requestsFor(requests(), method))
	}
}

func TestKiroGoalActionOutsideTheKnownFourIsUnsupported(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	_, err := a.PerformGoalAction(agent.GoalAction(99), "x")

	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	syncPeer(t, a)
	for _, method := range []string{kiroWorkflowCancelMethod, kiroWorkflowPauseMethod, kiroWorkflowResumeMethod} {
		assert.Empty(t, requestsFor(requests(), method))
	}
}

// failingGoalRequest answers one workflow request with a JSON-RPC error, and
// every other request with an empty result.
func failingGoalRequest(method string) func(agenttest.RecordedRequest) agenttest.RPCReply {
	return func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == method {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"Workflow wf_x not found"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

// A request that Kiro refuses changes nothing: the run still holds the goal,
// so its card and its controls stay.
func TestKiroGoalRunRequestThatKiroRefusesKeepsTheGoal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		action agent.GoalAction
		method string
	}{
		{action: agent.GoalActionClear, method: kiroWorkflowCancelMethod},
		{action: agent.GoalActionPause, method: kiroWorkflowPauseMethod},
		{action: agent.GoalActionResume, method: kiroWorkflowResumeMethod},
	} {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, failingGoalRequest(tc.method))
			a.HandleOutput(runStart(t, kiroGoalWorkflowName))

			_, err := a.PerformGoalAction(tc.action, "")

			require.ErrorContains(t, err, "Workflow wf_x not found")
			assert.NotErrorIs(t, err, agent.ErrGoalControlUnsupported, "the run exists, and Kiro refused the request")
			assert.Zero(t, sink.GoalClears())
			assert.Len(t, a.SupportedGoalActions(), 4, "the run still holds the goal")
		})
	}
}

func TestKiroGoalPauseReportsAnUnreadableReply(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, goalResponder(map[string]string{kiroWorkflowPauseMethod: `"paused"`}))
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	_, err := a.PerformGoalAction(agent.GoalActionPause, "")

	assert.ErrorContains(t, err, "read the Kiro pause reply")
}

// Kiro can report the end of the run before it answers the cancel. Whichever
// report arrives first clears the goal, and the other one clears nothing.
func TestKiroGoalClearThatTheRunEndOvertookClearsOnce(t *testing.T) {
	t.Parallel()
	var a *Agent
	var sink *agenttest.ControlSink
	a, sink, _ = newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == kiroWorkflowCancelMethod {
			a.HandleOutput(runComplete(t, kiroRunAborted, map[string]any{}))
			return agenttest.RPCReply{Result: json.RawMessage(`{"cancelled":true}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	_, err := a.PerformGoalAction(agent.GoalActionClear, "")

	require.NoError(t, err)
	assert.Equal(t, 1, sink.GoalClears())
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions())
}

// A goal run that starts while another one holds the goal replaces it. The
// notifications of the replaced run then change nothing.
func TestKiroNewGoalRunReplacesTheLastOne(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	a.HandleOutput(workflowNote(t, kiroWorkflowRunStartMethod, map[string]any{
		"workflowId": "wf_second", "workflowName": kiroGoalWorkflowName,
		"inputs": map[string]any{"prompt": "the second objective"},
	}))
	goal, _ := sink.LastGoal()
	assert.Equal(t, "wf_second", goal.NativeID)
	assert.Equal(t, "the second objective", goal.Objective)
	assert.Equal(t, 2, sink.GoalCapabilityPublishes(), "a new run changes the controls")
	reports := len(sink.Goals())

	a.HandleOutput(workflowNote(t, kiroWorkflowLoopIterationMethod, map[string]any{"iteration": 3}))
	a.HandleOutput(workflowNote(t, kiroWorkflowPausedMethod, map[string]any{"pauseReason": "the old run waits"}))
	a.HandleOutput(runComplete(t, kiroRunCompleted, map[string]any{}))

	assert.Len(t, sink.Goals(), reports, "the replaced run reports no goal")
	goal, _ = sink.LastGoal()
	assert.Equal(t, "wf_second", goal.NativeID)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Nil(t, goal.Iterations)
	assert.Zero(t, sink.GoalClears())
	assert.Len(t, a.SupportedGoalActions(), 4)
}

// A recovered run that resumes states no objective in its start. The goal
// keeps the objective that the recovery read.
func TestKiroResumedRecoveredRunKeepsItsObjective(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.restoreGoalRun(kiroTestSession, "wf_restored", "Ship it", kiroRunPaused, "Waiting for the reader.")

	a.HandleOutput(workflowNote(t, kiroWorkflowRunStartMethod, map[string]any{"workflowId": "wf_restored", "workflowName": kiroGoalWorkflowName}))

	goal, _ := sink.LastGoal()
	assert.Equal(t, "wf_restored", goal.NativeID)
	assert.Equal(t, "Ship it", goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Empty(t, goal.StatusDetail)
	assert.False(t, goal.Snapshot, "a live start is a change, not a restatement")
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "the same run changes no control")
}

func TestKiroGoalRecoveryRestatesAnOpenRun(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, goalResponder(map[string]string{
		kiroWorkflowListMethod: `{"runs":[
			{"workflowId":"wf_old","workflowName":"goal","status":"paused","updatedAt":"2026-09-24T04:00:00.000Z","parentSessionId":"session-1"},
			{"workflowId":"wf_new","workflowName":"goal","status":"paused","updatedAt":"2026-09-24T05:00:00.000Z","parentSessionId":"session-1"},
			{"workflowId":"wf_done","workflowName":"goal","status":"completed","updatedAt":"2026-09-24T06:00:00.000Z","parentSessionId":"session-1"},
			{"workflowId":"wf_review","workflowName":"review","status":"running","updatedAt":"2026-09-24T06:00:00.000Z","parentSessionId":"session-1"}
		]}`,
		kiroWorkflowInspectMethod: `{"workflowId":"wf_new","state":{"status":"paused","inputs":{"prompt":" Ship it ","max_iterations":"5"},"pauseReason":"Repeat 'goal-loop' reached maxIterations."}}`,
	}))

	a.recoverGoalRun(agent.Options{ResumeSessionID: kiroTestSession})

	require.Eventually(t, func() bool { return len(sink.Goals()) == 1 }, 30*time.Second, time.Millisecond)
	goal, _ := sink.LastGoal()
	assert.True(t, goal.Snapshot, "the run began before this process, so the report restates it")
	assert.Equal(t, "wf_new", goal.NativeID)
	assert.Equal(t, "Ship it", goal.Objective)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "Repeat 'goal-loop' reached maxIterations.", goal.StatusDetail)
	assert.Len(t, a.SupportedGoalActions(), 4)
	lists := requestsFor(requests(), kiroWorkflowListMethod)
	require.Len(t, lists, 1)
	assert.Equal(t, map[string]any{"sessionId": kiroTestSession}, lists[0].Params)
}

func TestKiroGoalRecoveryOfANewSessionAsksNothing(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, nil)

	a.recoverGoalRun(agent.Options{})
	syncPeer(t, a)

	assert.Empty(t, requestsFor(requests(), kiroWorkflowListMethod))
	assert.Empty(t, sink.Goals())
}

// An agent that serves no session yet has no session whose runs it could
// recover.
func TestKiroGoalRecoveryWithoutASessionAsksNothing(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, nil)
	a.SetSessionIDForTest("")

	a.recoverGoalRun(agent.Options{ResumeSessionID: kiroTestSession})
	syncPeer(t, a)

	assert.Empty(t, requestsFor(requests(), kiroWorkflowListMethod))
	assert.Empty(t, sink.Goals())
}

// openGoalRunList is a list of runs that holds one paused goal run of the
// main session.
const openGoalRunList = `{"runs":[{"workflowId":"wf_open","workflowName":"goal","status":"paused","updatedAt":"2026-09-24T04:00:00.000Z","parentSessionId":"session-1"}]}`

// Each request of the recovery can fail. The recovery then reports the step
// that failed and restores no goal.
func TestKiroGoalRecoveryReportsTheStepThatFailed(t *testing.T) {
	t.Parallel()
	failed := agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"Internal error"}`)}
	unreadable := agenttest.RPCReply{Result: json.RawMessage(`"x"`)}
	for _, tc := range []struct {
		name    string
		list    agenttest.RPCReply
		inspect agenttest.RPCReply
		want    string
	}{
		{name: "the list fails", list: failed, want: "list the workflow runs"},
		{name: "the list is unreadable", list: unreadable, want: "read the workflow runs"},
		{name: "the inspection fails", list: agenttest.RPCReply{Result: json.RawMessage(openGoalRunList)}, inspect: failed, want: "inspect the goal run"},
		{name: "the inspection is unreadable", list: agenttest.RPCReply{Result: json.RawMessage(openGoalRunList)}, inspect: unreadable, want: "read the goal run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
				switch req.Method {
				case kiroWorkflowListMethod:
					return tc.list
				case kiroWorkflowInspectMethod:
					return tc.inspect
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})

			err := a.recoverGoalRunOf(kiroTestSession)

			require.ErrorContains(t, err, tc.want)
			assert.Empty(t, sink.Goals())
			assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions())
		})
	}
}

// A session whose goal runs all ended has no run to inspect.
func TestKiroGoalRecoveryWithNoOpenRunInspectsNothing(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, goalResponder(map[string]string{
		kiroWorkflowListMethod: `{"runs":[
			{"workflowId":"wf_done","workflowName":"goal","status":"completed","updatedAt":"2026-09-24T04:00:00.000Z","parentSessionId":"session-1"},
			{"workflowId":"wf_review","workflowName":"review","status":"running","updatedAt":"2026-09-24T05:00:00.000Z","parentSessionId":"session-1"}
		]}`,
	}))

	require.NoError(t, a.recoverGoalRunOf(kiroTestSession))

	assert.Empty(t, requestsFor(requests(), kiroWorkflowInspectMethod))
	assert.Empty(t, sink.Goals())
}

// An inspection that states no status leaves the status of the list, so a run
// that the list states as paused restores as paused.
func TestKiroGoalRecoveryFallsBackToTheListedStatus(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, goalResponder(map[string]string{
		kiroWorkflowListMethod:    openGoalRunList,
		kiroWorkflowInspectMethod: `{"workflowId":"wf_open","state":{"inputs":{"prompt":"Ship it"},"pauseReason":"Waiting for the reader."}}`,
	}))

	require.NoError(t, a.recoverGoalRunOf(kiroTestSession))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "wf_open", goal.NativeID)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "Waiting for the reader.", goal.StatusDetail)
}

// A run that still runs restores as active, with no pause reason, whatever
// reason an earlier pause left in its state.
func TestKiroGoalRecoveryOfARunningRunIsActive(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.restoreGoalRun(kiroTestSession, "wf_running", "Ship it", kiroRunRunning, "An old pause.")

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Empty(t, goal.StatusDetail)
	assert.True(t, goal.Snapshot)
}

func TestKiroGoalRecoveryYieldsToALiveRun(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	a.restoreGoalRun(kiroTestSession, "wf_older", "old objective", kiroRunRunning, "")

	goal, _ := sink.LastGoal()
	assert.Equal(t, kiroRunID, goal.NativeID, "a live notification is newer than the stored run")
	assert.Len(t, sink.Goals(), 1)
}

// A context clear that lands while the recovery waits on Kiro replaces the
// session. The recovered goal belongs to the replaced session, so it reaches no
// card of the new one.
func TestKiroGoalRecoveryThatAClearOvertookWritesNoGoal(t *testing.T) {
	t.Parallel()
	var a *Agent
	replies := goalResponder(map[string]string{
		kiroWorkflowListMethod:    `{"runs":[{"workflowId":"wf_old","workflowName":"goal","status":"running","updatedAt":"2026-09-24T04:00:00.000Z","parentSessionId":"session-1"}]}`,
		kiroWorkflowInspectMethod: `{"workflowId":"wf_old","state":{"status":"running","inputs":{"prompt":"Ship it"}}}`,
	})
	a, sink, _ := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == kiroWorkflowInspectMethod {
			// The clear swaps the session while Kiro answers.
			a.SetSessionIDForTest("session-2")
		}
		return replies(req)
	})

	require.NoError(t, a.recoverGoalRunOf(kiroTestSession))

	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalCapabilityPublishes())
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions())
}

func TestKiroLatestOpenGoalRun(t *testing.T) {
	t.Parallel()
	runs := []kiroWorkflowRunSummary{
		{WorkflowID: "running-old", WorkflowName: "goal", Status: kiroRunRunning, UpdatedAt: "2026-09-24T01:00:00.000Z"},
		{WorkflowID: "by-name", Name: "goal", Status: kiroRunPaused, UpdatedAt: "2026-09-24T02:00:00.000Z"},
		{WorkflowID: "other-session", WorkflowName: "goal", Status: kiroRunRunning, UpdatedAt: "2026-09-24T09:00:00.000Z", ParentSessionID: "sess-other"},
		{WorkflowID: "ended", WorkflowName: "goal", Status: kiroRunAborted, UpdatedAt: "2026-09-24T09:00:00.000Z"},
		{WorkflowID: "not-a-goal", WorkflowName: "review", Status: kiroRunRunning, UpdatedAt: "2026-09-24T09:00:00.000Z"},
	}

	run, found := latestOpenGoalRun(runs, kiroTestSession)
	require.True(t, found)
	assert.Equal(t, "by-name", run.WorkflowID)

	_, found = latestOpenGoalRun(nil, kiroTestSession)
	assert.False(t, found)
	_, found = latestOpenGoalRun(runs[3:], kiroTestSession)
	assert.False(t, found)
}
