package commandcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type pendingCommandCodeSteer struct {
	sessionID  string
	resolution agent.SteerResolution
	tracked    bool
	reserved   bool
	delivered  bool
}

// SteerInput submits to the native parent-turn queue. It reserves a slot before
// the RPC so a turn/steers_delivered notification that overtakes the reply still
// attributes the steer without losing proof.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	sessionID, turnID, proofLost := a.sessionID, a.turnID, a.steerProofLost
	active := turnID != "" && !a.compacting && a.manualCompaction == nil
	a.Mu.Unlock()
	if !active {
		return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
	}
	if proofLost {
		return agent.NativeInputReceipt{}, &agent.AgentBusyError{Err: agent.ErrAgentBusy, ActiveTurnSteerable: false}
	}
	var content strings.Builder
	content.WriteString(request.Content)
	for _, attachment := range agent.ClassifyAttachments(request.Attachments) {
		if attachment.Kind != contracts.AttachmentMediaKindText {
			return agent.NativeInputReceipt{}, agent.ErrSteeringUnsupported
		}
		content.WriteString("\n\n")
		content.WriteString(providerkit.BuildInlineTextAttachmentBlock(attachment))
	}
	// The native queue has no per-entry ID. The dispatch attempt identifies
	// the entry whose admission and delivery the Worker observes in order.
	// Untracked steers (such as control replies without an attempt ID) still
	// occupy a slot so queue counts stay aligned without emitting resolutions.
	tracked := request.InputID != "" && request.AttemptID != ""
	var resolution agent.SteerResolution
	if tracked {
		resolution = agent.SteerResolution{InputID: request.InputID, AttemptID: request.AttemptID, Receipt: agent.NativeInputReceipt{
			SessionID: sessionID, TurnID: a.commandCodeDurableTurnID(turnID), InputID: "steer:" + request.AttemptID,
		}}
	}
	entry := &pendingCommandCodeSteer{
		sessionID:  sessionID,
		resolution: resolution,
		tracked:    tracked,
		reserved:   true,
	}
	a.dispatchMu.Lock()
	a.Mu.Lock()
	stillActive := a.turnID == turnID && a.sessionID == sessionID && !a.compacting && a.manualCompaction == nil
	proofLost = a.steerProofLost
	a.Mu.Unlock()
	if !stillActive {
		a.dispatchMu.Unlock()
		return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
	}
	if proofLost {
		a.dispatchMu.Unlock()
		return agent.NativeInputReceipt{}, &agent.AgentBusyError{Err: agent.ErrAgentBusy, ActiveTurnSteerable: false}
	}
	a.pendingSteers = append(a.pendingSteers, entry)
	a.dispatchMu.Unlock()

	var acknowledged bool
	var acknowledgmentErr error
	_, err := a.requestObserved(methodTurnSteer, map[string]string{"input": content.String()}, a.APITimeout(), func(result json.RawMessage, replyErr error) {
		if replyErr != nil {
			return
		}
		a.dispatchMu.Lock()
		defer a.dispatchMu.Unlock()
		acknowledgmentErr = a.observeCommandCodeSteerAck(result, turnID, entry)
		acknowledged = true
	})
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if entry.delivered {
		return agent.NativeInputReceipt{}, nil
	}
	if acknowledged {
		return agent.NativeInputReceipt{}, acknowledgmentErr
	}
	a.removePendingCommandCodeSteer(entry)
	if providerkit.HasJSONRPCErrorCode(err, -32001) {
		return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
	}
	if err == nil {
		err = errors.New("the Command Code response did not report steer admission")
	}
	err = providerkit.ClassifyJSONRPCDeliveryError(methodTurnSteer, err)
	if errors.Is(err, agent.ErrDeliveryUncertain) {
		a.loseCommandCodeSteerProof(turnID)
	}
	return agent.NativeInputReceipt{}, err
}

// observeCommandCodeSteerAck runs on the reader before later notifications.
// The caller holds dispatchMu.
func (a *Agent) observeCommandCodeSteerAck(raw json.RawMessage, wireTurnID string, entry *pendingCommandCodeSteer) error {
	if entry.delivered {
		return nil
	}
	var response struct {
		TurnID string `json:"turnId"`
		Queued int    `json:"queued"`
	}
	a.Mu.Lock()
	active, proofLost := a.turnID == wireTurnID && a.sessionID == entry.sessionID, a.steerProofLost
	a.Mu.Unlock()
	if !active {
		a.removePendingCommandCodeSteer(entry)
		return agent.ErrNoActiveTurn
	}
	if json.Unmarshal(raw, &response) != nil || response.TurnID != wireTurnID ||
		response.Queued != len(a.pendingSteers) || proofLost || !slices.Contains(a.pendingSteers, entry) {
		a.removePendingCommandCodeSteer(entry)
		a.loseCommandCodeSteerProof(wireTurnID)
		return fmt.Errorf("%w: Command Code steer acknowledgment disagreed with the tracked queue", agent.ErrDeliveryUncertain)
	}
	entry.reserved = false
	return nil
}

func (a *Agent) removePendingCommandCodeSteer(target *pendingCommandCodeSteer) {
	index := slices.Index(a.pendingSteers, target)
	if index < 0 {
		return
	}
	a.pendingSteers = slices.Delete(a.pendingSteers, index, index+1)
}

func (a *Agent) markPendingCommandCodeSteersUncertain() {
	for _, pending := range a.pendingSteers {
		if pending.reserved || !pending.tracked {
			continue
		}
		resolution := pending.resolution
		resolution.Outcome = agent.SteerOutcomeUncertain
		resolution.Receipt = agent.NativeInputReceipt{}
		a.reportCommandCodeSteer(resolution)
	}
	a.pendingSteers = nil
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
	if reply := a.replies.Lookup(turnID); reply != nil {
		reply.MarkUnproved()
	}
	a.markPendingCommandCodeSteersUncertain()
	a.PublishTurnActive()
}

func (a *Agent) deliverCommandCodeSteers(turnID string, count int) {
	a.Mu.Lock()
	activeTurnID, proofLost := a.turnID, a.steerProofLost
	a.Mu.Unlock()
	if count <= 0 || activeTurnID == "" || proofLost || turnID != "" && turnID != activeTurnID {
		return
	}
	// A drain can include an in-flight reserved steer, or drain only earlier
	// acknowledged steers before the host reads the in-flight steer request.
	matchesAll := count == len(a.pendingSteers)
	matchesAcknowledged := len(a.pendingSteers) > 1 && a.pendingSteers[len(a.pendingSteers)-1].reserved && count == len(a.pendingSteers)-1
	if turnID == "" || (!matchesAll && !matchesAcknowledged) {
		a.loseCommandCodeSteerProof(activeTurnID)
		return
	}
	a.flushPendingRunError()
	if err := a.persistCommandCodeGeneration(agent.MessageCompletionComplete, true); err != nil {
		slog.Error("retain Command Code output before a steer", "agent_id", a.AgentID(), "error", err)
	}
	for _, pending := range a.pendingSteers[:count] {
		pending.delivered = true
		if !pending.tracked {
			continue
		}
		resolution := pending.resolution
		resolution.Outcome = agent.SteerOutcomeEchoed
		if !a.recordCommandCodeSteerBoundary(turnID, resolution.Receipt) {
			resolution.Receipt.TurnID = ""
		}
		a.reportCommandCodeSteer(resolution)
		if resolution.Receipt.TurnID == "" {
			a.retireCommandCodeInput(resolution.Receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		}
	}
	clear(a.pendingSteers[:count])
	a.pendingSteers = a.pendingSteers[count:]
}

func (a *Agent) reportCommandCodeSteer(resolution agent.SteerResolution) {
	if a.sink == nil {
		return
	}
	if err := agent.RetainSteerResolution(a.sink, resolution); !agent.OutputRetained(err) {
		slog.Error("record the Command Code steer resolution", "agent_id", a.AgentID(), "input_id", resolution.InputID, "error", err)
	}
}
