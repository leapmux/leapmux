package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/db"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// setupTestDB creates an in-memory SQLite database with migrations applied
// and returns the raw *sql.DB and a *gendb.Queries handle.
func setupTestDB(t *testing.T) (*sql.DB, *gendb.Queries) {
	t.Helper()
	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	return sqlDB, gendb.New(sqlDB)
}

func TestCleanup_WorktreeSoftDelete(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()

	// Create a worktree.
	err := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
		ID:           "wt-1",
		WorktreePath: "/tmp/wt1",
		RepoRoot:     "/repo",
		BranchName:   "feature-1",
	})
	require.NoError(t, err)

	// Soft-delete the worktree.
	err = queries.DeleteWorktree(ctx, "wt-1")
	require.NoError(t, err)

	// GetWorktreeByPath should return sql.ErrNoRows (soft-deleted worktree is invisible).
	_, err = queries.GetWorktreeByPath(ctx, "/tmp/wt1")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// GetWorktreeByID should still return the worktree with DeletedAt set.
	wt, err := queries.GetWorktreeByID(ctx, "wt-1")
	require.NoError(t, err)
	assert.True(t, wt.DeletedAt.Valid, "expected DeletedAt to be set after soft delete")
}

func TestCleanup_HardDeleteWorktreesBefore(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	// Create and soft-delete a worktree.
	err := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
		ID:           "wt-old",
		WorktreePath: "/tmp/wt-old",
		RepoRoot:     "/repo",
		BranchName:   "old-branch",
	})
	require.NoError(t, err)

	err = queries.DeleteWorktree(ctx, "wt-old")
	require.NoError(t, err)

	// Backdate deleted_at to 8 days ago via raw SQL.
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	_, err = sqlDB.ExecContext(ctx, "UPDATE worktrees SET deleted_at = ? WHERE id = ?", eightDaysAgo, "wt-old")
	require.NoError(t, err)

	// Hard-delete worktrees older than 7 days.
	cutoff := timefmt.Format(time.Now().Add(-7 * 24 * time.Hour))
	result, err := queries.HardDeleteWorktreesBefore(ctx, cutoff)
	require.NoError(t, err)

	n, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// The worktree should be completely gone.
	_, err = queries.GetWorktreeByID(ctx, "wt-old")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestCleanup_HardDeleteWorktreesBefore_RetainsRecent(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()

	// Create and soft-delete a worktree (recently deleted).
	err := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
		ID:           "wt-recent",
		WorktreePath: "/tmp/wt-recent",
		RepoRoot:     "/repo",
		BranchName:   "recent-branch",
	})
	require.NoError(t, err)

	err = queries.DeleteWorktree(ctx, "wt-recent")
	require.NoError(t, err)

	// Hard-delete worktrees older than 7 days. The recent one should survive.
	cutoff := timefmt.Format(time.Now().Add(-7 * 24 * time.Hour))
	result, err := queries.HardDeleteWorktreesBefore(ctx, cutoff)
	require.NoError(t, err)

	n, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// The worktree should still exist.
	wt, err := queries.GetWorktreeByID(ctx, "wt-recent")
	require.NoError(t, err)
	assert.Equal(t, "wt-recent", wt.ID)
	assert.True(t, wt.DeletedAt.Valid, "expected DeletedAt to still be set")
}

func TestCleanup_WorktreeTabsCascadeOnHardDelete(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	// Create a worktree and add a tab reference.
	err := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
		ID:           "wt-cascade",
		WorktreePath: "/tmp/wt-cascade",
		RepoRoot:     "/repo",
		BranchName:   "cascade-branch",
	})
	require.NoError(t, err)

	err = queries.AddWorktreeTab(ctx, gendb.AddWorktreeTabParams{
		WorktreeID: "wt-cascade",
		TabType:    1, // TAB_TYPE_AGENT
		TabID:      "agent-1",
	})
	require.NoError(t, err)

	// Verify the tab reference exists.
	count, err := queries.CountWorktreeTabs(ctx, "wt-cascade")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Soft-delete the worktree.
	err = queries.DeleteWorktree(ctx, "wt-cascade")
	require.NoError(t, err)

	// Backdate deleted_at to 8 days ago.
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	_, err = sqlDB.ExecContext(ctx, "UPDATE worktrees SET deleted_at = ? WHERE id = ?", eightDaysAgo, "wt-cascade")
	require.NoError(t, err)

	// Hard-delete.
	cutoff := timefmt.Format(time.Now().Add(-7 * 24 * time.Hour))
	result, err := queries.HardDeleteWorktreesBefore(ctx, cutoff)
	require.NoError(t, err)

	n, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// The worktree should be gone.
	_, err = queries.GetWorktreeByID(ctx, "wt-cascade")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// The tab reference should also be gone (FK CASCADE).
	var tabCount int64
	err = sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM worktree_tabs WHERE worktree_id = ?", "wt-cascade").Scan(&tabCount)
	require.NoError(t, err)
	assert.Equal(t, int64(0), tabCount, "expected tab references to be cascade-deleted")
}

// TestCleanup_SweepsSameInstantBoundaries pins the millisecond-exact boundary
// behavior of the three retention sweeps: stored timestamps and cutoffs are
// both canonical (strftime wrap on write, timefmt.Format on the bound cutoff),
// so `col < cutoff` must delete strictly-older rows and keep the exact-cutoff
// and newer rows even when everything shares the same second. The existing
// retention tests use multi-day gaps, which is exactly how the prior
// driver-layout cutoff bind (which missed every same-day row) shipped green.
func TestCleanup_SweepsSameInstantBoundaries(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	cutoffTime := time.Now().UTC().Truncate(time.Millisecond)
	cutoff := timefmt.Format(cutoffTime)

	seedAgent := func(id string, closedAt time.Time) {
		require.NoError(t, queries.CreateAgent(ctx, gendb.CreateAgentParams{
			ID: id, WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/home", Title: id, Options: "{}",
		}))
		_, err := sqlDB.ExecContext(ctx, "UPDATE agents SET closed_at = ? WHERE id = ?", timefmt.Format(closedAt), id)
		require.NoError(t, err)
	}
	seedAgent("agent-expired", cutoffTime.Add(-time.Millisecond))
	seedAgent("agent-at-cutoff", cutoffTime)
	seedAgent("agent-live", cutoffTime.Add(time.Millisecond))

	res, err := queries.DeleteClosedAgentsBefore(ctx, cutoff)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "agents sweep must delete exactly the strictly-older same-second row")
	_, err = queries.GetAgentByID(ctx, "agent-expired")
	assert.ErrorIs(t, err, sql.ErrNoRows)
	for _, keep := range []string{"agent-at-cutoff", "agent-live"} {
		_, err := queries.GetAgentByID(ctx, keep)
		assert.NoError(t, err)
	}

	seedTerminal := func(id string, closedAt time.Time) {
		require.NoError(t, queries.UpsertTerminal(ctx, gendb.UpsertTerminalParams{
			ID: id, WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/home", Shell: "/bin/zsh",
			Title: id, Cols: 80, Rows: 24, Screen: []byte{},
			ClosedAt: sql.NullTime{Time: closedAt, Valid: true},
		}))
	}
	seedTerminal("term-expired", cutoffTime.Add(-time.Millisecond))
	seedTerminal("term-at-cutoff", cutoffTime)
	seedTerminal("term-live", cutoffTime.Add(time.Millisecond))

	res, err = queries.DeleteClosedTerminalsBefore(ctx, cutoff)
	require.NoError(t, err)
	n, err = res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "terminals sweep must delete exactly the strictly-older same-second row")

	seedWorktree := func(id string, deletedAt time.Time) {
		require.NoError(t, queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
			ID: id, WorktreePath: "/tmp/" + id, RepoRoot: "/repo", BranchName: id,
		}))
		_, err := sqlDB.ExecContext(ctx, "UPDATE worktrees SET deleted_at = ? WHERE id = ?", timefmt.Format(deletedAt), id)
		require.NoError(t, err)
	}
	seedWorktree("wt-expired", cutoffTime.Add(-time.Millisecond))
	seedWorktree("wt-at-cutoff", cutoffTime)
	seedWorktree("wt-live", cutoffTime.Add(time.Millisecond))

	res, err = queries.HardDeleteWorktreesBefore(ctx, cutoff)
	require.NoError(t, err)
	n, err = res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "worktrees sweep must delete exactly the strictly-older same-second row")
}
