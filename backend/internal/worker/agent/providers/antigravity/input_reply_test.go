package antigravity

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityExactInputReplyUsesNativeExecutionAndStepOrder(t *testing.T) {
	rpc, requests := fakeAntigravityRPC(t)
	a, sink := testAgent(t)
	a.rpc = rpc
	receipt, err := a.SendInputWithReceipt("identical prompt", nil)
	require.NoError(t, err)
	require.NotEmpty(t, receipt.InputID)
	require.Equal(t, "cascade", receipt.SessionID)
	require.Empty(t, receipt.TurnID)
	require.Len(t, requests("SendUserCascadeMessage"), 1)
	assert.Equal(t, receipt.InputID, requests("SendUserCascadeMessage")[0]["clientMessageId"])
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 4, []int{0, 3, 2},
		replyInputStep(receipt.InputID, "owned-execution"),
		replyPlannerStep("foreign-execution", "foreign text", contracts.AntigravityStepStatusDone),
		replyPlannerStep("owned-execution", "second text", contracts.AntigravityStepStatusDone),
	))
	assert.Empty(t, sink.NativeReplyTurns())
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusIdle, "trajectory", 4, []int{1},
		replyPlannerStep("owned-execution", "first text", contracts.AntigravityStepStatusDone),
	))
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "cascade", TurnID: "owned-execution", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "antigravity:cascade:1"}, {Ordinal: 3, MessageKey: "antigravity:cascade:2"}},
	}}, sink.NativeReplyTurns())
	assert.Empty(t, sink.NativeReplyRetirements())
	next, err := a.SendInputWithReceipt("identical prompt", nil)
	require.NoError(t, err)
	assert.NotEqual(t, receipt.InputID, next.InputID)
}

func TestAntigravityExactReplySkipsBlankPlannerRows(t *testing.T) {
	rpc, _ := fakeAntigravityRPC(t)
	a, sink := testAgent(t)
	a.rpc = rpc
	receipt, err := a.SendInputWithReceipt("report readiness", nil)
	require.NoError(t, err)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusIdle, "trajectory", 3, []int{0, 1, 2},
		replyInputStep(receipt.InputID, "execution"),
		replyPlannerStep("execution", "\uFEFF \t", contracts.AntigravityStepStatusDone),
		replyPlannerStep("execution", "ready", contracts.AntigravityStepStatusDone),
	))
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 3, MessageKey: "antigravity:cascade:2"}}, turns[0].Items)
}

func TestAntigravityUnreadSteerKeepsItsReceiptForTheNextExecution(t *testing.T) {
	a, sink := testAgent(t)
	rpc, requests := fakeAntigravityRPC(t)
	a.rpc = rpc
	first, err := a.SendInputWithReceipt("original prompt", nil)
	require.NoError(t, err)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 1, []int{0}, replyInputStep(first.InputID, "first")))
	steer, err := a.SteerInputWithReceipt(agent.SteerRequest{Content: "revision"})
	require.NoError(t, err)
	require.NotEmpty(t, steer.InputID)
	assert.Empty(t, steer.TurnID)
	assert.Equal(t, steer.InputID, requests("SendUserCascadeMessage")[1]["clientMessageId"])
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusIdle, "trajectory", 2, []int{1},
		replyPlannerStep("first", "original reply", contracts.AntigravityStepStatusDone),
	))
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: first.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
	assert.Empty(t, sink.NativeReplyRetirements())

	next, err := a.SendInputWithReceipt("continue", nil)
	require.NoError(t, err)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusIdle, "trajectory", 5, []int{2, 3, 4},
		replyInputStep(steer.InputID, "second"),
		replyInputStep(next.InputID, "second"),
		replyPlannerStep("second", "revised reply", contracts.AntigravityStepStatusDone),
	))
	turns = sink.NativeReplyTurns()
	require.Len(t, turns, 2)
	assert.Equal(t, agent.NativeReplyTurn{
		SessionID: "cascade", TurnID: "second", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: steer.InputID, AfterOrdinal: 3}, {InputID: next.InputID, AfterOrdinal: 4}},
		Items:  []agent.NativeReplyItem{{Ordinal: 5, MessageKey: "antigravity:cascade:4"}},
	}, turns[1])
}

func TestAntigravityRetiresAnUnreadChildSteerOnStreamLoss(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	rpc, _ := fakeAntigravityRPC(t)
	a.rpc = rpc
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	receipt, err := a.SteerChildInputWithReceipt(child.id, "revision", nil)
	require.NoError(t, err)
	require.Equal(t, child.id, receipt.SessionID)
	require.NotEmpty(t, receipt.InputID)
	a.fatalCascade(child, fmt.Errorf("stream closed before the child read the steer"))
	assert.Empty(t, sink.NativeReplyRetirements())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.Child(child.childAgentID).NativeReplyRetirements())
}

type steerControlBindingSink struct {
	agent.ProviderServices
	bindings map[string][]string
}

func (sink *steerControlBindingSink) SetNativeControlInputs(binding agent.NativeControlInputs) error {
	sink.bindings[binding.RequestID] = binding.InputIDs
	return nil
}

func TestAntigravitySteerUpdatesAlreadyPublishedControlsByNativePosition(t *testing.T) {
	a, _ := testAgent(t)
	rpc, _ := fakeAntigravityRPC(t)
	a.rpc = rpc
	sink := &steerControlBindingSink{ProviderServices: a.root.sink, bindings: make(map[string][]string)}
	a.root.sink = sink
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning, TrajectoryID: "trajectory"})
	steer, err := a.SteerInputWithReceipt(agent.SteerRequest{Content: "revised input"})
	require.NoError(t, err)
	permission := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_WAITING","metadata":{"executionId":"execution"},"requestedInteraction":{"permission":{"resource":{"action":"command","target":"printf ready"}}}}`)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 4, []int{1, 3}, permission, permission))
	assert.Empty(t, sink.bindings)

	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 4, []int{2}, replyInputStep(steer.InputID, "execution")))
	assert.Empty(t, sink.bindings["antigravity:cascade:trajectory:1"])
	assert.Equal(t, []string{steer.InputID}, sink.bindings["antigravity:cascade:trajectory:3"])

	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 5, []int{4}, replyInputStep(steer.InputID, "another execution")))
	assert.Empty(t, sink.bindings["antigravity:cascade:trajectory:3"])
}

func TestAntigravityChildReplyUsesItsOwnCascade(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	rpc, requests := fakeAntigravityRPC(t)
	a.rpc = rpc
	rootReceipt, err := a.SendInputWithReceipt("identical prompt", nil)
	require.NoError(t, err)
	result, err := a.SendChildInput(child.id, agent.ChildInput{Content: "identical prompt"})
	require.NoError(t, err)
	require.True(t, result.StartsTurn)
	childReceipt := result.NativeReceipt
	require.Equal(t, child.id, childReceipt.SessionID)
	require.NotEmpty(t, childReceipt.InputID)
	require.NotEqual(t, rootReceipt.InputID, childReceipt.InputID)
	require.Len(t, requests("SendUserCascadeMessage"), 2)
	assert.Equal(t, childReceipt.InputID, requests("SendUserCascadeMessage")[1]["clientMessageId"])

	update := inputReplyUpdate(cascadeStatusIdle, "child-trajectory", 2, []int{0, 1},
		replyInputStep(childReceipt.InputID, "execution"),
		replyPlannerStep("execution", "child text", contracts.AntigravityStepStatusDone),
	)
	require.NoError(t, a.applyUpdate(child, update, false))
	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: child.id, TurnID: "execution", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: childReceipt.InputID, AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "antigravity:child:1"}},
	}}, sink.Child(child.childAgentID).NativeReplyTurns())
}

func TestAntigravityRetiresChildReceiptWhenTheCascadeEndsBeforeEcho(t *testing.T) {
	for _, exit := range []string{"child stream loss", "owner stop"} {
		t.Run(exit, func(t *testing.T) {
			a, sink, child := childLifecycleTestAgent(t)
			rpc, _ := fakeAntigravityRPC(t)
			a.rpc = rpc
			require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
			require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
			result, err := a.SendChildInput(child.id, agent.ChildInput{Content: "continue"})
			require.NoError(t, err)
			require.NotEmpty(t, result.NativeReceipt.InputID)
			if exit == "child stream loss" {
				a.fatalCascade(child, fmt.Errorf("stream closed before echo"))
			} else {
				a.finishCascades([]*cascadeState{a.root, child}, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
			}
			assert.Empty(t, sink.NativeReplyRetirements())
			childSink := sink.Child(child.childAgentID)
			assert.Empty(t, childSink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
				result.NativeReceipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			}, childSink.NativeReplyRetirements())
		})
	}
}

func TestAntigravityExactReplyRequiresUnambiguousNativeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name             string
		echoExecution    string
		plannerExecution string
		plannerStatus    string
		settledLength    int
		settledHistory   string
		duplicateEcho    bool
	}{
		{name: "missing echo execution", plannerExecution: "execution", settledLength: 2},
		{name: "unowned planner text", echoExecution: "execution", settledLength: 2},
		{name: "missing step", echoExecution: "execution", plannerExecution: "execution", settledLength: 3},
		{name: "oversized step count", echoExecution: "execution", plannerExecution: "execution", settledLength: math.MaxInt},
		{name: "negative step count", echoExecution: "execution", plannerExecution: "execution", settledLength: -1},
		{name: "trajectory replacement", echoExecution: "execution", plannerExecution: "execution", settledLength: 2, settledHistory: "replacement"},
		{name: "conflicting echo", echoExecution: "execution", plannerExecution: "execution", settledLength: 3, duplicateEcho: true},
		{name: "unfinished text", echoExecution: "execution", plannerExecution: "execution", plannerStatus: contracts.AntigravityStepStatusGenerating, settledLength: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc, _ := fakeAntigravityRPC(t)
			a, sink := testAgent(t)
			a.rpc = rpc
			receipt, err := a.SendInputWithReceipt("check readiness", nil)
			require.NoError(t, err)
			feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 1, []int{0}, replyInputStep(receipt.InputID, tc.echoExecution)))
			plannerStatus := tc.plannerStatus
			if plannerStatus == "" {
				plannerStatus = contracts.AntigravityStepStatusDone
			}
			indices := []int{1}
			steps := []json.RawMessage{replyPlannerStep(tc.plannerExecution, "unproved text", plannerStatus)}
			if tc.duplicateEcho {
				indices = append(indices, 2)
				steps = append(steps, replyInputStep(receipt.InputID, "another execution"))
			}
			feedUpdate(t, a, inputReplyUpdate(cascadeStatusIdle, tc.settledHistory, tc.settledLength, indices, steps...))
			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
				receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			}, sink.NativeReplyRetirements())
		})
	}
}

func TestAntigravityExactReplyRetiresAnInputWhenTheProcessEndsBeforeEcho(t *testing.T) {
	rpc, _ := fakeAntigravityRPC(t)
	a, sink := testAgent(t)
	a.rpc = rpc
	receipt, err := a.SendInputWithReceipt("check readiness", nil)
	require.NoError(t, err)
	a.finishCascades([]*cascadeState{a.root}, agent.MessageCompletionError, bgtask.StatusFailed)
	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
}

func TestAntigravityExactReplyRequiresNativeEvidenceOfAnAbortedExecution(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interrupted bool
		execution   string
		outcome     leapmuxv1.AgentInputTurnOutcome
	}{
		{name: "own error", execution: "execution", outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED},
		{name: "foreign error", execution: "foreign", outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
		{name: "own cancellation", execution: "execution", interrupted: true, outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED},
		{name: "foreign cancellation", execution: "foreign", interrupted: true, outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc, _ := fakeAntigravityRPC(t)
			a, sink := testAgent(t)
			a.rpc = rpc
			receipt, err := a.SendInputWithReceipt("check readiness", nil)
			require.NoError(t, err)
			feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 1, []int{0}, replyInputStep(receipt.InputID, "execution")))
			step := json.RawMessage(fmt.Sprintf(`{"type":%q,"status":%q,"metadata":{"executionId":%q},"errorMessage":{}}`,
				contracts.AntigravityStepTypeErrorMessage, contracts.AntigravityStepStatusError, tc.execution))
			if tc.interrupted {
				require.NoError(t, a.Interrupt(agent.StopContext{}))
				step = replyPlannerStep(tc.execution, "partial reply", contracts.AntigravityStepStatusCanceled)
			}
			feedUpdate(t, a, inputReplyUpdate(cascadeStatusIdle, "trajectory", 2, []int{1}, step))
			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{receipt: tc.outcome}, sink.NativeReplyRetirements())
		})
	}
}

func TestAntigravityLateInputRefusalKeepsTheNextInputInFlight(t *testing.T) {
	firstSent := make(chan struct{}, 1)
	allowRefusal, refuse := newTestGate(t)
	defer refuse()
	a, _ := testAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case languageServerService + "GetCascadeModelConfigData", languageServerService + "RetrieveUserQuotaSummary":
			_, _ = w.Write([]byte("{}"))
			return
		}
		if !assert.Equal(t, languageServerService+"SendUserCascadeMessage", r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var request struct {
			Items []struct{ Text string }
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) || !assert.Len(t, request.Items, 1) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Items[0].Text == "first" {
			firstSent <- struct{}{}
			select {
			case <-allowRefusal:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("{}"))
	})
	done := make(chan error, 1)
	go func() { done <- a.SendInput("first", nil) }()
	receiveTestValue(t, firstSent)
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning})
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	receipt, err := a.SendInputWithReceipt("next", nil)
	require.NoError(t, err)
	refuse()
	require.Error(t, receiveTestValue(t, done))
	_, err = a.SendInputWithReceipt("must stay queued", nil)
	assert.ErrorIs(t, err, agent.ErrAgentBusy)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	require.Len(t, a.root.inputReplies, 1)
	require.NotNil(t, a.root.inputReplies[receipt.InputID])
	assert.Equal(t, receipt, a.root.inputReplies[receipt.InputID].receipt)
}

func inputReplyUpdate(status, trajectory string, length int, indices []int, steps ...json.RawMessage) agentStateUpdate {
	update := updateWithSteps(status, indices, steps)
	update.TrajectoryID = trajectory
	update.MainTrajectory.StepsUpdate.TotalLength = length
	return update
}

func replyInputStep(clientMessageID, executionID string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":%q,"status":%q,"metadata":{"executionId":%q},"userInput":{"clientMessageId":%q}}`,
		contracts.AntigravityStepTypeUserInput, contracts.AntigravityStepStatusDone, executionID, clientMessageID))
}

func replyPlannerStep(executionID, text, status string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":%q,"status":%q,"metadata":{"executionId":%q},"plannerResponse":{"response":%q}}`,
		contracts.AntigravityStepTypePlannerResponse, status, executionID, text))
}
