package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// ---------------------------------------------------------------------------
// deriveAgentStatus / deriveTerminalStatus unit tests
// ---------------------------------------------------------------------------

// TestDeriveAgentStatus_AllBranches exercises each branch of the priority
// order: runtime Manager → startup registry → persisted DB column → INACTIVE.
func TestDeriveAgentStatus_AllBranches(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)

	dbAgent := db.Agent{ID: "agent-1"}

	// 1. Runtime ACTIVE: isRunning=true wins over everything.
	agentHandle := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, agentHandle)
	dbAgent.StartupError = "stale error"
	status, errStr, _ := deriveAgentStatus(&dbAgent, liveness(svc, "agent-1", true))
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, status)
	assert.Empty(t, errStr)

	// 2. Registry STARTING: isRunning=false + registry populated.
	dbAgent.StartupError = ""
	status, errStr, _ = deriveAgentStatus(&dbAgent, liveness(svc, "agent-1", false))
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, status)
	assert.Empty(t, errStr)

	// 3. Registry STARTUP_FAILED: fail() wins over DB column.
	dbAgent.StartupError = "stale db error"
	svc.AgentStartup.fail(agentHandle, "registry error")
	status, errStr, _ = deriveAgentStatus(&dbAgent, liveness(svc, "agent-1", false))
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "registry error", errStr)

	// 4. Clear the registry; DB column now drives STARTUP_FAILED.
	svc.AgentStartup.cancelAndClear("agent-1", keepWorktreeOnClose)
	status, errStr, _ = deriveAgentStatus(&dbAgent, liveness(svc, "agent-1", false))
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "stale db error", errStr)

	// 5. No registry entry, no DB column, not running → INACTIVE.
	dbAgent.StartupError = ""
	status, errStr, _ = deriveAgentStatus(&dbAgent, liveness(svc, "agent-1", false))
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, status)
	assert.Empty(t, errStr)
}

// TestDeriveTerminalStatus_AllBranches is the terminal analogue: registry >
// DB > READY, where READY becomes EXITED once the manager holds no live shell.
func TestDeriveTerminalStatus_AllBranches(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)

	term := db.Terminal{ID: "term-1"}

	// 1. Registry STARTING.
	terminalHandle := svc.TerminalStartup.begin("term-1", func() {})
	require.NotNil(t, terminalHandle)
	status, errStr, _ := svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, status)
	assert.Empty(t, errStr)

	// 2. Registry STARTUP_FAILED wins over DB column.
	term.StartupError = "stale db error"
	svc.TerminalStartup.fail(terminalHandle, "registry error")
	status, errStr, _ = svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "registry error", errStr)

	// 3. Registry cleared → DB column drives STARTUP_FAILED.
	svc.TerminalStartup.cancelAndClear("term-1", keepWorktreeOnClose)
	status, errStr, _ = svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "stale db error", errStr)

	// 4. No registry, no DB column, no live shell → EXITED.
	term.StartupError = ""
	status, errStr, _ = svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, status)
	assert.Empty(t, errStr)

	// 5. No registry, no DB column, a live shell → READY.
	require.NoError(t, svc.Terminals.StartTerminal(context.Background(), terminal.Options{
		ID: "term-1", Shell: testutil.TestShell(), WorkingDir: t.TempDir(), Cols: 80, Rows: 24,
	}, func([]byte, int64, []terminal.Signal) {}, nil))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, "term-1")
	status, errStr, _ = svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, status)
	assert.Empty(t, errStr)
}

// exitedTerminalStatus turns READY into EXITED for a shell that exited, and
// leaves every startup status alone: a startup that has not ended, or that
// failed, owns the status.
func TestExitedTerminalStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status leapmuxv1.TerminalStatus
		exited bool
		want   leapmuxv1.TerminalStatus
	}{
		{leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, true, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED},
		{leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, false, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY},
		{leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, true, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING},
		{leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, true, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED},
		{leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, false, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, exitedTerminalStatus(c.status, c.exited), "status %v, exited %v", c.status, c.exited)
	}
}

// ---------------------------------------------------------------------------
// Agent startup_error persistence — happy + edge paths
// ---------------------------------------------------------------------------

// TestOpenAgent_PersistsStartupErrorOnFailure asserts the failure path of
// runAgentStartup writes the error to the agents.startup_error column so a
// worker restart preserves the STARTUP_FAILED state.
func TestOpenAgent_PersistsStartupErrorOnFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, errors.New("boom: forced start failure")
	}

	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	agentID := resp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	// Wait until the registry records STARTUP_FAILED — by construction
	// (see the reorder in runAgentStartup's failure path) this is the
	// last step of the goroutine, so the DB write has already landed.
	waitForStartupFailure(t, svc, agentID)

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Contains(t, row.StartupError, "forced start failure",
		"agents.startup_error column must retain the formatted error")
	assert.False(t, row.ClosedAt.Valid,
		"the row must stay open so the in-tab error UI is reachable")
}

// TestOpenAgent_ClearsStartupErrorOnSuccess ensures a prior failure's DB
// column is cleared when a subsequent runAgentStartup for the same id
// succeeds. Simulates the "previously-failed agent row reused" edge case
// by pre-populating the column and then invoking runAgentStartup directly
// with a success mock.
func TestOpenAgent_ClearsStartupErrorOnSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	agentID := "agent-clear-1"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkingDir:    t.TempDir(),
		Title:         "reused",
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "sonnet"}),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.SetAgentStartupError(ctx, db.SetAgentStartupErrorParams{
		StartupError: "old error",
		ID:           agentID,
	}))

	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return map[string]string{}, nil
	}

	startupHandle := svc.AgentStartup.begin(agentID, func() {})
	dbAgent, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	svc.runAgentStartup(ctx, dbAgent, gitModePlan{Mode: gitModeUseCurrent}, agent.Options{
		AgentID:       agentID,
		Options:       map[string]string{agent.OptionIDModel: "sonnet", agent.OptionIDEffort: "high"},
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, startupHandle)

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupError, "successful startup must clear startup_error")
}

// TestListAgents_ReportsStartupFailedFromDBColumnAfterRegistryWipe
// simulates a worker restart: an agent row has startup_error set but the
// in-memory registry is empty. ListAgents must still surface
// STARTUP_FAILED so the frontend renders the startup panel.
func TestListAgents_ReportsStartupFailedFromDBColumnAfterRegistryWipe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, errors.New("doom")
	}

	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	agentID := resp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	waitForStartupFailure(t, svc, agentID)

	// Simulate worker restart: wipe the in-memory registry while the DB
	// column remains populated.
	svc.AgentStartup = newAgentStartupRegistry(testutil.NewQuartzMock(t))

	listW := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: []string{agentID}}, listW)
	require.Empty(t, listW.errors)
	require.Len(t, listW.responses, 1)
	var listResp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(listW.responses[0].GetPayload(), &listResp))
	require.Len(t, listResp.GetAgents(), 1)
	info := listResp.GetAgents()[0]
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, info.GetStatus())
	assert.Contains(t, info.GetStartupError(), "doom")
	_ = ctx
}

// This test ensures that queue dispatch honors the persisted database column after a
// worker restart. Without this, a stale frontend could slip a message
// past the empty in-memory registry and trigger an ensureAgentRunning
// restart of a known-bad agent.
func TestEnqueueAgentInput_PersistedStartupErrorPausesFailedInput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	agentID := "agent-send-reject"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkingDir:    t.TempDir(),
		Title:         "failed",
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "sonnet"}),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.SetAgentStartupError(ctx, db.SetAgentStartupErrorParams{
		StartupError: "persisted failure",
		ID:           agentID,
	}))
	// Register a tab so requireAccessibleAgent succeeds via workspace ACL.

	dispatch(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: agentID,
		Text:    "hi",
	}, w)
	require.Empty(t, w.errors)
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && snapshot.Paused && len(snapshot.Items) == 1 &&
			snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED &&
			strings.Contains(snapshot.Items[0].Error, "failed to start")
	}, time.Second, 10*time.Millisecond)
}

// TestWatchEvents_CatchUpBroadcastsStartupFailedFromDBColumn asserts a
// late-subscribing watcher (e.g. the frontend reconnecting after a worker
// restart) still receives a STARTUP_FAILED status change sourced from the
// persisted DB column.
func TestWatchEvents_CatchUpBroadcastsStartupFailedFromDBColumn(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, errors.New("kaput")
	}

	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	agentID := resp.GetAgent().GetId()
	require.NotEmpty(t, agentID)
	waitForStartupFailure(t, svc, agentID)

	// Simulate worker restart: wipe the in-memory registry.
	svc.AgentStartup = newAgentStartupRegistry(testutil.NewQuartzMock(t))

	wWatch := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, ReplayId: 1}},
	}, wWatch)

	deadline := time.Now().Add(5 * time.Second)
	var sawFailed bool
	for time.Now().Before(deadline) && !sawFailed {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			sc := resp.GetAgentEvent().GetStatusChange()
			if sc == nil {
				continue
			}
			if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED && sc.GetStartupError() != "" {
				assert.Contains(t, sc.GetStartupError(), "kaput")
				sawFailed = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.True(t, sawFailed, "catch-up replay should emit STARTUP_FAILED from the persisted DB column")
}

// ---------------------------------------------------------------------------
// Terminal startup_error persistence
// ---------------------------------------------------------------------------

// TestOpenTerminal_PersistsStartupErrorOnFailure mirrors the agent test:
// runTerminalStartup's failure path writes to terminals.startup_error.
func TestOpenTerminal_PersistsStartupErrorOnFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("terminal boom")
	}

	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		WorkingDir: t.TempDir(),
		Shell:      testutil.TestShell(),
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	ids := collectTerminalIDs(w)
	require.Len(t, ids, 1)
	terminalID := ids[0]

	waitForStartupFailure(t, svc, terminalID)

	row, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.Contains(t, row.StartupError, "terminal boom")
	assert.False(t, row.ClosedAt.Valid,
		"the row must stay open so the in-tab error UI is reachable")
}

// TestListTerminals_ReportsStartupFailedFromDBColumnAfterRegistryWipe
// asserts the ListTerminals handler surfaces STARTUP_FAILED via the DB
// column when the in-memory registry has been wiped (worker restart).
// The in-memory manager has also forgotten the terminal (since PTY spawn
// failed), so this hits the DB-only branch of ListTerminals.
func TestListTerminals_ReportsStartupFailedFromDBColumnAfterRegistryWipe(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("pty no")
	}

	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		WorkingDir: t.TempDir(),
		Shell:      testutil.TestShell(),
	}, w)
	ids := collectTerminalIDs(w)
	require.Len(t, ids, 1)
	terminalID := ids[0]
	waitForStartupFailure(t, svc, terminalID)

	// Simulate worker restart.
	svc.TerminalStartup = newTerminalStartupRegistry(testutil.NewQuartzMock(t))

	listW := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{TabIds: []string{terminalID}}, listW)
	require.Empty(t, listW.errors)
	require.Len(t, listW.responses, 1)
	var listResp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(listW.responses[0].GetPayload(), &listResp))
	require.Len(t, listResp.GetTerminals(), 1)
	info := listResp.GetTerminals()[0]
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, info.GetStatus())
	assert.Contains(t, info.GetStartupError(), "pty no")
}

// TestDeriveAgentStatus_ActiveClearsLingeringDBError verifies that once a
// crashed subprocess is restarted (runtime ACTIVE), the derivation no
// longer surfaces a stale DB column. The test mirrors the race where
// queue dispatch restarts an agent through `ensureAgentRunning` before
// the DB column has been cleared.
func TestDeriveAgentStatus_ActiveClearsLingeringDBError(t *testing.T) {
	t.Parallel()

	dbAgent := db.Agent{ID: "agent-a", StartupError: "lingering"}
	status, errStr, _ := deriveAgentStatus(&dbAgent, agentLiveness{running: true})
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, status)
	assert.Empty(t, errStr, "runtime ACTIVE must suppress a lingering DB error")
}

// Sanity: confirm an agent row with ClosedAt set isn't reanimated by a
// lingering DB startup_error (not strictly necessary, but documents that
// closed rows are filtered by ListAgents' query, not by derivation).
func TestGetAgentByID_StartupErrorSurvivesClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	agentID := "agent-closed"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkingDir:    t.TempDir(),
		Title:         "closed",
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "sonnet"}),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.SetAgentStartupError(ctx, db.SetAgentStartupErrorParams{
		StartupError: "preserved",
		ID:           agentID,
	}))
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, agentID)))

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid)
	assert.Equal(t, "preserved", row.StartupError)

	// But ListAgentsByIDs (used by ListAgents) filters closed rows.
	rows, err := svc.Queries.ListAgentsByIDs(ctx, []string{agentID})
	require.NoError(t, err)
	assert.Empty(t, rows, "ListAgentsByIDs must not surface closed rows")
}

// The terminal manager keeps an exited terminal until RestartTerminal or
// RemoveTerminal, so "the manager holds it" does not mean "its shell runs".
// deriveTerminalStatus feeds the WatchEvents replay, which must state EXITED for
// a shell that exited while the manager still holds it, as ListTerminals does.
// Otherwise a page that loads cold reads READY, and Enter sends input to a dead
// shell instead of restarting it.
func TestDeriveTerminalStatus_AnExitedShellTheManagerStillHoldsIsExited(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	startTestTerminal(t, svc, ctx, "exited-in-manager")
	svc.Terminals.StopTerminal("exited-in-manager")
	svc.Terminals.WaitForExit("exited-in-manager")
	require.True(t, svc.Terminals.HasTerminal("exited-in-manager"), "precondition: the manager still holds the terminal")

	row, err := svc.Queries.GetTerminal(ctx, "exited-in-manager")
	require.NoError(t, err)
	status, startupError, _ := svc.deriveTerminalStatus(&row)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, status)
	assert.Empty(t, startupError)
}

// A live shell reads READY, and a terminal the manager does not hold reads
// EXITED: the two other inputs of the rule.
func TestDeriveTerminalStatus_ReadyWhileTheShellRunsAndExitedWithoutAShell(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	startTestTerminal(t, svc, ctx, "running")
	row, err := svc.Queries.GetTerminal(ctx, "running")
	require.NoError(t, err)
	status, _, _ := svc.deriveTerminalStatus(&row)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, status)

	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "no-shell", WorkingDir: t.TempDir(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	row, err = svc.Queries.GetTerminal(ctx, "no-shell")
	require.NoError(t, err)
	status, _, _ = svc.deriveTerminalStatus(&row)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, status)
}
