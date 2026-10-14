package db_test

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/db"
	queries "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteDeadWorktreeTabs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Migrate(ctx, sqlDB))
	q := queries.New(sqlDB)
	for _, wt := range []string{"target", "other", "empty"} {
		_, err := q.CreateWorktree(ctx, queries.CreateWorktreeParams{ID: wt, WorktreePath: "/" + wt, RepoRoot: "/repo", BranchName: wt})
		require.NoError(t, err)
	}
	require.NoError(t, q.CreateAgent(ctx, queries.CreateAgentParams{ID: "live-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	require.NoError(t, q.UpsertTerminal(ctx, queries.UpsertTerminalParams{ID: "live-terminal", Screen: []byte{}}))
	require.NoError(t, q.UpsertWorkerTabPayload(ctx, queries.UpsertWorkerTabPayloadParams{UserID: "owner-b", TabID: "shared", TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE), Payload: []byte{}}))
	links := []queries.AddWorktreeTabParams{
		{WorktreeID: "target", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "dead-agent"},
		{WorktreeID: "target", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "live-agent"},
		{WorktreeID: "target", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "live-terminal"},
		{WorktreeID: "target", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "shared", UserID: "owner-a"},
		{WorktreeID: "target", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "shared", UserID: "owner-b"},
		{WorktreeID: "other", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "dead-agent"},
		{WorktreeID: "target", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "live-agent", UserID: "owner-a"},
		{WorktreeID: "target", TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabID: "live-terminal", UserID: "owner-a"},
		{WorktreeID: "target", TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, TabID: "shared", UserID: "owner-b"},
	}
	for _, link := range links {
		require.NoError(t, q.AddWorktreeTab(ctx, link))
	}
	for range 2 {
		require.NoError(t, q.DeleteDeadWorktreeTabs(ctx, "target"))
		require.NoError(t, q.DeleteDeadWorktreeTabs(ctx, "empty"))
		require.NoError(t, q.DeleteDeadWorktreeTabs(ctx, "absent"))
		count, err := q.CountWorktreeTabs(ctx, "target")
		require.NoError(t, err)
		assert.Equal(t, int64(3), count)
		count, err = q.CountWorktreeTabs(ctx, "other")
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
		wt, err := q.GetWorktreeForTab(ctx, queries.GetWorktreeForTabParams{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "shared", UserID: "owner-b"})
		require.NoError(t, err)
		assert.Equal(t, "target", wt.ID)
		stored, err := q.GetWorktreeByID(ctx, "target")
		require.NoError(t, err)
		assert.False(t, stored.DeletedAt.Valid)
	}
}
