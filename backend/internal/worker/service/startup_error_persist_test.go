package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	svc, _, _ := setupTestService(t, "ws-1")

	dbAgent := db.Agent{ID: "agent-1"}

	// 1. Runtime ACTIVE: isRunning=true wins over everything.
	svc.AgentStartup.begin("agent-1", func() {})
	dbAgent.StartupError = "stale error"
	status, errStr := svc.deriveAgentStatus(&dbAgent, true)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, status)
	assert.Empty(t, errStr)

	// 2. Registry STARTING: isRunning=false + registry populated.
	dbAgent.StartupError = ""
	status, errStr = svc.deriveAgentStatus(&dbAgent, false)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, status)
	assert.Empty(t, errStr)

	// 3. Registry STARTUP_FAILED: fail() wins over DB column.
	dbAgent.StartupError = "stale db error"
	svc.AgentStartup.fail("agent-1", "registry error")
	status, errStr = svc.deriveAgentStatus(&dbAgent, false)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "registry error", errStr)

	// 4. Clear the registry; DB column now drives STARTUP_FAILED.
	svc.AgentStartup.cancelAndClear("agent-1")
	status, errStr = svc.deriveAgentStatus(&dbAgent, false)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "stale db error", errStr)

	// 5. No registry entry, no DB column, not running → INACTIVE.
	dbAgent.StartupError = ""
	status, errStr = svc.deriveAgentStatus(&dbAgent, false)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, status)
	assert.Empty(t, errStr)
}

// TestDeriveTerminalStatus_AllBranches is the terminal analogue. The
// terminal flavor has no "runtime ACTIVE" concept in the derivation
// (callers decide exited vs running) — only registry > DB > READY.
func TestDeriveTerminalStatus_AllBranches(t *testing.T) {
	svc, _, _ := setupTestService(t, "ws-1")

	term := db.Terminal{ID: "term-1"}

	// 1. Registry STARTING.
	svc.TerminalStartup.begin("term-1", func() {})
	status, errStr := svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, status)
	assert.Empty(t, errStr)

	// 2. Registry STARTUP_FAILED wins over DB column.
	term.StartupError = "stale db error"
	svc.TerminalStartup.fail("term-1", "registry error")
	status, errStr = svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "registry error", errStr)

	// 3. Registry cleared → DB column drives STARTUP_FAILED.
	svc.TerminalStartup.cancelAndClear("term-1")
	status, errStr = svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "stale db error", errStr)

	// 4. No registry, no DB column → READY.
	term.StartupError = ""
	status, errStr = svc.deriveTerminalStatus(&term)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, status)
	assert.Empty(t, errStr)
}

// ---------------------------------------------------------------------------
// Agent startup_error persistence — happy + edge paths
// ---------------------------------------------------------------------------

// TestOpenAgent_PersistsStartupErrorOnFailure asserts the failure path of
// runAgentStartup writes the error to the agents.startup_error column so a
// worker restart preserves the STARTUP_FAILED state.
func TestOpenAgent_PersistsStartupErrorOnFailure(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return nil, errors.New("boom: forced start failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
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
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	agentID := "agent-clear-1"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		Title:         "reused",
		Model:         "sonnet",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.SetAgentStartupError(ctx, db.SetAgentStartupErrorParams{
		StartupError: "old error",
		ID:           agentID,
	}))

	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return &leapmuxv1.AgentSettings{}, nil
	}

	svc.AgentStartup.begin(agentID, func() {})
	svc.runAgentStartup(ctx, agentID, gitModeResult{}, agent.Options{
		AgentID:       agentID,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, "sonnet", "high", nil)

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupError, "successful startup must clear startup_error")
}

// TestListAgents_ReportsStartupFailedFromDBColumnAfterRegistryWipe
// simulates a worker restart: an agent row has startup_error set but the
// in-memory registry is empty. ListAgents must still surface
// STARTUP_FAILED so the frontend renders the startup panel.
func TestListAgents_ReportsStartupFailedFromDBColumnAfterRegistryWipe(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return nil, errors.New("doom")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
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
	svc.AgentStartup = newAgentStartupRegistry()

	listW := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{agentID}}, listW)
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

// TestSendAgentMessage_RejectedByPersistedStartupError ensures the
// SendAgentMessage gate also honors the persisted DB column after a
// worker restart. Without this, a stale frontend could slip a message
// past the empty in-memory registry and trigger an ensureAgentRunning
// restart of a known-bad agent.
func TestSendAgentMessage_RejectedByPersistedStartupError(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	agentID := "agent-send-reject"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		Title:         "failed",
		Model:         "sonnet",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.SetAgentStartupError(ctx, db.SetAgentStartupErrorParams{
		StartupError: "persisted failure",
		ID:           agentID,
	}))
	// Register a tab so requireAccessibleAgent succeeds via workspace ACL.

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "hi",
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(9), w.errors[0].code, "want FAILED_PRECONDITION")
	assert.Contains(t, w.errors[0].message, "failed to start")
}

// TestWatchEvents_CatchUpBroadcastsStartupFailedFromDBColumn asserts a
// late-subscribing watcher (e.g. the frontend reconnecting after a worker
// restart) still receives a STARTUP_FAILED status change sourced from the
// persisted DB column.
func TestWatchEvents_CatchUpBroadcastsStartupFailedFromDBColumn(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return nil, errors.New("kaput")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
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
	svc.AgentStartup = newAgentStartupRegistry()

	wWatch := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: 0}},
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
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.startTerminalFn = func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("terminal boom")
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       testutil.TestShell(),
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
	svc, d, w := setupTestService(t, "ws-1")
	svc.startTerminalFn = func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("pty no")
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       testutil.TestShell(),
	}, w)
	ids := collectTerminalIDs(w)
	require.Len(t, ids, 1)
	terminalID := ids[0]
	waitForStartupFailure(t, svc, terminalID)

	// Simulate worker restart.
	svc.TerminalStartup = newTerminalStartupRegistry()

	listW := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{terminalID}}, listW)
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
// `SendAgentMessage` restarts an agent via `ensureAgentRunning` before
// the DB column has been cleared.
func TestDeriveAgentStatus_ActiveClearsLingeringDBError(t *testing.T) {
	svc, _, _ := setupTestService(t, "ws-1")
	dbAgent := db.Agent{ID: "agent-a", StartupError: "lingering"}
	status, errStr := svc.deriveAgentStatus(&dbAgent, true)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, status)
	assert.Empty(t, errStr, "runtime ACTIVE must suppress a lingering DB error")
}

// Sanity: confirm an agent row with ClosedAt set isn't reanimated by a
// lingering DB startup_error (not strictly necessary, but documents that
// closed rows are filtered by ListAgents' query, not by derivation).
func TestGetAgentByID_StartupErrorSurvivesClose(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	agentID := "agent-closed"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		Title:         "closed",
		Model:         "sonnet",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.SetAgentStartupError(ctx, db.SetAgentStartupErrorParams{
		StartupError: "preserved",
		ID:           agentID,
	}))
	require.NoError(t, svc.Queries.CloseAgent(ctx, agentID))

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid)
	assert.Equal(t, "preserved", row.StartupError)

	// But ListAgentsByIDs (used by ListAgents) filters closed rows.
	rows, err := svc.Queries.ListAgentsByIDs(ctx, []string{agentID})
	require.NoError(t, err)
	assert.Empty(t, rows, "ListAgentsByIDs must not surface closed rows")

	_ = sql.ErrNoRows // silence unused-import
}
