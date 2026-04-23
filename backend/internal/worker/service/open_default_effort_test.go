package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// TestOpenAgent_DefaultsEffortToAuto verifies that when the OpenAgent
// request omits the effort, the backend fills it in with the "auto"
// sentinel so the agent binary picks its own default (rather than pinning
// Leapmux to a specific effort name that older CLIs may not recognize).
func TestOpenAgent_DefaultsEffortToAuto(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	var capturedMu sync.Mutex
	var captured agent.Options
	done := make(chan struct{})
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		capturedMu.Lock()
		captured = opts
		capturedMu.Unlock()
		close(done)
		return &leapmuxv1.AgentSettings{}, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)

	require.Empty(t, w.errors, "OpenAgent should succeed")
	require.Len(t, w.responses, 1)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}

	capturedMu.Lock()
	effort := captured.Effort
	capturedMu.Unlock()
	assert.Equal(t, "auto", effort,
		"agent.Options.Effort should default to \"auto\" (CLI picks its own default)")

	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.NotNil(t, resp.GetAgent())

	assert.Equal(t, "auto", resp.GetAgent().GetEffort(),
		"response agent.effort should echo the \"auto\" sentinel")

	require.Eventually(t, func() bool {
		dbAgent, err := svc.Queries.GetAgentByID(ctx, resp.GetAgent().GetId())
		return err == nil && dbAgent.Effort == "auto"
	}, 5*time.Second, 20*time.Millisecond)
}

// TestOpenAgent_RespectsEnvOverride verifies that when
// LEAPMUX_CLAUDE_DEFAULT_EFFORT is set, the backend injects that value
// instead of the "auto" sentinel. This is the documented escape hatch for
// users who want to pin a specific effort across workspaces.
func TestOpenAgent_RespectsEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_EFFORT", "high")

	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	var capturedMu sync.Mutex
	var captured agent.Options
	done := make(chan struct{})
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		capturedMu.Lock()
		captured = opts
		capturedMu.Unlock()
		close(done)
		return &leapmuxv1.AgentSettings{}, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)

	require.Empty(t, w.errors, "OpenAgent should succeed")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}

	capturedMu.Lock()
	effort := captured.Effort
	capturedMu.Unlock()
	assert.Equal(t, "high", effort,
		"env var LEAPMUX_CLAUDE_DEFAULT_EFFORT should override the \"auto\" default")
}

// TestOpenAgent_PreservesExplicitEffort verifies that an effort specified
// on the OpenAgent request passes through untouched, even when the env var
// override is set (explicit request wins).
func TestOpenAgent_PreservesExplicitEffort(t *testing.T) {
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_EFFORT", "high")

	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	var capturedMu sync.Mutex
	var captured agent.Options
	done := make(chan struct{})
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		capturedMu.Lock()
		captured = opts
		capturedMu.Unlock()
		close(done)
		return &leapmuxv1.AgentSettings{}, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Effort:        "medium",
	}, w)

	require.Empty(t, w.errors, "OpenAgent should succeed")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}

	capturedMu.Lock()
	effort := captured.Effort
	capturedMu.Unlock()
	assert.Equal(t, "medium", effort,
		"explicit effort in OpenAgent request should win over env var override")
}
