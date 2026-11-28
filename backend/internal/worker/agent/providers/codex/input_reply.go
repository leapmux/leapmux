package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codexTrackedReply owns native reply correlation for one SendInput turn/start
// request independently of turn-start admission state. Guarded by Agent.Mu.
// The done channel retains its identity.
type codexTrackedReply struct {
	receipt        agent.NativeInputReceipt
	done           chan struct{}
	err            error
	settled        bool
	detached       bool
	turnID         string
	endedBeforeAck map[string]bool
}

func newCodexTrackedReply(receipt agent.NativeInputReceipt) *codexTrackedReply {
	return &codexTrackedReply{
		receipt:        receipt,
		done:           make(chan struct{}),
		endedBeforeAck: make(map[string]bool),
	}
}

// allowsTurnStartLocked reports whether a turn/started notification for turnID
// may attach to a pending start carrying this reply. The caller holds Agent.Mu.
func (r *codexTrackedReply) allowsTurnStartLocked(turnID string) bool {
	if r == nil {
		return true
	}
	if r.turnID != "" {
		return r.turnID == turnID
	}
	return r.detached
}

func codexReplyTurnID(raw json.RawMessage) string {
	var result struct {
		Turn struct {
			ID     string            `json:"id"`
			Status string            `json:"status"`
			Items  []json.RawMessage `json:"items"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Turn.ID == "" || result.Turn.Items == nil {
		return ""
	}
	switch result.Turn.Status {
	case "inProgress", "completed", "interrupted", "failed":
		return result.Turn.ID
	default:
		return ""
	}
}

func (a *Agent) finishCodexReply(reply *codexTrackedReply, err error) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	finishCodexReplyLocked(reply, err)
}

func finishCodexReplyLocked(reply *codexTrackedReply, err error) {
	if !reply.settled {
		reply.settled, reply.err = true, err
		close(reply.done)
	}
}

// recordCodexSteerEcho retains the input row at the matching notification.
// The caller holds outputMu, which orders the echo with reply rows and turn end.
func (a *Agent) recordCodexSteerEcho(threadID, turnID, clientID string) {
	receipt := agent.NativeInputReceipt{SessionID: threadID, TurnID: turnID, InputID: clientID}
	report, matched := a.steerLedger.TakeEcho(receipt)
	if matched && a.retainCodexSteerResolution(report) {
		a.replySteers = append(a.replySteers, receipt)
	}
}

func (a *Agent) resolveCodexUnechoedSteers(threadID, turnID string, outcome agent.SteerOutcome) {
	// Each resolved steer moves ahead of the other resolved inputs. Resolve
	// newest first to preserve the original send order in the queue.
	for _, report := range slices.Backward(a.steerLedger.TakeUnechoed(threadID, turnID)) {
		report.Outcome = outcome
		report.Receipt = agent.NativeInputReceipt{}
		a.retainCodexSteerResolution(report)
	}
}

func (a *Agent) retainCodexSteerResolution(report agent.SteerResolution) bool {
	err := agent.RetainSteerResolution(a.sink, report)
	if !agent.OutputRetained(err) {
		slog.Warn("retain the Codex steer outcome", "input_id", report.InputID, "error", err)
		return false
	}
	return true
}

// takeCodexSteerReplies releases echoed steers for the selected native turn,
// session, or process. The caller holds outputMu.
func (a *Agent) takeCodexSteerReplies(threadID, turnID string) []agent.NativeInputReceipt {
	var receipts []agent.NativeInputReceipt
	a.replySteers = slices.DeleteFunc(a.replySteers, func(receipt agent.NativeInputReceipt) bool {
		if (threadID != "" && receipt.SessionID != threadID) || (turnID != "" && receipt.TurnID != turnID) {
			return false
		}
		receipts = append(receipts, receipt)
		return true
	})
	return receipts
}

func (a *Agent) recordCodexReplyEnd(threadID, turnID string, retained bool) {
	if threadID == "" || turnID == "" {
		return
	}
	unproved := a.takeCodexSteerReplies(threadID, turnID)
	if retained {
		unproved = nil
	}
	a.Mu.Lock()
	for _, inputID := range slices.Sorted(maps.Keys(a.replyStarts)) {
		reply := a.replyStarts[inputID]
		if reply.receipt.SessionID != threadID {
			continue
		}
		switch reply.turnID {
		case turnID:
			delete(a.replyStarts, inputID)
			if !retained {
				unproved = append(unproved, reply.receipt)
			}
		case "":
			reply.endedBeforeAck[turnID] = retained
		}
	}
	a.Mu.Unlock()
	for _, receipt := range unproved {
		a.retireCodexNativeReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

func (a *Agent) codexControlRequestOwner(params json.RawMessage) providerkit.ControlRequestOwner {
	var owner struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if json.Unmarshal(params, &owner) != nil || owner.ThreadID == "" || owner.TurnID == "" || !a.isMainThreadID(owner.ThreadID) {
		return providerkit.ControlRequestOwner{}
	}
	return providerkit.ControlRequestOwner{SessionID: owner.ThreadID, TurnID: owner.TurnID}
}

func (a *Agent) retireCodexInputReplies(threadID string) {
	receipts := a.takeCodexSteerReplies(threadID, "")
	a.Mu.Lock()
	for _, inputID := range slices.Sorted(maps.Keys(a.replyStarts)) {
		reply := a.replyStarts[inputID]
		if threadID == "" || threadID == reply.receipt.SessionID {
			delete(a.replyStarts, inputID)
			receipts = append(receipts, reply.receipt)
			var err error
			if reply.turnID == "" {
				err = fmt.Errorf("%w: Codex retired the request before confirming it", agent.ErrDeliveryUncertain)
			}
			finishCodexReplyLocked(reply, err)
		}
	}
	a.Mu.Unlock()
	for _, receipt := range receipts {
		a.retireCodexNativeReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
}

func (a *Agent) retireCodexNativeReply(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Warn("retain the Codex native input outcome", "error", err)
	}
}

func codexNativeReplyOutcome(status string) leapmuxv1.AgentInputTurnOutcome {
	switch status {
	case "completed":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case "interrupted":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case "failed":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
}
