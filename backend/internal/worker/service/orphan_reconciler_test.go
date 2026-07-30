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
	*recordingTeardown,
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
	teardown := &recordingTeardown{q: q, files: files}
	if opts.CloseTab == nil {
		opts.CloseTab = teardown.closeTab
	}
	rec := service.NewOrphanReconciler(q, files, listFn, opts)
	setFake := func(ownerUserID string, tabs []*leapmuxv1.OwnedTab, err error) {
		fakeResp = &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: ownerUserID, Tabs: tabs}
		fakeErr = err
	}
	return q, files, rec, setFake, teardown
}

// testWriter routes slog output through testing.TB.Log so failing
// tests print log lines but passing tests stay quiet.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

func TestOrphanReconciler_FileTab_MissingOnHub_Revoked(t *testing.T) {
	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	// Local row that the hub no longer knows about.
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost", FilePath: "/r/a.go",
	}))
	setFake("user-1", nil, nil)

	// Manually drive a single pass — Run loop semantics are exercised by
	// the Trigger test below.
	require.NoError(t, runOnce(ctx, rec))

	rows, err := q.ListAllWorkerFileTabs(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "stale local file tab should have been revoked")
}
func TestOrphanReconciler_Agent_MissingOnHub_Closed(t *testing.T) {
	q, _, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ghost-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	agent, err := q.GetAgentByID(ctx, "ghost-agent")
	require.NoError(t, err)
	assert.True(t, agent.ClosedAt.Valid, "stale agent should have been closed locally")
}

func TestOrphanReconciler_Terminal_MissingOnHub_Closed(t *testing.T) {
	q, _, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:     "ghost-term", // screen is NOT NULL; an empty byte slice satisfies the constraint.
		Screen: []byte{},
	}))
	setFake("user-1", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	term, err := q.GetTerminal(ctx, "ghost-term")
	require.NoError(t, err)
	assert.True(t, term.ClosedAt.Valid, "stale terminal should have been closed locally")
}

// recordingTeardown stands in for the Service's per-type teardown hooks, which
// the reconciler now requires. It records which ids it was handed AND performs
// the real DB close, so a test can assert both the delegation (the reconciler's
// own job: spot staleness and hand off) and the storage effect.
//
// It replaces the old fakeAgentStopper / fakeTerminalStopper pair. Those existed
// to observe the reconciler's narrower fallback tier -- a DB close plus a bare
// process stop -- which has been deleted: stopping the subprocess is now part of
// the one shared teardown an online close runs, so there is no separate stopper
// to fake.
type recordingTeardown struct {
	q     *db.Queries
	files *service.FileTabPathStore

	agents    []string
	terminals []string
	fileTabs  []string
}

// closeTab stands in for (*Service).CloseTabForReconcile, which this harness has
// no Service to call. It records which ids it was handed AND performs the real
// storage effect, so a test can assert both the delegation (the reconciler's own
// job: spot staleness and hand off) and the result.
//
// It is faithful to what CloseTabForReconcile does under dropWorktreeLink for a
// FILE tab -- delete the worker_file_tabs row -- and it drives the real
// FileTabPathStore, so the tests still assert against real storage rather than
// against what a mock was told to return.
//
// One method, not three: it replaces the old per-type fakes, which existed to
// observe the reconciler's deleted fallback tier.
func (r *recordingTeardown) closeTab(tabType leapmuxv1.TabType, userID, tabID string) {
	ctx := context.Background()
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		r.agents = append(r.agents, tabID)
		_, _ = r.q.CloseAgent(ctx, tabID)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		r.terminals = append(r.terminals, tabID)
		_, _ = r.q.CloseTerminal(ctx, tabID)
	case leapmuxv1.TabType_TAB_TYPE_FILE:
		r.fileTabs = append(r.fileTabs, tabID)
		_ = r.files.RevokeRow(ctx, userID, tabID)
	case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
	}
}

// TestOrphanReconciler_Agent_MissingOnHub_StopsInMemory asserts the reconciler
// hands a hub-absent agent to the shared per-type teardown, which is what stops
// the live exec.Cmd. Without that hop the subprocess keeps running until the
// worker process exits -- closed_at alone only prevents a respawn.
func TestOrphanReconciler_Agent_MissingOnHub_StopsInMemory(t *testing.T) {
	q, _, rec, setFake, teardown := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ghost-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	agent, err := q.GetAgentByID(ctx, "ghost-agent")
	require.NoError(t, err)
	assert.True(t, agent.ClosedAt.Valid, "stale agent row must be closed in SQLite")
	assert.Equal(t, []string{"ghost-agent"}, teardown.agents,
		"reconciler must hand the stale agent to the shared teardown -- which stops the exec.Cmd, clears runtime state and runs the registered cleanups -- rather than only closing the row")
}

// TestOrphanReconciler_Terminal_MissingOnHub_StopsInMemory mirrors the agent
// variant for terminal subprocesses (PTY-attached shells). The shared teardown
// uses RemoveTerminal, which also drops the manager's terminals/meta/exitDone
// entries -- the deleted fallback leaked one set per reaped terminal.
func TestOrphanReconciler_Terminal_MissingOnHub_StopsInMemory(t *testing.T) {
	q, _, rec, setFake, teardown := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "ghost-term", Screen: []byte{},
	}))
	setFake("user-1", nil, nil)

	require.NoError(t, runOnce(ctx, rec))

	term, err := q.GetTerminal(ctx, "ghost-term")
	require.NoError(t, err)
	assert.True(t, term.ClosedAt.Valid, "stale terminal row must be closed in SQLite")
	assert.Equal(t, []string{"ghost-term"}, teardown.terminals,
		"reconciler must hand the stale terminal to the shared teardown so the PTY shell is reaped now")
}

// TestOrphanReconciler_Agent_PresentOnHub_DoesNotStop is the don't-overreach
// test: when the hub still references the agent, the reconciler must not hand it
// to the teardown at all (otherwise every live agent would be killed hourly).
func TestOrphanReconciler_Agent_PresentOnHub_DoesNotStop(t *testing.T) {
	q, _, rec, setFake, teardown := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "live-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "live-agent"},
	}, nil)

	require.NoError(t, runOnce(ctx, rec))

	agent, err := q.GetAgentByID(ctx, "live-agent")
	require.NoError(t, err)
	assert.False(t, agent.ClosedAt.Valid, "agent still referenced by hub must stay open")
	assert.Empty(t, teardown.agents, "live agent must NOT receive a stop signal")
}

func TestOrphanReconciler_ListError_DoesNotPanic_DoesNotCloseRows(t *testing.T) {
	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "live", FilePath: "/r/a.go",
	}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "live-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
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
	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		// A long interval keeps the tick from racing with Trigger.
		Interval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost", FilePath: "/r/a.go",
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
		UserID: "user-1", TabID: "ghost2", FilePath: "/r/b.go",
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
// row is then reconciled against a stranger's hub row, so user-a's live tab is
// judged by whether USER-B still owns that id -- and reaped when they do not.
func TestOrphanReconciler_FileTab_SharedTabIDStaysWithItsOwner(t *testing.T) {
	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	const sharedTabID = "file-1700000000000-1"
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: sharedTabID, FilePath: "/r/a.go",
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: sharedTabID, FilePath: "/r/b.go",
	}))

	// The hub knows both rows, each naming its own owner. Neither is absent,
	// so neither is reaped -- and the owner-keyed comparison is what makes that
	// true for BOTH: an owner-blind key would collapse them into one entry.
	setFake("user-a", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, UserId: "user-a"},
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, UserId: "user-b"},
	}, nil)

	require.NoError(t, runOnce(ctx, rec))

	byUser := fileTabsByUser(t, q, ctx)
	require.Contains(t, byUser, "user-a")
	require.Contains(t, byUser, "user-b")
	assert.Equal(t, "/r/a.go", byUser["user-a"].FilePath,
		"user-a must be reconciled against user-a's hub row, not user-b's")
	assert.Equal(t, "/r/b.go", byUser["user-b"].FilePath,
		"user-b's row must survive on its own hub row")
}

// TestOrphanReconciler_FileTab_SharedTabIDReapsOnlyTheStaleOwner is the
// destructive half of the same aliasing bug: when only ONE of two owners
// sharing a tab id is still known to the hub, the owner-blind key lets the
// stale row match the live owner's entry -- so it is never reaped, and the
// dropped owner's row survives indefinitely under the live owner's cover.
//
// The response is scoped to user-b, the owner being reaped: that is what makes
// user-b's absence mean "dropped" rather than "not asked about". It also
// carries user-a's row, which the scope does not cover -- deliberately
// adversarial, so the reap decision cannot lean on the hub having sent only
// in-scope rows.
func TestOrphanReconciler_FileTab_SharedTabIDReapsOnlyTheStaleOwner(t *testing.T) {
	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	const sharedTabID = "file-1700000000000-1"
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: sharedTabID, FilePath: "/r/a.go",
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: sharedTabID, FilePath: "/r/b.go",
	}))

	// Only user-a's tab survives at the hub.
	setFake("user-b", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, UserId: "user-a"},
	}, nil)

	require.NoError(t, runOnce(ctx, rec))

	byUser := fileTabsByUser(t, q, ctx)
	require.Contains(t, byUser, "user-a", "the hub-known owner's row must survive")
	assert.Equal(t, "/r/a.go", byUser["user-a"].FilePath,
		"and must not be confused with a stranger's row")
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
	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: "file-a", FilePath: "/r/a.go",
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: "file-b", FilePath: "/r/b.go",
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
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file-a", UserId: "user-a"},
	}, nil)

	require.NoError(t, runOnce(ctx, rec))

	byUser := fileTabsByUser(t, q, ctx)
	require.Contains(t, byUser, "user-a", "the in-scope owner's listed tab must survive")
	require.Contains(t, byUser, "user-b",
		"a row owned by a user the response does not cover must be left alone, not reaped")

	links, err := q.CountWorktreeTabs(ctx, "wt-b")
	require.NoError(t, err)
	assert.Equal(t, int64(1), links, "the out-of-scope row's worktree link must survive too")
}

// TestOrphanReconciler_UndeclaredScopeReapsNothing covers the degenerate case:
// a response that names no owner is authoritative for nobody, so an empty tab
// list must not be read as "every local tab is an orphan". Fail closed -- a
// missed reap is a leak, an unfounded reap is data loss.
func TestOrphanReconciler_UndeclaredScopeReapsNothing(t *testing.T) {
	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		// Warn-level: the skipped pass logs at Warn and the harness default
		// only prints Errors, which would hide it.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: "file-a", FilePath: "/r/a.go",
	}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-a", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
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
