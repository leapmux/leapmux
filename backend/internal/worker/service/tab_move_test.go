package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestMoveTabWorkspace_Agent(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1", "ws-2")

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))

	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:          "agent-1",
		NewWorkspaceId: "ws-2",
	}, w)

	require.Empty(t, w.errors, "expected no errors")
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.MoveTabWorkspaceResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	// Verify the agent's workspace_id was updated in the DB.
	wsID, err := svc.Queries.GetAgentWorkspaceID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "ws-2", wsID)
}

func TestMoveTabWorkspace_Terminal(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1", "ws-2")

	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("hello"),
	}))

	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		TabId:          "term-1",
		NewWorkspaceId: "ws-2",
	}, w)

	require.Empty(t, w.errors, "expected no errors")
	require.Len(t, w.responses, 1)

	// Verify the terminal's workspace_id was updated in the DB.
	termWsID, err := svc.Queries.GetTerminalWorkspaceID(ctx, "term-1")
	require.NoError(t, err)
	assert.Equal(t, "ws-2", termWsID)
}

func TestMoveTabWorkspace_TargetNotAccessible(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1") // only ws-1 accessible

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))

	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:          "agent-1",
		NewWorkspaceId: "ws-forbidden",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(7), w.errors[0].code, "expected PERMISSION_DENIED")
	assert.Empty(t, w.responses, "expected no success response")

	// Agent should remain in ws-1.
	wsID, err := svc.Queries.GetAgentWorkspaceID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "ws-1", wsID)
}

func TestMoveTabWorkspace_MissingFields(t *testing.T) {
	_, d, _ := setupTestService(t, "ws-1")

	tests := []struct {
		name string
		req  *leapmuxv1.MoveTabWorkspaceRequest
	}{
		{"missing tab_id", &leapmuxv1.MoveTabWorkspaceRequest{
			TabType:        leapmuxv1.TabType_TAB_TYPE_AGENT,
			NewWorkspaceId: "ws-1",
		}},
		{"missing new_workspace_id", &leapmuxv1.MoveTabWorkspaceRequest{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabId:   "agent-1",
		}},
		{"both missing", &leapmuxv1.MoveTabWorkspaceRequest{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &testResponseWriter{channelID: "test-ch"}
			dispatch(d, "MoveTabWorkspace", tt.req, w)
			require.Len(t, w.errors, 1)
			assert.Equal(t, int32(3), w.errors[0].code, "expected INVALID_ARGUMENT")
		})
	}
}

func TestMoveTabWorkspace_UnsupportedTabType(t *testing.T) {
	_, d, w := setupTestService(t, "ws-1", "ws-2")

	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_FILE,
		TabId:          "file-1",
		NewWorkspaceId: "ws-2",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(3), w.errors[0].code, "expected INVALID_ARGUMENT")
}

func TestMoveTabWorkspace_NonexistentAgent(t *testing.T) {
	_, d, w := setupTestService(t, "ws-1", "ws-2")

	// Moving a non-existent agent — the SQL UPDATE affects 0 rows but
	// doesn't error. The handler should still return a success response.
	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:          "nonexistent",
		NewWorkspaceId: "ws-2",
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
}
