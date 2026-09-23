package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/require"
)

func createPlanSessionTestAgent(t *testing.T, svc *Service, provider leapmuxv1.AgentProvider, tool string) db.Agent {
	t.Helper()
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: provider,
		Options: marshalOptions(map[string]string{
			agent.OptionIDPermissionMode: "on-request", contracts.CodexOptionSandboxPolicy: codex.SandboxWorkspaceWrite,
			contracts.CodexOptionNetworkAccess: codex.NetworkRestricted, contracts.CodexOptionCollaborationMode: codex.CollaborationPlan,
		}),
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "original"}))
	path := filepath.Join(t.TempDir(), "plan.md")
	writeTestPlanFile(t, path, "# Plan\n\nKeep this plan in its original session.\n")
	require.NoError(t, svc.Queries.UpdateAgentPlan(t.Context(), db.UpdateAgentPlanParams{ID: "agent-1", PlanFilePath: path}))
	_, err := svc.InputQueue.SetPaused(t.Context(), "agent-1", true)
	require.NoError(t, err)
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", AgentSessionID: "original", RequestID: "plan", ClaimToken: "claim",
		Payload: []byte(`{"request":{"tool_name":"` + tool + `"}}`),
	})
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	return row
}

func TestPlanApprovalRefusesAReplacedSessionBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		provider leapmuxv1.AgentProvider
		tool     string
		clear    bool
	}{
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "CodexPlanModePrompt", false},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "ExitPlanMode", true},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			old := createPlanSessionTestAgent(t, svc, tc.provider, tc.tool)
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "replacement"}))
			err := svc.processControlResponse(old, &leapmuxv1.SendControlResponseRequest{
				ClaimToken: "claim", Content: []byte(`{"response":{"request_id":"plan","response":{"behavior":"allow"}}}`),
				PlanApproval: &leapmuxv1.PlanApprovalSettings{ClearContext: tc.clear},
			})
			require.ErrorContains(t, err, "session")
			current, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			require.Equal(t, old.Options, current.Options)
			snapshot, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			require.Empty(t, snapshot.Items)
		})
	}
}

func TestRestrictivePlanModeDoesNotEnableBypassOptions(t *testing.T) {
	svc, _, _ := setupTestService(t)
	row := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "CodexPlanModePrompt")
	require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{
		ClaimToken: "claim", Content: []byte(`{"response":{"request_id":"plan","response":{"behavior":"allow"}}}`),
		PlanApproval: &leapmuxv1.PlanApprovalSettings{PermissionMode: "on-request"},
	}))
	current, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	options := parseOptions(current.Options)
	require.Equal(t, codex.SandboxWorkspaceWrite, options[contracts.CodexOptionSandboxPolicy])
	require.Equal(t, codex.NetworkRestricted, options[contracts.CodexOptionNetworkAccess])
}

func TestPlanApprovalRequiresConfirmedLiveSettings(t *testing.T) {
	svc, _, _ := setupTestService(t)
	row := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "CodexPlanModePrompt")
	// This process accepts input but cannot confirm Codex settings.
	_, err := svc.Agents.StartAgentWith(t.Context(), agent.Options{
		AgentID: "agent-1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		WorkingDir: row.WorkingDir, APITimeout: 10 * time.Millisecond,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAgent("agent-1") })
	err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{
		ClaimToken: "claim", Content: []byte(`{"response":{"request_id":"plan","response":{"behavior":"allow"}}}`),
	})
	require.ErrorContains(t, err, "confirm")
	snapshot, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Empty(t, snapshot.Items, "execution cannot start with unconfirmed plan settings")
}

func TestPlanApprovalReportsSettingsPersistenceFailure(t *testing.T) {
	svc, _, _ := setupTestService(t)
	row := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "CodexPlanModePrompt")
	_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_plan_settings BEFORE UPDATE OF options ON agents
WHEN NEW.id='agent-1' BEGIN SELECT RAISE(ABORT, 'plan settings storage failed'); END`)
	require.NoError(t, err)
	err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{
		ClaimToken: "claim", Content: []byte(`{"response":{"request_id":"plan","response":{"behavior":"allow"}}}`),
	})
	require.ErrorContains(t, err, "plan settings storage failed")
	snapshot, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Empty(t, snapshot.Items)
}

func TestNativePlanRecordingDoesNotChangeAReplacementSession(t *testing.T) {
	svc, _, _ := setupTestService(t)
	old := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "ExitPlanMode")
	response := []byte(`{"response":{"request_id":"plan","response":{"behavior":"allow"}}}`)
	claimed, err := svc.Output.claimControlResponseAnswer(db.ClaimControlResponseAnswerParams{
		AgentID: "agent-1", RequestID: "plan", ClaimToken: "claim", AgentSessionID: "original",
		AgentProvider: old.AgentProvider, RequestPayload: []byte(`{"request":{"tool_name":"ExitPlanMode"}}`),
		ResponseContent: response, ResolvedContent: response,
		PlanApprovalSettings: []byte(`{"permissionMode":"bypassPermissions","clearContext":false}`),
	})
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = svc.Queries.SetControlResponseDeliveryState(t.Context(), db.SetControlResponseDeliveryStateParams{
		AgentID: "agent-1", RequestID: "plan", ClaimToken: "claim", State: storedStateDelivered, RequiredState: storedStatePending,
	})
	require.NoError(t, err)
	answer, err := svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{AgentID: "agent-1", RequestID: "plan", ClaimToken: "claim"})
	require.NoError(t, err)
	require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "replacement"}))
	require.NoError(t, svc.finalizeControlResponse(answer))
	current, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Equal(t, old.Options, current.Options, "recording an old answer cannot change the replacement's permission mode")
}

func TestPlanContextClearPreservesAnUnchangedPermissionChoice(t *testing.T) {
	svc, _, _ := setupTestService(t)
	row := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "CodexPlanModePrompt")
	values := parseOptions(row.Options)
	values[agent.OptionIDPermissionMode] = "never"
	row.Options = marshalOptions(values)
	_, err := svc.DB.ExecContext(t.Context(), `UPDATE agents SET options=? WHERE id=?`, row.Options, row.ID)
	require.NoError(t, err)
	require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{
		ClaimToken: "claim", Content: []byte(`{"response":{"request_id":"plan","response":{"behavior":"allow"}}}`),
		PlanApproval: &leapmuxv1.PlanApprovalSettings{ClearContext: true},
	}))
	snapshot, err := svc.InputQueue.Snapshot(t.Context(), row.ID)
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	require.Equal(t, "never", snapshot.Items[0].TargetMode)
}
