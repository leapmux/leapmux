package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (a *Agent) prepareIdentifiedPiSteer(sessionID string, request agent.SteerRequest, payload map[string]any, text string) (piInputDispatch, error) {
	inputID := id.Generate()
	resolution := agent.SteerResolution{InputID: request.InputID, AttemptID: request.AttemptID, Receipt: agent.NativeInputReceipt{
		SessionID: sessionID, InputID: inputID,
	}}
	a.Mu.Lock()
	if a.identifiedSteers == nil {
		a.identifiedSteers = make(map[string]agent.SteerResolution)
	}
	a.identifiedSteers[inputID] = resolution
	a.Mu.Unlock()
	payload["message"] = a.steerBridge.Prefix + inputID + "\n" + text
	wait, err := a.beginPiCommand(CommandPrompt, payload)
	if err != nil {
		if !errors.Is(err, agent.ErrDeliveryUncertain) {
			a.takeIdentifiedPiSteer(inputID)
		}
		return piInputDispatch{}, err
	}
	return piInputDispatch{wait: func() error {
		raw, err := wait(a.APITimeout())
		if err != nil {
			var refusal *piCommandRefusal
			if errors.As(err, &refusal) {
				a.takeIdentifiedPiSteer(inputID)
				return err
			}
			return fmt.Errorf("%w: Pi did not confirm steer delivery: %v", agent.ErrDeliveryUncertain, err)
		}
		switch piPromptDisposition(raw) {
		case "queued", "started":
			return nil
		case "handled":
			if handled, ok := a.takeIdentifiedPiSteer(inputID); ok {
				handled.Receipt.TurnID = inputID
				handled.Outcome = agent.SteerOutcomeEchoed
				a.retainPiSteerResolution(handled)
				a.publishPiReply(newPiReplyRun(handled.Receipt, false), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED)
			}
			return nil
		default:
			return fmt.Errorf("%w: Pi returned an unknown steer disposition", agent.ErrDeliveryUncertain)
		}
	}}, nil
}

func (a *Agent) takeIdentifiedPiSteer(inputID string) (agent.SteerResolution, bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	resolution, ok := a.identifiedSteers[inputID]
	delete(a.identifiedSteers, inputID)
	return resolution, ok
}

func (a *Agent) retainPiSteerResolution(resolution agent.SteerResolution) {
	if err := agent.RetainSteerResolution(a.sink, resolution); !agent.OutputRetained(err) {
		slog.Error("record Pi steer resolution", "agent_id", a.AgentID(), "input_id", resolution.InputID, "error", err)
	}
}

// observePiSteerEcho closes a completed segment before the next native user
// message. Steers read together, or while a tool-use segment is open, share its
// result from their respective input positions.
func (a *Agent) observePiSteerEcho(raw []byte) bool {
	var header struct {
		Message map[string]json.RawMessage `json:"message"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return false
	}
	var inputID string
	if encoded, found := header.Message[piSteerMessageIDField]; found {
		_ = json.Unmarshal(encoded, &inputID)
	}
	a.Mu.Lock()
	var completed *piReplyRun
	sessionID := a.sessionHandleLocked()
	completedOutcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	if reply := a.replyRun; reply != nil && (reply.receipt.SessionID != sessionID || reply.stop == contracts.PiStopReasonStop || reply.stop == contracts.PiStopReasonLength) {
		completed = reply
		a.replyRun = nil
		if reply.receipt.SessionID != sessionID {
			completedOutcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
	}
	resolution, found := a.identifiedSteers[inputID]
	if found && resolution.Receipt.SessionID == sessionID {
		delete(a.identifiedSteers, inputID)
		if a.replyRun == nil {
			resolution.Receipt.TurnID = inputID
			a.replyRun = newPiReplyRun(resolution.Receipt, true)
		} else {
			a.replyRun.ordinal++
			a.replyRun.inputs = append(a.replyRun.inputs, agent.NativeReplyBoundary{InputID: inputID, AfterOrdinal: a.replyRun.ordinal})
		}
		reply := a.replyRun
		reply.steered = true
		reply.stop = ""
		resolution.Receipt.TurnID = reply.receipt.TurnID
		resolution.Outcome = agent.SteerOutcomeEchoed
	} else {
		found = false
	}
	a.Mu.Unlock()
	if completed != nil {
		a.publishPiReply(completed, completedOutcome)
	}
	if found {
		a.retainPiSteerResolution(resolution)
	}
	return inputID != ""
}

func (a *Agent) updatePiAssistantBoundary(raw []byte, finished bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	reply := a.piReplyRunLocked()
	if reply == nil {
		return
	}
	var envelope struct {
		Message piAgentEndMessage `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Message.Role != contracts.PiRoleAssistant {
		return
	}
	reply.stop = ""
	if finished {
		reply.stop = envelope.Message.StopReason
	}
}

func (a *Agent) dropIdentifiedPiSteers() {
	a.Mu.Lock()
	pending := a.identifiedSteers
	a.identifiedSteers = nil
	a.Mu.Unlock()
	ids := make([]string, 0, len(pending))
	for inputID := range pending {
		ids = append(ids, inputID)
	}
	slices.Sort(ids)
	for _, inputID := range ids {
		resolution := pending[inputID]
		resolution.Outcome = agent.SteerOutcomeDropped
		a.retainPiSteerResolution(resolution)
	}
}
