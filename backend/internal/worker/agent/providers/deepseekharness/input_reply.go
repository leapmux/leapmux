package deepseekharness

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type harnessReplySubmission struct {
	sink    agent.ProviderServices
	receipt agent.NativeInputReceipt
	steer   agent.SteerRequest
	echoed  bool
}

// Each tracked Session (root or continuable child) runs one native turn at a
// time. The Remote reader applies durable events in sequence under dispatchMu.
type harnessReplyTurn struct {
	sessionID string
	sink      agent.ProviderServices
	id        string
	ordinal   int64
	inputs    []agent.NativeReplyBoundary
	// inputIDs is an immutable snapshot of the input IDs in inputs, replaced
	// whenever bindReplyInput appends a new input so earlier tool calls keep
	// their own slice.
	inputIDs   []string
	items      []agent.NativeReplyItem
	toolInputs map[string][]string
	unproved   bool
}

func harnessTurnID(number *int64) string {
	if number == nil || *number < 0 || *number > maxSessionSequence {
		return ""
	}
	return strconv.FormatInt(*number, 10)
}

// Register before the HTTP request. An echo can reach the WebSocket while
// the HTTP response is still in flight.
func (a *Agent) registerReplySubmission(sink agent.ProviderServices, receipt agent.NativeInputReceipt, steer *agent.SteerRequest) *harnessReplySubmission {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.replySubmissions == nil {
		a.replySubmissions = make(map[string]*harnessReplySubmission)
	}
	submission := &harnessReplySubmission{sink: sink, receipt: receipt}
	if steer != nil {
		submission.steer = agent.SteerRequest{InputID: steer.InputID, AttemptID: steer.AttemptID}
	}
	a.replySubmissions[receipt.InputID] = submission
	return submission
}

// takeReplySubmissionEchoed removes submission from pending tracking and
// reports whether a committed user echo already proved its admission.
func (a *Agent) takeReplySubmissionEchoed(submission *harnessReplySubmission) bool {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	delete(a.replySubmissions, submission.receipt.InputID)
	return submission.echoed
}

func (a *Agent) beginReplyTurn(stream *sessionStream, data json.RawMessage) {
	sessionID := stream.sessionID
	if sessionID == "" {
		return
	}
	if previous := a.replyTurns[sessionID]; previous != nil {
		// The old turn never supplied its matching end.
		a.retireReplyTurn(previous)
		delete(a.replyTurns, sessionID)
	}
	var start struct {
		Turn *int64 `json:"turn"`
	}
	if json.Unmarshal(data, &start) == nil {
		if id := harnessTurnID(start.Turn); id != "" {
			if a.replyTurns == nil {
				a.replyTurns = make(map[string]*harnessReplyTurn)
			}
			a.replyTurns[sessionID] = &harnessReplyTurn{sessionID: sessionID, sink: a.streamSink(stream), id: id}
		}
	}
}

// A durable user/message carries the request ID but no turn number. The open
// turn/start bracket identifies the turn that admitted it.
func (a *Agent) bindReplyInput(stream *sessionStream, requestID string) {
	sessionID := stream.sessionID
	submission := a.replySubmissions[requestID]
	if submission == nil || submission.receipt.SessionID != sessionID {
		return
	}
	delete(a.replySubmissions, requestID)
	submission.echoed = true
	sink := submission.sink
	if sink == nil {
		sink = a.streamSink(stream)
	}
	turn := a.replyTurns[sessionID]
	provedTurn := turn != nil && turn.sessionID == sessionID
	receipt := submission.receipt
	var err error
	if submission.steer.AttemptID != "" {
		if provedTurn {
			receipt.TurnID = turn.id
		}
		err = a.reportSteerResolution(sink, submission.steer, agent.SteerOutcomeEchoed, receipt)
	} else if provedTurn {
		err = sink.BindNativeInputTurn(receipt, turn.id)
	}
	if !agent.OutputRetained(err) {
		slog.Warn("Store the DeepSeek Harness input binding", "agent_id", a.AgentID(), "error", err)
	}
	if !provedTurn || !agent.OutputRetained(err) {
		a.retireReplyInput(sink, receipt)
		return
	}
	turn.ordinal++
	turn.inputs = append(turn.inputs, agent.NativeReplyBoundary{InputID: requestID, AfterOrdinal: turn.ordinal})
	nextIDs := make([]string, len(turn.inputs))
	for i, input := range turn.inputs {
		nextIDs[i] = input.InputID
	}
	turn.inputIDs = nextIDs
}

func (a *Agent) reportSteerResolution(sink agent.ProviderServices, request agent.SteerRequest, outcome agent.SteerOutcome, receipt agent.NativeInputReceipt) error {
	return agent.RetainSteerResolution(sink, agent.SteerResolution{
		InputID: request.InputID, AttemptID: request.AttemptID, Outcome: outcome, Receipt: receipt,
	})
}

func (a *Agent) trackedReplyTurn(sessionID string) *harnessReplyTurn {
	turn := a.replyTurns[sessionID]
	if turn == nil || turn.sessionID != sessionID || len(turn.inputs) == 0 {
		return nil
	}
	return turn
}

func (a *Agent) replyTurnForText(sessionID string, number *int64) *harnessReplyTurn {
	turn := a.trackedReplyTurn(sessionID)
	if turn == nil {
		return nil
	}
	if id := harnessTurnID(number); id != turn.id {
		if id == "" {
			turn.unproved = true
		}
		return nil
	}
	return turn
}

func (turn *harnessReplyTurn) recordText(key string) {
	turn.ordinal++
	turn.items = append(turn.items, agent.NativeReplyItem{Ordinal: turn.ordinal, MessageKey: key})
}

func (a *Agent) recordReplyToolCall(sessionID, callID string, number *int64) {
	turn := a.trackedReplyTurn(sessionID)
	if turn == nil || harnessTurnID(number) != turn.id || turn.toolInputs[callID] != nil {
		return
	}
	inputs := turn.inputIDs
	if turn.toolInputs == nil {
		turn.toolInputs = make(map[string][]string)
	}
	turn.toolInputs[callID] = inputs

	if a.unboundControls.Load() == 0 {
		return
	}

	// The Remote control stream can deliver a request before session/follow
	// delivers its tool call. Attach the proof when both have arrived.
	type controlBinding struct {
		sink    agent.ProviderServices
		binding agent.NativeControlInputs
	}
	var bindings []controlBinding
	a.Mu.Lock()
	controlIDs := make([]string, 0, len(a.controls))
	for id, control := range a.controls {
		if control.agentID == sessionID && control.replyCallID == callID {
			controlIDs = append(controlIDs, id)
		}
	}
	slices.Sort(controlIDs)
	for _, id := range controlIDs {
		control := a.controls[id]
		control.replyInputIDs = inputs
		a.clearControlUnboundLocked(control)
		if binding := control.takeReplyInputsLocked(id); binding.RequestID != "" {
			bindings = append(bindings, controlBinding{sink: control.sink, binding: binding})
		}
	}
	a.Mu.Unlock()
	for _, item := range bindings {
		a.bindReplyControl(item.sink, item.binding)
	}
}

func (a *Agent) forgetReplyToolCall(sessionID, callID string) {
	if turn := a.replyTurns[sessionID]; turn != nil && turn.toolInputs != nil {
		delete(turn.toolInputs, callID)
	}
}

func (a *Agent) replyToolInputs(sessionID, callID string) []string {
	if turn := a.trackedReplyTurn(sessionID); turn != nil {
		return turn.toolInputs[callID]
	}
	return nil
}

// Caller holds a.Mu. Whichever stream completes the proof owns its write.
func (control *nativeControl) takeReplyInputsLocked(id string) agent.NativeControlInputs {
	if !control.published || control.replyBound || len(control.replyInputIDs) == 0 {
		return agent.NativeControlInputs{}
	}
	control.replyBound = true
	return agent.NativeControlInputs{
		SessionID: control.agentID, RequestID: id, InstanceID: control.instanceID, InputIDs: control.replyInputIDs,
	}
}

func (a *Agent) bindReplyControl(sink agent.ProviderServices, binding agent.NativeControlInputs) {
	if binding.RequestID == "" || sink == nil {
		return
	}
	if err := sink.SetNativeControlInputs(binding); !agent.OutputRetained(err) {
		slog.Warn("Store the DeepSeek Harness control's input ownership", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) finishReplyTurn(sessionID string, number *int64, outcome leapmuxv1.AgentInputTurnOutcome) {
	turn := a.replyTurns[sessionID]
	if turn == nil || turn.sessionID != sessionID {
		return
	}
	delete(a.replyTurns, sessionID)
	if len(turn.inputs) == 0 {
		return
	}
	if turn.unproved || harnessTurnID(number) != turn.id || outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		a.retireReplyTurn(turn)
		return
	}
	if err := turn.sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
		SessionID: turn.sessionID, TurnID: turn.id, Outcome: outcome, Items: turn.items, Inputs: turn.inputs,
	}); !agent.OutputRetained(err) {
		slog.Warn("Store the exact DeepSeek Harness reply", "agent_id", a.AgentID(), "error", err)
		a.retireReplyTurn(turn)
	}
}

func (a *Agent) retireReplyInput(sink agent.ProviderServices, receipt agent.NativeInputReceipt) {
	if sink == nil {
		return
	}
	if err := sink.RetireNativeInputTurn(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
		slog.Warn("Store the DeepSeek Harness reply outcome", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireReplyTurn(turn *harnessReplyTurn) {
	for _, input := range turn.inputs {
		a.retireReplyInput(turn.sink, agent.NativeInputReceipt{SessionID: turn.sessionID, InputID: input.InputID, TurnID: turn.id})
	}
}

func (a *Agent) retireUnfinishedReplies() {
	turnKeys := make([]string, 0, len(a.replyTurns))
	for key := range a.replyTurns {
		turnKeys = append(turnKeys, key)
	}
	slices.Sort(turnKeys)
	for _, key := range turnKeys {
		a.retireReplyTurn(a.replyTurns[key])
	}
	clear(a.replyTurns)

	submissionKeys := make([]string, 0, len(a.replySubmissions))
	for key := range a.replySubmissions {
		submissionKeys = append(submissionKeys, key)
	}
	slices.Sort(submissionKeys)
	for _, key := range submissionKeys {
		submission := a.replySubmissions[key]
		if submission.steer.AttemptID == "" {
			a.retireReplyInput(submission.sink, submission.receipt)
		} else if submission.sink != nil {
			if err := a.reportSteerResolution(submission.sink, submission.steer, agent.SteerOutcomeUncertain, agent.NativeInputReceipt{}); !agent.OutputRetained(err) {
				slog.Warn("Store the DeepSeek Harness steer outcome", "agent_id", a.AgentID(), "error", err)
			}
		}
	}
	clear(a.replySubmissions)
	a.clearPendingSteers()
}
