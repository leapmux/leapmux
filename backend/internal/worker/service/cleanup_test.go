package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/sqltime"
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

	err = db.Migrate(context.Background(), sqlDB)
	require.NoError(t, err)

	return sqlDB, gendb.New(sqlDB)
}

func TestCleanup_WorktreeSoftDelete(t *testing.T) {
	t.Parallel()

	_, queries := setupTestDB(t)
	ctx := context.Background()

	// Create a worktree.
	_, err := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
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
	t.Parallel()

	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	// Create and soft-delete a worktree.
	_, err := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
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
	cutoff := sqltime.SQLiteNullTimeOf(time.Now().Add(-7 * 24 * time.Hour))
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
	t.Parallel()

	_, queries := setupTestDB(t)
	ctx := context.Background()

	// Create and soft-delete a worktree (recently deleted).
	_, err := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
		ID:           "wt-recent",
		WorktreePath: "/tmp/wt-recent",
		RepoRoot:     "/repo",
		BranchName:   "recent-branch",
	})
	require.NoError(t, err)

	err = queries.DeleteWorktree(ctx, "wt-recent")
	require.NoError(t, err)

	// Hard-delete worktrees older than 7 days. The recent one should survive.
	cutoff := sqltime.SQLiteNullTimeOf(time.Now().Add(-7 * 24 * time.Hour))
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
	t.Parallel()

	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	// Create a worktree and add a tab reference.
	_, err := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
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
	cutoff := sqltime.SQLiteNullTimeOf(time.Now().Add(-7 * 24 * time.Hour))
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
// both canonical (SQLiteTime/SQLiteNullTime binds floor to the millisecond and
// serialize the canonical layout for both the stored rows and the cutoff),
// so `col < cutoff` must delete strictly-older rows and keep the exact-cutoff
// and newer rows even when everything shares the same second. The existing
// retention tests use multi-day gaps, which is exactly how the prior
// driver-layout cutoff bind (which missed every same-day row) shipped green.
func TestCleanup_SweepsSameInstantBoundaries(t *testing.T) {
	t.Parallel()

	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	cutoffTime := time.Now().UTC().Truncate(time.Millisecond)
	cutoff := sqltime.SQLiteNullTimeOf(cutoffTime)

	seedAgent := func(id string, closedAt time.Time) {
		require.NoError(t, queries.CreateAgent(ctx, gendb.CreateAgentParams{
			ID: id, WorkingDir: "/tmp", HomeDir: "/home", Title: id, Options: "{}",
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
			ID: id, WorkingDir: "/tmp", HomeDir: "/home", Shell: "/bin/zsh",
			Title: id, Cols: 80, Rows: 24, Screen: []byte{},
			ClosedAt: sqltime.SQLiteNullTimeOf(closedAt),
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
		_, cwErr := queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
			ID: id, WorktreePath: "/tmp/" + id, RepoRoot: "/repo", BranchName: id,
		})
		require.NoError(t, cwErr)
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

// TestCloseAgentAndCloseTerminalAreIdempotent is the regression for a leak that
// made the retention sweep unreachable.
//
// The orphan reconciler re-closes rows it finds absent from the hub's list, and
// ListAllAgentIDs / ListAllTerminalIDs return closed rows too -- so without
// `AND closed_at IS NULL` every hourly pass re-stamped closed_at = now. The
// retention delete matches `closed_at < cutoff`, so a row whose timestamp keeps
// advancing can never fall past the cutoff: the rows, their cascaded messages,
// and the terminals' 100KB screen blobs accumulate for the machine's lifetime.
//
// Asserted by planting an OLD closed_at and re-closing: comparing two rapid
// closes would be flaky, since the timestamps could round to the same value and
// pass either way.
func TestCloseAgentAndCloseTerminalAreIdempotent(t *testing.T) {
	t.Parallel()

	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	old := timefmt.Format(time.Now().Add(-30 * 24 * time.Hour))

	require.NoError(t, queries.CreateAgent(ctx, gendb.CreateAgentParams{ID: "agent-1", WorkingDir: "/tmp"}))
	require.NoError(t, queries.UpsertTerminal(ctx, gendb.UpsertTerminalParams{
		ID: "term-1", Cols: 80, Rows: 24, Screen: []byte("screen"),
	}))
	_, err := sqlDB.ExecContext(ctx, `UPDATE agents SET closed_at = ? WHERE id = ?`, old, "agent-1")
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx, `UPDATE terminals SET closed_at = ? WHERE id = ?`, old, "term-1")
	require.NoError(t, err)

	// What the reconciler does on every pass for a row the hub no longer lists.
	require.NoError(t, closeErr(queries.CloseAgent(ctx, "agent-1")))
	require.NoError(t, closeErr(queries.CloseTerminal(ctx, "term-1")))

	agent, err := queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, old, timefmt.Format(agent.ClosedAt.Time),
		"re-closing must not advance closed_at, or the retention sweep can never reclaim this row")

	terminal, err := queries.GetTerminal(ctx, "term-1")
	require.NoError(t, err)
	assert.Equal(t, old, timefmt.Format(terminal.ClosedAt.Time),
		"same for terminals, whose rows carry the screen blob")

	// And the row is now genuinely reachable by retention, which is the point.
	cutoff := sqltime.SQLiteNullTimeOf(time.Now().Add(-cleanupRetention))
	res, err := queries.DeleteClosedTerminalsBefore(ctx, cutoff)
	require.NoError(t, err)
	affected, err := res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected, "the terminal must fall past the retention cutoff")
}

// closeErr discards the affected-row count from an :execresult tab close and
// returns just the error, so tests that only need the side effect can keep using
// require.NoError. CloseAgent / CloseTerminal report that count because
// closeTabCommon uses it to tell a live close from a re-close of an
// already-closed row.
func closeErr(_ sql.Result, err error) error { return err }

// TestCleanupRegistry_CloseDuringStartupWindowStillRetiresTheResource pins the
// mechanism that lets the orphan reconciler read only OPEN rows.
//
// A tab's row becomes durable (and therefore reapable) BEFORE spawnControlIPC
// registers its cleanup. A close landing in that window used to make run() a
// silent no-op, and the cleanup registered afterwards was fired by nobody: the
// tab's unix-socket listener stayed open and its delegation token unrevoked for
// the life of the worker process. The reconciler covered for it only by
// re-tearing-down closed rows on every pass.
func TestCleanupRegistry_CloseDuringStartupWindowStillRetiresTheResource(t *testing.T) {
	t.Parallel()

	var reg cleanupRegistry
	ran := 0

	// Row is durable; the cleanup did not arrive yet.
	reg.claim("tab-1")
	// A reap lands in the window.
	reg.closeTab("tab-1")
	require.Equal(t, 0, ran, "nothing to retire yet -- the resource does not exist")

	// The spawn finishes and registers. It must be retired IMMEDIATELY rather
	// than stored for a close that already happened.
	reg.register("tab-1", func() { ran++ })
	assert.Equal(t, 1, ran, "a cleanup arriving after its tab was closed must fire at once, not sit unreachable")

	// ...and only once: a later close must not double-fire it.
	reg.closeTab("tab-1")
	assert.Equal(t, 1, ran, "the cleanup must not run twice")
}

// TestCleanupRegistry_CloseAfterTheCleanupLandedStillTombstonesTheTab covers the
// OTHER order, which is the one closeTab exists for.
//
// A close runs its teardown BEFORE it stamps closed_at. A relaunch that lands
// between the two reads an open row, passes every guard, mints a socket and
// registers it -- and nothing runs that cleanup again, because the close already
// fired. Marking the tab on the way out is what makes that register retire the
// socket instead of storing it for a tab no client can see.
func TestCleanupRegistry_CloseAfterTheCleanupLandedStillTombstonesTheTab(t *testing.T) {
	t.Parallel()

	var reg cleanupRegistry
	first, second := 0, 0

	reg.claim("tab-1")
	reg.register("tab-1", func() { first++ })
	reg.closeTab("tab-1")
	require.Equal(t, 1, first, "the close retires the live cleanup")

	// The relaunch that raced the close.
	reg.replace("tab-1")
	reg.register("tab-1", func() { second++ })
	assert.Equal(t, 1, second,
		"a socket minted for a tab whose close already ran must be retired at once, not stored for nobody")
}

// TestCleanupRegistry_RetireIsNotAClose pins the half that the unconditional
// mark must NOT reach.
//
// A startup that rolls itself back retires its own resource, but the tab stays
// open and the user can restart it. Tombstoning there would make the next
// restart's register retire the socket it had just minted, so the process would
// come up with no remote control -- the exact failure the mark exists to
// prevent, caused by the mark.
func TestCleanupRegistry_RetireIsNotAClose(t *testing.T) {
	t.Parallel()

	var reg cleanupRegistry
	rolledBack, relaunched := 0, 0

	reg.claim("tab-1")
	reg.register("tab-1", func() { rolledBack++ })
	reg.retire("tab-1")
	require.Equal(t, 1, rolledBack, "the rollback retires its own resource")

	// The user restarts the tab. Its socket must survive registration.
	reg.claim("tab-1")
	reg.register("tab-1", func() { relaunched++ })
	assert.Zero(t, relaunched,
		"a rollback tombstoned a live tab; the restart retired the socket it had just minted")

	reg.closeTab("tab-1")
	assert.Equal(t, 1, relaunched, "the real close retires it")
}

// TestCleanupRegistry_NormalOrderAndAbandonedClaim covers the two paths that
// must NOT trigger the window behaviour: the ordinary
// register-then-run sequence, and a spawn that aborts before registering.
func TestCleanupRegistry_NormalOrderAndAbandonedClaim(t *testing.T) {
	t.Parallel()

	var reg cleanupRegistry

	// Ordinary order: claim, register, then close.
	ran := 0
	reg.claim("tab-ok")
	reg.register("tab-ok", func() { ran++ })
	assert.Equal(t, 0, ran, "registering must not fire the cleanup on its own")
	reg.closeTab("tab-ok")
	assert.Equal(t, 1, ran, "the close fires it")

	// A spawn that aborts before it registers leaves nothing behind, so a
	// rollback for that id retires nothing and tombstones nothing.
	reg.claim("tab-aborted")
	reg.abandonClaim("tab-aborted")
	reg.retire("tab-aborted")
	late := 0
	reg.register("tab-aborted", func() { late++ })
	assert.Equal(t, 0, late,
		"a rollback for a spawn that registered nothing must leave the tab live -- the user can still restart it")
}

// TestCleanup_DeleteClosedAgentsReclaimsChildAlongsideRoot pins that a CLOSED
// child agent row is reclaimed by the same retention sweep that deletes its
// root. Children soft-close with the tree (their closed_at is stamped when the
// root closes), and agents.parent_agent_id carries ON DELETE CASCADE, so once
// the sweep deletes the root the child is removed by the cascade -- it does not
// strand behind a parent that no longer exists.
//
// Both rows are backdated past the retention window; after the sweep BOTH must
// be gone. (RowsAffected reports only the explicit delete; the child is removed
// by the FK cascade, so the count is not a reliable signal here -- row
// existence is.)
func TestCleanup_DeleteClosedAgentsReclaimsChildAlongsideRoot(t *testing.T) {
	t.Parallel()

	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	// Root + child, linked via parent_agent_id.
	require.NoError(t, queries.CreateAgent(ctx, gendb.CreateAgentParams{
		ID: "root-closed", WorkingDir: "/tmp", HomeDir: "/home",
	}))
	require.NoError(t, queries.CreateChildAgent(ctx, gendb.CreateChildAgentParams{
		ID:            "child-closed",
		ParentAgentID: sql.NullString{String: "root-closed", Valid: true},
		SpawnSpanID:   "span-1",
		WorkingDir:    "/tmp",
		HomeDir:       "/home",
	}))

	// Backdate BOTH rows' closed_at past the retention window. A child soft-
	// closes with its tree, so both are stamped when the root closes.
	old := time.Now().UTC().Add(-(cleanupRetention + 24*time.Hour)).Format("2006-01-02T15:04:05.000Z")
	_, err := sqlDB.ExecContext(ctx, `UPDATE agents SET closed_at = ? WHERE id IN (?, ?)`,
		old, "root-closed", "child-closed")
	require.NoError(t, err)

	// The retention sweep: same cutoff runCleanup uses.
	cutoff := sqltime.SQLiteNullTimeOf(time.Now().Add(-cleanupRetention))
	_, err = queries.DeleteClosedAgentsBefore(ctx, cutoff)
	require.NoError(t, err)

	// Both rows are gone -- the child does not strand behind a deleted root. The
	// child is reclaimed by the parent_agent_id ON DELETE CASCADE when the root
	// is removed, so a closed tree is fully reclaimed in one sweep.
	_, err = queries.GetAgentByID(ctx, "root-closed")
	assert.ErrorIs(t, err, sql.ErrNoRows, "the closed root must be reclaimed")
	_, err = queries.GetAgentByID(ctx, "child-closed")
	assert.ErrorIs(t, err, sql.ErrNoRows,
		"the closed child must be reclaimed alongside its root via the parent_agent_id cascade, not stranded")
}

// TestCleanup_DeleteClosedAgentsRetainsChildWhenRootRetained is the retention-
// window twin: when the closed tree is NEWER than the cutoff, the sweep retains
// BOTH the root and the child. The child shares the root's retention fate --
// it is not deleted out from under a root the sweep chose to keep.
func TestCleanup_DeleteClosedAgentsRetainsChildWhenRootRetained(t *testing.T) {
	t.Parallel()

	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	require.NoError(t, queries.CreateAgent(ctx, gendb.CreateAgentParams{
		ID: "root-recent", WorkingDir: "/tmp", HomeDir: "/home",
	}))
	require.NoError(t, queries.CreateChildAgent(ctx, gendb.CreateChildAgentParams{
		ID:            "child-recent",
		ParentAgentID: sql.NullString{String: "root-recent", Valid: true},
		SpawnSpanID:   "span-2",
		WorkingDir:    "/tmp",
		HomeDir:       "/home",
	}))

	// Stamp closed_at just inside the retention window (newer than the cutoff).
	recent := time.Now().UTC().Add(-(cleanupRetention - 24*time.Hour)).Format("2006-01-02T15:04:05.000Z")
	_, err := sqlDB.ExecContext(ctx, `UPDATE agents SET closed_at = ? WHERE id IN (?, ?)`,
		recent, "root-recent", "child-recent")
	require.NoError(t, err)

	cutoff := sqltime.SQLiteNullTimeOf(time.Now().Add(-cleanupRetention))
	res, err := queries.DeleteClosedAgentsBefore(ctx, cutoff)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "rows inside the retention window must not be reclaimed")

	for _, id := range []string{"root-recent", "child-recent"} {
		_, err := queries.GetAgentByID(ctx, id)
		assert.NoError(t, err, "%s must survive: it shares its root's retention fate", id)
	}
}
