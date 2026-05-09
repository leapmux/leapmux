package crdt

import (
	"sync"
	"time"
)

type presenceEntry struct {
	clientID   string
	receivedAt time.Time
}

// PresenceTracker maintains per-(workspace, client) heartbeats and
// computes the most recently active client for each workspace. Server
// receive time is authoritative; client_id comes from the
// authenticated session, not the request body. Entries persist for the
// lifetime of the client's WebSocket subscription — there is no
// inactivity-based GC. Cleanup is driven by the manager when the
// associated subscription disconnects (with a short grace period to
// hide reconnect blips).
type PresenceTracker struct {
	mu sync.Mutex
	// workspace_id -> client_id -> entry
	rows map[string]map[string]*presenceEntry
	// last broadcast active client per workspace, used to suppress
	// duplicate broadcasts.
	lastActive map[string]string
	now        func() time.Time
}

// NewPresenceTracker returns an empty tracker. nowFn is overridable
// for tests; pass nil to use time.Now.
func NewPresenceTracker(nowFn func() time.Time) *PresenceTracker {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &PresenceTracker{
		rows:       map[string]map[string]*presenceEntry{},
		lastActive: map[string]string{},
		now:        nowFn,
	}
}

// Heartbeat records a presence event. Returns the new active client
// id for the workspace plus a flag indicating whether it changed
// since the last call.
func (p *PresenceTracker) Heartbeat(workspaceID, clientID string) (active string, changed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	row, ok := p.rows[workspaceID]
	if !ok {
		row = map[string]*presenceEntry{}
		p.rows[workspaceID] = row
	}
	row[clientID] = &presenceEntry{clientID: clientID, receivedAt: now}
	active = p.activeLocked(workspaceID)
	if active != p.lastActive[workspaceID] {
		p.lastActive[workspaceID] = active
		return active, true
	}
	return active, false
}

// Active returns the currently-active client for the workspace
// without recording a heartbeat.
func (p *PresenceTracker) Active(workspaceID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeLocked(workspaceID)
}

// SweepInactive drops entries whose `receivedAt` is before `cutoff`
// and returns the workspace_id → new active-client map for any
// workspace whose leader changed as a result. Defense-in-depth pass
// driven by the manager's runPresence sweep ticker: the normal
// disconnect path (Subscribe → unsub → clearCh → RemoveClient)
// reaches a clean state within PresenceClearGrace of the last live
// subscription closing, so under healthy operation SweepInactive
// finds nothing. It catches entries left behind when that path is
// short-circuited (panic, lost clearCh job, hub-side state mismatch).
func (p *PresenceTracker) SweepInactive(cutoff time.Time) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	changes := map[string]string{}
	for wsID, row := range p.rows {
		for clientID, entry := range row {
			if entry.receivedAt.Before(cutoff) {
				delete(row, clientID)
			}
		}
		if len(row) == 0 {
			delete(p.rows, wsID)
		}
		active := p.activeLocked(wsID)
		prev := p.lastActive[wsID]
		if active == prev {
			continue
		}
		if active == "" {
			delete(p.lastActive, wsID)
		} else {
			p.lastActive[wsID] = active
		}
		changes[wsID] = active
	}
	return changes
}

// RemoveClient drops every entry for the given client_id and returns a
// map of workspace_id → new active client for the workspaces whose
// leader changed. The manager calls this after a WS subscription's
// grace period elapses with no reconnect.
func (p *PresenceTracker) RemoveClient(clientID string) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	changes := map[string]string{}
	for wsID, row := range p.rows {
		if _, ok := row[clientID]; !ok {
			continue
		}
		delete(row, clientID)
		if len(row) == 0 {
			delete(p.rows, wsID)
		}
		active := p.activeLocked(wsID)
		if active != p.lastActive[wsID] {
			if active == "" {
				delete(p.lastActive, wsID)
			} else {
				p.lastActive[wsID] = active
			}
			changes[wsID] = active
		}
	}
	return changes
}

// activeLocked returns the unique client whose last heartbeat is
// strictly more recent than every other client's. Returns "" when the
// workspace has no entries or when the top two heartbeats share the
// same timestamp (no clear leader). Single-pass O(n) — tracks both the
// current leader and the next-best timestamp so the tie check folds
// into the same iteration.
func (p *PresenceTracker) activeLocked(workspaceID string) string {
	row := p.rows[workspaceID]
	var leader *presenceEntry
	var runnerUpAt time.Time
	for _, e := range row {
		switch {
		case leader == nil || e.receivedAt.After(leader.receivedAt):
			if leader != nil {
				runnerUpAt = leader.receivedAt
			}
			leader = e
		case e.receivedAt.After(runnerUpAt):
			runnerUpAt = e.receivedAt
		}
	}
	if leader == nil {
		return ""
	}
	if !leader.receivedAt.After(runnerUpAt) && len(row) > 1 {
		return ""
	}
	return leader.clientID
}
