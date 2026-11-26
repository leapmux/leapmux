package providerkit

import (
	"log/slog"
	"maps"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const nativeReplyInputOrdinal int64 = 1

// NativeReplyServices persists a completed native reply turn or retires an
// individual native input receipt.
type NativeReplyServices interface {
	PublishNativeReplyTurn(turn agent.NativeReplyTurn) error
	RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error
}

// NativeReplyEntry holds the correlated receipts, items, and steer boundaries
// of one tracked native turn.
type NativeReplyEntry struct {
	wireTurnID string
	receipt    agent.NativeInputReceipt
	started    bool
	unproved   bool
	outcome    leapmuxv1.AgentInputTurnOutcome
	keys       map[string]bool
	items      []agent.NativeReplyItem
	steers     []agent.NativeReplyBoundary
}

// TurnID returns the durable turn ID for this entry.
func (e *NativeReplyEntry) TurnID() string {
	return e.receipt.TurnID
}

// ItemCount returns the number of retained reply items recorded for this entry.
func (e *NativeReplyEntry) ItemCount() int {
	return len(e.items)
}

// HasKey reports whether key was already recorded for this entry.
func (e *NativeReplyEntry) HasKey(key string) bool {
	return e.keys[key]
}

// MarkUnproved prevents this entry from publishing an exact reply on completion.
func (e *NativeReplyEntry) MarkUnproved() {
	e.unproved = true
}

// RecordItem records one persisted reply item for this entry, or marks the
// entry unproved when err indicates that the output was not retained.
func (e *NativeReplyEntry) RecordItem(key string, err error) {
	if !agent.OutputRetained(err) {
		e.unproved = true
		return
	}
	if e.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED || e.keys[key] {
		return
	}
	if e.keys == nil {
		e.keys = make(map[string]bool)
	}
	e.keys[key] = true
	e.items = append(e.items, agent.NativeReplyItem{
		Ordinal:    nativeReplyInputOrdinal + int64(len(e.items)) + 1,
		MessageKey: key,
	})
}

// NativeReplyLedger correlates in-flight turn-start responses, streamed reply
// items, steer boundaries, and turn completions for one agent.
//
// The caller serializes all method calls on a ledger (for example under its
// event dispatch mutex). Its zero value is ready for use with RequireStart
// false and an identity DurableTurnID mapping.
type NativeReplyLedger struct {
	// RequireStart requires MarkStarted before a completed turn publishes an
	// exact reply. A turn that completes without MarkStarted retires as unproved.
	RequireStart bool
	// DurableTurnID maps a wire turn ID to the durable turn ID stored in
	// receipts and reply turns. Nil uses the wire turn ID unchanged.
	DurableTurnID func(wireTurnID string) string
	// Provider identifies the provider in error logs.
	Provider string
	// AgentID identifies the agent in error logs.
	AgentID string

	starts       int
	streamClosed bool
	turns        map[string]*NativeReplyEntry
}

func (l *NativeReplyLedger) durableTurnID(wireTurnID string) string {
	if l.DurableTurnID != nil {
		return l.DurableTurnID(wireTurnID)
	}
	return wireTurnID
}

// Len returns the number of turns currently tracked by the ledger.
func (l *NativeReplyLedger) Len() int {
	return len(l.turns)
}

// PendingStarts returns the number of in-flight start requests.
func (l *NativeReplyLedger) PendingStarts() int {
	return l.starts
}

// BeginStart records that a turn-start request is in flight so turns observed
// on the event stream before its response arrives stay correlated.
func (l *NativeReplyLedger) BeginStart() {
	l.starts++
}

// FinishStart binds the receipt returned by a turn-start request and publishes
// the turn if it already completed. When no start requests remain in flight, it
// sweeps untracked turns in sorted wire turn ID order: turns without steers are
// discarded, and completed steer-only turns publish.
func (l *NativeReplyLedger) FinishStart(wireTurnID string, receipt agent.NativeInputReceipt, sink NativeReplyServices) {
	if receipt.InputID != "" && wireTurnID != "" {
		reply := l.Turn(wireTurnID)
		if reply == nil {
			reply = l.Register(wireTurnID)
		}
		reply.receipt = receipt
		if l.streamClosed && reply.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
			reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
		l.publish(reply, sink)
	}
	if l.starts > 0 {
		l.starts--
	}
	if l.starts == 0 {
		for _, id := range slices.Sorted(maps.Keys(l.turns)) {
			reply := l.turns[id]
			if reply.receipt.InputID == "" {
				if len(reply.steers) == 0 {
					delete(l.turns, id)
				} else {
					l.publish(reply, sink)
				}
			}
		}
	}
}

// Lookup returns an already tracked entry for wireTurnID without creating one,
// even when a start request is in flight.
func (l *NativeReplyLedger) Lookup(wireTurnID string) *NativeReplyEntry {
	if wireTurnID == "" {
		return nil
	}
	return l.turns[wireTurnID]
}

// Turn returns the tracked entry for wireTurnID, creating one while a start
// request is in flight. It returns nil when wireTurnID is empty or untracked
// with no start request in flight.
func (l *NativeReplyLedger) Turn(wireTurnID string) *NativeReplyEntry {
	if wireTurnID == "" {
		return nil
	}
	if existing := l.turns[wireTurnID]; existing != nil {
		return existing
	}
	if l.starts == 0 {
		return nil
	}
	return l.Register(wireTurnID)
}

// Register returns the entry for wireTurnID, creating one even when no start
// request is in flight. It returns nil for an empty wireTurnID.
func (l *NativeReplyLedger) Register(wireTurnID string) *NativeReplyEntry {
	if wireTurnID == "" {
		return nil
	}
	if existing := l.turns[wireTurnID]; existing != nil {
		return existing
	}
	if l.turns == nil {
		l.turns = make(map[string]*NativeReplyEntry)
	}
	reply := &NativeReplyEntry{
		wireTurnID: wireTurnID,
		receipt:    agent.NativeInputReceipt{TurnID: l.durableTurnID(wireTurnID)},
	}
	l.turns[wireTurnID] = reply
	return reply
}

// MarkStarted marks wireTurnID as having observed its native start notification.
func (l *NativeReplyLedger) MarkStarted(wireTurnID string) {
	if reply := l.Turn(wireTurnID); reply != nil {
		reply.started = true
	}
}

// MarkUnproved marks wireTurnID as unable to publish an exact reply on completion.
func (l *NativeReplyLedger) MarkUnproved(wireTurnID string) {
	if reply := l.Turn(wireTurnID); reply != nil {
		reply.unproved = true
	}
}

// RecordSteerBoundary attaches a native steer receipt to wireTurnID at the
// current item boundary. It returns false without modifying the ledger when the
// receipt is incomplete, mismatched, or arrives after turn completion.
func (l *NativeReplyLedger) RecordSteerBoundary(wireTurnID string, receipt agent.NativeInputReceipt) bool {
	if wireTurnID == "" || receipt.SessionID == "" || receipt.InputID == "" {
		return false
	}
	if receipt.TurnID != l.durableTurnID(wireTurnID) {
		return false
	}
	reply := l.turns[wireTurnID]
	if reply != nil {
		if reply.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED ||
			reply.receipt.TurnID != receipt.TurnID ||
			reply.receipt.SessionID != "" && reply.receipt.SessionID != receipt.SessionID {
			return false
		}
	} else {
		reply = l.Register(wireTurnID)
	}
	reply.receipt.SessionID = receipt.SessionID
	reply.steers = append(reply.steers, agent.NativeReplyBoundary{
		InputID:      receipt.InputID,
		AfterOrdinal: nativeReplyInputOrdinal + int64(len(reply.items)),
	})
	return true
}

// RecordItem records one persisted reply item for wireTurnID, or marks the turn
// unproved when err indicates that the output was not retained.
func (l *NativeReplyLedger) RecordItem(wireTurnID, key string, err error) {
	if reply := l.Turn(wireTurnID); reply != nil {
		reply.RecordItem(key, err)
	}
}

// Finish records the final outcome of wireTurnID and publishes or retires it
// when its receipt ownership is settled.
func (l *NativeReplyLedger) Finish(wireTurnID string, outcome leapmuxv1.AgentInputTurnOutcome, sink NativeReplyServices) {
	reply := l.Turn(wireTurnID)
	if reply == nil || reply.outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		return
	}
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	reply.outcome = outcome
	l.publish(reply, sink)
}

// CloseStream marks the native event stream as closed so any start response
// that finishes afterward without an observed completion retires as unproved.
func (l *NativeReplyLedger) CloseStream() {
	l.streamClosed = true
}

// RetireAllAfterExit closes the stream and settles every tracked turn in sorted
// wire turn ID order. Already observed completions stay available for start
// responses that are still in flight.
func (l *NativeReplyLedger) RetireAllAfterExit(sink NativeReplyServices) {
	l.streamClosed = true
	for _, id := range slices.Sorted(maps.Keys(l.turns)) {
		reply := l.turns[id]
		if reply.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
			reply.outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
		}
		l.publish(reply, sink)
	}
}

// RetireInput retires one native input receipt against sink and logs any
// non-retained error.
func (l *NativeReplyLedger) RetireInput(sink NativeReplyServices, receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	if sink == nil {
		return
	}
	if err := sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Error("record the native reply outcome", "provider", l.Provider, "agent_id", l.AgentID, "error", err)
	}
}

func (l *NativeReplyLedger) publish(reply *NativeReplyEntry, sink NativeReplyServices) {
	receipt, outcome := reply.receipt, reply.outcome
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED ||
		receipt.InputID == "" && (l.starts > 0 || len(reply.steers) == 0) {
		return
	}
	delete(l.turns, reply.wireTurnID)
	if sink == nil {
		return
	}
	inputs := reply.steers
	if receipt.InputID != "" {
		inputs = append([]agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: nativeReplyInputOrdinal}}, inputs...)
	}
	if outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED {
		if (!l.RequireStart || reply.started) && !reply.unproved {
			err := sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
				SessionID: receipt.SessionID,
				TurnID:    receipt.TurnID,
				Outcome:   outcome,
				Inputs:    inputs,
				Items:     reply.items,
			})
			if agent.OutputRetained(err) {
				return
			}
			slog.Error("store the exact native reply", "provider", l.Provider, "agent_id", l.AgentID, "error", err)
		}
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	}
	for _, input := range inputs {
		receipt.InputID = input.InputID
		l.RetireInput(sink, receipt, outcome)
	}
}
