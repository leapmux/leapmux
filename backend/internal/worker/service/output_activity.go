package service

import (
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The Worker owns the answer to "is this agent busy".
//
// It used to have no answer at all: each provider kept a private turn flag for
// its own control flow and published nothing, so the browser re-derived the
// state on every render by scanning the transcript backwards, and the Control
// CLI could not ask the question at all. This file is the one derivation, and
// both surfaces read it.
//
// The state is EDGE-TRIGGERED. A notification-class broadcast pays a snapshot,
// a marshal and one SendStream per subscribed channel whether or not any tab is
// on screen, so refreshActivity broadcasts only when the derived value actually
// changes -- never per output line.
//
// It is also PURELY IN MEMORY, and must stay that way. Do not add a column for
// it. Every process boundary resets it (see NoteAgentProcessStarted), so a
// persisted value could only ever be a lie waiting to be read: a worker restart
// would load "busy" for an agent whose process died with the previous one, and
// nothing about that stored row would say so. The registry and control-request
// tables this derivation reads are persisted for their own reasons; the answer
// derived from them is not.

// agentActivity is one agent's activity inputs plus the last value published
// for it. Every field is guarded by mu.
//
// One entry per AGENT id, roots and virtual children alike, because a child tab
// shows its own spinner and its own Interrupt button.
type agentActivity struct {
	mu sync.Mutex

	// rootAgentID is the id whose PROCESS feeds this agent; it equals the agent's
	// own id for a root. Recorded the first time a sink touches the entry,
	// because the handler-level control-request paths know only an agent id and
	// resolving a child against ITSELF would report every child idle the moment
	// a prompt was answered.
	rootAgentID string

	// turnActive is what the provider last reported through
	// OutputSink.SetTurnActive. Meaningful for a root only: a child owns no
	// process and no turn of its own, and its run IS its registry row.
	turnActive bool

	// pendingControl holds the request ids of the unanswered control requests
	// (permission prompts) this agent is blocked on. An agent waiting for the
	// user is NOT busy -- it is waiting, and the user is looking straight at the
	// prompt.
	//
	// Tracked in memory rather than counted in agent_background_tasks' sibling
	// table because every mutation already funnels through this handler, and a
	// COUNT on the settle path would put a DB round trip inside the registry
	// chokepoint. A worker restart drops the map, which is correct: the boot
	// sweep marks every active row interrupted and no process is running yet, so
	// the agent derives idle either way.
	pendingControl map[string]struct{}

	// settledToolUses carries the tool-call count of the turn that most recently
	// ended, waiting for the busy->false edge that turn leads to. A turn that
	// spawns a subagent keeps the agent busy past its own end, so the count has
	// to outlive the turn to reach the settle it belongs to. Cleared when a new
	// turn starts and consumed by the edge.
	settledToolUses *int32

	// published is the last value broadcast, and hasPublished distinguishes
	// "published false" from "never published". Without the second field the
	// first genuine idle transition after startup would look like a no-op and
	// never reach a client.
	published    bool
	hasPublished bool
}

// activityFor returns agentID's entry, creating it on first sight. A non-empty
// rootAgentID is recorded so later callers that know only an agent id can still
// resolve the feeding process.
func (h *OutputHandler) activityFor(agentID, rootAgentID string) *agentActivity {
	v, ok := h.activity.Load(agentID)
	if !ok {
		v, _ = h.activity.LoadOrStore(agentID, &agentActivity{rootAgentID: rootAgentID})
	}
	st := v.(*agentActivity)
	if rootAgentID != "" {
		st.mu.Lock()
		st.rootAgentID = rootAgentID
		st.mu.Unlock()
	}
	return st
}

// resolveRoot fills in the feeding process's id when the caller does not know
// it, falling back to the agent's own id for an agent no sink ever registered.
func (h *OutputHandler) resolveRoot(agentID, rootAgentID string) string {
	if rootAgentID != "" {
		return rootAgentID
	}
	if v, ok := h.activity.Load(agentID); ok {
		st := v.(*agentActivity)
		st.mu.Lock()
		known := st.rootAgentID
		st.mu.Unlock()
		if known != "" {
			return known
		}
	}
	return agentID
}

// ForgetActivity drops an agent's activity entry. Called from the same cleanup
// that prunes the other per-agent caches, so a closed agent leaves nothing
// behind in the map.
func (h *OutputHandler) ForgetActivity(agentID string) {
	h.activity.Delete(agentID)
}

// AgentBusy reports the current derived activity for agentID without
// broadcasting. The READ leg behind AgentInfo.busy, so a list query and a live
// event can never disagree about the rule.
func (h *OutputHandler) AgentBusy(agentID, rootAgentID string) bool {
	busy, _ := h.AgentActivitySnapshot(agentID, rootAgentID)
	return busy
}

// ActiveBackgroundTaskCount reports how many pending/running rows this agent's
// work occupies. Reported on AgentInfo so a close guard can name what it refuses
// to interrupt rather than only that it refuses.
func (h *OutputHandler) ActiveBackgroundTaskCount(agentID, rootAgentID string) int32 {
	_, n := h.AgentActivitySnapshot(agentID, rootAgentID)
	return n
}

// AgentActivitySnapshot is THE definition of "is this agent working". Every
// surface -- the thinking indicator, the Interrupt button, the close guard in
// the browser and in the CLI -- resolves to this one rule.
//
// It returns the count alongside the flag because every caller that wants one
// wants the other: AgentInfo carries both, and the CLI's refusal message states
// both. Asking separately read the registry twice per agent, on a path that runs
// for every row of a ListAgents reply.
//
// The order is not arbitrary. A dead process is idle whatever else is recorded.
// An agent blocked on a permission prompt is idle even mid-turn, because the
// user is the one holding it up. Only then does running work count.
func (h *OutputHandler) AgentActivitySnapshot(agentID, rootAgentID string) (busy bool, activeTasks int32) {
	rootAgentID = h.resolveRoot(agentID, rootAgentID)
	// A root counts every descendant's row, which is the roll-up its tab has
	// always meant. A child counts only its own, because a subagent's registry
	// row IS its run -- and counting the root's whole registry kept a finished
	// subagent spinning for as long as any SIBLING ran.
	childID := ""
	if agentID != rootAgentID {
		childID = agentID
	}
	activeTasks = countActiveBackgroundTasks(h.backgroundTaskRows(rootAgentID), childID)

	// The feeding process. A child never owns one, so both legs ask about the
	// root -- a child tab is busy only while the process feeding it runs.
	if !h.isProcessRunning(rootAgentID) {
		return false, activeTasks
	}
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	blocked := len(st.pendingControl) > 0
	turnActive := st.turnActive
	st.mu.Unlock()
	if blocked {
		return false, activeTasks
	}
	if childID == "" {
		return turnActive || activeTasks > 0, activeTasks
	}
	// A subagent needs no turn-start signal of its own; its row is its run.
	return activeTasks > 0, activeTasks
}

// computeBusy is the internal shorthand for the flag alone.
func (h *OutputHandler) computeBusy(agentID, rootAgentID string) bool {
	busy, _ := h.AgentActivitySnapshot(agentID, rootAgentID)
	return busy
}

// isProcessRunning answers the process leg through the seam, falling back to the
// agent manager. A handler with neither reports idle: no process, no work.
func (h *OutputHandler) isProcessRunning(rootAgentID string) bool {
	if h.processRunning != nil {
		return h.processRunning(rootAgentID)
	}
	return h.agents != nil && h.agents.HasAgent(rootAgentID)
}

// backgroundTaskRows returns the root's registry display list.
//
// The DISPLAY list rather than a table count, because the two agree on exactly
// this question: the cap evicts finished rows only (see bgtask.MaxTasks), so an
// ACTIVE row is always in the list. That keeps the answer free of a DB round
// trip on the registry chokepoint, and keeps it identical to what the client
// sees in its own Background tasks list.
func (h *OutputHandler) backgroundTaskRows(rootAgentID string) []bgtask.Item {
	if h.queries == nil {
		// No store, so no registry: there is no work to report.
		return nil
	}
	rows, err := h.LoadBackgroundTasks(h.bgTaskCtx(), rootAgentID)
	if err != nil {
		// Report idle rather than guessing busy. A wrong busy pins the spinner
		// and, worse, keeps the close guard refusing a tab the user can no
		// longer close by any route.
		slog.Warn("activity: load background tasks", "agent_id", rootAgentID, "error", err)
		return nil
	}
	return rows
}

// countActiveBackgroundTasks counts the pending/running rows -- every row when
// childAgentID is empty, or that child's own otherwise. The pure half, so the
// root/child rule is testable without a handler or a database.
func countActiveBackgroundTasks(rows []bgtask.Item, childAgentID string) int32 {
	var n int32
	for i := range rows {
		if rows[i].Status.IsFinished() {
			continue
		}
		if childAgentID == "" || rows[i].ChildAgentID == childAgentID {
			n++
		}
	}
	return n
}

// refreshActivity recomputes agentID's activity and broadcasts it if it changed.
// Every input site calls this; none of them broadcasts on its own.
//
// The caller must hold no registry cache lock: this reads the background-task
// cache and then calls BroadcastAgentEvent, which can block on a slow transport.
func (h *OutputHandler) refreshActivity(agentID, rootAgentID string) {
	if agentID == "" {
		return
	}
	rootAgentID = h.resolveRoot(agentID, rootAgentID)
	busy := h.computeBusy(agentID, rootAgentID)

	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	if st.hasPublished && st.published == busy {
		st.mu.Unlock()
		return
	}
	var toolUses *int32
	if !busy {
		// The settle edge spends the count the last turn end recorded. A settle
		// that follows no turn end -- a control request, a process exit -- leaves
		// it unset, which is what tells the client to alert unconditionally.
		toolUses = st.settledToolUses
	}
	st.settledToolUses = nil
	st.published = busy
	st.hasPublished = true
	st.mu.Unlock()

	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_ActivityChanged{
			ActivityChanged: &leapmuxv1.AgentActivityChanged{
				Busy:        busy,
				NumToolUses: toolUses,
			},
		},
	})
}

// refreshActivityTree recomputes the root and every child that owns a registry
// row under it. A process exit and a registry change both move more than one
// tab's answer at once, and a child that is never recomputed keeps a spinner
// that nothing will ever clear.
func (h *OutputHandler) refreshActivityTree(rootAgentID string) {
	if rootAgentID == "" {
		return
	}
	h.refreshActivity(rootAgentID, rootAgentID)
	if h.queries == nil {
		return
	}
	rows, err := h.LoadBackgroundTasks(h.bgTaskCtx(), rootAgentID)
	if err != nil {
		return
	}
	seen := make(map[string]struct{}, len(rows))
	for i := range rows {
		childID := rows[i].ChildAgentID
		if childID == "" || childID == rootAgentID {
			continue
		}
		if _, dup := seen[childID]; dup {
			continue
		}
		seen[childID] = struct{}{}
		h.refreshActivity(childID, rootAgentID)
	}
}

// setTurnActive records the provider's turn bookkeeping and republishes.
// Providers reach it through OutputSink.SetTurnActive.
func (h *OutputHandler) setTurnActive(agentID, rootAgentID string, active bool) {
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	changed := st.turnActive != active
	st.turnActive = active
	if active {
		// A fresh turn supersedes whatever the previous one left unspent, so a
		// stale count cannot silence the alert for the turn now starting.
		st.settledToolUses = nil
	}
	st.mu.Unlock()
	if !changed {
		return
	}
	h.refreshActivity(agentID, rootAgentID)
}

// noteTurnEnded records the finished turn's tool-call count for the settle edge
// this turn leads to. It publishes nothing itself: the turn may leave a subagent
// running, in which case the agent stays busy and the count waits.
func (h *OutputHandler) noteTurnEnded(agentID string, count int32, ok bool) {
	st := h.activityFor(agentID, "")
	st.mu.Lock()
	if ok {
		v := count
		st.settledToolUses = &v
	} else {
		st.settledToolUses = nil
	}
	st.mu.Unlock()
}

// noteControlRequestAdded records a permission prompt the agent is now blocked
// on, and republishes: an agent waiting for the user is not busy.
func (h *OutputHandler) noteControlRequestAdded(agentID, rootAgentID, requestID string) {
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	if st.pendingControl == nil {
		st.pendingControl = make(map[string]struct{}, 1)
	}
	_, dup := st.pendingControl[requestID]
	st.pendingControl[requestID] = struct{}{}
	st.mu.Unlock()
	if dup {
		return
	}
	h.refreshActivity(agentID, rootAgentID)
}

// noteControlRequestsRemoved drops answered or cancelled prompts and
// republishes. Passing no ids clears every pending prompt, which is what a
// subprocess teardown does.
func (h *OutputHandler) noteControlRequestsRemoved(agentID, rootAgentID string, requestIDs ...string) {
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	changed := false
	if len(requestIDs) == 0 {
		changed = len(st.pendingControl) > 0
		st.pendingControl = nil
	} else {
		for _, requestID := range requestIDs {
			if _, ok := st.pendingControl[requestID]; ok {
				delete(st.pendingControl, requestID)
				changed = true
			}
		}
	}
	st.mu.Unlock()
	if !changed {
		return
	}
	h.refreshActivity(agentID, rootAgentID)
}

// NoteAgentProcessExited forces the whole tree idle when the feeding process
// goes away.
//
// This is also the one place that reports a CRASH. HandleAgentProcessExit
// broadcasts no AgentStatusChange, so before this existed a client that lost the
// process kept whatever activity it last saw until some unrelated event
// corrected it.
func (h *OutputHandler) NoteAgentProcessExited(rootAgentID string) {
	h.resetAgentActivity(rootAgentID)
}

// NoteAgentProcessStarted forces the agent idle as a NEW process takes over.
//
// A restart NEVER resumes a turn, and nothing here tries to bring one back.
// Whatever the old process was doing died with it: no envelope is coming to
// close that turn, and the new process has not begun one. The only honest state
// for a freshly started agent is idle, and it stays idle until its provider
// reports a turn of its own.
//
// This runs at the START rather than trusting the exit path to have run first.
// The two are wired independently -- a worker that restarted while the agent was
// mid-turn never ran an exit handler at all -- and a turn flag that survives
// into a new process is one nothing will ever clear: the indicator spins
// forever, the Interrupt button offers to cancel a turn that does not exist, and
// the close guard refuses a tab on work that already stopped.
func (h *OutputHandler) NoteAgentProcessStarted(rootAgentID string) {
	h.resetAgentActivity(rootAgentID)
}

// resetAgentActivity drops every input and republishes the tree. Shared by the
// two process boundaries, which want exactly the same thing for the same reason:
// no turn is in flight across them.
//
// The RESET is synchronous -- three field writes under the entry's own mutex --
// so the new state is in place before the process it belongs to starts. The
// PUBLISH is not: restartAgentLocked calls this holding the per-agent lifecycle
// lock, and a broadcast reaches SendStream, which blocks on a slow transport.
// Publishing there would stall every relaunch behind the slowest watcher, which
// is the rule broadcastBackgroundTasks already states for the registry.
//
// Deferring it is safe because refreshActivity RE-READS the state rather than
// replaying a captured value: a turn that starts before the goroutine runs
// simply gets published as busy, and the edge trigger drops the duplicate.
func (h *OutputHandler) resetAgentActivity(rootAgentID string) {
	if rootAgentID == "" {
		return
	}
	st := h.activityFor(rootAgentID, rootAgentID)
	st.mu.Lock()
	st.turnActive = false
	st.pendingControl = nil
	// A process boundary is not a completed turn. Drop any unspent count so the
	// settle it produces alerts unconditionally, the way an agent going INACTIVE
	// always has.
	st.settledToolUses = nil
	st.mu.Unlock()
	go h.refreshActivityTree(rootAgentID)
}
