package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The ids of the probe's goal run.
const (
	kiroRunID       = "wf_c32efb68937c77c4"
	kiroStepSession = "sess_7a123558"
)

// workflowNote encodes one workflow notification of the main session's run.
func workflowNote(t *testing.T, method string, params map[string]any) []byte {
	t.Helper()
	if _, ok := params["workflowId"]; !ok {
		params["workflowId"] = kiroRunID
	}
	if _, ok := params["parentSessionId"]; !ok {
		params["parentSessionId"] = kiroTestSession
	}
	return notification(t, method, params)
}

// runStart is the start of the probe's goal run.
func runStart(t *testing.T, name string) []byte {
	t.Helper()
	return workflowNote(t, kiroWorkflowRunStartMethod, map[string]any{
		"workflowName": name,
		"inputs":       map[string]any{"prompt": "make hello.txt say bye", "max_iterations": "2"},
	})
}

// stepPath is the node path of one round's step.
func stepPath(iteration string) []any {
	return []any{kiroRunID, "goal-loop", iteration, "work"}
}

// nodeStart is the start of one step. Kiro reports it twice, and only the
// second one states the session of the step.
func nodeStart(t *testing.T, iteration int, sessionID string) []byte {
	t.Helper()
	params := map[string]any{
		"nodeId": "work", "nodePath": stepPath([]string{"iter-0", "iter-1"}[iteration]),
		"type": "step", "agentName": "wf-coder", "iteration": iteration,
	}
	if sessionID != "" {
		params["sessionId"] = sessionID
	}
	return workflowNote(t, kiroWorkflowNodeStartMethod, params)
}

// nodeComplete is the end of one step.
func nodeComplete(t *testing.T, iteration int, status, output string) []byte {
	t.Helper()
	return workflowNote(t, kiroWorkflowNodeCompleteMethod, map[string]any{
		"nodeId": "work", "nodePath": stepPath([]string{"iter-0", "iter-1"}[iteration]),
		"status": status, "capturedOutput": output,
	})
}

// runComplete is the end of the run.
func runComplete(t *testing.T, status string, finalState map[string]any) []byte {
	t.Helper()
	return workflowNote(t, kiroWorkflowRunCompleteMethod, map[string]any{"status": status, "finalState": finalState})
}

// failedFinalState is the final state of a goal run whose step failed, in the
// shape that the probe recorded. Kiro states the reason on the failed step and
// on the round above it, and never on the run itself.
func failedFinalState(reason string) map[string]any {
	step := map[string]any{
		"nodeId": "work", "type": "step", "status": kiroRunFailed,
		"completionSignal": "error", "completionSignalSource": "send_message", "failureReason": reason,
	}
	round := map[string]any{"nodeId": "goal-loop#0", "type": "sequence", "status": kiroRunFailed, "iteration": 0, "children": []any{step}, "failureReason": reason}
	loop := map[string]any{"nodeId": "goal-loop", "type": "repeat", "status": kiroRunFailed, "children": []any{round}}
	return map[string]any{
		"workflowId": kiroRunID, "workflowName": kiroGoalWorkflowName, "status": kiroRunFailed,
		"root": map[string]any{"nodeId": kiroRunID, "type": "sequence", "status": kiroRunFailed, "children": []any{loop}},
	}
}

// runRowKey is the registry row of the probe's run.
const runRowKey = kiroWorkflowRowPrefix + kiroRunID

func TestKiroWorkflowRunAndItsSteps(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(runStart(t, "review"))
	run, ok := sink.BackgroundTask(runRowKey)
	require.True(t, ok, "the run opens a row")
	assert.Equal(t, bgtask.KindWorkflow, run.Kind)
	assert.Equal(t, "review: make hello.txt say bye", run.Title)
	assert.Equal(t, kiroRunID, run.GroupKey)
	assert.Equal(t, "review", run.GroupLabel)
	assert.Equal(t, bgtask.StatusRunning, run.Status)

	// The repeat node and the first report of the step open nothing.
	a.HandleOutput(workflowNote(t, kiroWorkflowNodeStartMethod, map[string]any{
		"nodeId": "goal-loop", "nodePath": []any{kiroRunID, "goal-loop"}, "type": "repeat",
	}))
	a.HandleOutput(nodeStart(t, 0, ""))
	assert.Len(t, sink.BackgroundTasks(), 1)

	a.HandleOutput(nodeStart(t, 0, kiroStepSession))
	step, ok := sink.BackgroundTask(kiroStepSession)
	require.True(t, ok, "the step opens a row once its session exists")
	assert.Equal(t, "review · work #1", step.Title)
	assert.Equal(t, kiroRunID, step.GroupKey, "the step groups with its run")
	assert.Equal(t, bgtask.StatusRunning, step.Status)
	require.NotEmpty(t, step.ChildAgentID)
	child := sink.Child(step.ChildAgentID)

	// The step's own session streams into the step's transcript.
	a.HandleOutput(sessionUpdate(t, kiroStepSession, map[string]any{
		"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "Make hello.txt say bye."},
	}))
	a.HandleOutput(sessionUpdate(t, kiroStepSession, infoUpdateObject(map[string]any{"kind": kiroKindTurnStart})))
	a.HandleOutput(sessionUpdate(t, kiroStepSession, map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Progress."},
	}))
	assert.False(t, a.AgentTurnActive(), "the turn markers of a step session are no turn of the main session")

	a.HandleOutput(nodeComplete(t, 0, "completed", "(goal-fallback) progress"))
	step, _ = sink.BackgroundTask(kiroStepSession)
	assert.Equal(t, bgtask.StatusCompleted, step.Status)
	assert.Equal(t, []string{"Make hello.txt say bye."}, userContents(t, child), "the step's instruction opens its transcript")
	assert.Equal(t, []string{"Progress."}, childTexts(t, child))
	assert.Equal(t, []string{"(goal-fallback) progress"}, reportTexts(child))
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "Progress.", "the step's text stays out of the parent")
	}

	a.HandleOutput(runComplete(t, kiroRunCompleted, map[string]any{}))
	run, _ = sink.BackgroundTask(runRowKey)
	assert.Equal(t, bgtask.StatusCompleted, run.Status)
}

func TestKiroWorkflowOfAnotherSessionOpensNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(workflowNote(t, kiroWorkflowRunStartMethod, map[string]any{"workflowName": "review", "parentSessionId": "sess-other"}))
	a.HandleOutput(workflowNote(t, kiroWorkflowNodeStartMethod, map[string]any{
		"nodeId": "work", "nodePath": stepPath("iter-0"), "type": "step", "sessionId": "s", "parentSessionId": "sess-other",
	}))

	assert.Empty(t, sink.BackgroundTasks())
}

func TestKiroWorkflowNotificationWithoutAParentBelongsToAKnownRun(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	// No parent and no known run: nothing to follow.
	a.HandleOutput(notification(t, kiroWorkflowPausedMethod, map[string]any{"workflowId": "wf_unknown", "pauseReason": "x"}))
	assert.Empty(t, sink.BackgroundTasks())

	a.HandleOutput(runStart(t, "review"))
	a.HandleOutput(notification(t, kiroWorkflowPausedMethod, map[string]any{"workflowId": kiroRunID, "pauseReason": "Waiting for input."}))
	run, _ := sink.BackgroundTask(runRowKey)
	assert.Equal(t, "Paused: Waiting for input.", run.ActiveForm)
}

func TestKiroUnreadableWorkflowNotificationChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroWorkflowRunStartMethod, map[string]any{"workflowName": "review", "parentSessionId": kiroTestSession}))
	a.HandleOutput(notification(t, kiroWorkflowRunStartMethod, map[string]any{"workflowId": 7}))

	assert.Empty(t, sink.BackgroundTasks())
}

func TestKiroFailedStepReportsItsReason(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, "review"))
	a.HandleOutput(nodeStart(t, 0, kiroStepSession))

	a.HandleOutput(workflowNote(t, kiroWorkflowNodeCompleteMethod, map[string]any{
		"nodeId": "work", "nodePath": stepPath("iter-0"), "status": "failed", "failureReason": "The build broke.",
	}))

	step, _ := sink.BackgroundTask(kiroStepSession)
	assert.Equal(t, bgtask.StatusFailed, step.Status)
	assert.Equal(t, []string{"The build broke."}, reportTexts(sink.Child(step.ChildAgentID)))
}

func TestKiroStepEndOfAnUnknownStepChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, "review"))

	a.HandleOutput(nodeComplete(t, 0, "completed", "never opened"))
	a.HandleOutput(workflowNote(t, kiroWorkflowNodePausedMethod, map[string]any{"nodeId": "work", "nodePath": stepPath("iter-0")}))

	assert.Len(t, sink.BackgroundTasks(), 1, "only the run's own row exists")
}

func TestKiroPausedStepStatesItsReason(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, "review"))
	a.HandleOutput(nodeStart(t, 0, kiroStepSession))

	a.HandleOutput(workflowNote(t, kiroWorkflowNodePausedMethod, map[string]any{
		"nodeId": "work", "nodePath": stepPath("iter-0"), "reason": "The step asks a question.",
	}))

	step, _ := sink.BackgroundTask(kiroStepSession)
	assert.Equal(t, bgtask.StatusRunning, step.Status, "a paused step can go on")
	assert.Equal(t, "Paused: The step asks a question.", step.ActiveForm)
}

func TestKiroRunThatEndsPausedKeepsItsRowOpen(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, "review"))

	a.HandleOutput(workflowNote(t, kiroWorkflowPausedMethod, map[string]any{"pauseReason": "Repeat 'goal-loop' reached maxIterations."}))
	a.HandleOutput(runComplete(t, kiroRunPaused, map[string]any{"status": "paused", "pauseReason": "Repeat 'goal-loop' reached maxIterations."}))

	run, _ := sink.BackgroundTask(runRowKey)
	assert.Equal(t, bgtask.StatusRunning, run.Status, "a paused run can resume")
	assert.Equal(t, "Paused: Repeat 'goal-loop' reached maxIterations.", run.ActiveForm)
	assert.True(t, run.EndedAt.IsZero())

	// A resume reports the start again.
	a.HandleOutput(runStart(t, "review"))
	run, _ = sink.BackgroundTask(runRowKey)
	assert.Equal(t, bgtask.StatusRunning, run.Status)
}

func TestKiroRunEndClosesTheStepsItLeftOpen(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status string
		want   bgtask.Status
	}{
		{status: kiroRunAborted, want: bgtask.StatusStopped},
		{status: kiroRunFailed, want: bgtask.StatusFailed},
		{status: kiroRunCompleted, want: bgtask.StatusCompleted},
	} {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
			a.HandleOutput(runStart(t, "review"))
			a.HandleOutput(nodeStart(t, 0, kiroStepSession))

			finalState := map[string]any{}
			if tc.status == kiroRunFailed {
				finalState = failedFinalState("The build broke.")
			}
			a.HandleOutput(runComplete(t, tc.status, finalState))

			step, _ := sink.BackgroundTask(kiroStepSession)
			assert.Equal(t, tc.want, step.Status)
			run, _ := sink.BackgroundTask(runRowKey)
			assert.Equal(t, tc.want, run.Status)
			assert.False(t, run.EndedAt.IsZero())
			a.stateMu.Lock()
			assert.Empty(t, a.workflows.runs, "an ended run is no longer followed")
			a.stateMu.Unlock()
		})
	}
}

// A pause that states no reason still shows that the work waits.
func TestKiroPauseWithoutAReasonStatesPaused(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, "review"))
	a.HandleOutput(nodeStart(t, 0, kiroStepSession))

	a.HandleOutput(workflowNote(t, kiroWorkflowNodePausedMethod, map[string]any{"nodeId": "work", "nodePath": stepPath("iter-0"), "reason": "  "}))
	a.HandleOutput(workflowNote(t, kiroWorkflowPausedMethod, map[string]any{}))

	step, _ := sink.BackgroundTask(kiroStepSession)
	assert.Equal(t, "Paused", step.ActiveForm)
	run, _ := sink.BackgroundTask(runRowKey)
	assert.Equal(t, "Paused", run.ActiveForm)
}

// A loop notice that states no round counts nothing.
func TestKiroLoopIterationWithoutARoundChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))
	reports := len(sink.Goals())

	a.HandleOutput(workflowNote(t, kiroWorkflowLoopIterationMethod, map[string]any{"loopId": "goal-loop"}))

	assert.Len(t, sink.Goals(), reports)
	goal, _ := sink.LastGoal()
	assert.Nil(t, goal.Iterations)
}

func TestKiroWorkflowRunWithoutANameOrObjective(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(workflowNote(t, kiroWorkflowRunStartMethod, map[string]any{}))

	run, ok := sink.BackgroundTask(runRowKey)
	require.True(t, ok)
	assert.Equal(t, "Workflow", run.Title)
	assert.Equal(t, "Workflow", run.GroupLabel)
}

// openingSession answers session/new with a new session, as Kiro does for a
// context clear.
func openingSession(sessionID string) func(agenttest.RecordedRequest) agenttest.RPCReply {
	return func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"` + sessionID + `"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

func TestKiroClearContextCancelsEachUnfinishedRun(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, openingSession("session-2"))
	a.HandleOutput(runStart(t, "review"))
	a.HandleOutput(workflowNote(t, kiroWorkflowRunStartMethod, map[string]any{"workflowId": "wf_done", "workflowName": "other"}))
	a.HandleOutput(workflowNote(t, kiroWorkflowRunCompleteMethod, map[string]any{"workflowId": "wf_done", "status": kiroRunCompleted}))

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncPeer(t, a)

	cancels := requestsFor(requests(), kiroWorkflowCancelMethod)
	require.Len(t, cancels, 1, "an ended run needs no cancel")
	assert.Equal(t, kiroRunID, cancels[0].Params["workflowId"])
	assert.Equal(t, "user", cancels[0].Params["initiator"])
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.workflows.runs)
}

// A goal run that live notifications reported is both a run and the goal. The
// clear cancels each unfinished run once, in the order of their ids.
func TestKiroClearContextCancelsEachUnfinishedRunOnce(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, openingSession("session-2"))
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))
	a.HandleOutput(workflowNote(t, kiroWorkflowRunStartMethod, map[string]any{"workflowId": "wf_a_review", "workflowName": "review"}))

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncPeer(t, a)

	var cancelled []any
	for _, request := range requestsFor(requests(), kiroWorkflowCancelMethod) {
		cancelled = append(cancelled, request.Params["workflowId"])
	}
	assert.Equal(t, []any{"wf_a_review", kiroRunID}, cancelled)
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions())
}

// Nothing waits for the answer of a cancel that a clear sends. A cancel that
// Kiro refuses leaves the clear whole, and the runs of the old session are
// forgotten all the same.
func TestKiroClearContextSurvivesARefusedCancel(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		switch req.Method {
		case acp.MethodSessionNew:
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		case kiroWorkflowCancelMethod:
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"Workflow not found"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.HandleOutput(runStart(t, "review"))

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncPeer(t, a)

	assert.Len(t, requestsFor(requests(), kiroWorkflowCancelMethod), 1)
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.workflows.runs)
}

// A cancel that cannot reach Kiro -- the process is gone -- still leaves the
// retired session with no run and no goal.
func TestKiroRetireSessionThatCannotSendACancelForgetsItsRuns(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))
	a.SetStdinForTest(agenttest.FailingStdin{})

	a.retireSession(kiroTestSession)

	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions())
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.workflows.runs)
}

// A goal run that the start recovered from Kiro's store belongs to the
// outgoing session too, although no live notification reported it.
func TestKiroClearContextCancelsTheRecoveredGoalRun(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, openingSession("session-2"))
	a.restoreGoalRun(kiroTestSession, "wf_restored", "Ship it", kiroRunPaused, "")
	require.Len(t, sink.Goals(), 1)

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncPeer(t, a)

	cancels := requestsFor(requests(), kiroWorkflowCancelMethod)
	require.Len(t, cancels, 1)
	assert.Equal(t, "wf_restored", cancels[0].Params["workflowId"])
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet}, a.SupportedGoalActions(), "the new session holds no run")
}

// An agent that answers session/new with the outgoing id serves that session
// still, so its runs go on.
func TestKiroClearContextKeepsTheRunsOfASessionThatStays(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, openingSession(kiroTestSession))
	a.HandleOutput(runStart(t, kiroGoalWorkflowName))

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncPeer(t, a)

	assert.Empty(t, requestsFor(requests(), kiroWorkflowCancelMethod))
	assert.Len(t, a.SupportedGoalActions(), 4, "the goal run of the session goes on")
}

func TestKiroStepTitle(t *testing.T) {
	t.Parallel()
	zero, two := 0, 2
	assert.Equal(t, "goal · work #1", stepTitle(&workflowRun{name: "goal"}, kiroWorkflowNotification{NodeID: "work", Iteration: &zero}))
	assert.Equal(t, "goal · work #3", stepTitle(&workflowRun{name: "goal"}, kiroWorkflowNotification{NodeID: "work", Iteration: &two}))
	assert.Equal(t, "Workflow · lint", stepTitle(&workflowRun{}, kiroWorkflowNotification{NodeID: "lint"}))
}

func TestKiroNodeAndRunStatusMapping(t *testing.T) {
	t.Parallel()
	assert.Equal(t, bgtask.StatusCompleted, kiroNodeStatus(kiroRunCompleted))
	assert.Equal(t, bgtask.StatusFailed, kiroNodeStatus(kiroRunFailed))
	assert.Equal(t, bgtask.StatusStopped, kiroNodeStatus(kiroRunAborted))
	assert.Equal(t, bgtask.StatusStopped, kiroNodeStatus(kiroNodeSkipped))
	assert.Equal(t, bgtask.StatusCompleted, kiroRunStatus(kiroRunCompleted))
	assert.Equal(t, bgtask.StatusFailed, kiroRunStatus(kiroRunFailed))
	assert.Equal(t, bgtask.StatusStopped, kiroRunStatus(kiroRunAborted))
	assert.Equal(t, bgtask.StatusStopped, kiroRunStatus("something-new"))
}

func TestKiroFailedRunStatesTheReasonOfItsFailedStep(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(runStart(t, "review"))

	a.HandleOutput(runComplete(t, kiroRunFailed, failedFinalState("Step signaled error via send_message.")))

	run, ok := sink.BackgroundTask(runRowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, run.Status)
	assert.Equal(t, "Step signaled error via send_message.", run.ActiveForm)
}

func TestKiroWorkflowNodeFailureReason(t *testing.T) {
	t.Parallel()
	decode := func(raw string) kiroWorkflowNode {
		var node kiroWorkflowNode
		require.NoError(t, json.Unmarshal([]byte(raw), &node))
		return node
	}

	for _, tc := range []struct {
		name string
		tree string
		want string
	}{
		{name: "the deepest failed node", tree: `{"status":"failed","failureReason":"the round","children":[{"status":"failed","failureReason":" the step "}]}`, want: "the step"},
		{name: "a failed node over a child with no reason", tree: `{"status":"failed","failureReason":"the round","children":[{"status":"failed"}]}`, want: "the round"},
		{name: "the first failed branch", tree: `{"status":"failed","children":[{"status":"completed","failureReason":"stale"},{"status":"failed","failureReason":"second"},{"status":"failed","failureReason":"third"}]}`, want: "second"},
		{name: "a node that did not fail", tree: `{"status":"completed","failureReason":"stale"}`, want: ""},
		{name: "no reason anywhere", tree: `{"status":"failed","children":[{"status":"failed"}]}`, want: ""},
		{name: "an empty tree", tree: `{}`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, decode(tc.tree).failureReason())
		})
	}
}
