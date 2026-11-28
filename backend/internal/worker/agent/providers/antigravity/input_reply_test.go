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
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityExactInputReplyUsesNativeExecutionAndStepOrder(t *testing.T) {
	rpc, requests := fakeAntigravityRPC(t)
	a, sink := testAgent(t)
	a.rpc = rpc
	receipt, err := a.SendInput("identical prompt", nil)
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
	next, err := a.SendInput("identical prompt", nil)
	require.NoError(t, err)
	assert.NotEqual(t, receipt.InputID, next.InputID)
}

func TestAntigravityExactReplySkipsBlankPlannerRows(t *testing.T) {
	rpc, _ := fakeAntigravityRPC(t)
	a, sink := testAgent(t)
	a.rpc = rpc
	receipt, err := a.SendInput("report readiness", nil)
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
	first, err := a.SendInput("original prompt", nil)
	require.NoError(t, err)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 1, []int{0}, replyInputStep(first.InputID, "first")))
	steer, err := a.SteerInput(agent.SteerRequest{Content: "revision"})
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

	next, err := a.SendInput("continue", nil)
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
	receipt, err := a.SteerChildInput(child.id, agent.SteerRequest{Content: "revision"})
	require.NoError(t, err)
	require.Equal(t, child.id, receipt.SessionID)
	require.NotEmpty(t, receipt.InputID)
	a.fatalCascade(child, fmt.Errorf("stream closed before the child read the steer"))
	assert.Empty(t, sink.NativeReplyRetirements())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.Child(child.childAgentID).NativeReplyRetirements())
}

func latestControlInputBindings(sink *agenttest.Sink) map[string][]string {
	bindings := make(map[string][]string)
	for _, entry := range sink.NativeControlInputBindings() {
		bindings[entry.RequestID] = entry.InputIDs
	}
	return bindings
}

func TestAntigravitySteerUpdatesAlreadyPublishedControlsByNativePosition(t *testing.T) {
	a, sink := testAgent(t)
	rpc, _ := fakeAntigravityRPC(t)
	a.rpc = rpc
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning, TrajectoryID: "trajectory"})
	steer, err := a.SteerInput(agent.SteerRequest{Content: "revised input"})
	require.NoError(t, err)
	permission := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_WAITING","metadata":{"executionId":"execution"},"requestedInteraction":{"permission":{"resource":{"action":"command","target":"printf ready"}}}}`)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 4, []int{1, 3}, permission, permission))
	assert.Empty(t, latestControlInputBindings(sink))

	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 4, []int{2}, replyInputStep(steer.InputID, "execution")))
	bindings := latestControlInputBindings(sink)
	assert.Empty(t, bindings["antigravity:cascade:trajectory:1"])
	assert.Equal(t, []string{steer.InputID}, bindings["antigravity:cascade:trajectory:3"])

	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 5, []int{4}, replyInputStep(steer.InputID, "another execution")))
	assert.Empty(t, latestControlInputBindings(sink)["antigravity:cascade:trajectory:3"])
}

func TestAntigravityChildReplyUsesItsOwnCascade(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	rpc, requests := fakeAntigravityRPC(t)
	a.rpc = rpc
	rootReceipt, err := a.SendInput("identical prompt", nil)
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
			receipt, err := a.SendInput("check readiness", nil)
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
	receipt, err := a.SendInput("check readiness", nil)
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
			receipt, err := a.SendInput("check readiness", nil)
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
	go func() { done <- agenttest.SendInput(a, "first", nil) }()
	receiveTestValue(t, firstSent)
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning})
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	receipt, err := a.SendInput("next", nil)
	require.NoError(t, err)
	refuse()
	require.Error(t, receiveTestValue(t, done))
	_, err = a.SendInput("must stay queued", nil)
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

type failingBindOnceSink struct {
	agent.ProviderServices
	publishCount int
	bindAttempts int
	failBind     bool
}

func (s *failingBindOnceSink) PublishControlRequest(req agent.ControlRequest) error {
	s.publishCount++
	return s.ProviderServices.PublishControlRequest(req)
}

func (s *failingBindOnceSink) SetNativeControlInputs(binding agent.NativeControlInputs) error {
	s.bindAttempts++
	if s.failBind {
		return fmt.Errorf("transient binding failure")
	}
	return s.ProviderServices.SetNativeControlInputs(binding)
}

func TestAntigravitySteerBindingFailureRetriesWithoutRepublishing(t *testing.T) {
	a, sink := testAgent(t)
	rpc, _ := fakeAntigravityRPC(t)
	a.rpc = rpc
	var spy *failingBindOnceSink
	a.root.sink = agent.DecorateProviderServices(a.root.sink, func(direct agent.ProviderServices) agent.ProviderServices {
		spy = &failingBindOnceSink{ProviderServices: direct, failBind: true}
		return spy
	})
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning, TrajectoryID: "trajectory"})
	steer, err := a.SteerInput(agent.SteerRequest{Content: "revised input"})
	require.NoError(t, err)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 3, []int{1}, replyInputStep(steer.InputID, "execution")))
	permission := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_WAITING","metadata":{"executionId":"execution"},"requestedInteraction":{"permission":{"resource":{"action":"command","target":"printf ready"}}}}`)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 3, []int{2}, permission))
	assert.Equal(t, 1, spy.publishCount, "control must be published once")
	assert.Positive(t, spy.bindAttempts, "initial binding attempt must run")
	assert.Empty(t, latestControlInputBindings(sink))

	// Repeated WAITING frame must retry only the failed binding write, without republishing the control.
	spy.failBind = false
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 3, []int{2}, permission))
	assert.Equal(t, 1, spy.publishCount, "retry must not call PublishControlRequest again")
	assert.Equal(t, map[string][]string{"antigravity:cascade:trajectory:2": {steer.InputID}}, latestControlInputBindings(sink))
	assert.False(t, sink.OutputQueue().HasPending(), "output queue must have no pending writes after recovery")

	attemptsAfterRecovery := spy.bindAttempts
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 3, []int{2}, permission))
	assert.Equal(t, 1, spy.publishCount)
	assert.Equal(t, attemptsAfterRecovery, spy.bindAttempts, "a third WAITING frame must not re-run the completed binding")
}

func TestAntigravityControlReplyRejectsMissingOrNullStepIndex(t *testing.T) {
	provider := antigravityProvider{}
	for _, content := range []string{
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory"}}`,
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":null}}`,
	} {
		assert.Empty(t, provider.ControlResponseRequestID([]byte(content)), content)
	}

	a, sink := testControlAgent(t)
	rpc, requests := fakeAntigravityRPC(t)
	a.rpc = rpc
	permission := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_WAITING","requestedInteraction":{"permission":{"resource":{"action":"command","target":"printf ready"}}}}`)
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 1, []int{0}, permission))
	published := sink.PublishedControls()
	require.Len(t, published, 1)

	for _, content := range []string{
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","permission":{"allow":true}}}`,
		`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":null,"permission":{"allow":true}}}`,
	} {
		err := a.executeControlReply([]byte(content), published[0].InstanceID)
		require.Error(t, err, content)
		assert.NotErrorIs(t, err, agent.ErrControlRequestNotPending, content)
		assert.ErrorContains(t, err, "malformed interaction address", content)
	}
	assert.Empty(t, requests("HandleCascadeUserInteraction"))
}

func TestAntigravityReplyTextReturnsFalseForBlankPlannerStep(t *testing.T) {
	step := replyPlannerStep("execution", "   \n\t", contracts.AntigravityStepStatusDone)
	text, ok := (antigravityProvider{}).ReplyText(agent.MessageContent{Original: step})
	assert.False(t, ok)
	assert.Empty(t, text)
}

func TestAntigravityFinishCascadeOutputRetiresIdlePendingReplies(t *testing.T) {
	a, sink := testAgent(t)
	receipt := agent.NativeInputReceipt{SessionID: a.root.id, InputID: "idle-input-1"}
	a.Mu.Lock()
	a.root.turnPhase = cascadeTurnIdle
	a.root.inputReplies = map[string]*inputReply{receipt.InputID: {receipt: receipt}}
	a.Mu.Unlock()
	require.NoError(t, a.finishCascadeOutput(a.root, agent.MessageCompletionError))
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
}

type failingTurnEndSink struct {
	agent.ProviderServices
	err error
}

func (s *failingTurnEndSink) PersistTurnEnd(agent.MessageContent, agent.SpanInfo) error {
	return s.err
}

func TestAntigravityFinishCascadeOutputActiveErrorRetiresPendingReplies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failStep bool
	}{
		{name: "PersistTurnEnd error"},
		{name: "flushOpenSteps error", failStep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink := testAgent(t)
			persistErr := fmt.Errorf("storage unavailable")
			if tc.failStep {
				admission := &transcriptAdmissionSink{ProviderServices: a.root.sink}
				admission.refuse.Store(true)
				a.root.sink = admission
				a.root.steps[0] = &stepState{
					status: contracts.AntigravityStepStatusGenerating,
					raw:    plannerStep(contracts.AntigravityStepStatusGenerating, "partial"),
				}
				a.root.turnSteps[0] = struct{}{}
			} else {
				a.root.sink = &failingTurnEndSink{ProviderServices: a.root.sink, err: persistErr}
			}
			receipt := agent.NativeInputReceipt{SessionID: a.root.id, InputID: "active-input-1"}
			a.Mu.Lock()
			a.root.turnPhase = cascadeTurnActive
			a.root.inputReplies = map[string]*inputReply{receipt.InputID: {receipt: receipt}}
			a.Mu.Unlock()

			err := a.finishCascadeOutput(a.root, agent.MessageCompletionError)
			require.Error(t, err)
			if !tc.failStep {
				assert.ErrorIs(t, err, persistErr)
			} else {
				assert.ErrorContains(t, err, "transcript admission is unavailable")
			}
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
				receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			}, sink.NativeReplyRetirements())
			a.Mu.Lock()
			assert.Empty(t, a.root.inputReplies)
			a.Mu.Unlock()
		})
	}
}

func TestAntigravitySettleInputRepliesDeterministicOrder(t *testing.T) {
	state := newCascadeState("cascade", nil, 0, false)
	state.trajectoryID = "trajectory"
	state.stepCount = 6
	for i := 0; i < 6; i++ {
		state.turnSteps[i] = struct{}{}
	}
	state.steps[0] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true, executionID: "exec-b"}
	state.steps[1] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true, executionID: "exec-b", plannerResponse: true, replyText: true}
	state.steps[2] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true, executionID: "exec-a"}
	state.steps[3] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true, executionID: "exec-a"}
	state.steps[4] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true, executionID: "exec-a", plannerResponse: true, replyText: true}
	state.steps[5] = &stepState{status: contracts.AntigravityStepStatusDone, consumed: true, executionID: "exec-b"}

	state.inputReplies = map[string]*inputReply{
		"unproved-z":  {receipt: agent.NativeInputReceipt{SessionID: "cascade", InputID: "unproved-z"}},
		"unproved-a":  {receipt: agent.NativeInputReceipt{SessionID: "cascade", InputID: "unproved-a"}},
		"exec-b-late": {receipt: agent.NativeInputReceipt{SessionID: "cascade", InputID: "exec-b-late"}, trajectoryID: "trajectory", executionID: "exec-b", inputIndex: 5},
		"exec-b-lead": {receipt: agent.NativeInputReceipt{SessionID: "cascade", InputID: "exec-b-lead"}, trajectoryID: "trajectory", executionID: "exec-b", inputIndex: 0},
		"exec-a-2":    {receipt: agent.NativeInputReceipt{SessionID: "cascade", InputID: "exec-a-2"}, trajectoryID: "trajectory", executionID: "exec-a", inputIndex: 3, steered: true},
		"exec-a-1":    {receipt: agent.NativeInputReceipt{SessionID: "cascade", InputID: "exec-a-1"}, trajectoryID: "trajectory", executionID: "exec-a", inputIndex: 2},
	}

	settlements := state.settleInputReplies(true, agent.MessageCompletionComplete)
	require.Len(t, settlements, 3)

	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, settlements[0].outcome)
	require.Len(t, settlements[0].receipts, 2)
	assert.Equal(t, "unproved-a", settlements[0].receipts[0].receipt.InputID)
	assert.Equal(t, "unproved-z", settlements[0].receipts[1].receipt.InputID)

	assert.Equal(t, "exec-a", settlements[1].turn.TurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, settlements[1].outcome)
	assert.Equal(t, []agent.NativeReplyBoundary{
		{InputID: "exec-a-1", AfterOrdinal: 3},
		{InputID: "exec-a-2", AfterOrdinal: 4},
	}, settlements[1].turn.Inputs)

	assert.Equal(t, "exec-b", settlements[2].turn.TurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, settlements[2].outcome)
	assert.Equal(t, []agent.NativeReplyBoundary{
		{InputID: "exec-b-lead", AfterOrdinal: 1},
		{InputID: "exec-b-late", AfterOrdinal: 6},
	}, settlements[2].turn.Inputs)
}

func TestAntigravityLeadAndSteerInFailedOrCanceledExecutionBothRetire(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interrupted bool
		want        leapmuxv1.AgentInputTurnOutcome
	}{
		{name: "failed execution", want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED},
		{name: "canceled execution", interrupted: true, want: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc, _ := fakeAntigravityRPC(t)
			a, sink := testAgent(t)
			a.rpc = rpc
			lead, err := a.SendInput("initial prompt", nil)
			require.NoError(t, err)
			feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 1, []int{0}, replyInputStep(lead.InputID, "execution")))
			steer, err := a.SteerInput(agent.SteerRequest{Content: "follow-up steer"})
			require.NoError(t, err)
			feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 2, []int{1}, replyInputStep(steer.InputID, "execution")))

			finalStep := json.RawMessage(fmt.Sprintf(`{"type":%q,"status":%q,"metadata":{"executionId":"execution"},"errorMessage":{}}`,
				contracts.AntigravityStepTypeErrorMessage, contracts.AntigravityStepStatusError))
			if tc.interrupted {
				require.NoError(t, a.Interrupt(agent.StopContext{}))
				finalStep = replyPlannerStep("execution", "partial", contracts.AntigravityStepStatusCanceled)
			}
			feedUpdate(t, a, inputReplyUpdate(cascadeStatusIdle, "trajectory", 3, []int{2}, finalStep))
			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
				lead:  tc.want,
				steer: tc.want,
			}, sink.NativeReplyRetirements())
		})
	}
}

func TestAntigravityConflictingUnreadSteerRetiresOnNativeIdle(t *testing.T) {
	a, sink := testAgent(t)
	rpc, _ := fakeAntigravityRPC(t)
	a.rpc = rpc
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning, TrajectoryID: "trajectory"})
	steer, err := a.SteerInput(agent.SteerRequest{Content: "revision"})
	require.NoError(t, err)
	// Echo with empty executionId marks the unread steer conflicting while leaving executionID empty.
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusRunning, "trajectory", 1, []int{0}, replyInputStep(steer.InputID, "")))
	feedUpdate(t, a, inputReplyUpdate(cascadeStatusIdle, "trajectory", 1, nil))

	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		steer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
	a.Mu.Lock()
	assert.Empty(t, a.root.inputReplies)
	a.Mu.Unlock()
}

func TestAntigravityFinishCascadeOutputIdleFlushErrorRetiresRepliesAndSkipsReset(t *testing.T) {
	a, sink := testAgent(t)
	admission := &transcriptAdmissionSink{ProviderServices: a.root.sink}
	admission.refuse.Store(true)
	a.root.sink = admission
	receipt := agent.NativeInputReceipt{SessionID: a.root.id, InputID: "idle-flush-err"}
	a.Mu.Lock()
	a.root.turnPhase = cascadeTurnIdle
	a.root.inputReplies = map[string]*inputReply{receipt.InputID: {receipt: receipt}}
	a.root.steps[0] = &stepState{
		status: contracts.AntigravityStepStatusGenerating,
		raw:    plannerStep(contracts.AntigravityStepStatusGenerating, "unflushed"),
	}
	a.Mu.Unlock()

	err := a.finishCascadeOutput(a.root, agent.MessageCompletionError)
	require.ErrorContains(t, err, "transcript admission is unavailable")
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
	assert.NotContains(t, sink.TurnLifecycle(), "reset_spans")
	assert.NotContains(t, sink.ProgressUpdates(), agent.ResetProgress())
}

func TestAntigravityFatalCascadeOnIdleRootAndChildRetiresUnreadSteer(t *testing.T) {
	t.Run("idle root", func(t *testing.T) {
		a, sink := testAgent(t)
		rpc, _ := fakeAntigravityRPC(t)
		a.rpc = rpc
		receipt := agent.NativeInputReceipt{SessionID: a.root.id, InputID: "unread-root-steer"}
		a.Mu.Lock()
		a.root.seenUpdate = true
		a.root.turnPhase = cascadeTurnIdle
		a.root.inputReplies = map[string]*inputReply{receipt.InputID: {receipt: receipt, steered: true}}
		a.Mu.Unlock()

		a.fatalCascade(a.root, fmt.Errorf("stream closed"))
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, sink.NativeReplyRetirements())
	})

	t.Run("idle child", func(t *testing.T) {
		a, sink, child := childLifecycleTestAgent(t)
		rpc, _ := fakeAntigravityRPC(t)
		a.rpc = rpc
		receipt := agent.NativeInputReceipt{SessionID: child.id, InputID: "unread-child-steer"}
		a.Mu.Lock()
		child.seenUpdate = true
		child.turnPhase = cascadeTurnIdle
		child.rowOpen = false
		child.inputReplies = map[string]*inputReply{receipt.InputID: {receipt: receipt, steered: true}}
		a.Mu.Unlock()

		a.fatalCascade(child, fmt.Errorf("child stream closed"))
		assert.Empty(t, sink.NativeReplyRetirements())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, sink.Child(child.childAgentID).NativeReplyRetirements())
	})
}

func TestAntigravityFinishCascadeOutputIdleWhileDiscardingSkipsFlushAndRetirements(t *testing.T) {
	a, sink := testAgent(t)
	receipt := agent.NativeInputReceipt{SessionID: a.root.id, InputID: "discarded-idle-input"}
	a.Mu.Lock()
	a.root.turnPhase = cascadeTurnIdle
	a.root.inputReplies = map[string]*inputReply{receipt.InputID: {receipt: receipt}}
	a.root.steps[0] = &stepState{
		status: contracts.AntigravityStepStatusGenerating,
		raw:    plannerStep(contracts.AntigravityStepStatusGenerating, "should not flush"),
	}
	a.Mu.Unlock()
	a.DiscardOutput()

	require.NoError(t, a.finishCascadeOutput(a.root, agent.MessageCompletionInterrupted))
	assert.Empty(t, transcriptMessages(sink))
	assert.Empty(t, sink.NativeReplyRetirements())
	a.Mu.Lock()
	assert.Empty(t, a.root.inputReplies)
	a.Mu.Unlock()
}
