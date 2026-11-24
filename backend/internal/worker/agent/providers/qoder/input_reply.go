package qoder

import (
	"bytes"
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// inputReply tracks one input from submission through the native result.
// Agent.mu protects it.
type inputReply struct {
	receipt    agent.NativeInputReceipt
	steer      *agent.SteerResolution
	started    bool
	echoed     bool
	incomplete bool
	result     *inputResult
	toolCalls  map[string]struct{}
}

func (reply *inputReply) owns(sessionID string) bool {
	return reply != nil && reply.receipt.SessionID == sessionID && reply.started && reply.echoed && !reply.incomplete
}

type inputResult struct {
	raw         []byte
	isError     *bool
	interrupted bool
}

const (
	commandStarted   = "started"
	commandCompleted = "completed"
	commandCancelled = "cancelled"
	commandDiscarded = "discarded"
)

// outcome uses the native command's end as well as the result inside it.
func (result *inputResult) outcome(state string) leapmuxv1.AgentInputTurnOutcome {
	if result == nil || result.isError == nil {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
	switch {
	case state == commandCancelled && result.interrupted:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case *result.isError && (state == commandCompleted || state == commandCancelled):
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	case state == commandCompleted && !*result.isError:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	default:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
}

// observeInputEcho accepts the replay of the submitted root user message.
func (a *Agent) observeInputEcho(raw []byte) {
	var echo struct {
		SessionID string `json:"session_id"`
		UUID      string `json:"uuid"`
		Replay    bool   `json:"isReplay"`
		Synthetic bool   `json:"isSynthetic"`
	}
	if json.Unmarshal(raw, &echo) != nil || !echo.Replay || echo.UUID == "" {
		return
	}
	a.mu.Lock()
	reply := a.inputReply
	if reply == nil || !reply.started || reply.receipt.SessionID != echo.SessionID {
		a.mu.Unlock()
		return
	}
	if reply.receipt.InputID != echo.UUID || echo.Synthetic {
		reply.incomplete = true
		a.mu.Unlock()
		return
	}
	if reply.echoed {
		a.mu.Unlock()
		return
	}
	reply.echoed = true
	a.mu.Unlock()
	if reply.steer != nil && !a.resolveQueuedSteer(reply, agent.SteerOutcomeEchoed) {
		a.mu.Lock()
		reply.incomplete = true
		a.mu.Unlock()
		a.resolveQueuedSteer(reply, agent.SteerOutcomeUncertain)
	}
}

// scopeInputControl attaches a blocking tool request to its proved root input.
func (a *Agent) scopeInputControl(control *agent.ControlRequest, envelope controlRequestEnvelope) {
	var request struct {
		ToolUseID string `json:"tool_use_id"`
	}
	if json.Unmarshal(envelope.Request, &request) != nil || request.ToolUseID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	reply := a.inputReply
	if !reply.owns(a.sessionID) || reply.result != nil || (envelope.SessionID != "" && envelope.SessionID != a.sessionID) {
		return
	}
	if _, ok := reply.toolCalls[request.ToolUseID]; ok {
		control.AgentSessionID, control.NativeTurnID = reply.receipt.SessionID, reply.receipt.TurnID
	}
}

// observeToolCalls records the tool IDs of an owned root assistant frame.
// The caller holds Agent.mu.
func (reply *inputReply) observeToolCalls(content json.RawMessage) {
	var blocks []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return
	}
	for _, block := range blocks {
		if block.Type == "tool_use" && block.ID != "" {
			if reply.toolCalls == nil {
				reply.toolCalls = make(map[string]struct{})
			}
			reply.toolCalls[block.ID] = struct{}{}
		}
	}
}

// handleCommandLifecycle follows the command UUID, which is separate from the
// UUID that Qoder assigns to each output frame. A queued command does not own
// output. Qoder emits the result before the matching command completion.
func (a *Agent) handleCommandLifecycle(raw []byte) {
	var frame struct {
		SessionID string `json:"session_id"`
		CommandID string `json:"command_uuid"`
		State     string `json:"state"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.CommandID == "" {
		return
	}
	if a.handleQueuedSteer(frame.SessionID, frame.CommandID, frame.State) {
		return
	}
	a.mu.Lock()
	reply := a.inputReply
	if reply == nil || reply.receipt.SessionID != frame.SessionID {
		a.mu.Unlock()
		return
	}
	ownCommand := frame.CommandID == reply.receipt.InputID
	switch frame.State {
	case commandStarted:
		if ownCommand {
			reply.started = true
			a.mu.Unlock()
			return
		}
		// A second start can share a native query. A result can prove
		// ownership only while the command is the sole known owner.
		if reply.started {
			reply.incomplete = true
		}
		if reply.result == nil {
			a.mu.Unlock()
			return
		}
	case commandCompleted, commandCancelled, commandDiscarded:
		if !ownCommand {
			a.mu.Unlock()
			return
		}
	default:
		a.mu.Unlock()
		return
	}
	a.inputReply = nil
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	if reply.owns(frame.SessionID) {
		outcome = reply.result.outcome(frame.State)
	}
	a.mu.Unlock()
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		a.finishUnprovedInput(reply)
	} else if !a.persistInputResult(*reply.result, reply.receipt, outcome) {
		a.retireInputReply(reply)
	}
	a.setTurnActive(false)
}

// holdInputResult saves a candidate until Qoder reports the command's outcome.
// A second result in the same interval makes the association ambiguous. Return
// the earlier candidate so the transcript can still retain both in order.
func (a *Agent) holdInputResult(sessionID string, result inputResult) (bool, *inputReply) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reply := a.inputReply
	if reply == nil {
		return false, nil
	}
	if reply.result != nil {
		a.inputReply = nil
		return false, reply
	}
	if !reply.owns(sessionID) {
		return false, nil
	}
	result.raw = bytes.Clone(result.raw)
	reply.result = &result
	return true, nil
}

func (a *Agent) finishUnprovedInput(reply *inputReply) {
	if reply == nil {
		return
	}
	if reply.result != nil {
		a.persistInputResult(*reply.result, agent.NativeInputReceipt{}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
	}
	if reply.steer != nil && !reply.echoed {
		a.resolveQueuedSteer(reply, agent.SteerOutcomeUncertain)
		return
	}
	a.retireInputReply(reply)
}

func (a *Agent) retireInputReply(reply *inputReply) {
	if err := a.sink.RetireNativeInputTurn(reply.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
		slog.Warn("Qoder could not retire the input reply", "agent_id", a.AgentID(), "error", err)
	}
}
