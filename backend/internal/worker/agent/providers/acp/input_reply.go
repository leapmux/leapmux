package acp

import (
	"encoding/json"
	"errors"
	"log/slog"

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

func (b *Base) rootControlTurnID(sessionID string, line *providerkit.ParsedLine) string {
	var toolCallID string
	if line.Method == acpMethodSessionRequestPermission {
		params, _ := providerkit.RawObject(line.Params)
		toolCallID = providerkit.StringField(params["toolCall"], contracts.ACPSupplementIdentityToolCallID)
	}
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	target := b.rootTarget
	if tool := b.toolUpdateState[toolCallID]; toolCallID != "" && tool != nil {
		target = tool.target
	}
	request := b.rootPromptRequest
	if target == nil || target.sessionID != sessionID || !target.receiptStarted ||
		request == nil || request.target != target || request.answered != nil {
		return ""
	}
	return target.inputReceipt.TurnID
}

func tagInputReply(content agent.MessageContent, receipt agent.NativeInputReceipt) agent.MessageContent {
	content.AgentSessionID = receipt.SessionID
	content.NativeInputID = receipt.InputID
	content.NativeTurnID = receipt.TurnID
	return content
}

type acpInputReplyProof struct {
	receipt    agent.NativeInputReceipt
	outcome    leapmuxv1.AgentInputTurnOutcome
	retirement leapmuxv1.AgentInputTurnOutcome
}

func proveACPInputReply(request *acpRootPromptRequest, turn acpTurnSnapshot, response json.RawMessage, err error) acpInputReplyProof {
	if request == nil || request.target == nil || request.target.inputReceipt.TurnID == "" {
		return acpInputReplyProof{}
	}
	receipt := request.target.inputReceipt
	proof := acpInputReplyProof{receipt: receipt, retirement: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED}
	if request.answered == nil || turn.endingAdmission == nil || turn.endingAdmission.receipt != receipt {
		return proof
	}
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
		content = tagInputReply(content, proof.receipt)
		content.InputTurnOutcome = proof.outcome
	}
	return content
}

func (b *Base) retireAbandonedInputReply(admission *acpTranscriptAdmission) {
	if admission != nil && admission.receipt.TurnID != "" {
		b.retireInputReply(admission.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

func (b *Base) retireInputReply(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := b.sink.RetireNativeInputTurn(receipt, outcome); err != nil && !agent.OutputRetained(err) {
		slog.Error("retire ACP input reply", "agent_id", b.AgentID(), "session_id", receipt.SessionID, "error", err)
	}
}
