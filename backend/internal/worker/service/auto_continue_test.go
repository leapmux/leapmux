package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func createAutoContinueTestAgent(t *testing.T, queries *db.Queries, agentID string) {
	t.Helper()
	require.NoError(t, queries.CreateAgent(bgCtx(), db.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
}

func TestAutoContinueSchedule_SurvivesRestart(t *testing.T) {
	_, queries := setupTestDB(t)
	createAutoContinueTestAgent(t, queries, "agent-1")

	require.NoError(t, queries.UpsertAutoContinueSchedule(bgCtx(), db.UpsertAutoContinueScheduleParams{
		AgentID:       "agent-1",
		Reason:        string(agent.AutoContinueReasonRateLimit),
		Content:       autoContinueContent,
		DueAt:         time.Now().UTC().Add(100 * time.Millisecond),
		JitterMs:      0,
		NextBackoffMs: 0,
		SourcePayload: []byte{},
	}))

	var fired atomic.Int32
	h2 := NewOutputHandler(queries, nil, nil, nil)
	h2.SetSendMessageFunc(func(agentID, content string) {
		if agentID == "agent-1" && content == autoContinueContent {
			fired.Add(1)
		}
	})
	h2.restoreAutoContinueSchedules()

	require.Eventually(t, func() bool { return fired.Load() == 1 }, 2*time.Second, 25*time.Millisecond)

	row, err := queries.GetAutoContinueSchedule(bgCtx(), db.GetAutoContinueScheduleParams{
		AgentID: "agent-1",
		Reason:  string(agent.AutoContinueReasonRateLimit),
	})
	require.NoError(t, err)
	assert.Equal(t, autoContinueStateFired, row.State)
}

func TestAutoContinueSchedule_RescheduleUpdatesSingleRow(t *testing.T) {
	_, queries := setupTestDB(t)
	createAutoContinueTestAgent(t, queries, "agent-1")

	h := NewOutputHandler(queries, nil, nil, nil)
	firstDueAt := time.Now().UTC().Add(10 * time.Minute)
	secondDueAt := firstDueAt.Add(20 * time.Minute)

	h.scheduleAutoContinue("agent-1", agent.AutoContinueSchedule{
		Reason: agent.AutoContinueReasonRateLimit,
		DueAt:  firstDueAt,
	})
	h.scheduleAutoContinue("agent-1", agent.AutoContinueSchedule{
		Reason: agent.AutoContinueReasonRateLimit,
		DueAt:  secondDueAt,
	})
	t.Cleanup(func() { h.cleanupAutoContinue("agent-1") })

	rows, err := queries.ListActiveAutoContinueSchedules(bgCtx())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "agent-1", rows[0].AgentID)
	assert.Equal(t, string(agent.AutoContinueReasonRateLimit), rows[0].Reason)
	assert.True(t, rows[0].DueAt.After(firstDueAt))
}

func TestAutoContinueSchedule_ReasonsCanCoexist(t *testing.T) {
	_, queries := setupTestDB(t)
	createAutoContinueTestAgent(t, queries, "agent-1")

	h := NewOutputHandler(queries, nil, nil, nil)
	h.scheduleAutoContinue("agent-1", agent.AutoContinueSchedule{
		Reason: agent.AutoContinueReasonAPIError,
		DueAt:  time.Now().UTC(),
	})
	h.scheduleAutoContinue("agent-1", agent.AutoContinueSchedule{
		Reason: agent.AutoContinueReasonRateLimit,
		DueAt:  time.Now().UTC().Add(time.Hour),
	})
	t.Cleanup(func() { h.cleanupAutoContinue("agent-1") })

	rows, err := queries.ListActiveAutoContinueSchedules(bgCtx())
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestAutoContinueSchedule_CancelOneReasonLeavesOtherIntact(t *testing.T) {
	_, queries := setupTestDB(t)
	createAutoContinueTestAgent(t, queries, "agent-1")

	h := NewOutputHandler(queries, nil, nil, nil)
	h.scheduleAutoContinue("agent-1", agent.AutoContinueSchedule{
		Reason: agent.AutoContinueReasonAPIError,
		DueAt:  time.Now().UTC(),
	})
	h.scheduleAutoContinue("agent-1", agent.AutoContinueSchedule{
		Reason: agent.AutoContinueReasonRateLimit,
		DueAt:  time.Now().UTC().Add(time.Hour),
	})

	h.cancelAutoContinue("agent-1", agent.AutoContinueReasonAPIError)
	t.Cleanup(func() { h.cleanupAutoContinue("agent-1") })

	rows, err := queries.ListActiveAutoContinueSchedules(bgCtx())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, string(agent.AutoContinueReasonRateLimit), rows[0].Reason)

	apiRow, err := queries.GetAutoContinueSchedule(bgCtx(), db.GetAutoContinueScheduleParams{
		AgentID: "agent-1",
		Reason:  string(agent.AutoContinueReasonAPIError),
	})
	require.NoError(t, err)
	assert.Equal(t, autoContinueStateCancelled, apiRow.State)
}

func TestAutoContinueSchedule_FiresOnceAndDoesNotRefireAfterRestart(t *testing.T) {
	_, queries := setupTestDB(t)
	createAutoContinueTestAgent(t, queries, "agent-1")

	require.NoError(t, queries.UpsertAutoContinueSchedule(bgCtx(), db.UpsertAutoContinueScheduleParams{
		AgentID:       "agent-1",
		Reason:        string(agent.AutoContinueReasonRateLimit),
		Content:       autoContinueContent,
		DueAt:         time.Now().UTC().Add(100 * time.Millisecond),
		JitterMs:      0,
		NextBackoffMs: 0,
		SourcePayload: []byte{},
	}))

	var fired atomic.Int32
	h1 := NewOutputHandler(queries, nil, nil, nil)
	h1.SetSendMessageFunc(func(agentID, content string) {
		if agentID == "agent-1" && content == autoContinueContent {
			fired.Add(1)
		}
	})
	h1.restoreAutoContinueSchedules()
	require.Eventually(t, func() bool { return fired.Load() == 1 }, 2*time.Second, 25*time.Millisecond)

	h2 := NewOutputHandler(queries, nil, nil, nil)
	h2.SetSendMessageFunc(func(agentID, content string) {
		if agentID == "agent-1" && content == autoContinueContent {
			fired.Add(1)
		}
	})
	h2.restoreAutoContinueSchedules()
	time.Sleep(250 * time.Millisecond)

	assert.Equal(t, int32(1), fired.Load())
}
