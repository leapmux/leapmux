package agent_test

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The function field makes this value non-comparable. Runtime identity must
// use its registration rather than compare its Agent interface values.
type reconciliationRuntime struct {
	agenttest.IdleAgent
	reconcile func(context.Context) error
}

func (runtime reconciliationRuntime) ReconcileBeforeDispatch(ctx context.Context) error {
	return runtime.reconcile(ctx)
}

func TestManagerReconcileQueueStateReleasesLifecycleDuringNativeIO(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	manager := agent.NewManager(testRegistry, nil)
	manager.PutAgentForTest("root", reconciliationRuntime{reconcile: func(ctx context.Context) error {
		acquired := make(chan struct{})
		go func() {
			unlock := manager.LockAgent("root")
			defer unlock()
			close(acquired)
		}()
		select {
		case <-acquired:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	assert.NoError(t, manager.ReconcileQueueState(ctx, "root"))
}

func TestManagerReconcileQueueStateRejectsAReplacementRegistration(t *testing.T) {
	t.Parallel()
	manager := agent.NewManager(testRegistry, nil)
	manager.PutAgentForTest("root", reconciliationRuntime{reconcile: func(context.Context) error {
		manager.PutAgentForTest("root", agenttest.IdleAgent{})
		return nil
	}})
	assert.ErrorIs(t, manager.ReconcileQueueState(t.Context(), "root"), agent.ErrAgentNotFound)
	assert.True(t, manager.AgentAlive("root"))
}

func TestManagerReconcileQueueStateReportsMissingRuntimeAndCapability(t *testing.T) {
	t.Parallel()
	manager := agent.NewManager(testRegistry, nil)
	assert.ErrorIs(t, manager.ReconcileQueueState(t.Context(), "missing"), agent.ErrAgentNotFound)
	manager.PutAgentForTest("root", agenttest.IdleAgent{})
	assert.ErrorContains(t, manager.ReconcileQueueState(t.Context(), "root"), "cannot synchronize native queue state")
}

func TestManagerReconcileQueueStatePreservesNativeErrorsAndContext(t *testing.T) {
	t.Parallel()
	manager := agent.NewManager(testRegistry, nil)
	manager.PutAgentForTest("root", reconciliationRuntime{reconcile: func(context.Context) error { return assert.AnError }})
	assert.ErrorIs(t, manager.ReconcileQueueState(t.Context(), "root"), assert.AnError)
	type contextKey struct{}
	key := contextKey{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key, "caller value"))
	cancel()
	manager.PutAgentForTest("root", reconciliationRuntime{reconcile: func(received context.Context) error {
		require.Equal(t, "caller value", received.Value(key))
		return received.Err()
	}})
	assert.ErrorIs(t, manager.ReconcileQueueState(ctx, "root"), context.Canceled)
}
