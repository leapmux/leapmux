package cline

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// clinePromptApproval is a stored tool approval, as the Worker publishes it,
// with the arguments as the raw `inputJson` value.
func clinePromptApproval(t *testing.T, toolName, inputJSON any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"version": "v1", "event": contracts.ClineEventApprovalRequested, "sessionId": "s1",
		"payload": map[string]any{
			"approvalId": "approval_1", "sessionId": "s1", "agentId": "agent_1", "toolCallId": "call_1",
			"toolName": toolName, "inputJson": inputJSON,
		},
	})
	require.NoError(t, err)
	return payload
}

// clinePromptApprovalOf is a stored tool approval whose arguments are input.
func clinePromptApprovalOf(t *testing.T, toolName string, input map[string]any) []byte {
	t.Helper()
	inputJSON, err := json.Marshal(input)
	require.NoError(t, err)
	return clinePromptApproval(t, toolName, string(inputJSON))
}

// clinePromptQuestion is a stored question request whose executor states
// args.
func clinePromptQuestion(t *testing.T, args any, capabilityName string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"version": "v1", "event": contracts.ClineEventCapabilityRequested, "sessionId": "s1",
		"payload": map[string]any{
			"requestId": "capreq_1", "targetClientId": "leapmux-a", "capabilityName": capabilityName,
			"payload": map[string]any{"executor": "askQuestion", "args": args, "context": map[string]any{"toolCallId": "call_q"}},
		},
	})
	require.NoError(t, err)
	return payload
}

func clineLabelOption(label string) *leapmuxv1.QuestionOption {
	return &leapmuxv1.QuestionOption{Value: &label, Label: label}
}

// clineColorQuestion is the question of clinePromptQuestion with the colors.
var clineColorQuestion = []any{"Which color do you prefer?", []any{"Red", "Blue"}}

func TestClineReadControlRequest_AnApprovalIsAPermissionWithItsArguments(t *testing.T) {
	got := agenttest.ControlPromptOf(clineProvider{}, clinePromptApprovalOf(t, "editor", map[string]any{"path": "/work/a.ts", "old_text": "a", "new_text": "b"}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:     "editor",
		InputJson: `{"new_text":"b","old_text":"a","path":"/work/a.ts"}`,
		Options:   agent.AllowDenyOptions(),
	}}}, got)
}

// The arguments are the JSON text that Cline states, which the decision row
// draws as it stands.
func TestClineReadControlRequest_TheArgumentsKeepTheOrderThatClineStates(t *testing.T) {
	got := agenttest.ControlPromptOf(clineProvider{}, clinePromptApproval(t, "editor", ` {"path": "/a.ts", "old_text": "a"} `)).GetPermission()
	assert.Equal(t, `{"path":"/a.ts","old_text":"a"}`, got.GetInputJson())
}

func TestClineReadControlRequest_ReadsArgumentsThatAreNoObjectAsNone(t *testing.T) {
	for _, inputJSON := range []any{`[1,2]`, `"text"`, `null`, `3`, nil, `not json`, ``, 7} {
		got := agenttest.ControlPromptOf(clineProvider{}, clinePromptApproval(t, "editor", inputJSON)).GetPermission()
		require.NotNil(t, got, "%v", inputJSON)
		assert.Equal(t, "{}", got.GetInputJson(), "%v", inputJSON)
	}
}

func TestClineReadControlRequest_TitlesAnApprovalThatStatesNoToolAsATool(t *testing.T) {
	for _, toolName := range []any{"", 7, nil} {
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
			Title: "Tool", InputJson: "{}", Options: agent.AllowDenyOptions(),
		}}}, agenttest.ControlPromptOf(clineProvider{}, clinePromptApproval(t, toolName, `{}`)))
	}
}

func TestClineReadControlRequest_StatesTheCommandsOfACommandApproval(t *testing.T) {
	got := agenttest.ControlPromptOf(clineProvider{}, clinePromptApprovalOf(t, contracts.ClineToolRunCommands, map[string]any{"commands": []any{
		"git status",
		map[string]any{"command": "go", "args": []any{"test", "./..."}},
	}})).GetPermission()
	assert.Equal(t, "git status\ngo test ./...", got.GetCommand())
	assert.Equal(t, contracts.ClineToolRunCommands, got.GetTitle())

	got = agenttest.ControlPromptOf(clineProvider{}, clinePromptApprovalOf(t, contracts.ClineToolRunCommands, map[string]any{"command": "make test"})).GetPermission()
	assert.Equal(t, "make test", got.GetCommand(), "a call can state one command in `command`")

	got = agenttest.ControlPromptOf(clineProvider{}, clinePromptApprovalOf(t, contracts.ClineToolRunCommands, map[string]any{"commands": []any{
		"", 3, nil, []any{"ls"}, map[string]any{"command": "", "args": []any{"", 7, "only"}},
	}})).GetPermission()
	assert.Equal(t, "only", got.GetCommand(), "an element that states nothing, and an empty part, is no command")

	for _, tc := range []struct {
		name  string
		entry map[string]any
	}{
		{name: "args missing", entry: map[string]any{"command": "ls"}},
		{name: "args null", entry: map[string]any{"command": "ls", "args": nil}},
		{name: "args string", entry: map[string]any{"command": "ls", "args": "-la"}},
		{name: "args number", entry: map[string]any{"command": "ls", "args": 42}},
		{name: "args object", entry: map[string]any{"command": "ls", "args": map[string]any{"flag": true}}},
	} {
		got = agenttest.ControlPromptOf(clineProvider{}, clinePromptApprovalOf(t, contracts.ClineToolRunCommands, map[string]any{
			"commands": []any{tc.entry},
		})).GetPermission()
		assert.Equal(t, "ls", got.GetCommand(), tc.name)
	}
}

// An empty command line is no command, so the decision row draws the
// arguments alone.
func TestClineReadControlRequest_StatesNoCommandForACommandCallThatStatesNone(t *testing.T) {
	for _, inputJSON := range []string{`{}`, `{"commands":[]}`, `{"commands":["",{"args":[]}]}`, `{"commands":null,"command":7}`} {
		got := agenttest.ControlPromptOf(clineProvider{}, clinePromptApproval(t, contracts.ClineToolRunCommands, inputJSON)).GetPermission()
		require.NotNil(t, got, inputJSON)
		assert.Empty(t, got.GetCommand(), inputJSON)
	}
}

// Only a shell call has a command line. A `command` field of another tool is
// one of its arguments.
func TestClineReadControlRequest_ReadsTheCommandArgumentOfAnotherToolAsAnArgumentAlone(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title: "github__deploy", InputJson: `{"command":"ship"}`, Options: agent.AllowDenyOptions(),
	}}}, agenttest.ControlPromptOf(clineProvider{}, clinePromptApproval(t, "github__deploy", `{"command":"ship"}`)))
}

// A subagent, a configured agent and a teammate each run their own tools
// without asking, so the approval that starts one says so.
func TestClineReadControlRequest_WarnsThatTheAgentThatACallStartsRunsItsToolsWithoutAsking(t *testing.T) {
	for tool, input := range map[string]map[string]any{
		contracts.ClineToolSpawnAgent: {"task": "Look."},
		"subagent_reviewer_1a2b":      {"prompt": "Review the change."},
		"subagent_":                   {},
		"team_spawn_teammate":         {"agentId": "researcher", "rolePrompt": "You research."},
		"team_run_task":               {"agentId": "researcher", "task": "Report a word."},
	} {
		got := agenttest.ControlPromptOf(clineProvider{}, clinePromptApprovalOf(t, tool, input)).GetPermission()
		require.NotNil(t, got, tool)
		assert.Equal(t, contracts.ClinePromptReasonSpawnAgent, got.GetReason(), tool)
	}
	for _, tool := range []string{"editor", contracts.ClineToolRunCommands, "team_status", "github__subagent_search", "mcp__subagent_reviewer", ""} {
		got := agenttest.ControlPromptOf(clineProvider{}, clinePromptApprovalOf(t, tool, map[string]any{})).GetPermission()
		require.NotNil(t, got, tool)
		assert.Empty(t, got.GetReason(), tool)
	}
}

func TestClineReadControlRequest_ThePlanToolsApprovalIsThePlanWhateverItsArgumentsState(t *testing.T) {
	for _, inputJSON := range []any{`{}`, `not json`, nil} {
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}},
			agenttest.ControlPromptOf(clineProvider{}, clinePromptApproval(t, contracts.ClineToolSwitchToActMode, inputJSON)))
	}
}

// An envelope of another event, a row that is no envelope, and a capability
// request of another capability read as unrecognized. Cline's hub takes an
// answer only to an approval or to a question, and the resolution refuses any
// other answer, so a prompt with Allow and Deny would fail only after a click.
func TestClineReadControlRequest_ReadsNothingElse(t *testing.T) {
	for _, payload := range []string{
		`{"version":"v1","event":"tool.started","payload":{"toolName":"editor"}}`,
		`{"payload":{"toolName":"editor","inputJson":"{}"}}`,
		`{"event":7,"payload":{"toolName":"editor"}}`,
		`{"type":"control_request"}`,
		`{}`,
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(clineProvider{}, []byte(payload)), payload)
	}
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(clineProvider{}, clinePromptQuestion(t, clineColorQuestion, "tool_executor.other")))
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(clineProvider{}, clinePromptQuestion(t, clineColorQuestion, "custom_tool.switch_to_act_mode")))
}

// An approval envelope whose payload is no object still asks: it states no
// tool and no arguments.
func TestClineReadControlRequest_AnApprovalWithNoPayloadIsAnUntitledPermission(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title: "Tool", InputJson: "{}", Options: agent.AllowDenyOptions(),
	}}}, agenttest.ControlPromptOf(clineProvider{}, []byte(`{"event":"approval.requested","payload":"text"}`)))
}

func TestClineReadControlRequest_ReadsTheQuestionAndItsOptions(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Question: "Which color do you prefer?",
			Options:  []*leapmuxv1.QuestionOption{clineLabelOption("Red"), clineLabelOption("Blue")},
		}},
	}}}, agenttest.ControlPromptOf(clineProvider{}, clinePromptQuestion(t, clineColorQuestion, contracts.ClineCapabilityAskQuestion)))
}

// The approval of the question tool asks whether the tool may run. Only the
// capability request of the question executor is a question.
func TestClineReadControlRequest_RecognizesAQuestionAndNothingElse(t *testing.T) {
	assert.NotNil(t, agenttest.ControlPromptOf(clineProvider{}, clinePromptApprovalOf(t, contracts.ClineToolAskQuestion, map[string]any{})).GetPermission())
	assert.NotNil(t, agenttest.ControlPromptOf(clineProvider{}, []byte(`{"version":"v1","event":"approval.requested","payload":{"capabilityName":"tool_executor.askQuestion"}}`)).GetPermission())
	assert.Nil(t, agenttest.ControlPromptOf(clineProvider{}, clinePromptQuestion(t, clineColorQuestion, "custom_tool.switch_to_act_mode")).GetQuestion())
}

func TestClineReadControlRequest_TrimsTheQuestionAndKeepsOnlyTheOptionsThatAreText(t *testing.T) {
	got := agenttest.ControlPromptOf(clineProvider{}, clinePromptQuestion(t, []any{"  Which color?  ", []any{"Red", 3, nil, "Blue", ""}}, contracts.ClineCapabilityAskQuestion))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Question: "Which color?",
			Options:  []*leapmuxv1.QuestionOption{clineLabelOption("Red"), clineLabelOption("Blue"), clineLabelOption("")},
		}},
	}}}, got)

	// JavaScript's trim removes U+FEFF and keeps U+0085, unlike strings.TrimSpace.
	got = agenttest.ControlPromptOf(clineProvider{}, clinePromptQuestion(t, []any{"\uFEFF Why? \u0085", []any{}}, contracts.ClineCapabilityAskQuestion))
	require.Len(t, got.GetQuestion().GetQuestions(), 1)
	assert.Equal(t, "Why? \u0085", got.GetQuestion().GetQuestions()[0].GetQuestion())
}

func TestClineReadControlRequest_ReadsAQuestionWithNoOptionListAsAQuestionWithNoOption(t *testing.T) {
	for _, args := range []any{[]any{"Why?"}, []any{"Why?", "not a list"}} {
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: []*leapmuxv1.ControlQuestion{{Question: "Why?"}},
		}}}, agenttest.ControlPromptOf(clineProvider{}, clinePromptQuestion(t, args, contracts.ClineCapabilityAskQuestion)))
	}
}

// A question request whose executor states no question still waits for an
// answer, so it stays a question, with nothing to ask.
func TestClineReadControlRequest_ReadsNoQuestionFromArgumentsThatStateNone(t *testing.T) {
	for _, args := range []any{nil, map[string]any{}, "Which color?", []any{}, []any{3, []any{"Red"}}, []any{"  ", []any{"A"}}} {
		got := agenttest.ControlPromptOf(clineProvider{}, clinePromptQuestion(t, args, contracts.ClineCapabilityAskQuestion))
		require.NotNil(t, got.GetQuestion(), "%v", args)
		assert.Empty(t, got.GetQuestion().GetQuestions(), "%v", args)
	}
	got := agenttest.ControlPromptOf(clineProvider{}, []byte(`{"event":"capability.requested","payload":{"capabilityName":"tool_executor.askQuestion","payload":"text"}}`))
	require.NotNil(t, got.GetQuestion(), "an executor payload that is no object states no arguments")
	assert.Empty(t, got.GetQuestion().GetQuestions())
}

// The composer's text refuses the call, and rides the refusal as the reason
// that Cline hands the model. The answer to the Deny button carries the
// placeholder.
func TestClineAnswerControlRequest_APermissionAllowsOrDeniesWithTheFeedback(t *testing.T) {
	payload := clinePromptApprovalOf(t, "editor", map[string]any{"path": "/a.ts"})
	got, err := agenttest.AnswerControl(clineProvider{}, payload, "approval_1", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use the other file."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval_1","response":{"behavior":"deny","message":"Use the other file."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)

	got, err = agenttest.AnswerControl(clineProvider{}, payload, "approval_1", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval_1","response":{"behavior":"deny","message":"`+contracts.ControlAnswerMessageRejectedByUser+`"}}}`, string(got.Content))

	// Cline's payload states no `request.input`, so the allow sends an empty
	// updated input.
	got, err = agenttest.AnswerControl(clineProvider{}, payload, "approval_1", agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval_1","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
}

func TestClineAnswerControlRequest_RefusesADecisionThatDoesNotAnswerThePrompt(t *testing.T) {
	payload := clinePromptApprovalOf(t, "editor", map[string]any{})
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"feedback beside the allow": agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, "Why not."),
		"an option not offered":     agenttest.PermissionDecision("session", ""),
		"a plan decision":           agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}),
		"no kind":                   {},
	} {
		_, err := agenttest.AnswerControl(clineProvider{}, payload, "approval_1", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
	_, err := agenttest.AnswerControl(clineProvider{}, clinePromptApprovalOf(t, contracts.ClineToolSwitchToActMode, map[string]any{}), "approval_1", agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a plan takes no permission decision")
}

func TestClineAnswerControlRequest_APlanApprovalCarriesTheSettingsAndARefusalItsFeedback(t *testing.T) {
	payload := clinePromptApprovalOf(t, contracts.ClineToolSwitchToActMode, map[string]any{})
	got, err := agenttest.AnswerControl(clineProvider{}, payload, "approval_1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.ClinePermissionModeAutoApprove}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval_1","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.ClinePermissionModeAutoApprove}, got.PlanApproval))

	got, err = agenttest.AnswerControl(clineProvider{}, payload, "approval_1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split the second step."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval_1","response":{"behavior":"deny","message":"Split the second step."}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)

	_, err = agenttest.AnswerControl(clineProvider{}, payload, "approval_1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "revise"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Cline's plan offers no choices")
}

// clineQuestionAnswerText answers the color question and returns the answer
// text that the envelope carries.
func clineQuestionAnswerText(t *testing.T, args any, answer *leapmuxv1.QuestionAnswer) string {
	t.Helper()
	got, err := agenttest.AnswerControl(clineProvider{}, clinePromptQuestion(t, args, contracts.ClineCapabilityAskQuestion), "capreq_1", agenttest.QuestionDecision(answer))
	require.NoError(t, err)
	var envelope struct {
		Response struct {
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(got.Content, &envelope))
	assert.Equal(t, "capreq_1", envelope.Response.RequestID)
	assert.Equal(t, "allow", envelope.Response.Response["behavior"])
	text, ok := envelope.Response.Response[contracts.ClineQuestionAnswerAnswer].(string)
	require.True(t, ok, "the answer is text")
	return text
}

// The whole envelope, because the Worker reads the behavior and the request
// id too: an answer with another behavior or id would not reach the question.
func TestClineAnswerControlRequest_AnswersWithThePickedOption(t *testing.T) {
	got, err := agenttest.AnswerControl(clineProvider{}, clinePromptQuestion(t, clineColorQuestion, contracts.ClineCapabilityAskQuestion), "capreq_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Blue"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"capreq_1","response":{"behavior":"allow","answer":"Blue"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)

	assert.Equal(t, "Blue", clineQuestionAnswerText(t, clineColorQuestion, &leapmuxv1.QuestionAnswer{Selected: []string{"Blue"}, CustomText: "   "}),
		"blank typed words give way to the pick")
}

func TestClineAnswerControlRequest_AnswersWithTheReadersOwnWordsWhenTheyTypedAny(t *testing.T) {
	assert.Equal(t, "Green", clineQuestionAnswerText(t, clineColorQuestion, &leapmuxv1.QuestionAnswer{CustomText: "  Green  "}))
	assert.Equal(t, "Teal", clineQuestionAnswerText(t, []any{"Which color?", []any{}}, &leapmuxv1.QuestionAnswer{CustomText: " Teal "}),
		"a question that offers no option takes the typed words")
}

// The builder answers the prompt that it is given. Cline's reader never states
// a multiple-choice question, so this prompt is built by hand: it pins that a
// multiple-choice answer keeps each pick and the typed words.
func TestClineAnswerControlRequest_AMultipleChoiceAnswerSendsEveryPickAndTheTypedWords(t *testing.T) {
	red, blue := "Red", "Blue"
	got, err := clineProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request:   agent.NewControlRequestInput(clinePromptQuestion(t, clineColorQuestion, contracts.ClineCapabilityAskQuestion)),
		RequestID: "capreq_1",
		Prompt: &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: []*leapmuxv1.ControlQuestion{{
				Question: "Which colors?", MultiSelect: true,
				Options: []*leapmuxv1.QuestionOption{{Value: &red, Label: red}, {Value: &blue, Label: blue}},
			}},
		}}},
		Decision: agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Red", "Blue"}, CustomText: " Teal "}),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"capreq_1","response":{"behavior":"allow","answer":"Red, Blue, Teal"}}}`, string(got.Content))
}

// The submit waits for an answer, so no reader sends this one; the answer
// still states the empty text, which ResolveControlResponse refuses.
// Cline's executor needs an answer with text, and the browser sends one
// only when the reader picked or typed something, so the Worker refuses an
// empty answer before it reaches Cline.
func TestClineAnswerControlRequest_RefusesAnAnswerOfNothing(t *testing.T) {
	question := clinePromptQuestion(t, clineColorQuestion, contracts.ClineCapabilityAskQuestion)
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"no answer":        agenttest.QuestionDecision(),
		"an empty answer":  agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{}),
		"blank text alone": agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: " \n "}),
	} {
		_, err := agenttest.AnswerControl(clineProvider{}, question, "capreq_1", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// A pick that the question does not offer is not the reader's answer, and a
// Cline question takes one pick at most.
func TestClineAnswerControlRequest_RefusesAPickThatTheQuestionDoesNotOffer(t *testing.T) {
	colors := clinePromptQuestion(t, clineColorQuestion, contracts.ClineCapabilityAskQuestion)
	noOption := clinePromptQuestion(t, []any{"Which color?", []any{}}, contracts.ClineCapabilityAskQuestion)
	for name, c := range map[string]struct {
		payload  []byte
		selected []string
	}{
		"a color that the question does not offer": {colors, []string{"Green"}},
		"several picks, one not offered":           {colors, []string{"Green", "Red", "Blue"}},
		"two offered picks":                        {colors, []string{"Red", "Blue"}},
		"a pick of a question with no option":      {noOption, []string{"Red"}},
	} {
		_, err := agenttest.AnswerControl(clineProvider{}, c.payload, "capreq_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: c.selected}))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
	// Cline's answer is one line, so a pick and typed words cannot both be it.
	_, err := agenttest.AnswerControl(clineProvider{}, colors, "capreq_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Blue"}, CustomText: "Green"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a pick and typed words")
	_, err = agenttest.AnswerControl(clineProvider{}, colors, "capreq_1", agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a question takes no permission decision")
}

func TestClineAnswerControlRequest_RefusesWithTheReadersWords(t *testing.T) {
	got, err := agenttest.AnswerControl(clineProvider{}, clinePromptQuestion(t, clineColorQuestion, contracts.ClineCapabilityAskQuestion), "capreq_1", agenttest.CancelQuestions("Not now."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"capreq_1","response":{"behavior":"deny","message":"Not now."}}}`, string(got.Content))
}

// The answer is the neutral envelope that ResolveControlResponse reads today,
// so it turns into Cline's own approval and capability replies.
func TestClineAnswerControlRequest_ResolvesIntoClinesOwnReply(t *testing.T) {
	approval := clinePromptApprovalOf(t, "editor", map[string]any{})
	deny, err := agenttest.AnswerControl(clineProvider{}, approval, "approval_1", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use the other file."))
	require.NoError(t, err)
	resolution := agenttest.ResolveAnswer(clineProvider{}, approval, "approval_1", deny)
	require.False(t, resolution.Withhold)
	assert.JSONEq(t, `{"approvalId":"approval_1","approved":false,"reason":"Use the other file."}`, string(resolution.Content))

	plan := clinePromptApprovalOf(t, contracts.ClineToolSwitchToActMode, map[string]any{})
	approve, err := agenttest.AnswerControl(clineProvider{}, plan, "approval_1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.ClinePermissionModeAutoApprove}))
	require.NoError(t, err)
	resolution = agenttest.ResolveAnswer(clineProvider{}, plan, "approval_1", approve)
	require.False(t, resolution.Withhold)
	assert.JSONEq(t, `{"approvalId":"approval_1","approved":true,"permissionMode":"auto_approve"}`, string(resolution.Content))

	question := clinePromptQuestion(t, clineColorQuestion, contracts.ClineCapabilityAskQuestion)
	answer, err := agenttest.AnswerControl(clineProvider{}, question, "capreq_1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Blue"}}))
	require.NoError(t, err)
	resolution = agenttest.ResolveAnswer(clineProvider{}, question, "capreq_1", answer)
	require.False(t, resolution.Withhold)
	assert.JSONEq(t, `{"requestId":"capreq_1","ok":true,"payload":{"result":"Blue"}}`, string(resolution.Content))

}
