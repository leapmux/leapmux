package qwen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// planApproval is Qwen's plan approval: a session/request_permission of its
// `exit_plan_mode` tool, raised in sessionID with plan as the tool's argument.
func planApproval(t *testing.T, id int, sessionID string) []byte {
	t.Helper()
	return planApprovalOf(t, id, sessionID, "# Ship the release\n\n1. Tag it.")
}

func planApprovalOf(t *testing.T, id int, sessionID string, plan any) []byte {
	t.Helper()
	return frame(t, map[string]any{"id": id, "method": "session/request_permission", "params": map[string]any{
		"sessionId": sessionID,
		"options": []any{
			map[string]any{"optionId": contracts.QwenPermissionOptionProceedOnce, "name": "Yes", "kind": "allow_once"},
			map[string]any{"optionId": contracts.QwenPermissionOptionCancel, "name": "No, keep planning", "kind": "reject_once"},
		},
		"toolCall": map[string]any{
			"toolCallId": "call_plan",
			"rawInput":   map[string]any{"plan": plan},
			"_meta":      map[string]any{"toolName": contracts.QwenToolExitPlanMode},
		},
	}})
}

// The plan approval stores its plan, as each provider's plan approval does. A
// clear-context approval then hands the plan to the new session: without it,
// the fresh session received "Implement the plan." and held no plan to
// implement.
func TestQwenPlanApprovalStoresThePlan(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)

	a.HandleOutput(planApproval(t, 4, qwenTestSession))

	require.Equal(t, 1, sink.PlanUpdateCount())
	update := sink.LastPlanUpdate()
	assert.Equal(t, "Ship the release", update.Title)
	plan, err := msgcodec.Decompress(update.Content, update.Compression)
	require.NoError(t, err)
	assert.Equal(t, "# Ship the release\n\n1. Tag it.", string(plan), "the whole plan is stored, not only its title")
	assert.Equal(t, 1, sink.PublishedControlCount(), "the plan approval still reaches the reader")
}

func TestQwenPlanApprovalWithNoPlanStoresNothing(t *testing.T) {
	t.Parallel()
	for _, plan := range []any{nil, "", " \n\t", 7} {
		a, sink, _ := newQwenAgent(t, nil, nil)
		a.HandleOutput(planApprovalOf(t, 4, qwenTestSession, plan))
		assert.Zero(t, sink.PlanUpdateCount(), "plan %v", plan)
		assert.Equal(t, 1, sink.PublishedControlCount())
	}
}

// Only the plan approval states a plan. Another permission request that
// carries a `plan` argument stores nothing.
func TestQwenPermissionOfAnotherToolStoresNoPlan(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(frame(t, map[string]any{"id": 5, "method": "session/request_permission", "params": map[string]any{
		"sessionId": qwenTestSession,
		"toolCall": map[string]any{
			"toolCallId": "call_sh",
			"rawInput":   map[string]any{"command": "ls", "plan": "# Not a plan"},
			"_meta":      map[string]any{"toolName": contracts.QwenToolRunShellCommand},
		},
	}}))
	assert.Zero(t, sink.PlanUpdateCount())
}

// A context clear with an open plan approval releases the approval of the
// outgoing session. Qwen blocks the turn on it and reads `cancelled` as "No,
// keep planning", so the clear answers it and cancels that turn. Without that,
// a later stop of the NEW session answered the old approval, and the old turn
// planned on where nobody could see it.
func TestQwenClearContextReleasesThePlanApprovalOfTheOutgoingSession(t *testing.T) {
	t.Parallel()
	a, sink, requests := newQwenAgent(t, nil, qwenTaskResponder(`[]`, `{"cancelled":true}`))
	a.SetPromptActiveForTest(true)
	a.HandleOutput(planApproval(t, 30, qwenTestSession))
	card := sink.LastPublishedControl().RequestID

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncPeer(t, a)

	answers := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, answers["30"])
	assert.Equal(t, []string{card}, sink.CanceledControls())
	cancels := requestsFor(requests(), "session/cancel")
	require.Len(t, cancels, 1)
	assert.Equal(t, qwenTestSession, cancels[0].Params["sessionId"])

	// The old session asks again after the clear. The reader never sees it, and
	// its plan does not replace the plan that the reader approved.
	a.HandleOutput(planApprovalOf(t, 31, qwenTestSession, "# An older plan"))
	syncPeer(t, a)
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, 1, sink.PlanUpdateCount())
	answers = agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, answers["31"])
}
