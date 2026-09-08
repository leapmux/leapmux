package service

import (
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

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
// One edge is DEBOUNCED: the stop. A settle waits out settleDelay before it
// reaches a client, and work that resumes
// inside that window cancels it. So a wake or
// a subagent restart never rings the
// completion sound at the moment the agent
// starts again. The broadcast alone waits, and this derivation stays
// exact throughout: AgentActivitySnapshot answers from the inputs and never
// consults the window, so ListAgents and the CLI close guard are unaffected. A
// client that CACHES the published state is not -- see settleDelay for what
// that costs.
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
	// OutputSink.SetTurnActive. The DERIVED state reads it for a root only:
	// activityStateLocked answers a child from its background-task registry
	// row, which IS the child's run.
	//
	// A child still publishes the flag, because the input queue follows the
	// same signal and a collab child owns a queue of its own. So this field is
	// written for a child and never read for one, and setTurnActive must keep
	// every other effect of that publish away from a child entry.
	turnActive bool

	// turnSeq is the ordering token of the last publish this entry accepted. A
	// provider reads its flag under a lock and calls the sink without one, so
	// two goroutines reach here unordered and the older value can arrive second.
	// Comparing the token drops what it overtook.
	//
	// The token is monotonic within ONE provider process. It restarts at zero in
	// the next one, so acceptTurnPublish resets this whenever the publisher
	// changes -- see turnPublisher.
	turnSeq uint64

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

	// settleTimer runs the debounce window for a settle this entry derived but
	// did not publish. See settleDelay.
	//
	// The handle alone answers nothing, because three paths release it early:
	// deliverHeldSettle spends it before the re-derivation, and settleImmediate
	// and ForgetActivity both cancel it. Read settlePending for the STATE and
	// this field only to stop the timer.
	settleTimer settleStopper

	// settlePending says a settle waits: this entry derived a stop and no client
	// knows about it. `published` still says WORKING, and the client still holds
	// WORKING, so the window can still end in silence.
	//
	// It is the state that settleTimer only looks like. voidHeldSettleLocked and
	// setTurnActive both read it, and both were wrong while they read the handle.
	// Cleared where the settle reaches the wire, and where the work comes back.
	settlePending bool

	// interrupted says the USER stopped this agent, so the stop it produces
	// cannot be resumed and publishes at once. InterruptAgent pauses the input
	// queue before it signals, so nothing wakes the agent back into the turn it
	// just cancelled.
	//
	// Cleared wherever the agent moves on: a fresh turn, any publish, and a
	// process boundary. Without that bound, an interrupt the agent IGNORES would
	// leave the mark set and let a LATER resumable stop publish early.
	interrupted bool

	// settleGen counts the windows this entry opened. holdSettleLocked captures
	// the current value in the timer's closure, and deliverHeldSettle publishes
	// only when the entry still carries it.
	//
	// A Stop cannot recall a callback that already started -- see
	// cancelSettleLocked. Without the token, a spent callback publishes for an
	// agent ForgetActivity retired. It also destroys the handle of a window that
	// a later stop opened. Every cancel moves the token, which retires the
	// callback it could not stop.
	settleGen uint64

	// published is the last state broadcast, and hasPublished distinguishes
	// "published IDLE" from "never published". Without the second field the
	// first genuine idle transition after startup would look like a no-op and
	// never reach a client.
	published    leapmuxv1.AgentActivityState
	hasPublished bool

	// publishedSeq is the ticket of the freshest refresh this entry acted on --
	// one that published, one that agreed with what stands, or one that opened a
	// settle window. A refresh that read its registry rows EARLIER carries a
	// lower ticket and is dropped, so a slow reader cannot latch a stale answer.
	// Without it the edge trigger then swallows the correction. The next real
	// change compares equal to what the stale publish recorded, so the tab keeps a
	// spinner for a whole turn and misses its settle.
	publishedSeq uint64
}

// settleDelay is how long a WORKING -> not-WORKING publish waits before it
// reaches a client.
//
// The window exists because the worker sees work ending and the same work
// resuming as two events microseconds apart. A backgrounded shell completes and
// the CLI wakes the agent into a new turn. A subagent's own shell completes and
// the CLI restarts that subagent, which ends its registry row and reopens it in
// the same output burst. Publishing the gap rings the completion sound and drops
// the spinner for an instant, and the work then carries on -- which tells the
// user their agent finished at the moment it started again.
//
// Every genuine settle pays it, and the price rises with the value. The sound,
// the thinking indicator and the Interrupt button are all this much late. The
// browser's close guard is late too, because it reads its cached copy of the
// published state rather than asking the Worker. A tab closed inside the window
// therefore warns about work that already finished. Three seconds covers a wake
// that needs the CLI to start a whole new turn. A few hundred milliseconds would
// cover only the same-burst reopen, so the window takes the longer value and
// pays the cost above.
//
// Only a stop that something can RESUME pays it. A stop the user must clear --
// a permission prompt -- publishes at once, because no wake can arrive while the
// agent waits for an answer. See settleCanResumeLocked.
//
// This derivation is exact while a settle waits. AgentActivitySnapshot reads the
// inputs and never consults the window, so ListAgents and the CLI close guard
// answer correctly throughout; only what was BROADCAST lags. A client that
// CACHES the published state must therefore read the PUBLISHED value and not
// this one, or its cache and the later broadcast disagree and the settle edge
// disappears between them. AgentActivityPublished is that read.
const settleDelay = 3 * time.Second

// settleStopper is the part of *time.Timer a held settle needs.
type settleStopper interface{ Stop() bool }

// settleMode says what a refresh does with a settle it derives.
//
// An int, not a bool, so the ZERO value is the debounced mode. A bool put
// settleImmediate at zero, which made a forgotten field or an unset variable
// silently turn the debounce off.
type settleMode int

const (
	// settleHeld waits out the debounce window, so work that resumes at once
	// never reaches a client as a stop. Every ordinary input uses it, and it is
	// the zero value.
	settleHeld settleMode = iota
	// settleImmediate publishes at once, and supersedes any window in flight.
	// Three reasons ask for it. The window closing, which IS the settle's
	// delivery. A process boundary, where a held settle would leave a timer to
	// fire after shutdown closed the database it reads. And an entry about to be
	// retired, whose held settle has no later refresh to deliver it.
	//
	// "nothing can resume this stop" is NOT one of them. That rule belongs to the
	// derivation, which reads it from its own inputs -- see settleCanResumeLocked.
	settleImmediate
)

// afterSettleDelay schedules f at the end of the settle window.
//
// It goes through the seam rather than calling time.AfterFunc directly, so a
// test fires the window on demand. Sizing this window with a real timer would
// let the machine that runs the suite decide whether a case passes.
func (h *OutputHandler) afterSettleDelay(f func()) settleStopper {
	if h.newSettleTimer != nil {
		return h.newSettleTimer(settleDelay, f)
	}
	return time.AfterFunc(settleDelay, f)
}

// holdSettleLocked opens the window for a settle, unless one already runs.
// Caller must hold st.mu.
//
// It never RE-arms. The window opens at the edge that stopped the work, and a
// second derivation of the same stop is not a second edge.
// Restarting there would let a busy registry push a real
// settle out indefinitely.
//
// It reports whether a window now runs, which is false only when Shutdown
// latched h.shuttingDown. The caller must then PUBLISH rather than return: a
// refusal that swallowed the settle would lose it for good, because the client
// still holds WORKING and no later refresh can find the edge again. The
// counterpart of the latch is activityRefreshes: every armed window holds one
// count, which Shutdown joins after it cancels.
func (h *OutputHandler) holdSettleLocked(st *agentActivity, agentID, rootAgentID string) bool {
	if st.settlePending {
		return true
	}
	if h.shuttingDown.Load() {
		return false
	}
	st.settleGen++
	gen := st.settleGen
	st.settlePending = true
	h.activityRefreshes.Add(1)
	st.settleTimer = h.afterSettleDelay(func() {
		h.deliverHeldSettle(agentID, rootAgentID, gen)
	})
	return true
}

// cancelSettleLocked stops a held settle's timer. Caller must hold st.mu.
//
// A Stop that reports false means the timer already fired and its callback runs
// now. This call cannot recall it, so it retires the callback instead: moving
// settleGen makes deliverHeldSettle return without publishing. That token is
// what the comment
// here used to
// claim the
// re-derivation
// gave for free,
// and the
// re-derivation
// does not give
// it. Against a
// retired entry or
// a dead process,
// the callback
// derives a state
// that DIFFERS
// from `published`
// and publishes
// it.
//
// It leaves settlePending alone. Stopping the timer says nothing about whether a
// client learned the work stopped; only a publish and voidHeldSettleLocked do.
func (st *agentActivity) cancelSettleLocked(h *OutputHandler) {
	if st.settleTimer == nil {
		return
	}
	st.settleGen++
	if st.settleTimer.Stop() {
		// The callback will never run, so its count is this caller's to release.
		// A Stop that reports false leaves the callback to release its own.
		h.activityRefreshes.Done()
	}
	st.settleTimer = nil
}

// cancelSettle is cancelSettleLocked with the entry's lock. Caller must NOT hold
// st.mu.
func (st *agentActivity) cancelSettle(h *OutputHandler) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.cancelSettleLocked(h)
}

// voidHeldSettleLocked drops a held settle because the work came back, and drops
// the count that settle was carrying with it. Caller must hold st.mu.
//
// The count described the turn that stopped, and that stop did not last. The
// settle that eventually comes belongs to the work running NOW, so it must alert
// unconditionally -- the same rule setTurnActive applies to a clear that settles
// nothing. Keeping a zero here would silence the settle that follows, which is
// the failure that rule exists to prevent.
//
// It reads settlePending, not settleTimer. The handle is already nil on both
// paths that reach here through settleImmediate, so testing it made this
// function a no-op exactly when a resume raced the window's own delivery.
func (st *agentActivity) voidHeldSettleLocked(h *OutputHandler) {
	if !st.settlePending {
		return
	}
	st.cancelSettleLocked(h)
	st.settlePending = false
	st.settledToolUses = nil
}

// deliverHeldSettle publishes the settle whose window just closed. `gen`
// identifies the window, and a later cancel retires this call by moving it.
//
// It re-derives rather than replaying the value the window captured, so a state
// that moved inside the window reaches the client as what it IS. An agent that
// went back to working publishes nothing at all, which is the case the window
// exists for.
//
// It resolves the entry with Load and not activityFor. Minting here would
// re-create the entry ForgetActivity just deleted, and publish for an agent that
// no longer has a tab.
func (h *OutputHandler) deliverHeldSettle(agentID, rootAgentID string, gen uint64) {
	defer h.activityRefreshes.Done()
	v, ok := h.activity.Load(agentID)
	if !ok {
		return
	}
	st := v.(*agentActivity)
	st.mu.Lock()
	if st.settleGen != gen {
		// A cancel retired this window. Whatever it did with the settle stands,
		// and the handle now standing belongs to a LATER window that must keep it.
		st.mu.Unlock()
		return
	}
	// Release the slot, so the ticket guard below can drop this call without
	// leaving a handle that blocks every later settle from opening a window.
	st.settleTimer = nil
	st.mu.Unlock()
	seq := h.activitySeq.Add(1)
	h.refreshActivityIn(st, agentID, rootAgentID, h.backgroundTaskRows(rootAgentID), seq, settleImmediate)
}

// NoteAgentInterrupted records that the USER stopped this agent, so the stop it
// produces reaches the client at once rather than waiting out settleDelay.
//
// The window exists for a stop the CLI takes back microseconds later. An
// interrupt is the opposite: InterruptAgent pauses the input queue before it
// signals, so no wake follows. Holding it left the thinking indicator and the
// Interrupt button on screen for three seconds after the user cancelled, which
// invites a second interrupt.
func (h *OutputHandler) NoteAgentInterrupted(agentID, rootAgentID string) {
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	st.interrupted = true
	st.mu.Unlock()
}

// CancelHeldSettles drops every settle window still open. Shutdown calls it
// BEFORE it joins the deferred refreshes, so no timer reads the registry or
// broadcasts once the caller closes the database.
//
// The order matters in both directions. An armed window holds an
// activityRefreshes count, so joining
// first would park Shutdown for a whole
// settleDelay. And holdSettleLocked
// refuses to arm once the latch is set,
// so nothing can open a window after this
// call.
//
// The process exits already cancel each window they reach, because they publish
// through settleImmediate. This call is the redundant guard: it stays correct
// whatever order the exits ran in. It costs one pass over a map that holds one
// entry per open tab.
func (h *OutputHandler) CancelHeldSettles() {
	h.activity.Range(func(_, v any) bool {
		v.(*agentActivity).cancelSettle(h)
		return true
	})
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

// settleCanResumeLocked answers whether a wake can undo the stop this refresh
// derived, which is the only case the debounce window is worth paying for.
// Caller must hold st.mu.
//
// The window exists for a stop the CLI takes back microseconds later. A
// backgrounded shell completes and wakes the agent into a new turn, or a
// subagent's shell completes and the CLI restarts that subagent. An agent
// blocked on a permission prompt is a different stop. Nothing can resume it
// until the user answers, so holding it spins the thinking indicator at somebody
// who is being asked a question, and delays the alert that asks them.
//
// Worse than late. If the answer arrives inside the window, the refresh it
// drives derives WORKING again and VOIDS the settle. The prompt's state then
// never reaches the client at all, and no alert ever rings for it.
//
// A dead process is the same class, reached the other way: nothing can resume a
// stop whose process is gone. So is a stop the USER asked for -- see
// NoteAgentInterrupted. The rule lives here rather than at each call site, so no
// caller can hold a settle that nothing can ever undo.
//
// Only a ROOT can be blocked, because activityStateLocked answers a child from
// its registry row and never reads pendingControl.
func settleCanResumeLocked(st *agentActivity, in activityInputs) bool {
	if !in.processAlive || st.interrupted {
		return false
	}
	return in.childID != "" || len(st.pendingControl) == 0
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
		h.indexTreeChild(agentID, rootAgentID)
	}
	return st
}

// indexTreeChild records a child under its root, so treeChildIDs can ask for one
// tree instead of walking every agent this worker ever published for.
//
// A derived copy, not a second source of truth: the entry's own rootAgentID is
// the authority, this is written from the same call that writes it, and
// ForgetActivity deletes from both. The data flows one way, and rebuilding this
// from the map is a walk of the map.
func (h *OutputHandler) indexTreeChild(agentID, rootAgentID string) {
	if agentID == rootAgentID {
		return
	}
	v, _ := h.treeChildren.LoadOrStore(rootAgentID, &sync.Map{})
	v.(*sync.Map).Store(agentID, struct{}{})
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
	v, ok := h.activity.Load(agentID)
	if !ok {
		return
	}
	st := v.(*agentActivity)

	// DELIVER a held settle, do not drop it. A subagent's last registry row
	// closes and the provider retires the child on the next line (see
	// CleanupChildAgent), which lands inside the window that close opened.
	// Cancelling there loses the settle for good. The entry goes away, so no later
	// refresh can find the edge again, and the child's tab keeps a spinner and an
	// armed Interrupt button on a run that ended.
	st.mu.Lock()
	deliver := st.settlePending
	// Read the root from the entry rather than through resolveRoot, which takes
	// this same lock. An entry no sink ever touched records none, and an agent is
	// its own root.
	rootAgentID := st.rootAgentID
	st.cancelSettleLocked(h)
	st.mu.Unlock()
	if rootAgentID == "" {
		rootAgentID = agentID
	}
	// Before the delete, so the refresh reaches THIS entry rather than minting a
	// replacement that no cleanup would ever reap.
	if deliver {
		seq := h.activitySeq.Add(1)
		h.refreshActivityIn(st, agentID, rootAgentID, h.backgroundTaskRows(rootAgentID), seq, settleImmediate)
	}
	h.activity.Delete(agentID)
	if rootAgentID != agentID {
		if v, ok := h.treeChildren.Load(rootAgentID); ok {
			v.(*sync.Map).Delete(agentID)
		}
	}
	// A root takes its whole index with it, so a tree that closes leaves nothing.
	h.treeChildren.Delete(agentID)
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

// InterruptsWork reports whether closing this tab would stop something. It
// differs from Working precisely for a blocked agent, whose turn a close still
// kills.
//
// Both close guards resolve to this RULE, but they no longer read the same
// value, and the debounce is why. The CLI guard asks the Worker and gets the
// exact derivation; the browser guard reads its cached copy of the published
// state, which lags by up to settleDelay. So for the length of one window the
// CLI lets a close through and the browser still warns. See settleDelay.
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

// AgentActivityPublished is the last state this Worker BROADCAST for agentID,
// which is what a client that caches the pushed state already holds.
//
// The catch-up baseline reads this rather than AgentActivitySnapshot, and the
// difference is a lost alert. While a settle waits out its window the snapshot
// answers IDLE and the broadcast still says WORKING. A tab that promotes to FULL
// inside the window is then seeded with the exact IDLE. The window's own
// AgentActivityChanged arrives as a no-op, the client sees no WORKING ->
// not-WORKING edge, and neither the completion sound nor the tab badge comes. A
// baseline drawn from the value the live events carry cannot lose that edge.
// Every other reader wants the exact answer -- see AgentActivitySnapshot.
//
// It falls back to the exact derivation when this Worker published nothing yet.
// That is safe rather than a hole: a window opens only where `published` is
// WORKING, and `published` is written only together with hasPublished, so no
// window can be open when this returns the fallback.
func (h *OutputHandler) AgentActivityPublished(agentID, rootAgentID string) leapmuxv1.AgentActivityState {
	// Load, not activityFor: a pure read must not mint an entry.
	if v, ok := h.activity.Load(agentID); ok {
		st := v.(*agentActivity)
		st.mu.Lock()
		published, has := st.published, st.hasPublished
		st.mu.Unlock()
		if has {
			return published
		}
	}
	return h.AgentActivitySnapshot(agentID, rootAgentID).State
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
	h.refreshActivityFrom(agentID, rootAgentID, h.backgroundTaskRows(rootAgentID), seq, settleHeld)
}

// refreshActivityFrom is refreshActivity against a root the caller already
// resolved, registry rows it already holds, and the ticket it took before
// reading them.
//
// The entry's own inputs are read INSIDE the same critical section that records
// the answer. Reading them separately let two refreshes derive, then publish in
// the reverse order, so a stale value latched.
//
// A settle it derives is HELD rather than published, unless the caller asks for
// settleImmediate. See settleDelay for what the window is worth and what it
// costs.
//
// One residual, stated because it is not free to close: the broadcast runs after
// the unlock, because BroadcastAgentEvent can block on a slow transport and
// holding a lock across it would serialize every registry op for the root behind
// the slowest watcher. So two publishes that BOTH survive the ticket can still
// reach the wire out of order. The ticket removes the case that mattered -- a
// stale reader overwriting a fresher answer, which the edge trigger then made
// permanent -- and leaves only a reordering of two genuine transitions.
func (h *OutputHandler) refreshActivityFrom(agentID, rootAgentID string, rows []bgtask.Item, seq uint64, mode settleMode) {
	h.refreshActivityIn(h.activityFor(agentID, rootAgentID), agentID, rootAgentID, rows, seq, mode)
}

// refreshActivityIn is refreshActivityFrom against an entry the caller already
// resolved.
//
// A held settle's delivery needs this: it holds the entry its window was opened
// on, and resolving again would MINT a replacement for an agent ForgetActivity
// retired in the meantime. See deliverHeldSettle.
func (h *OutputHandler) refreshActivityIn(
	st *agentActivity, agentID, rootAgentID string, rows []bgtask.Item, seq uint64, mode settleMode,
) {
	in := h.externalActivityInputs(agentID, rootAgentID, rows)

	st.mu.Lock()
	if st.hasPublished && seq < st.publishedSeq {
		// This refresh read its rows before the one that already published. Its
		// answer is older, so it must not overwrite a fresher one.
		st.mu.Unlock()
		return
	}
	if mode == settleImmediate {
		// A process boundary supersedes any window in flight: no wake can follow a
		// dead process. AFTER the ticket guard, never before -- an older refresh
		// that cancels and then drops itself takes the settle with it, and the tab
		// keeps a spinner for an agent whose process is gone.
		st.cancelSettleLocked(h)
	}
	state := activityStateLocked(st, in)
	if st.hasPublished && st.published == state {
		st.publishedSeq = seq
		// The agent is doing what the client already believes it is doing. A held
		// settle described a stop that did not last, so it is void -- this is the
		// cancel that a wake, a subagent restart and a drained input queue all
		// reach. The ticket guard above is what makes it safe: a stale reader
		// cannot cancel a window a fresher one opened.
		st.voidHeldSettleLocked(h)
		st.mu.Unlock()
		return
	}
	var toolUses *int32
	working := leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
	settling := state != working && st.published == working
	if settling && mode == settleHeld && settleCanResumeLocked(st, in) {
		// A settle, and this client still does not know the work stopped. Hold
		// it: the CLI resumes the same work microseconds later often enough that
		// publishing here rings the completion sound at the moment the agent
		// starts again. `published` stays WORKING, so the window ending re-derives
		// against the state the client actually holds.
		if h.holdSettleLocked(st, agentID, rootAgentID) {
			st.publishedSeq = seq
			st.mu.Unlock()
			return
		}
		// Shutdown refused the window. Fall through and publish: the settle has
		// nowhere else to come from once this process stops.
	}
	if settling {
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
	// A subagent whose run ended, and whose end the client now knows, has nothing
	// left to remember. Read the verdict here, under the lock that owns those
	// fields; the retire itself runs after the broadcast below.
	retire := in.childID != "" && state != working && in.activeTasks == 0
	// The client learns the state below, so nothing is pending any more --
	// whether this publish delivers a held settle or supersedes one.
	//
	// Cancel the window with it. A fall-through publish reaches here with a
	// window still armed: a prompt, a dead process, the shutdown latch. A handle
	// left behind is one the NEXT settle adopts without opening a window of its
	// own. That settle then lands on somebody else's deadline, and setTurnActive
	// drops the count it should have carried.
	st.cancelSettleLocked(h)
	st.settlePending = false
	// The stop the user asked for reached them, so the mark has done its work.
	// An interrupt the agent IGNORED is cleared here too, by the WORKING publish
	// that says so -- otherwise it would exempt a later, resumable stop.
	st.interrupted = false
	retire = retire && st.retirableLocked()
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

	// Drop the finished child here rather than waiting for a provider to retire
	// it. Only Codex, ZCode and the ACP providers call CleanupChildAgent, so
	// under Claude and Pi every subagent a session ever spawned kept an entry
	// until the tab closed. Each of those then cost a point query per registry
	// mutation once the display cap evicted its row.
	//
	// After the broadcast, so the settle it just published is not lost. A child
	// that is still RUNNING publishes WORKING and keeps its entry, which is what
	// the display cap needs. See retirableLocked for what else keeps one.
	if retire {
		h.ForgetActivity(agentID)
	}
}

// retirableLocked answers whether this entry holds nothing worth keeping.
// Caller must hold st.mu.
//
// `published` alone is not worth keeping: a child that comes back mints a fresh
// entry and publishes WORKING, which is a real transition. Everything else is,
// and each field here is a way to lose something real. An unspent count belongs
// to a turn end whose settle has not landed. A held settle has not reached the
// client. A prompt is unanswered. An interrupt mark is unspent. And a turn flag
// means a COLLAB
// child, whose
// own input queue
// follows that
// flag. Its token
// also orders two
// publishes that
// can arrive out
// of order, so
// re-minting
// would reset
// that token and
// let a stale
// publish latch a
// turn that is
// over.
func (st *agentActivity) retirableLocked() bool {
	return !st.turnActive && st.turnSeq == 0 && len(st.pendingControl) == 0 &&
		st.settledToolUses == nil && !st.settlePending && st.settleTimer == nil && !st.interrupted
}

// refreshActivityTree recomputes the root and every child that owns a registry
// row under it. A process exit and a registry change both move more than one
// tab's answer at once, and a child that is never recomputed keeps a spinner
// that nothing will ever clear.
func (h *OutputHandler) refreshActivityTree(rootAgentID string, mode settleMode) {
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
	h.refreshActivityFrom(rootAgentID, rootAgentID, rows, seq, mode)
	for _, childID := range h.treeChildIDs(rootAgentID, rows) {
		h.refreshActivityFrom(childID, rootAgentID, rows, seq, mode)
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
		// An ACTIVE row only. A child whose run ended needs recomputing exactly
		// while it still holds an entry, and the index below supplies every one
		// of those. Adding it from a finished row as well would re-create the entry
		// the reap just dropped, on every mutation for the life of the
		// root. That is the accumulation this pair exists to stop.
		if !rows[i].Status.IsFinished() {
			add(rows[i].ChildAgentID)
		}
	}
	if v, ok := h.treeChildren.Load(rootAgentID); ok {
		v.(*sync.Map).Range(func(key, _ any) bool {
			if childID, ok := key.(string); ok {
				add(childID)
			}
			return true
		})
	}
	return childIDs
}

// setTurnActive records the provider's turn bookkeeping and republishes.
// Providers reach it through OutputSink.SetTurnActive.
func (h *OutputHandler) setTurnActive(agentID, rootAgentID string, active bool) {
	// The settle count belongs to the ROOT's turn. A child publishes this flag
	// too, because the input queue follows it, but activityStateLocked answers
	// a child from its registry row and never reads the flag -- so the refresh
	// below cannot spend a child's count, and clearing it here would only
	// destroy what PersistTurnEnd just recorded. The child's settle then
	// reports no tool count at all.
	root := agentID == rootAgentID
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	changed := st.turnActive != active
	st.turnActive = active
	if active && root {
		// A fresh turn supersedes whatever the previous one left unspent, so a
		// stale count cannot silence the alert for the turn now starting.
		st.settledToolUses = nil
		// And it is not the turn the user interrupted, so its settle is an
		// ordinary one that waits out its window.
		st.interrupted = false
	}
	st.mu.Unlock()
	if !changed {
		return
	}
	h.refreshActivity(agentID, rootAgentID)
	if active || !root {
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
	//
	// A HELD settle is exempt, and reading settlePending is what tells the two
	// apart. That settle IS the one this clear produced, and it still waits in
	// its window. Its count is therefore still unspent. Clearing here would make
	// every ordinary turn end ring, zero-tool turns included.
	st.mu.Lock()
	if !st.settlePending {
		st.settledToolUses = nil
	}
	st.mu.Unlock()
}

// acceptTurnPublish answers whether one publish of the turn flag is current,
// and records it when it is.
//
// Two publishes can arrive out of order, and the wrong one winning latches a
// turn that is over: nothing later clears it, so the input queue holds every
// message the user sends after it. Two things can reorder them.
//
// Inside ONE provider process, the reader goroutine that ends a turn and the
// drain goroutine that a busy refusal answers both reach here. Their tokens come
// from one counter under the provider's own lock, so the later token is the
// later read of the flag, and a lower one is stale.
//
// ACROSS processes, a replaced process can still publish: its reader is draining
// the pipe of an agent the Worker already stopped. Its tokens mean nothing to
// the new process, whose counter restarted at zero. publisher identifies the
// SINK the publish came through -- one sink per launch -- so a publish from a
// superseded sink is dropped whatever its token, and the first publish of a new
// one is accepted whatever its token.
func (h *OutputHandler) acceptTurnPublish(agentID, rootAgentID string, publisher any, seq uint64) bool {
	current, ok := h.turnPublisher.Load(rootAgentID)
	if ok && current != publisher {
		return false
	}
	st := h.activityFor(agentID, rootAgentID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !ok {
		// No launch adopted a publisher yet, which is the state a bare unit test
		// and the window before the first NoteAgentProcessStarted both leave.
		// Accept, because dropping here would lose the only publisher there is.
		st.turnSeq = seq
		return true
	}
	if seq <= st.turnSeq {
		return false
	}
	st.turnSeq = seq
	return true
}

// adoptTurnPublisher makes one sink the only publisher of an agent's turn flag,
// and forgets the token of the process that came before it. A launch calls it:
// the sink it registers is the one the new process publishes through, and every
// later publish from the old process is answering for a turn that died with it.
func (h *OutputHandler) adoptTurnPublisher(rootAgentID string, publisher any) {
	h.turnPublisher.Store(rootAgentID, publisher)
	st := h.activityFor(rootAgentID, rootAgentID)
	st.mu.Lock()
	st.turnSeq = 0
	st.mu.Unlock()
}

// TurnActive reports the flag the agent's provider last published. It is the
// SAME value the provider's own SendInput refuses input from, and the same one
// the input queue's dispatch guard follows -- so a caller that must not run into
// a turn asks here rather than deriving an answer of its own.
//
// It answers for a ROOT. A child owns no turn of its own: its run IS its
// registry row.
func (h *OutputHandler) TurnActive(agentID string) bool {
	st := h.activityFor(agentID, agentID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.turnActive
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
	// The sink registered now belongs to the process starting now, and it is the
	// only one whose turn flag counts from here on. See acceptTurnPublish.
	if sink, ok := h.rootSinks.Load(rootAgentID); ok {
		h.adoptTurnPublisher(rootAgentID, sink)
	}
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
	// The interrupt mark belongs to the process that carried it, so it dies with
	// that process rather than exempting the next one's first stop.
	st.interrupted = false
	st.mu.Unlock()
	h.activityRefreshes.Add(1)
	go func() {
		defer h.activityRefreshes.Done()
		h.refreshActivityTree(rootAgentID, settleImmediate)
	}()
}

// WaitActivityRefreshes joins the deferred tree refreshes resetAgentActivity
// spawned. Shutdown calls it after the processes stop and before it cancels the
// background-task context, so no refresh reads the registry or broadcasts after
// the caller closes the database.
func (h *OutputHandler) WaitActivityRefreshes() {
	h.activityRefreshes.Wait()
}
