package service

import (
	"bytes"
	"context"
	"testing"

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

	testutil.AssertEventually(t, func() bool {
		for _, data := range collectTerminalData(t, w1, "t-resub") {
			if data.GetEndOffset() > 0 {
				return true
			}
		}
		return false
	}, "first subscribe delivered the current screen")

	// Freeze the screen buffer before reading the head offset.
	// cmd.exe keeps emitting prompt-setup bytes (alt-screen, title bar,
	// cursor toggles) for hundreds of ms after start, so any wall-clock
	// "settle" is racy on slow CI; Stop+WaitForReadDrained is sync.
	svc.Terminals.StopTerminal("t-resub")
	require.True(t, svc.Terminals.WaitForReadDrained("t-resub"))

	_, currentOffset, _ := svc.Terminals.ScreenSnapshotSince("t-resub", 0)
	require.Greater(t, currentOffset, int64(0))

	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "t-resub", AfterOffset: currentOffset}},
	}, w2)

	// dispatch is synchronous: catch-up has run by the time it returns.
	datas := collectTerminalData(t, w2, "t-resub")
	assert.Empty(t, datas,
		"resubscribe with after_offset=current must not replay bytes — the client already has them")
}

// TestWatchEvents_Terminal_IncrementalDeltaAfterResubscribe: client
// subscribes, captures the offset, more output arrives, client
// resubscribes. The second subscribe's catch-up must contain EXACTLY
// the newly-written bytes (is_snapshot=false), not the whole buffer.
//
// Uses AppendOutput for deterministic bytes: SendInput + AssertEventually
// was racy because AssertEventually fires on the first byte the shell
// emits, leaving firstOffset mid-echo. AppendOutput writes synchronously
// under the ring mutex, so the marker is fully committed before we read
// the offset. Concurrent shell prompt bytes are fine — they're nameless
// and can't collide with the markers we assert on.
func TestWatchEvents_Terminal_IncrementalDeltaAfterResubscribe(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, "ws-1")
	startTestTerminal(t, svc, ctx, "t-delta", "ws-1")

	firstMarker := []byte("FIRST_CHUNK_MARKER_BYTES\r\n")
	require.True(t, svc.Terminals.AppendOutput("t-delta", firstMarker))

	_, firstOffset, _ := svc.Terminals.ScreenSnapshotSince("t-delta", 0)
	require.GreaterOrEqual(t, firstOffset, int64(len(firstMarker)))

	secondMarker := []byte("SECOND_CHUNK_MARKER_BYTES\r\n")
	require.True(t, svc.Terminals.AppendOutput("t-delta", secondMarker))

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
	assert.Contains(t, string(delta.GetData()), string(secondMarker),
		"delta must contain the bytes the absent client missed")
	assert.NotContains(t, string(delta.GetData()), string(firstMarker),
		"delta must NOT re-send bytes the client already has")
}

// TestWatchEvents_Terminal_AltScreenRecoveryAfterRingWrap: the bug this
// whole feature exists to fix. A subscriber that joins (or rejoins)
// after the ring has wrapped past an alt-screen toggle must receive a
// snapshot whose first bytes re-establish alt screen. Without the
// prefix, xterm.reset() drops to main screen and replays bytes that
// assume alt screen — vim/less/htop render as garbage until the
// program repaints.
func TestWatchEvents_Terminal_AltScreenRecoveryAfterRingWrap(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, "ws-1")
	startTestTerminal(t, svc, ctx, "t-altrecover", "ws-1")
	fillerLen := injectAltScreenAndFlushPastRing(t, svc, "t-altrecover")

	w := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "t-altrecover", AfterOffset: 0}},
	}, w)

	var snap *leapmuxv1.TerminalData
	testutil.AssertEventually(t, func() bool {
		for _, data := range collectTerminalData(t, w, "t-altrecover") {
			snap = data
			return true
		}
		return false
	}, "cold subscribe delivered the snapshot")
	require.NotNil(t, snap)
	require.True(t, snap.GetIsSnapshot(),
		"fell-behind cold subscribe must be a snapshot so the frontend resets first")
	assert.True(t, bytes.HasPrefix(snap.GetData(), []byte("\x1b[?1049h")),
		"snapshot must lead with the alt-screen restore — without it, vim/less render garbage after the frontend's terminal.reset()")

	// The tracker prefix is synthesized; it must NOT inflate end_offset
	// beyond the actual byte counter the client uses to resume.
	currentTotal := int64(len("\x1b[?1049h") + fillerLen)
	assert.GreaterOrEqual(t, snap.GetEndOffset(), currentTotal,
		"end_offset reflects total bytes written, not prefix length")
}

// injectAltScreenAndFlushPastRing writes the alt-screen toggle followed
// by enough plain bytes to overwrite the retained ring, then waits for
// the manager's offset to confirm the bytes have landed. Returns the
// filler length so callers can compute the expected end_offset. Shared
// by service-layer tests that assert the modeTracker prefix appears on
// a snapshot taken AFTER the toggle has fallen out of the ring.
func injectAltScreenAndFlushPastRing(t *testing.T, svc *Context, terminalID string) int {
	t.Helper()
	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x1b[?1049h")))
	// 110 KB > screenBufferSize (100 KB), so the toggle is guaranteed
	// out of the ring after this single write completes.
	filler := make([]byte, 110*1024)
	for i := range filler {
		filler[i] = 'a'
	}
	require.True(t, svc.Terminals.AppendOutput(terminalID, filler))
	testutil.AssertEventually(t, func() bool {
		_, off, _ := svc.Terminals.ScreenSnapshotSince(terminalID, 0)
		return off >= int64(len(filler))
	}, "alt-screen + filler arrived")
	return len(filler)
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
