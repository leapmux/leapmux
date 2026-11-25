package providerkit

import (
	"slices"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// SteerTicket identifies one steer within a ledger.
type SteerTicket uint64

type steerEntry struct {
	ticket SteerTicket
	key    string
	id     string
	state  agent.SteerOutcome
}

type identifiedSteer struct {
	ticket     SteerTicket
	resolution agent.SteerResolution
}

// SteerLedger reports count-settled steers in send order.
// Its zero value is ready for use. Every method permits concurrent calls.
type SteerLedger struct {
	mu       sync.Mutex
	next     SteerTicket
	entries  []steerEntry
	settling bool
}

// Add records a steer before its native write and returns its ticket.
func (l *SteerLedger) Add(key string) SteerTicket {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	l.entries = append(l.entries, steerEntry{ticket: l.next, key: key})
	return l.next
}

// IdentifiedSteerLedger tracks steers that a provider resolves by native receipt
// rather than by ordered count. Its zero value is ready for use. Every method
// permits concurrent calls.
type IdentifiedSteerLedger struct {
	mu      sync.Mutex
	next    SteerTicket
	entries []identifiedSteer
}

// Add records the session and native input that a native echo must match.
// Set the turn ID when only that turn can read the input. Register before the write.
func (l *IdentifiedSteerLedger) Add(resolution agent.SteerResolution) SteerTicket {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	l.entries = append(l.entries, identifiedSteer{ticket: l.next, resolution: resolution})
	return l.next
}

// TakeEcho resolves one matching dispatch and removes it from the ledger.
func (l *IdentifiedSteerLedger) TakeEcho(receipt agent.NativeInputReceipt) (agent.SteerResolution, bool) {
	if receipt.SessionID == "" || receipt.InputID == "" || receipt.TurnID == "" {
		return agent.SteerResolution{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, entry := range l.entries {
		submitted := entry.resolution.Receipt
		if submitted.SessionID == receipt.SessionID && submitted.InputID == receipt.InputID &&
			(submitted.TurnID == "" || submitted.TurnID == receipt.TurnID) {
			l.entries = slices.Delete(l.entries, i, i+1)
			entry.resolution.Outcome = agent.SteerOutcomeEchoed
			entry.resolution.Receipt = receipt
			return entry.resolution, true
		}
	}
	return agent.SteerResolution{}, false
}

// TakeUnechoed removes pending dispatches for this native turn.
// An empty turn ID selects all pending dispatches in the session.
func (l *IdentifiedSteerLedger) TakeUnechoed(sessionID, turnID string) []agent.SteerResolution {
	l.mu.Lock()
	defer l.mu.Unlock()
	var pending []agent.SteerResolution
	l.entries = slices.DeleteFunc(l.entries, func(entry identifiedSteer) bool {
		receipt := entry.resolution.Receipt
		if receipt.SessionID != sessionID || (turnID != "" && receipt.TurnID != turnID) {
			return false
		}
		pending = append(pending, entry.resolution)
		return true
	})
	return pending
}

// Forget removes a failed steer call.
func (l *IdentifiedSteerLedger) Forget(t SteerTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = slices.DeleteFunc(l.entries, func(entry identifiedSteer) bool { return entry.ticket == t })
}

// Reset forgets every entry. Ticket numbers stay unique across resets.
func (l *IdentifiedSteerLedger) Reset() {
	l.mu.Lock()
	l.entries = nil
	l.mu.Unlock()
}

// SetID records the native ID that the steer reply supplied.
func (l *SteerLedger) SetID(t SteerTicket, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.entries {
		if l.entries[i].ticket == t {
			l.entries[i].id = id
			return
		}
	}
}

// ID returns the native ID of a retained ticket, or an empty string.
func (l *SteerLedger) ID(t SteerTicket) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if entry.ticket == t {
			return entry.id
		}
	}
	return ""
}

// Echo marks a pending ticket as read by the model.
func (l *SteerLedger) Echo(t SteerTicket) {
	l.resolve(func(entry steerEntry) bool { return entry.ticket == t }, agent.SteerOutcomeEchoed, 1)
}

// EchoKey marks the oldest pending steer with the supplied echo key.
func (l *SteerLedger) EchoKey(key string) bool {
	return l.resolve(func(entry steerEntry) bool { return entry.key == key }, agent.SteerOutcomeEchoed, 1) == 1
}

// EchoID marks the oldest pending steer with the supplied nonempty native ID.
func (l *SteerLedger) EchoID(id string) bool {
	if id == "" {
		return false
	}
	return l.resolve(func(entry steerEntry) bool { return entry.id == id }, agent.SteerOutcomeEchoed, 1) == 1
}

// EchoOldest marks up to count pending steers as read and returns their count.
func (l *SteerLedger) EchoOldest(count int) int {
	if count <= 0 {
		return 0
	}
	return l.resolve(func(steerEntry) bool { return true }, agent.SteerOutcomeEchoed, count)
}

// Drop marks a pending ticket as dropped before the model read it.
func (l *SteerLedger) Drop(t SteerTicket) {
	l.resolve(func(entry steerEntry) bool { return entry.ticket == t }, agent.SteerOutcomeDropped, 1)
}

// Uncertain marks a pending ticket whose delivery the provider cannot prove.
func (l *SteerLedger) Uncertain(t SteerTicket) {
	l.resolve(func(entry steerEntry) bool { return entry.ticket == t }, agent.SteerOutcomeUncertain, 1)
}

// DropID marks the oldest pending steer with the supplied nonempty native ID as dropped.
func (l *SteerLedger) DropID(id string) bool {
	if id == "" {
		return false
	}
	return l.resolve(func(entry steerEntry) bool { return entry.id == id }, agent.SteerOutcomeDropped, 1) == 1
}

// DropAll marks every pending ticket as dropped and returns their count.
func (l *SteerLedger) DropAll() int {
	return l.resolve(func(steerEntry) bool { return true }, agent.SteerOutcomeDropped, -1)
}

func (l *SteerLedger) resolve(match func(steerEntry) bool, state agent.SteerOutcome, count int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	resolved := 0
	for i := range l.entries {
		if count >= 0 && resolved >= count {
			break
		}
		if l.entries[i].state == agent.SteerOutcomePending && match(l.entries[i]) {
			l.entries[i].state = state
			resolved++
		}
	}
	return resolved
}

// Forget removes a failed steer call. The queue already knows the failure.
// Call Settle afterward to report later entries that this removal releases.
func (l *SteerLedger) Forget(t SteerTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = slices.DeleteFunc(l.entries, func(entry steerEntry) bool { return entry.ticket == t })
}

// Pending returns unresolved tickets in send order.
func (l *SteerLedger) Pending() []SteerTicket {
	l.mu.Lock()
	defer l.mu.Unlock()
	var pending []SteerTicket
	for _, entry := range l.entries {
		if entry.state == agent.SteerOutcomePending {
			pending = append(pending, entry.ticket)
		}
	}
	return pending
}

// PendingSteer is a copy of one unresolved steer.
type PendingSteer struct {
	Ticket SteerTicket
	Key    string
	ID     string
}

// PendingEntries returns unresolved steers in send order, with their keys and native IDs.
func (l *SteerLedger) PendingEntries() []PendingSteer {
	l.mu.Lock()
	defer l.mu.Unlock()
	var pending []PendingSteer
	for _, entry := range l.entries {
		if entry.state == agent.SteerOutcomePending {
			pending = append(pending, PendingSteer{Ticket: entry.ticket, Key: entry.key, ID: entry.id})
		}
	}
	return pending
}

// Settle reports resolved runs from the head and stops at the first pending entry.
// One caller drains reports. Concurrent or nested calls leave that caller in control.
// Sink calls run outside mu, so a sink can read or update this ledger.
func (l *SteerLedger) Settle(sink agent.SteerEchoServices) {
	l.mu.Lock()
	if l.settling {
		l.mu.Unlock()
		return
	}
	l.settling = true
	for {
		if len(l.entries) == 0 || l.entries[0].state == agent.SteerOutcomePending {
			l.settling = false
			l.mu.Unlock()
			return
		}
		state := l.entries[0].state
		count := 1
		for count < len(l.entries) && l.entries[count].state == state {
			count++
		}
		l.entries = slices.Delete(l.entries, 0, count)
		l.mu.Unlock()
		switch state {
		case agent.SteerOutcomePending:
			panic("cannot report a pending steer")
		case agent.SteerOutcomeEchoed:
			sink.ReportSteersEchoed(count)
		case agent.SteerOutcomeDropped:
			sink.ReportSteersDropped(count)
		case agent.SteerOutcomeUncertain:
			sink.ReportSteersUncertain(count)
		}
		l.mu.Lock()
	}
}

// Reset forgets every entry. Ticket numbers stay unique across resets.
func (l *SteerLedger) Reset() {
	l.mu.Lock()
	l.entries = nil
	l.mu.Unlock()
}
