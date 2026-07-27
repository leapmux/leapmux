package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// newOrphanReconcilerHarness builds a worker DB + FileTabPathStore +
// reconciler and returns the lever to inject the hub's view via a
// mutable response the test owns.
//
// The injector takes the owner scope first because a hub response is only
// authoritative for the owner it names: `setFake("user-1", nil, nil)` means
// "user-1 has no tabs left", which is a reap instruction for user-1 and says
// nothing at all about anyone else.
func newOrphanReconcilerHarness(t *testing.T, opts service.OrphanReconcilerOptions) (
	*db.Queries,
	*service.FileTabPathStore,
	*service.OrphanReconciler,
	func(string, []*leapmuxv1.OwnedTab, error),
) {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(sqlDB))
	q := db.New(sqlDB)
	bus := service.NewPrivateEventsBus()
	t.Cleanup(bus.Stop)
	files := service.NewFileTabPathStore(q, bus)

	var (
		fakeResp *leapmuxv1.ListOwnedTabsForWorkerResponse
		fakeErr  error
	)
	listFn := func(_ context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
		return fakeResp, fakeErr
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(testWriter{t: t}, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	rec := service.NewOrphanReconciler(q, files, listFn, opts)
	setFake := func(ownerUserID string, tabs []*leapmuxv1.OwnedTab, err error) {
		fakeResp = &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: ownerUserID, Tabs: tabs}
		fakeErr = err
	}
	return q, files, rec, setFake
}

// testWriter routes slog output through testing.TB.Log so failing
// tests print log lines but passing tests stay quiet.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

func TestOrphanReconciler_FileTab_MissingOnHub_Revoked(t *testing.T) {
	q, files, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	// Local row that the hub no longer knows about.
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost", WorkspaceID: "w1", FilePath: "/r/a.go",
	}))
	setFake("user-1", nil, nil)

	// Manually drive a single pass — Run loop semantics are exercised by
	// the Trigger test below.
	require.NoError(t, runOnce(ctx, rec))

	rows, err := q.ListAllWorkerFileTabs(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "stale local file tab should have been revoked")
}

func TestOrphanReconciler_FileTab_WorkspaceMismatch_Relocated(t *testing.T) {
	q, files, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "f1", WorkspaceID: "w1", FilePath: "/r/a.go",
	}))
	// Hub says this tab is now in w2 (CRDT moved it after a client crash).
	setFake("user-1", []*leapmuxv1.OwnedTab{{
		TabType:     leapmuxv1.TabType_TAB_TYPE_FILE,
		TabId:       "f1",
		WorkspaceId: "w2",
		UserId:      "user-1",
	}}, nil)

	require.NoError(t, runOnce(ctx, rec))

	rows, err := q.ListAllWorkerFileTabs(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "w2", rows[0].WorkspaceID, "local row should track the CRDT workspace_id")
}

func TestOrphanReconciler_Agent_MissingOnHub_Closed(t *testing.T) {
	q, _, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ghost-agent", WorkspaceID: "w1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	agent, err := q.GetAgentByID(ctx, "ghost-agent")
	require.NoError(t, err)
	assert.True(t, agent.ClosedAt.Valid, "stale agent should have been closed locally")
}

func TestOrphanReconciler_Terminal_MissingOnHub_Closed(t *testing.T) {
	q, _, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "ghost-term", WorkspaceID: "w1",
		// screen is NOT NULL; an empty byte slice satisfies the constraint.
		Screen: []byte{},
	}))
	setFake("user-1", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	term, err := q.GetTerminal(ctx, "ghost-term")
	require.NoError(t, err)
	assert.True(t, term.ClosedAt.Valid, "stale terminal should have been closed locally")
}

// fakeAgentStopper records every StopAgent call so tests can assert
// the orphan reconciler dispatched a stop signal alongside the DB
// closed_at update. `found` lets tests simulate the
// already-exited case (agent.Manager.StopAgent returns false then).
type fakeAgentStopper struct {
	stopped []string
	found   bool
}

func (f *fakeAgentStopper) StopAgent(id string) bool {
	f.stopped = append(f.stopped, id)
	return f.found
}

// fakeTerminalStopper mirrors fakeAgentStopper for terminals.
// terminal.Manager.StopTerminal returns no value, so the fake
// matches.
type fakeTerminalStopper struct {
	stopped []string
}

func (f *fakeTerminalStopper) StopTerminal(id string) {
	f.stopped = append(f.stopped, id)
}

// TestOrphanReconciler_Agent_MissingOnHub_StopsInMemory asserts the
// reconciler dispatches a StopAgent call alongside the DB close
// when the hub no longer references the agent's tab. Without this
// hop the live exec.Cmd keeps running until the worker process
// exits — the bug this change closes.
func TestOrphanReconciler_Agent_MissingOnHub_StopsInMemory(t *testing.T) {
	agents := &fakeAgentStopper{found: true}
	q, _, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{Agents: agents})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ghost-agent", WorkspaceID: "w1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	agent, err := q.GetAgentByID(ctx, "ghost-agent")
	require.NoError(t, err)
	assert.True(t, agent.ClosedAt.Valid, "stale agent row must be closed in SQLite")
	assert.Equal(t, []string{"ghost-agent"}, agents.stopped,
		"reconciler must dispatch StopAgent so the exec.Cmd is reaped now, not at worker restart")
}

// TestOrphanReconciler_Terminal_MissingOnHub_StopsInMemory mirrors
// the agent variant for terminal subprocesses (PTY-attached
// shells). Without StopTerminal alongside the DB close, the shell
// keeps holding the PTY until the worker process exits.
func TestOrphanReconciler_Terminal_MissingOnHub_StopsInMemory(t *testing.T) {
	terms := &fakeTerminalStopper{}
	q, _, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{Terminals: terms})
	ctx := context.Background()

	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "ghost-term", WorkspaceID: "w1",
		Screen: []byte{},
	}))
	setFake("user-1", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	term, err := q.GetTerminal(ctx, "ghost-term")
	require.NoError(t, err)
	assert.True(t, term.ClosedAt.Valid, "stale terminal row must be closed in SQLite")
	assert.Equal(t, []string{"ghost-term"}, terms.stopped,
		"reconciler must dispatch StopTerminal so the PTY shell is reaped now")
}

// TestOrphanReconciler_Agent_PresentOnHub_DoesNotStop is the
// don't-overreach test: when the hub still references the agent
// the reconciler must NOT touch the in-memory manager (otherwise
// any live agent would be killed every hour).
func TestOrphanReconciler_Agent_PresentOnHub_DoesNotStop(t *testing.T) {
	agents := &fakeAgentStopper{found: true}
	q, _, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{Agents: agents})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "live-agent", WorkspaceID: "w1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "live-agent", WorkspaceId: "w1"},
	}, nil)

	require.NoError(t, runOnce(ctx, rec))

	agent, err := q.GetAgentByID(ctx, "live-agent")
	require.NoError(t, err)
	assert.False(t, agent.ClosedAt.Valid, "agent still referenced by hub must stay open")
	assert.Empty(t, agents.stopped, "live agent must NOT receive a stop signal")
}

func TestOrphanReconciler_ListError_DoesNotPanic_DoesNotCloseRows(t *testing.T) {
	q, files, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "live", WorkspaceID: "w1", FilePath: "/r/a.go",
	}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "live-agent", WorkspaceID: "w1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, errors.New("hub unavailable"))

	require.NoError(t, runOnce(ctx, rec))

	rows, err := q.ListAllWorkerFileTabs(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "list error must not revoke live rows")
	agent, err := q.GetAgentByID(ctx, "live-agent")
	require.NoError(t, err)
	assert.False(t, agent.ClosedAt.Valid, "list error must not close live agents")
}

func TestOrphanReconciler_TriggerRunsPassImmediately(t *testing.T) {
	q, files, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		// A long interval keeps the tick from racing with Trigger.
		Interval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost", WorkspaceID: "w1", FilePath: "/r/a.go",
	}))
	setFake("user-1", nil, nil)

	go rec.Run(ctx)
	// Run kicks off one pass at start; wait for it to settle.
	require.Eventually(t, func() bool {
		rows, err := q.ListAllWorkerFileTabs(ctx)
		return err == nil && len(rows) == 0
	}, 2*time.Second, 10*time.Millisecond, "startup pass should revoke the orphan")

	// Add another orphan and confirm Trigger fires a fresh pass.
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost2", WorkspaceID: "w1", FilePath: "/r/b.go",
	}))
	rec.Trigger()
	require.Eventually(t, func() bool {
		rows, err := q.ListAllWorkerFileTabs(ctx)
		return err == nil && len(rows) == 0
	}, 2*time.Second, 10*time.Millisecond, "Trigger should run another pass")

	cancel()
	rec.Stop()
}

// runOnce drives a single reconciliation pass by triggering the
// reconciler against a bounded context. The reconciler doesn't
// expose its private `reconcileOnce` method; running through Run
// with an Interval >> test duration gives us one startup pass.
func runOnce(ctx context.Context, rec *service.OrphanReconciler) error {
	passCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		rec.Run(passCtx)
		close(done)
	}()
	// Wait until the startup pass has settled, then cancel to exit Run.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		return errors.New("Run did not exit after cancel")
	}
	return nil
}

// TestOrphanReconciler_FileTab_SharedTabIDStaysWithItsOwner pins the owner axis
// of the reconciler's hub-vs-local comparison.
//
// ListAllWorkerFileTabs walks EVERY user's rows and the hub's owned-tab view is
// keyed by (user_id, tab_id), so two owners can legitimately appear with the
// same client-minted FILE tab id. Keying the comparison by (tab_type, tab_id)
// alone collapses them into one map entry -- last one wins -- and every local
// row is then reconciled against a stranger's hub row: user-a's tab gets
// relocated into user-b's workspace.
func TestOrphanReconciler_FileTab_SharedTabIDStaysWithItsOwner(t *testing.T) {
	q, files, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	const sharedTabID = "file-1700000000000-1"
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: sharedTabID, WorkspaceID: "ws-a", FilePath: "/r/a.go",
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: sharedTabID, WorkspaceID: "ws-b", FilePath: "/r/b.go",
	}))

	// The hub knows both rows. user-a stayed put; user-b was moved to ws-b2.
	// A LISTED row is authoritative for itself regardless of the response's
	// owner scope -- it names its own owner -- so both relocations apply even
	// though the scope names only user-a. Scope gates the absence inference
	// (reaping), not the presence one; see ownerScope in orphan_reconciler.go.
	setFake("user-a", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, WorkspaceId: "ws-a", UserId: "user-a"},
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, WorkspaceId: "ws-b2", UserId: "user-b"},
	}, nil)

	require.NoError(t, runOnce(ctx, rec))

	byUser := fileTabsByUser(t, q, ctx)
	require.Contains(t, byUser, "user-a")
	require.Contains(t, byUser, "user-b")
	assert.Equal(t, "ws-a", byUser["user-a"].WorkspaceID,
		"user-a must be reconciled against user-a's hub row, not user-b's")
	assert.Equal(t, "ws-b2", byUser["user-b"].WorkspaceID,
		"user-b must follow its own hub row's workspace")
}

// TestOrphanReconciler_FileTab_SharedTabIDReapsOnlyTheStaleOwner is the
// destructive half of the same aliasing bug: when only ONE of two owners
// sharing a tab id is still known to the hub, the owner-blind key lets the
// stale row match the live owner's entry -- so it is never reaped, and is
// relocated into the live owner's workspace instead.
//
// The response is scoped to user-b, the owner being reaped: that is what makes
// user-b's absence mean "dropped" rather than "not asked about". It also
// carries user-a's row, which the scope does not cover -- deliberately
// adversarial, so the reap decision cannot lean on the hub having sent only
// in-scope rows.
func TestOrphanReconciler_FileTab_SharedTabIDReapsOnlyTheStaleOwner(t *testing.T) {
	q, files, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	const sharedTabID = "file-1700000000000-1"
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: sharedTabID, WorkspaceID: "ws-a", FilePath: "/r/a.go",
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: sharedTabID, WorkspaceID: "ws-b", FilePath: "/r/b.go",
	}))

	// Only user-a's tab survives at the hub.
	setFake("user-b", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, WorkspaceId: "ws-a", UserId: "user-a"},
	}, nil)

	require.NoError(t, runOnce(ctx, rec))

	byUser := fileTabsByUser(t, q, ctx)
	require.Contains(t, byUser, "user-a", "the hub-known owner's row must survive")
	assert.Equal(t, "ws-a", byUser["user-a"].WorkspaceID,
		"and must not be relocated by a stranger's row")
	assert.NotContains(t, byUser, "user-b",
		"the owner the hub no longer knows about must be reaped, not shielded by the collision")
}

// TestOrphanReconciler_FileTab_OutOfScopeOwnerIsNotReaped pins the invariant
// that makes the hub's owner-scoped ListOwnedTabsForWorker safe.
//
// The hub's query binds user_id, so its response enumerates ONE owner's tabs.
// This reconciler reaps by absence -- "not in the hub list" means "the CRDT
// dropped it" -- and ListAllWorkerFileTabs walks every owner's local rows. Read
// a one-owner list as a universal absence and every other owner's live file
// tabs are destroyed (worktree link dropped, then the row revoked) on the next
// tick. That is a live data-loss bug, not a latent one, which is why the
// response declares its scope and this test holds the line.
func TestOrphanReconciler_FileTab_OutOfScopeOwnerIsNotReaped(t *testing.T) {
	q, files, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: "file-a", WorkspaceID: "ws-a", FilePath: "/r/a.go",
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: "file-b", WorkspaceID: "ws-b", FilePath: "/r/b.go",
	}))
	// Link user-b's tab to a worktree: the reap drops that link FIRST, so an
	// unscoped reap is observable here even before the file-tab row goes.
	require.NoError(t, q.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID: "wt-b", WorktreePath: "/r/b", RepoRoot: "/r", BranchName: "b",
	}))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
		WorktreeID: "wt-b", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-b", UserID: "user-b",
	}))

	// A response scoped to user-a, listing user-a's live tab. It says nothing
	// about user-b -- who is not "absent", merely not asked about.
	setFake("user-a", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file-a", WorkspaceId: "ws-a", UserId: "user-a"},
	}, nil)

	require.NoError(t, runOnce(ctx, rec))

	byUser := fileTabsByUser(t, q, ctx)
	require.Contains(t, byUser, "user-a", "the in-scope owner's listed tab must survive")
	assert.Equal(t, "ws-a", byUser["user-a"].WorkspaceID)
	require.Contains(t, byUser, "user-b",
		"a row owned by a user the response does not cover must be left alone, not reaped")
	assert.Equal(t, "ws-b", byUser["user-b"].WorkspaceID, "and must not be relocated either")

	links, err := q.CountWorktreeTabs(ctx, "wt-b")
	require.NoError(t, err)
	assert.Equal(t, int64(1), links, "the out-of-scope row's worktree link must survive too")
}

// TestOrphanReconciler_UndeclaredScopeReapsNothing covers the degenerate case:
// a response that names no owner is authoritative for nobody, so an empty tab
// list must not be read as "every local tab is an orphan". Fail closed -- a
// missed reap is a leak, an unfounded reap is data loss.
func TestOrphanReconciler_UndeclaredScopeReapsNothing(t *testing.T) {
	q, files, rec, setFake := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		// Warn-level: the skipped pass logs at Warn and the harness default
		// only prints Errors, which would hide it.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: "file-a", WorkspaceID: "ws-a", FilePath: "/r/a.go",
	}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-a", WorkspaceID: "ws-a", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	rows, err := q.ListAllWorkerFileTabs(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "an unscoped response must not revoke file tabs")
	agent, err := q.GetAgentByID(ctx, "agent-a")
	require.NoError(t, err)
	assert.False(t, agent.ClosedAt.Valid, "an unscoped response must not close agents either")
}

// fileTabsByUser indexes every worker_file_tabs row by owner. The tests above
// deliberately share one tab id across owners, so the owner is the only usable
// index key.
func fileTabsByUser(t *testing.T, q *db.Queries, ctx context.Context) map[string]db.WorkerFileTab {
	t.Helper()
	rows, err := q.ListAllWorkerFileTabs(ctx)
	require.NoError(t, err)
	out := make(map[string]db.WorkerFileTab, len(rows))
	for _, r := range rows {
		out[r.UserID] = r
	}
	return out
}
