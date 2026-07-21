package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/sqltime"
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
		DueAt:         sqltime.NewSQLiteTime(time.Now().UTC().Add(100 * time.Millisecond)),
		JitterMs:      0,
		NextBackoffMs: 0,
		SourcePayload: []byte{},
	}))

	var fired atomic.Int32
	h2 := NewOutputHandler(nil, queries, nil, nil, nil)
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

	h := NewOutputHandler(nil, queries, nil, nil, nil)
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

	h := NewOutputHandler(nil, queries, nil, nil, nil)
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

	h := NewOutputHandler(nil, queries, nil, nil, nil)
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

// TestAutoContinueSchedule_ArmedDueAtSurvivesDBRoundtrip pins the contract
// between the schedule builders and fireAutoContinue's DueAt.Equal guard: the
// dueAt a builder hands back for arming the in-memory timer must equal the DB
// roundtrip of the record it built. due_at is stored at millisecond precision
// (SQLiteTime floors due_at on bind), so the builders truncate to the millisecond
// before binding; without that, every live-armed dueAt carries sub-millisecond
// residue the storage floors away, the Equal guard rejects every firing, and
// auto-continue silently never fires on the live path. The restore-path tests
// above can't catch this: they arm from a DB readback, which is already on the
// millisecond grid.
func TestAutoContinueSchedule_ArmedDueAtSurvivesDBRoundtrip(t *testing.T) {
	_, queries := setupTestDB(t)
	createAutoContinueTestAgent(t, queries, "agent-1")
	h := NewOutputHandler(nil, queries, nil, nil, nil)

	// Sub-millisecond residue plus a non-UTC zone: the shape every live
	// scheduling call has (time.Now() carries nanosecond resolution).
	zone := time.FixedZone("UTC+9", 9*60*60)
	now := time.Now().In(zone).Truncate(time.Millisecond).Add(123_456 * time.Nanosecond)

	for _, reason := range []agent.AutoContinueReason{
		agent.AutoContinueReasonAPIError,
		agent.AutoContinueReasonRateLimit,
	} {
		record, dueAt, err := h.buildAutoContinueRecord("agent-1", agent.AutoContinueSchedule{
			Reason: reason,
			DueAt:  now.Add(time.Hour),
		}, now)
		require.NoError(t, err, reason)
		assert.True(t, dueAt.Equal(dueAt.Truncate(time.Millisecond)),
			"%s: armed dueAt %s carries sub-millisecond residue the storage would floor away", reason, dueAt)

		require.NoError(t, queries.UpsertAutoContinueSchedule(bgCtx(), record))
		row, err := queries.GetAutoContinueSchedule(bgCtx(), db.GetAutoContinueScheduleParams{
			AgentID: "agent-1",
			Reason:  string(reason),
		})
		require.NoError(t, err)
		assert.True(t, row.DueAt.Equal(dueAt),
			"%s: stored due_at %s != armed dueAt %s -- fireAutoContinue's DueAt.Equal guard would reject the firing",
			reason, row.DueAt, dueAt)
	}
}

func TestAutoContinueSchedule_FiresOnceAndDoesNotRefireAfterRestart(t *testing.T) {
	_, queries := setupTestDB(t)
	createAutoContinueTestAgent(t, queries, "agent-1")

	require.NoError(t, queries.UpsertAutoContinueSchedule(bgCtx(), db.UpsertAutoContinueScheduleParams{
		AgentID:       "agent-1",
		Reason:        string(agent.AutoContinueReasonRateLimit),
		Content:       autoContinueContent,
		DueAt:         sqltime.NewSQLiteTime(time.Now().UTC().Add(100 * time.Millisecond)),
		JitterMs:      0,
		NextBackoffMs: 0,
		SourcePayload: []byte{},
	}))

	var fired atomic.Int32
	h1 := NewOutputHandler(nil, queries, nil, nil, nil)
	h1.SetSendMessageFunc(func(agentID, content string) {
		if agentID == "agent-1" && content == autoContinueContent {
			fired.Add(1)
		}
	})
	h1.restoreAutoContinueSchedules()
	require.Eventually(t, func() bool { return fired.Load() == 1 }, 2*time.Second, 25*time.Millisecond)

	h2 := NewOutputHandler(nil, queries, nil, nil, nil)
	h2.SetSendMessageFunc(func(agentID, content string) {
		if agentID == "agent-1" && content == autoContinueContent {
			fired.Add(1)
		}
	})
	h2.restoreAutoContinueSchedules()
	time.Sleep(250 * time.Millisecond)

	assert.Equal(t, int32(1), fired.Load())
}
