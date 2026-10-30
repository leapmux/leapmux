package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

const (
	zcodeReplayPassLimit     = 4
	zcodeReplayRetryInterval = 30 * time.Second
)

// zcodeReplayState retains accepted events until their available range dispatches.
// dispatchMu guards its fields. mu serializes fetch passes without blocking the reader.
type zcodeReplayState struct {
	pending       map[int64]zcodeEventEnvelope
	active        bool
	generation    uint64
	highWatermark int64
	worker        *zcodeReplayWorker
	mu            sync.Mutex
}

type zcodeReplayWorker struct{ cancel context.CancelFunc }

func (s *zcodeReplayState) init() { s.pending = make(map[int64]zcodeEventEnvelope) }

// dispatchZCodeEventOrdered is the ingress for live, replayed and directly fed events.
// Sequence zero preserves arrival order. Positive live sequences wait for their
// predecessors or an unrestricted replay that confirms the retained range.
func (a *Agent) dispatchZCodeEventOrdered(event zcodeEventEnvelope) {
	a.dispatchMu.Lock()
	needsReplay := a.admitZCodeEventLocked(event)
	a.unlockZCodeDispatch()
	if needsReplay {
		a.scheduleZCodeReplay()
	}
}

// admitZCodeEventLocked shares ordering between live events and a subscription batch.
// The caller holds dispatchMu.
func (a *Agent) admitZCodeEventLocked(event zcodeEventEnvelope) bool {
	if !a.acceptsZCodeEventSessionLocked(event) {
		return false
	}
	if event.Seq == 0 {
		a.dispatchZCodeEventLocked(event)
		return false
	}
	a.storePendingZCodeEventLocked(event)
	if !a.replay.active {
		a.drainPendingZCodeEventsLocked(0)
	}
	return a.zcodeReplayHasGapLocked()
}

// acceptsZCodeEventSessionLocked runs before an event can change ordering state.
func (a *Agent) acceptsZCodeEventSessionLocked(event zcodeEventEnvelope) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return event.SessionID == "" || a.sessionID == "" || event.SessionID == a.sessionID
}

func (a *Agent) storePendingZCodeEventLocked(event zcodeEventEnvelope) {
	if !a.acceptsZCodeEventSessionLocked(event) {
		return
	}
	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	if event.Seq <= cursor || event.Seq <= 0 {
		return
	}
	if a.replay.pending == nil {
		a.replay.init()
	}
	if previous, exists := a.replay.pending[event.Seq]; exists && (!previous.archive || event.archive) {
		return
	}
	a.replay.pending[event.Seq] = event
	a.replay.highWatermark = max(a.replay.highWatermark, event.Seq)
}

// drainPendingZCodeEventsLocked is the only incremental cursor writer.
// A successful unrestricted fetch confirms the retained representation through
// availableThrough. Missing numbers there may represent expired transient events.
// Beyond that checkpoint, only a contiguous live prefix can advance the cursor.
func (a *Agent) drainPendingZCodeEventsLocked(availableThrough int64) {
	a.Mu.Lock()
	cursor := a.lastSeq
	a.Mu.Unlock()
	if availableThrough > cursor {
		sequences := make([]int64, 0, len(a.replay.pending))
		for seq := range a.replay.pending {
			if seq > cursor && seq <= availableThrough {
				sequences = append(sequences, seq)
			}
		}
		slices.Sort(sequences)
		for _, seq := range sequences {
			event := a.replay.pending[seq]
			delete(a.replay.pending, seq)
			event.replayUncertain = a.replay.active
			a.dispatchZCodeEventLocked(event)
			a.Mu.Lock()
			a.lastSeq = max(a.lastSeq, seq)
			a.Mu.Unlock()
		}
		a.Mu.Lock()
		a.lastSeq = max(a.lastSeq, availableThrough)
		a.Mu.Unlock()
	}
	for {
		a.Mu.Lock()
		cursor := a.lastSeq
		a.Mu.Unlock()
		event, ok := a.replay.pending[cursor+1]
		if !ok {
			return
		}
		delete(a.replay.pending, event.Seq)
		event.replayUncertain = a.replay.active
		a.dispatchZCodeEventLocked(event)
		a.Mu.Lock()
		a.lastSeq = max(a.lastSeq, event.Seq)
		a.Mu.Unlock()
	}
}

func (a *Agent) zcodeReplayHasGapLocked() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.replay.highWatermark > a.lastSeq
}

// scheduleZCodeReplay reserves one worker before the reader can schedule another.
func (a *Agent) scheduleZCodeReplay() {
	a.dispatchMu.Lock()
	if a.replay.worker != nil || !a.zcodeReplayHasGapLocked() {
		a.unlockZCodeDispatch()
		return
	}
	ctx, cancel := context.WithCancel(a.Context())
	generation := a.replay.generation
	worker := &zcodeReplayWorker{cancel: cancel}
	a.replay.worker = worker
	a.unlockZCodeDispatch()
	work := func() {
		defer cancel()
		defer func() {
			a.dispatchMu.Lock()
			if a.replay.worker == worker {
				a.replay.worker = nil
			}
			a.unlockZCodeDispatch()
		}()
		for {
			if err := a.replayZCodeEvents(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("zcode event replay failed", "agent_id", a.AgentID(), "error", err)
			}
			a.dispatchMu.Lock()
			current := a.replay.generation == generation
			needed := current && a.zcodeReplayHasGapLocked()
			if current && !needed && a.replay.worker == worker {
				a.replay.worker = nil
			}
			a.unlockZCodeDispatch()
			if !needed || ctx.Err() != nil {
				return
			}
			timer := a.Clock().NewTimer(zcodeReplayRetryInterval, "zcode-event-replay-retry")
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			case <-a.ProcessDone():
				timer.Stop()
				return
			}
		}
	}
	if a.replayScheduler != nil {
		a.replayScheduler(work)
	} else {
		go work()
	}
}

// replayZCodeEvents fetches the available range outside dispatchMu, then merges it
// into the same pending set as live events. A failed pass retains every received event.
func (a *Agent) replayZCodeEvents(ctx context.Context) error {
	a.beginZCodeSessionPublicationDeferral()
	a.replay.mu.Lock()
	defer func() { a.replay.mu.Unlock(); a.endZCodeSessionPublicationDeferral() }()
	var missingFrom, missingTo int64
	for range zcodeReplayPassLimit {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.dispatchMu.Lock()
		if !a.zcodeReplayHasGapLocked() {
			a.unlockZCodeDispatch()
			return nil
		}
		a.Mu.Lock()
		afterSeq, sessionID := a.lastSeq, a.sessionID
		a.Mu.Unlock()
		generation := a.replay.generation
		requestedThrough := a.replay.highWatermark
		a.replay.active = true
		a.unlockZCodeDispatch()

		fetched, high, err := a.fetchZCodeSessionEvents(ctx, sessionID, afterSeq)
		a.dispatchMu.Lock()
		a.Mu.Lock()
		current := a.sessionID == sessionID
		a.Mu.Unlock()
		if generation != a.replay.generation || !current {
			a.unlockZCodeDispatch()
			return nil
		}
		if err != nil {
			a.replay.active = false
			// Live delivery can close the prefix while the fetch fails.
			a.drainPendingZCodeEventsLocked(0)
			needed := a.zcodeReplayHasGapLocked()
			a.unlockZCodeDispatch()
			if !needed {
				return nil
			}
			return fmt.Errorf("session/events after seq %d: %w", afterSeq, err)
		}
		a.replay.highWatermark = max(a.replay.highWatermark, high)
		a.markZCodeCanonicalRecoveryLocked()
		for _, event := range fetched {
			a.storePendingZCodeEventLocked(event)
		}
		// Only the checkpoint known before this request is covered. Mapping the
		// fetched snapshot can allocate newer public numbers during the request.
		a.drainPendingZCodeEventsLocked(requestedThrough)
		a.replay.active = false
		a.Mu.Lock()
		cursor := a.lastSeq
		a.Mu.Unlock()
		high = a.replay.highWatermark
		a.unlockZCodeDispatch()
		if high <= cursor {
			return nil
		}
		missingFrom, missingTo = cursor+1, high
	}
	return fmt.Errorf("event replay exhausted %d passes; seq %d..%d still missing", zcodeReplayPassLimit, missingFrom, missingTo)
}

// fetchZCodeSessionEvents omits limit because the native endpoint limits the newest
// tail, which cannot recover the beginning of a larger gap. It returns all available events.
func (a *Agent) fetchZCodeSessionEvents(ctx context.Context, sessionID string, afterSeq int64) ([]zcodeEventEnvelope, int64, error) {
	if sessionID == "" {
		return nil, 0, fmt.Errorf("no session to fetch events for")
	}
	raw, err := a.sendZCodeRequestContext(ctx, MethodSessionEvents, map[string]any{
		"sessionId": sessionID, "afterSeq": afterSeq,
	}, a.APITimeout())
	if err != nil {
		return nil, 0, err
	}
	var response struct {
		SessionID *string               `json:"sessionId"`
		EventSeq  *int64                `json:"eventSeq"`
		Events    *[]zcodeEventEnvelope `json:"events"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, 0, fmt.Errorf("decode session/events response: %w", err)
	}
	if response.Events == nil {
		return nil, 0, fmt.Errorf("session/events returned no event list")
	}
	if response.SessionID != nil && *response.SessionID != sessionID {
		return nil, 0, fmt.Errorf("session/events returned another session")
	}
	high := int64(0)
	if response.EventSeq != nil {
		if *response.EventSeq < 0 {
			return nil, 0, fmt.Errorf("session/events returned a negative event sequence")
		}
		high = *response.EventSeq
	}
	events, err := prepareZCodeArchiveEvents(*response.Events, sessionID)
	if err != nil {
		return nil, 0, fmt.Errorf("session/events: %w", err)
	}
	for _, event := range events {
		high = max(high, event.Seq)
	}
	return events, high, nil
}

// prepareZCodeArchiveEvents validates the complete request-scoped response
// before it can authorize a checkpoint or alter pending live events.
func prepareZCodeArchiveEvents(events []zcodeEventEnvelope, sessionID string) ([]zcodeEventEnvelope, error) {
	prepared := slices.Clone(events)
	for index := range prepared {
		event := &prepared[index]
		if event.Seq <= 0 {
			return nil, fmt.Errorf("expected a positive event sequence, received %d", event.Seq)
		}
		if event.Type == "" {
			return nil, fmt.Errorf("the response contains no event type")
		}
		if event.SessionID != "" && event.SessionID != sessionID {
			return nil, fmt.Errorf("the response contains an event for another session")
		}
		event.SessionID = sessionID
		event.archive = true
	}
	return prepared, nil
}

func (a *Agent) invalidateZCodeEventReplay() {
	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.invalidateZCodeEventReplayLocked()
}

// invalidateZCodeEventReplayLocked runs in the session-adoption critical section.
func (a *Agent) invalidateZCodeEventReplayLocked() {
	if a.replay.worker != nil {
		a.replay.worker.cancel()
	}
	a.replay.generation++
	a.replay.active = false
	a.replay.worker = nil
	a.replay.highWatermark = 0
	a.replay.init()
}

// ensureZCodeReplayThrough requires an authoritative available range through
// the snapshot's own sequence before its state can apply.
func (a *Agent) ensureZCodeReplayThrough(ctx context.Context, sessionID string, observedSeq int64, epoch uint64) error {
	a.dispatchMu.Lock()
	a.Mu.Lock()
	current := a.sessionID == sessionID && sessionID != "" && a.sessionEpoch == epoch && !a.sessionChanging
	a.Mu.Unlock()
	if !current {
		a.unlockZCodeDispatch()
		return errNoZCodeSession
	}
	a.replay.highWatermark = max(a.replay.highWatermark, observedSeq)
	needed := a.zcodeReplayHasGapLocked()
	a.unlockZCodeDispatch()
	if needed {
		if err := a.replayZCodeEvents(ctx); err != nil {
			return err
		}
	}
	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.sessionID != sessionID || a.sessionEpoch != epoch || a.sessionChanging {
		return errNoZCodeSession
	}
	if a.lastSeq < a.replay.highWatermark {
		return fmt.Errorf("event replay still lacks seq %d..%d", a.lastSeq+1, a.replay.highWatermark)
	}
	return nil
}

// markZCodeCanonicalRecoveryLocked freezes uncertain appends before replay can
// dispatch a model response or native end. The caller holds dispatchMu.
func (a *Agent) markZCodeCanonicalRecoveryLocked() {
	a.Mu.Lock()
	for _, owner := range a.turns {
		if !owner.finalized {
			owner.canonicalRequired = true
		}
	}
	a.Mu.Unlock()
}
