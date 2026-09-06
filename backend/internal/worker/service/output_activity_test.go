package service

import (
	"context"
	"sync"
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
		out = append(out, e.GetBusy())
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
// which computeBusy reads as "no process": the fake below replaces that.
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
	assert.False(t, h.computeBusy("agent-1", "agent-1"))
	h.setTurnActive("agent-1", "agent-1", true)
	assert.True(t, h.computeBusy("agent-1", "agent-1"), "the restarted agent can report a turn again")
}

func TestActivity_SettleCarriesTheToolCountOfTheTurnThatEnded(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)

	h.noteTurnEnded("agent-1", 3, true)
	h.setTurnActive("agent-1", "agent-1", false)

	last := rec.last()
	require.NotNil(t, last)
	assert.False(t, last.GetBusy())
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
	h.noteTurnEnded("agent-1", 0, true)
	h.setTurnActive("agent-1", "agent-1", false)

	last := rec.last()
	require.NotNil(t, last.NumToolUses)
	assert.Equal(t, int32(0), last.GetNumToolUses())
}

func TestActivity_ProviderThatReportsNoCountLeavesItUnset(t *testing.T) {
	t.Parallel()

	h, rec := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.noteTurnEnded("agent-1", 0, false)
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
	h.noteTurnEnded("agent-1", 9, true)

	// A fresh turn supersedes whatever the previous one left unspent, so a stale
	// count cannot silence the alert for the turn now starting.
	h.setTurnActive("agent-1", "agent-1", true)
	h.setTurnActive("agent-1", "agent-1", false)

	assert.Nil(t, rec.last().NumToolUses)
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

// --- The registry legs, against a real store -------------------------------
//
// The tests above drive the in-memory inputs with no database, so the
// background-task leg of the derivation -- the one that carries the root/child
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
	rootBusy, rootTasks := svc.Output.AgentActivitySnapshot(rootID, rootID)
	assert.True(t, rootBusy, "a running descendant makes the root busy with no turn of its own")
	assert.Equal(t, int32(1), rootTasks)
	childBusy, childTasks := svc.Output.AgentActivitySnapshot(childID, rootID)
	assert.True(t, childBusy)
	assert.Equal(t, int32(1), childTasks)
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
	childBusy, _ := svc.Output.AgentActivitySnapshot(childID, rootID)
	assert.False(t, childBusy, "a child whose own row ended is done, whatever its siblings do")
	siblingBusy, _ := svc.Output.AgentActivitySnapshot(siblingID, rootID)
	assert.True(t, siblingBusy)
	rootBusy, rootTasks := svc.Output.AgentActivitySnapshot(rootID, rootID)
	assert.True(t, rootBusy, "the root still rolls up the sibling")
	assert.Equal(t, int32(1), rootTasks, "the finished row drops out of the count")
}

func TestActivity_ChildIsIdleWhenTheFeedingProcessIsGone(t *testing.T) {
	t.Parallel()

	svc, sink, rootID, childID := setupActivityRegistryTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-key-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "child task", Status: bgtask.StatusRunning,
	}))
	// A child owns no process. Both legs ask about the root, because a child tab
	// is working only while the process feeding it runs -- and a crash leaves
	// registry rows that never reached a final status.
	svc.Output.processRunning = func(string) bool { return false }

	childBusy, childTasks := svc.Output.AgentActivitySnapshot(childID, rootID)
	assert.False(t, childBusy)
	assert.Equal(t, int32(1), childTasks, "the count still reports the stranded row")
	rootBusy, _ := svc.Output.AgentActivitySnapshot(rootID, rootID)
	assert.False(t, rootBusy)
}

func TestAgentToProto_CarriesTheDerivedActivity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, rootID, childID := setupActivityRegistryTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-key-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "child task", Status: bgtask.StatusRunning,
	}))

	// The hydration leg. A tab watching in NOTIFY mode gets no catch-up replay,
	// so a list read is the only place it learns the current value -- and both
	// close guards start from a list read.
	rootRow, err := svc.Queries.GetAgentByID(ctx, rootID)
	require.NoError(t, err)
	rootInfo := svc.agentToProto(&rootRow, true, nil)
	assert.True(t, rootInfo.GetBusy())
	assert.Equal(t, int32(1), rootInfo.GetActiveBackgroundTasks())

	childRow, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	childInfo := svc.agentToProto(&childRow, false, nil)
	assert.True(t, childInfo.GetBusy(), "a child is busy while its own row runs")
	assert.Equal(t, int32(1), childInfo.GetActiveBackgroundTasks())

	require.NoError(t, sink.CloseBackgroundTask("row-key-1", bgtask.StatusCompleted))
	childRow, err = svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	assert.False(t, svc.agentToProto(&childRow, false, nil).GetBusy())
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
