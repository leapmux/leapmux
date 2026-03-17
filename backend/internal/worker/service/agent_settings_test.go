package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestUpdateAgentSettings_ClearsSessionIDOnRestartFailure(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents)

	workDir := t.TempDir()

	// Create an agent in the DB with a session ID already set
	// (simulates a previously running agent that established a session).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  workDir,
		HomeDir:     t.TempDir(),
		Model:       "opus",
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "old-session-id",
		ID:             "agent-1",
	}))

	// Register a mock agent so HasAgent returns true and the restart
	// path is entered. The subsequent StartAgent call will fail because
	// the mock agent doesn't implement the Claude Code initialization
	// protocol (sendControlAndWait will time out or fail).
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Model:      "opus",
		WorkingDir: workDir,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	// Switch model — StopAndWaitAgent will kill the mock agent, then
	// StartAgent will try to start a real claude process which will fail
	// during the initialization handshake (startup timeout).
	// Use a very short startup timeout to speed up the test.
	svc.AgentStartupTimeout = 1 * time.Nanosecond // forces immediate timeout

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Model:   "sonnet",
	}, w)

	// Verify the session ID was cleared from the DB.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, dbAgent.AgentSessionID,
		"session ID should be cleared after failed restart")

	// Verify the model was still updated in the DB.
	assert.Equal(t, "sonnet", dbAgent.Model,
		"model should be updated even when restart fails")
}

func TestUpdateAgentSettings_DoesNotResumeSessionOnRestart(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")

	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents)

	// Create an agent with a session ID.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Model:       "opus",
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "old-session-id",
		ID:             "agent-1",
	}))

	// Agent is NOT running (HasAgent returns false), so the restart
	// block is skipped. Verify the DB update works correctly.
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Model:   "sonnet",
	}, w)

	// Verify the response was successful (no errors).
	require.Empty(t, w.errors, "expected no errors")
	require.Len(t, w.responses, 1, "expected one response")

	var resp leapmuxv1.UpdateAgentSettingsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	// Verify the model was updated.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "sonnet", dbAgent.Model)

	// Session ID should remain unchanged when no restart was attempted.
	assert.Equal(t, "old-session-id", dbAgent.AgentSessionID,
		"session ID should not change when agent is not running")
}
