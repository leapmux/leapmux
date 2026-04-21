package service

import (
	"context"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// failedEntryTTL bounds how long a terminal-state failure entry lingers
// in the registry after fail() so long-lived workers with user churn
// don't accumulate entries for never-closed failed tabs. The DB
// startup_error column is the authoritative source across restarts, so
// expiry just evicts the in-memory copy; subsequent reads fall back to
// the DB path.
const failedEntryTTL = 5 * time.Minute

// startupEntry tracks the in-flight (or recently-failed) startup of a single
// agent or terminal. It exists to:
//   - expose STARTING / STARTUP_FAILED status to callers of ListAgents /
//     ListTerminals and to Context.agentToProto — status is otherwise derived from
//     "is the process in the Manager?" which doesn't distinguish "starting"
//     from "inactive".
//   - let CloseAgent / CloseTerminal cancel an in-flight startup goroutine.
//   - retain the startup error so the frontend can re-fetch it after a
//     page refresh (it is not persisted across worker restarts; that is
//     an acceptable edge case).
//   - hold a pending terminal resize (cols/rows) that arrived before the
//     PTY was registered in the Manager, so runTerminalStartup can apply
//     it the moment StartTerminal returns.
type startupEntry struct {
	// failed is set once startup fails terminally. While false and cancel
	// is non-nil, startup is still in progress (STARTING).
	failed       bool
	startupError string
	// startupMessage is the current phase label ("Checking Git status…",
	// "Starting zsh…"). Stored so a late WatchEvents subscriber —
	// typically the client that just opened the tab and subscribed after
	// the initial STARTING broadcast already fired — can surface it
	// through catch-up replay instead of showing a generic fallback.
	startupMessage string
	cancel         context.CancelFunc
	// evictTimer fires failedEntryTTL after fail() to drop the entry.
	// Stored so cancelAndClear/succeed can Stop() it and release the
	// runtime timer slot early.
	evictTimer *time.Timer
	// pendingResize carries the latest ResizeTerminal dims that arrived
	// while the PTY was still being spawned. The backend PTY is created
	// with the placeholder 80x24 from the OpenTerminal request; the
	// frontend's first fit() fires long before the Manager holds the
	// terminal, so without this stash its RPC would land on a not-yet-
	// registered terminal, be dropped, and vim/nvim would see 80x24 on
	// its first TIOCGWINSZ query. runTerminalStartup calls
	// takePendingResize after StartTerminal and applies the result.
	pendingResize    [2]uint16
	hasPendingResize bool
}

// startupCore is the shared state-machine for tracking a set of in-flight
// or recently-failed startups keyed by id. It is embedded by the agent
// and terminal registries which add typed status accessors.
type startupCore struct {
	mu      sync.Mutex
	entries map[string]*startupEntry
}

func newStartupCore() startupCore {
	return startupCore{entries: make(map[string]*startupEntry)}
}

// begin records an entry in STARTING state. The cancel function should be
// the one tied to the startup goroutine's context.
func (r *startupCore) begin(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[id] = &startupEntry{cancel: cancel}
}

// setMessage records the current phase label for id. No-op if the entry
// is absent. Call this before firing the STARTING broadcast so that a
// watcher that arrives after the broadcast (via WatchEvents catch-up)
// still reads the same phase label.
func (r *startupCore) setMessage(id, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries[id]; ok {
		entry.startupMessage = message
	}
}

// succeed removes the entry (on successful startup the runtime state comes
// from the Manager, not from this registry).
func (r *startupCore) succeed(id string) {
	r.mu.Lock()
	entry := r.entries[id]
	delete(r.entries, id)
	r.mu.Unlock()
	if entry != nil && entry.evictTimer != nil {
		entry.evictTimer.Stop()
	}
}

// fail retains the entry with the error string for later observation and
// schedules its eviction after failedEntryTTL so a failed tab the user
// never closes eventually drops out of the in-memory map. Status queries
// after eviction fall back to the persisted startup_error column.
func (r *startupCore) fail(id, startupError string) {
	entry := &startupEntry{failed: true, startupError: startupError}
	entry.evictTimer = time.AfterFunc(failedEntryTTL, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if current, ok := r.entries[id]; ok && current == entry {
			delete(r.entries, id)
		}
	})
	r.mu.Lock()
	// Stop any prior pending eviction if fail is called twice.
	if prev, ok := r.entries[id]; ok && prev.evictTimer != nil {
		prev.evictTimer.Stop()
	}
	r.entries[id] = entry
	r.mu.Unlock()
}

// cancelAndClear triggers the cancel func if one is registered and removes
// the entry. Called from Close handlers so an in-flight startup is torn
// down along with the agent/terminal.
func (r *startupCore) cancelAndClear(id string) {
	r.mu.Lock()
	entry := r.entries[id]
	delete(r.entries, id)
	r.mu.Unlock()
	if entry == nil {
		return
	}
	if entry.evictTimer != nil {
		entry.evictTimer.Stop()
	}
	if entry.cancel != nil {
		entry.cancel()
	}
}

// setPendingResize records the latest ResizeTerminal dims for id. Returns
// false if no startup is in flight for id (caller should treat that as
// "terminal not found"). Overwrites any prior stashed dims so only the
// most recent resize is applied when startup completes.
func (r *startupCore) setPendingResize(id string, cols, rows uint16) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[id]
	if !ok || entry.failed {
		return false
	}
	entry.pendingResize = [2]uint16{cols, rows}
	entry.hasPendingResize = true
	return true
}

// takePendingResize returns the stashed dims (if any) and clears the
// slot. Called by runTerminalStartup after StartTerminal registers the
// PTY so the stored size lands on the real terminal.
func (r *startupCore) takePendingResize(id string) (cols, rows uint16, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, found := r.entries[id]
	if !found || !entry.hasPendingResize {
		return 0, 0, false
	}
	cols, rows = entry.pendingResize[0], entry.pendingResize[1]
	entry.hasPendingResize = false
	return cols, rows, true
}

// clearPendingResize drops any stashed dims for id. Called from the
// ResizeTerminal handler when the PTY is already in the Manager so the
// direct Resize call has already been applied; leaving a stale stash in
// place would cause runTerminalStartup to overwrite the newer size.
func (r *startupCore) clearPendingResize(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries[id]; ok {
		entry.hasPendingResize = false
	}
}

// snapshot returns (failed, startupError, startupMessage, ok) for the given id.
func (r *startupCore) snapshot(id string) (failed bool, startupError, startupMessage string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, found := r.entries[id]
	if !found {
		return false, "", "", false
	}
	return entry.failed, entry.startupError, entry.startupMessage, true
}

// startupRegistry wraps startupCore with typed status accessors for a
// specific proto enum (AgentStatus, TerminalStatus). Callers supply the
// three enum values at construction time so each registry instance
// returns values in its own proto namespace.
type startupRegistry[S ~int32] struct {
	startupCore
	unspecifiedStatus S
	startingStatus    S
	failedStatus      S
}

func newStartupRegistry[S ~int32](unspec, starting, failed S) *startupRegistry[S] {
	return &startupRegistry[S]{
		startupCore:       newStartupCore(),
		unspecifiedStatus: unspec,
		startingStatus:    starting,
		failedStatus:      failed,
	}
}

// status returns the status override, startup_error, and the current
// phase message for an id, if one is currently tracked. ok=false means
// the id is not in the registry and the caller should derive status
// from the runtime Manager (agents) or default to READY (terminals).
func (r *startupRegistry[S]) status(id string) (status S, startupError, startupMessage string, ok bool) {
	failed, errStr, msg, found := r.snapshot(id)
	if !found {
		return r.unspecifiedStatus, "", "", false
	}
	if failed {
		return r.failedStatus, errStr, "", true
	}
	return r.startingStatus, "", msg, true
}

func newAgentStartupRegistry() *startupRegistry[leapmuxv1.AgentStatus] {
	return newStartupRegistry(
		leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED,
		leapmuxv1.AgentStatus_AGENT_STATUS_STARTING,
		leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED,
	)
}

func newTerminalStartupRegistry() *startupRegistry[leapmuxv1.TerminalStatus] {
	return newStartupRegistry(
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED,
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
	)
}
