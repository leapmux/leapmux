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
	"github.com/leapmux/leapmux/internal/util/testutil"
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
// PTY in the Manager must report screen_end_offset = cumulative bytes
// written, which equals len(screen) until the ring wraps but can diverge
// afterwards. Sanity-check: the returned offset matches Manager.ScreenSnapshot
// (Manager is the authoritative source).
func TestListTerminals_ScreenEndOffset_LiveTerminal(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-A")
	startTestTerminal(t, svc, ctx, "t-live", "ws-A")

	// Drive some output so the screen buffer is non-empty.
	require.NoError(t, svc.Terminals.SendInput("t-live", []byte("echo live_offset_test"+testutil.TestShellEnter())))
	testutil.AssertEventually(t, func() bool {
		screen, _, _ := svc.Terminals.ScreenSnapshotSince("t-live", 0)
		return len(screen) > 0
	}, "expected live output")

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"t-live"},
	}, w)

	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	ti := resp.GetTerminals()[0]

	// The authoritative offset is whatever the Manager's ScreenBuffer
	// holds at the time ListTerminals ran. Read the current value and
	// assert the response matches — both should equal len(screen) before
	// the ring wraps, and both should drift in lockstep after.
	_, managerOffset, _ := svc.Terminals.ScreenSnapshotSince("t-live", 0)
	assert.Equal(t, managerOffset, ti.GetScreenEndOffset(),
		"live terminal: screen_end_offset must match the Manager's offset")
	assert.Equal(t, int64(len(ti.GetScreen())), ti.GetScreenEndOffset(),
		"before ring wrap: screen_end_offset equals len(screen)")
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
