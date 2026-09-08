package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// activityRecorder captures the AgentActivityChanged events one channel
// receives, decoded. The COUNT is as much the assertion as the values: the
// state is edge-triggered, and a level-emitted version would flood a
// notification-class broadcast on every output line and ring the doorbell over
// and over.
type activityRecorder struct {
	mockResponseWriter

	mu     sync.Mutex
	events []*leapmuxv1.AgentActivityChanged
	agents []string
}

func newActivityRecorder(channelID string) *activityRecorder {
	return &activityRecorder{mockResponseWriter: mockResponseWriter{channelID: channelID}}
}

func (r *activityRecorder) SendStream(msg *leapmuxv1.InnerStreamMessage) error {
	var resp leapmuxv1.WatchEventsResponse
	if err := proto.Unmarshal(msg.GetPayload(), &resp); err == nil {
		if ev := resp.GetAgentEvent(); ev != nil {
			if act := ev.GetActivityChanged(); act != nil {
				r.mu.Lock()
				r.events = append(r.events, act)
				r.agents = append(r.agents, ev.GetAgentId())
				r.mu.Unlock()
			}
		}
	}
	return r.mockResponseWriter.SendStream(msg)
}

func (r *activityRecorder) busyStates() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]bool, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.GetState() == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING)
	}
	return out
}

func (r *activityRecorder) last() *leapmuxv1.AgentActivityChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return nil
	}
	return r.events[len(r.events)-1]
}

func (r *activityRecorder) agentIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.agents...)
}

// settleWindows drives the settle debounce in a test.
//
// The handler holds a WORKING -> not-WORKING publish for settleDelay, so a case
// that asserts a settle has to say WHEN that window closes. Letting the real
// delay run would make the machine that runs the suite decide the outcome, which
// is the reason the handler takes its timer through a seam at all.
//
// A window cannot deliver while holdSettleLocked arms it, because
// holdSettleLocked arms under the entry's mutex and the delivery takes that
// mutex again.
type settleWindows struct {
	t    *testing.T
	mu   sync.Mutex
	open []*heldWindow
}

type heldWindow struct {
	windows *settleWindows
	deliver func()
	// closed covers both ends: delivered, and stopped before it could deliver.
	closed bool
}

func (w *settleWindows) timer(d time.Duration, deliver func()) settleStopper {
	// The fake ignores the delay otherwise, so nothing else would catch a seam
	// invoked with the wrong one. assert and not require: a window can be armed
	// off the test goroutine, where FailNow is undefined.
	assert.Equal(w.t, settleDelay, d, "every window is sized by settleDelay")
	w.mu.Lock()
	defer w.mu.Unlock()
	held := &heldWindow{windows: w, deliver: deliver}
	w.open = append(w.open, held)
	return held
}

// Stop reports whether this window was still open, matching *time.Timer.Stop.
func (h *heldWindow) Stop() bool {
	h.windows.mu.Lock()
	defer h.windows.mu.Unlock()
	if h.closed {
		return false
	}
	h.closed = true
	return true
}

// expire fires every window open right now, but hands the deliveries back
// instead of running them.
//
// time.AfterFunc runs a callback on its own goroutine, so a window FIRES and
// lands later, and a cancel and a re-arm both fit in that gap. A case that needs
// the gap drives the two halves itself; close runs them back to back.
func (w *settleWindows) expire() []func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	deliveries := make([]func(), 0, len(w.open))
	for _, held := range w.open {
		if !held.closed {
			held.closed = true
			deliveries = append(deliveries, held.deliver)
		}
	}
	w.open = nil
	return deliveries
}

// close ends every window open right now, exactly as the delay expiring would.
// It reports how many delivered, so a case can prove a window was open at all
// rather than asserting against a settle that never waited.
func (w *settleWindows) close() int {
	// Outside the lock: each delivery re-derives, which can arm the NEXT window.
	deliveries := w.expire()
	for _, deliver := range deliveries {
		deliver()
	}
	return len(deliveries)
}

// openWindows lists the windows still waiting, so a case can assert that a
// cancel left none rather than that a delivery did not happen.
func (w *settleWindows) openWindows() []*heldWindow {
	w.mu.Lock()
	defer w.mu.Unlock()
	still := make([]*heldWindow, 0, len(w.open))
	for _, held := range w.open {
		if !held.closed {
			still = append(still, held)
		}
	}
	return still
}

// settleFakes remembers the fake already installed on a handler, so holdSettles
// is idempotent. Package-level because the handler cannot carry a test type.
var settleFakes sync.Map // *OutputHandler -> *settleWindows

// holdSettles puts a controllable settle window on h and returns it.
//
// Every handler a test builds gets one from its own constructor, so no case ever
// arms a real settleDelay timer that outlives it. A case that drives the window
// calls this again for the handle, and gets the SAME fake back.
//
// Idempotent, and that is load-bearing rather than tidy. Installing a second
// fake would leave any window already open on the first one stranded: nothing
// can ever fire it, so a settle disappears and close reports zero while a
// spinner is genuinely stuck. The old form was safe only while every case
// happened to call this before any output flowed.
func holdSettles(t *testing.T, h *OutputHandler) *settleWindows {
	t.Helper()
	if v, ok := settleFakes.Load(h); ok {
		return v.(*settleWindows)
	}
	w := &settleWindows{t: t}
	settleFakes.Store(h, w)
	t.Cleanup(func() { settleFakes.Delete(h) })
	h.newSettleTimer = w.timer
	return w
}

// newActivityHandler wires a handler whose process-running check always answers
// true, so a test can drive the other inputs in isolation. `agents` is nil here,
// which the derivation reads as "no process": the fake below replaces that.
func newActivityHandler(t *testing.T, agentID string) (*OutputHandler, *activityRecorder) {
	t.Helper()
	m := NewWatcherManager()
	rec := newActivityRecorder("ch-1")
	m.agents.setWatches("ch-1", []watchEntry{{id: agentID, mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, rec)
	h := &OutputHandler{watcher: m, processRunning: func(string) bool { return true }}
	holdSettles(t, h)
	return h, rec
}

func TestActivity_TurnOpensAndClosesTheBusyState(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)

	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	// Opening the turn is published at once; the settle waits out its window,
	// because work that resumes inside it never reaches a client as a stop.
	assert.Equal(t, []bool{true}, rec.busyStates())
	require.Equal(t, 1, settles.close(), "the clear opened a settle window")

	assert.Equal(t, []bool{true, false}, rec.busyStates())
	assert.Equal(t, []string{"agent-1", "agent-1"}, rec.agentIDs())
}

func TestActivity_EdgeTriggeredSoARepeatBroadcastsNothing(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")

	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", true)
	h.refreshActivity("agent-1", "agent-1")
	h.refreshActivity("agent-1", "agent-1")

	// A notification-class broadcast pays a snapshot, a marshal and one
	// SendStream per subscribed channel whether or not a tab is on screen. A
	// level-emitted version would do that per output line.
	assert.Equal(t, []bool{true}, rec.busyStates(), "only the transition is published")
}

func TestActivity_FirstIdleIsPublishedEvenThoughFalseIsTheZeroValue(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")

	// Without the "has published" mark, an agent's first genuine settle looks
	// like a no-op against the zero value and never reaches a client.
	h.refreshActivity("agent-1", "agent-1")

	assert.Equal(t, []bool{false}, rec.busyStates())
}

func TestActivity_PendingControlRequestMakesAnAgentIdleMidTurn(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	// The agent is blocked on the user, and the user is looking straight at the
	// prompt. Reporting busy there would spin an indicator at somebody who is
	// being asked a question.
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	require.Equal(t, 0, settles.close(), "a prompt publishes at once, so there is no window")
	assert.Equal(t, []bool{true, false}, rec.busyStates())

	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-1")
	assert.Equal(t, []bool{true, false, true}, rec.busyStates(),
		"answering the prompt hands the turn back")
}

func TestActivity_APromptMidTurnIsWaitingRatherThanIdle(t *testing.T) {
	t.Parallel()

	// The indicator and the close guard need OPPOSITE answers here, which one
	// boolean could not give: the spinner must stop, because the user is looking
	// straight at the prompt, but the turn is still in flight and closing the tab
	// kills it along with every subagent under it.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")

	got := h.AgentActivitySnapshot("agent-1", "agent-1")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, got.State)
	assert.False(t, got.Working(), "the indicator must not spin at somebody being asked a question")
	assert.True(t, got.InterruptsWork(), "but a close would still kill the turn")

	// No window: only the user can answer a prompt, so no wake can undo this
	// stop. See settleCanResumeLocked.
	assert.Equal(t, 0, settles.close(), "a prompt waits out no window")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		rec.last().GetState(), "and the state reaches the wire, so both surfaces read the same rule")
}

func TestActivity_APromptWithNoTurnBehindItIsPlainIdle(t *testing.T) {
	t.Parallel()

	// A shell task can ask for permission after its agent's turn ended. What a
	// close interrupts there is the task, which the count already reports -- so
	// claiming a turn is waiting would name work that does not exist.
	h, _ := newActivityHandler(t, "agent-1")
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")

	got := h.AgentActivitySnapshot("agent-1", "agent-1")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, got.State)
	assert.False(t, got.InterruptsWork())
}

func TestActivity_DuplicateControlRequestPublishesOnce(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)

	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-unknown")

	require.Equal(t, 0, settles.close(), "a prompt publishes at once, so there is no window")
	assert.Equal(t, []bool{true, false}, rec.busyStates())
}

func TestActivity_TwoPromptsNeedBothAnswersBeforeWorkResumes(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	h.noteControlRequestAdded("agent-1", "agent-1", "req-2")
	require.Equal(t, 0, settles.close(), "a prompt publishes at once, so there is no window")
	require.Equal(t, []bool{true, false}, rec.busyStates())

	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-1")
	assert.Equal(t, []bool{true, false}, rec.busyStates(), "still blocked on the second prompt")

	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-2")
	assert.Equal(t, []bool{true, false, true}, rec.busyStates())
}

func TestActivity_DeadProcessIsIdleWhateverElseIsRecorded(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	running := true
	h.processRunning = func(string) bool { return running }

	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	// A crash leaves the turn flag set: no envelope arrives to clear it. The
	// process check is what stops a lost agent from showing work forever.
	running = false
	h.refreshActivity("agent-1", "agent-1")

	require.Equal(t, 0, settles.close(), "a dead process resumes nothing, so its settle publishes at once")
	assert.Equal(t, []bool{true, false}, rec.busyStates())
}

func TestActivity_ProcessExitClearsEverythingAndSettles(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	require.Equal(t, 0, settles.close(), "a prompt publishes at once, so there is no window")
	require.Equal(t, []bool{true, false}, rec.busyStates())

	// HandleAgentProcessExit broadcasts no AgentStatusChange, so this is the only
	// signal a client gets that a crashed agent stopped working.
	h.NoteAgentProcessExited("agent-1")
	h.WaitActivityRefreshes()

	// The settle half, which the name promises and nothing else covers: a dead
	// process is IDLE, not still waiting on the prompt it died holding.
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, rec.last().GetState())
	assert.Equal(t, 0, settles.close(), "and a process boundary opens no window")

	// The reset lands synchronously even though its broadcast does not.
	st := h.activityFor("agent-1", "agent-1")
	st.mu.Lock()
	defer st.mu.Unlock()
	assert.False(t, st.turnActive)
	assert.Empty(t, st.pendingControl)
}

func TestActivity_RestartNeverResumesTheOldTurn(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	// The old process died mid-turn without an exit handler ever running -- a
	// worker that restarted, a stop that raced the relaunch. The flag is still
	// set, and no envelope is coming to clear it.
	h.NoteAgentProcessStarted("agent-1")

	// The publish is deferred off the caller's lifecycle lock; the state reset
	// itself is not, and is asserted synchronously below.
	assert.Eventually(t, func() bool {
		return len(rec.busyStates()) == 2
	}, time.Second, 5*time.Millisecond)
	assert.Equal(t, []bool{true, false}, rec.busyStates(),
		"a new process owns no turn, so it starts idle")

	st := h.activityFor("agent-1", "agent-1")
	st.mu.Lock()
	defer st.mu.Unlock()
	assert.False(t, st.turnActive, "nothing restores the old process's turn")
	assert.Empty(t, st.pendingControl, "the prompts the dead process was blocked on died with it")
	assert.Nil(t, st.settledToolUses)
}

func TestActivity_RestartWhileBlockedOnAPromptAlsoStartsIdle(t *testing.T) {
	t.Parallel()

	h, _ := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")

	h.NoteAgentProcessStarted("agent-1")

	// Nothing will ever answer a prompt whose process is gone, so a retained one
	// would pin the agent idle-because-blocked rather than genuinely idle -- and
	// the next real turn could not report busy through it.
	assert.False(t, h.AgentBusy("agent-1", "agent-1"))
	h.setTurnActive("agent-1", "agent-1", true)
	assert.True(t, h.AgentBusy("agent-1", "agent-1"), "the restarted agent can report a turn again")
}

func TestActivity_SettleCarriesTheToolCountOfTheTurnThatEnded(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)

	h.noteTurnEnded("agent-1", "agent-1", 3, true)
	h.setTurnActive("agent-1", "agent-1", false)
	require.Equal(t, 1, settles.close())

	last := rec.last()
	require.NotNil(t, last)
	assert.NotEqual(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, last.GetState())
	require.NotNil(t, last.NumToolUses, "the settle spends the count the turn end recorded")
	assert.Equal(t, int32(3), last.GetNumToolUses())
}

func TestActivity_ZeroToolTurnStaysDistinguishableFromNoCount(t *testing.T) {
	t.Parallel()

	// The client suppresses the alert for a zero-tool turn, so 0 and unset must
	// not collapse: unset means "ring", 0 means "this turn did nothing worth
	// interrupting the user for".
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 0, true)
	h.setTurnActive("agent-1", "agent-1", false)
	require.Equal(t, 1, settles.close())

	last := rec.last()
	require.NotNil(t, last.NumToolUses)
	assert.Equal(t, int32(0), last.GetNumToolUses())
}

func TestActivity_ProviderThatReportsNoCountLeavesItUnset(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 0, false)
	h.setTurnActive("agent-1", "agent-1", false)
	// The settle waits out its window, so the count has to be read from the
	// event the window DELIVERS. Read rec.last() before this and it answers with
	// the busy publish, whose count is nil whatever the settle path decides.
	require.Equal(t, 1, settles.close(), "the clear opened the settle window")

	require.Equal(t, []bool{true, false}, rec.busyStates(), "the settle reached the wire")
	assert.Nil(t, rec.last().NumToolUses, "unset, so the client rings rather than guessing 0")
}

func TestActivity_SettleWithNoTurnEndCarriesNoCount(t *testing.T) {
	t.Parallel()

	// A control request arriving mid-turn, and a process exit, both settle the
	// agent without a turn ending. Neither should inherit an earlier turn's
	// count, which would silence an alert the user needs.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)

	// An earlier turn ends and spends its count, so there IS one to inherit.
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 5, true)
	h.setTurnActive("agent-1", "agent-1", false)
	require.Equal(t, 1, settles.close())
	require.Equal(t, int32(5), rec.last().GetNumToolUses())

	// The next turn is blocked on a prompt. That settle carries no count, and it
	// publishes at once, because only the user can answer.
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")

	require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		rec.last().GetState())
	assert.Nil(t, rec.last().NumToolUses)
}

func TestActivity_NewTurnDropsAnUnspentCount(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.noteTurnEnded("agent-1", "agent-1", 9, true)

	// A fresh turn supersedes whatever the previous one left unspent, so a stale
	// count cannot silence the alert for the turn now starting.
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)
	require.Equal(t, 1, settles.close(), "the clear opened the settle window")

	require.Equal(t, []bool{true, false}, rec.busyStates())
	assert.Nil(t, rec.last().NumToolUses)
}

// --- The settle debounce window. See settleDelay. ---

func TestActivity_WorkThatResumesInsideTheWindowNeverSettles(t *testing.T) {
	t.Parallel()

	// The window's whole reason. A backgrounded shell completes and the CLI wakes
	// the agent into a new turn; publishing the gap between the two rings the
	// completion sound at the moment the agent starts again.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	h.setTurnActive("agent-1", "agent-1", false)
	h.setTurnActive("agent-1", "agent-1", true)

	assert.Equal(t, 0, settles.close(), "the wake voided the window")
	assert.Equal(t, []bool{true}, rec.busyStates(),
		"the client was never told the work stopped, so there is nothing to correct")
}

func TestActivity_ASubagentRestartDoesNotSettleTheRoot(t *testing.T) {
	t.Parallel()

	// The same flicker through the registry. A subagent's own shell completes,
	// the CLI restarts that subagent, and the row ends and reopens in one output
	// burst. The root has no turn of its own here, so the row IS its work.
	const rootID = "root-1"
	svc, sink, _ := setupRunningSubagent(t, rootID, bgtask.StatusRunning)
	rec := watchActivity(t, svc, rootID)
	settles := holdSettles(t, svc.Output)
	require.True(t, svc.Output.AgentActivitySnapshot(rootID, rootID).Working())

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.NoError(t, sink.ReviveBackgroundTask("task-1"))

	assert.Equal(t, 0, settles.close(), "the restart voided the window")
	assert.Empty(t, rec.busyStates(), "the root published nothing at all")
	assert.True(t, svc.Output.AgentActivitySnapshot(rootID, rootID).Working())
}

func TestActivity_AFallThroughPublishSupersedesTheWindow(t *testing.T) {
	t.Parallel()

	// A stop that cannot be resumed publishes at once even while a window runs,
	// and it must take that window with it. A handle left behind is one the NEXT
	// settle adopts. holdSettleLocked finds
	// a window already open, so that settle
	// never opens one of its own, never owns
	// its count, and lands on the leftover
	// of somebody else's deadline.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)
	require.Len(t, settles.openWindows(), 1, "the clear opened the window this case supersedes")

	// A shell task asks for permission after the turn ended, so the agent is
	// plain idle and only the user can move it. That publishes at once.
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")

	assert.Empty(t, settles.openWindows(), "the publish superseded the window")
	require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, rec.last().GetState(),
		"a prompt with no turn behind it is plain idle, and that is what lands")

	// The next turn's settle must own its own window and its own count.
	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 7, true)
	h.setTurnActive("agent-1", "agent-1", false)

	require.Equal(t, 1, settles.close(), "and opens a window of its own")
	require.NotNil(t, rec.last().NumToolUses, "carrying the count its turn recorded")
	assert.Equal(t, int32(7), rec.last().GetNumToolUses())
}

func TestActivity_ASecondDerivationDoesNotRestartTheWindow(t *testing.T) {
	t.Parallel()

	// The window opens at the edge that stopped the work. A refresh that derives
	// the same stop again is not a second edge, and re-arming there would let a
	// busy registry push a real settle out for as long as it kept mutating.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)
	h.refreshActivity("agent-1", "agent-1")
	h.refreshActivity("agent-1", "agent-1")

	require.Equal(t, 1, settles.close(), "three derivations of one stop, one window")
	assert.Equal(t, []bool{true, false}, rec.busyStates())
}

func TestActivity_AVoidedWindowDropsTheCountItCarried(t *testing.T) {
	t.Parallel()

	// A zero-tool turn ends, work resumes inside the window, and the settle that
	// eventually comes belongs to that work. Keeping the zero would silence it --
	// the same failure AClearThatDoesNotSettleDropsTheCount prevents, reached
	// through the window instead of through the clear.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 0, true)
	h.setTurnActive("agent-1", "agent-1", false)
	require.NotNil(t, unspentToolCount(h, "agent-1"), "the held settle still owns the count")

	// The wake, then a stop that really lasts.
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, 0, settles.close())
	assert.Nil(t, unspentToolCount(h, "agent-1"), "the voided window dropped it")

	h.setTurnActive("agent-1", "agent-1", false)
	require.Equal(t, 1, settles.close())
	assert.Nil(t, rec.last().NumToolUses, "so this settle alerts unconditionally")
}

func TestActivity_AProcessBoundaryPublishesWithoutWaiting(t *testing.T) {
	t.Parallel()

	// A dead process resumes nothing, so there is nothing to wait for -- and a
	// window held there would leave a timer to fire after shutdown closed the
	// database it reads.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	h.NoteAgentProcessExited("agent-1")
	h.WaitActivityRefreshes()

	assert.Equal(t, []bool{true, false}, rec.busyStates(), "the exit lands with no window")
	assert.Equal(t, 0, settles.close(), "and opened none")
}

func TestActivity_ShutdownDropsAWindowStillOpen(t *testing.T) {
	t.Parallel()

	// Shutdown is the redundant guard behind the process exits. A timer that
	// survived shutdown would read the registry and broadcast after the caller
	// closed the database.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	h.CancelHeldSettles()

	assert.Equal(t, 0, settles.close(), "the window was already dropped")
	assert.Equal(t, []bool{true}, rec.busyStates(), "and nothing reached the wire")
}

func TestActivity_ForgettingAnAgentDeliversItsHeldSettle(t *testing.T) {
	t.Parallel()

	// Retiring an agent is the LAST chance a held settle has. A subagent's row
	// closes and the
	// provider retires the
	// child on the next
	// line, which lands
	// inside the window
	// that close opened.
	// Dropping it there
	// loses the settle for
	// good: the entry that
	// held the edge is
	// gone, and no later
	// refresh can find it. The child's tab kept a spinner on a run that ended.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)
	require.Equal(t, []bool{true}, rec.busyStates(), "the settle is still waiting")

	h.ForgetActivity("agent-1")

	assert.Equal(t, []bool{true, false}, rec.busyStates(), "the settle landed on the way out")
	assert.Equal(t, 0, settles.close(), "and its window is gone, not left to fire later")
	_, alive := h.activity.Load("agent-1")
	assert.False(t, alive, "the delivery must not leave the entry behind")
}

func TestActivity_AStaleRefreshDoesNotCancelAFresherWindow(t *testing.T) {
	t.Parallel()

	// The ticket guard covers the settle window too, and the ORDER inside
	// refreshActivityFrom is what makes it hold. A refresh that cancels before it
	// checks its ticket takes a fresher window down and then drops itself, so the
	// settle is lost and the tab keeps a spinner nothing will ever clear.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	// A process-boundary refresh, parked between taking its ticket and deriving.
	staleSeq := h.activitySeq.Add(1)

	// The turn ends meanwhile, on a fresher ticket, and opens the window.
	h.setTurnActive("agent-1", "agent-1", false)

	// Now the parked one lands, carrying inputs older than that.
	h.refreshActivityFrom("agent-1", "agent-1", nil, staleSeq, settleImmediate)

	require.Equal(t, 1, settles.close(), "the older refresh must leave the window alone")
	assert.Equal(t, []bool{true, false}, rec.busyStates(), "so the settle still lands")
}

func TestActivity_AChildSettleIsHeldAndDeliveredUnderItsOwnID(t *testing.T) {
	t.Parallel()

	// The window is per AGENT, and the delivery has to address the agent it was
	// opened for. A subagent tab shows its own spinner, so a settle delivered
	// under the root's id would leave the child spinning and drop the parent's
	// indicator instead.
	const rootID = "root-1"
	svc, sink, childID := setupRunningSubagent(t, rootID, bgtask.StatusRunning)
	rec := watchActivity(t, svc, rootID, childID)
	settles := holdSettles(t, svc.Output)
	// The root owes the user a reply, so only the CHILD settles when the row ends.
	svc.Output.setTurnActive(rootID, rootID, true)

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	assert.Empty(t, rec.agentIDs(), "the child's settle waits out its window")

	require.Equal(t, 1, settles.close(), "one window, for the child alone")

	assert.Equal(t, []string{childID}, rec.agentIDs())
	assert.Equal(t, []bool{false}, rec.busyStates())
	assert.True(t, svc.Output.AgentActivitySnapshot(rootID, rootID).Working(),
		"and the root's turn is untouched")
}

func TestActivity_TreeChildIDsIncludeAChildTheDisplayCapDropped(t *testing.T) {
	t.Parallel()

	// The cap gives up the oldest ACTIVE row when the pool is full, which is
	// exactly why the snapshot answers a missing child from the table instead.
	// So a child can be published BUSY and then vanish from the display list.
	// Enumerating only the listed rows leaves that child spinning forever, on a
	// run that already ended.
	h, _ := newActivityHandler(t, "root-1")
	h.setTurnActive("child-1", "root-1", true)

	// An empty list stands in for the cap having dropped every row.
	assert.Equal(t, []string{"child-1"}, h.treeChildIDs("root-1", nil),
		"the activity map is the one child source the cap cannot truncate")
}

func TestActivity_TreeChildrenUnionTheListAndTheMapWithoutDuplicates(t *testing.T) {
	t.Parallel()

	h, _ := newActivityHandler(t, "root-1")
	h.setTurnActive("child-1", "root-1", true)
	h.setTurnActive("root-1", "root-1", true)
	// A child of a DIFFERENT root must not leak into this tree.
	h.setTurnActive("child-9", "root-2", true)

	rows := []bgtask.Item{
		{RowKey: "a", ChildAgentID: "child-1", Status: bgtask.StatusRunning},
		{RowKey: "b", ChildAgentID: "child-2", Status: bgtask.StatusRunning},
		{RowKey: "c", Status: bgtask.StatusRunning},
		{RowKey: "d", ChildAgentID: "root-1", Status: bgtask.StatusRunning},
	}

	got := h.treeChildIDs("root-1", rows)

	assert.ElementsMatch(t, []string{"child-1", "child-2"}, got)
	assert.Len(t, got, 2, "child-1 is in both sources, and a root is not its own child")
}

func TestActivity_ATurnEndRootsAChildEntryItCreates(t *testing.T) {
	t.Parallel()

	// A subagent's result envelope can arrive before its task_started, so no
	// registry row links it yet and the turn end is the FIRST touch of its
	// entry. An entry created with no root resolves against ITSELF, which asks
	// whether a process named after the child runs. None ever does, so the
	// subagent would read idle for the rest of its run.
	h, _ := newActivityHandler(t, "child-1")
	h.noteTurnEnded("child-1", "root-1", 2, true)

	assert.Equal(t, "root-1", h.resolveRoot("child-1", ""),
		"the turn end records the feeding process like every other mutator")
}

func TestActivity_AChildTurnEndLeavesTheRootWorking(t *testing.T) {
	t.Parallel()

	// The main tab's thinking indicator reads the ROOT's activity, and a
	// subagent is one step of the root's turn. A child that ended its own turn
	// must not settle the root: the root still owes the user a reply, and
	// dropping the spinner there says it finished while the root still runs.
	h, rec := newActivityHandler(t, "root-1")
	settles := holdSettles(t, h)
	h.setTurnActive("root-1", "root-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	// What a child sink does at its own turn end: it records the count against
	// the CHILD, and any turn flag it publishes identifies the CHILD too.
	h.noteTurnEnded("child-1", "root-1", 4, true)
	h.setTurnActive("child-1", "root-1", false)

	assert.Equal(t, []bool{true}, rec.busyStates(), "the root published no settle")
	// The window would SWALLOW a wrong settle, so an unchanged wire is not on its
	// own proof that the root's derivation stayed WORKING.
	require.Equal(t, 0, settles.close(), "and opened no window on the root either")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		h.AgentActivitySnapshot("root-1", "root-1").State)

	// And the count stays the child's. The root's own settle carries none,
	// because no turn end of the ROOT's recorded one -- which is what tells the
	// client to ring rather than to read a zero.
	h.setTurnActive("root-1", "root-1", false)
	require.Equal(t, 1, settles.close(), "the root's own clear is the settle that waits")
	require.Equal(t, []bool{true, false}, rec.busyStates())
	assert.Nil(t, rec.last().NumToolUses, "a child's count belongs to the child's settle")
}

func TestCountActiveBackgroundTasks(t *testing.T) {
	t.Parallel()

	rows := []bgtask.Item{
		{RowKey: "a", ChildAgentID: "child-1", Status: bgtask.StatusCompleted},
		{RowKey: "b", ChildAgentID: "child-2", Status: bgtask.StatusRunning},
		{RowKey: "c", Status: bgtask.StatusPending},
	}

	// A ROOT counts every descendant's row; that roll-up is what the root tab has
	// always meant.
	assert.Equal(t, int32(2), countActiveBackgroundTasks(rows, ""))
	// A CHILD counts only its own -- counting the root's whole registry kept a
	// finished subagent spinning for as long as any SIBLING ran.
	assert.Equal(t, int32(0), countActiveBackgroundTasks(rows, "child-1"), "its row finished")
	assert.Equal(t, int32(1), countActiveBackgroundTasks(rows, "child-2"))
	assert.Equal(t, int32(0), countActiveBackgroundTasks(rows, "child-unknown"), "no row means no run")
	assert.Equal(t, int32(0), countActiveBackgroundTasks(nil, ""))
}

// --- The registry inputs, against a real store -----------------------------
//
// The tests above drive the in-memory inputs with no database, so the
// background-task input of the derivation -- the one that carries the root/child
// split -- never runs there. These wire the real registry.

// setupActivityRegistryTest gives a root, one linked child and a handler whose
// process check answers true, so the registry is the only input that moves.
func setupActivityRegistryTest(t *testing.T) (*Service, agent.OutputSink, string, string) {
	t.Helper()
	svc, _, childID, rootID := setupChildAgentTest(t)
	// A running process is a precondition of every busy answer, and this suite
	// is about what the registry says. The real check reads the agent manager,
	// which holds no subprocess in a test.
	svc.Output.processRunning = func(string) bool { return true }
	return svc, svc.Output.NewSink(rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), rootID, childID
}

// unspentToolCount reads the count waiting for an agent's next settle. The
// latch is the mechanism behind NumToolUses, and reading it directly lets a case
// pin WHICH settle may spend it without having to drive the settle itself.
func unspentToolCount(h *OutputHandler, agentID string) *int32 {
	st := h.activityFor(agentID, "")
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.settledToolUses
}

func TestActivity_ARootCountsTheRunningRowsTheCapHid(t *testing.T) {
	t.Parallel()

	// The cap is a DISPLAY limit. When a pool of 64 holds no finished row it
	// gives up an ACTIVE one, and retention keeps that row running in the table.
	// Reading the list alone then makes the root settle -- ringing the completion
	// sound and letting the close guard pass -- while the subagent still runs.
	svc, sink, childID := setupRunningSubagent(t, "root-1", bgtask.StatusRunning)

	// Every filler row is active too, so eviction has no finished row to take and
	// gives up the oldest -- this child's.
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	displayed, err := svc.Output.LoadBackgroundTasks(context.Background(), "root-1")
	require.NoError(t, err)
	require.False(t, hasRegistryRowFor(displayed, childID),
		"the display list must actually have dropped the row, or this proves nothing")

	got := svc.Output.AgentActivitySnapshot("root-1", "root-1")

	assert.True(t, got.Working(), "the evicted subagent is still running")
	assert.Equal(t, int32(bgtask.MaxTasks+1), got.ActiveTasks,
		"the count adds back what the cap hid, so the root does not settle early")
}

func TestActivity_AReadmittedRowStopsBeingCountedTwice(t *testing.T) {
	t.Parallel()

	// The hidden-active count corrects for what the DISPLAY list cannot see, so
	// a row that comes BACK into the list must leave the correction. Otherwise
	// the list and the correction both speak for it, the count drifts up on
	// every re-admit, and the root can never settle.
	//
	// The pool is full here, so a re-admit necessarily evicts another active row
	// and the hidden COUNT stays 1. Which row is hidden changes; how many are
	// running does not.
	svc, sink := setupRootSink(t, "root-1")
	svc.Output.processRunning = func(string) bool { return true }
	childID, err := sink.EnsureChildAgent("spawn-1", "task-1", "SCAN")
	require.NoError(t, err)
	// A CHANGED title each time, so the upsert is a real mutation. An identical
	// one is a no-op the registry never re-admits.
	upsertChild := func(title string) {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: "task-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
			Title: title, Status: bgtask.StatusRunning,
		}))
	}
	upsertChild("SCAN")
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	require.Equal(t, int32(1), svc.Output.hiddenActiveTasks("root-1"), "the cap hid a row")
	require.Equal(t, int32(bgtask.MaxTasks+1),
		svc.Output.AgentActivitySnapshot("root-1", "root-1").ActiveTasks)

	// Its next mutation re-admits it, twice over.
	upsertChild("SCAN two")
	upsertChild("SCAN three")

	assert.Equal(t, int32(1), svc.Output.hiddenActiveTasks("root-1"),
		"one row is hidden, not one more per re-admit")
	assert.Equal(t, int32(bgtask.MaxTasks+1),
		svc.Output.AgentActivitySnapshot("root-1", "root-1").ActiveTasks,
		"65 subagents run, however many times the cap swapped which one it hides")
}

func TestActivity_AStaleDerivationDoesNotLatchOverAFresherOne(t *testing.T) {
	// Two refreshes for one agent derive from registry reads taken at different
	// moments, and neither holds a lock across the derivation. Without the
	// ticket the slower reader publishes LAST and its older answer wins.
	//
	// The edge trigger then makes that permanent: the next genuine change
	// compares equal to what the stale publish recorded, so nothing is sent. The
	// tab keeps no spinner for the whole turn AND misses its settle.
	svc, sink, rootID, _ := setupActivityRegistryTest(t)

	// The slow refresh, up to the point where it holds its inputs but has not
	// published. Taking the ticket and reading the rows is exactly what
	// refreshActivity does before it derives, so this is that refresh parked
	// mid-flight -- driven directly, because a seam that parks the FIRST
	// derivation cannot tell which caller it caught.
	staleSeq := svc.Output.activitySeq.Add(1)
	// An empty list is what that read returned: it happened before any task of
	// this root's existed.
	var staleRows []bgtask.Item
	stale := svc.Output.activitySnapshotFrom(rootID, rootID, staleRows)
	require.False(t, stale.Working(), "the slow refresh would derive idle from what it read")

	// A task starts and publishes busy while that refresh is still in flight.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-shell", Kind: bgtask.KindShell,
		Title: "npm run build", Status: bgtask.StatusRunning,
	}))
	require.True(t, svc.Output.AgentActivitySnapshot(rootID, rootID).Working(),
		"the running task makes the agent busy")

	// Now the slow refresh finishes, carrying its older rows.
	svc.Output.refreshActivityFrom(rootID, rootID, staleRows, staleSeq, settleHeld)

	st := svc.Output.activityFor(rootID, rootID)
	st.mu.Lock()
	published, hasPublished := st.published, st.hasPublished
	st.mu.Unlock()
	require.True(t, hasPublished)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, published,
		"the older derivation must not overwrite the answer a fresher one already published")
}

func TestActivity_ShutdownJoinsTheDeferredTreeRefresh(t *testing.T) {
	// resetAgentActivity publishes on its own goroutine, so a relaunch is not
	// stalled behind a slow watcher. Nothing joined that goroutine, so the
	// refresh could still be reading the registry and broadcasting after
	// Shutdown returned and the caller closed the database.
	svc, _, rootID, _ := setupActivityRegistryTest(t)

	var calls atomic.Int32
	parked, release := make(chan struct{}), make(chan struct{})
	// Only the FIRST derivation parks. refreshActivityTree asks once for the
	// root and once per child, and sync.Once would block the second caller
	// instead of letting it through.
	svc.Output.processRunning = func(string) bool {
		if calls.Add(1) == 1 {
			close(parked)
			<-release
		}
		return false
	}

	svc.Output.NoteAgentProcessExited(rootID)
	<-parked

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Shutdown()
	}()
	select {
	case <-done:
		t.Fatal("Shutdown returned while a deferred activity refresh was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
}

func TestActivity_AClearThatDoesNotSettleDropsTheCount(t *testing.T) {
	t.Parallel()

	// The count describes the turn that ENDED, and only the settle that turn's
	// own clear produces may spend it.
	//
	// Here a shell task from an earlier turn is still running, so this turn's
	// clear settles nothing. The settle arrives later, when that task ends, and
	// it belongs to the task rather than to the zero-tool turn. Spending the
	// stale 0 there makes the client suppress the alert, so the user never
	// learns the long task finished.
	svc, sink, rootID, _ := setupActivityRegistryTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-shell", Kind: bgtask.KindShell,
		Title: "npm run build", Status: bgtask.StatusRunning,
	}))

	svc.Output.setTurnActive(rootID, rootID, true)
	svc.Output.noteTurnEnded(rootID, rootID, 0, true)
	svc.Output.setTurnActive(rootID, rootID, false)

	require.True(t, svc.Output.AgentActivitySnapshot(rootID, rootID).Working(),
		"the shell task holds the agent past its own turn end")
	assert.Nil(t, unspentToolCount(svc.Output, rootID),
		"the settle the shell task causes belongs to the task, so it must alert")
}

// setupRunningSubagent builds a root whose single subagent row is open, which is
// where most of the cases below start. The row's status is the one input that
// varies, so it is the one parameter.
//
// The caller may replace processRunning afterwards: this leaves it answering
// true, so a case drives the registry inputs in isolation.
func setupRunningSubagent(
	t *testing.T, rootID string, status bgtask.Status,
) (*Service, agent.OutputSink, string) {
	t.Helper()
	svc, sink := setupRootSink(t, rootID)
	svc.Output.processRunning = func(string) bool { return true }
	childID, err := sink.EnsureChildAgent("spawn-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "SCAN", Status: status,
	}))
	return svc, sink, childID
}

// unspentTurnActive reads the turn flag an entry still holds, which for a collab
// child is what its input queue follows.
func unspentTurnActive(h *OutputHandler, agentID string) bool {
	v, ok := h.activity.Load(agentID)
	if !ok {
		return false
	}
	st := v.(*agentActivity)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.turnActive
}

// watchActivity attaches a recorder to a service-backed test, so a case can
// assert what reached the WIRE rather than only what a snapshot read derives.
func watchActivity(t *testing.T, svc *Service, agentIDs ...string) *activityRecorder {
	t.Helper()
	rec := newActivityRecorder("ch-activity")
	entries := make([]watchEntry, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		entries = append(entries, watchEntry{id: agentID, mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY})
	}
	svc.Watchers.agents.setWatches("ch-activity", entries, rec)
	return rec
}

func TestActivity_TheLastBackgroundTaskSettlesTheAgent(t *testing.T) {
	t.Parallel()

	// The other end of AClearThatDoesNotSettleDropsTheCount, which stops at the
	// unspent count. A turn that leaves a shell task running rings nothing, and
	// the ring the user waits for is the one that task's END produces. Without
	// this the whole path is asserted up to the last hop and never through it.
	// setupRootSink, not setupActivityRegistryTest: the latter leaves a running
	// subagent row behind, and a second live row means the shell task's close is
	// never the LAST one.
	const rootID = "root-1"
	svc, sink := setupRootSink(t, rootID)
	svc.Output.processRunning = func(string) bool { return true }
	rec := watchActivity(t, svc, rootID)
	settles := holdSettles(t, svc.Output)
	svc.Output.setTurnActive(rootID, rootID, true)
	require.Equal(t, []bool{true}, rec.busyStates())
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-shell", Kind: bgtask.KindShell,
		Title: "npm run build", Status: bgtask.StatusRunning,
	}))

	svc.Output.noteTurnEnded(rootID, rootID, 4, true)
	svc.Output.setTurnActive(rootID, rootID, false)
	assert.Equal(t, []bool{true}, rec.busyStates(),
		"the shell task holds the agent past its own turn end, so nothing rings yet")
	require.Equal(t, 0, settles.close(), "and no window is waiting to ring it late either")

	require.NoError(t, sink.CloseBackgroundTask("row-shell", bgtask.StatusCompleted))
	require.Equal(t, 1, settles.close(), "the last task's close opened the settle window")

	require.Equal(t, []bool{true, false}, rec.busyStates(), "the last task ends the work")
	assert.Nil(t, rec.last().NumToolUses,
		"the settle belongs to the task, not to the turn, so it alerts unconditionally")
}

func TestActivity_AnEarlierBackgroundTaskSettlesNothing(t *testing.T) {
	t.Parallel()

	// LAST, not any. A task that ends while a sibling runs moved nothing the
	// user is waiting for, and ringing there is the same early alert the
	// subagent rule exists to prevent.
	const rootID = "root-1"
	svc, sink := setupRootSink(t, rootID)
	svc.Output.processRunning = func(string) bool { return true }
	rec := watchActivity(t, svc, rootID)
	settles := holdSettles(t, svc.Output)
	for _, rowKey := range []string{"row-shell-1", "row-shell-2"} {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: rowKey, Kind: bgtask.KindShell,
			Title: rowKey, Status: bgtask.StatusRunning,
		}))
	}
	require.Equal(t, []bool{true}, rec.busyStates())

	require.NoError(t, sink.CloseBackgroundTask("row-shell-1", bgtask.StatusCompleted))

	// The window would SWALLOW a wrong settle, so "nothing was published" alone
	// no longer distinguishes "no settle derived" from "a settle is waiting".
	assert.Equal(t, 0, settles.close(), "one of two tasks ending opens no window")
	assert.Equal(t, []bool{true}, rec.busyStates(), "one of two tasks ending is not a settle")
	assert.True(t, svc.Output.AgentActivitySnapshot(rootID, rootID).Working())
}

func TestActivity_AClearThatSettlesStillSpendsTheCount(t *testing.T) {
	t.Parallel()

	// The other half of the rule: when the clear DOES settle the agent, the
	// count reaches that settle. Dropping it here would ring the completion
	// sound for every turn that used no tool, which is the case it exists to
	// suppress.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 0, true)
	h.setTurnActive("agent-1", "agent-1", false)
	// The clear runs while the settle it produced still waits, so the count has
	// to survive the window. Clearing it there made every ordinary turn end ring,
	// zero-tool turns included.
	require.Equal(t, 1, settles.close())

	last := rec.last()
	require.NotNil(t, last)
	assert.NotEqual(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, last.GetState())
	require.NotNil(t, last.NumToolUses, "the settle this clear produced spends the count")
	assert.Equal(t, int32(0), last.GetNumToolUses())
}

func TestActivity_RootRollsUpDescendantsAndAChildReadsOnlyItsOwnRow(t *testing.T) {
	t.Parallel()

	svc, sink, rootID, childID := setupActivityRegistryTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-key-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "child task", Status: bgtask.StatusRunning,
	}))

	// A subagent's registry row IS its run, so the child needs no turn of its
	// own. The root rolls the same row up: its tab has always meant "anything
	// under me is working".
	root := svc.Output.AgentActivitySnapshot(rootID, rootID)
	assert.True(t, root.Working(), "a running descendant makes the root busy with no turn of its own")
	assert.Equal(t, int32(1), root.ActiveTasks)
	child := svc.Output.AgentActivitySnapshot(childID, rootID)
	assert.True(t, child.Working())
	assert.Equal(t, int32(1), child.ActiveTasks)
}

func TestActivity_AFinishedChildIsIdleWhileASiblingKeepsRunning(t *testing.T) {
	t.Parallel()

	svc, sink, rootID, childID := setupActivityRegistryTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-key-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "child task", Status: bgtask.StatusRunning,
	}))
	siblingID, err := sink.EnsureChildAgent("spawn-span-2", "row-key-2", "sibling task")
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-key-2", Kind: bgtask.KindSubagent, ChildAgentID: siblingID,
		Title: "sibling task", Status: bgtask.StatusRunning,
	}))

	require.NoError(t, sink.CloseBackgroundTask("row-key-1", bgtask.StatusCompleted))

	// Counting the root's whole registry for a child was the bug this split
	// exists to prevent: it kept a finished subagent spinning for as long as any
	// sibling ran.
	child := svc.Output.AgentActivitySnapshot(childID, rootID)
	assert.False(t, child.Working(), "a child whose own row ended is done, whatever its siblings do")
	assert.True(t, svc.Output.AgentActivitySnapshot(siblingID, rootID).Working())
	root := svc.Output.AgentActivitySnapshot(rootID, rootID)
	assert.True(t, root.Working(), "the root still rolls up the sibling")
	assert.Equal(t, int32(1), root.ActiveTasks, "the finished row drops out of the count")
}

func TestActivity_ChildIsIdleWhenTheFeedingProcessIsGone(t *testing.T) {
	t.Parallel()

	svc, sink, rootID, childID := setupActivityRegistryTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-key-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "child task", Status: bgtask.StatusRunning,
	}))
	// A child owns no process. Both paths ask about the root, because a child tab
	// is working only while the process feeding it runs -- and a crash leaves
	// registry rows that never reached a final status.
	svc.Output.processRunning = func(string) bool { return false }

	child := svc.Output.AgentActivitySnapshot(childID, rootID)
	assert.False(t, child.Working())
	assert.Equal(t, int32(1), child.ActiveTasks, "the count still reports the stranded row")
	assert.False(t, svc.Output.AgentActivitySnapshot(rootID, rootID).Working())
}

func TestAgentToProto_CarriesTheDerivedActivity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, rootID, childID := setupActivityRegistryTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-key-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "child task", Status: bgtask.StatusRunning,
	}))

	// The hydration path. A tab watching in NOTIFY mode gets no catch-up replay,
	// so a list read is the only place it learns the current value -- and both
	// close guards start from a list read.
	rootRow, err := svc.Queries.GetAgentByID(ctx, rootID)
	require.NoError(t, err)
	rootInfo := svc.agentToProto(&rootRow, true, nil)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, rootInfo.GetActivityState())
	assert.Equal(t, int32(1), rootInfo.GetActiveBackgroundTasks())

	childRow, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	childInfo := svc.agentToProto(&childRow, false, nil)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, childInfo.GetActivityState(),
		"a child is working while its own row runs")
	assert.Equal(t, int32(1), childInfo.GetActiveBackgroundTasks())

	require.NoError(t, sink.CloseBackgroundTask("row-key-1", bgtask.StatusCompleted))
	childRow, err = svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.agentToProto(&childRow, false, nil).GetActivityState())

	// The third state reaches the wire too. It is the one a close guard reading
	// a single "is it working" boolean could not see, so a list read that
	// flattened it back to IDLE would let a mid-turn tab close unwarned.
	svc.Output.setTurnActive(rootID, rootID, true)
	svc.Output.noteControlRequestAdded(rootID, rootID, "req-1")
	rootRow, err = svc.Queries.GetAgentByID(ctx, rootID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		svc.agentToProto(&rootRow, true, nil).GetActivityState())
}

// --- The terminal half of the close guard ----------------------------------

// TestInspectTerminalProcesses_OmitsWhatItCannotWarnAbout pins the handler's
// contract at the boundary the guard reads.
//
// Four situations answer alike, and they have to: the terminal sits idle, its
// shell exited, the PTY has not spawned yet, or the Worker no longer holds the
// id. A close guard treats every one as "nothing to warn about", and an ERROR
// instead of an empty answer would fail the whole close rather than let it
// proceed unwarned.
func TestInspectTerminalProcesses_OmitsWhatItCannotWarnAbout(t *testing.T) {
	t.Parallel()

	_, d, w := setupTestService(t)

	dispatch(d, "InspectTerminalProcesses", &leapmuxv1.InspectTerminalProcessesRequest{
		// A blank id, and one no PTY was ever started for.
		TerminalIds: []string{"", "no-such-terminal"},
	}, w)

	require.Empty(t, w.errors, "an unanswerable probe is an empty answer, never a failed close")
	var resp leapmuxv1.InspectTerminalProcessesResponse
	lastResponse(t, w, &resp)
	assert.Empty(t, resp.GetTerminals())
}

// TestActivity_AChildStaysBusyAfterTheCapDropsItsRow is the boundary the display
// list cannot answer on its own.
//
// The registry cap gives up its oldest ACTIVE row when the pool is full of
// running work, so a subagent that still runs can be missing from the list.
// Reading that as idle drops the spinner and hides the Interrupt button on a run
// the user can watch happening -- the load-bearing case, because the button is
// how they cancel it.
func TestActivity_AChildStaysBusyAfterTheCapDropsItsRow(t *testing.T) {
	t.Parallel()

	svc, sink, childID := setupRunningSubagent(t, "root-1", bgtask.StatusRunning)
	before := svc.Output.AgentActivitySnapshot(childID, "root-1")
	require.True(t, before.Working(), "the child is running before the cap moves")
	require.Equal(t, int32(1), before.ActiveTasks)

	// Every filler row is active too, so eviction has no finished row to take and
	// gives up the oldest -- this child's.
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	displayed, err := svc.Output.LoadBackgroundTasks(context.Background(), "root-1")
	require.NoError(t, err)
	require.False(t, hasRegistryRowFor(displayed, childID),
		"the display list must actually have dropped the row, or this proves nothing")

	after := svc.Output.AgentActivitySnapshot(childID, "root-1")

	assert.True(t, after.Working(), "the row left the sidebar, not the machine")
	assert.Equal(t, int32(1), after.ActiveTasks)
}

// The mirror. The table fallback must not resurrect a subagent that really
// finished, or a closed child tab would spin for the life of the session.
func TestActivity_AFinishedChildStaysIdlePastTheCap(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	svc.Output.processRunning = func(string) bool { return true }
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)

	got := svc.Output.AgentActivitySnapshot(childID, "root-1")

	assert.False(t, got.Working())
	assert.Zero(t, got.ActiveTasks)
}

// An agent id that owns no registry row at all -- a child whose spawn never
// reached the registry, or one the caller made up -- is idle, not an error.
func TestActivity_AChildWithNoRowAnywhereIsIdle(t *testing.T) {
	t.Parallel()

	svc, _ := setupRootSink(t, "root-1")
	svc.Output.processRunning = func(string) bool { return true }

	got := svc.Output.AgentActivitySnapshot("never-spawned", "root-1")

	assert.False(t, got.Working())
	assert.Zero(t, got.ActiveTasks)
}

func TestHasRegistryRowFor(t *testing.T) {
	t.Parallel()

	rows := []bgtask.Item{
		{RowKey: "a", ChildAgentID: "child-1", Status: bgtask.StatusCompleted},
		{RowKey: "b", Status: bgtask.StatusRunning},
	}

	// "This child's row says finished" and "the list cannot say" are different
	// facts, and only the second one may fall through to the table. A finished
	// row that re-read the store on every snapshot would put a query on the
	// settle path of every closed subagent.
	assert.True(t, hasRegistryRowFor(rows, "child-1"), "a finished row is still an answer")
	assert.False(t, hasRegistryRowFor(rows, "child-2"), "no row for this child at all")
	// A shell task carries no child agent id, so a blank query would match the
	// first one and report an answer the list never gave.
	assert.False(t, hasRegistryRowFor(rows, ""), "an empty id is nobody's row")
	assert.False(t, hasRegistryRowFor(nil, "child-1"))
}

func TestActivity_AChildIsIdleWhenThereIsNoStoreToAsk(t *testing.T) {
	t.Parallel()

	// The in-memory handler owns no queries, and the child miss path runs before
	// anything else can stop it. Reporting idle is the same stance
	// backgroundTaskRows takes for a failed read: a wrong busy strands a tab the
	// user can no longer close by any route.
	h, rec := newActivityHandler(t, "child-1")

	got := h.AgentActivitySnapshot("child-1", "root-1")

	assert.False(t, got.Working())
	assert.Zero(t, got.ActiveTasks)
	assert.Empty(t, rec.busyStates(), "a read publishes nothing")
}

func TestActivity_APendingChildRowCountsAsWorkPastTheCap(t *testing.T) {
	t.Parallel()

	// A subagent that the agent spawned but that never reported yet is PENDING, and
	// that is the state it sits in for the whole window where the user is most
	// likely to close the tab by accident. The fallback has to treat it as work,
	// not just `running`.
	svc, sink, childID := setupRunningSubagent(t, "root-1", bgtask.StatusPending)
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	displayed, err := svc.Output.LoadBackgroundTasks(context.Background(), "root-1")
	require.NoError(t, err)
	require.False(t, hasRegistryRowFor(displayed, childID), "the cap must have dropped the row")

	got := svc.Output.AgentActivitySnapshot(childID, "root-1")

	assert.True(t, got.Working())
	assert.Equal(t, int32(1), got.ActiveTasks)
}

func TestActivity_AChildTurnFlagKeepsTheCountItsTurnEndRecorded(t *testing.T) {
	t.Parallel()

	// A collab child publishes SetTurnActive on its OWN sink, because the input
	// queue follows that flag and a child owns a queue of its own. The publish
	// must not touch the settle count: activityStateLocked answers a child from
	// its registry row and never reads the flag, so the refresh cannot spend the
	// count, and clearing it would only destroy what PersistTurnEnd recorded one
	// call earlier. The child's settle then reports no tool count at all, and a
	// client that reads a missing count alerts unconditionally.
	h, _ := newActivityHandler(t, "root-1")
	h.setTurnActive("child-1", "root-1", true)
	h.noteTurnEnded("child-1", "root-1", 4, true)

	h.setTurnActive("child-1", "root-1", false)
	require.NotNil(t, unspentToolCount(h, "child-1"),
		"the child's clear must not discard the count its own turn end recorded")
	assert.Equal(t, int32(4), *unspentToolCount(h, "child-1"))

	// The next turn's rising edge must not discard it either: the child's
	// registry row settles the run, and that settle is what spends the count.
	h.setTurnActive("child-1", "root-1", true)
	require.NotNil(t, unspentToolCount(h, "child-1"))
	assert.Equal(t, int32(4), *unspentToolCount(h, "child-1"))
}

func TestActivity_ARootTurnFlagStillOwnsTheCount(t *testing.T) {
	t.Parallel()

	// The counterpart to the child rule above: a ROOT's flag keeps both effects.
	// A fresh turn supersedes an unspent count, and a clear whose refresh could
	// not spend it drops it so the settle that eventually comes alerts rather
	// than reading a stale zero.
	h, _ := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 7, true)
	require.NotNil(t, unspentToolCount(h, "agent-1"))

	h.setTurnActive("agent-1", "agent-1", true)
	assert.Nil(t, unspentToolCount(h, "agent-1"), "a fresh turn supersedes an unspent count")
}

// --- What a Stop cannot recall. See agentActivity.settleGen. ---

func TestActivity_ALateCallbackDoesNotStealTheNextWindow(t *testing.T) {
	t.Parallel()

	// time.AfterFunc runs a callback on its own goroutine, so a window FIRES and
	// lands later. A cancel and a re-arm both fit in that gap, and the spent
	// callback then owns a handle that belongs to a window it never opened.
	// Without the generation token it clears that handle and publishes at once, so
	// the SECOND stop skips its whole window. That is the early completion sound
	// this file exists to remove, reached the long way round.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	deliveries := settles.expire()
	require.Len(t, deliveries, 1, "the clear opened one window, and it has now fired")

	// Work resumes and stops again inside the gap, which voids the first settle
	// and opens a SECOND window.
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	deliveries[0]()

	assert.Equal(t, []bool{true}, rec.busyStates(), "the spent callback published nothing")
	require.Equal(t, 1, settles.close(), "the second window still owns the delivery")
	assert.Equal(t, []bool{true, false}, rec.busyStates())
	// A window holds one activityRefreshes count, released by whichever of the
	// cancel and the callback got there. A missing release parks Shutdown for
	// good; a double release panics. This returns at once when they balance.
	h.WaitActivityRefreshes()
}

func TestActivity_ALateCallbackDoesNotResurrectAForgottenAgent(t *testing.T) {
	t.Parallel()

	// ForgetActivity cannot recall a callback that already fired. Before the
	// token, that
	// callback called
	// activityFor,
	// whose
	// LoadOrStore
	// MINTED a
	// replacement
	// entry for the
	// agent just
	// retired. It
	// broadcast for a
	// closed tab, and
	// nothing ever
	// reaped that
	// entry, because
	// the one cleanup
	// that deletes it
	// already ran.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	deliveries := settles.expire()
	require.Len(t, deliveries, 1)

	// The tab closes while that callback is on its way. The settle it was
	// carrying lands here, on the way out.
	h.ForgetActivity("agent-1")
	require.Equal(t, []bool{true, false}, rec.busyStates())

	deliveries[0]()

	assert.Equal(t, []bool{true, false}, rec.busyStates(), "the spent callback published nothing")
	_, alive := h.activity.Load("agent-1")
	assert.False(t, alive, "and minted no replacement entry for a retired agent")
	h.WaitActivityRefreshes()
}

func TestActivity_AResumeThatRacesTheDeliveryStillDropsTheCount(t *testing.T) {
	t.Parallel()

	// The count-void has to key on the SETTLE, not on the timer handle. The
	// delivery
	// releases
	// that
	// handle
	// before it
	// re-derives,
	// so a
	// resume
	// that lands
	// in between
	// found a
	// nil handle
	// and kept
	// the count.
	// A
	// zero-tool
	// turn then
	// left a
	// zero
	// behind,
	// and the
	// client
	// suppresses
	// a zero. The background task
	// still running finished later in silence.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 0, true)
	h.setTurnActive("agent-1", "agent-1", false)
	require.NotNil(t, unspentToolCount(h, "agent-1"), "the held settle still owns the count")

	// The window fires, so deliverHeldSettle releases the handle and then
	// re-derives. The work comes back in that gap. Setting the flag directly is
	// what makes the gap reachable. A
	// provider publishes its turn
	// flag a moment before the
	// refresh that reads it lands, so
	// the delivery's own derivation
	// sees WORKING while no refresh
	// has run to void anything.
	deliveries := settles.expire()
	require.Len(t, deliveries, 1)
	st := h.activityFor("agent-1", "agent-1")
	st.mu.Lock()
	st.turnActive = true
	st.mu.Unlock()

	deliveries[0]()

	assert.Nil(t, unspentToolCount(h, "agent-1"), "the resume voided the count the settle carried")
	assert.Equal(t, []bool{true}, rec.busyStates(), "and published nothing, because the work resumed")

	// A stop that really lasts must now alert unconditionally.
	st.mu.Lock()
	st.turnActive = false
	st.mu.Unlock()
	h.refreshActivity("agent-1", "agent-1")
	require.Equal(t, 1, settles.close())
	assert.Nil(t, rec.last().NumToolUses, "so this settle rings rather than reading a stale zero")
}

func TestActivity_APromptAnsweredAtOnceStillReachesTheClient(t *testing.T) {
	t.Parallel()

	// The severe half of holding a prompt. A held WAITING is not merely late. An
	// answer arriving inside the window
	// derives WORKING again, which VOIDS the
	// settle, so the prompt's state never
	// reaches the client at all and nothing
	// ever alerts for it. Publishing at once is what makes the state survive an
	// answer of any speed.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-1")

	assert.Equal(t, 0, settles.close(), "a prompt opens no window, whoever answers it")
	assert.Equal(t, []bool{true, false, true}, rec.busyStates(),
		"the prompt reached the client even though the answer followed at once")
}

func TestActivity_AProcessExitPublishesTheTreeWithoutWaiting(t *testing.T) {
	t.Parallel()

	// MarkAgentBackgroundTasksExited is a process-death boundary: a dead process
	// resumes nothing. Holding there parked every descendant's idle publish for a
	// whole settleDelay, and on the tab-close path nothing later delivered it --
	// the cleanup that follows retires the entry instead. A watcher of a subagent
	// transcript kept a spinner on work whose process was already gone.
	const rootID = "root-1"
	svc, _, childID := setupRunningSubagent(t, rootID, bgtask.StatusRunning)
	alive := true
	svc.Output.processRunning = func(string) bool { return alive }
	rec := watchActivity(t, svc, rootID, childID)
	settles := holdSettles(t, svc.Output)
	svc.Output.setTurnActive(rootID, rootID, true)
	require.True(t, svc.Output.AgentActivitySnapshot(childID, rootID).Working())

	alive = false
	svc.Output.MarkAgentBackgroundTasksExited(rootID, true)

	assert.Equal(t, 0, settles.close(), "a dead process opens no window")
	assert.Contains(t, rec.agentIDs(), childID, "the child's idle publish landed at once")
	assert.False(t, svc.Output.AgentActivitySnapshot(childID, rootID).Working())
}

func TestActivity_TheCatchUpBaselineIsWhatTheClientWasTold(t *testing.T) {
	t.Parallel()

	// A client that CACHES the pushed state has to be seeded from the same
	// version the live events carry. Seeded from the exact derivation instead, a
	// tab that promotes inside a settle window
	// holds IDLE before the window's own
	// AgentActivityChanged arrives. That event then
	// moves nothing, the client sees no WORKING ->
	// not-WORKING edge, and the completion sound
	// never rings.
	h, _ := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		h.AgentActivitySnapshot("agent-1", "agent-1").State,
		"the derivation is exact throughout, which is what ListAgents and the CLI read")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		h.AgentActivityPublished("agent-1", "agent-1"),
		"but the baseline reports what the client actually holds")

	require.Equal(t, 1, settles.close())
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		h.AgentActivityPublished("agent-1", "agent-1"),
		"and the two agree again once the settle lands")
}

func TestActivity_ThePublishedBaselineFallsBackBeforeAnythingIsPublished(t *testing.T) {
	t.Parallel()

	// An agent this Worker never published for has no cached client answer to
	// match, so the exact derivation IS the right baseline. The fallback cannot
	// hide a window either: one opens only where `published` says WORKING, which
	// requires the publish this branch is the absence of.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)

	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		h.AgentActivityPublished("agent-1", "agent-1"),
		"nothing was published, so the exact answer is what a client should hold")
	assert.Empty(t, rec.busyStates(), "and reading the baseline broadcasts nothing")

	// The read above minted the entry, so the fallback now runs its INNER
	// branch: an entry that exists and has published nothing. Dropping the
	// hasPublished guard answers UNSPECIFIED here, which the client would ignore.
	_, minted := h.activity.Load("agent-1")
	require.True(t, minted, "a read mints the entry this branch is about")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		h.AgentActivityPublished("agent-1", "agent-1"),
		"an entry that published nothing still falls back to the exact answer")

	// A turn then runs and settles. The fallback is gone from here on, because
	// the first publish supplies the value.
	h.setTurnActive("agent-1", "agent-1", true)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		h.AgentActivityPublished("agent-1", "agent-1"))
	h.setTurnActive("agent-1", "agent-1", false)
	require.Equal(t, 1, settles.close())
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		h.AgentActivityPublished("agent-1", "agent-1"))
}

func TestActivity_ShutdownCancelsHeldSettlesBeforeItJoins(t *testing.T) {
	t.Parallel()

	// The WIRING, not the method. Deleting the CancelHeldSettles call in
	// Service.Shutdown, or moving it after the join, broke no case before this
	// one -- and both are load-bearing. A window that survives reads the registry
	// and broadcasts after the caller closes the
	// database. And because an armed window
	// holds one of the counts the join waits on,
	// joining FIRST parks Shutdown for a whole
	// settleDelay.
	const rootID = "root-1"
	svc, _ := setupRootSink(t, rootID)
	svc.Output.processRunning = func(string) bool { return true }
	settles := holdSettles(t, svc.Output)
	svc.Output.setTurnActive(rootID, rootID, true)
	svc.Output.setTurnActive(rootID, rootID, false)
	require.Equal(t, 1, len(settles.openWindows()), "this case needs an open window to survive")

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Shutdown()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown parked: it joined the refreshes before it cancelled the windows")
	}

	assert.Empty(t, settles.openWindows(), "Shutdown left no window to fire against a closed database")
}

func TestActivity_AWindowCannotOpenOnceShutdownBegins(t *testing.T) {
	t.Parallel()

	// The latch, which is what makes the cancel above final. Without it the sweep
	// is a one-shot pass. A
	// refresh that lands
	// after it, such as a
	// provider still
	// draining its pipe,
	// opens a window that
	// fires three seconds
	// later against a
	// database the caller
	// already closed.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	h.SetShuttingDown()
	h.setTurnActive("agent-1", "agent-1", false)

	assert.Empty(t, settles.openWindows(), "no window opened after the latch")
	assert.Equal(t, []bool{true, false}, rec.busyStates(),
		"and the settle publishes at once rather than disappearing")
}

func TestActivity_ASubagentSettlesWhenTheProviderRetiresIt(t *testing.T) {
	t.Parallel()

	// The whole path, through the sink the providers actually use. A subagent's
	// last row closes and the provider calls CleanupChildAgent on the very next
	// line, so the retire lands INSIDE the window that close opened. Dropping the
	// settle there
	// loses it for
	// good: the
	// entry that
	// held the edge
	// is gone, and
	// no later
	// refresh can
	// find it. The
	// child's tab
	// then keeps a
	// spinner and
	// an armed
	// Interrupt
	// button on a
	// run that
	// ended.
	const rootID = "root-1"
	svc, sink, childID := setupRunningSubagent(t, rootID, bgtask.StatusRunning)
	rec := watchActivity(t, svc, rootID, childID)
	settles := holdSettles(t, svc.Output)
	svc.Output.setTurnActive(rootID, rootID, true)
	require.True(t, svc.Output.AgentActivitySnapshot(childID, rootID).Working())

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.Empty(t, rec.agentIDs(), "the child's settle is still waiting out its window")
	sink.CleanupChildAgent(childID)

	assert.Equal(t, []string{childID}, rec.agentIDs(), "the retire delivered it")
	assert.Equal(t, []bool{false}, rec.busyStates())
	assert.Equal(t, 0, settles.close(), "and left no window to fire against a retired agent")
	assert.True(t, svc.Output.AgentActivitySnapshot(rootID, rootID).Working(),
		"the root's own turn is untouched")
}

func TestSettleWindows_HoldSettlesKeepsTheFakeAlreadyInstalled(t *testing.T) {
	t.Parallel()

	// The harness's own invariant. A case takes its handle from a SECOND call,
	// after the constructor already installed one. If that call swapped the fake,
	// a window opened in between
	// would be stranded on the
	// discarded instance. Nothing
	// could fire it, so a real settle
	// would vanish and close would
	// report zero while a spinner
	// stayed stuck.
	h, _ := newActivityHandler(t, "agent-1")
	first := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	second := holdSettles(t, h)

	assert.Same(t, first, second, "the second call must hand back the fake in use")
	assert.Equal(t, 1, second.close(), "so the window opened in between is still reachable")
}

// --- The activity map's own lifecycle. See retirableLocked. ---

func TestActivity_AFinishedSubagentLeavesNoEntryBehind(t *testing.T) {
	t.Parallel()

	// Only Codex, ZCode and the ACP providers retire a child. Under Claude and Pi
	// every subagent a
	// session ever spawned
	// kept its entry until
	// the tab closed. Each
	// one then cost a
	// point query per
	// registry mutation,
	// once the display cap
	// evicted its row. A run that ended, and whose end the
	// client knows, has nothing left to remember.
	const rootID = "root-1"
	svc, sink, childID := setupRunningSubagent(t, rootID, bgtask.StatusRunning)
	rec := watchActivity(t, svc, rootID, childID)
	settles := holdSettles(t, svc.Output)
	svc.Output.setTurnActive(rootID, rootID, true)
	require.True(t, svc.Output.AgentActivitySnapshot(childID, rootID).Working())
	_, held := svc.Output.activity.Load(childID)
	require.True(t, held, "the running child owns an entry")

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.Equal(t, 1, settles.close(), "the close opened the child's settle window")

	require.Equal(t, []string{childID}, rec.agentIDs(), "the settle reached the client first")
	_, kept := svc.Output.activity.Load(childID)
	assert.False(t, kept, "and then the finished child's entry went with it")
	assert.NotContains(t, svc.Output.treeChildIDs(rootID, nil), childID,
		"so a later tree refresh does not derive it again")
	assert.False(t, svc.Output.AgentActivitySnapshot(childID, rootID).Working(),
		"a read still answers correctly from the registry")
}

func TestActivity_ARunningSubagentKeepsItsEntryPastTheCap(t *testing.T) {
	t.Parallel()

	// The mirror, and the reason the reap tests the derived state rather than
	// the display list. The cap gives up an ACTIVE row when the pool is full, so a
	// child that is still going can vanish from that list. The
	// map is the one source the cap cannot truncate.
	const rootID = "root-1"
	svc, _, childID := setupRunningSubagent(t, rootID, bgtask.StatusRunning)
	holdSettles(t, svc.Output)
	svc.Output.refreshActivityTree(rootID, settleHeld)
	require.True(t, svc.Output.AgentActivitySnapshot(childID, rootID).Working())

	// An empty list stands in for the cap having dropped every row.
	assert.Contains(t, svc.Output.treeChildIDs(rootID, nil), childID,
		"a running child stays reachable when the display list cannot show it")
	_, kept := svc.Output.activity.Load(childID)
	assert.True(t, kept, "and keeps the entry that carries its published state")
}

func TestActivity_ACollabChildKeepsItsEntryWhileItsTurnRuns(t *testing.T) {
	t.Parallel()

	// A collab child publishes its own turn flag, because its input queue
	// follows that flag. Its registry row can end while that turn is still open,
	// and the row's end is what publishes IDLE. The reap then
	// sees a finished child. It would drop the flag its queue
	// depends on, plus the token that orders two publishes
	// arriving out of order. Only an entry that holds
	// nothing may go.
	const rootID = "root-1"
	svc, sink, childID := setupRunningSubagent(t, rootID, bgtask.StatusRunning)
	settles := holdSettles(t, svc.Output)
	svc.Output.setTurnActive(rootID, rootID, true)
	require.True(t, svc.Output.AgentActivitySnapshot(childID, rootID).Working())

	// The child opens a turn of its own, which changes no derived state: a
	// child answers from its registry row and never reads the flag.
	svc.Output.setTurnActive(childID, rootID, true)

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.Equal(t, 1, settles.close(), "the row's end settles the child")

	_, kept := svc.Output.activity.Load(childID)
	assert.True(t, kept, "the open turn keeps the entry the reap would have taken")
	assert.True(t, unspentTurnActive(svc.Output, childID), "and the flag its input queue follows")
	assert.Contains(t, svc.Output.treeChildIDs(rootID, nil), childID)
}

func TestActivity_AChildWithAnUnspentCountIsNotReaped(t *testing.T) {
	t.Parallel()

	// The other half of the same rule. A count its turn end recorded belongs to
	// a settle that has not landed, so an entry holding one is not empty.
	h, _ := newActivityHandler(t, "root-1")
	holdSettles(t, h)
	h.setTurnActive("child-1", "root-1", true)
	h.noteTurnEnded("child-1", "root-1", 4, true)
	h.setTurnActive("child-1", "root-1", false)

	require.NotNil(t, unspentToolCount(h, "child-1"), "the child's count survives its own clear")
	assert.Equal(t, int32(4), *unspentToolCount(h, "child-1"))
	assert.Contains(t, h.treeChildIDs("root-1", nil), "child-1",
		"and the entry that carries it is still reachable")
}

func TestActivity_AUserInterruptPublishesWithoutWaiting(t *testing.T) {
	t.Parallel()

	// The window exists for a stop the CLI takes back microseconds later. An
	// interrupt is the opposite: InterruptAgent pauses the input queue before it
	// signals, so no wake follows. Holding it left the thinking indicator and
	// the Interrupt button on screen for three seconds after the user cancelled,
	// which invites a second interrupt.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	h.NoteAgentInterrupted("agent-1", "agent-1")
	h.setTurnActive("agent-1", "agent-1", false)

	assert.Equal(t, 0, settles.close(), "the user stopped it, so nothing can resume it")
	assert.Equal(t, []bool{true, false}, rec.busyStates())
}

func TestActivity_AnIgnoredInterruptDoesNotExemptTheNextTurn(t *testing.T) {
	t.Parallel()

	// The bound on that mark, and the reason it is not simply latched. An agent
	// can IGNORE an interrupt and keep working. The stop that eventually comes
	// then belongs to work the user never cancelled, and it is resumable like
	// any other -- so it must wait out its window.
	h, rec := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, h)
	h.setTurnActive("agent-1", "agent-1", true)
	h.NoteAgentInterrupted("agent-1", "agent-1")

	// The agent carries on: a new turn opens, and its WORKING publish is what
	// says the interrupt did not land.
	h.setTurnActive("agent-1", "agent-1", false)
	require.Equal(t, 0, settles.close(), "the interrupt published at once")
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	require.Equal(t, 1, settles.close(), "the next stop is an ordinary settle again")
	assert.Equal(t, []bool{true, false, true, false}, rec.busyStates())
}
