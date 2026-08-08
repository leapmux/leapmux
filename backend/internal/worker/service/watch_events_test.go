package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func lastWatchUpdateAck(t *testing.T, w *testResponseWriter) *leapmuxv1.WatchUpdateAck {
	t.Helper()
	var last *leapmuxv1.WatchUpdateAck
	for _, s := range w.streamsSnapshot() {
		if s.GetIsError() {
			continue
		}
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		if ack := resp.GetUpdateAck(); ack != nil {
			last = ack
		}
	}
	return last
}

func streamEndedWithError(w *testResponseWriter) bool {
	for _, s := range w.streamsSnapshot() {
		if s.GetIsError() {
			return true
		}
	}
	return false
}

func waitWatchUpdateAck(t *testing.T, w *testResponseWriter) *leapmuxv1.WatchUpdateAck {
	t.Helper()
	var ack *leapmuxv1.WatchUpdateAck
	require.Eventually(t, func() bool {
		ack = lastWatchUpdateAck(t, w)
		return ack != nil
	}, time.Second, 10*time.Millisecond)
	return ack
}

func waitWatchUpdateAckWhere(t *testing.T, w *testResponseWriter, pred func(*leapmuxv1.WatchUpdateAck) bool) *leapmuxv1.WatchUpdateAck {
	t.Helper()
	var ack *leapmuxv1.WatchUpdateAck
	require.Eventually(t, func() bool {
		ack = lastWatchUpdateAck(t, w)
		return ack != nil && pred(ack)
	}, time.Second, 10*time.Millisecond)
	return ack
}

func waitAgentWatchCount(t *testing.T, svc *Service, agentID string, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return svc.Watchers.agents.count(agentID) == want
	}, time.Second, 10*time.Millisecond)
}

// waitAgentWatchLive / waitTerminalWatchLive are the waitXWatchCount pair for a
// caller that is NOT the test goroutine: the close-during-startup tests
// subscribe from inside startAgentFn / startTerminalFn. assert rather than
// require, because FailNow off the test goroutine is undefined.
//
// They are load-bearing rather than defensive. handleWatchEvents returns once
// the request is SUBMITTED and the session goroutine registers the watch
// afterwards, so a test that subscribes and then triggers a broadcast can miss
// it entirely -- and a test whose assertion is "this event must NEVER arrive"
// would then pass without having observed anything at all.
func waitAgentWatchLive(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	assert.Eventually(t, func() bool {
		return svc.Watchers.agents.count(agentID) == 1
	}, time.Second, 10*time.Millisecond,
		"agent watch never registered; the broadcast assertions cannot mean anything")
}

func waitTerminalWatchLive(t *testing.T, svc *Service, termID string) {
	t.Helper()
	assert.Eventually(t, func() bool {
		return svc.Watchers.terminals.count(termID) == 1
	}, time.Second, 10*time.Millisecond,
		"terminal watch never registered; the broadcast assertions cannot mean anything")
}

func waitTerminalWatchCount(t *testing.T, svc *Service, termID string, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return svc.Watchers.terminals.count(termID) == want
	}, time.Second, 10*time.Millisecond)
}

func streamEnded(w *testResponseWriter) bool {
	for _, s := range w.streamsSnapshot() {
		if s.GetEnd() {
			return true
		}
	}
	return false
}

func TestWatchEvents_OpeningRequestRegistersAndAcks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)

	assert.False(t, streamEndedWithError(w))
	ack := waitWatchUpdateAck(t, w)
	require.NotNil(t, ack)
	waitAgentWatchCount(t, svc, "agent-1", 1)
}

func TestWatchEvents_PartialRejectionAcksAndKeepsStream(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-good", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: "agent-good", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL},
			{AgentId: "agent-bad", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL},
		},
	}, w)

	ack := waitWatchUpdateAck(t, w)
	require.NotNil(t, ack)
	require.Len(t, ack.GetRejectedAgents(), 1)
	assert.Equal(t, "agent-bad", ack.GetRejectedAgents()[0].GetEntityId())
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND,
		ack.GetRejectedAgents()[0].GetReason())
	waitAgentWatchCount(t, svc, "agent-good", 1)
	assert.Equal(t, 0, svc.Watchers.agents.count("agent-bad"))
	assert.False(t, streamEndedWithError(w), "stream must stay open after partial rejection")
	assert.False(t, streamEnded(w), "stream must stay open after partial rejection")
}

func TestWatchEvents_AgentLookupFailureLeavesRegistrationsUntouched(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "term-1", WorkingDir: "/tmp", HomeDir: "/tmp", Cols: 80, Rows: 24, Screen: []byte("s"),
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)
	waitTerminalWatchCount(t, svc, "term-1", 1)

	_, err := svc.DB.Exec("DROP TABLE agents")
	require.NoError(t, err)

	payload, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}, {AgentId: "agent-2"}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1"}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(payload, false)

	ack := waitWatchUpdateAckWhere(t, w, func(a *leapmuxv1.WatchUpdateAck) bool {
		return len(a.GetRejectedAgents()) == 2
	})
	for _, r := range ack.GetRejectedAgents() {
		assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED, r.GetReason())
	}
	assert.Equal(t, 1, svc.Watchers.agents.count("agent-1"), "agent registrations unchanged on lookup failure")
	assert.Same(t, w, svc.Watchers.agents.senderFor("agent-1", testChannelID),
		"agent registration must stay byte-identical (same sender) on lookup failure")
	assert.Equal(t, 1, svc.Watchers.terminals.count("term-1"), "terminal half still applied")
}

func TestWatchEvents_EmptyRequestClearsAndAcks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)

	payload, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{})
	require.NoError(t, err)
	w.deliverStreamRequest(payload, false)

	require.Eventually(t, func() bool {
		return !svc.Watchers.agents.hasEntity("agent-1")
	}, time.Second, 10*time.Millisecond)
	ack := waitWatchUpdateAck(t, w)
	require.NotNil(t, ack)
	assert.Empty(t, ack.GetRejectedAgents())
	assert.False(t, streamEndedWithError(w))
}

func TestWatchEvents_CoalescedRevisionsApplyNewest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-2", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)

	first, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
	})
	require.NoError(t, err)
	second, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 2,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-2", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(first, false)
	w.deliverStreamRequest(second, false)

	require.Eventually(t, func() bool {
		ack := lastWatchUpdateAck(t, w)
		return ack != nil && ack.GetUpdateId() == 2
	}, time.Second, 10*time.Millisecond)

	assert.Equal(t, 0, svc.Watchers.agents.count("agent-1"))
	assert.Equal(t, 1, svc.Watchers.agents.count("agent-2"))
}

func TestWatchEvents_CancelFrameUnwatchesAndEnds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)

	w.deliverStreamRequest(nil, true)

	require.Eventually(t, func() bool {
		return !svc.Watchers.agents.hasEntity("agent-1") && streamEnded(w)
	}, time.Second, 10*time.Millisecond)
}

func TestWorkerPrivateEvents_CancelReleasesSubscriber(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)

	done := make(chan struct{})
	go func() {
		dispatch(d, "WatchWorkerPrivateEvents", &leapmuxv1.WatchWorkerPrivateEventsRequest{}, w)
		close(done)
	}()

	require.Eventually(t, func() bool {
		svc.PrivateEvents.mu.Lock()
		defer svc.PrivateEvents.mu.Unlock()
		return len(svc.PrivateEvents.subscribers["user-1"]) == 1
	}, time.Second, 10*time.Millisecond, "precondition: one subscriber registered")

	w.deliverStreamRequest(nil, true)
	<-done

	require.Eventually(t, func() bool {
		svc.PrivateEvents.mu.Lock()
		defer svc.PrivateEvents.mu.Unlock()
		_, ok := svc.PrivateEvents.subscribers["user-1"]
		return !ok
	}, time.Second, 10*time.Millisecond, "cancel must release the subscriber")

	w2 := newTestWriter()
	go dispatch(d, "WatchWorkerPrivateEvents", &leapmuxv1.WatchWorkerPrivateEventsRequest{}, w2)

	require.Eventually(t, func() bool {
		svc.PrivateEvents.mu.Lock()
		defer svc.PrivateEvents.mu.Unlock()
		return len(svc.PrivateEvents.subscribers["user-1"]) == 1
	}, time.Second, 10*time.Millisecond, "re-subscribe must register exactly one subscriber")

	svc.PrivateEvents.PublishTabRenamed(userid.MustNew("user-1"), "tab-1",
		leapmuxv1.TabType_TAB_TYPE_AGENT, "renamed", "")

	require.Eventually(t, func() bool {
		count := 0
		for _, s := range w2.streamsSnapshot() {
			if len(s.GetPayload()) > 0 {
				count++
			}
		}
		return count == 1
	}, time.Second, 10*time.Millisecond, "one live subscriber must receive exactly one event")
}

func TestWatchEvents_UndecodableRevisionSurvives(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)

	w.deliverStreamRequest([]byte{0xff, 0xff}, false)

	assert.Equal(t, 1, svc.Watchers.agents.count("agent-1"), "garbage revision must not kill subscription")
	assert.False(t, streamEndedWithError(w))
}

func countCatchUpCompletes(w *testResponseWriter) int {
	n := 0
	for _, e := range decodeAgentEvents(w) {
		if e.GetCatchUpComplete() != nil {
			n++
		}
	}
	return n
}

func TestWatchEvents_PromoteNotifyToFullReplaysOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)
	assert.Equal(t, 0, countCatchUpCompletes(w), "NOTIFY open must not catch-up replay")

	promote, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(promote, false)

	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) == 1
	}, time.Second, 10*time.Millisecond, "NOTIFY→FULL must replay exactly once")

	restatement, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 2,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(restatement, false)
	waitWatchUpdateAckWhere(t, w, func(a *leapmuxv1.WatchUpdateAck) bool {
		return a.GetUpdateId() == 2
	})
	assert.Equal(t, 1, countCatchUpCompletes(w), "identical FULL restatement must not replay again")
}

func TestWatchEvents_DemoteThenRepromoteReplaysAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)
	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) == 1
	}, time.Second, 10*time.Millisecond)

	demote, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(demote, false)
	waitWatchUpdateAckWhere(t, w, func(a *leapmuxv1.WatchUpdateAck) bool {
		return a.GetUpdateId() == 1
	})
	assert.Equal(t, 1, countCatchUpCompletes(w), "demotion must not replay")

	repromote, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 2,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(repromote, false)
	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) == 2
	}, time.Second, 10*time.Millisecond, "demote→re-promote must replay again")
}

// TestWatchEvents_DualPromote_TerminalCatchUpBeforeAgent pins that when an
// agent and a terminal promote together, terminal screen restore is not
// HOL-blocked behind agent message replay.
func TestWatchEvents_DualPromote_TerminalCatchUpBeforeAgent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-dual", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	startTestTerminal(t, svc, ctx, "term-dual")
	require.True(t, svc.Terminals.AppendOutput("term-dual", []byte("screen")))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-dual", Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-dual", Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-dual", 1)
	waitTerminalWatchCount(t, svc, "term-dual", 1)

	promote, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-dual", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{
			TerminalId: "term-dual", AfterOffset: 0, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL,
		}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(promote, false)

	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	termIdx, agentStartIdx := -1, -1
	for i, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		if te := resp.GetTerminalEvent(); te != nil && te.GetTerminalId() == "term-dual" && te.GetData() != nil && termIdx < 0 {
			termIdx = i
		}
		if ae := resp.GetAgentEvent(); ae != nil && ae.GetAgentId() == "agent-dual" && ae.GetCatchUpStart() != nil && agentStartIdx < 0 {
			agentStartIdx = i
		}
	}
	require.GreaterOrEqual(t, termIdx, 0, "expected terminal catch-up data")
	require.GreaterOrEqual(t, agentStartIdx, 0, "expected agent CatchUpStart")
	assert.Less(t, termIdx, agentStartIdx, "terminal catch-up must precede agent catch-up on dual promote")
}

func TestWatchEvents_LookupFailedRetryPerformsOwedReplay(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)
	assert.Equal(t, 0, countCatchUpCompletes(w))

	_, err := svc.DB.Exec("ALTER TABLE agents RENAME TO agents_bak")
	require.NoError(t, err)

	failPromote, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(failPromote, false)
	ack := waitWatchUpdateAckWhere(t, w, func(a *leapmuxv1.WatchUpdateAck) bool {
		return a.GetUpdateId() == 1 && len(a.GetRejectedAgents()) == 1
	})
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
		ack.GetRejectedAgents()[0].GetReason())
	assert.Equal(t, 0, countCatchUpCompletes(w), "LOOKUP_FAILED must not advance mode / replay")
	assert.Same(t, w, svc.Watchers.agents.senderFor("agent-1", testChannelID))

	_, err = svc.DB.Exec("ALTER TABLE agents_bak RENAME TO agents")
	require.NoError(t, err)

	retry, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 2,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(retry, false)
	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) == 1
	}, time.Second, 10*time.Millisecond, "successful retry must perform the owed FULL replay")
}

func TestWatchEvents_ChannelCloseUnwatches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)

	// Channel close releases stream controllers via OnCancel (same path as
	// releaseAll in HandleClose / CloseAll). The bootstrap UnwatchAll
	// callback is gone; this is the coverage for that teardown.
	w.deliverStreamRequest(nil, true)

	require.Eventually(t, func() bool {
		return !svc.Watchers.agents.hasEntity("agent-1")
	}, time.Second, 10*time.Millisecond)
}

// bindRefusingWriter wraps testResponseWriter but refuses BindStream, modelling
// a transport that has no revise/cancel path for the stream.
type bindRefusingWriter struct{ *testResponseWriter }

func (bindRefusingWriter) BindStream(channel.StreamController) (func(), bool) {
	return func() {}, false
}

// TestWatchEvents_BindStreamRefusalReleasesOwnership pins the fix for the
// BeginSession/bgCtx leak: when BindStream returns !ok the handler must retire
// the ownership newWatchSession just claimed, so a successor can claim the
// channel and no goroutine/ctx is orphaned.
func TestWatchEvents_BindStreamRefusalReleasesOwnership(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	refusing := &bindRefusingWriter{testResponseWriter: w}
	payload, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
		Method: "WatchEvents", Payload: payload,
	}, refusing)

	// The handler must have surfaced a FailedPrecondition error and NOT left a
	// watch registration behind.
	require.True(t, streamEndedWithError(w), "BindStream refusal must surface a stream error")

	// Ownership must be released: channelOwners must not pin the dead session,
	// so a subsequent genuine subscribe on the same channelID registers cleanly.
	svc.Watchers.ownerMu.Lock()
	_, claimed := svc.Watchers.channelOwners[w.channelID]
	svc.Watchers.ownerMu.Unlock()
	require.False(t, claimed, "channel ownership must be released when BindStream refuses")
	waitAgentWatchCount(t, svc, "agent-1", 0)
}

// TestWatchEvents_PromoteAfterCancelSkipsReplay pins the ownership re-check
// before the catch-up burst: a promote revision racing a cancel must NOT ship a
// catch-up replay to a retired session. Without the re-check the replay loop
// only gated sends on transport-death, not ownership, so a superseded session
// could emit a full catch-up burst the client no longer reads.
func TestWatchEvents_PromoteAfterCancelSkipsReplay(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)
	require.Equal(t, 0, countCatchUpCompletes(w))

	// Promote to FULL, then cancel the stream before the replay can land. The
	// ownership re-check in apply must suppress the catch-up burst.
	promote, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents:   []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(promote, false)
	w.deliverStreamRequest(nil, true) // cancel retires ownership via OnCancel

	// Give the apply goroutine a moment to observe the cancel; the replay must
	// be suppressed (no CatchUpComplete for the retired session).
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, countCatchUpCompletes(w),
		"a promote racing a cancel must not ship a catch-up replay")
}

// TestReplayIncludesBackgroundTasksSnapshot pins the background-task leg of
// replayAgentCatchUp: a tab that watches an agent in FULL mode must receive an
// AgentBackgroundTasksChanged event carrying the registry snapshot for the
// agent's root owner. The registry is keyed by root owner id (a child tab ships
// its root's registry under the root id), so the replay's AgentId is the root
// even when a child is watched -- but here a root is watched directly, so the
// event's AgentId equals the watched root.
func TestReplayIncludesBackgroundTasksSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	// Seed two background-task rows on the root via the OutputHandler's upsert
	// path -- the same path providers use at runtime.
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-a", Kind: bgtask.KindSubagent, Title: "alpha", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-b", Kind: bgtask.KindShell, Title: "beta", Status: bgtask.StatusPending,
	}))

	// Watch the root in FULL mode: this triggers replayAgentCatchUp, which must
	// emit a BackgroundTasksChanged event alongside the message/todo/status legs.
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "root-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)

	// The replay must converge.
	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) == 1
	}, time.Second, 10*time.Millisecond, "FULL watch must drive exactly one catch-up replay")
	assert.False(t, streamEndedWithError(w), "watch replay must not surface a stream error")

	// Find the BackgroundTasksChanged event in the replay burst and assert it
	// carries the seeded snapshot under the root's id.
	var bgEvent *leapmuxv1.AgentBackgroundTasksChanged
	for _, e := range decodeAgentEvents(w) {
		if changed := e.GetBackgroundTasksChanged(); changed != nil {
			bgEvent = changed
			break
		}
	}
	require.NotNil(t, bgEvent, "replay must include an AgentBackgroundTasksChanged event")
	assert.Equal(t, "root-1", bgEvent.GetAgentId(),
		"snapshot event must be keyed by the watched root's id")

	keys := make(map[string]*leapmuxv1.BackgroundTaskItem, len(bgEvent.GetTasks()))
	for _, tk := range bgEvent.GetTasks() {
		keys[tk.GetId()] = tk
	}
	require.Contains(t, keys, "task-a")
	assert.Equal(t, "alpha", keys["task-a"].GetTitle())
	require.Contains(t, keys, "task-b")
	assert.Equal(t, "beta", keys["task-b"].GetTitle())
}

// TestWatchChildAgentReplaysWithoutProcess pins that watching a CHILD agent
// (parent_agent_id set) replays its messages WITHOUT requiring a process.
// Children are tabless virtual transcripts fed by their root's process, so they
// never own one; the catch-up must succeed (no "agent not running" stream error)
// and must not attempt to start/resume a process for the child. The child's
// status is derived from agentProcessRunning, which resolves the root -- here
// the root is NOT running, so the child replays as INACTIVE and that is the
// expected, non-error outcome.
func TestWatchChildAgentReplaysWithoutProcess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	// Root + child, linked via the OutputHandler's EnsureChildAgent (the same
	// path providers use). The child row carries parent_agent_id = root-1.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-2",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	rootSink := svc.Output.NewSink("root-2", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	childID, err := rootSink.EnsureChildAgent("spawn-span-watch", "row-key-watch", "child task")
	require.NoError(t, err)
	require.NotEmpty(t, childID)
	// The root is NOT registered with the agent manager, so agentProcessRunning
	// resolves to false for both root and child. This is the condition under
	// which a non-child watch previously had to guard against "agent not
	// running"; a child must never hit that path.

	// Seed one message into the child transcript so the replay has something to
	// ship and we can confirm the replay actually ran (not just an empty
	// catch-up).
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "child-msg-1",
		AgentID:       childID,
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte("hello from child"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	})
	require.NoError(t, err)

	// Watch the CHILD in FULL mode. The replay must succeed without a process.
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: childID, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)

	// The child's catch-up must converge and must NOT surface a stream error --
	// the invariant: watching a child does not error on "agent not running" and
	// does not attempt to start/resume a process for the child.
	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) == 1
	}, time.Second, 10*time.Millisecond, "child FULL watch must drive exactly one catch-up replay")
	assert.False(t, streamEndedWithError(w),
		"watching a child must not surface a stream error (children have no process)")

	// The replayed message reached the watcher, proving the replay ran for the
	// child transcript rather than short-circuiting on a missing process.
	var sawChildMessage bool
	for _, e := range decodeAgentEvents(w) {
		if am := e.GetAgentMessage(); am != nil && am.GetId() == "child-msg-1" {
			sawChildMessage = true
			break
		}
	}
	assert.True(t, sawChildMessage, "child replay must ship the child transcript's messages")

	// And no process was registered for the child -- the precondition that
	// "watching a child does not attempt to start/resume a process" holds.
	assert.False(t, svc.Agents.HasAgent(childID),
		"a child watch must not register a process for the child")
	assert.False(t, svc.Agents.HasAgent("root-2"),
		"a child watch must not register a process for the root either")
}
