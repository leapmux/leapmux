package acp

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// SteerEchoTracker correlates ID-bearing steer RPC replies with echoed steer
// notifications that can arrive either before or after the RPC reply.
// Early echoes reserve their transcript sequence position without marking the
// active root turn as sharing a reply until Accept confirms ownership.
type SteerEchoTracker struct {
	mu       sync.Mutex
	inFlight *SteerEchoDispatch
	pending  map[string]*SteerEchoDispatch
}

// SteerEchoDispatch represents one tracked mid-turn steer RPC in flight or
// awaiting its native echo.
type SteerEchoDispatch struct {
	sessionID  string
	resolution agent.SteerResolution
	early      map[string]*steerEchoEarly
}

type steerEchoEarly struct {
	sessionID  string
	messageID  string
	resolution agent.SteerResolution
	outcome    agent.SteerOutcome
	target     *acpTranscriptTarget
	write      func() error
	publish    func() error
	accepted   bool
	ready      atomic.Bool
}

func (e *steerEchoEarly) run(agent.TranscriptSequence) error {
	if !e.ready.Load() {
		return agent.ErrOutputInFlight
	}
	if !e.accepted {
		return nil
	}
	return e.write()
}

func (e *steerEchoEarly) finish(accepted bool) error {
	e.accepted = accepted
	e.ready.Store(true)
	return e.publish()
}

// Begin registers one in-flight steer RPC for sessionID. Returns
// agent.ErrAgentBusy if another tracked steer RPC is already in flight.
func (t *SteerEchoTracker) Begin(sessionID string, resolution agent.SteerResolution) (*SteerEchoDispatch, error) {
	dispatch := &SteerEchoDispatch{
		sessionID:  sessionID,
		resolution: resolution,
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inFlight != nil {
		return nil, agent.ErrAgentBusy
	}
	t.inFlight = dispatch
	return dispatch, nil
}

// Accept binds dispatch to messageID once the steer RPC succeeds. Any early
// echo matching messageID commits its root-turn reply sharing and flushes at
// its reserved position; all other early echoes discard without mutating the
// active turn.
func (t *SteerEchoTracker) Accept(base *Base, provider, label string, dispatch *SteerEchoDispatch, messageID string) error {
	if messageID == "" {
		return fmt.Errorf("%w: %s queued a steer without a message ID", agent.ErrDeliveryUncertain, provider)
	}
	t.mu.Lock()
	if t.inFlight != dispatch {
		t.mu.Unlock()
		return fmt.Errorf("%w: the %s steer reply arrived after its dispatch ended", agent.ErrDeliveryUncertain, provider)
	}
	if t.pending[messageID] != nil {
		t.mu.Unlock()
		return fmt.Errorf("%w: %s reused a pending steer message ID", agent.ErrDeliveryUncertain, provider)
	}
	t.inFlight = nil
	early := dispatch.early
	dispatch.early = nil
	if early[messageID] == nil {
		if t.pending == nil {
			t.pending = make(map[string]*SteerEchoDispatch)
		}
		t.pending[messageID] = dispatch
	}
	t.mu.Unlock()

	for _, id := range slices.Sorted(maps.Keys(early)) {
		event := early[id]
		accepted := id == messageID
		if accepted {
			event.write = base.captureSteerEchoForTarget(event.target, event.sessionID, event.messageID, event.resolution, event.outcome)
		}
		base.reportSteerWrite(label, event.finish(accepted))
	}
	return nil
}

// Cancel aborts an in-flight steer dispatch and discards every early echo
// reserved while it was in flight without mutating active-turn ownership.
func (t *SteerEchoTracker) Cancel(base *Base, label string, dispatch *SteerEchoDispatch) {
	t.mu.Lock()
	if t.inFlight != dispatch {
		t.mu.Unlock()
		return
	}
	t.inFlight = nil
	early := dispatch.early
	dispatch.early = nil
	t.mu.Unlock()
	for _, id := range slices.Sorted(maps.Keys(early)) {
		base.reportSteerWrite(label, early[id].finish(false))
	}
}

// DiscardPending removes dispatch from the pending map if it has not yet been
// resolved by a native echo, returning whether it was still pending.
func (t *SteerEchoTracker) DiscardPending(dispatch *SteerEchoDispatch) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, candidate := range t.pending {
		if candidate == dispatch {
			delete(t.pending, id)
			return true
		}
	}
	return false
}

// Resolve settles a pending steer for messageID, or reserves an ordered early
// slot when the steer RPC is still in flight.
func (t *SteerEchoTracker) Resolve(base *Base, label, earlyLabel, messageID string, outcome agent.SteerOutcome) {
	if messageID == "" {
		return
	}
	sessionID := base.CurrentSessionID()
	t.mu.Lock()
	dispatch := t.pending[messageID]
	if dispatch != nil && dispatch.sessionID != sessionID {
		t.mu.Unlock()
		return
	}
	if dispatch != nil {
		delete(t.pending, messageID)
	}
	inFlight := t.inFlight
	if dispatch == nil && (inFlight == nil || inFlight.sessionID != sessionID || inFlight.early[messageID] != nil) {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	if dispatch != nil {
		base.RetainSteerWrite(label, base.captureSteerEcho(dispatch.sessionID, messageID, dispatch.resolution, outcome))
		return
	}

	var target *acpTranscriptTarget
	if outcome == agent.SteerOutcomeEchoed {
		base.turnMu.Lock()
		target = base.liveRootReplyTargetLocked(inFlight.sessionID)
		base.turnMu.Unlock()
	}
	event := &steerEchoEarly{
		sessionID:  inFlight.sessionID,
		messageID:  messageID,
		resolution: inFlight.resolution,
		outcome:    outcome,
		target:     target,
	}
	publish, err := agent.ReserveTranscriptSequence(base.Sink(), earlyLabel, event.run)
	if err != nil {
		base.reportSteerWrite(label, err)
		return
	}
	event.publish = publish
	t.mu.Lock()
	if t.inFlight != inFlight {
		t.mu.Unlock()
		base.reportSteerWrite(label, event.finish(false))
		return
	}
	if inFlight.early == nil {
		inFlight.early = make(map[string]*steerEchoEarly)
	}
	inFlight.early[messageID] = event
	t.mu.Unlock()
}

// DropPending resolves every remaining pending steer in deterministic ID order.
func (t *SteerEchoTracker) DropPending(base *Base, label string, outcome agent.SteerOutcome) {
	t.mu.Lock()
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()
	for _, id := range slices.Sorted(maps.Keys(pending)) {
		dispatch := pending[id]
		base.RetainSteerWrite(label, base.captureSteerEcho(dispatch.sessionID, id, dispatch.resolution, outcome))
	}
}

func (b *Base) captureSteerEcho(sessionID, messageID string, resolution agent.SteerResolution, outcome agent.SteerOutcome) func() error {
	resolution.Outcome = outcome
	if outcome == agent.SteerOutcomeEchoed {
		return b.CaptureRootSteer(sessionID, messageID, resolution)
	}
	return b.Sink().CaptureSteerResolution(resolution)
}

func (b *Base) captureSteerEchoForTarget(target *acpTranscriptTarget, sessionID, messageID string, resolution agent.SteerResolution, outcome agent.SteerOutcome) func() error {
	resolution.Outcome = outcome
	if outcome == agent.SteerOutcomeEchoed {
		return b.captureRootSteerForTarget(target, sessionID, messageID, resolution)
	}
	return b.Sink().CaptureSteerResolution(resolution)
}
