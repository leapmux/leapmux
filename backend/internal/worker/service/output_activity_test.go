package service

import (
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
