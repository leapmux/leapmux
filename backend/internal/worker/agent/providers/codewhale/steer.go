package codewhale

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Steering, and the steers that the runtime drops.
//
// POST /v1/threads/{id}/turns/{turn_id}/steer puts the text into the engine's
// mailbox. The engine settles each steer exactly once (`PendingSteer`, #6276):
//
//   - Accepted: it committed the text into the turn at a step boundary, and the
//     model reads it. The runtime emits `turn.steered`.
//   - Dropped: the turn ended, or moved on, before a step boundary took the
//     text. The model never reads it. The runtime emits `turn.steer_dropped`.
//
// From 0.10.0 the route waits 2 s for that verdict (STEER_SETTLE_WAIT). An
// accepted steer answers 200, and a dropped one answers 409. A steer that
// waits longer -- behind a tool call, or a model request -- answers 200 with
// the text still queued, and its verdict arrives later on the event stream. The
// runtime emits the drop event for every drop, the 409 ones included.
//
// A steer that waits in the mailbox when its turn ends is dropped only when the
// engine next reads the mailbox, which is the start of the NEXT turn. Until then
// no event states it. The runtime's store states it at once: a steer item that
// the engine never committed stays `queued` or turns `canceled`, and the
// runtime's own history rebuild leaves both out because the model never saw
// them. So at each turn end, and at a process exit, the agent reads that record
// for every steer whose verdict it still waits for.
//
// The record can lag the engine in one window. The runtime writes a commit on a
// task of its own, while the turn makes the model request that follows the
// commit, so the record states the commit before the turn can end unless that
// write takes longer than the whole request. The agent reads the record as the
// runtime's history rebuild reads it, and trusts it.
//
// 0.9.13 records each steer as delivered at once, emits `turn.steered`, and has
// no drop event.
//
// The input queue records a steer as delivered when SteerInput returns nil. So
// the agent keeps a ticket for each steer that it sent, and:
//
//   - A steer whose drop LeapMux learns before it answers the queue is refused
//     with ErrNoActiveTurn, and the queue sends the text as a turn of its own.
//   - A steer whose drop LeapMux learns after it answered the queue is handed
//     back to the queue through the sink's RequeueDroppedInput, as the reader's
//     next message.
//   - A drop that the queue already acts on -- a refused steer, or one handed
//     back -- draws no row, because the runtime's reason tells the reader to
//     resend a message that LeapMux sends again itself.
//   - A drop that nothing acts on draws the runtime's event as a row, so the
//     reader learns that the model never read the text.

// steerAbsorbCapacity limits how many drops the agent remembers as already
// acted on. A drop event arrives at most one turn after its steer, and a turn
// takes few steers, so a short memory is enough, and it keeps the ring from
// growing with the session.
const steerAbsorbCapacity = 64

// steerVerdict is what an event stated about a steer before the route answered.
type steerVerdict int

const (
	steerVerdictNone steerVerdict = iota
	steerVerdictDelivered
	steerVerdictDropped
)

// steerKey identifies a steer as the runtime's events state it: the turn, and
// the prompt as the route trims it.
type steerKey struct {
	turnID string
	input  string
}

// steerTicket is one steer that LeapMux sent and whose fate is not settled.
type steerTicket struct {
	// dropID identifies the steer within the agent. The hand-back carries it.
	dropID string
	key    steerKey
	// content and attachments are what the reader sent, which a hand-back queues
	// again as they were.
	content     string
	attachments []*leapmuxv1.Attachment
	// replied is true once the route answered 2xx and SteerInput told the queue
	// that the steer was delivered.
	replied bool
	// verdict is what an event stated while the route had not answered yet.
	verdict steerVerdict
}

// codewhaleSteers tracks the steers that LeapMux sent. The caller holds Mu.
type codewhaleSteers struct {
	open []*steerTicket
	// absorbed holds the drops that draw no row, oldest first.
	absorbed []steerKey
	seq      uint64
}

// begin records a steer that SteerInput is about to send.
func (s *codewhaleSteers) begin(turnID, prompt, content string, attachments []*leapmuxv1.Attachment) *steerTicket {
	s.seq++
	ticket := &steerTicket{
		dropID:      turnID + "/" + strconv.FormatUint(s.seq, 10),
		key:         steerKey{turnID: turnID, input: strings.TrimSpace(prompt)},
		content:     content,
		attachments: attachments,
	}
	s.open = append(s.open, ticket)
	return ticket
}

// match returns the oldest open steer with key that no event settled yet, or
// nil.
func (s *codewhaleSteers) match(key steerKey) *steerTicket {
	for _, ticket := range s.open {
		if ticket.key == key && ticket.verdict == steerVerdictNone {
			return ticket
		}
	}
	return nil
}

// remove forgets one steer.
func (s *codewhaleSteers) remove(ticket *steerTicket) {
	for i, open := range s.open {
		if open == ticket {
			s.open = append(s.open[:i], s.open[i+1:]...)
			return
		}
	}
}

// absorb records a drop that the queue acts on already, so its event draws no
// row.
func (s *codewhaleSteers) absorb(key steerKey) {
	if len(s.absorbed) == steerAbsorbCapacity {
		s.absorbed = s.absorbed[1:]
	}
	s.absorbed = append(s.absorbed, key)
}

// takeAbsorbed reports whether a drop of key was absorbed, and forgets one such
// drop.
func (s *codewhaleSteers) takeAbsorbed(key steerKey) bool {
	for i, absorbed := range s.absorbed {
		if absorbed == key {
			s.absorbed = append(s.absorbed[:i], s.absorbed[i+1:]...)
			return true
		}
	}
	return false
}

// awaiting returns every steer that the route accepted and whose verdict no
// event stated yet, for turnID, or for every turn when turnID is "".
func (s *codewhaleSteers) awaiting(turnID string) []*steerTicket {
	var tickets []*steerTicket
	for _, ticket := range s.open {
		if ticket.replied && ticket.verdict == steerVerdictNone && (turnID == "" || ticket.key.turnID == turnID) {
			tickets = append(tickets, ticket)
		}
	}
	return tickets
}

// settleSteerReply reads the route's answer to one steer against what the
// events already stated, and returns what SteerInput tells the queue.
//
// The route refuses a steer with 409 when the engine dropped it within the
// route's wait, and when no turn runs; with 404 when the thread is gone; and
// with 400 when the turn is stopping, when it is no longer in progress, and
// when the thread's engine is not loaded or changed. Each one means that the
// running turn takes no steer, so the queue sends the text as a turn of its
// own.
func (a *Agent) settleSteerReply(ticket *steerTicket, err error) error {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	verdict := ticket.verdict
	var status *providerkit.HTTPStatusError
	switch {
	case err == nil:
		switch verdict {
		case steerVerdictDropped:
			// The drop arrived before the answer: the model never read the text, and
			// the queue has not counted it as delivered yet.
			a.steers.remove(ticket)
			return agent.ErrNoActiveTurn
		case steerVerdictDelivered:
			a.steers.remove(ticket)
			return nil
		default:
			ticket.replied = true
			return nil
		}
	case errors.As(err, &status):
		a.steers.remove(ticket)
		switch status.StatusCode {
		case httpStatusConflict, httpStatusNotFound, httpStatusBadRequest:
			if verdict == steerVerdictNone {
				// The runtime states a drop that its route refused as well. The queue
				// sends the text again, so that event draws no row.
				a.steers.absorb(ticket.key)
			}
			return agent.ErrNoActiveTurn
		default:
			return err
		}
	default:
		// The request failed on its way, and the engine may hold the text. An event
		// that already stated its fate settles it.
		a.steers.remove(ticket)
		switch verdict {
		case steerVerdictDelivered:
			return nil
		case steerVerdictDropped:
			return agent.ErrNoActiveTurn
		default:
			return fmt.Errorf("%w: Codewhale did not confirm the steer: %v", agent.ErrDeliveryUncertain, err)
		}
	}
}

// steerEventPayload is the payload of turn.steered and turn.steer_dropped.
type steerEventPayload struct {
	TurnID string `json:"turn_id"`
	Input  string `json:"input"`
}

// steerEventKey reads which steer an event states.
func steerEventKey(env codewhaleEnvelope) (steerKey, bool) {
	var payload steerEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return steerKey{}, false
	}
	turnID := payload.TurnID
	if turnID == "" {
		turnID = env.TurnID
	}
	return steerKey{turnID: turnID, input: strings.TrimSpace(payload.Input)}, turnID != ""
}

// handleSteerDelivered settles a steer that the model read. LeapMux already
// shows the text the reader typed, so the event draws no row.
func (a *Agent) handleSteerDelivered(env codewhaleEnvelope) {
	key, ok := steerEventKey(env)
	if !ok {
		return
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	ticket := a.steers.match(key)
	if ticket == nil {
		return
	}
	if ticket.replied {
		a.steers.remove(ticket)
		return
	}
	ticket.verdict = steerVerdictDelivered
}

// handleSteerDropped acts on a steer that the model never read. See the file
// comment for the four cases.
func (a *Agent) handleSteerDropped(env codewhaleEnvelope) {
	key, ok := steerEventKey(env)
	if !ok {
		a.persistNotification(env)
		return
	}
	a.Mu.Lock()
	ticket := a.steers.match(key)
	switch {
	case ticket != nil && !ticket.replied:
		// SteerInput has not answered the queue yet, and it refuses the steer.
		ticket.verdict = steerVerdictDropped
		a.Mu.Unlock()
		return
	case ticket != nil:
		a.steers.remove(ticket)
		a.Mu.Unlock()
		if !a.handBackSteer(ticket) {
			a.persistNotification(env)
		}
		return
	case a.steers.takeAbsorbed(key):
		a.Mu.Unlock()
		return
	default:
		a.Mu.Unlock()
		a.persistNotification(env)
	}
}

// handBackSteer queues a dropped steer again as the reader's next message, and
// reports whether the Worker took it.
func (a *Agent) handBackSteer(ticket *steerTicket) bool {
	if a.IsDiscardingOutput() {
		return false
	}
	if err := a.sink.RequeueDroppedInput(ticket.dropID, ticket.content, ticket.attachments); err != nil {
		slog.Error("codewhale queue a dropped steer again", "agent_id", a.AgentID(), "turn_id", ticket.key.turnID, "error", err)
		return false
	}
	return true
}

// settleSteersOfTurn hands back each steer of a turn that ended whose text the
// runtime's record states the model never read. Call it on the turn end, under
// dispatchMu, before the turn flag clears, so the text is back in the queue
// when the queue learns that the turn ended.
//
// A steer that the record states delivered is forgotten. A steer that the
// record does not state yet waits for its event.
func (a *Agent) settleSteersOfTurn(turnID string) {
	if turnID == "" {
		return
	}
	a.Mu.Lock()
	tickets := a.steers.awaiting(turnID)
	a.Mu.Unlock()
	if len(tickets) == 0 {
		return
	}
	a.settleFromStore(turnID, tickets)
}

// settleSteersAfterExit settles every steer that waits for its verdict, once
// the process exited: nothing states them after this. A steer that the record
// does not state is dropped from the memory with a log line, because no
// verdict can arrive for it any more.
func (a *Agent) settleSteersAfterExit() {
	a.Mu.Lock()
	tickets := a.steers.awaiting("")
	a.Mu.Unlock()
	byTurn := make(map[string][]*steerTicket)
	var turns []string
	for _, ticket := range tickets {
		if _, seen := byTurn[ticket.key.turnID]; !seen {
			turns = append(turns, ticket.key.turnID)
		}
		byTurn[ticket.key.turnID] = append(byTurn[ticket.key.turnID], ticket)
	}
	for _, turnID := range turns {
		a.settleFromStore(turnID, byTurn[turnID])
	}
	a.Mu.Lock()
	for _, ticket := range a.steers.awaiting("") {
		slog.Warn("codewhale steer with no verdict when the runtime exited", "agent_id", a.AgentID(), "turn_id", ticket.key.turnID)
		a.steers.remove(ticket)
	}
	a.Mu.Unlock()
}

// settleFromStore settles the tickets of one turn from the turn's record in the
// store.
func (a *Agent) settleFromStore(turnID string, tickets []*steerTicket) {
	undelivered, delivered, ok := readTurnSteers(a.store, turnID)
	if !ok {
		return
	}
	for _, ticket := range tickets {
		switch {
		case undelivered[ticket.key.input] > 0:
			undelivered[ticket.key.input]--
			if !a.handBackSteer(ticket) {
				// The ticket stays, so the drop event states the steer to the reader.
				continue
			}
			a.Mu.Lock()
			a.steers.remove(ticket)
			// The runtime states this drop later, when the engine next reads its
			// mailbox. The queue sends the text again, so that event draws no row.
			a.steers.absorb(ticket.key)
			a.Mu.Unlock()
		case delivered[ticket.key.input] > 0:
			delivered[ticket.key.input]--
			a.Mu.Lock()
			a.steers.remove(ticket)
			a.Mu.Unlock()
		}
	}
}

// storedTurn is the part of a turn record that lists its items.
type storedTurn struct {
	ItemIDs []string `json:"item_ids"`
}

// storedItem is the part of an item record that states a steer.
type storedItem struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// readTurnSteers reads the reader's messages of one turn from the store, and
// counts their texts by whether the model read them. A message the engine never
// committed is `queued` or `canceled`, and one it committed is `completed`. ok
// is false when the turn record cannot be read. An item that cannot be read is
// left out, so a steer that it states waits for its event.
//
// The texts are trimmed, as the steer route trims a prompt.
func readTurnSteers(store codewhaleStore, turnID string) (undelivered, delivered map[string]int, ok bool) {
	if store.dir == "" || turnID == "" || strings.ContainsAny(turnID, `/\`) {
		return nil, nil, false
	}
	root := store.runtimeDir()
	var turn storedTurn
	if err := sessionstore.ReadSidecarFile(filepath.Join(root, "turns", turnID+".json"), storeRecordMaxBytes, func(data []byte) error {
		return json.Unmarshal(data, &turn)
	}); err != nil {
		return nil, nil, false
	}
	undelivered = make(map[string]int)
	delivered = make(map[string]int)
	for _, itemID := range turn.ItemIDs {
		if itemID == "" || strings.ContainsAny(itemID, `/\`) {
			continue
		}
		var item storedItem
		if err := sessionstore.ReadSidecarFile(filepath.Join(root, "items", itemID+".json"), storeRecordMaxBytes, func(data []byte) error {
			return json.Unmarshal(data, &item)
		}); err != nil || item.Kind != contracts.CodewhaleItemKindUserMessage {
			continue
		}
		text := strings.TrimSpace(item.Detail)
		switch item.Status {
		case itemStatusQueued, itemStatusCanceled:
			undelivered[text]++
		case itemStatusCompleted:
			delivered[text]++
		}
	}
	return undelivered, delivered, true
}
