package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// These tests pin the contract that every agent-process stop path drives
// OutputHandler.ClearAgentRuntimeState — the helper is unit-tested
// elsewhere; here we prove each call site actually reaches it. A
// regression that drops one of those calls would let stale
// control_requests survive the process restart and reappear on the next
// WatchEvents subscribe.

// seedPendingControlRequest creates a DB row + registers a watcher and
// returns the request ID, ready for assertions on cleanup behavior.
func seedPendingControlRequest(t *testing.T, ctx context.Context, svc *Context, w *testResponseWriter, agentID, workspaceID string) string {
	t.Helper()

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkspaceID:   workspaceID,
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	requestID := "req-" + agentID
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   agentID,
		RequestID: requestID,
		Payload:   []byte(`{"jsonrpc":"2.0","id":1,"method":"tool/permission"}`),
	}))

	svc.Watchers.WatchAgent(agentID, &EventWatcher{
		ChannelID: w.channelID,
		Sender:    channel.NewSender(w),
	})
	return requestID
}

func assertControlRequestsCleared(t *testing.T, ctx context.Context, svc *Context, w *testResponseWriter, agentID, expectedRequestID string) {
	t.Helper()

	rows, err := svc.Queries.ListControlRequestsByAgentID(ctx, agentID)
	require.NoError(t, err)
	assert.Empty(t, rows, "control_requests rows should be deleted")

	cancels := collectBroadcastCancelIDs(t, w)
	assert.Contains(t, cancels, expectedRequestID, "expected a controlCancel broadcast for the cleared request")
}

// TestCloseAgent_ClearsPendingControlRequests proves the CloseAgent RPC
// stop-callback drives ClearAgentRuntimeState. A regression dropping the
// call from agent.go's CloseAgent handler would leave stale rows that
// the next WatchEvents replay would re-emit as unanswerable prompts.
func TestCloseAgent_ClearsPendingControlRequests(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	requestID := seedPendingControlRequest(t, ctx, svc, w, "agent-close", "ws-1")

	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId: "agent-close",
	}, w)

	require.Empty(t, w.errors, "unexpected RPC errors: %+v", w.errors)
	assertControlRequestsCleared(t, ctx, svc, w, "agent-close", requestID)
}

// TestInitiatePlanExecutionRestart_ClearsPendingControlRequests proves
// the plan-approval restart path drives ClearAgentRuntimeState before
// the new subprocess starts. The new process has its own request_id
// namespace, so any stale row would be unanswerable by it.
func TestInitiatePlanExecutionRestart_ClearsPendingControlRequests(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	// Mock a successful restart so the test exercises only the cleanup
	// path, not a real Claude Code subprocess.
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return &leapmuxv1.AgentSettings{}, nil
	}

	requestID := seedPendingControlRequest(t, ctx, svc, w, "agent-plan", "ws-1")
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-plan")
	require.NoError(t, err)

	svc.initiatePlanExecutionRestart("agent-plan", agent.PermissionModeDefault, dbAgent, "Execute the plan.")

	assertControlRequestsCleared(t, ctx, svc, w, "agent-plan", requestID)
}

// TestHandleClearContext_ClearsPendingControlRequests proves the /clear
// command drives ClearAgentRuntimeState before restarting the agent
// with a fresh context.
func TestHandleClearContext_ClearsPendingControlRequests(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return &leapmuxv1.AgentSettings{}, nil
	}

	requestID := seedPendingControlRequest(t, ctx, svc, w, "agent-clear", "ws-1")

	svc.handleClearContext("agent-clear")

	assertControlRequestsCleared(t, ctx, svc, w, "agent-clear", requestID)
}

// TestSubprocessCrash_FiresClearAgentRuntimeState wires the runner-style
// onExit handler and proves a subprocess exit (graceful or otherwise)
// drives the cleanup. This is the only place outside an explicit RPC
// where stale rows could survive into the next session, so the chain
// {Manager.startAgentWith Wait goroutine → onExit → ClearAgentRuntimeState}
// is the sole guard.
func TestSubprocessCrash_FiresClearAgentRuntimeState(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	// Mirror runner.go's wiring: every subprocess exit should run the
	// service-aware cleanup against svc.Output.
	cleared := make(chan string, 1)
	svc.Agents.SetOnExit(func(agentID string, _ int, _ error) {
		svc.Output.ClearAgentRuntimeState(agentID)
		cleared <- agentID
	})

	requestID := seedPendingControlRequest(t, ctx, svc, w, "agent-crash", "ws-1")

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-crash",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-crash", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)

	svc.Agents.StopAgent("agent-crash")

	select {
	case got := <-cleared:
		assert.Equal(t, "agent-crash", got)
	case <-time.After(2 * time.Second):
		t.Fatal("onExit handler never fired after subprocess stop")
	}

	assertControlRequestsCleared(t, ctx, svc, w, "agent-crash", requestID)
}
