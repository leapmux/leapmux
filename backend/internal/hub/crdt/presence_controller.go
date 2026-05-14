package crdt

import (
	"context"
	"sync"
	"time"
)

// PresenceController bundles the per-manager presence state that used
// to live as scattered fields on Manager: the PresenceTracker itself,
// the (clientID → refcount) map, and the deferred-clear timers.
//
// The controller owns its own mutex so Manager-side state writes
// (m.mu) and presence-side bookkeeping (p.mu) don't contend with
// each other. Lock order — if both are held — is m.mu first, then
// p.mu; the Subscribe path drops m.mu before touching the
// controller.
type PresenceController struct {
	tracker *PresenceTracker

	mu          sync.Mutex
	connCounts  map[string]int
	clearTimers map[string]*time.Timer
	clearGrace  time.Duration

	presenceCh chan presenceJob
	clearCh    chan presenceClearJob
	stop       <-chan struct{}
	now        func() time.Time
}

// newPresenceController constructs a controller with the given grace
// window and clock. `stop` is the Manager's stop channel — used by
// fired clear timers so they don't deliver on clearCh after the
// manager goroutine has exited.
func newPresenceController(now func() time.Time, clearGrace time.Duration, stop <-chan struct{}) *PresenceController {
	if now == nil {
		now = time.Now
	}
	return &PresenceController{
		tracker:     NewPresenceTracker(now),
		connCounts:  map[string]int{},
		clearTimers: map[string]*time.Timer{},
		clearGrace:  clearGrace,
		presenceCh:  make(chan presenceJob, 256),
		clearCh:     make(chan presenceClearJob, 64),
		stop:        stop,
		now:         now,
	}
}

// OnConnect bumps the refcount for `clientID` and cancels any pending
// deferred-clear timer so a reconnect inside the grace window keeps
// the client's presence entries intact. No-op for the empty
// ClientID (e.g. server-internal subscribers).
func (p *PresenceController) OnConnect(clientID string) {
	if clientID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connCounts[clientID]++
	if t, ok := p.clearTimers[clientID]; ok {
		t.Stop()
		delete(p.clearTimers, clientID)
	}
}

// OnDisconnect drops the refcount for `clientID`. When it reaches
// zero, schedules a deferred-clear job to fire after the grace
// window — a reconnect within that window cancels the timer.
// No-op for the empty ClientID.
func (p *PresenceController) OnDisconnect(clientID string) {
	if clientID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connCounts[clientID]--
	if p.connCounts[clientID] > 0 {
		return
	}
	delete(p.connCounts, clientID)
	cid := clientID
	p.clearTimers[cid] = time.AfterFunc(p.clearGrace, func() {
		select {
		case p.clearCh <- presenceClearJob{clientID: cid}:
		case <-p.stop:
		}
	})
}

// processClear handles a fired clear timer: re-check the refcount in
// case the client reconnected during the brief window between timer
// fire and processing; if still gone, evict every presence entry for
// that ClientID and return the workspace→new-active-client map for
// the caller to broadcast.
func (p *PresenceController) processClear(clientID string) map[string]string {
	p.mu.Lock()
	if _, scheduled := p.clearTimers[clientID]; !scheduled {
		p.mu.Unlock()
		return nil
	}
	if p.connCounts[clientID] > 0 {
		delete(p.clearTimers, clientID)
		p.mu.Unlock()
		return nil
	}
	delete(p.clearTimers, clientID)
	p.mu.Unlock()
	return p.tracker.RemoveClient(clientID)
}

// PostHeartbeat enqueues a heartbeat for the Run loop to process.
// Blocks until the slot is available or ctx is cancelled — bounded
// by the channel buffer (256).
func (p *PresenceController) PostHeartbeat(ctx context.Context, workspaceID, clientID string) error {
	select {
	case p.presenceCh <- presenceJob{workspaceID: workspaceID, clientID: clientID}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Active returns the currently-active client for `workspaceID`.
// Thread-safe; backed by the tracker's own mutex.
func (p *PresenceController) Active(workspaceID string) string {
	return p.tracker.Active(workspaceID)
}

// HasLiveConnections reports whether at least one subscriber is
// counted. The Registry's idle-eviction janitor consults this to
// decide whether a manager is safe to evict.
func (p *PresenceController) HasLiveConnections() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.connCounts) > 0
}

// Shutdown stops every pending clear timer. Call after the Run loop
// has exited so no dangling timer fires into a closed manager. Idempotent.
func (p *PresenceController) Shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for cid, t := range p.clearTimers {
		t.Stop()
		delete(p.clearTimers, cid)
	}
}

// Run owns the presence event loop: heartbeats, deferred-clear jobs,
// and the inactivity reaper sweep. `onBroadcast` is invoked for every
// active-client transition so the Manager can fan out PresenceUpdate
// events on the broadcast snapshot.
func (p *PresenceController) Run(ctx context.Context, onBroadcast func(workspaceID, activeClientID string)) {
	sweepTicker := time.NewTicker(presenceSweepInterval)
	defer sweepTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case pj := <-p.presenceCh:
			active, changed := p.tracker.Heartbeat(pj.workspaceID, pj.clientID)
			if changed {
				onBroadcast(pj.workspaceID, active)
			}
		case cj := <-p.clearCh:
			for ws, active := range p.processClear(cj.clientID) {
				onBroadcast(ws, active)
			}
		case <-sweepTicker.C:
			cutoff := p.now().Add(-PresenceEvictAfter)
			for ws, active := range p.tracker.SweepInactive(cutoff) {
				onBroadcast(ws, active)
			}
		}
	}
}
