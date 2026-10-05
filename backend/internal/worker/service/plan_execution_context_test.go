package service

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/require"
)

func TestPlanExecutionRetryKeepsItsPreparedSession(t *testing.T) {
	svc, _, _ := setupTestService(t)
	row := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "ExitPlanMode")
	_, err := svc.Agents.StartAgentWith(t.Context(), agent.Options{
		AgentID: "agent-1", WorkingDir: row.WorkingDir, ResumeSessionID: "original",
	}, svc.Output.NewSink("agent-1", row.AgentProvider), claudetest.StartEcho)
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
		return svc.Agents.StartAgentWith(ctx, options, sink, claudetest.StartEcho)
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

// TestPlanExecutionResumesThePreparedSessionAfterTheProcessExits pins the
// session that the cold start of a plan execution resumes.
//
// The dispatch reads the agent row first, and the context replacement then
// records the prepared session on that row. When the process that the
// replacement started exits before the plan reaches it, the dispatch starts the
// agent again. That start must resume the prepared session, which the row holds
// now. The session in the row that the dispatch read first is the replaced
// one, and validateSession refuses the plan in it.
func TestPlanExecutionResumesThePreparedSessionAfterTheProcessExits(t *testing.T) {
	svc, _, _ := setupTestService(t)
	row := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "ExitPlanMode")
	// An agent opened from a selected native session resumes its stored session
	// without a user message in it, so the cold start has a session to resume.
	_, err := svc.DB.ExecContext(t.Context(), `UPDATE agents SET resumed = 1 WHERE id = 'agent-1'`)
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), `INSERT INTO control_response_answers
        (agent_id,request_id,claim_token,state,input_id,agent_session_id,agent_provider,plan_approval_settings)
        VALUES ('agent-1','approval','claim',?,'execute','original',?,?)`,
		int64(leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED),
		row.AgentProvider, []byte(`{"permissionMode":"acceptEdits","clearContext":true}`))
	require.NoError(t, err)
	var resumeIDs []string
	svc.startAgentFn = func(ctx context.Context, options agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		resumeIDs = append(resumeIDs, options.ResumeSessionID)
		if len(resumeIDs) == 1 {
			// The replacement's process reports the prepared session and exits
			// before the plan reaches it: it never stays in the manager.
			sink.UpdateSessionID("prepared-session")
			return map[string]string{}, nil
		}
		// A resumed provider reports the session that it resumed.
		sink.UpdateSessionID(options.ResumeSessionID)
		return svc.Agents.StartAgentWith(ctx, options, sink, claudetest.StartEcho)
	}
	t.Cleanup(func() { svc.Agents.StopAgent("agent-1") })
	svc.updateAgentSettingsFn = func(_ string, options OptionMap) agent.SettingsApplyResult {
		settlements := agent.OptionSettlements{}
		for key, value := range options {
			settlements[key] = agent.OptionSettlement{State: agent.OptionSettlementConfirmed, Value: &value}
		}
		return agent.SettingsApplyResult{AppliedLive: true, Settlements: settlements, SurfacedOptions: options}
	}

	_, err = (&agentInputQueueAdapter{svc: svc}).Dispatch(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ID: "execute", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION,
		Text: "Execute the approved plan.", TargetMode: "acceptEdits", PrepareContext: true,
	}})
	require.Equal(t, []string{"", "prepared-session"}, resumeIDs,
		"the cold start resumed the session from the row that the dispatch read before the context replacement")
	require.NoError(t, err, "the plan must run in the prepared session")
}
