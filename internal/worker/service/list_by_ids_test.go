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
