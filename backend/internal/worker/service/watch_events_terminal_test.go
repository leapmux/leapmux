package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// collectTerminalData drains all TerminalData events (snapshot or
// incremental) addressed to terminalID from w's stream buffer.
func collectTerminalData(t *testing.T, w *testResponseWriter, terminalID string) []*leapmuxv1.TerminalData {
	t.Helper()
	var out []*leapmuxv1.TerminalData
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		te := resp.GetTerminalEvent()
		if te == nil || te.GetTerminalId() != terminalID {
			continue
		}
		if d := te.GetData(); d != nil {
			out = append(out, d)
		}
	}
	return out
}

// TestWatchEvents_Terminal_ResubscribeWithCurrentOffset_NoDuplicate:
// a resubscribe with after_offset == the terminal's current cumulative
// offset produces NO TerminalData event. The backend has nothing new
// to send, so the client's xterm must not receive any replay — seeing
// one would mean the backend is re-sending bytes the client already
// has, which manifests as duplicated prompt output in the live UI.
func TestWatchEvents_Terminal_ResubscribeWithCurrentOffset_NoDuplicate(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, "ws-1")
	startTestTerminal(t, svc, ctx, "t-resub", "ws-1")

	require.NoError(t, svc.Terminals.SendInput("t-resub", []byte("echo resubscribe_test"+testutil.TestShellEnter())))
	testutil.AssertEventually(t, func() bool {
		_, off, _ := svc.Terminals.ScreenSnapshotSince("t-resub", 0)
		return off > 0
	}, "initial shell output")

	// First subscribe: after_offset=0 delivers a cold-catch-up event with
	// the current screen contents.
	w1 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "t-resub", AfterOffset: 0}},
	}, w1)

	var initialOffset int64
	testutil.AssertEventually(t, func() bool {
		for _, data := range collectTerminalData(t, w1, "t-resub") {
			if data.GetEndOffset() > 0 {
				initialOffset = data.GetEndOffset()
				return true
			}
		}
		return false
	}, "first subscribe delivered the current screen")
	require.Greater(t, initialOffset, int64(0))

	// Second subscribe with after_offset at the current head. The backend
	// must not send any TerminalData.
	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "t-resub", AfterOffset: initialOffset}},
	}, w2)

	// Give the handler a moment; if it was going to emit data it'd be in
	// the stream buffer synchronously (the catch-up loop runs in the RPC
	// call path, not a background goroutine).
	time.Sleep(50 * time.Millisecond)
	datas := collectTerminalData(t, w2, "t-resub")
	assert.Empty(t, datas,
		"resubscribe with after_offset=current must not replay bytes — the client already has them")
}

// TestWatchEvents_Terminal_IncrementalDeltaAfterResubscribe: client
// subscribes, captures the offset, more output arrives, client
// resubscribes. The second subscribe's catch-up must contain EXACTLY
// the newly-written bytes (is_snapshot=false), not the whole buffer.
func TestWatchEvents_Terminal_IncrementalDeltaAfterResubscribe(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, "ws-1")
	startTestTerminal(t, svc, ctx, "t-delta", "ws-1")

	// Drive some initial output.
	require.NoError(t, svc.Terminals.SendInput("t-delta", []byte("echo first_chunk"+testutil.TestShellEnter())))
	testutil.AssertEventually(t, func() bool {
		screen, _, _ := svc.Terminals.ScreenSnapshotSince("t-delta", 0)
		return len(screen) > 0
	}, "first chunk")

	// Capture the current offset — this is what a frontend would have
	// after the first subscribe.
	_, firstOffset, _ := svc.Terminals.ScreenSnapshotSince("t-delta", 0)
	require.Greater(t, firstOffset, int64(0))

	// Drive more output the "absent" client will miss.
	require.NoError(t, svc.Terminals.SendInput("t-delta", []byte("echo second_chunk"+testutil.TestShellEnter())))
	testutil.AssertEventually(t, func() bool {
		_, off, _ := svc.Terminals.ScreenSnapshotSince("t-delta", 0)
		return off > firstOffset
	}, "second chunk")

	// Resubscribe at firstOffset. Must receive an incremental delta with
	// is_snapshot=false containing only the new bytes.
	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "t-delta", AfterOffset: firstOffset}},
	}, w2)

	var delta *leapmuxv1.TerminalData
	testutil.AssertEventually(t, func() bool {
		for _, data := range collectTerminalData(t, w2, "t-delta") {
			delta = data
			return true
		}
		return false
	}, "resubscribe delivered a catch-up event")
	require.NotNil(t, delta)
	assert.False(t, delta.GetIsSnapshot(),
		"in-window resume must NOT be flagged as snapshot — frontend would reset unnecessarily")
	assert.Contains(t, string(delta.GetData()), "second_chunk",
		"delta must contain the bytes the absent client missed")
	assert.NotContains(t, string(delta.GetData()), "first_chunk",
		"delta must NOT re-send bytes the client already has")
}

// TestWatchEvents_Terminal_ColdSubscribeAfterRingWrap: when the backend's
// ring has wrapped, a cold subscriber (after_offset=0) receives a full
// screen snapshot with is_snapshot=true so the client resets its xterm
// rather than appending to possibly-stale state. Guards the fallen-behind
// branch of SnapshotSince in the WatchEvents handler.
func TestWatchEvents_Terminal_ColdSubscribeAfterRingWrap(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, "ws-1")
	startTestTerminal(t, svc, ctx, "t-wrap", "ws-1")

	// Inject >100KB synthetic output directly so we don't have to wait on
	// the shell to produce it. AppendOutput goes through the same write
	// path as PTY output.
	big := make([]byte, 150*1024)
	for i := range big {
		big[i] = 'x'
	}
	require.True(t, svc.Terminals.AppendOutput("t-wrap", big))
	testutil.AssertEventually(t, func() bool {
		_, off, _ := svc.Terminals.ScreenSnapshotSince("t-wrap", 0)
		return off >= int64(len(big))
	}, "appended bytes arrived")

	// Cold subscribe — after_offset=0 is now well below the retained
	// window.
	w := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "t-wrap", AfterOffset: 0}},
	}, w)

	var snap *leapmuxv1.TerminalData
	testutil.AssertEventually(t, func() bool {
		for _, data := range collectTerminalData(t, w, "t-wrap") {
			snap = data
			return true
		}
		return false
	}, "cold subscribe delivered a catch-up event")
	require.NotNil(t, snap)
	assert.True(t, snap.GetIsSnapshot(),
		"fell-behind-ring cold subscribe must be flagged as snapshot so the frontend resets")
	// Delta size is bounded by the 100KB ring, even though the PTY has
	// emitted ~150KB.
	assert.LessOrEqual(t, len(snap.GetData()), 100*1024)
	assert.Greater(t, snap.GetEndOffset(), int64(100*1024),
		"end_offset reflects the cumulative counter, which is > ring size once the ring has wrapped")
}
