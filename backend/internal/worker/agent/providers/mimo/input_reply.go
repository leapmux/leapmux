package mimo

import (
	"encoding/json"
	"log/slog"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const mimoReceiptUpdated = "session.receipt.updated"

type mimoReceiptUpdate struct {
	ID         string `json:"receiptId"`
	SessionID  string `json:"sessionID"`
	ActorID    string `json:"agentID"`
	State      string `json:"state"`
	Outcome    string `json:"outcome"`
	InputID    string `json:"inputMessageID"`
	DeliveryID string `json:"deliveryMessageID"`
	Epoch      uint64 `json:"epoch"`
}

type mimoInputReply struct {
	native  mimoReceiptUpdate
	childID string
	owned   bool
	// A specified outcome freezes the parts observed before native settlement.
	outcome   leapmuxv1.AgentInputTurnOutcome
	projected bool
	// turnEpoch identifies the active main run in this event stream.
	turnEpoch uint64
	items     []agent.NativeReplyItem
	parts     map[string]mimoReplyPart
	// A retracted key can still identify old text. A child row can also fall
	// back to the root transcript when the child's sink is unavailable.
	textUnproved bool
}

type mimoReplyLane struct {
	actorID string
	childID string
}

type mimoReplyPart struct {
	messageID string
	retracted bool
}

func mimoNativeReceipt(sessionID, receiptID string) agent.NativeInputReceipt {
	if receiptID == "" {
		return agent.NativeInputReceipt{}
	}
	return agent.NativeInputReceipt{SessionID: sessionID, InputID: receiptID, TurnID: receiptID}
}

// ownsMessage reports whether this receipt reached the model by the time the
// given assistant message started. MiMo orders message IDs within a run.
func (reply *mimoInputReply) ownsMessage(messageID string, record *mimoMessageRecord) bool {
	if reply.outcome != 0 || reply.parts == nil || messageID < reply.native.DeliveryID || record == nil ||
		!record.identityKnown || record.role != roleAssistant || record.actorID != reply.native.ActorID || record.summary ||
		record.sessionID != reply.native.SessionID {
		return false
	}
	if reply.childID == "" {
		return record.turnEpoch == reply.turnEpoch
	}
	// Child lanes run independently of the root's turn counter.
	return record.actor != nil && record.actor.childAgentID == reply.childID
}

func (a *Agent) awaitingPromptReceiptLocked(sessionID, actorID string) (mimoReplyLane, bool) {
	for send := range a.inputSends[sessionID] {
		if send.replyLane.actorID == actorID {
			return send.replyLane, true
		}
	}
	return mimoReplyLane{}, false
}

func (a *Agent) inputReplyLocked(update mimoReceiptUpdate, childID string) *mimoInputReply {
	if a.inputReplies == nil {
		a.inputReplies = make(map[string]*mimoInputReply)
	}
	reply := a.inputReplies[update.ID]
	if reply == nil || reply.native.SessionID != update.SessionID || reply.native.ActorID != update.ActorID || reply.childID != childID {
		reply = &mimoInputReply{native: update, childID: childID}
		a.inputReplies[update.ID] = reply
	}
	return reply
}

// confirmPromptReceipt chooses the stream observations owned by the HTTP
// request. A fast turn can finish before the server returns the receipt ID.
func (a *Agent) confirmPromptReceipt(sessionID string, send *mimoInputSend, receiptID string) agent.NativeInputReceipt {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	lane := send.replyLane
	if lane.actorID == "" {
		a.Mu.Unlock()
		return agent.NativeInputReceipt{}
	}
	var reply *mimoInputReply
	if receiptID != "" {
		reply = a.inputReplyLocked(mimoReceiptUpdate{ID: receiptID, SessionID: sessionID, ActorID: lane.actorID}, lane.childID)
		reply.owned = true
		if reply.outcome == 0 && (a.StoppedLocked() || a.sessionID != sessionID) {
			reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
	}
	send.replyLane = mimoReplyLane{}
	if _, pending := a.awaitingPromptReceiptLocked(sessionID, lane.actorID); !pending {
		for id, candidate := range a.inputReplies {
			if !candidate.owned && candidate.native.SessionID == sessionID && candidate.native.ActorID == lane.actorID {
				delete(a.inputReplies, id)
			}
		}
	}
	settled := reply != nil && reply.outcome != 0
	if settled {
		delete(a.inputReplies, receiptID)
	}
	a.Mu.Unlock()
	if settled {
		a.publishInputReply(reply)
	} else if reply != nil && lane.actorID == mainActorID {
		a.bindOpenReplyControls()
	}
	return mimoNativeReceipt(sessionID, receiptID)
}

// handleReceiptUpdated consumes native delivery and outcome metadata without a
// transcript row. The HTTP reply and the stream can reach the Worker in either
// order. The receipt identifies the same result in both paths.
func (a *Agent) handleReceiptUpdated(raw json.RawMessage) {
	var update mimoReceiptUpdate
	if json.Unmarshal(raw, &update) != nil || update.ID == "" || update.InputID == "" || update.ActorID == "" {
		return
	}
	a.Mu.Lock()
	if update.SessionID == "" || update.SessionID != a.sessionID {
		a.Mu.Unlock()
		return
	}
	reply := a.inputReplies[update.ID]
	var childID string
	if reply == nil {
		lane, pending := a.awaitingPromptReceiptLocked(update.SessionID, update.ActorID)
		if !pending {
			a.Mu.Unlock()
			return
		}
		childID = lane.childID
	} else {
		childID = reply.childID
	}
	if reply != nil && (reply.outcome != 0 || reply.native.SessionID != update.SessionID || reply.native.ActorID != update.ActorID ||
		reply.native.InputID != "" && (reply.native.InputID != update.InputID || reply.native.Epoch != update.Epoch) ||
		reply.native.DeliveryID != "" && update.DeliveryID != "" && reply.native.DeliveryID != update.DeliveryID) {
		a.Mu.Unlock()
		return
	}
	switch update.State {
	case "accepted", "claimed":
		reply = a.inputReplyLocked(update, childID)
		reply.native.InputID, reply.native.Epoch = update.InputID, update.Epoch
		nowDelivered := update.DeliveryID != "" && reply.parts == nil
		if nowDelivered {
			reply.native.DeliveryID = update.DeliveryID
			reply.turnEpoch = a.turnEpoch
			reply.parts = make(map[string]mimoReplyPart)
		}
		bind := nowDelivered && reply.owned && update.ActorID == mainActorID
		a.Mu.Unlock()
		if bind {
			a.bindOpenReplyControls()
		}
	case "settled", "cancelled", "rejected":
		reply = a.inputReplyLocked(update, childID)
		reply.projected = !reply.textUnproved && reply.ownsMessage(reply.native.DeliveryID, a.messages[reply.native.DeliveryID]) &&
			!a.hasPendingReplyTextLocked(reply)
		reply.outcome = mimoReceiptOutcome(update, reply.projected)
		owned := reply.owned
		if owned {
			delete(a.inputReplies, update.ID)
		}
		a.Mu.Unlock()
		if owned {
			a.publishInputReply(reply)
		}
	default:
		a.Mu.Unlock()
	}
}

func (a *Agent) hasPendingReplyTextLocked(reply *mimoInputReply) bool {
	for _, part := range a.parts {
		if part.kind == agent.AssembledMessageKindText && !part.skip && reply.ownsMessage(part.messageID, a.messages[part.messageID]) {
			return true
		}
	}
	return false
}

func mimoReceiptOutcome(update mimoReceiptUpdate, proved bool) leapmuxv1.AgentInputTurnOutcome {
	switch {
	case update.State == "settled" && update.Outcome == "success" && proved:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case update.Outcome == "assistant_error" || update.State == "rejected":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	case update.Outcome == "interrupted" || update.Outcome == "never_ran" && update.State == "cancelled":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	default:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
}

func (a *Agent) publishInputReply(reply *mimoInputReply) {
	receipt := mimoNativeReceipt(reply.native.SessionID, reply.native.ID)
	sink := a.sink
	if reply.childID != "" {
		sink = a.sink.ChildSink(reply.childID)
	}
	outcome := reply.outcome
	if reply.projected && outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
		items := slices.DeleteFunc(reply.items, func(item agent.NativeReplyItem) bool {
			return reply.parts[item.MessageKey].retracted
		})
		err := sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
			SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: outcome, Items: items,
			Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		})
		if agent.OutputRetained(err) {
			return
		}
		slog.Warn("retain the MiMo exact reply", "agent_id", a.AgentID(), "error", err)
		if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
	}
	if err := sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Warn("retain the MiMo input outcome", "agent_id", a.AgentID(), "error", err)
	}
}

// abandonInputReplies ends receipt observation when the stream or session can
// no longer supply a complete reply. The caller holds dispatchMu.
func (a *Agent) abandonInputReplies() {
	a.Mu.Lock()
	var replies []*mimoInputReply
	for id, reply := range a.inputReplies {
		if reply.outcome == 0 {
			reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
		if reply.owned {
			delete(a.inputReplies, id)
			replies = append(replies, reply)
		}
	}
	a.Mu.Unlock()
	for _, reply := range replies {
		a.publishInputReply(reply)
	}
}

// recordInputReplyText adds a text row once the transcript owns its write.
func (a *Agent) recordInputReplyText(record *mimoMessageRecord, messageID, key, childID string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, reply := range a.inputReplies {
		if !reply.ownsMessage(messageID, record) {
			continue
		}
		if reply.childID != childID {
			reply.textUnproved = true
			continue
		}
		if part, seen := reply.parts[key]; seen {
			reply.textUnproved = reply.textUnproved || part.retracted
		} else {
			reply.parts[key] = mimoReplyPart{messageID: messageID}
			reply.items = append(reply.items, agent.NativeReplyItem{Ordinal: int64(len(reply.items)) + 2, MessageKey: key})
		}
	}
}

// retractReplyTextLocked drops native rollback output from still-open replies.
// An empty key retracts every part of the message.
func (a *Agent) retractReplyTextLocked(messageID, key string) {
	for _, reply := range a.inputReplies {
		if !reply.ownsMessage(messageID, a.messages[messageID]) {
			continue
		}
		if key != "" {
			if part, ok := reply.parts[key]; ok && part.messageID == messageID {
				part.retracted = true
				reply.parts[key] = part
			}
			continue
		}
		for key, part := range reply.parts {
			if part.messageID == messageID {
				part.retracted = true
				reply.parts[key] = part
			}
		}
	}
}

// bindOpenReplyControls retries attribution when a delivery or its HTTP
// acknowledgment follows the control's publication.
func (a *Agent) bindOpenReplyControls() {
	a.Mu.Lock()
	controls := make(map[string]*mimoControl, len(a.controls))
	for id, control := range a.controls {
		controls[id] = control
	}
	a.Mu.Unlock()
	for id, control := range controls {
		a.bindControlReplyInputs(id, control)
	}
}

func (a *Agent) bindControlReplyInputs(requestID string, control *mimoControl) {
	a.Mu.Lock()
	record := a.messages[control.messageID]
	var ids []string
	if a.controls[requestID] == control && control.published && !control.answered && record != nil && record.sessionID == control.sessionID {
		for id, reply := range a.inputReplies {
			if reply.owned && reply.childID == "" && reply.ownsMessage(control.messageID, record) {
				ids = append(ids, id)
			}
		}
	}
	a.Mu.Unlock()
	if len(ids) == 0 {
		return
	}
	slices.Sort(ids)
	err := a.sink.SetNativeControlInputs(agent.NativeControlInputs{
		SessionID: control.sessionID, RequestID: requestID, InstanceID: control.instanceID, InputIDs: ids,
	})
	if !agent.OutputRetained(err) {
		slog.Warn("bind the MiMo control to its native inputs", "request_id", requestID, "error", err)
	}
}
