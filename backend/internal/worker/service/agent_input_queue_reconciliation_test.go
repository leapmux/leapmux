package service

import (
	"context"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeQueueReconciliationChecksRuntimeEligibilityBeforeStartup(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"unflagged", "closed", "archived", "failed", "failed clear", "failed recovery"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			registry := testRegistry
			if scenario != "unflagged" {
				registry = registryWithQueueReconciliation(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			}
			svc, _, _ := setupTestService(t, withRegistry(registry))
			const agentID = "agent-1"
			seedOpenAgent(t, svc, agentID, true)
			switch scenario {
			case "closed":
				require.NoError(t, closeErr(svc.Queries.CloseAgent(t.Context(), agentID)))
			case "archived":
				_, err := svc.DB.ExecContext(t.Context(), `UPDATE agents SET workspace_archived = 1 WHERE id = ?`, agentID)
				require.NoError(t, err)
			case "failed", "failed clear", "failed recovery":
				require.NoError(t, svc.Queries.SetAgentStartupError(t.Context(), db.SetAgentStartupErrorParams{ID: agentID, StartupError: "native startup failed"}))
			}
			var starts atomic.Int32
			svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
				starts.Add(1)
				return nil, assert.AnError
			}
			adapter := &agentInputQueueAdapter{svc: svc}
			required, err := adapter.RequiresQueueReconciliation(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, scenario != "unflagged", required)
			request := inputqueue.QueueReconciliationRequest{AgentID: agentID, InputID: "input", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE}
			switch scenario {
			case "failed clear":
				request.Kind = leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT
			case "failed recovery":
				request.InputID, request.Kind = "", 0
			}
			actual, err := adapter.ReconcileBeforeDispatch(t.Context(), request)
			if scenario == "failed" || scenario == "failed recovery" {
				assert.ErrorIs(t, err, errAgentStartupFailed)
			} else {
				assert.NoError(t, err)
			}
			assert.False(t, actual)
			assert.Zero(t, starts.Load())
		})
	}
}
