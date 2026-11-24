package ohmypi

import (
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type ompChildInput struct {
	receipt agent.NativeInputReceipt
	steer   agent.SteerResolution
}

// Caller holds Mu.
func (a *Agent) registerOMPChildInputLocked(c *conversation, request agent.SteerRequest, steer bool) agent.NativeInputReceipt {
	receipt := agent.NativeInputReceipt{SessionID: c.sessionFile, InputID: id.Generate()}
	input := ompChildInput{receipt: receipt}
	if steer {
		input.steer = agent.SteerResolution{InputID: request.InputID, AttemptID: request.AttemptID}
	}
	if c.childInputs == nil {
		c.childInputs = make(map[string]ompChildInput)
	}
	c.childInputs[receipt.InputID] = input
	return receipt
}

func (a *Agent) forgetOMPChildInput(c *conversation, inputID string) {
	a.Mu.Lock()
	delete(c.childInputs, inputID)
	a.Mu.Unlock()
}

func (a *Agent) observeOMPChildUserMessage(c *conversation, inputID string) {
	a.Mu.Lock()
	var completed *ompReplyPrompt
	if c.childReply != nil && c.childReply.completedAnswer() {
		completed, c.childReply = c.childReply, nil
	}
	input, found := c.childInputs[inputID]
	var turnID string
	if found {
		delete(c.childInputs, inputID)
		if c.childReply == nil {
			receipt := input.receipt
			receipt.TurnID = receipt.InputID
			c.childReply = newOMPReplyPrompt(receipt)
		} else {
			c.childReply.appendInput(inputID)
		}
		c.childReply.stop = ""
		turnID = c.childReply.receipt.TurnID
	}
	a.Mu.Unlock()
	if completed != nil {
		a.publishOMPChildReply(c, completed, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED)
	}
	if !found {
		return
	}
	if input.steer.InputID != "" {
		input.steer.Outcome = agent.SteerOutcomeEchoed
		input.steer.Receipt = input.receipt
		input.steer.Receipt.TurnID = turnID
		a.reportOMPSteerTo(c.sink, input.steer)
		return
	}
	if err := c.sink.BindNativeInputTurn(input.receipt, turnID); !agent.OutputRetained(err) {
		slog.Error("bind the Oh My Pi child input", "agent_id", a.AgentID(), "child_agent_id", c.childID, "error", err)
	}
}

func (a *Agent) finishOMPChildReplyAtYield(c *conversation, env agentEndEnvelope, end bool) {
	yielded := env.Yielded != nil && *env.Yielded ||
		end && env.Yielded == nil && (env.IsTerminal == nil || *env.IsTerminal)
	if !yielded {
		return
	}
	a.Mu.Lock()
	reply := c.childReply
	c.childReply = nil
	a.Mu.Unlock()
	if reply == nil {
		return
	}
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	for i := len(env.Messages) - 1; i >= 0; i-- {
		message := env.Messages[i]
		if message.Role != contracts.OhMyPiRoleAssistant {
			continue
		}
		switch message.StopReason {
		case ompAssistantStop, contracts.OhMyPiStopReasonLength, "toolUse":
			if reply.stop == message.StopReason {
				outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
			}
		case contracts.OhMyPiStopReasonAborted:
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
		case contracts.OhMyPiStopReasonError:
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
		break
	}
	a.publishOMPChildReply(c, reply, outcome)
}

func (a *Agent) publishOMPChildReply(c *conversation, reply *ompReplyPrompt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if reply.unproved || outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
		a.retireOMPReplyInputsOn(c.sink, reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		return
	}
	a.publishOMPReplyTo(c.sink, reply, outcome)
}

func (a *Agent) retireOMPChildInputs(c *conversation) {
	a.Mu.Lock()
	reply, pending := c.childReply, c.childInputs
	c.childReply, c.childInputs = nil, nil
	a.Mu.Unlock()
	if reply != nil {
		a.retireOMPReplyInputsOn(c.sink, reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	for _, input := range pending {
		if input.steer.InputID == "" {
			a.retireOMPReply(c.sink, input.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
			continue
		}
		input.steer.Outcome = agent.SteerOutcomeUncertain
		a.reportOMPSteerTo(c.sink, input.steer)
	}
}
