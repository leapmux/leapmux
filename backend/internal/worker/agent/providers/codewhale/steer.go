package codewhale

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// The native HTTP reply can acknowledge only a queued steer. turn.steered proves that the model read it.
// Native receipts identify an item. The queue retains the original input until the shared ledger reports its outcome.
//
// The runtime writes steer outcomes on a detached task. A queued record can lag an actual model read.
// Only completed and canceled records prove an outcome. An unresolved delivery becomes uncertain when its turn ends.
// The worker does not replay that input automatically.

// steerAbsorbCapacity limits recent receipt identities and refused drops that the worker remembers.
const steerAbsorbCapacity = 64

type steerKey struct {
	turnID string
	input  string
}

type steerOutputKind uint8

const (
	steerGeneratedOutput steerOutputKind = iota
	steerToolOutput
)

type steerOutputKey struct {
	kind steerOutputKind
	id   string
}

// steerTicket retains native identity evidence. The shared queue owns the input and its attachments.
type steerTicket struct {
	key            steerKey
	ledgerID       providerkit.SteerTicket
	nativeID       string
	before         map[string]bool
	baselineOK     bool
	replied        bool
	verdict        agent.SteerOutcome
	preceding      map[steerOutputKey]bool
	waitRequestEnd bool
}

// codewhaleSteers holds unresolved submissions. The caller holds Agent.Mu.
// Ledger reports run outside Agent.Mu.
type codewhaleSteers struct {
	open     []*steerTicket
	ledger   providerkit.SteerLedger
	absorbed []steerKey
	observed []string
}

func (s *codewhaleSteers) begin(turnID, prompt string, before map[string]bool, baselineOK bool) *steerTicket {
	key := steerKey{turnID: turnID, input: strings.TrimSpace(prompt)}
	ticket := &steerTicket{key: key, before: before, baselineOK: baselineOK, ledgerID: s.ledger.Add(turnID + "\x00" + key.input)}
	s.open = append(s.open, ticket)
	return ticket
}

func (s *codewhaleSteers) remove(ticket *steerTicket) {
	s.open = slices.DeleteFunc(s.open, func(candidate *steerTicket) bool { return candidate == ticket })
}

func (s *codewhaleSteers) seen(id string) bool { return id != "" && slices.Contains(s.observed, id) }

func (s *codewhaleSteers) observe(id string) {
	if id == "" || s.seen(id) {
		return
	}
	if len(s.observed) == steerAbsorbCapacity {
		s.observed = slices.Delete(s.observed, 0, 1)
	}
	s.observed = append(s.observed, id)
}

// match uses an exact known ID first. At most one accepted steer can lack an ID.
// That restriction prevents a receipt from selecting the wrong identical input.
func (s *codewhaleSteers) match(key steerKey, id string) *steerTicket {
	if id == "" || s.seen(id) {
		return nil
	}
	for _, ticket := range s.open {
		if ticket.nativeID == id {
			if ticket.key == key {
				return ticket
			}
			return nil
		}
	}
	for _, ticket := range s.open {
		if ticket.nativeID == "" && ticket.key == key && !ticket.before[id] {
			return ticket
		}
	}
	return nil
}

func (s *codewhaleSteers) unidentified() bool {
	for _, ticket := range s.open {
		if ticket.replied && ticket.nativeID == "" {
			return true
		}
	}
	return false
}

func (s *codewhaleSteers) absorb(key steerKey) {
	if len(s.absorbed) == steerAbsorbCapacity {
		s.absorbed = slices.Delete(s.absorbed, 0, 1)
	}
	s.absorbed = append(s.absorbed, key)
}

func (s *codewhaleSteers) takeAbsorbed(key steerKey) bool {
	for i, candidate := range s.absorbed {
		if candidate == key {
			s.absorbed = slices.Delete(s.absorbed, i, i+1)
			return true
		}
	}
	return false
}

func (s *codewhaleSteers) awaiting(turnID string) []*steerTicket {
	var tickets []*steerTicket
	for _, ticket := range s.open {
		if ticket.verdict == agent.SteerOutcomePending && (turnID == "" || ticket.key.turnID == turnID) {
			tickets = append(tickets, ticket)
		}
	}
	return tickets
}

func (s *codewhaleSteers) resolve(ticket *steerTicket, id string, outcome agent.SteerOutcome) {
	if ticket.verdict != agent.SteerOutcomePending {
		return
	}
	ticket.verdict = outcome
	if id != "" {
		ticket.nativeID = id
		s.ledger.SetID(ticket.ledgerID, id)
		s.observe(id)
	}
	switch outcome {
	case agent.SteerOutcomeEchoed:
		if len(ticket.preceding) > 0 || ticket.waitRequestEnd {
			return
		}
		s.ledger.Echo(ticket.ledgerID)
	case agent.SteerOutcomeDropped:
		s.ledger.Drop(ticket.ledgerID)
	case agent.SteerOutcomeUncertain:
		s.ledger.Uncertain(ticket.ledgerID)
	case agent.SteerOutcomePending:
		panic("cannot resolve a pending steer")
	}
	s.remove(ticket)
}

// bindSteerItem identifies the new native user item without claiming that the model read it.
// Native item IDs that existed before the write cannot identify this submission.
// The caller holds dispatchMu, and no other steer request starts before this call returns.
func (a *Agent) bindSteerItem(ticket *steerTicket) {
	a.Mu.Lock()
	eligible := ticket.verdict == agent.SteerOutcomePending && ticket.nativeID == "" && ticket.baselineOK
	a.Mu.Unlock()
	if !eligible {
		return
	}
	items, ok := readTurnSteers(a.store, ticket.key.turnID, ticket.before)
	if !ok {
		return
	}
	var id string
	for _, item := range items {
		if strings.TrimSpace(item.Detail) != ticket.key.input {
			continue
		}
		if id != "" {
			return
		}
		id = item.ID
	}
	if id == "" {
		return
	}
	a.Mu.Lock()
	if ticket.verdict == agent.SteerOutcomePending && ticket.nativeID == "" {
		ticket.nativeID = id
		a.steers.ledger.SetID(ticket.ledgerID, id)
	}
	a.Mu.Unlock()
}

// settleSteerReply gives native evidence precedence over the HTTP result.
func (a *Agent) settleSteerReply(ticket *steerTicket, err error) error {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.bindSteerItem(ticket)
	a.Mu.Lock()
	before := a.steerableLocked()
	var result error
	switch ticket.verdict {
	case agent.SteerOutcomeEchoed:
	case agent.SteerOutcomeDropped:
		result = agent.ErrNoActiveTurn
	case agent.SteerOutcomeUncertain:
		result = fmt.Errorf("%w: Codewhale could not prove the steer outcome", agent.ErrDeliveryUncertain)
	case agent.SteerOutcomePending:
		if err == nil {
			ticket.replied = true
		} else {
			a.steers.remove(ticket)
			a.steers.ledger.Forget(ticket.ledgerID)
			var status *providerkit.HTTPStatusError
			if errors.As(err, &status) {
				switch status.StatusCode {
				case httpStatusConflict, httpStatusNotFound, httpStatusBadRequest:
					a.steers.absorb(ticket.key)
					result = agent.ErrNoActiveTurn
				default:
					result = err
				}
			} else {
				result = fmt.Errorf("%w: Codewhale did not confirm the steer: %v", agent.ErrDeliveryUncertain, err)
			}
		}
	}
	changed := before != a.steerableLocked()
	a.Mu.Unlock()
	if !a.IsDiscardingOutput() {
		a.steers.ledger.Settle(a.sink)
	}
	if changed {
		a.PublishTurnActive()
	}
	return result
}

type steerEventPayload struct {
	TurnID string `json:"turn_id"`
	Input  string `json:"input"`
	Item   struct {
		ID string `json:"id"`
	} `json:"item"`
}

func steerEventKey(env codewhaleEnvelope) (steerKey, string, bool) {
	var payload steerEventPayload
	if json.Unmarshal(env.Payload, &payload) != nil {
		return steerKey{}, "", false
	}
	if env.TurnID != "" && payload.TurnID != "" && env.TurnID != payload.TurnID {
		return steerKey{}, "", false
	}
	if env.ItemID != "" && payload.Item.ID != "" && env.ItemID != payload.Item.ID {
		return steerKey{}, "", false
	}
	turnID, id := env.TurnID, env.ItemID
	if turnID == "" {
		turnID = payload.TurnID
	}
	if id == "" {
		id = payload.Item.ID
	}
	return steerKey{turnID: turnID, input: strings.TrimSpace(payload.Input)}, id, turnID != ""
}

func (a *Agent) handleSteerDelivered(env codewhaleEnvelope) {
	key, id, ok := steerEventKey(env)
	if !ok {
		return
	}
	preceding := a.steerPredecessors()
	a.Mu.Lock()
	before := a.steerableLocked()
	ticket := a.steers.match(key, id)
	if ticket != nil {
		ticket.preceding = preceding
		ticket.waitRequestEnd = a.modelRequestObserved
		a.steers.resolve(ticket, id, agent.SteerOutcomeEchoed)
	}
	changed := before != a.steerableLocked()
	a.Mu.Unlock()
	a.steers.ledger.Settle(a.sink)
	if changed {
		a.PublishTurnActive()
	}
}

func (a *Agent) handleSteerDropped(env codewhaleEnvelope) {
	key, id, ok := steerEventKey(env)
	if !ok {
		a.persistNotification(env)
		return
	}
	a.Mu.Lock()
	before := a.steerableLocked()
	known := a.steers.seen(id)
	ticket := a.steers.match(key, id)
	if ticket != nil {
		a.steers.resolve(ticket, id, agent.SteerOutcomeDropped)
		known = true
	} else if !known {
		known = a.steers.takeAbsorbed(key)
	}
	changed := before != a.steerableLocked()
	a.Mu.Unlock()
	a.steers.ledger.Settle(a.sink)
	if changed {
		a.PublishTurnActive()
	}
	if !known {
		a.persistNotification(env)
	}
}

// settleSteersOfTurn reports each exact native record before the final row releases the queue.
// A queued or unreadable record proves no outcome and must not trigger automatic replay.
func (a *Agent) settleSteersOfTurn(turnID string) { a.settleStoredSteers(turnID) }

func (a *Agent) settleSteersAfterExit() { a.settleStoredSteers("") }

func (a *Agent) settleStoredSteers(turnID string) {
	a.Mu.Lock()
	tickets := a.steers.awaiting(turnID)
	a.Mu.Unlock()
	if a.IsDiscardingOutput() {
		a.Mu.Lock()
		a.steers.open = nil
		a.steers.ledger.Reset()
		a.Mu.Unlock()
		return
	}
	turnItems := make(map[string]map[string]bool)
	for _, ticket := range tickets {
		a.bindSteerItem(ticket)
		a.Mu.Lock()
		id := ticket.nativeID
		a.Mu.Unlock()
		outcome := agent.SteerOutcomeUncertain
		ids, read := turnItems[ticket.key.turnID]
		if !read {
			ids, _ = readTurnItemIDs(a.store, ticket.key.turnID)
			turnItems[ticket.key.turnID] = ids
		}
		if id != "" && ids[id] {
			if item, ok := readSteerItem(a.store, ticket.key.turnID, id); ok && strings.TrimSpace(item.Detail) == ticket.key.input {
				switch item.Status {
				case itemStatusCompleted:
					outcome = agent.SteerOutcomeEchoed
				case itemStatusCanceled:
					outcome = agent.SteerOutcomeDropped
				}
			}
		}
		a.Mu.Lock()
		if ticket.verdict == agent.SteerOutcomePending {
			a.steers.resolve(ticket, id, outcome)
			if outcome != agent.SteerOutcomeEchoed {
				a.steers.absorb(ticket.key)
			}
		}
		a.Mu.Unlock()
	}
	a.steers.ledger.Settle(a.sink)
}

type storedTurn struct {
	ID      string   `json:"id"`
	ItemIDs []string `json:"item_ids"`
}

type storedItem struct {
	ID     string `json:"id"`
	TurnID string `json:"turn_id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func nativeRecordID(id string) bool { return id != "" && !strings.ContainsAny(id, `/\`) }

func readTurnItemIDs(store codewhaleStore, turnID string) (map[string]bool, bool) {
	if store.dir == "" || !nativeRecordID(turnID) {
		return nil, false
	}
	var turn storedTurn
	if err := sessionstore.ReadSidecarFile(filepath.Join(store.runtimeDir(), "turns", turnID+".json"), storeRecordMaxBytes, func(data []byte) error {
		return json.Unmarshal(data, &turn)
	}); err != nil || turn.ID != turnID || turn.ItemIDs == nil {
		return nil, false
	}
	ids := make(map[string]bool, len(turn.ItemIDs))
	for _, id := range turn.ItemIDs {
		if !nativeRecordID(id) || ids[id] {
			return nil, false
		}
		ids[id] = true
	}
	return ids, true
}

func readSteerItem(store codewhaleStore, turnID, id string) (storedItem, bool) {
	if store.dir == "" || !nativeRecordID(turnID) || !nativeRecordID(id) {
		return storedItem{}, false
	}
	var item storedItem
	if err := sessionstore.ReadSidecarFile(filepath.Join(store.runtimeDir(), "items", id+".json"), storeRecordMaxBytes, func(data []byte) error {
		return json.Unmarshal(data, &item)
	}); err != nil || item.ID != id || item.TurnID != turnID {
		return storedItem{}, false
	}
	return item, item.Kind == itemKindUserMessage
}

// readTurnSteers reads new native items only. An unreadable new record makes the identity ambiguous.
func readTurnSteers(store codewhaleStore, turnID string, before map[string]bool) ([]storedItem, bool) {
	ids, ok := readTurnItemIDs(store, turnID)
	if !ok {
		return nil, false
	}
	var items []storedItem
	for id := range ids {
		if before[id] {
			continue
		}
		var item storedItem
		if err := sessionstore.ReadSidecarFile(filepath.Join(store.runtimeDir(), "items", id+".json"), storeRecordMaxBytes, func(data []byte) error {
			return json.Unmarshal(data, &item)
		}); err != nil || item.ID != id || item.TurnID != turnID {
			return nil, false
		}
		if item.Kind == itemKindUserMessage {
			items = append(items, item)
		}
	}
	return items, true
}

// finishSteerCall releases a final native turn row after its outstanding HTTP request settles.
func (a *Agent) finishSteerCall(ticket *steerTicket) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	if a.steerCall == ticket {
		a.steerCall = nil
	}
	ready := a.steerCall == nil
	a.Mu.Unlock()
	if ready {
		a.drainDeferredTurnEnd()
	}
}

func (a *Agent) drainDeferredTurnEnd() {
	end := a.pendingTurnEnd
	if end == nil {
		return
	}
	a.pendingTurnEnd = nil
	a.completeTurn(*end)
	deferred := a.deferredEvents
	a.deferredEvents = nil
	for _, env := range deferred {
		a.dispatchAdmittedFrame(env)
	}
}

// openGenerationIDs includes native starts that emitted no delta.
// Pending writes remain in the shared output queue and have no open scope here.
func (a *Agent) openGenerationIDs() []string {
	ids := a.generationBuffer.ScopeIDs()
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	var extra []string
	for _, id := range a.openGeneratedItems {
		if !seen[id] {
			seen[id] = true
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	return append(ids, extra...)
}

// steerPredecessors captures the native output that precedes this receipt.
// CodeWhale can emit turn.steered before the preceding generated item's final event.
func (a *Agent) steerPredecessors() map[steerOutputKey]bool {
	preceding := make(map[steerOutputKey]bool)
	for _, id := range a.openGenerationIDs() {
		preceding[steerOutputKey{kind: steerGeneratedOutput, id: id}] = true
	}
	a.Mu.Lock()
	for id := range a.tools.bySpan {
		preceding[steerOutputKey{kind: steerToolOutput, id: id}] = true
	}
	a.Mu.Unlock()
	return preceding
}

// releaseSteerOutputs reports read inputs only after their preceding native rows enter the ordered sink.
func (a *Agent) releaseSteerOutputs(keys []steerOutputKey) {
	discarding := a.IsDiscardingOutput()
	a.Mu.Lock()
	kept := a.steers.open[:0]
	for _, ticket := range a.steers.open {
		for _, key := range keys {
			delete(ticket.preceding, key)
		}
		if ticket.verdict == agent.SteerOutcomeEchoed && len(ticket.preceding) == 0 && !ticket.waitRequestEnd {
			if discarding {
				a.steers.ledger.Forget(ticket.ledgerID)
			} else {
				a.steers.ledger.Echo(ticket.ledgerID)
			}
		} else {
			kept = append(kept, ticket)
		}
	}
	clear(a.steers.open[len(kept):])
	a.steers.open = kept
	a.Mu.Unlock()
	if !discarding {
		a.steers.ledger.Settle(a.sink)
	}
}

func generatedSteerOutputs(ids []string) []steerOutputKey {
	keys := make([]steerOutputKey, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, steerOutputKey{kind: steerGeneratedOutput, id: id})
	}
	return keys
}

// finishSteerRequestBoundary releases receipts after the preceding model request finishes.
func (a *Agent) finishSteerRequestBoundary() {
	a.Mu.Lock()
	for _, ticket := range a.steers.open {
		ticket.waitRequestEnd = false
	}
	a.Mu.Unlock()
	a.releaseSteerOutputs(nil)
}
