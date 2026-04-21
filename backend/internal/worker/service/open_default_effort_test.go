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

// TestOpenAgent_DefaultsEffortFromModel verifies that when the OpenAgent
// request omits the effort, the backend fills it in from the resolved
// model's DefaultEffort (e.g. Claude Code's default Opus[1m] → "xhigh")
// rather than leaving it empty and letting the agent binary pick its
// own default.
func TestOpenAgent_DefaultsEffortFromModel(t *testing.T) {
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
	assert.Equal(t, "xhigh", effort,
		"agent.Options.Effort should default to the resolved model's DefaultEffort")

	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.NotNil(t, resp.GetAgent())

	assert.Equal(t, "xhigh", resp.GetAgent().GetEffort(),
		"response agent.effort should reflect the resolved default effort")

	require.Eventually(t, func() bool {
		dbAgent, err := svc.Queries.GetAgentByID(ctx, resp.GetAgent().GetId())
		return err == nil && dbAgent.Effort == "xhigh"
	}, 5*time.Second, 20*time.Millisecond)
}
