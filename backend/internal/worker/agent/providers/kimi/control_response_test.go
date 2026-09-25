package kimi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The stored requests the resolution reads, in the server's own shape.
const (
	kimiStoredApproval = `{"type":"event.approval.requested","agentId":"main","approval_id":"approval_1","agent_id":"main",` +
		`"tool_name":"Bash","action":"Running: ls","tool_input_display":{"kind":"command","command":"ls"}}`
	kimiStoredPlan = `{"type":"event.approval.requested","agentId":"main","approval_id":"approval_2","agent_id":"main",` +
		`"tool_name":"ExitPlanMode","tool_input_display":{"kind":"plan_review","plan":"# Plan\n1. Do it.",` +
		`"options":[{"label":"Approach A","description":"Fast"},{"label":"Approach B"}]}}`
	kimiStoredSubagentPlan = `{"type":"event.approval.requested","agentId":"agent-0","approval_id":"approval_4","agent_id":"agent-0",` +
		`"tool_name":"ExitPlanMode","tool_input_display":{"kind":"plan_review","plan":"# A subagent's plan",` +
		`"options":[{"label":"Approach A"},{"label":"Approach B"}]}}`
	kimiStoredGoal = `{"type":"event.approval.requested","agentId":"main","approval_id":"approval_3","agent_id":"main",` +
		`"tool_name":"CreateGoal","tool_input_display":{"kind":"goal_start","objective":"Ship it"}}`
	kimiStoredQuestion = `{"type":"event.question.requested","agentId":"main","question_id":"question_1","agent_id":"main","questions":[` +
		`{"id":"q_0","question":"Color?","options":[{"id":"opt_0_0","label":"Red"},{"id":"opt_0_1","label":"Blue"}],"allow_other":true},` +
		`{"id":"q_1","question":"Tags?","options":[{"id":"opt_1_0","label":"a"},{"id":"opt_1_1","label":"b"}],"multi_select":true}]}`
)

// browserAnswer is the neutral envelope the browser sends, with the Kimi fields
// beside the behavior.
func browserAnswer(t *testing.T, requestID, behavior string, fields map[string]any) []byte {
	t.Helper()
	inner := map[string]any{"behavior": behavior}
	for key, value := range fields {
		inner[key] = value
	}
	data, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": requestID, "response": inner}})
	require.NoError(t, err)
	return data
}

func resolve(t *testing.T, requestID, stored string, content []byte, plan *leapmuxv1.PlanApprovalSettings) agent.ControlResponseResolution {
	t.Helper()
	return kimiProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID: requestID, RequestPayload: json.RawMessage(stored), ResponseContent: content, PlanApproval: plan,
	})
}

// nativeResponse reads the server body out of a resolved envelope.
func nativeResponse(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string         `json:"subtype"`
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(content, &envelope), string(content))
	require.Equal(t, "control_response", envelope.Type)
	return envelope.Response.Response
}

func TestKimiControlResponseConformance(t *testing.T) {
	t.Parallel()
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, kimiProvider{})
	agenttest.AssertPreservesTheResponseWithoutARequest(t, kimiProvider{})
}

func TestKimiResolvePermission(t *testing.T) {
	t.Parallel()

	t.Run("an approval", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_1", kimiStoredApproval, browserAnswer(t, "approval_1", "allow", nil), nil)
		require.False(t, res.Withhold)
		assert.Equal(t, map[string]any{"decision": "approved"}, nativeResponse(t, res.Content))
		assert.Equal(t, agent.PlanModeControlNone, res.PlanModeControl)
	})

	t.Run("an approval for the session", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_1", kimiStoredApproval, browserAnswer(t, "approval_1", "allow", map[string]any{"scope": "session"}), nil)
		require.False(t, res.Withhold)
		assert.Equal(t, map[string]any{"decision": "approved", "scope": "session"}, nativeResponse(t, res.Content))
	})

	t.Run("a refusal carries its reason", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_1", kimiStoredApproval, browserAnswer(t, "approval_1", "deny", map[string]any{"message": "Use rg instead."}), nil)
		require.False(t, res.Withhold)
		assert.Equal(t, map[string]any{"decision": "rejected", "feedback": "Use rg instead."}, nativeResponse(t, res.Content))
		assert.Empty(t, res.Feedback, "the reason rides the refusal itself")
	})

	t.Run("a refusal with the placeholder reason carries none", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_1", kimiStoredApproval, browserAnswer(t, "approval_1", "deny", map[string]any{"message": agent.ControlRejectedByUserMessage}), nil)
		assert.Equal(t, map[string]any{"decision": "rejected"}, nativeResponse(t, res.Content))
	})

	for name, fields := range map[string]map[string]any{
		"a scope the server does not offer":    {"scope": "forever"},
		"a choice a permission does not offer": {"choice": "Approach A"},
	} {
		t.Run(name+" is withheld", func(t *testing.T) {
			t.Parallel()
			res := resolve(t, "approval_1", kimiStoredApproval, browserAnswer(t, "approval_1", "allow", fields), nil)
			assert.True(t, res.Withhold)
		})
	}

	t.Run("a scope on a refusal is dropped", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_1", kimiStoredApproval, browserAnswer(t, "approval_1", "deny", map[string]any{"scope": "session"}), nil)
		require.False(t, res.Withhold)
		assert.Equal(t, map[string]any{"decision": "rejected"}, nativeResponse(t, res.Content))
	})
}

func TestKimiResolvePlan(t *testing.T) {
	t.Parallel()

	t.Run("an approval leaves plan mode in the mode the user picked", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "allow", nil),
			&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.KimiModeYolo})
		require.False(t, res.Withhold)
		assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
		assert.Equal(t, map[string]any{"decision": "approved", "permission_mode": "yolo"}, nativeResponse(t, res.Content))
	})

	t.Run("an approval with no pick returns to the default mode", func(t *testing.T) {
		t.Parallel()
		for _, picked := range []string{"", contracts.KimiModePlan, "bogus"} {
			res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "allow", nil),
				&leapmuxv1.PlanApprovalSettings{PermissionMode: picked})
			assert.Equal(t, contracts.KimiDefaultMode, nativeResponse(t, res.Content)["permission_mode"], "picked %q", picked)
		}
		res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "allow", nil), nil)
		assert.Equal(t, contracts.KimiDefaultMode, nativeResponse(t, res.Content)["permission_mode"])
	})

	t.Run("an approval that clears the context switches no mode", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "allow", nil),
			&leapmuxv1.PlanApprovalSettings{ClearContext: true, PermissionMode: contracts.KimiModeAuto})
		assert.Equal(t, map[string]any{"decision": "approved"}, nativeResponse(t, res.Content))
		assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
	})

	t.Run("an approval of one of the plan's own options", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "allow", map[string]any{"choice": "Approach B"}),
			&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.KimiModeManual})
		assert.Equal(t, map[string]any{"decision": "approved", "selected_label": "Approach B", "permission_mode": "manual"}, nativeResponse(t, res.Content))
	})

	t.Run("an option the plan did not offer is withheld", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "allow", map[string]any{"choice": "Approach C"}), nil)
		assert.True(t, res.Withhold)
	})

	t.Run("a revision request carries the feedback", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_2", kimiStoredPlan,
			browserAnswer(t, "approval_2", "deny", map[string]any{"choice": contracts.KimiPlanLabelRevise, "message": "Split step 1."}), nil)
		require.False(t, res.Withhold)
		assert.Equal(t, map[string]any{"decision": "rejected", "selected_label": "Revise", "feedback": "Split step 1."}, nativeResponse(t, res.Content))
		assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
	})

	t.Run("a refusal that ends plan mode", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_2", kimiStoredPlan,
			browserAnswer(t, "approval_2", "deny", map[string]any{"choice": contracts.KimiPlanLabelRejectAndExit}), nil)
		assert.Equal(t, map[string]any{"decision": "rejected", "selected_label": "Reject and Exit"}, nativeResponse(t, res.Content))
	})

	t.Run("a plan option on a refusal is withheld", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "deny", map[string]any{"choice": "Approach A"}), nil)
		assert.True(t, res.Withhold)
	})

	t.Run("a plan takes no session scope", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "allow", map[string]any{"scope": "session"}), nil)
		assert.True(t, res.Withhold)
	})
}

// A subagent's plan review is the subagent's own: its answer moves no mode of
// the main agent, and no plan approval of the main agent's runs for it.
func TestKimiResolveASubagentPlan(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		behavior string
		fields   map[string]any
		plan     *leapmuxv1.PlanApprovalSettings
		native   map[string]any
	}{
		"an approval": {
			behavior: "allow",
			plan:     &leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.KimiModeYolo},
			native:   map[string]any{"decision": "approved"},
		},
		"an approval that asks for a fresh context": {
			behavior: "allow",
			plan:     &leapmuxv1.PlanApprovalSettings{ClearContext: true, PermissionMode: contracts.KimiModeAuto},
			native:   map[string]any{"decision": "approved"},
		},
		"an approval of one of the plan's own options": {
			behavior: "allow",
			fields:   map[string]any{"choice": "Approach B"},
			native:   map[string]any{"decision": "approved", "selected_label": "Approach B"},
		},
		"a revision request": {
			behavior: "deny",
			fields:   map[string]any{"choice": contracts.KimiPlanLabelRevise, "message": "Split step 1."},
			native:   map[string]any{"decision": "rejected", "selected_label": "Revise", "feedback": "Split step 1."},
		},
		"a refusal that ends plan mode": {
			behavior: "deny",
			fields:   map[string]any{"choice": contracts.KimiPlanLabelRejectAndExit},
			native:   map[string]any{"decision": "rejected", "selected_label": "Reject and Exit"},
		},
		"a refusal with feedback": {
			behavior: "deny",
			fields:   map[string]any{"message": "Not yet."},
			native:   map[string]any{"decision": "rejected", "feedback": "Not yet."},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := resolve(t, "approval_4", kimiStoredSubagentPlan, browserAnswer(t, "approval_4", tc.behavior, tc.fields), tc.plan)
			require.False(t, res.Withhold)
			assert.Equal(t, agent.PlanModeControlNone, res.PlanModeControl, "the main agent does not leave plan mode")
			assert.Equal(t, tc.native, nativeResponse(t, res.Content), "no permission mode rides the answer")
		})
	}

	t.Run("an option the plan did not offer is withheld", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "approval_4", kimiStoredSubagentPlan, browserAnswer(t, "approval_4", "allow", map[string]any{"choice": "Approach C"}), nil)
		assert.True(t, res.Withhold)
	})

	t.Run("a plan review with no agent is the main agent's", func(t *testing.T) {
		t.Parallel()
		stored := `{"type":"event.approval.requested","approval_id":"approval_5","tool_name":"ExitPlanMode",` +
			`"tool_input_display":{"kind":"plan_review","plan":"# Plan"}}`
		res := resolve(t, "approval_5", stored, browserAnswer(t, "approval_5", "allow", nil), nil)
		assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl, "the server words an interaction with no agent tag as the main agent's")
	})
}

func TestKimiResolveGoalStart(t *testing.T) {
	t.Parallel()

	res := resolve(t, "approval_3", kimiStoredGoal, browserAnswer(t, "approval_3", "allow", map[string]any{"choice": "auto"}), nil)
	require.False(t, res.Withhold)
	assert.Equal(t, map[string]any{"decision": "approved", "selected_label": "auto", "permission_mode": "auto"}, nativeResponse(t, res.Content),
		"the stored reply states the mode the worker applies before it posts")

	res = resolve(t, "approval_3", kimiStoredGoal, browserAnswer(t, "approval_3", "allow", map[string]any{"choice": "plan"}), nil)
	assert.True(t, res.Withhold, "plan is not a mode a goal starts in")

	res = resolve(t, "approval_3", kimiStoredGoal, browserAnswer(t, "approval_3", "deny", map[string]any{"choice": "auto"}), nil)
	assert.True(t, res.Withhold, "a refusal starts the goal in no mode")

	res = resolve(t, "approval_3", kimiStoredGoal, browserAnswer(t, "approval_3", "allow", nil), nil)
	assert.Equal(t, map[string]any{"decision": "approved"}, nativeResponse(t, res.Content), "a plain approval keeps the current mode")

	res = resolve(t, "approval_3", kimiStoredGoal, browserAnswer(t, "approval_3", "deny", map[string]any{"message": "Not now."}), nil)
	require.False(t, res.Withhold)
	assert.Equal(t, map[string]any{"decision": "rejected", "feedback": "Not now."}, nativeResponse(t, res.Content),
		"a refused goal carries its reason and no mode")
	assert.Equal(t, agent.PlanModeControlNone, res.PlanModeControl)

	res = resolve(t, "approval_3", kimiStoredGoal, browserAnswer(t, "approval_3", "deny", nil), nil)
	assert.Equal(t, map[string]any{"decision": "rejected"}, nativeResponse(t, res.Content))
}

func TestKimiResolveAPlainRefusalOfThePlan(t *testing.T) {
	t.Parallel()
	res := resolve(t, "approval_2", kimiStoredPlan, browserAnswer(t, "approval_2", "deny", map[string]any{"message": "Too broad."}),
		&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.KimiModeAuto})
	require.False(t, res.Withhold)
	assert.Equal(t, map[string]any{"decision": "rejected", "feedback": "Too broad."}, nativeResponse(t, res.Content),
		"a refusal switches no mode, whatever the banner picked")
	assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl, "the answer is the plan exit's own")
}

func TestKimiResolveQuestion(t *testing.T) {
	t.Parallel()

	answer := func(t *testing.T, answers any) agent.ControlResponseResolution {
		t.Helper()
		return resolve(t, "question_1", kimiStoredQuestion, browserAnswer(t, "question_1", "allow", map[string]any{"answers": answers}), nil)
	}

	t.Run("every answer kind that fits", func(t *testing.T) {
		t.Parallel()
		answers := map[string]any{
			"q_0": map[string]any{"kind": "single", "option_id": "opt_0_1"},
			"q_1": map[string]any{"kind": "multi_with_other", "option_ids": []string{"opt_1_0"}, "other_text": "c"},
		}
		res := answer(t, answers)
		require.False(t, res.Withhold)
		body := nativeResponse(t, res.Content)
		assert.Equal(t, kimiQuestionMethod, body["method"])
		encoded, err := json.Marshal(body["answers"])
		require.NoError(t, err)
		expected, err := json.Marshal(answers)
		require.NoError(t, err)
		assert.JSONEq(t, string(expected), string(encoded))

		for _, fits := range []map[string]any{
			{"q_1": map[string]any{"kind": "multi", "option_ids": []string{"opt_1_0", "opt_1_1"}}},
			{"q_0": map[string]any{"kind": "other", "text": "Green"}},
			{"q_0": map[string]any{"kind": "skipped"}},
			{"q_1": map[string]any{"kind": "multi_with_other", "option_ids": []string{}, "other_text": "only other"}},
		} {
			assert.False(t, answer(t, fits).Withhold, "%v", fits)
		}
	})

	t.Run("an answer that does not fit its question is withheld", func(t *testing.T) {
		t.Parallel()
		for name, answers := range map[string]any{
			"no answers":                 map[string]any{},
			"answers that are not a map": []string{"Red"},
			"an unknown question":        map[string]any{"q_9": map[string]any{"kind": "skipped"}},
			"an option of another":       map[string]any{"q_0": map[string]any{"kind": "single", "option_id": "opt_1_0"}},
			"multi on a single question": map[string]any{"q_0": map[string]any{"kind": "multi", "option_ids": []string{"opt_0_0"}}},
			"multi with no option":       map[string]any{"q_1": map[string]any{"kind": "multi", "option_ids": []string{}}},
			"an unoffered multi option":  map[string]any{"q_1": map[string]any{"kind": "multi", "option_ids": []string{"opt_0_0"}}},
			"multi_with_other, no text":  map[string]any{"q_1": map[string]any{"kind": "multi_with_other", "option_ids": []string{"opt_1_0"}}},
			"other with no text":         map[string]any{"q_0": map[string]any{"kind": "other"}},
			"a kind the server lacks":    map[string]any{"q_0": map[string]any{"kind": "maybe"}},
			"multi_with_other on single": map[string]any{"q_0": map[string]any{"kind": "multi_with_other", "option_ids": []string{}, "other_text": "x"}},
		} {
			assert.True(t, answer(t, answers).Withhold, name)
		}
		res := resolve(t, "question_1", kimiStoredQuestion, browserAnswer(t, "question_1", "allow", nil), nil)
		assert.True(t, res.Withhold, "an approval with no answers answers nothing")
	})

	t.Run("a refusal with no reason dismisses the question and sends nothing", func(t *testing.T) {
		t.Parallel()
		for _, fields := range []map[string]any{nil, {"message": agent.ControlRejectedByUserMessage}} {
			res := resolve(t, "question_1", kimiStoredQuestion, browserAnswer(t, "question_1", "deny", fields), nil)
			require.False(t, res.Withhold)
			assert.Equal(t, map[string]any{"dismiss": true}, nativeResponse(t, res.Content))
			assert.Empty(t, res.Feedback, "the placeholder reason is no message of the user's")
		}
	})

	t.Run("a refusal dismisses the question and sends its reason as a message", func(t *testing.T) {
		t.Parallel()
		res := resolve(t, "question_1", kimiStoredQuestion, browserAnswer(t, "question_1", "deny", map[string]any{"message": "Pick for me."}), nil)
		require.False(t, res.Withhold)
		assert.Equal(t, map[string]any{"dismiss": true}, nativeResponse(t, res.Content))
		assert.Equal(t, "Pick for me.", res.Feedback)
	})
}

func TestKimiResolveWithholdsWhatItCannotRead(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		stored  string
		content []byte
	}{
		"no decision":                      {kimiStoredApproval, []byte(`{"response":{"request_id":"approval_1","response":{}}}`)},
		"an unknown behavior":              {kimiStoredApproval, []byte(`{"response":{"request_id":"approval_1","response":{"behavior":"maybe"}}}`)},
		"content that is not JSON":         {kimiStoredApproval, []byte(`not json`)},
		"another request's answer":         {kimiStoredApproval, []byte(`{"response":{"request_id":"approval_9","response":{"behavior":"allow"}}}`)},
		"a stored request of another kind": {`{"type":"tool.call.started"}`, []byte(`{"response":{"request_id":"approval_1","response":{"behavior":"allow"}}}`)},
		"fields of the wrong type":         {kimiStoredApproval, []byte(`{"response":{"request_id":"approval_1","response":{"behavior":"allow","scope":7}}}`)},
	} {
		res := resolve(t, "approval_1", tc.stored, tc.content, nil)
		assert.True(t, res.Withhold, name)
	}
}

func TestKimiPlanExitMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{contracts.KimiModeManual, contracts.KimiModeYolo, contracts.KimiModeAuto} {
		assert.Equal(t, mode, kimiPlanExitMode(mode))
	}
	assert.Equal(t, contracts.KimiDefaultMode, kimiPlanExitMode(contracts.KimiModePlan))
	assert.Equal(t, contracts.KimiDefaultMode, kimiPlanExitMode(""))
}
