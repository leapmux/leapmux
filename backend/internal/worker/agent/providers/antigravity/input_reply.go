package antigravity

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The language server echoes the client message ID in a user-input step.
// Only that step identifies the execution that received the input.
type inputReply struct {
	receipt      agent.NativeInputReceipt
	trajectoryID string
	executionID  string
	inputIndex   int
	conflicting  bool
	steered      bool
}

func (a *Agent) bindInputEcho(state *cascadeState, index int, step antigravityStep) error {
	if step.Status != contracts.AntigravityStepStatusDone || step.UserInput.ClientMessageID == "" {
		return nil
	}
	a.Mu.Lock()
	reply := state.inputReplies[step.UserInput.ClientMessageID]
	if !a.cascadeCurrentLocked(state) || state.closed || reply == nil || reply.conflicting {
		a.Mu.Unlock()
		return nil
	}
	executionID, trajectoryID := step.Metadata.ExecutionID, state.trajectoryID
	if executionID == "" || trajectoryID == "" || reply.executionID != "" &&
		(reply.executionID != executionID || reply.trajectoryID != trajectoryID || reply.inputIndex != index) {
		reply.conflicting = true
		previousExecution := reply.executionID
		a.Mu.Unlock()
		if reply.steered && previousExecution != "" {
			a.refreshSteerControlInputs(state, previousExecution)
		}
		return nil
	}
	reply.trajectoryID, reply.executionID, reply.inputIndex = trajectoryID, executionID, index
	receipt := reply.receipt
	a.Mu.Unlock()
	if reply.steered {
		// The Worker accepts the steer before the native echo. Its transcript
		// position can precede text already produced by the older prompt.
		// The native step positions determine its reply at settlement.
		a.refreshSteerControlInputs(state, executionID)
		return nil
	}
	if err := state.sink.BindNativeInputTurn(receipt, executionID); !agent.OutputRetained(err) {
		return fmt.Errorf("bind the Antigravity input to its execution: %w", err)
	}
	return nil
}

type inputReplySettlement struct {
	turn     agent.NativeReplyTurn
	receipts []*inputReply
	outcome  leapmuxv1.AgentInputTurnOutcome
}

func (a *Agent) finishInputReplies(state *cascadeState, nativeIdle bool, completion agent.MessageCompletion) {
	a.Mu.Lock()
	if a.IsDiscardingOutput() {
		clear(state.inputReplies)
		a.Mu.Unlock()
		return
	}
	settlements := state.settleInputReplies(nativeIdle && a.cascadeCurrentLocked(state) && !state.closed, completion)
	a.Mu.Unlock()
	for _, settlement := range settlements {
		outcome := settlement.outcome
		if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
			err := state.sink.PublishNativeReplyTurn(settlement.turn)
			if agent.OutputRetained(err) {
				continue
			}
			slog.Warn("Antigravity could not store the exact reply", "agent_id", a.AgentID(), "error", err)
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
		for _, reply := range settlement.receipts {
			if err := state.sink.RetireNativeInputTurn(reply.receipt, outcome); !agent.OutputRetained(err) {
				slog.Warn("Antigravity could not retire the input reply", "agent_id", a.AgentID(), "error", err)
			}
		}
	}
}

func (state *cascadeState) settleInputReplies(nativeIdle bool, completion agent.MessageCompletion) []inputReplySettlement {
	var settlements []inputReplySettlement
	var unproved []*inputReply
	byExecution := make(map[string][]*inputReply)
	for _, inputID := range slices.Sorted(maps.Keys(state.inputReplies)) {
		reply := state.inputReplies[inputID]
		// An interrupt leaves unread steers in Antigravity's queue. A later
		// prompt can start the execution that consumes them.
		if nativeIdle && reply.steered && reply.executionID == "" && !reply.conflicting {
			continue
		}
		delete(state.inputReplies, inputID)
		if !nativeIdle || reply.conflicting || reply.executionID == "" || reply.trajectoryID != state.trajectoryID {
			unproved = append(unproved, reply)
			continue
		}
		byExecution[reply.executionID] = append(byExecution[reply.executionID], reply)
	}
	if len(unproved) > 0 {
		settlements = append(settlements, inputReplySettlement{
			receipts: unproved, outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		})
	}
	for _, executionID := range slices.Sorted(maps.Keys(byExecution)) {
		replies := byExecution[executionID]
		slices.SortFunc(replies, func(left, right *inputReply) int { return cmp.Compare(left.inputIndex, right.inputIndex) })
		turn, outcome := state.projectInputReply(replies[0], completion)
		if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
			for _, reply := range replies[1:] {
				turn.Inputs = append(turn.Inputs, inputReplyBoundary(reply))
			}
		}
		settlements = append(settlements, inputReplySettlement{turn: turn, receipts: replies, outcome: outcome})
	}
	return settlements
}

// A completed execution needs every position after its echo through the
// last reported trajectory length. The stream can spread those positions
// across patches and deliver them out of order.
func (state *cascadeState) projectInputReply(reply *inputReply, completion agent.MessageCompletion) (agent.NativeReplyTurn, leapmuxv1.AgentInputTurnOutcome) {
	unproved := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	if completion == agent.MessageCompletionError {
		step, err := decodeStep(state.heldErrorStep)
		if err == nil && step.Metadata.ExecutionID == reply.executionID {
			return agent.NativeReplyTurn{}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
		return agent.NativeReplyTurn{}, unproved
	}
	echo := state.steps[reply.inputIndex]
	if _, ok := state.turnSteps[reply.inputIndex]; !ok || echo == nil || !echo.consumed || reply.inputIndex >= state.stepCount {
		return agent.NativeReplyTurn{}, unproved
	}
	if completion == agent.MessageCompletionInterrupted {
		for index := range state.turnSteps {
			step := state.steps[index]
			if index > reply.inputIndex && step != nil && step.executionID == reply.executionID && step.status == contracts.AntigravityStepStatusCanceled {
				return agent.NativeReplyTurn{}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
			}
		}
		return agent.NativeReplyTurn{}, unproved
	}
	if completion != agent.MessageCompletionComplete {
		return agent.NativeReplyTurn{}, unproved
	}
	var items []agent.NativeReplyItem
	seen := 0
	for index := range state.turnSteps {
		if index <= reply.inputIndex {
			continue
		}
		step := state.steps[index]
		if step == nil || step.status == "" || index >= state.stepCount {
			return agent.NativeReplyTurn{}, unproved
		}
		seen++
		if !step.plannerResponse {
			continue
		}
		if step.executionID == "" || step.executionID == reply.executionID &&
			(!step.consumed || step.status != contracts.AntigravityStepStatusDone) {
			return agent.NativeReplyTurn{}, unproved
		}
		if step.executionID == reply.executionID && step.replyText {
			items = append(items, agent.NativeReplyItem{Ordinal: int64(index) + 1, MessageKey: stepMessageKey(state.id, index)})
		}
	}
	if seen != state.stepCount-reply.inputIndex-1 {
		return agent.NativeReplyTurn{}, unproved
	}
	slices.SortFunc(items, func(left, right agent.NativeReplyItem) int { return cmp.Compare(left.Ordinal, right.Ordinal) })
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	return agent.NativeReplyTurn{
		SessionID: reply.receipt.SessionID, TurnID: reply.executionID, Outcome: outcome, Items: items,
		Inputs: []agent.NativeReplyBoundary{inputReplyBoundary(reply)},
	}, outcome
}

func inputReplyBoundary(reply *inputReply) agent.NativeReplyBoundary {
	return agent.NativeReplyBoundary{InputID: reply.receipt.InputID, AfterOrdinal: int64(reply.inputIndex) + 1}
}
