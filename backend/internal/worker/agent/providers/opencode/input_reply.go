package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (s *openCodeSessionRuntime) pendingInputReplies() map[string]agent.NativeInputReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.replyInputs)
}

func (s *openCodeSessionRuntime) forgetInputReply(inputID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.replyInputs, inputID)
}

func (s *openCodeSessionRuntime) retireInputReply(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	if err := s.sink.RetireNativeInputTurn(receipt, outcome); err != nil {
		return err
	}
	s.forgetInputReply(receipt.InputID)
	return nil
}

// openCodeReplyProjection tracks one execution in projected message order.
// OpenCode can promote another input in the same execution. An earlier input
// then has no separate execution result, so it cannot use this ending record.
type openCodeReplyProjection struct {
	receipt    agent.NativeInputReceipt
	assistants []openCodeStoredMessage
	earlier    []agent.NativeInputReceipt
	outcome    leapmuxv1.AgentInputTurnOutcome
}

func isOpenCodeReplyInput(message openCodeStoredMessage) bool {
	return message.Type == contracts.OpenCodeNativeMessageTypeUser || message.Type == "synthetic"
}

func openCodeReceiptForMessage(message openCodeStoredMessage, receipts map[string]agent.NativeInputReceipt) agent.NativeInputReceipt {
	if message.Type == contracts.OpenCodeNativeMessageTypeUser {
		return receipts[message.ID]
	}
	return agent.NativeInputReceipt{}
}

func (projection *openCodeReplyProjection) observe(message openCodeStoredMessage, receipts map[string]agent.NativeInputReceipt) {
	if isOpenCodeReplyInput(message) {
		if projection.receipt.InputID != "" {
			projection.earlier = append(projection.earlier, projection.receipt)
		}
		projection.receipt = openCodeReceiptForMessage(message, receipts)
		projection.assistants = nil
	} else if message.Type == "assistant" && projection.receipt.InputID != "" {
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
func (run *openCodeRun) readReplyProjection(ctx context.Context, endingID string, receipts map[string]agent.NativeInputReceipt) (openCodeReplyProjection, error) {
	var newestFirst []openCodeStoredMessage
	var foundInput bool
	ending, err := run.visitReplyPredecessors(ctx, endingID, contracts.OpenCodeNativeMessageTypeIdle, func(message openCodeStoredMessage) bool {
		if isOpenCodeReplyInput(message) {
			foundInput = true
			message.Raw = nil
			newestFirst = append(newestFirst, message)
		} else if !foundInput && message.Type == "assistant" {
			newestFirst = append(newestFirst, message)
		}
		return false
	})
	if err != nil {
		return openCodeReplyProjection{}, err
	}
	projection := openCodeReplyProjection{outcome: openCodeProjectedReplyOutcome(ending)}
	for _, message := range slices.Backward(newestFirst) {
		projection.observe(message, receipts)
	}
	return projection, nil
}

// controlReplyTurn starts at the assistant message that raised the request.
// An unrelated request can arrive after another prompt has been delivered.
func (run *openCodeRun) controlReplyTurn(messageID string) string {
	receipts := run.pendingInputReplies()
	if messageID == "" || len(receipts) == 0 {
		return ""
	}
	var turnID string
	_, err := run.visitReplyPredecessors(run.ctx, messageID, "assistant", func(message openCodeStoredMessage) bool {
		if isOpenCodeReplyInput(message) {
			turnID = openCodeReceiptForMessage(message, receipts).TurnID
			return true
		}
		return false
	})
	if err != nil {
		slog.Debug("could not prove the owner of an OpenCode control", "session_id", run.id, "error", err)
		return ""
	}
	return turnID
}

func (run *openCodeRun) publishInputReply(projection openCodeReplyProjection) error {
	for _, receipt := range projection.earlier {
		if err := run.retireInputReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil {
			return err
		}
	}
	receipt := projection.receipt
	if receipt.InputID == "" {
		return nil
	}
	if projection.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		return run.retireInputReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	const inputOrdinal int64 = 1
	turn := agent.NativeReplyTurn{
		SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: projection.outcome,
		Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: inputOrdinal}},
	}
	for _, message := range projection.assistants {
		snapshot, err := readOpenCodeAssistantSnapshot(message)
		if err != nil {
			return err
		}
		if providerkit.RawPresent(snapshot.Retry) {
			continue
		}
		if err := run.text.recoverSnapshot(message.ID, snapshot); err != nil {
			return err
		}
		ordinal := 0
		for _, raw := range *snapshot.Content {
			var part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &part); err != nil {
				return err
			}
			if part.Type != "text" {
				continue
			}
			if part.Text != "" {
				turn.Items = append(turn.Items, agent.NativeReplyItem{
					Ordinal:    inputOrdinal + 1 + int64(len(turn.Items)),
					MessageKey: openCodeTextScopeID(message.ID, agent.AssembledMessageKindText, ordinal),
				})
			}
			ordinal++
		}
	}
	if err := run.sink.PublishNativeReplyTurn(turn); err != nil {
		return fmt.Errorf("publish the exact OpenCode reply: %w", err)
	}
	run.forgetInputReply(receipt.InputID)
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
	var result error
	for _, receipt := range run.pendingInputReplies() {
		result = errors.Join(result, run.retireInputReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED))
	}
	return result
}
