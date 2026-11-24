package commandcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// SteerInput submits to the native parent-turn queue. The RPC acknowledges
// each append before the reader can observe the next drain of that queue.
func (a *Agent) SteerInput(request agent.SteerRequest) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	sessionID, turnID, proofLost := a.sessionID, a.turnID, a.steerProofLost
	active := turnID != "" && !a.compacting && a.manualCompaction == nil
	a.Mu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	if proofLost {
		return &agent.AgentBusyError{Err: agent.ErrAgentBusy, ActiveTurnSteerable: false}
	}
	content := request.Content
	for _, attachment := range agent.ClassifyAttachments(request.Attachments) {
		if attachment.Kind != contracts.AttachmentMediaKindText {
			return agent.ErrSteeringUnsupported
		}
		content += "\n\n" + providerkit.BuildInlineTextAttachmentBlock(attachment)
	}
	// The native queue has no per-entry ID. The dispatch attempt identifies
	// the entry whose admission and delivery the Worker observes in order.
	resolution := agent.SteerResolution{InputID: request.InputID, AttemptID: request.AttemptID, Receipt: agent.NativeInputReceipt{
		SessionID: sessionID, TurnID: a.commandCodeDurableTurnID(turnID), InputID: "steer:" + request.AttemptID,
	}}
	var acknowledged bool
	var acknowledgmentErr error
	_, err := a.requestObserved(methodTurnSteer, map[string]string{"input": content}, a.APITimeout(), func(result json.RawMessage, replyErr error) {
		if replyErr != nil {
			return
		}
		a.dispatchMu.Lock()
		defer a.dispatchMu.Unlock()
		acknowledgmentErr = a.observeCommandCodeSteerAck(result, turnID, resolution)
		acknowledged = true
	})
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if acknowledged {
		return acknowledgmentErr
	}
	if providerkit.HasJSONRPCErrorCode(err, -32001) {
		return agent.ErrNoActiveTurn
	}
	if err == nil {
		err = errors.New("the Command Code response did not report steer admission")
	}
	err = providerkit.ClassifyJSONRPCDeliveryError(methodTurnSteer, err)
	if errors.Is(err, agent.ErrDeliveryUncertain) {
		a.loseCommandCodeSteerProof(turnID)
	}
	return err
}

// observeCommandCodeSteerAck runs on the reader before later notifications.
// The caller holds dispatchMu.
func (a *Agent) observeCommandCodeSteerAck(raw json.RawMessage, wireTurnID string, resolution agent.SteerResolution) error {
	var response struct {
		TurnID string `json:"turnId"`
		Queued int    `json:"queued"`
	}
	receipt := resolution.Receipt
	a.Mu.Lock()
	active, proofLost := a.turnID == wireTurnID && a.sessionID == receipt.SessionID, a.steerProofLost
	a.Mu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	if json.Unmarshal(raw, &response) != nil || response.TurnID != wireTurnID ||
		response.Queued != len(a.pendingSteers)+1 || proofLost {
		a.loseCommandCodeSteerProof(wireTurnID)
		return fmt.Errorf("%w: Command Code steer acknowledgment disagreed with the tracked queue", agent.ErrDeliveryUncertain)
	}
	a.pendingSteers = append(a.pendingSteers, resolution)
	return nil
}

// SendRawInput forwards opaque provider input. A raw steer can add an entry
// outside the typed queue, so the Worker cannot interpret later drain counts.
func (a *Agent) SendRawInput(input agent.RawInput) error {
	if containsCommandCodeRawSteer(input.Data) {
		a.sendMu.Lock()
		defer a.sendMu.Unlock()
		a.dispatchMu.Lock()
		a.Mu.Lock()
		turnID := a.turnID
		a.Mu.Unlock()
		a.loseCommandCodeSteerProof(turnID)
		a.dispatchMu.Unlock()
	}
	return a.JSONRPCProcess.SendRawInput(input)
}

func containsCommandCodeRawSteer(data []byte) bool {
	// The runtime parses each input line independently and uses the literal
	// JSON key "method". A raw write can contain several lines.
	for _, frame := range bytes.FieldsFunc(data, func(r rune) bool { return r == '\n' || r == '\r' }) {
		var message map[string]json.RawMessage
		if json.Unmarshal(frame, &message) == nil {
			if method, _ := providerkit.RawString(message["method"]); method == methodTurnSteer {
				return true
			}
		}
	}
	return false
}

func (a *Agent) loseCommandCodeSteerProof(turnID string) {
	a.Mu.Lock()
	if turnID == "" || a.turnID != turnID || a.steerProofLost {
		a.Mu.Unlock()
		return
	}
	a.steerProofLost = true
	a.Mu.Unlock()
	if reply := a.nativeReplies[turnID]; reply != nil {
		reply.unproved = true
	}
	for _, pending := range a.pendingSteers {
		pending.Outcome = agent.SteerOutcomeUncertain
		pending.Receipt = agent.NativeInputReceipt{}
		a.reportCommandCodeSteer(pending)
	}
	a.pendingSteers = nil
	a.PublishTurnActive()
}

func (a *Agent) deliverCommandCodeSteers(turnID string, count int) {
	a.Mu.Lock()
	activeTurnID, proofLost := a.turnID, a.steerProofLost
	a.Mu.Unlock()
	if count <= 0 || activeTurnID == "" || proofLost || turnID != "" && turnID != activeTurnID {
		return
	}
	if turnID == "" || count != len(a.pendingSteers) {
		a.loseCommandCodeSteerProof(activeTurnID)
		return
	}
	a.flushPendingRunError()
	if err := a.persistCommandCodeGeneration(agent.MessageCompletionComplete, true); err != nil {
		slog.Error("retain Command Code output before a steer", "agent_id", a.AgentID(), "error", err)
	}
	for _, pending := range a.pendingSteers[:count] {
		pending.Outcome = agent.SteerOutcomeEchoed
		if !a.recordCommandCodeSteerBoundary(turnID, pending.Receipt) {
			pending.Receipt.TurnID = ""
		}
		a.reportCommandCodeSteer(pending)
		if pending.Receipt.TurnID == "" {
			a.retireCommandCodeInput(pending.Receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		}
	}
	clear(a.pendingSteers[:count])
	a.pendingSteers = a.pendingSteers[count:]
}

func (a *Agent) reportCommandCodeSteer(resolution agent.SteerResolution) {
	if err := agent.RetainSteerResolution(a.sink, resolution); !agent.OutputRetained(err) {
		slog.Error("record the Command Code steer resolution", "agent_id", a.AgentID(), "input_id", resolution.InputID, "error", err)
	}
}
