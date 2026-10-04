package service

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func seedArchivedChildForResume(t *testing.T, svc *Service, rootID, parentID, childID, rowKey string, seq int64, status bgtask.Status) {
	t.Helper()
	ctx := t.Context()
	parent, err := svc.Queries.GetAgentByID(ctx, parentID)
	require.NoError(t, err)
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: childID, ParentAgentID: sql.NullString{String: parentID, Valid: true},
		SpawnSpanID: rowKey + "-span", WorkingDir: parent.WorkingDir,
		HomeDir: parent.HomeDir, Title: rowKey + " title", AgentProvider: parent.AgentProvider,
	}))
	_, err = svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID: childID + "-message", AgentID: childID,
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: []byte(childID + " archived answer"),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:                  parent.AgentProvider,
		CreatedAt:                      sqltime.NewSQLiteTime(nowMillis()),
	})
	require.NoError(t, err)
	registryParentID := ""
	if parentID != rootID {
		registryParentID = parentID
	}
	stamp := sqltime.NewSQLiteTime(nowMillis())
	require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: rootID, RowKey: rowKey, Seq: seq,
		Kind:         leapmuxv1.BackgroundTaskKind(bgtask.KindSubagent),
		ChildAgentID: childID, ParentAgentID: registryParentID,
		Title: rowKey + " title", Description: "archived child",
		Status: leapmuxv1.BackgroundTaskStatus(status), CreatedAt: stamp, UpdatedAt: stamp,
	}))
}

func TestResumeCopiesNestedChildTranscriptsAndRegistry(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "old-root", workingDir, "session-a1", provider, false)
	seedArchivedChildForResume(t, svc, "old-root", "old-root", "old-child", "first", 1, bgtask.StatusCompleted)
	seedArchivedChildForResume(t, svc, "old-root", "old-child", "old-grandchild", "second", 2, bgtask.StatusInterrupted)
	for _, id := range []string{"old-grandchild", "old-child", "old-root"} {
		_, err := svc.Queries.CloseAgent(t.Context(), id)
		require.NoError(t, err)
	}

	newRootID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(t.Context(), db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: newRootID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 2, "the reopened root keeps both child registry rows")
	byKey := map[string]db.AgentBackgroundTask{}
	for _, row := range rows {
		byKey[row.RowKey] = row
	}
	child := byKey["first"]
	grandchild := byKey["second"]
	require.NotEmpty(t, child.ChildAgentID)
	require.NotEmpty(t, grandchild.ChildAgentID)
	assert.NotEqual(t, "old-child", child.ChildAgentID)
	assert.NotEqual(t, "old-grandchild", grandchild.ChildAgentID)
	assert.Empty(t, child.ParentAgentID)
	assert.Equal(t, child.ChildAgentID, grandchild.ParentAgentID)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusCompleted), child.Status)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted), grandchild.Status)
	assert.False(t, bgtask.Status(child.Status).IsWorking())
	assert.False(t, bgtask.Status(grandchild.Status).IsWorking())
	for _, pair := range []struct{ newID, oldID, parentID, rowKey string }{
		{child.ChildAgentID, "old-child", newRootID, "first"},
		{grandchild.ChildAgentID, "old-grandchild", child.ChildAgentID, "second"},
	} {
		newChild, getErr := svc.Queries.GetAgentByID(t.Context(), pair.newID)
		require.NoError(t, getErr)
		assert.False(t, newChild.ClosedAt.Valid)
		assert.Equal(t, pair.parentID, newChild.ParentAgentID.String)
		assert.Equal(t, pair.rowKey+"-span", newChild.SpawnSpanID)
		assert.Equal(t, pair.rowKey+" title", newChild.Title)
		rootID, rootErr := svc.Queries.GetRootAgentID(t.Context(), pair.newID)
		require.NoError(t, rootErr)
		assert.Equal(t, newRootID, rootID)
		messages, messagesErr := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{
			AgentID: pair.newID, Seq: 0,
		})
		require.NoError(t, messagesErr)
		require.Len(t, messages, 1)
		assert.Equal(t, []byte(pair.oldID+" archived answer"), messages[0].Content)
		assert.Equal(t, pair.newID+":1", messages[0].ID)
		oldChild, oldErr := svc.Queries.GetAgentByID(t.Context(), pair.oldID)
		require.NoError(t, oldErr)
		assert.True(t, oldChild.ClosedAt.Valid)
	}

	_, err = svc.Queries.DeleteClosedAgentsBefore(t.Context(), sqltime.SQLiteNullTimeOf(time.Now().Add(time.Hour)))
	require.NoError(t, err)
	_, err = svc.Queries.GetAgentByID(t.Context(), "old-root")
	require.ErrorIs(t, err, sql.ErrNoRows)
	retained, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(t.Context(), db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: newRootID, Limit: 10,
	})
	require.NoError(t, err)
	assert.Len(t, retained, 2, "old-root retention cannot remove the reopened child registry")
	for _, childID := range []string{child.ChildAgentID, grandchild.ChildAgentID} {
		_, err := svc.Queries.GetAgentByID(t.Context(), childID)
		require.NoError(t, err)
	}
}

func TestResumeRejectsBrokenArchivedChildLinkWithoutCreatingARoot(t *testing.T) {
	t.Parallel()
	svc, _ := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "old-root", workingDir, "session-a1", provider, false)
	_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
		ID: "root-message", AgentID: "old-root", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:                        []byte("prior input"),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:                  provider,
		CreatedAt:                      sqltime.NewSQLiteTime(nowMillis()),
	})
	require.NoError(t, err)
	stamp := sqltime.NewSQLiteTime(nowMillis())
	require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(t.Context(), db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "old-root", RowKey: "broken", Seq: 1,
		Kind:         leapmuxv1.BackgroundTaskKind(bgtask.KindSubagent),
		ChildAgentID: "missing-child", Status: leapmuxv1.BackgroundTaskStatus(bgtask.StatusCompleted),
		CreatedAt: stamp, UpdatedAt: stamp,
	}))
	_, err = svc.Queries.CloseAgent(t.Context(), "old-root")
	require.NoError(t, err)

	err = svc.createAgentRecordWithTranscript(t.Context(), db.CreateAgentParams{
		ID: "new-root", WorkingDir: workingDir, HomeDir: svc.HomeDir,
		AgentProvider: provider, Resumed: 1,
	}, "session-a1")
	require.ErrorContains(t, err, "child")
	_, err = svc.Queries.GetAgentByID(t.Context(), "new-root")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestRepeatedResumeCopiesTheNewestChildTree(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "old-root", workingDir, "session-a1", provider, false)
	seedArchivedChildForResume(t, svc, "old-root", "old-root", "old-child", "first", 1, bgtask.StatusCompleted)
	for _, id := range []string{"old-child", "old-root"} {
		_, err := svc.Queries.CloseAgent(t.Context(), id)
		require.NoError(t, err)
	}
	firstRootID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	firstRows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(t.Context(), db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: firstRootID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, firstRows, 1)
	firstChildID := firstRows[0].ChildAgentID
	require.NotEmpty(t, firstChildID)
	require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
		ID: firstRootID, AgentSessionID: "session-a1",
	}))
	for _, id := range []string{firstChildID, firstRootID} {
		_, err := svc.Queries.CloseAgent(t.Context(), id)
		require.NoError(t, err)
	}
	_, err = svc.DB.ExecContext(t.Context(), "UPDATE agents SET closed_at = ? WHERE id = ?", "2026-01-01T00:00:00.000Z", "old-root")
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), "UPDATE agents SET closed_at = ? WHERE id = ?", "2026-01-02T00:00:00.000Z", firstRootID)
	require.NoError(t, err)

	secondRootID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	secondRows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(t.Context(), db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: secondRootID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, secondRows, 1)
	secondChildID := secondRows[0].ChildAgentID
	require.NotEmpty(t, secondChildID)
	assert.NotEqual(t, firstChildID, secondChildID)
	assert.NotEqual(t, "old-child", secondChildID)
	assert.Equal(t, secondRootID, secondRows[0].OwnerAgentID)
	messages, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{
		AgentID: secondChildID, Seq: 0,
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, []byte("old-child archived answer"), messages[0].Content)
}

func TestResumeKeepsChildRegistryRowsPastTheDisplayCap(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "old-root", workingDir, "session-a1", provider, false)
	for seq := 1; seq <= bgtask.MaxTasks+1; seq++ {
		childID := fmt.Sprintf("old-child-%03d", seq)
		seedArchivedChildForResume(t, svc, "old-root", "old-root", childID, fmt.Sprintf("row-%03d", seq), int64(seq), bgtask.StatusCompleted)
		_, err := svc.Queries.CloseAgent(t.Context(), childID)
		require.NoError(t, err)
	}
	_, err := svc.Queries.CloseAgent(t.Context(), "old-root")
	require.NoError(t, err)

	newRootID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(t.Context(), db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: newRootID, Limit: int64(bgtask.MaxTasks + 1),
	})
	require.NoError(t, err)
	require.Len(t, rows, bgtask.MaxTasks+1, "the clone does not trim child history to the sidebar cap")
	oldest := rows[len(rows)-1]
	assert.Equal(t, "row-001", oldest.RowKey)
	assert.NotEqual(t, "old-child-001", oldest.ChildAgentID)
	messages, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{
		AgentID: oldest.ChildAgentID, Seq: 0,
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, []byte("old-child-001 archived answer"), messages[0].Content)
}

func TestResumeFinalizesAnActiveArchivedChild(t *testing.T) {
	t.Parallel()
	for _, status := range []bgtask.Status{bgtask.StatusPending, bgtask.StatusRunning, bgtask.StatusPaused} {
		t.Run(status.String(), func(t *testing.T) {
			t.Parallel()
			svc, dispatcher := transcriptResumeService(t)
			workingDir := t.TempDir()
			provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
			seedTranscriptSource(t, svc, "old-root", workingDir, "session-a1", provider, false)
			seedArchivedChildForResume(t, svc, "old-root", "old-root", "old-child", "first", 1, status)
			for _, id := range []string{"old-child", "old-root"} {
				_, err := svc.Queries.CloseAgent(t.Context(), id)
				require.NoError(t, err)
			}

			newRootID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
			rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(t.Context(), db.ListAgentBackgroundTasksNewestFirstParams{
				OwnerAgentID: newRootID, Limit: 10,
			})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted), rows[0].Status)
			assert.True(t, rows[0].EndedAt.Valid, "an archived child has an end time")
			assert.False(t, bgtask.Status(rows[0].Status).IsWorking())
			oldRows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(t.Context(), db.ListAgentBackgroundTasksNewestFirstParams{
				OwnerAgentID: "old-root", Limit: 10,
			})
			require.NoError(t, err)
			require.Len(t, oldRows, 1)
			assert.Equal(t, leapmuxv1.BackgroundTaskStatus(status), oldRows[0].Status, "the copy leaves old history unchanged")
		})
	}
}
