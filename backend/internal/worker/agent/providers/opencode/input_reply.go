package opencode

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (s *openCodeSessionRuntime) pendingInputReplies() []agent.NativeInputReceipt {
	return s.collectPendingInputReplies(false)
}

func (s *openCodeSessionRuntime) pendingSettledInputReplies() []agent.NativeInputReceipt {
	return s.collectPendingInputReplies(true)
}

func (s *openCodeSessionRuntime) collectPendingInputReplies(skipAdmitting bool) []agent.NativeInputReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	replies := make([]agent.NativeInputReceipt, 0, len(s.replyInputs))
	for inputID, receipt := range s.replyInputs {
		if skipAdmitting && s.admissions[inputID] != nil {
			continue
		}
		replies = append(replies, receipt)
	}
	slices.SortFunc(replies, func(left, right agent.NativeInputReceipt) int {
		return cmp.Compare(left.InputID, right.InputID)
	})
	return replies
}

func (s *openCodeSessionRuntime) hasPendingInputReplies() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.replyInputs) != 0
}

func (s *openCodeSessionRuntime) forgetInputReply(inputID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.replyInputs, inputID)
}

func (s *openCodeSessionRuntime) retireInputReply(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	if err := s.sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		return err
	}
	s.forgetInputReply(receipt.InputID)
	return nil
}

type openCodeReplyInput struct {
	receipt          agent.NativeInputReceipt
	assistantsBefore int
}

// openCodeReplyProjection tracks the inputs and replies before one native
// ending record. Each identified steer shares the execution once OpenCode
// delivers it. An unknown input leaves earlier ownership uncertain.
type openCodeReplyProjection struct {
	inputs     []openCodeReplyInput
	assistants []openCodeStoredMessage
	unproved   []agent.NativeInputReceipt
	ending     openCodeStoredMessage
}

func isOpenCodeReplyInput(message openCodeStoredMessage) bool {
	return message.Type == contracts.OpenCodeNativeMessageTypeUser || message.Type == "synthetic"
}

func (s *openCodeSessionRuntime) replyReceipt(message openCodeStoredMessage) agent.NativeInputReceipt {
	if message.Type == contracts.OpenCodeNativeMessageTypeUser {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.replyInputs[message.ID]
	}
	return agent.NativeInputReceipt{}
}

func (projection *openCodeReplyProjection) observe(message openCodeStoredMessage, receipt agent.NativeInputReceipt) {
	if isOpenCodeReplyInput(message) {
		if receipt.InputID != "" {
			projection.inputs = append(projection.inputs, openCodeReplyInput{receipt: receipt, assistantsBefore: len(projection.assistants)})
			return
		}
		for _, input := range projection.inputs {
			projection.unproved = append(projection.unproved, input.receipt)
		}
		projection.inputs = nil
		projection.assistants = nil
	} else if message.Type == "assistant" && len(projection.inputs) != 0 {
		projection.assistants = append(projection.assistants, message)
	}
}

// Read only the recent pages needed to prove a reply. Full restore still
// reads the whole transcript with its larger pages.
const openCodeReplyPageSize = 16

// visitReplyPredecessors skips records newer than the anchor and stops at the
// previous native execution boundary. The caller can stop earlier.
func (run *openCodeRun) visitReplyPredecessors(ctx context.Context, anchorID, anchorType string, visit func(openCodeStoredMessage) bool) (openCodeStoredMessage, error) {
	var anchor openCodeStoredMessage
	err := run.rpc.scanMessages(ctx, run.id, url.Values{"order": {"desc"}}, openCodeReplyPageSize, func(message openCodeStoredMessage) (bool, error) {
		if anchor.ID == "" {
			if message.ID == anchorID && message.Type == anchorType {
				anchor = message
			}
			return false, nil
		}
		if message.Type == contracts.OpenCodeNativeMessageTypeIdle {
			return true, nil
		}
		return visit(message), nil
	})
	return anchor, err
}

// readReplyProjection starts at the exact native ending record. A snapshot
// can already contain a newer execution when its predecessor's event arrives.
func (run *openCodeRun) readReplyProjection(ctx context.Context, endingID string) (openCodeReplyProjection, error) {
	var newestFirst []openCodeStoredMessage
	ending, err := run.visitReplyPredecessors(ctx, endingID, contracts.OpenCodeNativeMessageTypeIdle, func(message openCodeStoredMessage) bool {
		if isOpenCodeReplyInput(message) {
			message.Raw = nil
			newestFirst = append(newestFirst, message)
		} else if message.Type == "assistant" {
			newestFirst = append(newestFirst, message)
		}
		return false
	})
	if err != nil {
		return openCodeReplyProjection{}, err
	}
	if ending.ID == "" {
		return openCodeReplyProjection{}, fmt.Errorf("OpenCode ending record %q not found", endingID)
	}
	projection := openCodeReplyProjection{ending: ending}
	for _, message := range slices.Backward(newestFirst) {
		projection.observe(message, run.replyReceipt(message))
	}
	return projection, nil
}

// controlReplyInputs starts at the request's own assistant message and follows
// the execution back to its previous ending or an unidentified input.
func (run *openCodeRun) controlReplyInputs(messageID string) []string {
	if messageID == "" || !run.hasPendingInputReplies() {
		return nil
	}
	var inputs []string
	_, err := run.visitReplyPredecessors(run.ctx, messageID, "assistant", func(message openCodeStoredMessage) bool {
		if isOpenCodeReplyInput(message) {
			receipt := run.replyReceipt(message)
			if receipt.InputID == "" {
				return true
			}
			inputs = append(inputs, receipt.InputID)
		}
		return false
	})
	if err != nil {
		slog.Debug("could not prove the owner of an OpenCode control", "session_id", run.id, "error", err)
		return nil
	}
	return inputs
}

func (run *openCodeRun) publishInputReply(projection openCodeReplyProjection) error {
	for _, receipt := range projection.unproved {
		if err := run.confirmProjectedInput(receipt.InputID); err != nil {
			return err
		}
		if err := run.retireInputReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil {
			return err
		}
	}
	if len(projection.inputs) == 0 {
		return nil
	}
	for _, input := range projection.inputs {
		if err := run.confirmProjectedInput(input.receipt.InputID); err != nil {
			return err
		}
	}
	outcome := openCodeProjectedReplyOutcome(projection.ending)
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		for _, input := range projection.inputs {
			if err := run.retireInputReply(input.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil {
				return err
			}
		}
		return nil
	}
	const inputOrdinal int64 = 1
	turn := agent.NativeReplyTurn{
		SessionID: run.id, TurnID: projection.ending.ID, Outcome: outcome,
	}
	inputs := projection.inputs
	for index, message := range projection.assistants {
		for len(inputs) > 0 && inputs[0].assistantsBefore == index {
			turn.Inputs = append(turn.Inputs, agent.NativeReplyBoundary{InputID: inputs[0].receipt.InputID, AfterOrdinal: inputOrdinal + int64(len(turn.Items))})
			inputs = inputs[1:]
		}
		snapshot, err := readOpenCodeAssistantSnapshot(message)
		if err != nil {
			return err
		}
		if providerkit.RawPresent(snapshot.Retry) {
			continue
		}
		textKeys, err := run.text.recoverSnapshotWithTextKeys(message.ID, snapshot)
		if !agent.OutputRetained(err) {
			return err
		}
		for _, key := range textKeys {
			turn.Items = append(turn.Items, agent.NativeReplyItem{
				Ordinal:    inputOrdinal + 1 + int64(len(turn.Items)),
				MessageKey: key,
			})
		}
	}
	for _, input := range inputs {
		turn.Inputs = append(turn.Inputs, agent.NativeReplyBoundary{InputID: input.receipt.InputID, AfterOrdinal: inputOrdinal + int64(len(turn.Items))})
	}
	if err := run.sink.PublishNativeReplyTurn(turn); !agent.OutputRetained(err) {
		return fmt.Errorf("publish the exact OpenCode reply: %w", err)
	}
	for _, input := range turn.Inputs {
		run.forgetInputReply(input.InputID)
	}
	return nil
}

func openCodeProjectedReplyOutcome(message openCodeStoredMessage) leapmuxv1.AgentInputTurnOutcome {
	switch providerkit.StringField(message.Raw, "outcome") {
	case contracts.OpenCodeNativeOutcomeSucceeded:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case contracts.OpenCodeNativeOutcomeInterrupted:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case contracts.OpenCodeNativeOutcomeFailed:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
}

func (run *openCodeRun) retirePendingInputReplies() error {
	return run.retireInputReplies(run.pendingInputReplies())
}

func (run *openCodeRun) retireSettledPendingInputReplies() error {
	return run.retireInputReplies(run.pendingSettledInputReplies())
}

func (run *openCodeRun) retireInputReplies(replies []agent.NativeInputReceipt) error {
	var result error
	for _, receipt := range replies {
		result = errors.Join(result, run.retireInputReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED))
	}
	return result
}
