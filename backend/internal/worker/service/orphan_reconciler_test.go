package service_test

import (
	"context"
	"errors"
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
// mutable slice the test owns.
func newOrphanReconcilerHarness(t *testing.T, opts service.OrphanReconcilerOptions) (
	*db.Queries,
	*service.FileTabPathStore,
	*service.OrphanReconciler,
	func([]*leapmuxv1.OwnedTab, error),
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
		fakeTabs []*leapmuxv1.OwnedTab
		fakeErr  error
	)
	listFn := func(_ context.Context) ([]*leapmuxv1.OwnedTab, error) {
		return fakeTabs, fakeErr
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(testWriter{t: t}, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	rec := service.NewOrphanReconciler(q, files, listFn, opts)
	setFake := func(tabs []*leapmuxv1.OwnedTab, err error) {
		fakeTabs = tabs
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
		OrgID: "org", TabID: "ghost", WorkspaceID: "w1", FilePath: "/r/a.go",
	}))
	setFake(nil, nil)

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
		OrgID: "org", TabID: "f1", WorkspaceID: "w1", FilePath: "/r/a.go",
	}))
	// Hub says this tab is now in w2 (CRDT moved it after a client crash).
	setFake([]*leapmuxv1.OwnedTab{{
		TabType:     leapmuxv1.TabType_TAB_TYPE_FILE,
		TabId:       "f1",
		WorkspaceId: "w2",
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
	setFake(nil, nil)

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
	setFake(nil, nil)

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
	setFake(nil, nil)

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
	setFake(nil, nil)

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
	setFake([]*leapmuxv1.OwnedTab{
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
		OrgID: "org", TabID: "live", WorkspaceID: "w1", FilePath: "/r/a.go",
	}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "live-agent", WorkspaceID: "w1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	setFake(nil, errors.New("hub unavailable"))

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
		OrgID: "org", TabID: "ghost", WorkspaceID: "w1", FilePath: "/r/a.go",
	}))
	setFake(nil, nil)

	go rec.Run(ctx)
	// Run kicks off one pass at start; wait for it to settle.
	require.Eventually(t, func() bool {
		rows, err := q.ListAllWorkerFileTabs(ctx)
		return err == nil && len(rows) == 0
	}, 2*time.Second, 10*time.Millisecond, "startup pass should revoke the orphan")

	// Add another orphan and confirm Trigger fires a fresh pass.
	require.NoError(t, files.Register(ctx, service.RegisterFileTabPathParams{
		OrgID: "org", TabID: "ghost2", WorkspaceID: "w1", FilePath: "/r/b.go",
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
