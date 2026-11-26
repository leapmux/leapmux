package qoder

import (
	"bytes"
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
	a.mu.Lock()
	waiting := a.inputReply != nil && a.inputReply.started
	a.mu.Unlock()
	if !waiting || !bytes.Contains(raw, []byte(`"isReplay"`)) {
		return
	}
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

// scopeInputControl attaches a blocking root control request to its proved input.
// Subagent requests carry request.agent_id and stay unscoped on the root tab.
func (a *Agent) scopeInputControl(control *agent.ControlRequest, envelope controlRequestEnvelope) {
	fields, ok := providerkit.RawObject(envelope.Request)
	if !ok {
		return
	}
	if rawAgentID := fields["agent_id"]; providerkit.RawPresent(rawAgentID) {
		agentID, isString := providerkit.RawString(rawAgentID)
		if !isString || agentID != "" {
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	reply := a.inputReply
	if !reply.owns(a.sessionID) || reply.result != nil || (envelope.SessionID != "" && envelope.SessionID != a.sessionID) {
		return
	}
	control.AgentSessionID, control.NativeTurnID = reply.receipt.SessionID, reply.receipt.TurnID
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
	if frame.State == commandStarted {
		a.interruptRequested = false
	}
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
	if frame.State != commandStarted {
		a.setTurnActive(false)
	}
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

// abandonInputReply rolls back an input whose receipt was never returned to the
// caller. It drops the unreturned receipt without calling RetireNativeInputTurn
// and still flushes any result that arrived before the rollback.
func (a *Agent) abandonInputReply(reply *inputReply) {
	a.mu.Lock()
	var heldResult *inputResult
	if reply == nil {
		if a.inputReply == nil {
			a.active = false
			a.interruptRequested = false
		}
	} else if a.inputReply == reply {
		heldResult = reply.result
		a.inputReply = nil
		a.active = false
		a.interruptRequested = false
	}
	active := a.active
	a.activityRev++
	seq := a.turnOrder.NextTurnSeq()
	a.mu.Unlock()
	if heldResult != nil {
		a.persistInputResult(*heldResult, agent.NativeInputReceipt{}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
	}
	providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: active}, seq)
}

func (a *Agent) finishUnprovedInput(reply *inputReply) {
	a.finishUnprovedReply(reply, true)
}

func (a *Agent) finishUnprovedInputOnExit(reply *inputReply) {
	a.finishUnprovedReply(reply, false)
}

func (a *Agent) finishUnprovedReply(reply *inputReply, refreshUsage bool) {
	if reply == nil {
		return
	}
	if reply.result != nil {
		if refreshUsage {
			a.persistInputResult(*reply.result, agent.NativeInputReceipt{}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
		} else {
			a.persistTurnEndResult(*reply.result, agent.NativeInputReceipt{}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
		}
	}
	if reply.steer != nil && !reply.echoed {
		a.resolveQueuedSteer(reply, agent.SteerOutcomeUncertain)
		return
	}
	a.retireInputReply(reply)
}

func (a *Agent) retireInputReply(reply *inputReply) {
	if a.sink == nil {
		return
	}
	if err := a.sink.RetireNativeInputTurn(reply.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
		slog.Warn("Qoder could not retire the input reply", "agent_id", a.AgentID(), "error", err)
	}
}
