package deepseekharness

import (
	"encoding/json"
	"log/slog"
	"strconv"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type harnessReplySubmission struct {
	receipt agent.NativeInputReceipt
	echoed  bool
}

// A root Session runs one native turn at a time. The Remote reader applies its
// durable events in sequence under dispatchMu.
type harnessReplyTurn struct {
	sessionID  string
	id         string
	ordinal    int64
	inputs     []agent.NativeReplyBoundary
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
func (a *Agent) registerReplySubmission(receipt agent.NativeInputReceipt) *harnessReplySubmission {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.replySubmissions == nil {
		a.replySubmissions = make(map[string]*harnessReplySubmission)
	}
	submission := &harnessReplySubmission{receipt: receipt}
	a.replySubmissions[receipt.InputID] = submission
	return submission
}

// A committed user echo proves admission even if the HTTP result is lost.
func (a *Agent) replySubmissionEchoed(submission *harnessReplySubmission) bool {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	delete(a.replySubmissions, submission.receipt.InputID)
	return submission.echoed
}

func (a *Agent) beginReplyTurn(sessionID string, data json.RawMessage) {
	if sessionID != a.session() {
		return
	}
	if a.replyTurn != nil {
		// The old turn never supplied its matching end.
		a.retireReplyTurn(a.replyTurn)
		a.replyTurn = nil
	}
	var start struct {
		Turn *int64 `json:"turn"`
	}
	if json.Unmarshal(data, &start) == nil {
		if id := harnessTurnID(start.Turn); id != "" {
			a.replyTurn = &harnessReplyTurn{sessionID: sessionID, id: id}
		}
	}
}

// A durable user/message carries the request ID but no turn number. The open
// turn/start bracket identifies the turn that admitted it.
func (a *Agent) bindReplyInput(sessionID, requestID string) {
	submission := a.replySubmissions[requestID]
	if submission == nil || submission.receipt.SessionID != sessionID {
		return
	}
	delete(a.replySubmissions, requestID)
	submission.echoed = true
	turn := a.replyTurn
	if turn == nil || turn.sessionID != sessionID {
		a.retireReplyInput(submission.receipt)
		return
	}
	if err := a.sink.BindNativeInputTurn(submission.receipt, turn.id); !agent.OutputRetained(err) {
		slog.Warn("Store the DeepSeek Harness input binding", "agent_id", a.AgentID(), "error", err)
		a.retireReplyInput(submission.receipt)
		return
	}
	turn.ordinal++
	turn.inputs = append(turn.inputs, agent.NativeReplyBoundary{InputID: requestID, AfterOrdinal: turn.ordinal})
}

func (a *Agent) trackedReplyTurn(sessionID string) *harnessReplyTurn {
	turn := a.replyTurn
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
	inputs := make([]string, 0, len(turn.inputs))
	for _, input := range turn.inputs {
		inputs = append(inputs, input.InputID)
	}
	if turn.toolInputs == nil {
		turn.toolInputs = make(map[string][]string)
	}
	turn.toolInputs[callID] = inputs

	// The Remote control stream can deliver a request before session/follow
	// delivers its tool call. Attach the proof when both have arrived.
	var bindings []agent.NativeControlInputs
	a.Mu.Lock()
	for id, control := range a.controls {
		if control.agentID == sessionID && control.replyCallID == callID {
			control.replyInputIDs = inputs
			if binding := control.takeReplyInputsLocked(id); binding.RequestID != "" {
				bindings = append(bindings, binding)
			}
		}
	}
	a.Mu.Unlock()
	for _, binding := range bindings {
		a.bindReplyControl(binding)
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

func (a *Agent) bindReplyControl(binding agent.NativeControlInputs) {
	if binding.RequestID == "" {
		return
	}
	if err := a.sink.SetNativeControlInputs(binding); !agent.OutputRetained(err) {
		slog.Warn("Store the DeepSeek Harness control's input ownership", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) finishReplyTurn(sessionID string, number *int64, reason string) {
	turn := a.replyTurn
	if turn == nil || turn.sessionID != sessionID {
		return
	}
	a.replyTurn = nil
	if len(turn.inputs) == 0 {
		return
	}
	outcome := harnessReplyOutcome(reason)
	if turn.unproved || harnessTurnID(number) != turn.id || outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		a.retireReplyTurn(turn)
		return
	}
	if err := a.sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
		SessionID: turn.sessionID, TurnID: turn.id, Outcome: outcome, Items: turn.items, Inputs: turn.inputs,
	}); !agent.OutputRetained(err) {
		slog.Warn("Store the exact DeepSeek Harness reply", "agent_id", a.AgentID(), "error", err)
		a.retireReplyTurn(turn)
	}
}

func harnessReplyOutcome(reason string) leapmuxv1.AgentInputTurnOutcome {
	switch reason {
	case "completed", "max-tokens":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case "aborted", "interrupted":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case "blocked", "error":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
}

func (a *Agent) retireReplyInput(receipt agent.NativeInputReceipt) {
	if err := a.sink.RetireNativeInputTurn(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
		slog.Warn("Store the DeepSeek Harness reply outcome", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireReplyTurn(turn *harnessReplyTurn) {
	for _, input := range turn.inputs {
		a.retireReplyInput(agent.NativeInputReceipt{SessionID: turn.sessionID, InputID: input.InputID, TurnID: turn.id})
	}
}

func (a *Agent) retireUnfinishedReplies() {
	if a.replyTurn != nil {
		a.retireReplyTurn(a.replyTurn)
		a.replyTurn = nil
	}
	for _, submission := range a.replySubmissions {
		a.retireReplyInput(submission.receipt)
	}
	clear(a.replySubmissions)
}
