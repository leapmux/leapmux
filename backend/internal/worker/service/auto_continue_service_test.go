package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestCloseAgent_CancelsPendingSchedules(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")

	require.NoError(t, svc.Queries.CreateAgent(bgCtx(), db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	require.NoError(t, svc.Queries.UpsertAutoContinueSchedule(bgCtx(), db.UpsertAutoContinueScheduleParams{
		AgentID:       "agent-1",
		Reason:        "rate_limit",
		Content:       autoContinueContent,
		DueAt:         time.Now().UTC().Add(time.Hour),
		JitterMs:      0,
		NextBackoffMs: 0,
		SourcePayload: []byte{},
	}))

	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"}, w)
	require.Empty(t, w.errors)

	row, err := svc.Queries.GetAutoContinueSchedule(bgCtx(), db.GetAutoContinueScheduleParams{
		AgentID: "agent-1",
		Reason:  "rate_limit",
	})
	require.NoError(t, err)
	assert.Equal(t, autoContinueStateCancelled, row.State)
}

func TestCleanupWorkspace_CancelsPendingSchedules(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")

	require.NoError(t, svc.Queries.CreateAgent(bgCtx(), db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	require.NoError(t, svc.Queries.UpsertAutoContinueSchedule(bgCtx(), db.UpsertAutoContinueScheduleParams{
		AgentID:       "agent-1",
		Reason:        "api_error",
		Content:       autoContinueContent,
		DueAt:         time.Now().UTC().Add(time.Hour),
		JitterMs:      0,
		NextBackoffMs: 20000,
		SourcePayload: []byte{},
	}))

	dispatch(d, "CleanupWorkspace", &leapmuxv1.CleanupWorkspaceRequest{WorkspaceId: "ws-1"}, w)
	require.Empty(t, w.errors)

	row, err := svc.Queries.GetAutoContinueSchedule(bgCtx(), db.GetAutoContinueScheduleParams{
		AgentID: "agent-1",
		Reason:  "api_error",
	})
	require.NoError(t, err)
	assert.Equal(t, autoContinueStateCancelled, row.State)
}
