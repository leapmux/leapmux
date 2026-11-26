package droid

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Droid uses the committed lead message ID as the native turn ID. Assistant
// and steer messages point to a parent, which can be a tool result.
type droidReplyMessage struct {
	ID         string          `json:"id"`
	ParentID   string          `json:"parentId"`
	Role       string          `json:"role"`
	Visibility string          `json:"visibility"`
	Content    json.RawMessage `json:"content"`
}

type droidNativeReply struct {
	receipt    agent.NativeInputReceipt
	requestID  string
	echoed     bool
	unproved   bool
	messageIDs map[string]bool
	toolIDs    map[string]bool
	inputs     []agent.NativeReplyBoundary
	items      []agent.NativeReplyItem
}

const droidReplyInputOrdinal int64 = 1

// beginDroidReply registers the client-generated message ID before Droid can
// echo or finish that input. It does not hold the output lock across the RPC.
func (a *Agent) beginDroidReply(receipt agent.NativeInputReceipt, requestID string) *droidNativeReply {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	return a.registerDroidReply(receipt, requestID)
}

// registerDroidReply adds a native turn. The caller holds dispatchMu.
func (a *Agent) registerDroidReply(receipt agent.NativeInputReceipt, requestID string) *droidNativeReply {
	if a.nativeReplies == nil {
		a.nativeReplies = make(map[string]*droidNativeReply)
	}
	reply := &droidNativeReply{
		receipt: receipt, requestID: requestID,
		messageIDs: make(map[string]bool), toolIDs: make(map[string]bool),
		inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: droidReplyInputOrdinal}},
	}
	a.nativeReplies[receipt.TurnID] = reply
	a.currentReply = reply
	return reply
}

// A committed echo can prove delivery when the RPC response is lost.
func (a *Agent) droidReplyEchoedAfterInputFailure(reply *droidNativeReply) bool {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if reply.echoed {
		return true
	}
	a.forgetDroidReply(reply)
	return false
}

func (a *Agent) forgetDroidReply(reply *droidNativeReply) {
	delete(a.nativeReplies, reply.receipt.TurnID)
	if a.currentReply == reply {
		a.currentReply = nil
	}
}

// observeDroidReplyMessage adds only events whose native IDs establish their
// ownership. Another tracked input starts its own chain at its exact echo.
// The caller holds dispatchMu.
func (a *Agent) observeDroidReplyMessage(requestID string, message droidReplyMessage) {
	if message.Role == "user" {
		if reply := a.nativeReplies[message.ID]; reply != nil {
			if message.Visibility != "llm_only" && (requestID == "" || requestID == reply.requestID) {
				reply.observeLeadEcho()
			}
			return
		}
	}
	if a.droidReplyOwningMessage(message.ID) != nil {
		return
	}
	reply := a.droidReplyOwningMessage(message.ParentID)
	if reply == nil {
		if message.Role == "assistant" && a.currentReply != nil && a.currentReply.echoed {
			a.currentReply.unproved = true
		}
		return
	}
	if message.ID == "" || message.Role == "user" && message.Visibility != "llm_only" {
		// A reader input can start a different turn. A native tool result
		// reaches this chain through its own messageId and toolUseId.
		reply.unproved = true
		return
	}
	reply.messageIDs[message.ID] = true
}

func (reply *droidNativeReply) observeLeadEcho() {
	reply.echoed = true
	reply.messageIDs[reply.receipt.InputID] = true
}

func (a *Agent) droidReplyOwningMessage(messageID string) *droidNativeReply {
	for _, reply := range a.nativeReplies {
		if reply.messageIDs[messageID] {
			return reply
		}
	}
	return nil
}

func (a *Agent) droidReplyOwningTool(toolID string) *droidNativeReply {
	for _, reply := range a.nativeReplies {
		if reply.toolIDs[toolID] {
			return reply
		}
	}
	return nil
}

// droidReplyTurnForTools returns a turn only when the same committed reply
// owns every tool identified by the control request.
func (a *Agent) droidReplyTurnForTools(toolIDs []string) string {
	if len(toolIDs) == 0 {
		return ""
	}
	reply := a.droidReplyOwningTool(toolIDs[0])
	if reply == nil {
		return ""
	}
	for _, toolID := range toolIDs[1:] {
		if !reply.toolIDs[toolID] {
			return ""
		}
	}
	return reply.receipt.TurnID
}

func (reply *droidNativeReply) recordText(messageKey string, retained bool) {
	if !retained {
		reply.unproved = true
		return
	}
	reply.items = append(reply.items, agent.NativeReplyItem{
		Ordinal: droidReplyInputOrdinal + int64(len(reply.items)) + 1, MessageKey: messageKey,
	})
}

// admitSteer places the boundary after the last committed reply item. Two
// inputs read without any intervening assistant text share that boundary.
func (reply *droidNativeReply) admitSteer(messageID string) {
	reply.messageIDs[messageID] = true
	reply.inputs = append(reply.inputs, agent.NativeReplyBoundary{
		InputID: messageID, AfterOrdinal: droidReplyInputOrdinal + int64(len(reply.items)),
	})
}

func (a *Agent) isOlderDroidReplyEnd(turnID string) bool {
	return a.currentReply != nil && turnID != "" && a.currentReply.receipt.TurnID != turnID &&
		(a.currentReply.echoed || a.nativeReplies[turnID] != nil)
}

// finishDroidReply uses the native completion ID, even if the current live
// turn has already changed. A status notification by itself proves no reply.
func (a *Agent) finishDroidReply(turnID, reason string) {
	if turnID == "" {
		if reply := a.currentReply; reply != nil && reply.echoed {
			a.retireDroidReply(reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		}
		return
	}
	reply := a.nativeReplies[turnID]
	if reply == nil {
		return
	}
	_, status := droidTurnCompletion(reason)
	switch {
	case reason == "":
	case status == bgtask.StatusFailed:
		a.retireDroidReply(reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED)
		return
	case status == bgtask.StatusInterrupted:
		a.retireDroidReply(reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED)
		return
	case reply.echoed && !reply.unproved:
		receipt := reply.receipt
		err := a.orderedSink().PublishNativeReplyTurn(agent.NativeReplyTurn{
			SessionID: receipt.SessionID, TurnID: receipt.TurnID,
			Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs:  reply.inputs,
			Items:   reply.items,
		})
		if agent.OutputRetained(err) {
			a.forgetDroidReply(reply)
			return
		}
		slog.Error("Store the exact Droid reply", "agent_id", a.AgentID(), "error", err)
	}
	a.retireDroidReply(reply, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
}

func (a *Agent) retireDroidReply(reply *droidNativeReply, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.forgetDroidReply(reply)
	receipt := reply.receipt
	sink := a.orderedSink()
	for _, input := range reply.inputs {
		receipt.InputID = input.InputID
		a.retireDroidInput(sink, receipt, outcome)
	}
}

func (a *Agent) retireDroidInput(sink agent.ProviderServices, receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if err := sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Error("Record the Droid reply outcome", "agent_id", a.AgentID(), "error", err)
	}
}
