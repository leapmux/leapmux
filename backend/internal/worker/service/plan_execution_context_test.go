package service

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/require"
)

func TestPlanExecutionRetryKeepsItsPreparedSession(t *testing.T) {
	svc, _, _ := setupTestService(t)
	row := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "ExitPlanMode")
	_, err := svc.Agents.MockStartAgent(t.Context(), agent.Options{
		AgentID: "agent-1", WorkingDir: row.WorkingDir, ResumeSessionID: "original",
	}, svc.Output.NewSink("agent-1", row.AgentProvider))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAgent("agent-1") })
	_, err = svc.DB.ExecContext(t.Context(), `INSERT INTO control_response_answers
        (agent_id,request_id,claim_token,state,input_id,agent_session_id,agent_provider,plan_approval_settings)
        VALUES ('agent-1','approval','claim',?,'execute','original',?,?)`,
		int64(leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED),
		row.AgentProvider, []byte(`{"permissionMode":"acceptEdits","clearContext":true}`))
	require.NoError(t, err)
	starts := 0
	svc.startAgentFn = func(ctx context.Context, options agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		starts++
		options.ResumeSessionID = "prepared-session"
		sink.UpdateSessionID(options.ResumeSessionID)
		return svc.Agents.MockStartAgent(ctx, options, sink)
	}
	settingsCalls := 0
	svc.updateAgentSettingsFn = func(_ string, options OptionMap) agent.SettingsApplyResult {
		settingsCalls++
		if settingsCalls == 1 {
			return agent.SettingsApplyResult{}
		}
		settlements := agent.OptionSettlements{}
		for key, value := range options {
			settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		}
		return agent.SettingsApplyResult{AppliedLive: true, Settlements: settlements, SurfacedOptions: options}
	}
	item := inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ID: "execute", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION,
		Text: "Execute the approved plan.", TargetMode: "acceptEdits", PrepareContext: true,
	}}
	adapter := &agentInputQueueAdapter{svc: svc}
	_, err = adapter.Dispatch(item)
	require.ErrorContains(t, err, "confirm")
	_, err = adapter.Dispatch(item)
	require.NoError(t, err)
	require.Equal(t, 1, starts, "a retry must use the prepared session without clearing again")
	require.Equal(t, 2, settingsCalls, "execution must wait for confirmed settings in the prepared session")
}
