package service

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildNativeKeyKeepsExactProviderBytesOutsideTheRegistryKey(t *testing.T) {
	t.Parallel()
	svc, sink, _, _ := setupBgTaskTestWithService(t)
	key := strings.Repeat("한😀/", 100)
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: key, Title: "Read the native source"})
	require.NoError(t, err)
	child, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, key, child.ProviderChildKey)
	replayID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: key, SpawnSpanID: "native-call"})
	require.NoError(t, err)
	assert.Equal(t, childID, replayID)
	child, err = svc.Queries.GetChildAgentByProviderKey(t.Context(), db.GetChildAgentByProviderKeyParams{ParentAgentID: sqlString(child.ParentAgentID.String), ProviderChildKey: key})
	require.NoError(t, err)
	assert.Equal(t, "native-call", child.SpawnSpanID)
	linkedID, _, found, err := sink.LookupBackgroundTask(key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, childID, linkedID)
}

func TestChildNativeKeyRejectsARegistryChildOwnedByAnotherParent(t *testing.T) {
	t.Parallel()
	svc, root, _, _ := setupBgTaskTestWithService(t)
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", SpawnSpanID: "spawn", Title: "Native child"})
	require.NoError(t, err)
	_, err = root.ChildSink(childID).EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", SpawnSpanID: "nested-spawn"})
	require.ErrorContains(t, err, "parent")
	child, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, "spawn", child.SpawnSpanID)
	assert.Equal(t, "agent-1", child.ParentAgentID.String)
}

func TestChildNativeKeyCollisionDoesNotPublishOrRelinkTheRegistry(t *testing.T) {
	t.Parallel()
	svc, sink, _, rows := setupBgTaskTestWithService(t)
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-a", SpawnSpanID: "spawn", Title: "First"})
	require.NoError(t, err)
	_, err = sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-b", SpawnSpanID: "spawn", Title: "Wrong child"})
	require.Error(t, err)
	retained := rows()
	require.Len(t, retained, 1, "a rejected native identity must not create a registry row")
	assert.Equal(t, "native-a", retained[0].RowKey)
	assert.Equal(t, childID, retained[0].ChildAgentID)
	child, err := svc.Queries.GetAgentByID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, "native-a", child.ProviderChildKey)
}

func TestConcurrentUnlinkedNativeChildCreatesOneOwnedAgent(t *testing.T) {
	t.Parallel()
	svc, sink, _, rows := setupBgTaskTestWithService(t)
	type outcome struct {
		id  string
		err error
	}
	results := make(chan outcome, 16)
	var group sync.WaitGroup
	for range cap(results) {
		group.Go(func() {
			id, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", Title: "Read the source"})
			results <- outcome{id: id, err: err}
		})
	}
	group.Wait()
	close(results)
	var childID string
	for result := range results {
		require.NoError(t, result.err)
		if childID == "" {
			childID = result.id
		}
		assert.Equal(t, childID, result.id)
	}
	require.NotEmpty(t, childID)
	assert.Len(t, rows(), 1)
	children, err := svc.Queries.ListAgentDescendantsForResume(t.Context(), sqlString("agent-1"))
	require.NoError(t, err)
	assert.Len(t, children, 1)
}

func TestFailedChildNativeResultWriteAcceptsTheNextValidAttempt(t *testing.T) {
	t.Parallel()
	svc, root, _, _ := setupBgTaskTestWithService(t)
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", Title: "Read the source"})
	require.NoError(t, err)
	child := root.ChildSink(childID)
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_child_divider BEFORE INSERT ON messages WHEN NEW.agent_id <> 'agent-1' BEGIN SELECT RAISE(FAIL, 'the store refused the child divider'); END`)
	require.NoError(t, err)
	content := agent.MessageContent{Original: []byte(`{"type":"result","duration_ms":12}`), AgentSessionID: "native", IdempotencyKey: "turn:1"}
	require.ErrorContains(t, child.PersistTurnEnd(content, agent.SpanInfo{}), "the store refused the child divider")
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_child_divider")
	require.NoError(t, err)
	require.NoError(t, child.PersistTurnEnd(content, agent.SpanInfo{}))
	assert.Len(t, transcriptMessages(t, svc, childID), 1, "the next valid write must store its native result")
	require.NoError(t, root.CloseBackgroundTask("native-child", bgtask.StatusSucceeded))
	assert.Len(t, transcriptMessages(t, svc, childID), 1)
}

func TestChildRegistryLinkFailureReturnsAnErrorAndReplaysTheSameChild(t *testing.T) {
	t.Parallel()
	svc, sink, _, rows := setupBgTaskTestWithService(t)
	_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_child_link BEFORE INSERT ON agent_background_tasks BEGIN SELECT RAISE(FAIL, 'the store refused the child link'); END`)
	require.NoError(t, err)
	spec := agent.ChildAgentSpec{ProviderChildKey: "native-link-child", SpawnSpanID: "native-link-span", Title: "Read the source"}
	_, err = sink.EnsureChildAgent(spec)
	require.ErrorContains(t, err, "the store refused the child link")
	assert.Empty(t, rows())
	child, err := svc.Queries.GetChildAgentByProviderKey(t.Context(), db.GetChildAgentByProviderKeyParams{ParentAgentID: sqlString("agent-1"), ProviderChildKey: spec.ProviderChildKey})
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_child_link")
	require.NoError(t, err)
	replayID, err := sink.EnsureChildAgent(spec)
	require.NoError(t, err)
	assert.Equal(t, child.ID, replayID)
	linked := rows()
	require.Len(t, linked, 1)
	assert.Equal(t, child.ID, linked[0].ChildAgentID)
	children, err := svc.Queries.ListAgentDescendantsForResume(t.Context(), sqlString("agent-1"))
	require.NoError(t, err)
	assert.Len(t, children, 1)
}

func TestFailedBackgroundTaskUpsertAtCapPreservesStoreAndSidebar(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, rows := setupBgTaskTestWithService(t)
	for index := range bgtask.MaxTasks {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("native-shell-%d", index), Kind: bgtask.KindShell,
			Title: "Read the source", Status: bgtask.StatusRunning,
		}))
	}
	stored := rows()
	displayed, err := svc.Output.LoadBackgroundTasks(t.Context(), ownerID)
	require.NoError(t, err)
	require.Len(t, displayed, int(bgtask.MaxTasks))
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_new_task BEFORE INSERT ON agent_background_tasks WHEN NEW.row_key = 'refused-native-shell' BEGIN SELECT RAISE(FAIL, 'the store refused the new task'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "refused-native-shell", Kind: bgtask.KindShell,
		Title: "Read more source", Status: bgtask.StatusRunning,
	}), "the store refused the new task")
	assert.Equal(t, stored, rows(), "a refused insert cannot delete the prior durable task")
	after, err := svc.Output.LoadBackgroundTasks(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, displayed, after, "a refused insert cannot change the prior sidebar")
}

func TestFailedChildRegistryLinkAtCapPreservesEveryExistingSidebarRow(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, rows := setupBgTaskTestWithService(t)
	for index := range bgtask.MaxTasks {
		_, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
			ProviderChildKey: fmt.Sprintf("native-child-%d", index), Title: "Read the source",
		})
		require.NoError(t, err)
	}
	stored := rows()
	displayed, err := svc.Output.LoadBackgroundTasks(t.Context(), ownerID)
	require.NoError(t, err)
	require.Len(t, displayed, int(bgtask.MaxTasks))
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_new_child_link BEFORE INSERT ON agent_background_tasks WHEN NEW.row_key = 'refused-native-child' BEGIN SELECT RAISE(FAIL, 'the store refused the new child link'); END`)
	require.NoError(t, err)
	_, err = sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "refused-native-child", Title: "Read more source"})
	require.ErrorContains(t, err, "the store refused the new child link")
	assert.Equal(t, stored, rows(), "existing child transcripts must remain linked")
	after, err := svc.Output.LoadBackgroundTasks(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, displayed, after, "a refused child link cannot remove an existing sidebar row")
}

func TestFailedColdRegistryUpsertReloadsItsExistingStoredRows(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, rows := setupBgTaskTestWithService(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "retained-native-shell", Kind: bgtask.KindShell,
		Title: "Read the source", Status: bgtask.StatusRunning,
	}))
	displayed, err := svc.Output.LoadBackgroundTasks(t.Context(), ownerID)
	require.NoError(t, err)
	stored := rows()
	svc.Output.bgtasks.Delete(ownerID)
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_cold_task BEFORE INSERT ON agent_background_tasks WHEN NEW.row_key = 'refused-cold-shell' BEGIN SELECT RAISE(FAIL, 'the store refused the cold task'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "refused-cold-shell", Kind: bgtask.KindShell,
		Title: "Read more source", Status: bgtask.StatusRunning,
	}), "the store refused the cold task")
	assert.Equal(t, stored, rows())
	after, err := svc.Output.LoadBackgroundTasks(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, displayed, after, "a failed cold write must not mark an empty restored cache as loaded")
}
