package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// Gap-safe ordered event dispatch and replay.
//
// The app-server numbers every session event with `seq`. A live subscription
// can miss events (a transport gap, a slow reader), and a re-subscribe or a
// `session/events` fetch recovers them. This file keeps the dispatch cursor
// (`Agent.lastSeq`) contiguous: it advances only after the NEXT event is
// dispatched, and a gap triggers a replay pass that fetches and merges the
// missing range.
//
// Two goroutines dispatch events: the read loop delivers live notifications,
// and a replay pass dispatches fetched events. `dispatchMu` serializes them
// (see dispatchZCodeEvent). The pending map and the replay bookkeeping live
// under the same lock.

// Concrete limits for the replay coordinator.
const (
	// zcodeReplayPassLimit is how many fetch-and-dispatch passes one replay
	// call runs before it reports an unclosable gap.
	zcodeReplayPassLimit = 4
	// zcodeReplayEventLimit is the page size of one session/events fetch.
	zcodeReplayEventLimit = 256
)

// zcodeReplayState is the pending-event bookkeeping. Guarded by dispatchMu
// except replayMu, which serializes whole replay passes.
type zcodeReplayState struct {
	// pending holds sequenced events past a gap, keyed by seq. Entries survive
	// between and outside replay passes so a failed replay does not lose them.
	pending map[int64]zcodeEventEnvelope
	// active is true between a replay pass's capture and its dispatch. Live
	// events join pending while it is set, so the pass merges them.
	active bool
	// generation identifies the session generation. invalidateZCodeEventReplay
	// increments it so an obsolete pass cannot commit against a replacement
	// session.
	generation uint64
	// highWatermark is the highest sequence observed from any source (live
	// event, subscribe reply, fetch response).
	highWatermark int64
	// mu serializes replay passes. Taken BEFORE dispatchMu.
	mu sync.Mutex
}

// initZCodeReplayState prepares the pending map. Called from Agent construction
// and from invalidateZCodeEventReplay.
func (s *zcodeReplayState) init() {
	s.pending = make(map[int64]zcodeEventEnvelope)
}

// dispatchZCodeEventOrdered is the single entry point for every session event,
// live or replayed. It keeps lastSeq contiguous and routes gaps into the
// pending set for replayZCodeEvents to recover.
//
// Rule summary:
//   - seq 0 keeps arrival-order dispatch: it carries no ordering information.
//   - seq <= lastSeq is a duplicate and is suppressed.
//   - seq == lastSeq+1 dispatches immediately, then drains now-contiguous
//     pending events.
//   - seq > lastSeq+1 goes to pending and schedules a replay. lastSeq is NOT
//     advanced past a gap.
func (a *Agent) dispatchZCodeEventOrdered(event zcodeEventEnvelope) {
	if event.Seq == 0 {
		a.dispatchMu.Lock()
		a.dispatchZCodeEventLocked(event)
		a.dispatchMu.Unlock()
		return
	}

	a.dispatchMu.Lock()
	if a.replay.active {
		// A replay pass will merge and dispatch this event. Hold it in the
		// pending set so the read loop never blocks.
		a.storePendingZCodeEventLocked(event)
		a.dispatchMu.Unlock()
		return
	}

	a.Mu.Lock()
	lastSeq := a.lastSeq
	a.Mu.Unlock()

	if event.Seq <= lastSeq {
		a.dispatchMu.Unlock()
		return
	}

	if event.Seq == lastSeq+1 {
		a.dispatchZCodeEventLocked(event)
		a.Mu.Lock()
		a.lastSeq = event.Seq
		a.Mu.Unlock()
		a.drainPendingZCodeEventsLocked()
		a.dispatchMu.Unlock()
		return
	}

	// A gap: hold the event and recover the missing range.
	a.storePendingZCodeEventLocked(event)
	a.dispatchMu.Unlock()
	a.scheduleZCodeReplay()
}

// storePendingZCodeEventLocked records one event awaiting contiguous dispatch
// and raises the high watermark. The caller holds dispatchMu.
func (a *Agent) storePendingZCodeEventLocked(event zcodeEventEnvelope) {
	if a.replay.pending == nil {
		a.replay.init()
	}
	a.replay.pending[event.Seq] = event
	if event.Seq > a.replay.highWatermark {
		a.replay.highWatermark = event.Seq
	}
}

// drainPendingZCodeEventsLocked dispatches pending events that became
// contiguous after the cursor advanced. The caller holds dispatchMu.
func (a *Agent) drainPendingZCodeEventsLocked() {
	for {
		a.Mu.Lock()
		next := a.lastSeq + 1
		a.Mu.Unlock()
		event, ok := a.replay.pending[next]
		if !ok {
			return
		}
		delete(a.replay.pending, next)
		a.dispatchZCodeEventLocked(event)
		a.Mu.Lock()
		a.lastSeq = event.Seq
		a.Mu.Unlock()
	}
}

// scheduleZCodeReplay starts a replay pass on its own goroutine so the read
// loop never blocks on the fetch RPC. A test replaces replayScheduler to
// suppress automatic scheduling.
func (a *Agent) scheduleZCodeReplay() {
	if a.replayScheduler != nil {
		a.replayScheduler()
		return
	}
	go func() {
		if err := a.replayZCodeEvents(a.Context()); err != nil {
			slog.Warn("zcode event replay failed", "agent_id", a.AgentID(), "error", err)
		}
	}()
}

// replayZCodeEvents recovers a sequence gap by fetching the missing range and
// dispatching it in order.
//
// Passes are serialized with replayMu. Each pass:
//
//  1. Captures afterSeq, session ID and replay generation under dispatchMu and
//     sets replay.active so live events join the pending set.
//  2. Fetches session/events OUTSIDE every lock so the read loop stays free.
//  3. Merges the response with the pending set and dispatches the contiguous
//     prefix under dispatchMu, after verifying that the session and generation
//     still match.
//
// A pass that leaves a gap runs again, up to zcodeReplayPassLimit. After that
// the function returns an error naming the missing range. Callers that must
// not proceed with a gap (startup, context clear) check the error; a
// background watcher retries later.
func (a *Agent) replayZCodeEvents(ctx context.Context) error {
	a.replay.mu.Lock()
	defer a.replay.mu.Unlock()

	var missingFrom, missingTo int64
	for pass := 0; pass < zcodeReplayPassLimit; pass++ {
		// Phase 1: capture.
		a.dispatchMu.Lock()
		a.Mu.Lock()
		afterSeq := a.lastSeq
		sessionID := a.sessionID
		a.Mu.Unlock()
		generation := a.replay.generation
		a.replay.active = true
		a.dispatchMu.Unlock()

		// Phase 2: fetch outside every lock.
		fetched, highSeq, err := a.fetchZCodeSessionEvents(ctx, sessionID, afterSeq)
		if err != nil {
			a.dispatchMu.Lock()
			a.endZCodeReplayPassLocked(generation)
			a.dispatchMu.Unlock()
			return fmt.Errorf("session/events after seq %d: %w", afterSeq, err)
		}

		// Phase 3: merge and dispatch the contiguous prefix.
		a.dispatchMu.Lock()
		if generation != a.replay.generation {
			// The session was replaced while the fetch was in flight. The
			// response belongs to the old session; drop it. The pending set
			// was already discarded by invalidateZCodeEventReplay.
			a.dispatchMu.Unlock()
			return nil
		}
		if highSeq > a.replay.highWatermark {
			a.replay.highWatermark = highSeq
		}
		merged := a.mergeZCodeReplayEventsLocked(fetched)
		a.endZCodeReplayPassLocked(generation)
		dispatched := a.dispatchZCodeReplayPrefixLocked(merged)
		_ = dispatched
		a.Mu.Lock()
		cursor := a.lastSeq
		a.Mu.Unlock()
		a.dispatchMu.Unlock()

		// Phase 4: decide whether another pass is needed.
		a.dispatchMu.Lock()
		high := a.replay.highWatermark
		a.dispatchMu.Unlock()
		if high <= cursor {
			return nil
		}
		missingFrom = cursor + 1
		missingTo = high
	}
	return fmt.Errorf("event replay exhausted %d passes; seq %d..%d still missing",
		zcodeReplayPassLimit, missingFrom, missingTo)
}

// fetchZCodeSessionEvents asks the app-server for events after afterSeq.
// It returns the events and the highest sequence the response covers.
func (a *Agent) fetchZCodeSessionEvents(ctx context.Context, sessionID string, afterSeq int64) ([]zcodeEventEnvelope, int64, error) {
	if sessionID == "" {
		return nil, 0, fmt.Errorf("no session to fetch events for")
	}
	raw, err := a.sendZCodeRequestContext(ctx, MethodSessionEvents, map[string]any{
		"sessionId": sessionID,
		"afterSeq":  afterSeq,
		"limit":     zcodeReplayEventLimit,
	}, a.APITimeout())
	if err != nil {
		return nil, 0, err
	}
	var resp struct {
		EventSeq int64                `json:"eventSeq"`
		Events   []zcodeEventEnvelope `json:"events"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, 0, fmt.Errorf("decode session/events response: %w", err)
	}
	high := resp.EventSeq
	for _, ev := range resp.Events {
		if ev.Seq > high {
			high = ev.Seq
		}
	}
	return resp.Events, high, nil
}

// mergeZCodeReplayEventsLocked combines fetched events with the pending set.
// Sequenced events merge by seq (fetched wins on collision because it is the
// server's own copy). The result is sorted by seq. The caller holds dispatchMu.
func (a *Agent) mergeZCodeReplayEventsLocked(fetched []zcodeEventEnvelope) []zcodeEventEnvelope {
	merged := make(map[int64]zcodeEventEnvelope, len(fetched)+len(a.replay.pending))
	for seq, ev := range a.replay.pending {
		merged[seq] = ev
	}
	for _, ev := range fetched {
		if ev.Seq > 0 {
			merged[ev.Seq] = ev
		}
	}
	out := make([]zcodeEventEnvelope, 0, len(merged))
	for _, ev := range merged {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// dispatchZCodeReplayPrefixLocked dispatches the contiguous prefix of the
// merged event list and removes each dispatched event from the pending set.
// It returns how many events it dispatched. The caller holds dispatchMu.
func (a *Agent) dispatchZCodeReplayPrefixLocked(events []zcodeEventEnvelope) int {
	count := 0
	for _, ev := range events {
		a.Mu.Lock()
		next := a.lastSeq + 1
		a.Mu.Unlock()
		if ev.Seq != next {
			// A gap remains: stop here. Later events stay pending.
			break
		}
		delete(a.replay.pending, ev.Seq)
		a.dispatchZCodeEventLocked(ev)
		a.Mu.Lock()
		a.lastSeq = ev.Seq
		a.Mu.Unlock()
		count++
	}
	return count
}

// endZCodeReplayPassLocked clears the active flag when the pass that set it is
// the one ending. An obsolete pass (generation mismatch) leaves the newer
// pass's state alone. The caller holds dispatchMu.
func (a *Agent) endZCodeReplayPassLocked(generation uint64) {
	if generation == a.replay.generation {
		a.replay.active = false
	}
}

// invalidateZCodeEventReplay retires every replay artifact of the current
// session. ClearContext calls it so ID-less events of the replaced session
// cannot enter the new session's dispatch.
func (a *Agent) invalidateZCodeEventReplay() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.replay.generation++
	a.replay.active = false
	a.replay.init()
	a.replay.highWatermark = 0
}

// ensureZCodeReplayContiguous runs replay passes until the cursor reaches the
// high watermark or the pass limit is exhausted. Startup and ClearContext use
// it to guarantee no known gap survives them.
func (a *Agent) ensureZCodeReplayContiguous(ctx context.Context) error {
	a.dispatchMu.Lock()
	high := a.replay.highWatermark
	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	a.dispatchMu.Unlock()
	if high <= cursor {
		return nil
	}
	return a.replayZCodeEvents(ctx)
}
