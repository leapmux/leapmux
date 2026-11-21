package droid

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Droid uses the committed user message ID as the native turn ID. Every
// assistant message points to its parent, including native tool-result rows.
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
	items      []agent.NativeReplyItem
}

const droidReplyInputOrdinal int64 = 1

// beginDroidReply registers the client-generated message ID before Droid can
// echo or finish that input. It does not hold the output lock across the RPC.
func (a *Agent) beginDroidReply(receipt agent.NativeInputReceipt, requestID string) *droidNativeReply {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.nativeReplies == nil {
		a.nativeReplies = make(map[string]*droidNativeReply)
	}
	reply := &droidNativeReply{
		receipt: receipt, requestID: requestID,
		messageIDs: make(map[string]bool), toolIDs: make(map[string]bool),
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
				reply.echoed = true
				reply.messageIDs[message.ID] = true
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
// owns every tool named by the control request.
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
			Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: droidReplyInputOrdinal}},
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
	if err := a.orderedSink().RetireNativeInputTurn(reply.receipt, outcome); !agent.OutputRetained(err) {
		slog.Error("Record the Droid reply outcome", "agent_id", a.AgentID(), "error", err)
	}
}
