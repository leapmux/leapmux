package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestListAgentMessages_AnchorPaging exercises the four MessagePageAnchor modes
// of ListAgentMessages. The response is always ordered ascending by seq and
// has_more reports whether further messages exist in the page's direction.
func TestListAgentMessages_AnchorPaging(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))

	// Seed five messages; capture their assigned (ascending) seqs.
	var seqs []int64
	for i := 0; i < 5; i++ {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            fmt.Sprintf("msg-%d", i+1),
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:       []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     time.Now(),
		})
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}

	list := func(req *leapmuxv1.ListAgentMessagesRequest) *leapmuxv1.ListAgentMessagesResponse {
		w := newTestWriter()
		dispatch(d, "ListAgentMessages", req, w)
		require.Len(t, w.responses, 1)
		var resp leapmuxv1.ListAgentMessagesResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		return &resp
	}
	gotSeqs := func(resp *leapmuxv1.ListAgentMessagesResponse) []int64 {
		out := make([]int64, 0, len(resp.GetMessages()))
		for _, m := range resp.GetMessages() {
			out = append(out, m.GetSeq())
		}
		return out
	}

	// LATEST: the newest page, returned ascending.
	resp := list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: "agent-1",
		Anchor:  leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST,
		Limit:   2,
	})
	assert.Equal(t, []int64{seqs[3], seqs[4]}, gotSeqs(resp))
	assert.True(t, resp.GetHasMore())

	// OLDEST: the earliest page, ascending.
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: "agent-1",
		Anchor:  leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST,
		Limit:   2,
	})
	assert.Equal(t, []int64{seqs[0], seqs[1]}, gotSeqs(resp))
	assert.True(t, resp.GetHasMore())

	// AFTER cursor=seqs[1]: the next two, ascending.
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   "agent-1",
		Anchor:    leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER,
		CursorSeq: seqs[1],
		Limit:     2,
	})
	assert.Equal(t, []int64{seqs[2], seqs[3]}, gotSeqs(resp))
	assert.True(t, resp.GetHasMore())

	// BEFORE cursor=seqs[3]: the two preceding, ascending (reversed from DESC).
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   "agent-1",
		Anchor:    leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE,
		CursorSeq: seqs[3],
		Limit:     2,
	})
	assert.Equal(t, []int64{seqs[1], seqs[2]}, gotSeqs(resp))
	assert.True(t, resp.GetHasMore())

	// A limit above the total returns the whole history with has_more=false.
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: "agent-1",
		Anchor:  leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST,
		Limit:   10,
	})
	assert.Equal(t, seqs, gotSeqs(resp))
	assert.False(t, resp.GetHasMore())

	// Defensive cursor handling: the CLI/frontend never send a non-positive
	// cursor for AFTER/BEFORE, but a misbehaving caller must get a safe boundary
	// page, not a malformed query.
	//
	// AFTER cursor=0: `seq > 0` returns from the oldest message (the natural
	// "after nothing").
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   "agent-1",
		Anchor:    leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER,
		CursorSeq: 0,
		Limit:     2,
	})
	assert.Equal(t, []int64{seqs[0], seqs[1]}, gotSeqs(resp))

	// BEFORE cursor=0: nothing precedes the first seq, so an empty page.
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   "agent-1",
		Anchor:    leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE,
		CursorSeq: 0,
		Limit:     2,
	})
	assert.Empty(t, gotSeqs(resp))

	// A negative cursor is clamped to 0 server-side rather than feeding the query
	// a negative bound, so AFTER -5 behaves like AFTER 0 (from the oldest).
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   "agent-1",
		Anchor:    leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER,
		CursorSeq: -5,
		Limit:     2,
	})
	assert.Equal(t, []int64{seqs[0], seqs[1]}, gotSeqs(resp))
}

// TestListAgentMessages_ShipsTodosOnDefaultAnchor asserts the cold-start to-do
// snapshot ships on the proto-default (UNSPECIFIED) anchor -- which resolves to
// the LATEST page -- not only on an explicit LATEST. A scroll page (AFTER) still
// omits it (the client already holds the snapshot and gets live updates via
// AgentTodosChanged).
func TestListAgentMessages_ShipsTodosOnDefaultAnchor(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-1",
		AgentID:       "agent-1",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte("hi"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Queries.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
		AgentID: "agent-1", RowKey: "k1", Seq: 1, TaskID: "t1",
		Content: "Run tests", ActiveForm: "Running tests", Status: "in_progress",
	}))

	list := func(req *leapmuxv1.ListAgentMessagesRequest) *leapmuxv1.ListAgentMessagesResponse {
		w := newTestWriter()
		dispatch(d, "ListAgentMessages", req, w)
		require.Len(t, w.responses, 1)
		var resp leapmuxv1.ListAgentMessagesResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		return &resp
	}

	// UNSPECIFIED (anchor left unset) resolves to the LATEST page, so it ships todos.
	// TodosLoaded is true so the client treats the snapshot as authoritative.
	resp := list(&leapmuxv1.ListAgentMessagesRequest{AgentId: "agent-1", Limit: 10})
	require.Len(t, resp.GetTodos(), 1)
	assert.Equal(t, "Run tests", resp.GetTodos()[0].GetContent())
	assert.True(t, resp.GetTodosLoaded())
	// The authoritative live-tail seq is carried so the --follow CLI can resume after
	// it instead of inferring a spurious 0 from an empty page (see resumeCursorFor).
	assert.Equal(t, int64(1), resp.GetLatestSeq())

	// A scroll page (AFTER) does not re-ship the snapshot, and TodosLoaded is false
	// so the client leaves its existing to-do list intact (rather than treating the
	// absent snapshot as an authoritative empty list and wiping it).
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   "agent-1",
		Anchor:    leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER,
		CursorSeq: 0,
		Limit:     10,
	})
	assert.Empty(t, resp.GetTodos())
	assert.False(t, resp.GetTodosLoaded())

	// An UNKNOWN anchor (a newer/forward-compat client) resolves to the LATEST
	// page via resolveMessagePage's default, so it must ship todos too. This holds
	// because isLatestPage is derived from the resolved plan (plan.mode ==
	// messagePageLatest), not from matching the raw anchor against named constants.
	resp = list(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: "agent-1",
		Anchor:  leapmuxv1.MessagePageAnchor(99),
		Limit:   10,
	})
	require.Len(t, resp.GetTodos(), 1)
	assert.True(t, resp.GetTodosLoaded())
	assert.Len(t, resp.GetMessages(), 1) // latest page returned, not empty
}

// TestWatchEvents_ReplaysLatestPageForFreshSubscriber asserts that a fresh
// WatchEvents subscriber gets the LATEST page replayed, not the OLDEST. The
// windowing client loads the latest page itself via ListAgentMessages(LATEST);
// replaying the oldest page here would splice the first messages in front of
// that latest window and tear a gap into the loaded history (the missing-chunk
// bug).
func TestWatchEvents_ReplaysLatestPageForFreshSubscriber(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))

	// Seed 60 messages so the latest 50 differ from the oldest 50.
	var seqs []int64
	for i := 0; i < 60; i++ {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            fmt.Sprintf("msg-%d", i+1),
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:       []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     time.Now(),
		})
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}

	// A fresh subscriber that REQUESTS the latest page, and one that OMITS the
	// replay field (UNSPECIFIED -- the proto3 default a client sends when it leaves
	// the field unset), must BOTH get the latest page: the handler routes anything
	// other than AFTER_CURSOR (including UNSPECIFIED) through its LATEST default
	// branch. Replaying the oldest page instead would tear the missing-chunk gap.
	for _, tc := range []struct {
		name   string
		replay leapmuxv1.WatchReplayMode
	}{
		{"explicit LATEST", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST},
		{"unset defaults to LATEST", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_UNSPECIFIED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wWatch := newTestWriter()
			dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
				Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Replay: tc.replay}},
			}, wWatch)

			collectReplayedSeqs := func() []int64 {
				var out []int64
				for _, s := range wWatch.streamsSnapshot() {
					var resp leapmuxv1.WatchEventsResponse
					if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
						continue
					}
					if am := resp.GetAgentEvent().GetAgentMessage(); am != nil {
						out = append(out, am.GetSeq())
					}
				}
				return out
			}

			var replayed []int64
			require.Eventually(t, func() bool {
				replayed = collectReplayedSeqs()
				return len(replayed) >= 50
			}, 5*time.Second, 20*time.Millisecond, "expected the latest 50 messages replayed")

			// The LATEST 50 (seqs[10:60]), ascending -- NOT the oldest 50.
			require.Len(t, replayed, 50)
			assert.Equal(t, seqs[10:60], replayed)
			assert.NotContains(t, replayed, seqs[0], "the oldest message must not be replayed to a fresh subscriber")
		})
	}
}

// TestWatchEvents_ResumeReplaysForwardPageFromCursor asserts that an AFTER_CURSOR
// subscriber gets the FIRST 50 messages after its cursor, ascending -- NOT the
// latest page. When the gap from the cursor to the live tail exceeds 50, only the
// first 50 replay here; the windowing client closes the remainder via
// catchUpToTail. This is the contract that keeps large reconnect gaps from
// over-replaying, and that catch-up -- not WatchEvents -- fills a >50 gap.
func TestWatchEvents_ResumeReplaysForwardPageFromCursor(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))

	// Seed 60 messages so the cursor-to-tail gap (54) exceeds the 50-row replay cap.
	var seqs []int64
	for i := 0; i < 60; i++ {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            fmt.Sprintf("msg-%d", i+1),
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:       []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     time.Now(),
		})
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}

	// Resume from the 6th message's seq (cursor = seqs[5]).
	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, CursorSeq: seqs[5]}},
	}, wWatch)

	collectReplayedSeqs := func() []int64 {
		var out []int64
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			if am := resp.GetAgentEvent().GetAgentMessage(); am != nil {
				out = append(out, am.GetSeq())
			}
		}
		return out
	}

	var replayed []int64
	require.Eventually(t, func() bool {
		replayed = collectReplayedSeqs()
		return len(replayed) >= 50
	}, 5*time.Second, 20*time.Millisecond, "expected the first 50 messages after the cursor replayed")

	// The first 50 with seq > cursor (seqs[6:56]), ascending -- not the cursor row,
	// and NOT the latest tail (seqs[56:60]), which catch-up fills instead.
	require.Len(t, replayed, 50)
	assert.Equal(t, seqs[6:56], replayed)
	assert.NotContains(t, replayed, seqs[5], "the cursor row must not be replayed")
	assert.NotContains(t, replayed, seqs[59], "the live tail beyond the 50-row page is left to catch-up")
}

// decodeAgentEvents decodes the ordered WatchEvents stream frames into AgentEvents.
func decodeAgentEvents(w *testResponseWriter) []*leapmuxv1.AgentEvent {
	var out []*leapmuxv1.AgentEvent
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		if ae := resp.GetAgentEvent(); ae != nil {
			out = append(out, ae)
		}
	}
	return out
}

// TestWatchEvents_ResumeEmitsCatchUpStart asserts the reconnect-replay protocol: a
// pre-replay CatchUpStart carrying the authoritative tail is emitted BEFORE the message
// burst (so a windowed client trims phantom rows up front), and CatchUpComplete carries
// the authoritative tail + start_tail_seq (the reap band the client exempts live arrivals
// from). The bounded replay's gap is closed by the client's CONTINUOUS tail-reconcile, so
// there is no per-frame replay_has_more flag.
func TestWatchEvents_ResumeEmitsCatchUpStart(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	// 60 messages; resuming from the 6th leaves a 54-message gap > the 50-row cap.
	var seqs []int64
	for i := 0; i < 60; i++ {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID: fmt.Sprintf("msg-%d", i+1), AgentID: "agent-1",
			Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, CreatedAt: time.Now(),
		})
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}
	tail := seqs[59]

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, CursorSeq: seqs[5]}},
	}, wWatch)

	var events []*leapmuxv1.AgentEvent
	require.Eventually(t, func() bool {
		events = decodeAgentEvents(wWatch)
		for _, e := range events {
			if e.GetCatchUpComplete() != nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected a CatchUpComplete frame")

	startIdx, firstMsgIdx, completeIdx := -1, -1, -1
	for i, e := range events {
		switch {
		case e.GetCatchUpStart() != nil && startIdx == -1:
			startIdx = i
		case e.GetAgentMessage() != nil && firstMsgIdx == -1:
			firstMsgIdx = i
		case e.GetCatchUpComplete() != nil && completeIdx == -1:
			completeIdx = i
		}
	}
	require.NotEqual(t, -1, startIdx, "a CatchUpStart frame must be emitted")
	require.NotEqual(t, -1, firstMsgIdx, "messages must replay")
	assert.Less(t, startIdx, firstMsgIdx, "CatchUpStart must precede the message replay so phantoms trim first")
	assert.Equal(t, tail, events[startIdx].GetCatchUpStart().GetLatestSeq(), "CatchUpStart carries the authoritative tail")

	require.NotEqual(t, -1, completeIdx)
	complete := events[completeIdx].GetCatchUpComplete()
	assert.Equal(t, tail, complete.GetLatestSeq())
	// start_tail_seq (== the tail when replay began) bounds the client's phantom reap; the
	// bounded 54-message gap is forward-filled by the client's continuous tail-reconcile.
	assert.Equal(t, tail, complete.GetStartTailSeq(), "CatchUpComplete carries the start-of-replay tail for the reap band")
}

// TestWatchEvents_FreshSubscribeEmitsCatchUpFrames asserts that a cold-start LATEST
// replay emits CatchUpStart + CatchUpComplete both carrying the authoritative tail (and
// the start-of-replay tail), so a fresh client reconciles against it.
func TestWatchEvents_FreshSubscribeEmitsCatchUpFrames(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	var tail int64
	for i := 0; i < 3; i++ {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID: fmt.Sprintf("msg-%d", i+1), AgentID: "agent-1",
			Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, CreatedAt: time.Now(),
		})
		require.NoError(t, err)
		tail = seq
	}

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST}},
	}, wWatch)

	var complete *leapmuxv1.CatchUpComplete
	var start *leapmuxv1.CatchUpStart
	require.Eventually(t, func() bool {
		for _, e := range decodeAgentEvents(wWatch) {
			if s := e.GetCatchUpStart(); s != nil {
				start = s
			}
			if c := e.GetCatchUpComplete(); c != nil {
				complete = c
			}
		}
		return complete != nil
	}, 5*time.Second, 20*time.Millisecond, "expected a CatchUpComplete frame")

	require.NotNil(t, start, "a CatchUpStart frame must be emitted")
	assert.Equal(t, tail, start.GetLatestSeq())
	assert.Equal(t, tail, complete.GetLatestSeq())
	assert.Equal(t, tail, complete.GetStartTailSeq(), "CatchUpComplete carries the start-of-replay tail")
}

// TestWatchEvents_AfterCursorWithZeroSeqReplaysLatest asserts that an AFTER_CURSOR
// entry whose cursor is 0 (a malformed resume that names no real point -- seqs are
// assigned from 1) is treated as a FRESH subscriber and gets the LATEST page, NOT
// the oldest page the forward scan (seq > 0) would otherwise return. No in-repo
// client builds this (AgentWatchEntry maps seq <= 0 to LATEST), so this guards the
// wire boundary against splicing the oldest page in front of a latest window.
func TestWatchEvents_AfterCursorWithZeroSeqReplaysLatest(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))

	var seqs []int64
	for i := 0; i < 60; i++ {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            fmt.Sprintf("msg-%d", i+1),
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:       []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     time.Now(),
		})
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, CursorSeq: 0}},
	}, wWatch)

	collectReplayedSeqs := func() []int64 {
		var out []int64
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			if am := resp.GetAgentEvent().GetAgentMessage(); am != nil {
				out = append(out, am.GetSeq())
			}
		}
		return out
	}

	var replayed []int64
	require.Eventually(t, func() bool {
		replayed = collectReplayedSeqs()
		return len(replayed) >= 50
	}, 5*time.Second, 20*time.Millisecond, "expected the latest 50 messages replayed")

	// The LATEST 50 (seqs[10:60]), ascending -- NOT the oldest 50.
	require.Len(t, replayed, 50)
	assert.Equal(t, seqs[10:60], replayed)
	assert.NotContains(t, replayed, seqs[0], "a zero-cursor AFTER_CURSOR must not replay the oldest message")
}

// TestWatchEvents_ReplayShipsTodosSnapshot asserts a (re)subscribe replays a fresh
// AgentTodosChanged snapshot. A RESUMING client catches up via AFTER pages, which
// (unlike the cold-start LATEST page) never carry the to-do snapshot, and it does
// not re-run its initial latest-page load -- so a to-do mutation missed while
// disconnected would leave the sidebar stale until a manual jump-to-latest without
// this replay-time snapshot.
func TestWatchEvents_ReplayShipsTodosSnapshot(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	var seqs []int64
	for i := 0; i < 3; i++ {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            fmt.Sprintf("msg-%d", i+1),
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:       []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     time.Now(),
		})
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}
	require.NoError(t, svc.Queries.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
		AgentID: "agent-1", RowKey: "k1", Seq: 1, TaskID: "t1",
		Content: "Run tests", ActiveForm: "Running tests", Status: "in_progress",
	}))

	// Resume from the first message's seq: a RESUMING subscriber whose catch-up
	// would use AFTER pages and never re-fetch the todo snapshot.
	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, CursorSeq: seqs[0]}},
	}, wWatch)

	collectTodos := func() []*leapmuxv1.TodoItem {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			if tc := resp.GetAgentEvent().GetTodosChanged(); tc != nil {
				return tc.GetTodos()
			}
		}
		return nil
	}

	var todos []*leapmuxv1.TodoItem
	require.Eventually(t, func() bool {
		todos = collectTodos()
		return len(todos) > 0
	}, 5*time.Second, 20*time.Millisecond, "expected an AgentTodosChanged snapshot during replay")
	require.Len(t, todos, 1)
	assert.Equal(t, "Run tests", todos[0].GetContent())
}

// TestWatchEvents_CatchUpCompleteCarriesLatestSeq asserts the CatchUpComplete
// sentinel reports the agent's authoritative live-tail seq (highest EXISTING
// message), which lags the high-water after a tail delete. A reconnecting windowed
// client uses it to drop phantom rows it never saw deleted and to clamp its recorded
// live-tail.
func TestWatchEvents_CatchUpCompleteCarriesLatestSeq(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	var seqs []int64
	for i := 0; i < 3; i++ {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            fmt.Sprintf("msg-%d", i+1),
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:       []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     time.Now(),
		})
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}
	// Delete the tail (seq 3): MAX(live) is now seq 2 even though the high-water is 3.
	_, err := svc.Queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{AgentID: "agent-1", ID: "msg-3"})
	require.NoError(t, err)

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST}},
	}, wWatch)

	var latest int64 = -2
	var startTail int64 = -2
	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			if cc := resp.GetAgentEvent().GetCatchUpComplete(); cc != nil {
				latest = cc.GetLatestSeq()
				startTail = cc.GetStartTailSeq()
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected a CatchUpComplete sentinel")
	assert.Equal(t, seqs[1], latest, "latest_seq must be the highest EXISTING seq (2), not the deleted tail's seq (3)")
	// start_tail_seq carries the tail when replay began so the client can exempt a live
	// arrival (seq above it) from the phantom reap. No concurrent change here, so it
	// matches latest_seq.
	assert.Equal(t, seqs[1], startTail, "start_tail_seq must carry the start-of-replay tail (2)")
}
