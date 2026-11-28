package gemini

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

const (
	geminiTestRequestID = "jsonrpc:4"
	geminiTestPlanPath  = "/private/gemini/session/plans/plan.md"
	geminiTestPlanText  = "# Complete native plan\n\n1. Inspect the source."
	geminiTestCallID    = "exit_plan_mode__native-call"
)

// geminiTestToolCall is the compact exit_plan_mode call of Gemini CLI's plan
// approval.
func geminiTestToolCall() map[string]any {
	return map[string]any{
		"toolCallId": geminiTestCallID,
		"title":      "Requesting plan approval for: " + geminiTestPlanPath,
		"kind":       "other",
		"status":     "pending",
		"content":    []any{},
	}
}

// geminiTestPermission is a session/request_permission for toolCall, with the
// two options that Gemini CLI offers.
func geminiTestPermission(t *testing.T, toolCall map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "session/request_permission",
		"params": map[string]any{
			"sessionId": "native-root",
			"toolCall":  toolCall,
			"options": []any{
				map[string]any{"optionId": "proceed_once", "name": "Allow", "kind": "allow_once"},
				map[string]any{"optionId": "cancel", "name": "Reject", "kind": "reject_once"},
			},
		},
	})
	require.NoError(t, err)
	return payload
}

// geminiTestSupplement is the supplement that PublishControlRequest stores on
// the plan request's source row.
func geminiTestSupplement() map[string]any {
	return map[string]any{
		"sessionUpdate":     "tool_call",
		"toolCallId":        geminiTestCallID,
		"geminiPlanPath":    geminiTestPlanPath,
		"geminiPlanContent": geminiTestPlanText,
	}
}

// geminiTestSource is a loader of the plan request's source row: the
// exit_plan_mode call frame with overrides applied, and supplement as its
// supplement.
func geminiTestSource(t *testing.T, overrides map[string]any, supplement any) func() (agent.MessageContent, bool) {
	t.Helper()
	frame := map[string]any{"sessionUpdate": "tool_call"}
	maps.Copy(frame, geminiTestToolCall())
	maps.Copy(frame, overrides)
	original, err := json.Marshal(frame)
	require.NoError(t, err)
	supplemental, err := json.Marshal(supplement)
	require.NoError(t, err)
	return func() (agent.MessageContent, bool) {
		return agent.MessageContent{Original: original, Supplemental: supplemental}, true
	}
}

func geminiRead(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	return Registration().Plugin.ReadControlRequest(input)
}

func geminiPlanPrompt(text string) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: text}}}
}

func TestReadControlRequest_GeminiPlanReadsThePlanFromItsExactSource(t *testing.T) {
	t.Parallel()
	input := agent.NewControlRequestInput(geminiTestPermission(t, geminiTestToolCall()))
	input.Source = geminiTestSource(t, nil, geminiTestSupplement())
	agenttest.RequireProto(t, geminiPlanPrompt(geminiTestPlanText), geminiRead(input))
}

// A plan that the source row cannot prove is this request's own stays out of
// the prompt, which is still a plan.
func TestReadControlRequest_GeminiPlanRefusesAMissingForeignOrMalformedSource(t *testing.T) {
	t.Parallel()
	payload := geminiTestPermission(t, geminiTestToolCall())
	agenttest.RequireProto(t, geminiPlanPrompt(""), geminiRead(agent.NewControlRequestInput(payload)))

	for name, overrides := range map[string]map[string]any{
		"another call":       {"toolCallId": "exit_plan_mode__foreign"},
		"another plan file":  {"title": "Requesting plan approval for: /other/plan.md"},
		"another update":     {"sessionUpdate": "tool_call_update"},
		"a malformed update": {"sessionUpdate": 7},
	} {
		input := agent.NewControlRequestInput(payload)
		input.Source = geminiTestSource(t, overrides, geminiTestSupplement())
		got := geminiRead(input)
		assert.Truef(t, proto.Equal(geminiPlanPrompt(""), got), "%s: %s", name, protojson.Format(got))
	}

	for name, supplement := range map[string]any{
		"null":              nil,
		"an array":          []any{},
		"an empty object":   map[string]any{},
		"another plan file": map[string]any{"toolCallId": geminiTestCallID, "geminiPlanPath": "/other/plan.md", "geminiPlanContent": "foreign"},
		"another call":      map[string]any{"toolCallId": "foreign", "geminiPlanPath": geminiTestPlanPath, "geminiPlanContent": "foreign"},
		"a number of text":  map[string]any{"toolCallId": geminiTestCallID, "geminiPlanPath": geminiTestPlanPath, "geminiPlanContent": 0},
	} {
		input := agent.NewControlRequestInput(payload)
		input.Source = geminiTestSource(t, nil, supplement)
		got := geminiRead(input)
		assert.Truef(t, proto.Equal(geminiPlanPrompt(""), got), "%s: %s", name, protojson.Format(got))
	}
}

// Only Gemini's exit_plan_mode request with a plan path is a plan. Every
// other request, an ordinary tool or a lookalike, is the base protocol's, and
// its reader never loads the plan source.
func TestReadControlRequest_GeminiReadsOrdinaryToolsAndLookalikesThroughTheBase(t *testing.T) {
	t.Parallel()
	read := func(payload []byte) *leapmuxv1.ControlPrompt {
		input := agent.NewControlRequestInput(payload)
		input.Source = func() (agent.MessageContent, bool) {
			t.Errorf("the reader loads the plan source of a request that is no plan: %s", payload)
			return agent.MessageContent{}, false
		}
		return geminiRead(input)
	}

	ordinary := geminiTestToolCall()
	ordinary["toolCallId"] = "run_shell_command__native-call"
	ordinary["kind"] = "execute"
	ordinary["rawInput"] = map[string]any{"command": "printf native"}
	permission := read(geminiTestPermission(t, ordinary)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "printf native", permission.GetCommand())
	require.Len(t, permission.GetOptions(), 2)
	assert.Equal(t, "proceed_once", permission.GetOptions()[0].GetOptionId())
	assert.Equal(t, "cancel", permission.GetOptions()[1].GetOptionId())

	for name, overrides := range map[string]map[string]any{
		"a bare tool title":       {"title": "exit_plan_mode"},
		"a title with no path":    {"title": "Requesting plan approval for: "},
		"an id with no separator": {"toolCallId": "exit_plan_mode"},
		"another tool's id":       {"toolCallId": "write_file__native-call"},
	} {
		toolCall := geminiTestToolCall()
		maps.Copy(toolCall, overrides)
		assert.NotNil(t, read(geminiTestPermission(t, toolCall)).GetPermission(), name)
	}

	var otherMethod map[string]any
	require.NoError(t, json.Unmarshal(geminiTestPermission(t, geminiTestToolCall()), &otherMethod))
	otherMethod["method"] = "_x/other"
	encoded, err := json.Marshal(otherMethod)
	require.NoError(t, err)
	assert.NotNil(t, read(encoded).GetPermission(), "only a permission request asks for a plan approval")

	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), read([]byte(`{"method":"unknown"}`)))
}

// Approve sends the neutral allow with the plan settings, and the Worker
// turns it into Gemini's proceed_once option and leaves plan mode.
func TestAnswerControlRequest_GeminiPlanApprovalTakesTheNeutralAllow(t *testing.T) {
	t.Parallel()
	payload := geminiTestPermission(t, geminiTestToolCall())
	got, err := agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: "autoEdit", ClearContext: true}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:4","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	require.NotNil(t, got.PlanApproval, "an approval carries its plan settings")
	assert.Equal(t, "autoEdit", got.PlanApproval.GetPermissionMode())
	assert.True(t, got.PlanApproval.GetClearContext())
	assert.Empty(t, got.FollowUp)

	resolved := agenttest.ResolveAnswer(Registration().Plugin, payload, geminiTestRequestID, got)
	assert.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"outcome":{"outcome":"selected","optionId":"proceed_once"}}}`, string(resolved.Content))
	assert.Equal(t, agent.PlanModeControlExit, resolved.PlanModeControl)
}

// Reject denies with the feedback. The Worker selects Gemini's cancel option
// and queues the feedback itself, so the answer states no follow-up of its
// own: two would fail the answer.
func TestAnswerControlRequest_GeminiPlanRefusalTakesTheNeutralDeny(t *testing.T) {
	t.Parallel()
	payload := geminiTestPermission(t, geminiTestToolCall())
	got, err := agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split the second step."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:4","response":{"behavior":"deny","message":"Split the second step."}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval, "a refusal carries no plan settings")
	assert.Empty(t, got.FollowUp)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, payload, geminiTestRequestID, got)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"outcome":{"outcome":"selected","optionId":"cancel"}}}`, string(resolved.Content))
	assert.Equal(t, agent.FeedbackFollowUp("Split the second step."), resolved.FollowUp)

	got, err = agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:4","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content))
	assert.Equal(t, agent.ControlFollowUp{}, agenttest.ResolveAnswer(Registration().Plugin, payload, geminiTestRequestID, got).FollowUp, "a bare refusal queues no message")
}

func TestAnswerControlRequest_GeminiPlanRefusesADecisionItDoesNotOffer(t *testing.T) {
	t.Parallel()
	payload := geminiTestPermission(t, geminiTestToolCall())
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"a choice the plan does not offer": agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "proceed_once"}),
		"a permission decision":            agenttest.PermissionDecision("proceed_once", ""),
		"an elicitation decision":          agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ""),
	} {
		_, err := agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// An ordinary permission selects Gemini's own option, and a refusal's reason
// follows the answer as a message, because Gemini states no field for it.
func TestAnswerControlRequest_GeminiAnswersAnOrdinaryPermissionThroughTheBase(t *testing.T) {
	t.Parallel()
	ordinary := geminiTestToolCall()
	ordinary["toolCallId"] = "run_shell_command__native-call"
	payload := geminiTestPermission(t, ordinary)

	got, err := agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, agenttest.PermissionDecision("cancel", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:4","result":{"outcome":{"outcome":"selected","optionId":"cancel"}}}`, string(got.Content))
	assert.Equal(t, "use the other file", got.FollowUp)
	assert.Equal(t, agent.ControlFollowUp{}, agenttest.ResolveAnswer(Registration().Plugin, payload, geminiTestRequestID, got).FollowUp, "the Worker queues the answer's follow-up, not a second one")

	got, err = agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, agenttest.PermissionDecision("proceed_once", ""))
	require.NoError(t, err)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, payload, geminiTestRequestID, got)
	assert.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"outcome":{"outcome":"selected","optionId":"proceed_once"}}}`, string(resolved.Content))
	assert.Equal(t, agent.PlanModeControlNone, resolved.PlanModeControl)

	_, err = agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an ordinary permission takes no plan decision")
}

func TestAnswerControlRequest_GeminiAnswersAnElicitationThroughTheBase(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"jsonrpc":"2.0","id":4,"method":"elicitation/create","params":{"mode":"form","message":"Choose a count.","requestedSchema":{"type":"object"}}}`)
	elicitation := geminiRead(agent.NewControlRequestInput(payload)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "Choose a count.", elicitation.GetMessage())

	got, err := agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:4","response":{"action":"decline"}}}`, string(got.Content))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"action":"decline"}}`, string(agenttest.ResolveAnswer(Registration().Plugin, payload, geminiTestRequestID, got).Content))
}

func TestReadControlRequest_GeminiPlanFallsBackWhenOptionsChanged(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "session/request_permission",
		"params": map[string]any{
			"sessionId": "native-root",
			"toolCall":  geminiTestToolCall(),
			"options": []any{
				map[string]any{"optionId": "approve_v2", "name": "Approve", "kind": "allow_once"},
				map[string]any{"optionId": "reject_v2", "name": "Reject", "kind": "reject_once"},
			},
		},
	})
	require.NoError(t, err)
	prompt := geminiRead(agent.NewControlRequestInput(payload))
	require.NotNil(t, prompt.GetPermission(), "renamed plan options must fall back to a permission prompt so the user can choose offered options")
	got, err := agenttest.AnswerControl(Registration().Plugin, payload, geminiTestRequestID, agenttest.PermissionDecision("approve_v2", ""))
	require.NoError(t, err)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, payload, geminiTestRequestID, got)
	require.NoError(t, resolved.Refusal())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"outcome":{"outcome":"selected","optionId":"approve_v2"}}}`, string(resolved.Content))
}

func TestGeminiConfigureHandlesNilServicesWithoutPanic(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	require.NotPanics(t, func() {
		_ = a.configure(t.Context(), agent.Options{}, nil)
	})
}
