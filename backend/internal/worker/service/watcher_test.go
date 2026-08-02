package service

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// mockResponseWriter counts SendStream calls for testing broadcast deduplication.
type mockResponseWriter struct {
	channelID   string
	streamCount atomic.Int64
	// sendErr, when non-nil, is returned from SendStream to simulate a
	// dead transport for invalidation tests. Stored as a pointer so tests
	// can clear it mid-run with sendErr.Store(nil) — atomic.Value would
	// panic on a nil store.
	sendErr atomic.Pointer[error]
	// onSend, when non-nil, runs at the top of SendStream. It lets a test
	// act from inside the broadcast send loop -- the one window where that
	// loop holds no lock -- to drive interleavings that would otherwise be
	// timing-dependent (e.g. a re-subscribe landing mid-broadcast).
	onSend atomic.Pointer[func()]
}

func (m *mockResponseWriter) SendResponse(_ *leapmuxv1.InnerRpcResponse) error { return nil }
func (m *mockResponseWriter) SendError(_ int32, _ string) error                { return nil }
func (m *mockResponseWriter) SendStream(_ *leapmuxv1.InnerStreamMessage) error {
	if fn := m.onSend.Load(); fn != nil {
		(*fn)()
	}
	m.streamCount.Add(1)
	if errPtr := m.sendErr.Load(); errPtr != nil {
		return *errPtr
	}
	return nil
}
func (m *mockResponseWriter) ChannelID() string   { return m.channelID }
func (*mockResponseWriter) MaxPayloadBudget() int { return 0 }
func (*mockResponseWriter) BindStream(channel.StreamController) (func(), bool) {
	return func() {}, false
}

func newTestWatcher(channelID string) *mockResponseWriter {
	return &mockResponseWriter{channelID: channelID}
}

// failSends arms the mock to return err from SendStream. Pass nil to clear.
func (m *mockResponseWriter) failSends(err error) {
	if err == nil {
		m.sendErr.Store(nil)
		return
	}
	m.sendErr.Store(&err)
}

// count reports how many channels are subscribed to entityID. Test-only
// accessor so assertions don't have to reproduce the lock discipline.
func (r *watcherRegistry) count(entityID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byEntity[entityID])
}

// hasEntity reports whether entityID has an entry at all, which is how
// the tests tell "no watchers left" from "empty map left behind".
func (r *watcherRegistry) hasEntity(entityID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byEntity[entityID]
	return ok
}

// channelIDs reports the channels subscribed to entityID, for assertions
// about which ones survived rather than how many.
func (r *watcherRegistry) channelIDs(entityID string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byEntity[entityID]))
	for channelID := range r.byEntity[entityID] {
		out = append(out, channelID)
	}
	return out
}

// senderFor reports the writer entityID's registration for channelID
// will send through. The whole point of rebinding is that this changes
// while count does not, so a count-only assertion cannot see it.
func (r *watcherRegistry) senderFor(entityID, channelID string) channel.ResponseWriter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byEntity[entityID][channelID].sender
}

func testAgentEvent(agentID string) *leapmuxv1.AgentEvent {
	return &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: agentID,
			Status:  leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
		}},
	}
}

func testTerminalEvent(terminalID string, data []byte) *leapmuxv1.TerminalEvent {
	return &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: data}},
	}
}

func TestBroadcastTerminalEvent_DeduplicatesWithinPerTerminal(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	// Register the same channel 5 times for the same terminal.
	for i := 0; i < 5; i++ {
		m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	}

	m.BroadcastTerminalEvent("term-1", testTerminalEvent("term-1", []byte("a")))

	assert.Equal(t, int64(1), mock.streamCount.Load())
}

func TestBroadcastAgentEvent_DeduplicatesWithinWatchers(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	for i := 0; i < 5; i++ {
		m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	}

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, int64(1), mock.streamCount.Load())
}

func TestBroadcastTerminalEvent_DistinctWatchersAllReceive(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock1 := newTestWatcher("ch-1")
	mock2 := newTestWatcher("ch-2")

	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock1)
	m.SetTerminalWatches("ch-2", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock2)

	m.BroadcastTerminalEvent("term-1", testTerminalEvent("term-1", []byte("a")))

	assert.Equal(t, int64(1), mock1.streamCount.Load(), "watcher 1")
	assert.Equal(t, int64(1), mock2.streamCount.Load(), "watcher 2")
}

// Registrations are keyed by channel id, so asserting that five repeats
// leave a count of 1 only restates the map's type. What repetition must
// actually preserve is DELIVERY: one event still reaches the channel
// once, through a sender that is still live.
func TestWatchTerminal_IdempotentRegistration(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	for i := 0; i < 5; i++ {
		m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	}

	assert.Equal(t, 1, m.terminals.count("term-1"))

	m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{TerminalId: "term-1"})

	assert.Equal(t, int64(1), mock.streamCount.Load(),
		"re-registering must not fan one event out several times")
}

func TestWatchAgent_IdempotentRegistration(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	for i := 0; i < 5; i++ {
		m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	}

	assert.Equal(t, 1, m.agents.count("agent-1"))

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{AgentId: "agent-1"})

	assert.Equal(t, int64(1), mock.streamCount.Load(),
		"re-registering must not fan one event out several times")
}

// TestWatchAgent_ReRegisterReplacesTheSender pins that re-registering a
// channel routes later events through the NEW sender, which is what lets
// a reconnect on the same channel ID pick up the fresh stream.
func TestWatchAgent_ReRegisterReplacesTheSender(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	first := newTestWatcher("ch-1")
	second := newTestWatcher("ch-1")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, first)
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, second)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, int64(0), first.streamCount.Load(), "the replaced sender must not be used")
	assert.Equal(t, int64(1), second.streamCount.Load(), "the replacing sender receives the event")
}

func TestAgentEvent_DoesNotReachTerminalWatchers(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	// Only register for terminal events.
	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, int64(0), mock.streamCount.Load(), "expected 0 broadcasts to terminal watcher")
}

func TestTerminalEvent_DoesNotReachAgentWatchers(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	// Only register for agent events.
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	m.BroadcastTerminalEvent("term-1", testTerminalEvent("term-1", []byte("a")))

	assert.Equal(t, int64(0), mock.streamCount.Load(), "expected 0 broadcasts to agent watcher")
}

func TestUnwatchAll_RemovesFromAllLists(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}, {id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}, {id: "term-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	m.UnwatchAll("ch-1")

	agentCount := m.agents.count("agent-1") + m.agents.count("agent-2")
	termCount := m.terminals.count("term-1") + m.terminals.count("term-2")
	assert.Equal(t, 0, agentCount, "expected 0 agent watchers after UnwatchAll")
	assert.Equal(t, 0, termCount, "expected 0 terminal watchers after UnwatchAll")

	// Verify no broadcasts reach the removed watcher.
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	m.BroadcastTerminalEvent("term-1", testTerminalEvent("term-1", []byte("a")))

	assert.Equal(t, int64(0), mock.streamCount.Load(), "expected 0 broadcasts after UnwatchAll")
}

func TestUnwatchSession_DoesNotWipeSuccessor(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	first := newTestWatcher("ch-1")
	second := newTestWatcher("ch-1")

	sid1 := m.BeginSession("ch-1")
	m.SetAgentWatchesForSession("ch-1", sid1, []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, first)

	sid2 := m.BeginSession("ch-1")
	// BeginSession clears predecessor watches so promotion starts clean.
	assert.Equal(t, 0, m.agents.count("agent-1"))
	m.SetAgentWatchesForSession("ch-1", sid2, []watchEntry{{id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, second)

	// Predecessor teardown must not clear the successor's registrations.
	m.UnwatchSession("ch-1", sid1)
	assert.Equal(t, 1, m.agents.count("agent-2"))
	assert.Equal(t, 0, m.agents.count("agent-1"))

	m.BroadcastAgentEvent("agent-2", testAgentEvent("agent-2"))
	assert.Equal(t, int64(1), second.streamCount.Load())
	assert.Equal(t, int64(0), first.streamCount.Load())
}

func TestBeginSession_ClearsPredecessorWatches(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	first := newTestWatcher("ch-1")
	sid1 := m.BeginSession("ch-1")
	m.SetAgentWatchesForSession("ch-1", sid1, []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, first)
	require.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, m.AgentModesForChannel("ch-1")["agent-1"])

	_ = m.BeginSession("ch-1")
	assert.Empty(t, m.AgentModesForChannel("ch-1"), "successor BeginSession must drop stale FULL so catch-up can re-fire")
}

func TestModesForChannel_ReflectsCurrentRegistrations(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	m.SetAgentWatches("ch-1", []watchEntry{
		{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL},
		{id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY},
	}, mock)
	m.SetTerminalWatches("ch-1", []watchEntry{
		{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL},
	}, mock)

	agentModes := m.AgentModesForChannel("ch-1")
	require.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, agentModes["agent-1"])
	require.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, agentModes["agent-2"])
	require.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, m.TerminalModesForChannel("ch-1")["term-1"])
	assert.Empty(t, m.AgentModesForChannel("ch-other"))

	// After replace, omitted entities disappear from the snapshot.
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, mock)
	agentModes = m.AgentModesForChannel("ch-1")
	require.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, agentModes["agent-1"])
	_, has2 := agentModes["agent-2"]
	assert.False(t, has2)
}

func TestSetAgentWatchesForSession_IgnoresSupersededSession(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	first := newTestWatcher("ch-1")
	second := newTestWatcher("ch-1")

	sid1 := m.BeginSession("ch-1")
	sid2 := m.BeginSession("ch-1")
	m.SetAgentWatchesForSession("ch-1", sid2, []watchEntry{{id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, second)
	// Late apply from the cancelled session must not re-register.
	m.SetAgentWatchesForSession("ch-1", sid1, []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, first)

	assert.Equal(t, 0, m.agents.count("agent-1"))
	assert.Equal(t, 1, m.agents.count("agent-2"))
}

func TestBroadcast_DropsWatcherOnSendError(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-dead")
	mock.failSends(errors.New("transport gone"))

	m.SetAgentWatches("ch-dead", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	// First broadcast hits the dead sender once.
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mock.streamCount.Load(), "expected 1 send attempt before invalidation")

	assert.Equal(t, 0, m.agents.count("agent-1"), "expected watcher to be removed after SendStream error")

	// Subsequent broadcasts must skip the dead watcher.
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mock.streamCount.Load(), "expected no further sends after invalidation")
}

func TestBroadcast_TerminalDropsWatcherOnSendError(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-dead")
	mock.failSends(errors.New("transport gone"))

	m.SetTerminalWatches("ch-dead", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	m.BroadcastTerminalEvent("term-1", testTerminalEvent("term-1", []byte("a")))
	assert.Equal(t, int64(1), mock.streamCount.Load())

	assert.Equal(t, 0, m.terminals.count("term-1"), "expected terminal watcher to be removed after SendStream error")

	m.BroadcastTerminalEvent("term-1", testTerminalEvent("term-1", []byte("b")))
	assert.Equal(t, int64(1), mock.streamCount.Load(), "expected no further sends after invalidation")
}

func TestBroadcast_DropsOnlyDeadWatcher(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mockDead := newTestWatcher("ch-dead")
	mockDead.failSends(errors.New("transport gone"))
	mockLive := newTestWatcher("ch-live")

	m.SetAgentWatches("ch-dead", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mockDead)
	m.SetAgentWatches("ch-live", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mockLive)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mockDead.streamCount.Load())
	assert.Equal(t, int64(1), mockLive.streamCount.Load())

	assert.Equal(t, 1, m.agents.count("agent-1"), "live watcher should remain registered")

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mockDead.streamCount.Load(), "dead watcher should not receive further events")
	assert.Equal(t, int64(2), mockLive.streamCount.Load(), "live watcher should receive subsequent events")
}

// TestBroadcast_KeepsWatcherWhenChannelRejectsOneMessage pins that a
// per-message rejection does NOT unsubscribe the client. The channel is
// healthy -- it refused this one payload for being unmarshalable or over
// the size cap -- so no transport error follows, which means nothing
// trips the frontend's reconnect. Retiring here would leave a live
// client silently receiving nothing for that entity until it happened to
// re-issue WatchEvents.
func TestBroadcast_KeepsWatcherWhenChannelRejectsOneMessage(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	mock.failSends(fmt.Errorf("message too large: 99 > 10: %w", channel.ErrMessageRejected))

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, 1, m.agents.count("agent-1"),
		"a rejected message must not unsubscribe a healthy client")

	// The subscription must still be usable once a sendable event comes.
	mock.failSends(nil)
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(2), mock.streamCount.Load(),
		"the surviving watcher must keep receiving later events")
}

// TestBroadcast_TerminalKeepsWatcherWhenChannelRejectsOneMessage is the
// terminal twin: a single oversized frame must not deafen the tab.
func TestBroadcast_TerminalKeepsWatcherWhenChannelRejectsOneMessage(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	mock.failSends(fmt.Errorf("message too large: 99 > 10: %w", channel.ErrMessageRejected))

	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	m.BroadcastTerminalEvent("term-1", testTerminalEvent("term-1", []byte("a")))

	assert.Equal(t, 1, m.terminals.count("term-1"),
		"a rejected frame must not unsubscribe a healthy terminal watcher")
}

// TestWatcher_ReSubscribeAfterInvalidate pins that a channel that lost
// its watcher to a SendStream failure can re-register on the same
// agent and receive subsequent broadcasts. Without this the registry
// slot would stay stuck closed and reconnect would silently keep
// missing events.
func TestWatcher_ReSubscribeAfterInvalidate(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()

	// First registration: send fails, watcher gets dropped.
	mockDead := newTestWatcher("ch-1")
	mockDead.failSends(errors.New("transport gone"))
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mockDead)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, 0, m.agents.count("agent-1"), "precondition: dead watcher should be dropped")

	// Re-subscribe on the same channel ID with a fresh sender.
	mockAlive := newTestWatcher("ch-1")
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mockAlive)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mockAlive.streamCount.Load(), "re-subscribed watcher should receive broadcasts")
}

// TestWatcher_InvalidateScopedToEntity pins the chosen semantic that a
// SendStream failure invalidates the watcher only for the failing
// entity, leaving the same channel's other-entity registrations intact
// AND functional. A future "drop the whole channel on first failure"
// change would surface here as a test failure rather than a behavior
// shift.
//
// The scope is deliberate: a send error is not necessarily channel-wide
// (see channel.ErrMessageRejected), so unsubscribing everything would
// punish a healthy client for one bad entity.
func TestWatcher_InvalidateScopedToEntity(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-multi")
	mock.failSends(errors.New("transport gone"))

	m.SetAgentWatches("ch-multi", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}, {id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	// First send to agent-1 fails — should drop the agent-1 registration
	// but leave agent-2's intact (same channel, same sender).
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, 0, m.agents.count("agent-1"), "agent-1 watcher should be dropped after its send failed")
	assert.Equal(t, 1, m.agents.count("agent-2"), "agent-2 watcher should remain registered")

	// With the transient error cleared, agent-2 broadcasts must reach
	// the surviving watcher — proving the registration isn't merely
	// present but still functional.
	mock.failSends(nil)
	beforeAgent2 := mock.streamCount.Load()
	m.BroadcastAgentEvent("agent-2", testAgentEvent("agent-2"))
	assert.Equal(t, beforeAgent2+1, mock.streamCount.Load(), "agent-2 broadcast should reach the surviving watcher")
}

// TestWatcher_AnotherChannelRegisteringDoesNotDisarmTheSweep pins that
// generations are per registration, not per registry-wide counter
// reading: a DIFFERENT channel subscribing mid-broadcast bumps nextGen,
// which must not make the failing channel's snapshot look stale.
//
// This is the surviving half of the defect that motivated the rewrite.
// The generation used to live on the caller-supplied watcher object,
// which WatchEvents registered under every requested entity, so each
// registration re-stamped the ones before it; the sweep then compared a
// stale snapshot against a bumped generation, concluded the failure
// belonged to a superseded registration, and left a genuinely dead one
// in place to fail again on every later event.
func TestWatcher_AnotherChannelRegisteringDoesNotDisarmTheSweep(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	mock.failSends(errors.New("transport gone"))
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	// Subscribe the other channel from inside the (unlocked) send loop so
	// the interleaving is deterministic rather than timing-dependent.
	other := newTestWatcher("ch-2")
	registerOther := func() {
		m.SetAgentWatches("ch-2", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, other)
	}
	mock.onSend.Store(&registerOther)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	mock.onSend.Store(nil)

	assert.Equal(t, []string{"ch-2"}, m.agents.channelIDs("agent-1"),
		"the dead channel must be retired even though another channel subscribed mid-broadcast")
}

// TestWatcher_TerminalRegistrationDoesNotDisarmTheAgentSweep is the
// cross-registry twin, and models the real handler shape: WatchEvents
// calls SetAgentWatches and SetTerminalWatches separately, so an agent
// broadcast can land between the two. The terminal call must not perturb
// the agent registration's generation.
func TestWatcher_TerminalRegistrationDoesNotDisarmTheAgentSweep(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	mock.failSends(errors.New("transport gone"))
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	registerTerminal := func() {
		m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	}
	mock.onSend.Store(&registerTerminal)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	mock.onSend.Store(nil)

	assert.Equal(t, 0, m.agents.count("agent-1"),
		"a terminal registration must not disarm the agent sweep")
	assert.Equal(t, 1, m.terminals.count("term-1"),
		"the terminal registration must survive")
}

func TestUnwatchAll_PreservesOtherChannels(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock1 := newTestWatcher("ch-1")
	mock2 := newTestWatcher("ch-2")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock1)
	m.SetAgentWatches("ch-2", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock2)
	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock1)
	m.SetTerminalWatches("ch-2", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock2)

	// Unwatch only ch-1.
	m.UnwatchAll("ch-1")

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	m.BroadcastTerminalEvent("term-1", testTerminalEvent("term-1", []byte("a")))

	assert.Equal(t, int64(0), mock1.streamCount.Load(), "ch-1: expected 0 broadcasts")
	assert.Equal(t, int64(2), mock2.streamCount.Load(), "ch-2: expected 2 broadcasts (agent+terminal)")
}

// TestWatcher_ResubscribeDuringBroadcastDoesNotRaceSender pins that the
// broadcast send loop never reads a registration's sender while a
// re-subscribe is replacing it. broadcast must run its sends OUTSIDE the
// registry lock (a SendStream can block on the transport, and the
// dead-watcher sweep afterwards takes the write lock), so the sender it
// uses has to be snapshotted under the read lock rather than read off
// the stored registration.
//
// sender is an interface -- two words, type descriptor plus data
// pointer -- so an unsynchronised read concurrent with a re-registration
// can tear and pair one implementation's type with another's pointer.
// Runs under -race, which is what actually catches the unsynchronised
// access; without -race this exercises the re-subscribe path and asserts
// every broadcast still lands somewhere.
func TestWatcher_ResubscribeDuringBroadcastDoesNotRaceSender(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	firstMock := newTestWatcher("ch-race")
	m.SetAgentWatches("ch-race", []watchEntry{{id: "agent-race", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, firstMock)

	const rounds = 200
	event := testAgentEvent("agent-race")

	// Each re-subscribe installs a DISTINCT mock, so a torn interface read
	// would pair one mock's type word with another's data pointer.
	mocks := make([]*mockResponseWriter, 0, rounds)
	resubscribed := make(chan struct{})
	go func() {
		defer close(resubscribed)
		for i := 0; i < rounds; i++ {
			nextMock := newTestWatcher("ch-race")
			mocks = append(mocks, nextMock)
			m.SetAgentWatches("ch-race", []watchEntry{{id: "agent-race", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, nextMock)
		}
	}()

	for i := 0; i < rounds; i++ {
		m.BroadcastAgentEvent("agent-race", event)
	}
	<-resubscribed

	delivered := firstMock.streamCount.Load()
	for _, mock := range mocks {
		delivered += mock.streamCount.Load()
	}
	assert.Equal(t, int64(rounds), delivered,
		"every broadcast must reach exactly one of the senders registered for the channel")
}

// TestWatcher_FailedSendDoesNotDropAFresherResubscribe pins that a
// SendStream failure retires only the registration that actually
// failed. The send loop runs unlocked, so a client that reconnects
// mid-broadcast can install a fresh sender between the snapshot and
// the send; the old sender's failure must not then unregister the new,
// live one. Dropping by channel ID alone did exactly that, leaving a
// just-reconnected client silently receiving nothing until it
// subscribed again.
//
// The re-subscribe is driven from inside SendStream so the
// interleaving is deterministic rather than timing-dependent.
func TestWatcher_FailedSendDoesNotDropAFresherResubscribe(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	staleMock := newTestWatcher("ch-1")
	staleMock.failSends(errors.New("transport gone"))
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, staleMock)

	freshMock := newTestWatcher("ch-1")
	resubscribe := func() {
		m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, freshMock)
	}
	staleMock.onSend.Store(&resubscribe)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, 1, m.agents.count("agent-1"),
		"the re-subscribed watcher must survive the stale sender's failure")

	// The surviving registration must be the FRESH one and still usable.
	staleMock.onSend.Store(nil)
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), freshMock.streamCount.Load(),
		"broadcasts after the failure must reach the re-subscribed sender")
	assert.Equal(t, int64(1), staleMock.streamCount.Load(),
		"the stale sender must not receive further broadcasts")
}

// TestWatcher_StaleFailureDoesNotDropAReusedChannelID pins that a
// generation is never reused, so a broadcast still in flight cannot
// retire a registration created after its own snapshot was taken.
//
// The interleaving: a broadcast snapshots a dying watcher, and while its
// (unlocked) send is running the channel is torn down and re-subscribed
// under the SAME channel ID. The sweep that follows must recognise the
// new registration as a different one and leave it alone. A counter that
// restarted per subscriber would collide with the stale snapshot;
// minting generations from the registry cannot.
func TestWatcher_StaleFailureDoesNotDropAReusedChannelID(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	staleMock := newTestWatcher("ch-1")
	staleMock.failSends(errors.New("transport gone"))
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, staleMock)

	freshMock := newTestWatcher("ch-1")
	teardownAndResubscribe := func() {
		m.UnwatchAll("ch-1")
		m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, freshMock)
	}
	staleMock.onSend.Store(&teardownAndResubscribe)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, 1, m.agents.count("agent-1"),
		"a registration created after the broadcast's snapshot must survive that broadcast's failure")

	staleMock.onSend.Store(nil)
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), freshMock.streamCount.Load(),
		"the re-subscribed sender must still receive broadcasts")
}

// TestBroadcast_DropsEverySimultaneouslyFailingWatcher pins the
// multi-failure path through retire: one sweep retires every channel
// that failed in the same broadcast, and leaves the survivors
// registered. TestBroadcast_DropsOnlyDeadWatcher covers the
// single-failure case; this one exercises the sweep with more than one
// entry, which is where a per-channel lookup could drop too few or too
// many.
func TestBroadcast_DropsEverySimultaneouslyFailingWatcher(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mockDeadA := newTestWatcher("ch-dead-a")
	mockDeadB := newTestWatcher("ch-dead-b")
	mockLive := newTestWatcher("ch-live")
	mockDeadA.failSends(errors.New("transport gone"))
	mockDeadB.failSends(errors.New("peer dropped"))

	m.SetAgentWatches("ch-dead-a", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mockDeadA)
	m.SetAgentWatches("ch-live", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mockLive)
	m.SetAgentWatches("ch-dead-b", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mockDeadB)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, []string{"ch-live"}, m.agents.channelIDs("agent-1"),
		"both failing channels must be retired in the same sweep, and only those")

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mockDeadA.streamCount.Load(), "ch-dead-a must not be sent to again")
	assert.Equal(t, int64(1), mockDeadB.streamCount.Load(), "ch-dead-b must not be sent to again")
	assert.Equal(t, int64(2), mockLive.streamCount.Load(), "the live channel keeps receiving")
}

// TestRetire_RemovesTheEntityEntryWhenItEmpties pins that retiring the
// last watcher for an entity deletes the entity's entry rather than
// leaving an empty inner map behind, so a long-lived worker doesn't
// accumulate one entry per entity it ever broadcast to.
func TestRetire_RemovesTheEntityEntryWhenItEmpties(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	mock.failSends(errors.New("transport gone"))
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.False(t, m.agents.hasEntity("agent-1"),
		"the entity entry must be deleted once its last watcher is retired")
}

// TestUnwatchAll_RemovesTheEntityEntryWhenItEmpties is the UnwatchAll
// twin of the test above.
func TestUnwatchAll_RemovesTheEntityEntryWhenItEmpties(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	m.UnwatchAll("ch-1")

	assert.False(t, m.agents.hasEntity("agent-1"),
		"the agent entry must be deleted once its last watcher leaves")
	assert.False(t, m.terminals.hasEntity("term-1"),
		"the terminal entry must be deleted once its last watcher leaves")
}

// TestSetAgentWatches_DropsEntitiesTheNewRequestOmits pins that a
// WatchEvents request states the channel's whole current interest, not
// an increment. Nothing else can retire the omitted entity: closing a
// stream is client-local and produces no frame the worker sees, so an
// additive registry kept shipping events for a tab nobody was listening
// to until the channel itself closed.
func TestSetAgentWatches_DropsEntitiesTheNewRequestOmits(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}, {id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	require.Equal(t, 1, m.agents.count("agent-2"), "precondition: both agents watched")

	// The tab for agent-2 closed; the client re-issues with the rest.
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	assert.Equal(t, 0, m.agents.count("agent-2"), "the omitted agent must be unsubscribed")
	m.BroadcastAgentEvent("agent-2", testAgentEvent("agent-2"))
	assert.Equal(t, int64(0), mock.streamCount.Load(), "the omitted agent must not broadcast")

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mock.streamCount.Load(), "the retained agent must still broadcast")
}

// TestSetTerminalWatches_DropsEntitiesTheNewRequestOmits is the terminal
// twin of the test above.
func TestSetTerminalWatches_DropsEntitiesTheNewRequestOmits(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}, {id: "term-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	assert.Equal(t, 0, m.terminals.count("term-2"), "the omitted terminal must be unsubscribed")
	assert.Equal(t, 1, m.terminals.count("term-1"), "the retained terminal stays")
}

// TestSetAgentWatches_LeavesOtherChannelsAlone pins that the prune is
// scoped to the calling channel: one client narrowing its tab set must
// not unsubscribe a different client watching the same agent.
func TestSetAgentWatches_LeavesOtherChannelsAlone(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mine := newTestWatcher("ch-1")
	theirs := newTestWatcher("ch-2")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}, {id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mine)
	m.SetAgentWatches("ch-2", []watchEntry{{id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, theirs)

	// ch-1 drops agent-2; ch-2 still wants it.
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mine)

	assert.Equal(t, []string{"ch-2"}, m.agents.channelIDs("agent-2"),
		"pruning one channel must not disturb another channel's subscription")

	m.BroadcastAgentEvent("agent-2", testAgentEvent("agent-2"))
	assert.Equal(t, int64(1), theirs.streamCount.Load(), "the other channel still receives")
	assert.Equal(t, int64(0), mine.streamCount.Load(), "the pruned channel does not")
}

// TestSetAgentWatches_EmptySetUnsubscribesEverything covers the boundary
// the prune loop makes reachable: a request naming no agents at all.
func TestSetAgentWatches_EmptySetUnsubscribesEverything(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}, {id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	m.SetAgentWatches("ch-1", nil, mock)

	assert.False(t, m.agents.hasEntity("agent-1"), "agent-1 entry must be gone")
	assert.False(t, m.agents.hasEntity("agent-2"), "agent-2 entry must be gone")
}

// TestSetAgentWatches_RepeatedIDRegistersOnce pins that a request naming
// the same agent twice yields one registration, not a duplicate that a
// later sweep would have to reconcile.
func TestSetAgentWatches_RepeatedIDRegistersOnce(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}, {id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)

	assert.Equal(t, 1, m.agents.count("agent-1"))
	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mock.streamCount.Load(), "one registration, one send")
}

// TestSetAgentWatches_SecondStreamOnSameChannelReplacesTheFirst pins the
// consequence of the invariant setWatches documents -- one live
// WatchEvents stream per channel id.
//
// It is a client-side discipline the registry cannot enforce, so this
// records what happens when it is broken: the second subscriber's set
// wins outright and the first is deafened on anything it uniquely held,
// with no error to either side. That is why a pooled cross-worker channel
// must not be shared by two subscriptions.
func TestSetAgentWatches_SecondStreamOnSameChannelReplacesTheFirst(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	firstStream := newTestWatcher("ch-1")
	secondStream := newTestWatcher("ch-1")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, firstStream)
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-2", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, secondStream)

	assert.False(t, m.agents.hasEntity("agent-1"),
		"the first stream's exclusive subscription is dropped, silently")
	m.BroadcastAgentEvent("agent-2", testAgentEvent("agent-2"))
	assert.Equal(t, int64(0), firstStream.streamCount.Load())
	assert.Equal(t, int64(1), secondStream.streamCount.Load())
}

// deadTransportWriter fails every SendStream with a transport-level
// error, and counts the attempts. The error is deliberately NOT
// channel.ErrMessageRejected: that sentinel means "this one message was
// refused", which a replay must survive rather than abandon.
type deadTransportWriter struct {
	mockResponseWriter
}

func newDeadTransportWriter(channelID string) *deadTransportWriter {
	w := &deadTransportWriter{mockResponseWriter{channelID: channelID}}
	w.failSends(errors.New("stream closed"))
	return w
}

// TestReplaySink_StopsSendingOnceTheTransportDies pins the abort the
// catch-up burst never had.
//
// A page refresh replays a CatchUpStart, a page of messages, a todo
// refresh, a status, every pending control request and a CatchUpComplete
// per agent. Every one of those discarded its send error, so a client
// that dropped at the start of the burst still had the worker marshal,
// encrypt and hand the whole thing to a transport that was already gone.
func TestReplaySink_StopsSendingOnceTheTransportDies(t *testing.T) {
	t.Parallel()

	w := newDeadTransportWriter("ch-1")
	sink := newReplaySink(w)

	for i := 0; i < 10; i++ {
		sink.send(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: testAgentEvent("agent-1")},
		})
	}

	assert.Equal(t, int64(1), w.streamCount.Load(),
		"the first failure must latch; the other nine sends never reach the transport")
	assert.False(t, sink.alive(), "and the sink reports the burst should be abandoned")
}

// TestReplaySink_KeepsGoingWhenOneMessageIsRejected pins the other side.
// An oversized event is the channel refusing ONE message on a healthy
// stream; abandoning the rest of a page refresh over it would lose every
// later message for no reason.
func TestReplaySink_KeepsGoingWhenOneMessageIsRejected(t *testing.T) {
	t.Parallel()

	w := newTestWatcher("ch-1")
	w.failSends(fmt.Errorf("message too large: %w", channel.ErrMessageRejected))
	sink := newReplaySink(w)

	for i := 0; i < 5; i++ {
		sink.send(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: testAgentEvent("agent-1")},
		})
	}

	assert.Equal(t, int64(5), w.streamCount.Load(),
		"a per-message rejection must not abandon the replay")
	assert.True(t, sink.alive())
}

// TestTransportDead_ClassifiesEachFailureKind pins the one place two
// callers agree on what a send error means. broadcast uses it to decide
// whether to retire a subscription; the replay sink uses it to decide
// whether to abandon a catch-up burst. Getting either wrong is silent:
// too eager deafens a live client, too lax keeps shipping into the void.
func TestTransportDead_ClassifiesEachFailureKind(t *testing.T) {
	t.Parallel()

	assert.False(t, transportDead(nil), "a successful send is not a dead transport")
	assert.False(t, transportDead(fmt.Errorf("too large: %w", channel.ErrMessageRejected)),
		"a refused message leaves the channel healthy")
	assert.False(t, transportDead(fmt.Errorf("bad envelope: %w", errEventNotMarshalable)),
		"an unencodable envelope never reached the transport")
	assert.True(t, transportDead(errors.New("stream closed")),
		"anything else means further sends are pointless")
}

// TestReplaySink_KeepsGoingWhenOneEventCannotBeMarshalled pins that a
// defect in one envelope costs that event and nothing more. Latching on
// it would drop the message page, the status and the control requests
// queued behind it, for a failure the client had no part in.
//
// The unencodable event is real, not simulated: proto3 string fields
// must hold valid UTF-8, so an agent id carrying a lone continuation
// byte fails proto.Marshal before anything reaches the transport.
func TestReplaySink_KeepsGoingWhenOneEventCannotBeMarshalled(t *testing.T) {
	t.Parallel()

	w := newTestWatcher("ch-1")
	sink := newReplaySink(w)

	unencodable := &leapmuxv1.AgentEvent{
		AgentId: string([]byte{0x80}),
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: string([]byte{0x80}),
			Status:  leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
		}},
	}
	require.Error(t, broadcastWatchEvent(w, &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: unencodable},
	}), "precondition: this envelope really is unencodable")
	before := w.streamCount.Load()

	sink.send(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: unencodable},
	})
	assert.True(t, sink.alive(), "an unencodable event must not abandon the burst")
	assert.Equal(t, before, w.streamCount.Load(), "and must not reach the transport")

	sink.send(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: testAgentEvent("agent-1")},
	})
	assert.Equal(t, before+1, w.streamCount.Load(), "the next event still goes out")
}

// TestRetire_DoesNotCleanUpAfterAnyoneElse pins the SCOPE of retire's
// entity-entry cleanup: it removes the entry only when its own work
// emptied the map.
//
// The empty-but-present map below is MANUFACTURED -- no production path
// leaves one, because setWatches, unwatchAll and retire all prune on
// empty. That is exactly the point. Ungated, the cleanup read as "delete
// whenever the map happens to be empty", which was correct only by
// agreement among three call sites and visible at none of them; gated on
// having dropped something, retire cleans up after itself and nothing
// else, so the next path that stops pruning cannot silently recruit this
// one to cover for it.
func TestRetire_DoesNotCleanUpAfterAnyoneElse(t *testing.T) {
	t.Parallel()

	r := newWatcherRegistry()
	r.mu.Lock()
	r.byEntity["e-1"] = map[string]registration{}
	r.mu.Unlock()

	// A failure that matches nothing, so retire drops nothing.
	r.retire("e-1", []registration{{channelID: "ch-gone", gen: 99}})

	assert.True(t, r.hasEntity("e-1"),
		"retire must not remove an entry it did not empty")
}

// TestRetire_DropsTheEntityOnceItsLastWatcherGoes is the other half: a
// retire that DID drop something cleans up after itself.
func TestBroadcast_ModeReregisterChangesDelivery(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, mock)
	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 1}},
	})
	assert.Equal(t, int64(0), mock.streamCount.Load(), "NOTIFY skips content")

	// Same channel, same entity, new mode — no extra registration.
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	assert.Equal(t, 1, m.agents.count("agent-1"))

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 2}},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load(), "FULL receives content after mode flip")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, mock)
	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 3}},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load(), "NOTIFY again skips content")
}

func TestBroadcast_ContentSkippedForNotifyOnlyRegistration(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, mock)

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 1}},
	})
	assert.Equal(t, int64(0), mock.streamCount.Load(), "content must not reach NOTIFY registration")

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))
	assert.Equal(t, int64(1), mock.streamCount.Load(), "notify must reach NOTIFY registration")
}

func TestBroadcast_ContentSelfGatesWithoutFullWatcher(t *testing.T) {
	t.Parallel()

	// The interest gate now lives in broadcast itself (not at each content
	// caller): when no channel holds the entity in FULL, a classContent event
	// must short-circuit before any snapshot/send — whether the audience is
	// NOTIFY-only, empty, or a mix. Pin the contract so a new content event
	// does not need to remember its own HasFull*Watcher guard.
	t.Run("NOTIFY-only audience skips content", func(t *testing.T) {
		t.Parallel()
		m := NewWatcherManager()
		notify := newTestWatcher("ch-1")
		m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, notify)
		m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
			AgentId: "agent-1",
			Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 1}},
		})
		assert.Equal(t, int64(0), notify.streamCount.Load(), "no FULL watcher → content must self-gate")
	})

	t.Run("empty audience skips content", func(t *testing.T) {
		t.Parallel()
		m := NewWatcherManager()
		// No registration at all — broadcast must be a quiet no-op.
		m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{
			TerminalId: "term-1",
			Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("x")}},
		})
	})

	t.Run("mixed audience delivers content only to FULL", func(t *testing.T) {
		t.Parallel()
		m := NewWatcherManager()
		notify := newTestWatcher("ch-notify")
		full := newTestWatcher("ch-full")
		m.SetAgentWatches("ch-notify", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, notify)
		m.SetAgentWatches("ch-full", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, full)
		m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
			AgentId: "agent-1",
			Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 1}},
		})
		assert.Equal(t, int64(0), notify.streamCount.Load(), "NOTIFY watcher must not receive content")
		assert.Equal(t, int64(1), full.streamCount.Load(), "FULL watcher receives content")
	})
}

func TestBroadcast_NotifyDeliveredToBothModes(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	notify := newTestWatcher("ch-notify")
	full := newTestWatcher("ch-full")

	m.SetAgentWatches("ch-notify", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, notify)
	m.SetAgentWatches("ch-full", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, full)

	m.BroadcastAgentEvent("agent-1", testAgentEvent("agent-1"))

	assert.Equal(t, int64(1), notify.streamCount.Load())
	assert.Equal(t, int64(1), full.streamCount.Load())
}

type payloadCountingWriter struct {
	mockResponseWriter
	payloadBytes atomic.Int64
}

func (m *payloadCountingWriter) SendStream(msg *leapmuxv1.InnerStreamMessage) error {
	m.payloadBytes.Add(int64(len(msg.GetPayload())))
	return m.mockResponseWriter.SendStream(msg)
}

func TestBroadcast_LazyMarshalSkipsWhenAllNotify(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := &payloadCountingWriter{mockResponseWriter: *newTestWatcher("ch-1")}

	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, mock)

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 1}},
	})

	assert.Equal(t, int64(0), mock.streamCount.Load())
	assert.Equal(t, int64(0), mock.payloadBytes.Load(), "content broadcast must not marshal when no FULL watcher exists")
}

func TestEventClassCoversEveryAgentOneofArm(t *testing.T) {
	t.Parallel()

	wantNotify := map[string]bool{
		"status_change":   true,
		"control_request": true,
		"control_cancel":  true,
		"turn_end":        true,
		"message_deleted": true,
		"todos_changed":   true,
	}

	msg := &leapmuxv1.AgentEvent{}
	desc := msg.ProtoReflect().Descriptor().Oneofs().ByName("event")
	require.NotNil(t, desc)
	for i := 0; i < desc.Fields().Len(); i++ {
		field := desc.Fields().Get(i)
		t.Run(string(field.Name()), func(t *testing.T) {
			ev := &leapmuxv1.AgentEvent{}
			ev.ProtoReflect().Set(field, ev.ProtoReflect().NewField(field))
			got := agentEventClass(ev)
			if wantNotify[string(field.Name())] {
				assert.Equal(t, classNotify, got)
			} else {
				assert.Equal(t, classContent, got)
			}
		})
	}
}

func TestBroadcast_MessageDeletedReachesNotifyWatcher(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, mock)

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_MessageDeleted{
			MessageDeleted: &leapmuxv1.AgentMessageDeleted{MessageId: "m1", Seq: 3},
		},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load(), "MessageDeleted is notify-class")

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 4}},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load(), "AgentMessage must stay content-only")
}

func TestBroadcast_TodosChangedReachesNotifyWatcher(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	m.SetAgentWatches("ch-1", []watchEntry{{id: "agent-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, mock)

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_TodosChanged{
			TodosChanged: &leapmuxv1.AgentTodosChanged{},
		},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load())
}

func TestHasFullTerminalWatcher(t *testing.T) {
	t.Parallel()

	m := NewWatcherManager()
	mock := newTestWatcher("ch-1")
	assert.False(t, m.HasFullTerminalWatcher("term-1"))

	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}, mock)
	assert.False(t, m.HasFullTerminalWatcher("term-1"))

	m.SetTerminalWatches("ch-1", []watchEntry{{id: "term-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, mock)
	assert.True(t, m.HasFullTerminalWatcher("term-1"))
}

func TestEventClassCoversEveryTerminalOneofArm(t *testing.T) {
	t.Parallel()

	msg := &leapmuxv1.TerminalEvent{}
	desc := msg.ProtoReflect().Descriptor().Oneofs().ByName("event")
	require.NotNil(t, desc)
	for i := 0; i < desc.Fields().Len(); i++ {
		field := desc.Fields().Get(i)
		t.Run(string(field.Name()), func(t *testing.T) {
			ev := &leapmuxv1.TerminalEvent{}
			ev.ProtoReflect().Set(field, ev.ProtoReflect().NewField(field))
			_ = terminalEventClass(ev)
		})
	}
}

func TestRetire_DropsTheEntityOnceItsLastWatcherGoes(t *testing.T) {
	t.Parallel()

	r := newWatcherRegistry()
	w := newTestWatcher("ch-1")
	r.setWatches("ch-1", []watchEntry{{id: "e-1", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, w)

	live := r.snapshot("e-1")
	require.Len(t, live, 1)
	r.retire("e-1", live)

	assert.Equal(t, 0, r.count("e-1"))
	assert.False(t, r.hasEntity("e-1"),
		"the entity entry goes with its last registration")
}
