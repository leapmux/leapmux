package service

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startLiveBusyTestAgent(t *testing.T, svc *Service, agentID, title string) agent.ProviderServices {
	t.Helper()
	ctx := context.Background()
	if svc.Queries != nil {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID:            agentID,
			WorkingDir:    t.TempDir(),
			Title:         title,
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}))
	}
	sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID:    agentID,
		WorkingDir: t.TempDir(),
	}, sink, func(_ context.Context, opts agent.Options, _ agent.ProviderServices) (agent.Agent, error) {
		return agenttest.NewRunningAgent(opts.AgentID), nil
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		unlock := svc.Agents.LockAgent(agentID)
		svc.Agents.StopAndWaitAgent(agentID)
		unlock()
	})
	return sink
}

func startLiveTestTerminal(t *testing.T, svc *Service, id, metaTitle string) {
	t.Helper()
	workingDir := t.TempDir()
	require.NoError(t, svc.Terminals.StartTerminal(context.Background(), terminal.Options{
		ID:         id,
		Shell:      testutil.TestShell(),
		WorkingDir: workingDir,
		Cols:       200,
		Rows:       24,
	}, func([]byte, int64, []terminal.Signal) {}, nil))
	if metaTitle != "" {
		svc.Terminals.UpdateTitle(id, metaTitle)
	}
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

	const marker = "busy_work_shell_ready"
	command, err := testutil.TestShellOutputCommand(marker)
	require.NoError(t, err)
	require.NoError(t, svc.Terminals.SendInput(id, []byte(command+testutil.TestShellEnter())))
	testutil.RequireEventually(t, func() bool {
		screen, _, _ := svc.Terminals.ScreenSnapshotSince(id, 0)
		return bytes.Contains(screen, []byte(marker))
	}, "expected the shell to execute the readiness marker")
}

func startSleepDescendant(t *testing.T, svc *Service, terminalID string) string {
	t.Helper()
	line, want := testutil.TestSleepCommand()
	require.True(t, testutil.SendShellJobUntil(t, svc.Terminals, terminalID, line, func() bool {
		got, err := svc.Terminals.DescendantProcesses(t.Context(), []string{terminalID})
		if err != nil || len(got) != 1 {
			return false
		}
		for _, proc := range got[0].Processes {
			if testutil.NormalizeProcessName(proc.Name) == want {
				return true
			}
		}
		return false
	}, "expected backgrounded sleep descendant in terminal %s", terminalID))
	return want
}

func TestInspectBusyWork_IdleWorker(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	startLiveBusyTestAgent(t, svc, "agent-idle", "Idle agent")
	startLiveTestTerminal(t, svc, "term-idle", "Idle shell")

	got := svc.InspectBusyWork(context.Background())
	assert.Empty(t, got.Agents)
	assert.Empty(t, got.Terminals)
}

func TestInspectBusyWork_ActiveTurnBackgroundTasksChildTaskAndChildAgents(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	sink := startLiveBusyTestAgent(t, svc, "agent-1", "Refactor auth")
	sink.SetTurnState(agent.TurnState{Active: true}, 1)

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role:   bgtask.RoleTaskOutcome,
		RowKey: "task-running",
		Kind:   bgtask.KindShell,
		Title:  "Run unit tests",
		Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role:   bgtask.RoleTaskOutcome,
		RowKey: "task-pending",
		Kind:   bgtask.KindWorkflow,
		Title:  "Build bundle",
		Status: bgtask.StatusPending,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role:   bgtask.RoleTaskOutcome,
		RowKey: "task-done",
		Kind:   bgtask.KindShell,
		Title:  "Lint finished",
		Status: bgtask.StatusSucceeded,
	}))
	_, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		ProviderChildKey: "child-task-key",
		Title:            "Check security",
	})
	require.NoError(t, err)
	_, err = sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildActivity,
		ProviderChildKey: "child-activity-key",
		Title:            "Codebase reviewer",
	})
	require.NoError(t, err)

	got := svc.InspectBusyWork(context.Background())
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "agent-1", got.Agents[0].TabID)
	assert.Equal(t, "Refactor auth", got.Agents[0].Title)
	assert.ElementsMatch(t, []string{"Run unit tests", "Build bundle", "Check security"}, got.Agents[0].ActiveBackgroundTasks)
	assert.Equal(t, []string{"Codebase reviewer"}, got.Agents[0].ActiveChildAgents)
	assert.Empty(t, got.Terminals)
}

func TestInspectBusyWork_WaitingForUserReportedAsBusy(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	sink := startLiveBusyTestAgent(t, svc, "agent-wait", "Waiting agent")
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	svc.Output.noteControlRequestAdded("agent-wait", "agent-wait", "req-1", "claim-1")

	activity := svc.Output.AgentActivitySnapshot("agent-wait", "agent-wait")
	require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, activity.State)
	require.False(t, activity.Working())
	require.True(t, activity.InterruptsWork())

	got := svc.InspectBusyWork(context.Background())
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "agent-wait", got.Agents[0].TabID)
	assert.Equal(t, "Waiting agent", got.Agents[0].Title)
}

func TestInspectBusyWork_TitleFallbacksCleaningAndSortOrder(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	ctx := context.Background()

	// Agents: one blank DB title ("Agent" fallback), two same-title agents to test TabID tiebreaker,
	// plus background task label fallbacks.
	sinkBlank := startLiveBusyTestAgent(t, svc, "agent-b", "   \x00\t  ")
	sinkBlank.SetTurnState(agent.TurnState{Active: true}, 1)
	require.NoError(t, sinkBlank.UpsertBackgroundTask(bgtask.Upsert{
		Role:        bgtask.RoleTaskOutcome,
		RowKey:      "key-from-desc",
		Kind:        bgtask.KindShell,
		Title:       "",
		Description: "  run\n\tunit\x07tests  ",
		Status:      bgtask.StatusRunning,
	}))
	require.NoError(t, sinkBlank.UpsertBackgroundTask(bgtask.Upsert{
		Role:        bgtask.RoleTaskOutcome,
		RowKey:      "fallback-row-key",
		Kind:        bgtask.KindShell,
		Title:       "",
		Description: "  \x07  ",
		Status:      bgtask.StatusRunning,
	}))
	require.NoError(t, sinkBlank.UpsertBackgroundTask(bgtask.Upsert{
		Role:        bgtask.RoleTaskOutcome,
		RowKey:      "\x07\x08",
		Kind:        bgtask.KindShell,
		Title:       "",
		Description: "",
		Status:      bgtask.StatusRunning,
	}))

	sinkTie := startLiveBusyTestAgent(t, svc, "agent-a", "Agent")
	sinkTie.SetTurnState(agent.TurnState{Active: true}, 1)

	sinkZ := startLiveBusyTestAgent(t, svc, "agent-z", "zeta\x07\tplan")
	sinkZ.SetTurnState(agent.TurnState{Active: true}, 1)

	// Verify control-char cleaning directly on raw item labels and process names.
	assert.Equal(t, "run build", cleanBusyTaskLabel(bgtask.Item{Description: "run\x00\x07\nbuild"}))
	assert.Equal(t, "Untitled", cleanBusyTaskLabel(bgtask.Item{RowKey: "\x00\x07"}))
	assert.Equal(t, "node server", cleanBusyProcessName("  node\n\t\x07\u202eserver  "))
	assert.Empty(t, cleanBusyProcessName("  \t\n\x00\u202e  "))

	// Terminals: GetMeta title, DB title fallback, Quake Terminal fallback, plain Terminal fallback.
	startLiveTestTerminal(t, svc, "term-meta", "  Alpha\x07Meta  ")
	startSleepDescendant(t, svc, "term-meta")

	startLiveTestTerminal(t, svc, "term-db", "")
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "term-db", WorkingDir: t.TempDir(), Title: "  Beta\tDB  ",
		Cols: 80, Rows: 24, Screen: []byte{},
	}))
	startSleepDescendant(t, svc, "term-db")

	startLiveTestTerminal(t, svc, "term-quake", "")
	_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
		ID: "term-quake", WorkingDir: t.TempDir(), Title: "   ",
		Cols: 80, Rows: 24,
	})
	require.True(t, created)
	startSleepDescendant(t, svc, "term-quake")

	startLiveTestTerminal(t, svc, "term-plain", "")
	startSleepDescendant(t, svc, "term-plain")

	got := svc.InspectBusyWork(ctx)
	require.Len(t, got.Agents, 3)
	assert.Equal(t, []string{"agent-a", "agent-b", "agent-z"}, []string{
		got.Agents[0].TabID, got.Agents[1].TabID, got.Agents[2].TabID,
	})
	assert.Equal(t, []string{"Agent", "Agent", "zeta plan"}, []string{
		got.Agents[0].Title, got.Agents[1].Title, got.Agents[2].Title,
	})
	assert.ElementsMatch(t, []string{"run unittests", "fallback-row-key", "Untitled"}, got.Agents[1].ActiveBackgroundTasks)

	require.Len(t, got.Terminals, 4)
	assert.Equal(t, []string{"term-meta", "term-db", "term-quake", "term-plain"}, []string{
		got.Terminals[0].TabID, got.Terminals[1].TabID, got.Terminals[2].TabID, got.Terminals[3].TabID,
	})
	assert.Equal(t, []string{"AlphaMeta", "Beta DB", "Quake Terminal", "Terminal"}, []string{
		got.Terminals[0].Title, got.Terminals[1].Title, got.Terminals[2].Title, got.Terminals[3].Title,
	})
}

func TestInspectBusyWork_LiveTerminalWithDescendantProcess(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	startLiveTestTerminal(t, svc, "term-idle", "Idle terminal")
	startLiveTestTerminal(t, svc, "term-busy", "Dev server")
	wantProc := startSleepDescendant(t, svc, "term-busy")

	got := svc.InspectBusyWork(context.Background())
	assert.Empty(t, got.Agents)
	require.Len(t, got.Terminals, 1)
	assert.Equal(t, "term-busy", got.Terminals[0].TabID)
	assert.Equal(t, "Dev server", got.Terminals[0].Title)
	assert.GreaterOrEqual(t, int(got.Terminals[0].TotalCount), len(got.Terminals[0].Processes))
	require.NotEmpty(t, got.Terminals[0].Processes)

	names := make([]string, 0, len(got.Terminals[0].Processes))
	for _, proc := range got.Terminals[0].Processes {
		assert.Positive(t, proc.PID)
		names = append(names, testutil.NormalizeProcessName(proc.Name))
	}
	assert.True(t, slices.Contains(names, wantProc), "expected %q in %v", wantProc, names)
}

func TestInspectBusyWork_NilSafetyNilQueriesAndCancelledContext(t *testing.T) {
	t.Parallel()

	// Nil receiver and zero Service.
	assert.Empty(t, (*Service)(nil).InspectBusyWork(context.Background()))
	assert.Empty(t, (&Service{}).InspectBusyWork(context.Background()))

	// Live busy agent and live busy terminal with svc.Queries == nil.
	svcNoQueries, _, _ := setupTestService(t)
	sink := startLiveBusyTestAgent(t, svcNoQueries, "agent-no-db", "Ignored DB title")
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	startLiveTestTerminal(t, svcNoQueries, "term-no-db", "")
	startSleepDescendant(t, svcNoQueries, "term-no-db")
	svcNoQueries.Queries = nil

	noQueriesGot := svcNoQueries.InspectBusyWork(context.Background())
	require.Len(t, noQueriesGot.Agents, 1)
	assert.Equal(t, "Agent", noQueriesGot.Agents[0].Title)
	require.Len(t, noQueriesGot.Terminals, 1)
	assert.Equal(t, "Terminal", noQueriesGot.Terminals[0].Title)

	// Already-cancelled context with live busy agent and live busy terminal.
	svcCancelled, _, _ := setupTestService(t)
	cancelledSink := startLiveBusyTestAgent(t, svcCancelled, "agent-cancelled", "DB title skipped")
	cancelledSink.SetTurnState(agent.TurnState{Active: true}, 1)
	startLiveTestTerminal(t, svcCancelled, "term-cancelled", "Live terminal")
	startSleepDescendant(t, svcCancelled, "term-cancelled")

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	cancelledGot := svcCancelled.InspectBusyWork(cancelledCtx)
	require.Len(t, cancelledGot.Agents, 1)
	assert.Equal(t, "Agent", cancelledGot.Agents[0].Title)
	assert.Empty(t, cancelledGot.Terminals, "process scan fails open when context is cancelled")
}

func TestInspectBusyWork_CancelledContextSkipsColdBackgroundTaskDBLoad(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	startLiveBusyTestAgent(t, svc, "agent-cold-bg", "Cold cache agent")

	// Insert an active background task directly into SQLite without seeding
	// the in-memory bgTaskCache, then cancel the inspection context.
	now := sqltime.NewSQLiteTime(time.Now().UTC())
	writeBackgroundTaskFixture(t, svc.Queries, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "agent-cold-bg",
		RowKey:       "cold-task-1",
		Seq:          1,
		Role:         leapmuxv1.BackgroundTaskRole(bgtask.RoleTaskOutcome),
		Kind:         leapmuxv1.BackgroundTaskKind(bgtask.KindShell),
		Title:        "Long running job",
		Status:       leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		CreatedAt:    now,
		UpdatedAt:    now,
	})

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	got := svc.InspectBusyWork(cancelledCtx)
	assert.Empty(t, got.Agents, "cancelled context must not seed cold background-task rows from SQLite")

	// With a live context, the same cold row loads and reports the agent busy.
	liveGot := svc.InspectBusyWork(context.Background())
	require.Len(t, liveGot.Agents, 1)
	assert.Equal(t, []string{"Long running job"}, liveGot.Agents[0].ActiveBackgroundTasks)
}

func TestInspectBusyWork_PreservesAllActiveTasksPastDisplayCap(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	sink := startLiveBusyTestAgent(t, svc, "agent-many-active", "Busy swarm")

	const totalActive = bgtask.MaxTasks + 3
	wantTitles := make([]string, 0, totalActive)
	for i := range totalActive {
		title := fmt.Sprintf("active-job-%02d", i)
		wantTitles = append(wantTitles, title)
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("job-%02d", i),
			Role:   bgtask.RoleTaskOutcome,
			Kind:   bgtask.KindShell,
			Title:  title,
			Status: bgtask.StatusRunning,
		}))
	}

	warm := svc.InspectBusyWork(context.Background())
	require.Len(t, warm.Agents, 1)
	assert.ElementsMatch(t, wantTitles, warm.Agents[0].ActiveBackgroundTasks)

	// Drop the warm cache and verify cold seeding also preserves every active row.
	svc.Output.bgtasks.Delete("agent-many-active")
	cold := svc.InspectBusyWork(context.Background())
	require.Len(t, cold.Agents, 1)
	assert.ElementsMatch(t, wantTitles, cold.Agents[0].ActiveBackgroundTasks)
}
