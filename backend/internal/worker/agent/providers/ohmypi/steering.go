package ohmypi

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const ompAssistantStop = "stop"

func (a *Agent) prepareOMPSteer(sessionID string, request agent.SteerRequest, payload map[string]any) agent.SteerResolution {
	inputID := id.Generate()
	payload["message"] = a.steerBridge.Prefix + inputID + "\n" + payload["message"].(string)
	return agent.SteerResolution{
		InputID: request.InputID, AttemptID: request.AttemptID,
		Receipt: agent.NativeInputReceipt{SessionID: sessionID, InputID: inputID, TurnID: inputID},
	}
}

func (a *Agent) registerOMPSteer(steer agent.SteerResolution) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.identifiedSteers == nil {
		a.identifiedSteers = make(map[string]agent.SteerResolution)
	}
	a.identifiedSteers[steer.Receipt.InputID] = steer
}

func (a *Agent) takeOMPSteer(id string) (agent.SteerResolution, bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	steer, found := a.identifiedSteers[id]
	delete(a.identifiedSteers, id)
	return steer, found
}

func (a *Agent) reportOMPSteer(steer agent.SteerResolution) {
	a.reportOMPSteerTo(a.sink, steer)
}

func (a *Agent) reportOMPSteerTo(sink agent.ProviderServices, steer agent.SteerResolution) {
	if err := agent.RetainSteerResolution(sink, steer); !agent.OutputRetained(err) {
		slog.Error("record the Oh My Pi steer outcome", "agent_id", a.AgentID(), "input_id", steer.InputID, "error", err)
	}
}

func (a *Agent) observeOMPSteerAdmission(id string, raw []byte) {
	var response rpcResponse
	if json.Unmarshal(raw, &response) == nil && response.Command == CommandPrompt && response.Success && !promptInvokesAgent(response.Data) {
		a.completeLocalOMPSteer(id)
	}
}

func (a *Agent) completeLocalOMPSteer(id string) {
	if steer, ok := a.takeOMPSteer(id); ok {
		a.dropSteer(id)
		steer.Outcome = agent.SteerOutcomeEchoed
		a.reportOMPSteer(steer)
		a.publishOMPReply(newOMPReplyPrompt(steer.Receipt), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED)
	}
}

func (a *Agent) resolveUnreadOMPSteer(id string, outcome agent.SteerOutcome) {
	if steer, ok := a.takeOMPSteer(id); ok {
		a.dropSteer(id)
		steer.Outcome = outcome
		steer.Receipt = agent.NativeInputReceipt{}
		a.reportOMPSteer(steer)
	}
}

// observeOMPUserMessage closes a completed assistant reply when another user
// message arrives. Several inputs may share the same reply when omp reads
// them together or reads them during tool use. It reports whether inputID
// matched a tracked identified steer.
func (a *Agent) observeOMPUserMessage(inputID string) bool {
	a.Mu.Lock()
	sessionID := a.sessionHandleLocked()
	reply := a.replyOutputLocked()
	var finished *ompReplyPrompt
	if reply != nil && reply.completedAnswer() {
		finished = a.takeReplyPromptLocked(reply.receipt.InputID)
		reply = nil
	}
	steer, found := a.identifiedSteers[inputID]
	found = found && (steer.Receipt.SessionID == sessionID || (a.sessionID != "" && steer.Receipt.SessionID == a.sessionID))
	if found {
		delete(a.identifiedSteers, inputID)
		if reply == nil {
			if sessionID != "" {
				steer.Receipt.SessionID = sessionID
			}
			reply = newOMPReplyPrompt(steer.Receipt)
			a.registerReplyPromptLocked(reply)
			a.replyOutput = reply
		} else {
			reply.appendInput(inputID)
			steer.Receipt.SessionID = reply.receipt.SessionID
		}
		reply.steered = true
		reply.stop = ""
		steer.Receipt.TurnID = reply.receipt.TurnID
		steer.Outcome = agent.SteerOutcomeEchoed
	}
	a.Mu.Unlock()
	if finished != nil {
		if finished.unproved {
			a.retireOMPReplyInputs(finished, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		} else {
			a.publishOMPReply(finished, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED)
		}
	}
	if found {
		a.dropSteer(inputID)
		a.reportOMPSteer(steer)
	}
	return found
}

func (a *Agent) observeOMPAssistantBoundary(c *conversation, raw []byte, finished bool) {
	var frame messageEndEnvelope
	if json.Unmarshal(raw, &frame) != nil || frame.Message.Role != contracts.OhMyPiRoleAssistant {
		return
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if reply := a.conversationReplyLocked(c); reply != nil {
		reply.stop = ""
		if finished {
			reply.stop = frame.Message.StopReason
		}
	}
}
