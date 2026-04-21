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
	defer r.mu.Unlock()
	delete(r.entries, id)
}

// fail retains the entry with the error string for later observation and
// schedules its eviction after failedEntryTTL so a failed tab the user
// never closes eventually drops out of the in-memory map. Status queries
// after eviction fall back to the persisted startup_error column.
func (r *startupCore) fail(id, startupError string) {
	r.mu.Lock()
	r.entries[id] = &startupEntry{failed: true, startupError: startupError}
	r.mu.Unlock()
	time.AfterFunc(failedEntryTTL, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if entry, ok := r.entries[id]; ok && entry.failed {
			delete(r.entries, id)
		}
	})
}

// cancelAndClear triggers the cancel func if one is registered and removes
// the entry. Called from Close handlers so an in-flight startup is torn
// down along with the agent/terminal.
func (r *startupCore) cancelAndClear(id string) {
	r.mu.Lock()
	entry := r.entries[id]
	delete(r.entries, id)
	r.mu.Unlock()
	if entry != nil && entry.cancel != nil {
		entry.cancel()
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

// agentStartupRegistry and terminalStartupRegistry are intentionally
// separate wrappers around startupCore even though their status()
// methods look structurally similar — each returns a distinct proto
// enum type (AgentStatus vs TerminalStatus). A generic collapse would
// force callers to pass the enum values in, which is noisier than the
// typed wrapper at every call site.

// agentStartupRegistry tracks in-flight / recently-failed agent startups.
type agentStartupRegistry struct{ startupCore }

func newAgentStartupRegistry() *agentStartupRegistry {
	return &agentStartupRegistry{startupCore: newStartupCore()}
}

// status returns the status override, startup_error, and the current
// phase message for an agent, if one is currently tracked. ok=false
// means the agent is not in the registry and the caller should derive
// status from the runtime Manager.
func (r *agentStartupRegistry) status(agentID string) (status leapmuxv1.AgentStatus, startupError, startupMessage string, ok bool) {
	failed, errStr, msg, found := r.snapshot(agentID)
	if !found {
		return leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED, "", "", false
	}
	if failed {
		return leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, errStr, "", true
	}
	return leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, "", msg, true
}

// terminalStartupRegistry tracks in-flight / recently-failed terminal startups.
type terminalStartupRegistry struct{ startupCore }

func newTerminalStartupRegistry() *terminalStartupRegistry {
	return &terminalStartupRegistry{startupCore: newStartupCore()}
}

// status returns the status override, startup_error, and the current
// phase message for a terminal.
func (r *terminalStartupRegistry) status(terminalID string) (status leapmuxv1.TerminalStatus, startupError, startupMessage string, ok bool) {
	failed, errStr, msg, found := r.snapshot(terminalID)
	if !found {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED, "", "", false
	}
	if failed {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, errStr, "", true
	}
	return leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, "", msg, true
}
