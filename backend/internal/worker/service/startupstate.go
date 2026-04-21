package service

import (
	"context"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// startupKind identifies whether a startup belongs to an agent or a terminal.
type startupKind int

const (
	startupKindAgent startupKind = iota
	startupKindTerminal
)

// startupEntry tracks the in-flight (or recently-failed) startup of a single
// agent or terminal. It exists to:
//   - expose STARTING / STARTUP_FAILED status to callers of ListAgents /
//     ListTerminals and to agentToProto — status is otherwise derived from
//     "is the process in the Manager?" which doesn't distinguish "starting"
//     from "inactive".
//   - let CloseAgent / CloseTerminal cancel an in-flight startup goroutine.
//   - retain the startup error so the frontend can re-fetch it after a
//     page refresh (it is not persisted across worker restarts; that is
//     an acceptable edge case).
type startupEntry struct {
	kind startupKind
	// failed is set once startup fails terminally. While false and cancel
	// is non-nil, startup is still in progress (STARTING).
	failed       bool
	startupError string
	cancel       context.CancelFunc
}

// startupRegistry is the Context-scoped tracker for all in-flight or
// recently-failed startups.
type startupRegistry struct {
	mu      sync.Mutex
	entries map[string]*startupEntry // id -> entry
}

func newStartupRegistry() *startupRegistry {
	return &startupRegistry{entries: make(map[string]*startupEntry)}
}

// begin records an entry in STARTING state. The cancel function should be
// the one tied to the startup goroutine's context.
func (r *startupRegistry) begin(id string, kind startupKind, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// If a caller (e.g. a racing Close) already recorded a failed entry,
	// don't clobber the error — just attach the cancel so it can still be
	// triggered. Typical callers go begin → done; this branch is defensive.
	if existing, ok := r.entries[id]; ok && existing.failed {
		existing.cancel = cancel
		return
	}
	r.entries[id] = &startupEntry{kind: kind, cancel: cancel}
}

// succeed removes the entry (on successful startup the runtime state comes
// from the Manager, not from this registry).
func (r *startupRegistry) succeed(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, id)
}

// fail retains the entry with the error string for later observation.
func (r *startupRegistry) fail(id, startupError string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[id] = &startupEntry{
		kind:         agentKindFromExisting(r.entries[id]),
		failed:       true,
		startupError: startupError,
	}
}

// cancelAndClear triggers the cancel func if one is registered and removes
// the entry. Called from Close handlers so an in-flight startup is torn
// down along with the agent/terminal.
func (r *startupRegistry) cancelAndClear(id string) {
	r.mu.Lock()
	entry := r.entries[id]
	delete(r.entries, id)
	r.mu.Unlock()
	if entry != nil && entry.cancel != nil {
		entry.cancel()
	}
}

// agentStatus returns the status override and startup_error for an agent,
// if one is currently tracked. ok=false means the agent is not in the
// registry and the caller should derive status from the runtime Manager.
func (r *startupRegistry) agentStatus(agentID string) (status leapmuxv1.AgentStatus, startupError string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, found := r.entries[agentID]
	if !found || entry.kind != startupKindAgent {
		return leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED, "", false
	}
	if entry.failed {
		return leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, entry.startupError, true
	}
	return leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, "", true
}

// terminalStatus is the terminal analogue of agentStatus.
func (r *startupRegistry) terminalStatus(terminalID string) (status leapmuxv1.TerminalStatus, startupError string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, found := r.entries[terminalID]
	if !found || entry.kind != startupKindTerminal {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED, "", false
	}
	if entry.failed {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, entry.startupError, true
	}
	return leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, "", true
}

// agentKindFromExisting preserves the kind of an existing entry if present,
// defaulting to startupKindAgent. Used when fail() is called and no entry
// exists yet (shouldn't happen via normal flow, but defensive).
func agentKindFromExisting(entry *startupEntry) startupKind {
	if entry != nil {
		return entry.kind
	}
	return startupKindAgent
}
