package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// --- ListAgents by IDs ---

func TestListAgents_ByIDs_ReturnsOnlyRequested(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	for _, id := range []string{"a1", "a2", "a3"} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: id, WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
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
	_, d, w := setupTestService(t, "ws-A")

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetAgents())
}

func TestListAgents_NonexistentIDs_ReturnsEmpty(t *testing.T) {
	_, d, w := setupTestService(t, "ws-A")

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"nonexistent-1", "nonexistent-2"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetAgents())
}

func TestListAgents_ClosedTabsFiltered(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a1", WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, svc.Queries.CloseAgent(ctx, "a1"))

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"a1"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetAgents(), "closed agent should not be returned")
}

func TestListAgents_MixExistingAndNonexistent(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a1", WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a2", WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"a1", "a2", "nonexistent"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Len(t, resp.GetAgents(), 2)
}

func TestListAgents_AccessibleWorkspaceAllowed(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a1", WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"a1"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Len(t, resp.GetAgents(), 1, "agent in accessible workspace should be returned")
}

func TestListAgents_InaccessibleWorkspaceDenied(t *testing.T) {
	ctx := context.Background()
	// Channel only has access to ws-A.
	svc, d, w := setupTestService(t, "ws-A")

	// Agent is in ws-B (not accessible).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a1", WorkspaceID: "ws-B", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"a1"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetAgents(), "agent in inaccessible workspace should be filtered out")
}

func TestListAgents_MixedAccess(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a1", WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a2", WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "a3", WorkspaceID: "ws-B", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"a1", "a2", "a3"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Len(t, resp.GetAgents(), 2, "only agents in ws-A should be returned")
}

// --- ListTerminals by IDs ---

func TestListTerminals_ByIDs_ReturnsOnlyRequested(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	for _, id := range []string{"t1", "t2", "t3"} {
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: id, WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
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
	_, d, w := setupTestService(t, "ws-A")

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals())
}

func TestListTerminals_NonexistentIDs_ReturnsEmpty(t *testing.T) {
	_, d, w := setupTestService(t, "ws-A")

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"nonexistent"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals())
}

func TestListTerminals_ClosedTabsFiltered(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "t1", WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: []byte("screen"),
		ClosedAt: sql.NullTime{Time: time.Now(), Valid: true},
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
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	screen := []byte("some persisted screen content")
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "t-db", WorkspaceID: "ws-A", WorkingDir: "/tmp", HomeDir: "/tmp",
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
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")
	startTestTerminal(t, svc, ctx, "t-live", "ws-A")

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

	// Screen and ScreenEndOffset are sampled atomically inside
	// buildEntryLocked (single Terminal.ScreenSnapshot call), so the
	// len(screen) == offset invariant holds regardless of concurrent
	// shell output. The marker presence confirms we got the Manager's
	// buffer, not the DB row's empty Screen.
	assert.Contains(t, string(ti.GetScreen()), string(marker),
		"live terminal must return Manager's screen (DB row has Screen=[]byte{})")
	assert.Equal(t, int64(len(ti.GetScreen())), ti.GetScreenEndOffset(),
		"before ring wrap: screen_end_offset equals len(screen)")
}

// TestListTerminals_AltScreenRecoveryAfterRingWrap: page-refresh is the
// most common path that hits the alt-screen rendering bug. The frontend
// seeds xterm from TerminalInfo.screen with isSnapshot=true (resets
// xterm before writing), so the bytes here MUST start with the
// mode-restore prefix when the alt-screen toggle has fallen out of the
// retained ring.
func TestListTerminals_AltScreenRecoveryAfterRingWrap(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")
	startTestTerminal(t, svc, ctx, "t-altrefresh", "ws-A")
	fillerLen := injectAltScreenAndFlushPastRing(t, svc, "t-altrefresh")

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t-altrefresh"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	ti := resp.GetTerminals()[0]
	require.GreaterOrEqual(t, len(ti.GetScreen()), len("\x1b[?1049h"))
	assert.Equal(t, []byte("\x1b[?1049h"), ti.GetScreen()[:len("\x1b[?1049h")],
		"ListTerminals must return the alt-screen restore prefix; without it, page-refresh leaves vim/less rendering as garbage")

	// screen_end_offset must NOT include the synthesized prefix bytes.
	// The frontend uses this offset to seed its WatchEvents resume
	// cursor; counting prefix bytes would skip real PTY output the next
	// time the backend computes a delta.
	assert.Equal(t, int64(len("\x1b[?1049h")+fillerLen), ti.GetScreenEndOffset(),
		"screen_end_offset reflects total PTY bytes, not screen-payload length (which includes the synthesized prefix)")
}

func TestListTerminals_InaccessibleWorkspaceDenied(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")

	// Terminal in inaccessible workspace.
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "t1", WorkspaceID: "ws-B", WorkingDir: "/tmp", HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: []byte("screen"),
	}))

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t1"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals(), "terminal in inaccessible workspace should be filtered out")
}
