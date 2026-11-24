package reasonix

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

const reasonixTestRequestID = "jsonrpc:3"

// reasonixPlanGate is Reasonix's plan gate: a permission for its
// exit_plan_mode tool, with the two options that the Worker selects.
const reasonixPlanGate = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{
	"sessionId":"plan-session",
	"toolCall":{"toolCallId":"gate-1","title":"exit_plan_mode","kind":"other","status":"pending",
		"_meta":{"reasonix.io":{"approvalId":"1","fresh":false,"subject":"","tool":"exit_plan_mode"}}},
	"options":[{"optionId":"allow_once","name":"Allow","kind":"allow_once"},{"optionId":"reject_once","name":"Reject","kind":"reject_once"}]}}`

// reasonixWritePermission is a write approval with Reasonix's two remembered
// scopes.
const reasonixWritePermission = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{
	"sessionId":"s",
	"toolCall":{"toolCallId":"w","kind":"edit","title":"write_file","_meta":{"reasonix.io":{"tool":"write_file"}}},
	"options":[
		{"optionId":"reasonix_write_once","kind":"allow_once","name":"Allow once"},
		{"optionId":"reasonix_write_session","kind":"allow_always","name":"Allow these directories for this session"},
		{"optionId":"reasonix_write_project","kind":"allow_always","name":"Add to project allow_write"},
		{"optionId":"reasonix_write_deny","kind":"reject_once","name":"Reject"}]}}`

// withJSON returns payload with one more change applied to its decoded form.
func withJSON(t *testing.T, payload string, change func(map[string]any)) string {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &decoded))
	change(decoded)
	encoded, err := json.Marshal(decoded)
	require.NoError(t, err)
	return string(encoded)
}

func reasonixPlanToolCall(decoded map[string]any) map[string]any {
	return decoded["params"].(map[string]any)["toolCall"].(map[string]any)
}

// Reasonix streams the proposal before its gate, so the plan prompt repeats
// no text of it.
func TestReadControlRequest_ReasonixPlanGateIsAPlanWithNoText(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}, agenttest.ControlPromptOf(Registration().Plugin, []byte(reasonixPlanGate)))
}

// Only the exact native metadata on a permission request is the gate. A
// title that names the tool is a lookalike, and the base reads it as an
// ordinary permission.
func TestReadControlRequest_ReasonixRecognizesOnlyTheExactGate(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string]string{
		"another method": withJSON(t, reasonixPlanGate, func(decoded map[string]any) { decoded["method"] = "_x/other" }),
		"another tool": withJSON(t, reasonixPlanGate, func(decoded map[string]any) {
			reasonixPlanToolCall(decoded)["_meta"] = map[string]any{"reasonix.io": map[string]any{"tool": "bash"}}
		}),
		"no metadata": withJSON(t, reasonixPlanGate, func(decoded map[string]any) {
			reasonixPlanToolCall(decoded)["_meta"] = map[string]any{}
		}),
		"a tool that is no string": withJSON(t, reasonixPlanGate, func(decoded map[string]any) {
			reasonixPlanToolCall(decoded)["_meta"] = map[string]any{"reasonix.io": map[string]any{"tool": 7}}
		}),
	} {
		permission := agenttest.ControlPromptOf(Registration().Plugin, []byte(payload)).GetPermission()
		require.NotNil(t, permission, name)
		assert.Equal(t, []string{"allow_once", "reject_once"}, reasonixOptionIDs(permission.GetOptions()), name)
	}
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(Registration().Plugin, []byte(`{}`)))
}

func reasonixOptionIDs(options []*leapmuxv1.PermissionOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

// Every other request is the base protocol's: the command of a shell call,
// the options in the order that Reasonix sends them, the MCP form, and the
// unrecognized prompt of a request that nothing recognizes.
func TestReadControlRequest_ReasonixReadsEveryOtherRequestThroughTheBase(t *testing.T) {
	t.Parallel()
	shell := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"session/request_permission","params":{
		"options":[{"optionId":"allow_once","name":"Allow","kind":"allow_once"}],
		"toolCall":{"toolCallId":"c","kind":"execute","rawInput":{"command":"touch x"}}}}`)).GetPermission()
	require.NotNil(t, shell)
	assert.Equal(t, "touch x", shell.GetCommand())
	assert.Equal(t, "execute", shell.GetTitle(), "a call with no title is titled by its kind")
	assert.Equal(t, []string{"allow_once", acp.CancelOptionID}, reasonixOptionIDs(shell.GetOptions()),
		"a request with no reject option still offers a refusal")

	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title: "write_file",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "reasonix_write_once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
			{OptionId: "reasonix_write_session", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow these directories for this session"},
			{OptionId: "reasonix_write_project", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Add to project allow_write"},
			{OptionId: "reasonix_write_deny", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject"},
		},
	}}}, agenttest.ControlPromptOf(Registration().Plugin, []byte(reasonixWritePermission)))

	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Message: "Choose a count.", SchemaJson: `{"type":"object","properties":{"count":{"type":"string","maxLength":4096}}}`,
	}}}, agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"elicitation/create","params":{"sessionId":"session-1","mode":"form","message":"Choose a count.",
		"requestedSchema":{"type":"object","properties":{"count":{"type":"string","maxLength":4096}}}}}`)))

	hi := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"elicitation/create","params":{"message":"Hi"}}`)).GetElicitation()
	require.NotNil(t, hi)
	assert.Equal(t, "Hi", hi.GetMessage())
	assert.Equal(t, "form", hi.GetMode(), "an elicitation that states no mode is a form")

	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"x"}`)))
}

// Approve sends the neutral allow with the plan settings, and the Worker
// turns it into the gate's allow_once option and leaves plan mode.
func TestAnswerControlRequest_ReasonixPlanApprovalTakesTheNeutralAllow(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(reasonixPlanGate), reasonixTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: "normal"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:3","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	require.NotNil(t, got.PlanApproval)
	assert.Equal(t, "normal", got.PlanApproval.GetPermissionMode())
	assert.False(t, got.PlanApproval.GetClearContext())
	assert.Empty(t, got.FollowUp)

	resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(reasonixPlanGate), reasonixTestRequestID, got)
	assert.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`, string(resolved.Content))
	assert.Equal(t, agent.PlanModeControlExit, resolved.PlanModeControl)
}

// Reject denies with the revision text. The Worker selects the gate's
// reject_once option, which carries no text, and queues the text itself, so
// the answer states no follow-up of its own: two would fail the answer.
func TestAnswerControlRequest_ReasonixPlanRefusalTakesTheNeutralDeny(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(reasonixPlanGate), reasonixTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Keep the old API."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:3","response":{"behavior":"deny","message":"Keep the old API."}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(reasonixPlanGate), reasonixTestRequestID, got)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":{"outcome":{"outcome":"selected","optionId":"reject_once"}}}`, string(resolved.Content))
	assert.Equal(t, agent.FeedbackFollowUp("Keep the old API."), resolved.FollowUp)
	assert.Equal(t, agent.PlanModeControlNone, resolved.PlanModeControl, "a refused plan keeps plan mode")

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(reasonixPlanGate), reasonixTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:3","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content))
	assert.Equal(t, agent.ControlFollowUp{}, agenttest.ResolveAnswer(Registration().Plugin, []byte(reasonixPlanGate), reasonixTestRequestID, got).FollowUp, "a bare refusal queues no message")
}

func TestAnswerControlRequest_ReasonixPlanRefusesADecisionItDoesNotOffer(t *testing.T) {
	t.Parallel()
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"a choice the gate does not offer": agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "allow_once"}),
		"the gate's own option":            agenttest.PermissionDecision("allow_once", ""),
	} {
		_, err := agenttest.AnswerControl(Registration().Plugin, []byte(reasonixPlanGate), reasonixTestRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// An ordinary permission selects the chosen option of Reasonix's own, under
// the native id once the Worker delivers it. A refusal's reason follows the
// answer as a message, because Reasonix states no field for it.
func TestAnswerControlRequest_ReasonixAnswersAnOrdinaryPermissionThroughTheBase(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"reasonix_write_once", "reasonix_write_session", "reasonix_write_project", "reasonix_write_deny"} {
		got, err := agenttest.AnswerControl(Registration().Plugin, []byte(reasonixWritePermission), reasonixTestRequestID, agenttest.PermissionDecision(id, ""))
		require.NoError(t, err, id)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"`+id+`"}}}`, string(got.Content), id)
		assert.Empty(t, got.FollowUp, id)
		resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(reasonixWritePermission), reasonixTestRequestID, got)
		assert.False(t, resolved.Withhold, id)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":{"outcome":{"outcome":"selected","optionId":"`+id+`"}}}`, string(resolved.Content), id)
	}

	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(reasonixWritePermission), reasonixTestRequestID, agenttest.PermissionDecision("reasonix_write_deny", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"reasonix_write_deny"}}}`, string(got.Content))
	assert.Equal(t, "use the other file", got.FollowUp)
	assert.Equal(t, agent.ControlFollowUp{}, agenttest.ResolveAnswer(Registration().Plugin, []byte(reasonixWritePermission), reasonixTestRequestID, got).FollowUp, "the Worker queues the answer's follow-up, not a second one")

	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(reasonixWritePermission), reasonixTestRequestID, agenttest.PermissionDecision("reasonix_write_project", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no feedback")
	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(reasonixWritePermission), reasonixTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an ordinary permission takes no plan decision")

	allowOnly := `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}}`
	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(allowOnly), reasonixTestRequestID, agenttest.PermissionDecision(acp.CancelOptionID, "not now"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"cancelled"}}}`, string(got.Content))
	assert.Equal(t, "not now", got.FollowUp)
}

func TestAnswerControlRequest_ReasonixAnswersAnElicitationThroughTheBase(t *testing.T) {
	t.Parallel()
	payload := `{"jsonrpc":"2.0","id":3,"method":"elicitation/create","params":{"mode":"form","message":"Choose the settings.","requestedSchema":{"type":"object"}}}`
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), reasonixTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"count":0,"enabled":false,"color":"b"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:3","response":{
		"action":"accept","content":{"count":0,"enabled":false,"color":"b"}}}}`, string(got.Content))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":{"action":"accept","content":{"count":0,"enabled":false,"color":"b"}}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(payload), reasonixTestRequestID, got).Content))
}
