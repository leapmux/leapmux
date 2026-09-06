package service

import (
	"database/sql"
	"errors"
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

	// published is the last state broadcast, and hasPublished distinguishes
	// "published IDLE" from "never published". Without the second field the
	// first genuine idle transition after startup would look like a no-op and
	// never reach a client.
	published    leapmuxv1.AgentActivityState
	hasPublished bool

	// publishedSeq is the ticket of the refresh that recorded `published`. A
	// refresh that read its registry rows EARLIER than one which already
	// published carries a lower ticket and is dropped, so a slow reader cannot
	// latch a stale answer. Without it the edge trigger then swallows the
	// correction: the next real change compares equal to what the stale publish
	// recorded, and the tab keeps a spinner for a whole turn and misses its
	// settle.
	publishedSeq uint64
}

// activityInputs are the derivation's inputs from OUTSIDE the entry. Reading
// them takes the registry cache lock and can reach the database, so they are
// read before the entry's own lock and never underneath it.
type activityInputs struct {
	activeTasks  int32
	processAlive bool
	// childID is empty for a root, which is what selects the roll-up rule.
	childID string
}

// activityStateLocked is THE rule, against inputs the caller already gathered.
// Caller must hold st.mu, so the entry's own inputs are read in the same
// critical section that records the answer.
//
// The order is not arbitrary. A dead process is idle whatever else is recorded.
// An agent blocked on a permission prompt is WAITING even mid-turn, because the
// user is the one holding it up -- the indicator must not spin, and the close
// guard must still warn. Only then does running work count.
func activityStateLocked(st *agentActivity, in activityInputs) leapmuxv1.AgentActivityState {
	if !in.processAlive {
		return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE
	}
	// A CHILD is never WAITING: a subagent owns no turn and no process, and its
	// prompts are recorded against the root that feeds it. Its registry row IS
	// its run, so it needs no turn-start signal of its own.
	if in.childID != "" {
		return idleOrWorking(in.activeTasks > 0)
	}
	// A prompt with no turn behind it does NOT make the agent wait: a shell task
	// can ask for permission after its agent's turn ended, and what a close would
	// interrupt there is the task, which activeTasks already reports.
	if len(st.pendingControl) > 0 && st.turnActive {
		return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
	}
	if len(st.pendingControl) > 0 {
		return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE
	}
	return idleOrWorking(st.turnActive || in.activeTasks > 0)
}

func idleOrWorking(working bool) leapmuxv1.AgentActivityState {
	if working {
		return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
	}
	return leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE
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

// AgentActivity is what the Worker knows about one agent's work: the state, and
// the count that lets a close guard name what it would interrupt.
type AgentActivity struct {
	State       leapmuxv1.AgentActivityState
	ActiveTasks int32
}

// Working reports whether a turn or a background task is in flight. The
// thinking indicator and the Interrupt button read this one, and a permission
// prompt deliberately turns it off: the user is looking straight at the prompt.
func (a AgentActivity) Working() bool {
	return a.State == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
}

// InterruptsWork reports whether closing this tab would stop something. Both
// close guards resolve to it, so they cannot answer differently -- and it
// differs from Working precisely for a blocked agent, whose turn a close still
// kills.
func (a AgentActivity) InterruptsWork() bool {
	return a.State == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING ||
		a.State == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
}

// AgentBusy reports whether agentID is working, without broadcasting. The READ
// path behind the thinking indicator, so a list query and a live event can never
// disagree about the rule.
func (h *OutputHandler) AgentBusy(agentID, rootAgentID string) bool {
	return h.AgentActivitySnapshot(agentID, rootAgentID).Working()
}

// AgentActivitySnapshot is THE definition of "what is this agent doing". Every
// surface -- the thinking indicator, the Interrupt button, the close guard in
// the browser and in the CLI -- resolves to this one rule.
//
// It returns all three answers together because every caller that wants one
// wants another: AgentInfo carries all three, and the CLI's refusal message
// states two. Asking separately read the registry twice per agent, on a path
// that runs for every row of a ListAgents reply.
//
// The order is not arbitrary. A dead process is idle whatever else is recorded.
// An agent blocked on a permission prompt is idle even mid-turn, because the
// user is the one holding it up. Only then does running work count.
func (h *OutputHandler) AgentActivitySnapshot(agentID, rootAgentID string) AgentActivity {
	rootAgentID = h.resolveRoot(agentID, rootAgentID)
	return h.activitySnapshotFrom(agentID, rootAgentID, h.backgroundTaskRows(rootAgentID))
}

// activitySnapshotFrom is AgentActivitySnapshot against a root the caller
// already resolved and registry rows it already holds. refreshActivityTree
// derives a whole tree through it from ONE read, so two sibling tabs cannot be
// published from lists taken at different moments.
func (h *OutputHandler) activitySnapshotFrom(agentID, rootAgentID string, rows []bgtask.Item) AgentActivity {
	in := h.externalActivityInputs(agentID, rootAgentID, rows)
	if !in.processAlive {
		// Answered without touching the entry, so a pure read of a dead agent
		// creates none. A process that is gone waits for nobody either.
		return AgentActivity{
			State:       leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
			ActiveTasks: in.activeTasks,
		}
	}
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return AgentActivity{State: activityStateLocked(st, in), ActiveTasks: in.activeTasks}
}

// externalActivityInputs gathers everything the derivation needs from outside
// the entry.
func (h *OutputHandler) externalActivityInputs(agentID, rootAgentID string, rows []bgtask.Item) activityInputs {
	// A root counts every descendant's row, which is the roll-up its tab has
	// always meant. A child counts only its own, because a subagent's registry
	// row IS its run -- and counting the root's whole registry kept a finished
	// subagent spinning for as long as any SIBLING ran.
	childID := ""
	if agentID != rootAgentID {
		childID = agentID
	}
	activeTasks := countActiveBackgroundTasks(rows, childID)
	if childID != "" && !hasRegistryRowFor(rows, childID) {
		// The display list holds no row for this child, and that is not yet an
		// answer: the cap gives up its oldest ACTIVE row when the pool is full of
		// running work, so a subagent that is still going can be missing from it.
		// Reading that as idle drops the spinner and hides the Interrupt button on
		// a run the user can see, which is the same failure
		// resolveChildRegistryRow's point lookup exists to prevent for messaging.
		//
		// One indexed lookup, and only on the miss. A child tab the user has open
		// is almost always in the list, so this costs nothing in the common case.
		activeTasks = h.storedChildActiveTasks(childID)
	}
	if childID == "" {
		// The display cap gives up an ACTIVE row when a pool of 64 holds no
		// finished one, and retention keeps that row running in the table. The
		// list can no longer see it, so a root that fanned out that wide would
		// settle -- ringing the completion sound and letting the close guard pass
		// -- while a subagent still ran. The registry is the only thing that knows
		// what it hid, so it is the only thing that can add it back.
		//
		// The CHILD path needs none of this: it already answers a missing row from
		// the table above.
		activeTasks += h.hiddenActiveTasks(rootAgentID)
	}
	return activityInputs{
		activeTasks: activeTasks,
		// The feeding process. A child never owns one, so both paths ask about
		// the root -- a child tab is busy only while the process feeding it runs.
		processAlive: h.isProcessRunning(rootAgentID),
		childID:      childID,
	}
}

// hiddenActiveTasks reports how many still-running rows the display cap dropped
// from this root's list.
func (h *OutputHandler) hiddenActiveTasks(rootAgentID string) int32 {
	if h.queries == nil {
		return 0
	}
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	return cache.EvictedActiveCount()
}

// isProcessRunning answers the process path through the seam, falling back to
// the agent manager. A handler with neither reports idle: no process, no work.
//
// AgentAlive, not HasAgent. The Manager keeps the slot registered through the
// whole exit callback, so that callback can pause durable input before another
// process starts -- and the same callback clears the pending prompts and ends
// the registry rows. HasAgent answers yes for all of it, so the removal of the
// LAST prompt derives busy (the turn flag is still set) and broadcasts it. The
// client then shows the spinner and the Interrupt button again, for an agent
// whose process already died.
func (h *OutputHandler) isProcessRunning(rootAgentID string) bool {
	if h.processRunning != nil {
		return h.processRunning(rootAgentID)
	}
	return h.agents != nil && h.agents.AgentAlive(rootAgentID)
}

// backgroundTaskRows returns the root's registry display list.
//
// The DISPLAY list rather than a table count, so the root pays no DB round trip
// on the registry chokepoint and its answer matches the Background tasks list
// the client renders. The COUNT saturates at bgtask.MaxTasks per kind, which is
// what a display count should do -- it is the number the sidebar shows.
//
// The list alone is NOT exact for the root, and the caller corrects it. The cap
// gives up an ACTIVE row when a full pool of 64 holds no finished one
// (makeRoomLocked falls through to evictOldestInBucketLocked), and a row that
// arrives already final evicts a running one the same way. A LINKED row survives
// that in the table (retention.keep), so the subagent keeps running with no row
// here. externalActivityInputs adds those back from the registry's own count of
// what it hid -- see hiddenActiveTasks -- because without it a root that fanned
// out that wide settles while a subagent still runs.
//
// An UNLINKED row -- a shell task -- is deleted outright rather than retained,
// so nothing can report it and the count does not try.
//
// A CHILD's question is different, and the cap changes it sooner: see the miss
// path in AgentActivitySnapshot.
func (h *OutputHandler) backgroundTaskRows(rootAgentID string) []bgtask.Item {
	if h.queries == nil {
		// No store, so no registry: there is no work to report.
		return nil
	}
	rows, err := h.LoadBackgroundTasks(h.bgTaskCtx(), rootAgentID)
	if err != nil {
		// Report idle rather than guessing busy, and the seeding rule is what
		// makes that safe rather than merely convenient. LoadBackgroundTasks
		// fails only while the cache is unseeded, because ensureSeededLocked
		// returns early once `seeded` is set and nothing ever clears it. At that
		// point no row this process wrote can exist -- every write seeds first --
		// and the boot sweep already interrupted every row the previous process
		// left. So there is no active row to miss, and guessing busy would pin a
		// spinner on an agent that owns no work. A turn in progress is unaffected
		// either way: turnActive is a separate input.
		slog.Warn("activity: load background tasks", "agent_id", rootAgentID, "error", err)
		return nil
	}
	return rows
}

// hasRegistryRowFor reports whether the display list holds a row for this child
// at all, which is what separates "this subagent finished" from "the list cannot
// say".
//
// An empty id is nobody's row. A registry-only row -- a shell task -- carries no
// child agent id, so a blank query would match the first one and report an
// answer the list never gave. The caller guards this today; the guard is here so
// it does not have to.
func hasRegistryRowFor(rows []bgtask.Item, childAgentID string) bool {
	if childAgentID == "" {
		return false
	}
	for i := range rows {
		if rows[i].ChildAgentID == childAgentID {
			return true
		}
	}
	return false
}

// storedChildActiveTasks answers the child path from the TABLE, for a child
// whose row left the display list.
//
// A read failure answers 0, which costs one child tab its spinner and its
// Interrupt button. It costs no close warning: both close guards exempt a
// subagent tab outright, because closing one is a UI-only act that stops
// nothing. So the trade is a missing spinner against an Interrupt button
// offered for a run that already ended, and the first is the cheaper mistake.
func (h *OutputHandler) storedChildActiveTasks(childAgentID string) int32 {
	if h.queries == nil {
		return 0
	}
	row, err := h.queries.GetAgentBackgroundTaskByChildAgentID(h.bgTaskCtx(), childAgentID)
	if err != nil {
		// ErrNoRows is the ordinary answer for an agent that owns no registry row
		// -- every root reaches this only through the childID guard above, so a
		// miss here really means "no such subagent run".
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("activity: read child background task", "child_agent_id", childAgentID, "error", err)
		}
		return 0
	}
	if bgItemFromRow(row).Status.IsFinished() {
		return 0
	}
	return 1
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
	// The ticket is taken BEFORE the rows are read, so it orders refreshes by
	// the age of the inputs they derive from.
	seq := h.activitySeq.Add(1)
	h.refreshActivityFrom(agentID, rootAgentID, h.backgroundTaskRows(rootAgentID), seq)
}

// refreshActivityFrom is refreshActivity against a root the caller already
// resolved, registry rows it already holds, and the ticket it took before
// reading them.
//
// The entry's own inputs are read INSIDE the same critical section that records
// the answer. Reading them separately let two refreshes derive, then publish in
// the reverse order, so a stale value latched.
//
// One residual, stated because it is not free to close: the broadcast runs after
// the unlock, because BroadcastAgentEvent can block on a slow transport and
// holding a lock across it would serialize every registry op for the root behind
// the slowest watcher. So two publishes that BOTH survive the ticket can still
// reach the wire out of order. The ticket removes the case that mattered -- a
// stale reader overwriting a fresher answer, which the edge trigger then made
// permanent -- and leaves only a reordering of two genuine transitions.
func (h *OutputHandler) refreshActivityFrom(agentID, rootAgentID string, rows []bgtask.Item, seq uint64) {
	in := h.externalActivityInputs(agentID, rootAgentID, rows)

	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	if st.hasPublished && seq < st.publishedSeq {
		// This refresh read its rows before the one that already published. Its
		// answer is older, so it must not overwrite a fresher one.
		st.mu.Unlock()
		return
	}
	state := activityStateLocked(st, in)
	if st.hasPublished && st.published == state {
		st.publishedSeq = seq
		st.mu.Unlock()
		return
	}
	var toolUses *int32
	working := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
	if state != working && st.published == working {
		// The settle edge spends the count the last turn end recorded. A settle
		// that follows no turn end -- a control request, a process exit -- leaves
		// it unset, which is what tells the client to alert unconditionally.
		//
		// Only the WORKING -> not-WORKING edge spends it. A move between IDLE and
		// WAITING_FOR_USER is not a settle, and consuming the count there would
		// silence the settle the turn is still heading for.
		toolUses = st.settledToolUses
		st.settledToolUses = nil
	}
	st.published = state
	st.hasPublished = true
	st.publishedSeq = seq
	st.mu.Unlock()

	h.watcher.BroadcastAgentEvent(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_ActivityChanged{
			ActivityChanged: &leapmuxv1.AgentActivityChanged{
				State:       state,
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
	// ONE read for the whole tree, and ONE ticket: every tab here derives from
	// the same instant, so they share the age that orders them against a
	// concurrent refresh. Asking per tab reloaded the same list once for the
	// root and once more for every child, and each tab then derived from a
	// different instant.
	seq := h.activitySeq.Add(1)
	rows := h.backgroundTaskRows(rootAgentID)
	h.refreshActivityFrom(rootAgentID, rootAgentID, rows, seq)
	for _, childID := range h.treeChildIDs(rootAgentID, rows) {
		h.refreshActivityFrom(childID, rootAgentID, rows, seq)
	}
}

// treeChildIDs lists every child that needs recomputing under this root.
//
// The display list alone does not answer this. Its cap gives up the oldest
// ACTIVE row when the pool is full, which is exactly why
// AgentActivitySnapshot answers a missing child from the TABLE instead -- so
// the cap can hide a child that was already published BUSY. Recomputing only
// the listed rows leaves that child with a spinner and an Interrupt button
// that nothing ever clears, on a run that already ended.
//
// The activity map holds every child this worker published for, and the cap
// cannot truncate it. Union the two: the list supplies a child whose row
// arrived before any entry did, and the map supplies the child the cap
// dropped.
func (h *OutputHandler) treeChildIDs(rootAgentID string, rows []bgtask.Item) []string {
	seen := make(map[string]struct{}, len(rows))
	childIDs := make([]string, 0, len(rows))
	add := func(childID string) {
		// A root is not its own child, and an empty id is nobody.
		if childID == "" || childID == rootAgentID {
			return
		}
		if _, dup := seen[childID]; dup {
			return
		}
		seen[childID] = struct{}{}
		childIDs = append(childIDs, childID)
	}
	for i := range rows {
		add(rows[i].ChildAgentID)
	}
	h.activity.Range(func(key, v any) bool {
		agentID, ok := key.(string)
		if !ok {
			return true
		}
		st := v.(*agentActivity)
		st.mu.Lock()
		owner := st.rootAgentID
		st.mu.Unlock()
		if owner == rootAgentID {
			add(agentID)
		}
		return true
	})
	return childIDs
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
	if active {
		return
	}
	// Only the settle that THIS clear produces may spend the count. When the
	// refresh above settled the agent it already spent it; when the agent
	// stayed busy, the work still running belongs to some other turn, so the
	// settle that eventually comes is not this turn's. Drop the count there and
	// let that settle alert unconditionally.
	//
	// Without this, a turn that used no tool while an unrelated shell task ran
	// left a zero behind, and the client suppresses a zero. The shell task then
	// finished hours later in silence.
	st.mu.Lock()
	st.settledToolUses = nil
	st.mu.Unlock()
}

// noteTurnEnded records the finished turn's tool-call count for the settle edge
// this turn leads to. It publishes nothing itself: the turn may leave a subagent
// running, in which case the agent stays busy and the count waits.
//
// It takes the root like every other mutator. A child's FIRST touch can be a
// turn end -- a subagent whose result envelope arrives before its task_started,
// so no registry row links it yet -- and an entry created with no root resolves
// against ITSELF. That asks whether a process named after the CHILD runs, and
// none ever does, so the subagent reads idle for the rest of its run.
func (h *OutputHandler) noteTurnEnded(agentID, rootAgentID string, count int32, ok bool) {
	st := h.activityFor(agentID, rootAgentID)
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
// Whatever the old process did died with it: no envelope arrives to close that
// turn, and the new process never began one. The only honest state for a freshly
// started agent is idle, and it stays idle until its provider reports a turn of
// its own.
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
	h.activityRefreshes.Add(1)
	go func() {
		defer h.activityRefreshes.Done()
		h.refreshActivityTree(rootAgentID)
	}()
}

// WaitActivityRefreshes joins the deferred tree refreshes resetAgentActivity
// spawned. Shutdown calls it after the processes stop and before it cancels the
// background-task context, so no refresh reads the registry or broadcasts after
// the caller closes the database.
func (h *OutputHandler) WaitActivityRefreshes() {
	h.activityRefreshes.Wait()
}
