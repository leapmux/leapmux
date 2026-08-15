package service

import (
	"bytes"
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// --- ListAgents by IDs ---

func TestListAgents_ByIDs_ReturnsOnlyRequested(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	for _, id := range []string{"a1", "a2", "a3"} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: id, WorkingDir: "/tmp", HomeDir: "/tmp",
		}))
	}

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"a1", "a3"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 2)
	ids := []string{resp.GetAgents()[0].GetId(), resp.GetAgents()[1].GetId()}
	assert.ElementsMatch(t, []string{"a1", "a3"}, ids)
}

func TestListAgents_EmptyTabIDs_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	_, d, w := setupTestService(t)

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetAgents())
}

func TestListAgents_NonexistentIDs_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	_, d, w := setupTestService(t)

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"nonexistent-1", "nonexistent-2"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetAgents())
}

func TestListAgents_ClosedTabsFiltered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, "a1")))

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"a1"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetAgents(), "closed agent should not be returned")
}

func TestListAgents_MixExistingAndNonexistent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a2", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"a1", "a2", "nonexistent"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Len(t, resp.GetAgents(), 2)
}

// --- ListTerminals by IDs ---

func TestListTerminals_ByIDs_ReturnsOnlyRequested(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	for _, id := range []string{"t1", "t2", "t3"} {
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: id, WorkingDir: "/tmp", HomeDir: "/tmp",
			Cols: 80, Rows: 24, Screen: []byte("screen"),
		}))
	}

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t1", "t3"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 2)
	ids := []string{resp.GetTerminals()[0].GetTerminalId(), resp.GetTerminals()[1].GetTerminalId()}
	assert.ElementsMatch(t, []string{"t1", "t3"}, ids)
}

func TestListTerminals_EmptyTabIDs_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	_, d, w := setupTestService(t)

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals())
}

func TestListTerminals_NonexistentIDs_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	_, d, w := setupTestService(t)

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"nonexistent"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals())
}

// TestListTerminals_DBReadFailure_ErrorsRatherThanStampingAbsent pins that a
// DB read failure fails the WHOLE call instead of falling through to the
// verdict pass.
//
// Falling through is not a partial success: `seen` would hold only the ids
// the in-memory manager happens to have, so tabHydrationVerdicts stamps every
// other requested id ABSENT. ABSENT is the one verdict the client treats as
// permanent -- retryableFrom drops those ids, the candidate-set hash never
// changes, and the tabs stay blank until a full page reload. ListAgents
// already fails the call on the same error; this asserts ListTerminals does
// too.
func TestListTerminals_DBReadFailure_ErrorsRatherThanStampingAbsent(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)

	// Closing the pool is the deterministic way to induce the failure this
	// handler must survive: a read error on an otherwise well-formed request.
	require.NoError(t, svc.DB.Close())

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t1", "t2"},
	}, w)

	assert.Empty(t, w.responses, "a DB read failure must not produce a success response")
	require.Len(t, w.errors, 1, "the failure must reach the client as an error so its backoff keeps retrying")
	assert.Equal(t, int32(codes.Internal), w.errors[0].code)
}

func TestListTerminals_ClosedTabsFiltered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "t1", WorkingDir: "/tmp", HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: []byte("screen"),
		ClosedAt: sqltime.SQLiteNullTimeOf(time.Now()),
	}))

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t1"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals(), "closed terminal should not be returned")
}

// TestListTerminals_ScreenEndOffset_DBOnly: terminals that exist only in
// the DB (no live PTY — worker restarted or shell exited) must report
// screen_end_offset = len(screen). The frontend seeds its WatchEvents
// after_offset from this, and the invariant means a cold subscribe
// against a dead terminal resolves to "caught up" with no replay.
func TestListTerminals_ScreenEndOffset_DBOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	screen := []byte("some persisted screen content")
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "t-db", WorkingDir: "/tmp", HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: screen,
	}))

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t-db"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	ti := resp.GetTerminals()[0]
	assert.Equal(t, screen, ti.GetScreen())
	assert.Equal(t, int64(len(screen)), ti.GetScreenEndOffset(),
		"DB-only terminals: screen_end_offset must equal len(screen)")
}

// TestListTerminals_ScreenEndOffset_LiveTerminal: terminals with a live
// PTY in the Manager must report screen_end_offset sourced from the
// Manager (cumulative bytes written), not from len(db_row.screen) which
// is empty until Shutdown persists. Proves the path by injecting a
// unique marker via AppendOutput and confirming the response carries
// it — the DB row for this terminal has Screen=[]byte{}, so a
// non-empty response can only have come from the Manager. Driving the
// shell with SendInput was previously racy: PTY output continues
// streaming asynchronously, so ti.ScreenEndOffset and a subsequent
// ScreenSnapshotSince read at different moments diverged.
func TestListTerminals_ScreenEndOffset_LiveTerminal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	startTestTerminal(t, svc, ctx, "t-live")

	// Freeze before injecting so the exact offset below is arithmetic,
	// not a guess about which shell bytes have landed. The manager keeps
	// the entry, so ListTerminals still takes its live-PTY branch.
	baseline := testutil.FreezeTerminalOutput(t, svc.Terminals, "t-live")

	// The alt-screen toggle leaves the modeTracker non-default, so screen
	// carries a restore prefix on every platform. A Windows cmd.exe sets
	// sticky modes by itself and a POSIX /bin/sh sets none, so without
	// this the prefix arithmetic below is only exercised on Windows.
	require.True(t, svc.Terminals.AppendOutput("t-live", []byte(altScreenEnter)))

	marker := []byte("live_offset_test_marker")
	require.True(t, svc.Terminals.AppendOutput("t-live", marker))

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t-live"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	ti := resp.GetTerminals()[0]

	// The marker presence confirms we got the Manager's buffer, not the
	// DB row's empty Screen, and the offset is the frozen baseline plus
	// exactly what this test injected.
	assert.True(t, bytes.HasSuffix(ti.GetScreen(), marker),
		"live terminal must return Manager's screen (DB row has Screen=[]byte{})")
	assert.Equal(t, baseline+int64(len(altScreenEnter)+len(marker)), ti.GetScreenEndOffset(),
		"screen_end_offset must come from the Manager's cumulative counter")
	// screen_end_offset counts stream bytes only. Screen leads with the
	// mode tracker's synthesized restore prefix on top of them, so before
	// the ring wraps it is strictly longer than the offset. Equating the
	// two asserts an empty prefix.
	assert.Greater(t, int64(len(ti.GetScreen())), ti.GetScreenEndOffset(),
		"before ring wrap: screen holds every counted byte plus the mode prefix")
}

// TestListTerminals_AltScreenRecoveryAfterRingWrap: page-refresh is the
// most common path that hits the alt-screen rendering bug. The frontend
// seeds xterm from TerminalInfo.screen with isSnapshot=true (resets
// xterm before writing), so the bytes here MUST start with the
// mode-restore prefix when the alt-screen toggle has fallen out of the
// retained ring.
func TestListTerminals_AltScreenRecoveryAfterRingWrap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	startTestTerminal(t, svc, ctx, "t-altrefresh")
	wantEndOffset := freezeAndInjectAltScreenPastRing(t, svc, "t-altrefresh")

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t-altrefresh"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	ti := resp.GetTerminals()[0]
	require.GreaterOrEqual(t, len(ti.GetScreen()), len(altScreenEnter))
	assert.Equal(t, []byte(altScreenEnter), ti.GetScreen()[:len(altScreenEnter)],
		"ListTerminals must return the alt-screen restore prefix; without it, page-refresh leaves vim/less rendering as garbage")

	// screen_end_offset must NOT include the synthesized prefix bytes.
	// The frontend uses this offset to seed its WatchEvents resume
	// cursor; counting prefix bytes would skip real PTY output the next
	// time the backend computes a delta.
	assert.Equal(t, wantEndOffset, ti.GetScreenEndOffset(),
		"screen_end_offset reflects total PTY bytes, not screen-payload length (which includes the synthesized prefix)")
	assert.True(t, ti.GetExited(),
		"the helper freezes the PTY to make the offset exact; pin that rather than leaving it incidental")
}

// The exact-offset tests above freeze the PTY, so on their own they would let
// an "exited terminals can't repaint, skip the prefix" fast path ship green --
// while the bug this feature exists to fix is a user with vim open in a LIVE
// tab hitting refresh. This case keeps the terminal running and therefore
// asserts only the prefix, never an offset: the login shell's own prompt bytes
// race any byte count, which is exactly why its siblings freeze.
func TestListTerminals_AltScreenRecoveryOnLiveTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The whole point of this case is a LIVE shell, and cmd.exe keeps
		// emitting prompt-setup bytes -- including alt-screen toggles -- for
		// hundreds of ms after start. One `\x1b[?1049l` landing between the
		// injection and the dispatch clears modeTracker's altScreen bit and
		// the prefix legitimately disappears. That is a race with the shell,
		// not a bug in the code under test, and it is the exact flake class
		// this file's frozen tests exist to avoid; they cover Windows.
		t.Skip("a live cmd.exe writes its own alt-screen toggles; see the frozen siblings")
	}
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	startTestTerminal(t, svc, ctx, "t-altlive")

	require.True(t, svc.Terminals.AppendOutput("t-altlive", []byte(altScreenEnter)))
	// 110 KB > screenBufferSize (100 KB), so the toggle is out of the ring.
	require.True(t, svc.Terminals.AppendOutput("t-altlive", bytes.Repeat([]byte{'a'}, 110*1024)))

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t-altlive"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	ti := resp.GetTerminals()[0]
	assert.False(t, ti.GetExited(), "this case is worthless unless the terminal is still running")
	require.GreaterOrEqual(t, len(ti.GetScreen()), len(altScreenEnter))
	assert.Equal(t, []byte(altScreenEnter), ti.GetScreen()[:len(altScreenEnter)],
		"a LIVE terminal must get the alt-screen restore prefix too; that is the vim-open-then-refresh case")
}
func TestWatchEvents_ListAgentsByIDsErrorReturnsInternalStreamError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	svc.Queries = db.New(&faultingDBTX{real: svc.DB, failSubstr: "FROM agents WHERE id IN"})

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a1"}},
	}, w)

	ack := waitWatchUpdateAck(t, w)
	require.NotNil(t, ack)
	require.Len(t, ack.GetRejectedAgents(), 1)
	assert.Equal(t, "a1", ack.GetRejectedAgents()[0].GetEntityId())
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
		ack.GetRejectedAgents()[0].GetReason())
	assert.False(t, streamEndedWithError(w))
}
