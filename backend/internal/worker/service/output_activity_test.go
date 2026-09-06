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

// newActivityHandler wires a handler whose process-running check always answers
// true, so a test can drive the other inputs in isolation. `agents` is nil here,
// which the derivation reads as "no process": the fake below replaces that.
func newActivityHandler(t *testing.T, agentID string) (*OutputHandler, *activityRecorder) {
	t.Helper()
	m := NewWatcherManager()
	rec := newActivityRecorder("ch-1")
	m.agents.setWatches("ch-1", []watchEntry{{id: agentID, mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, rec)
	h := &OutputHandler{watcher: m, processRunning: func(string) bool { return true }}
	return h, rec
}

func TestActivity_TurnOpensAndClosesTheBusyState(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")

	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

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
	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	// The agent is blocked on the user, and the user is looking straight at the
	// prompt. Reporting busy there would spin an indicator at somebody who is
	// being asked a question.
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
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
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")

	got := h.AgentActivitySnapshot("agent-1", "agent-1")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, got.State)
	assert.False(t, got.Working(), "the indicator must not spin at somebody being asked a question")
	assert.True(t, got.InterruptsWork(), "but a close would still kill the turn")
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
	h.setTurnActive("agent-1", "agent-1", true)

	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-unknown")

	assert.Equal(t, []bool{true, false}, rec.busyStates())
}

func TestActivity_TwoPromptsNeedBothAnswersBeforeWorkResumes(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	h.noteControlRequestAdded("agent-1", "agent-1", "req-2")
	require.Equal(t, []bool{true, false}, rec.busyStates())

	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-1")
	assert.Equal(t, []bool{true, false}, rec.busyStates(), "still blocked on the second prompt")

	h.noteControlRequestsRemoved("agent-1", "agent-1", "req-2")
	assert.Equal(t, []bool{true, false, true}, rec.busyStates())
}

func TestActivity_DeadProcessIsIdleWhateverElseIsRecorded(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	running := true
	h.processRunning = func(string) bool { return running }

	h.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, rec.busyStates())

	// A crash leaves the turn flag set: no envelope arrives to clear it. The
	// process check is what stops a lost agent from showing work forever.
	running = false
	h.refreshActivity("agent-1", "agent-1")

	assert.Equal(t, []bool{true, false}, rec.busyStates())
}

func TestActivity_ProcessExitClearsEverythingAndSettles(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")
	require.Equal(t, []bool{true, false}, rec.busyStates())

	// HandleAgentProcessExit broadcasts no AgentStatusChange, so this is the only
	// signal a client gets that a crashed agent stopped working.
	h.NoteAgentProcessExited("agent-1")

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
	h.setTurnActive("agent-1", "agent-1", true)

	h.noteTurnEnded("agent-1", "agent-1", 3, true)
	h.setTurnActive("agent-1", "agent-1", false)

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
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 0, true)
	h.setTurnActive("agent-1", "agent-1", false)

	last := rec.last()
	require.NotNil(t, last.NumToolUses)
	assert.Equal(t, int32(0), last.GetNumToolUses())
}

func TestActivity_ProviderThatReportsNoCountLeavesItUnset(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 0, false)
	h.setTurnActive("agent-1", "agent-1", false)

	assert.Nil(t, rec.last().NumToolUses, "unset, so the client rings rather than guessing 0")
}

func TestActivity_SettleWithNoTurnEndCarriesNoCount(t *testing.T) {
	t.Parallel()

	// A control request arriving mid-turn, and a process exit, both settle the
	// agent without a turn ending. Neither should inherit an earlier turn's
	// count, which would silence an alert the user needs.
	h, rec := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteControlRequestAdded("agent-1", "agent-1", "req-1")

	assert.Nil(t, rec.last().NumToolUses)
}

func TestActivity_NewTurnDropsAnUnspentCount(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	h.noteTurnEnded("agent-1", "agent-1", 9, true)

	// A fresh turn supersedes whatever the previous one left unspent, so a stale
	// count cannot silence the alert for the turn now starting.
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	assert.Nil(t, rec.last().NumToolUses)
}

func TestActivity_TreeRecomputesAChildTheDisplayCapDropped(t *testing.T) {
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
	svc, sink := setupRootSink(t, "root-1")
	svc.Output.processRunning = func(string) bool { return true }
	childID, err := sink.EnsureChildAgent("spawn-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "SCAN", Status: bgtask.StatusRunning,
	}))

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
	svc.Output.refreshActivityFrom(rootID, rootID, staleRows, staleSeq)

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

func TestActivity_AClearThatSettlesStillSpendsTheCount(t *testing.T) {
	t.Parallel()

	// The other half of the rule: when the clear DOES settle the agent, the
	// count reaches that settle. Dropping it here would ring the completion
	// sound for every turn that used no tool, which is the case it exists to
	// suppress.
	h, rec := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", "agent-1", 0, true)
	h.setTurnActive("agent-1", "agent-1", false)

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
// running work, so a subagent that is still going can be missing from the list.
// Reading that as idle drops the spinner and hides the Interrupt button on a run
// the user can watch happening -- the load-bearing case, because the button is
// how they cancel it.
func TestActivity_AChildStaysBusyAfterTheCapDropsItsRow(t *testing.T) {
	t.Parallel()

	svc, sink := setupRootSink(t, "root-1")
	svc.Output.processRunning = func(string) bool { return true }
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "SCAN", Status: bgtask.StatusRunning,
	}))
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
	svc, sink := setupRootSink(t, "root-1")
	svc.Output.processRunning = func(string) bool { return true }
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "SCAN", Status: bgtask.StatusPending,
	}))
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	displayed, err := svc.Output.LoadBackgroundTasks(context.Background(), "root-1")
	require.NoError(t, err)
	require.False(t, hasRegistryRowFor(displayed, childID), "the cap must have dropped the row")

	got := svc.Output.AgentActivitySnapshot(childID, "root-1")

	assert.True(t, got.Working())
	assert.Equal(t, int32(1), got.ActiveTasks)
}
