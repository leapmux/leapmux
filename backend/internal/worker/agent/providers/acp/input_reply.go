package acp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// startInputReply runs on the stdin writer. Serialize the ownership change
// with the reader and reserve preceding text before admitting the prompt.
// The sender prepares that text after the write so the stdin writer does no I/O
// here and the reader can still answer requests from the provider.
func (b *Base) startInputReply(request *acpRootPromptRequest) {
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	b.turnMu.Lock()
	if b.rootTarget != request.target || !b.ownsRootPromptEndLocked(request.sessionID, request.target, request) {
		b.turnMu.Unlock()
		return
	}
	preceding := b.takeBufferedTextLocked()
	preceding.sessionID = request.sessionID
	preceding.textProgressCompleted = true
	b.reserveBufferedTextLocked(&preceding)
	request.beforeDispatch = preceding
	request.target.receiptStarted = true
	b.turnMu.Unlock()
	b.completeBufferedTextProgress(preceding)
}

func (b *Base) controlRequestTurnID(sessionID string, line *providerkit.ParsedLine) string {
	var toolCallID string
	if line.Method == acpMethodSessionRequestPermission {
		params, _ := providerkit.RawObject(line.Params)
		toolCallID = providerkit.StringField(params["toolCall"], contracts.ACPSupplementIdentityToolCallID)
	}
	if toolCallID != "" && b.hooks.NativePermissionTurnID != nil {
		if turnID := b.hooks.NativePermissionTurnID(sessionID, toolCallID); turnID != "" {
			return turnID
		}
	}
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	target := b.rootTarget
	if tool := b.toolUpdateState[toolCallID]; toolCallID != "" && tool != nil {
		target = tool.target
	}
	if target == nil || target.sessionID != sessionID || !target.receiptStarted {
		return ""
	}
	if target.agentInitiated {
		if target == b.rootTarget {
			return target.inputReceipt.TurnID
		}
		return ""
	}
	request := b.rootPromptRequest
	if request == nil || request.target != target || request.answered != nil {
		return ""
	}
	return target.inputReceipt.TurnID
}

// RootReplyTurnID identifies the live reply scope of a served root session.
// A provider can bind delayed native consumption evidence to this scope.
func (b *Base) RootReplyTurnID(sessionID string) string {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	target, request := b.rootTarget, b.rootPromptRequest
	if target == nil || target.sessionID != sessionID || !target.receiptStarted {
		return ""
	}
	if target.agentInitiated || request != nil && request.target == target && request.answered == nil && !request.nativeReplyManaged {
		return target.inputReceipt.TurnID
	}
	return ""
}

// CaptureRootSteer records a provider-confirmed echo in the current root
// turn. The reader flushes earlier text before calling it and retains the
// returned write at the echo's transcript position.
func (b *Base) CaptureRootSteer(sessionID, nativeInputID string, resolution agent.SteerResolution) func() error {
	b.turnMu.Lock()
	target, request := b.rootTarget, b.rootPromptRequest
	sharesTurn := target != nil && target.sessionID == sessionID && target.receiptStarted &&
		target.inputReceipt.TurnID != "" && (target.agentInitiated ||
		request != nil && request.target == target && request.answered == nil && !request.nativeReplyManaged)
	if sharesTurn {
		target.sharingReply = true
		resolution.Receipt = agent.NativeInputReceipt{
			SessionID: sessionID, InputID: nativeInputID, TurnID: target.inputReceipt.TurnID,
		}
	}
	b.turnMu.Unlock()

	write := b.sink.CaptureSteerResolution(resolution)
	if !sharesTurn {
		return write
	}
	return func() error {
		if err := write(); err != nil {
			return err
		}
		target.mu.Lock()
		target.steerReceipts = append(target.steerReceipts, resolution.Receipt)
		target.mu.Unlock()
		return nil
	}
}

func tagInputReply(content agent.MessageContent, receipt agent.NativeInputReceipt, shared bool) agent.MessageContent {
	content.AgentSessionID = receipt.SessionID
	content.NativeInputID = receipt.InputID
	if shared {
		content.NativeInputID = ""
	}
	content.NativeTurnID = receipt.TurnID
	return content
}

type acpInputReplyProof struct {
	receipt    agent.NativeInputReceipt
	outcome    leapmuxv1.AgentInputTurnOutcome
	retirement leapmuxv1.AgentInputTurnOutcome
	shared     bool
}

func proveACPInputReply(request *acpRootPromptRequest, turn acpTurnSnapshot, response json.RawMessage, err error) acpInputReplyProof {
	if request == nil || request.nativeReplyManaged || request.target == nil || request.target.inputReceipt.TurnID == "" {
		return acpInputReplyProof{}
	}
	receipt := request.target.inputReceipt
	proof := acpInputReplyProof{receipt: receipt, retirement: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED}
	if request.answered == nil || turn.endingAdmission == nil || turn.endingAdmission.receipt != receipt {
		return proof
	}
	proof.shared = turn.endingAdmission.shared
	if err != nil {
		var responseError *providerkit.JSONRPCResponseError
		if errors.As(err, &responseError) {
			proof.retirement = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
		return proof
	}
	switch acpPromptStopReason(response) {
	case contracts.ACPStopReasonEndTurn, "max_tokens", "max_turn_requests", "refusal":
		proof.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case contracts.ACPStopReasonCancelled:
		proof.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	default:
		return proof
	}
	proof.retirement = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	return proof
}

func acpPromptStopReason(response json.RawMessage) string {
	var body struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason json.RawMessage `json:"stopReason"`
	}
	if json.Unmarshal(response, &body) != nil {
		return ""
	}
	if body.Role == contracts.ACPRoleResult {
		if content, ok := providerkit.RawObject(body.Content); ok {
			reason, _ := providerkit.RawString(content["stopReason"])
			return reason
		}
	}
	reason, _ := providerkit.RawString(body.StopReason)
	return reason
}

func (proof acpInputReplyProof) endRecord(response json.RawMessage) agent.MessageContent {
	content := agent.MessageContent{Original: response}
	if proof.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		content = tagInputReply(content, proof.receipt, proof.shared)
		content.InputTurnOutcome = proof.outcome
	}
	return content
}

func (b *Base) retireAbandonedInputReply(admission *acpTranscriptAdmission) {
	if admission != nil && admission.receipt.TurnID != "" {
		outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		if admission.receipt.InputID != "" {
			b.retireInputReply(admission.receipt, outcome)
		}
		b.retireSharedInputs(admission, outcome)
	}
}

func (b *Base) retireSharedInputs(admission *acpTranscriptAdmission, outcome leapmuxv1.AgentInputTurnOutcome) {
	if admission == nil || !admission.shared {
		return
	}
	target := admission.target
	writer := agent.DirectProviderServices(b.sink)
	b.queueReplyReconciliation(func() error {
		target.mu.Lock()
		receipts := slices.Clone(target.steerReceipts)
		target.mu.Unlock()
		for _, receipt := range receipts {
			if err := writer.RetireNativeInputTurn(receipt, outcome); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *Base) retireInputReply(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := b.sink.RetireNativeInputTurn(receipt, outcome); err != nil && !agent.OutputRetained(err) {
		slog.Error("retire ACP input reply", "agent_id", b.AgentID(), "session_id", receipt.SessionID, "error", err)
	}
}
