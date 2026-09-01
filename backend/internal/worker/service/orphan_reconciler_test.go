package service_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"sync"
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
	require.NoError(t, workerdb.Migrate(context.Background(), sqlDB))
	q := db.New(sqlDB)
	bus := service.NewPrivateEventsBus()
	t.Cleanup(bus.Stop)
	files := service.NewFileTabPathStore(q, bus)

	// Guarded: Run drives listFn on its own goroutine, so a test that changes
	// the hub's answer while the reconciler is running (to model a channel
	// that settles after a failed pass) would otherwise race the read.
	var (
		fakeMu   sync.Mutex
		fakeResp *leapmuxv1.ListOwnedTabsForWorkerResponse
		fakeErr  error
	)
	listFn := func(_ context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
		fakeMu.Lock()
		defer fakeMu.Unlock()
		return fakeResp, fakeErr
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(testWriter{t: t}, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	teardown := &recordingTeardown{q: q, files: files}
	if opts.CloseTab == nil {
		opts.CloseTab = teardown.closeTab
	}
	// These tests assert WHAT a reap does, not WHEN it is due, and they drive
	// single passes on a real clock. Disable the grace so each one still reaps
	// in one pass. The grace itself has its own tests, which drive a fake clock
	// across the window: see TestReconcileTabs_* in orphan_reconciler_gc_test.go.
	if opts.TabGrace == 0 {
		opts.TabGrace = -1
	}
	rec := service.NewOrphanReconciler(q, files, listFn, opts)
	setFake := func(ownerUserID string, tabs []*leapmuxv1.OwnedTab, err error) {
		fakeMu.Lock()
		defer fakeMu.Unlock()
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
	t.Parallel()

	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	// Local row that the hub no longer knows about.
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost", FilePath: absTestPath("/r/a.go"),
	}))
	setFake("user-1", nil, nil)

	// Manually drive a single pass — Run loop semantics are exercised by
	// the Trigger test below.
	require.True(t, runOnce(ctx, rec), "the pass must converge")

	rows, err := q.ListAllWorkerFileTabs(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "stale local file tab should have been revoked")
}
func TestOrphanReconciler_Agent_MissingOnHub_Closed(t *testing.T) {
	t.Parallel()

	q, _, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ghost-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

	agent, err := q.GetAgentByID(ctx, "ghost-agent")
	require.NoError(t, err)
	assert.True(t, agent.ClosedAt.Valid, "stale agent should have been closed locally")
}

func TestOrphanReconciler_Terminal_MissingOnHub_Closed(t *testing.T) {
	t.Parallel()

	q, _, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:     "ghost-term", // screen is NOT NULL; an empty byte slice satisfies the constraint.
		Screen: []byte{},
	}))
	setFake("user-1", nil, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

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
	t.Parallel()

	q, _, rec, setFake, teardown := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ghost-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

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
	t.Parallel()

	q, _, rec, setFake, teardown := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "ghost-term", Screen: []byte{},
	}))
	setFake("user-1", nil, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

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
	t.Parallel()

	q, _, rec, setFake, teardown := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "live-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "live-agent"},
	}, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

	agent, err := q.GetAgentByID(ctx, "live-agent")
	require.NoError(t, err)
	assert.False(t, agent.ClosedAt.Valid, "agent still referenced by hub must stay open")
	assert.Empty(t, teardown.agents, "live agent must NOT receive a stop signal")
}

func TestOrphanReconciler_ListError_DoesNotPanic_DoesNotCloseRows(t *testing.T) {
	t.Parallel()

	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "live", FilePath: absTestPath("/r/a.go"),
	}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "live-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, errors.New("hub unavailable"))

	require.False(t, runOnce(ctx, rec),
		"a failed hub leg must report NON-convergence -- that bool is what the retry backoff keys off")

	rows, err := q.ListAllWorkerFileTabs(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "list error must not revoke live rows")
	agent, err := q.GetAgentByID(ctx, "live-agent")
	require.NoError(t, err)
	assert.False(t, agent.ClosedAt.Valid, "list error must not close live agents")
}

func TestOrphanReconciler_TriggerRunsPassImmediately(t *testing.T) {
	t.Parallel()

	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		// A long interval keeps the tick from racing with Trigger.
		Interval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost", FilePath: absTestPath("/r/a.go"),
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
		UserID: "user-1", TabID: "ghost2", FilePath: absTestPath("/r/b.go"),
	}))
	rec.Trigger()
	require.Eventually(t, func() bool {
		rows, err := q.ListAllWorkerFileTabs(ctx)
		return err == nil && len(rows) == 0
	}, 2*time.Second, 10*time.Millisecond, "Trigger should run another pass")

	cancel()
	rec.Stop()
}

// TestOrphanReconciler_RetriesAfterHubListFailure pins the convergence
// guarantee the reconnect Trigger is supposed to provide.
//
// Trigger() fires when the worker reconnects, which is precisely when the hub
// RPC is most likely to be answered by a channel that has not settled yet. That
// one shot used to be dropped on the floor -- a failed listFn logged a warning
// and returned, and nothing ran again until the next interval tick, an hour
// away by default. So a tab the user closed while the machine was asleep kept
// its process alive on the worker long after it woke up.
//
// The interval here is an hour, so anything that converges within the Eventually
// budget can only have come from the failure retry.
func TestOrphanReconciler_RetriesAfterHubListFailure(t *testing.T) {
	t.Parallel()

	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		Interval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost", FilePath: absTestPath("/r/a.go"),
	}))
	// The startup pass and any retry before the switch below both fail.
	setFake("user-1", nil, errors.New("channel not ready"))

	go rec.Run(ctx)
	// The failing passes must not reap anything -- a hub answer we never got
	// says nothing about which tabs still exist.
	require.Never(t, func() bool {
		rows, err := q.ListAllWorkerFileTabs(ctx)
		return err == nil && len(rows) == 0
	}, 500*time.Millisecond, 50*time.Millisecond, "a failed hub list must not reap")

	// The channel settles. No Trigger and no tick: only the backoff retry can
	// pick this up.
	setFake("user-1", nil, nil)
	require.Eventually(t, func() bool {
		rows, err := q.ListAllWorkerFileTabs(ctx)
		return err == nil && len(rows) == 0
	}, 10*time.Second, 20*time.Millisecond,
		"a pass whose hub leg failed must be retried without waiting for the interval")

	cancel()
	rec.Stop()
}

// runOnce drives exactly one reconciliation pass and returns whether it
// CONVERGED -- the bool the seam exists to expose.
//
// It used to swallow that and return a literal nil, which made every
// `require.NoError(t, runOnce(...))` below unfailable: a regression that made
// reconcileOnce report false on every pass (a hub list error, a failed local
// probe, a blank owner scope) left all of them green as long as the specific
// rows each test inspected happened to line up.
//
// It calls the pass directly (through export_test.go) rather than starting Run
// and cancelling it after a sleep. The sleep was a window sized by the machine
// instead of by the state it waited for: the pass's DB writes take the same
// context Run does, so a cancel that landed mid-pass abandoned them, and the
// test then failed reporting that the reconciler had not closed a stale tab.
// Under -race with the whole package in parallel, 50 ms stopped being enough.
//
// Run's own loop semantics -- the startup pass, ticks, Trigger coalescing and
// the retry backoff -- are covered by the Trigger and retry tests below, which
// genuinely are about the loop.
func runOnce(ctx context.Context, rec *service.OrphanReconciler) bool {
	return rec.ReconcileOnceForTest(ctx)
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
	t.Parallel()

	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	const sharedTabID = "file-1700000000000-1"
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: sharedTabID, FilePath: absTestPath("/r/a.go"),
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: sharedTabID, FilePath: absTestPath("/r/b.go"),
	}))

	// The hub knows both rows, each naming its own owner. Neither is absent,
	// so neither is reaped -- and the owner-keyed comparison is what makes that
	// true for BOTH: an owner-blind key would collapse them into one entry.
	setFake("user-a", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, UserId: "user-a"},
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, UserId: "user-b"},
	}, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

	byUser := fileTabsByUser(t, q, ctx)
	require.Contains(t, byUser, "user-a")
	require.Contains(t, byUser, "user-b")
	assert.Equal(t, absTestPath("/r/a.go"), byUser["user-a"].FilePath,
		"user-a must be reconciled against user-a's hub row, not user-b's")
	assert.Equal(t, absTestPath("/r/b.go"), byUser["user-b"].FilePath,
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
	t.Parallel()

	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	const sharedTabID = "file-1700000000000-1"
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: sharedTabID, FilePath: absTestPath("/r/a.go"),
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: sharedTabID, FilePath: absTestPath("/r/b.go"),
	}))

	// Only user-a's tab survives at the hub.
	setFake("user-b", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: sharedTabID, UserId: "user-a"},
	}, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

	byUser := fileTabsByUser(t, q, ctx)
	require.Contains(t, byUser, "user-a", "the hub-known owner's row must survive")
	assert.Equal(t, absTestPath("/r/a.go"), byUser["user-a"].FilePath,
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
	t.Parallel()

	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: "file-a", FilePath: absTestPath("/r/a.go"),
	}))
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-b", TabID: "file-b", FilePath: absTestPath("/r/b.go"),
	}))
	// Link user-b's tab to a worktree: the reap drops that link FIRST, so an
	// unscoped reap is observable here even before the file-tab row goes.
	_, cwErr := q.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID: "wt-b", WorktreePath: absTestPath("/r/b"), RepoRoot: absTestPath("/r"), BranchName: "b",
	})
	require.NoError(t, cwErr)
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
		WorktreeID: "wt-b", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-b", UserID: "user-b",
	}))

	// A response scoped to user-a, listing user-a's live tab. It says nothing
	// about user-b -- who is not "absent", merely not asked about.
	setFake("user-a", []*leapmuxv1.OwnedTab{
		{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file-a", UserId: "user-a"},
	}, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

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
	t.Parallel()

	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		// Warn-level: the skipped pass logs at Warn and the harness default
		// only prints Errors, which would hide it.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx := context.Background()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-a", TabID: "file-a", FilePath: absTestPath("/r/a.go"),
	}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-a", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("", nil, nil)

	require.False(t, runOnce(ctx, rec),
		"an undeclared owner scope reaps nothing and must not claim to have converged")

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

// TestReconcileAgentsSkipsChildAgents pins that the orphan reconciler never
// reaps a child agent. reconcileAgents iterates ListAllOpenRootAgentIDs (roots
// only; parent_agent_id IS NULL), so a tabless child -- which is its DEFAULT
// state and disappears when its root closes -- is never handed to the teardown.
//
// Both agents here are open and absent from the hub (the orphan condition). The
// root MAY be reaped per the existing logic (and is), but the child must stay
// open: closed_at stays NULL and the shared teardown is never called for it.
func TestReconcileAgentsSkipsChildAgents(t *testing.T) {
	t.Parallel()

	q, _, rec, setFake, teardown := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{})
	ctx := context.Background()

	// Root + child, both open, both absent from the hub. The child is linked to
	// the root via parent_agent_id, so ListAllOpenRootAgentIDs returns only the
	// root.
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root-orphan", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, q.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "child-orphan",
		ParentAgentID: sql.NullString{String: "root-orphan", Valid: true},
		SpawnSpanID:   "span-1",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake("user-1", nil, nil)

	require.True(t, runOnce(ctx, rec), "the pass must converge")

	// The root is hub-absent, so it is reaped per the existing logic: closed_at
	// is stamped and the shared teardown ran for it.
	root, err := q.GetAgentByID(ctx, "root-orphan")
	require.NoError(t, err)
	assert.True(t, root.ClosedAt.Valid, "the hub-absent root must be reaped")
	assert.Contains(t, teardown.agents, "root-orphan",
		"the root must be handed to the shared teardown")

	// The child must NEVER be reaped: it is tabless by design and disappears
	// only when its root closes. closed_at stays NULL and the teardown is never
	// invoked for it.
	child, err := q.GetAgentByID(ctx, "child-orphan")
	require.NoError(t, err)
	assert.False(t, child.ClosedAt.Valid,
		"a child agent must never be reaped by the orphan reconciler -- it is tabless by design and disappears with its root")
	assert.NotContains(t, teardown.agents, "child-orphan",
		"the child must never be handed to the shared teardown")
}

// TestOrphanReconciler_OnPassReportsEveryPass pins the hook the boot-time agent
// resume starts on.
//
// Two properties matter and both are easy to lose. The report must carry the
// pass's REAL verdict -- a hook that always said true would start the resume
// sweep against a hub the worker never reached, which is the state in which a
// resumed agent cannot be given a control socket at all. And the report must
// also fire on the RETRY arm: the startup pass of a worker whose channel has
// not settled always fails, so if only the first pass reported, the eventual
// convergence would never reach the hook and the agents would stay cold for the
// life of the process.
func TestOrphanReconciler_OnPassReportsEveryPass(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var verdicts []bool
	q, files, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		Interval: time.Hour,
		OnPass: func(converged bool) {
			mu.Lock()
			verdicts = append(verdicts, converged)
			mu.Unlock()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "ghost", FilePath: absTestPath("/r/a.go"),
	}))
	setFake("user-1", nil, errors.New("channel not ready"))

	go rec.Run(ctx)

	// While the hub leg fails, every report must say "not converged".
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(verdicts) > 0
	}, 2*time.Second, 10*time.Millisecond, "the startup pass must report")
	mu.Lock()
	for i, v := range verdicts {
		assert.False(t, v, "pass %d reported convergence while the hub list was failing", i)
	}
	mu.Unlock()

	// The channel settles. Only the backoff retry can pick this up, so a true
	// report here proves the retry arm reports as well as the first pass.
	setFake("user-1", nil, nil)
	require.Eventually(t, func() bool {
		rows, err := q.ListAllWorkerFileTabs(ctx)
		if err != nil || len(rows) != 0 {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		return len(verdicts) > 0 && verdicts[len(verdicts)-1]
	}, 10*time.Second, 20*time.Millisecond,
		"a converged pass reached through the retry backoff must still report")

	cancel()
	rec.Stop()
}

// TestOrphanReconciler_NoOnPassHookIsFine pins that the hook stays optional --
// every existing caller and test constructs the reconciler without one.
func TestOrphanReconciler_NoOnPassHookIsFine(t *testing.T) {
	t.Parallel()

	_, _, rec, setFake, _ := newOrphanReconcilerHarness(t, service.OrphanReconcilerOptions{
		Interval: time.Hour,
	})
	setFake("user-1", nil, nil)
	assert.True(t, runOnce(context.Background(), rec))
}
