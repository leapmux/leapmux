package zcode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// zcodeTestRequestID is the stored id of the request that each test answers.
const zcodeTestRequestID = "req-1"

// zcodeControlPayload builds a stored control request: the tool name that the
// Worker records, the header input, and ZCode's own params.
func zcodeControlPayload(t *testing.T, method, toolName string, input, params any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type": "control_request", "request_id": "req-1", "id": 7, "method": method,
		"request": map[string]any{"tool_name": toolName, "input": input},
		"params":  params,
	})
	require.NoError(t, err)
	return payload
}

// zcodeQuestionControl is a stored question with the given header input and
// params.
func zcodeQuestionControl(t *testing.T, input, params any) []byte {
	t.Helper()
	return zcodeControlPayload(t, contracts.ZCodeMethodRequestUserInput, contracts.ZCodeToolNameAskUserQuestion, input, params)
}

// storeZCodeControlRequest feeds one native request to an agent and returns
// the payload that the agent stored, so a test reads the shape that the
// Worker publishes. The request ID of params becomes "req-1", the
// zcodeTestRequestID that each answer states, because the resolution refuses
// a stored request whose id differs from the key that the answer addresses.
func storeZCodeControlRequest(t *testing.T, method, params, requestID string) []byte {
	t.Helper()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 7, method, zcodeWithRequestID(t, params, requestID)))
	stored := zcodeStoredPayload(t, sink)
	require.Equal(t, "req-1", providerkit.StringField(stored, "request_id"))
	return stored
}

// zcodeWithRequestID replaces the request ID of a params fixture with "req-1".
func zcodeWithRequestID(t *testing.T, params, requestID string) string {
	t.Helper()
	old := `"requestId": "` + requestID + `"`
	require.Contains(t, params, old)
	return strings.Replace(params, old, `"requestId": "req-1"`, 1)
}

func zcodeQuestionsOf(t *testing.T, payload []byte) []*leapmuxv1.ControlQuestion {
	t.Helper()
	prompt := agenttest.ControlPromptOf(zcodeProvider{}, payload)
	require.NotNil(t, prompt.GetQuestion(), "a question reads as a question")
	return prompt.GetQuestion().GetQuestions()
}

// zcodeSentResponse reads the inner response of an answer.
func zcodeSentResponse(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string         `json:"subtype"`
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(content, &envelope))
	require.Equal(t, "control_response", envelope.Type)
	require.Equal(t, "success", envelope.Response.Subtype)
	require.Equal(t, "req-1", envelope.Response.RequestID)
	return envelope.Response.Response
}

// zcodeReplyResult resolves an answer through the provider's own resolution
// and returns the `result` of the reply that reaches the app-server.
func zcodeReplyResult(t *testing.T, stored, content []byte) (map[string]any, agent.ControlResponseResolution) {
	t.Helper()
	frame, res := zcodeResolve(t, stored, content)
	require.False(t, res.Withhold, "the resolution withheld the reply")
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	return result, res
}

var (
	zcodeAllowOnce   = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	zcodeAllowAlways = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS
	zcodeRejectOnce  = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
	zcodeOther       = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER
)

// zcodeCapturedOptions are the options that a live Bash permission request
// offered: an always-allow choice carries its permission-rule updates inside
// its own response.
var zcodeCapturedOptions = []any{
	map[string]any{"optionId": "allow_once", "kind": "allow_once", "name": "Allow once", "response": map[string]any{"decision": "allow", "reason": "Approved once"}},
	map[string]any{"optionId": "allow_project", "kind": "allow_always", "name": "Always allow in this project", "response": map[string]any{"decision": "allow", "permissionUpdates": []any{map[string]any{"type": "addRules"}}}},
	map[string]any{"optionId": "deny", "kind": "deny", "name": "Deny", "response": map[string]any{"decision": "deny"}},
}

func zcodeBashPermission(t *testing.T, params map[string]any) []byte {
	t.Helper()
	return zcodeControlPayload(t, contracts.ZCodeMethodRequestPermission, contracts.ZCodeToolNameBash, map[string]any{"command": "rm -rf build"}, params)
}

// --- questions ---

func TestZCodeReadControlRequest_RecognizesAQuestionByItsRecordedToolName(t *testing.T) {
	t.Parallel()
	assert.NotNil(t, agenttest.ControlPromptOf(zcodeProvider{}, []byte(`{"method":"interaction/requestUserInput","request":{"tool_name":"AskUserQuestion"}}`)).GetQuestion())
	assert.NotNil(t, agenttest.ControlPromptOf(zcodeProvider{}, []byte(`{"method":"interaction/requestUserInput","request":{"tool_name":"ExitPlanMode"}}`)).GetPlan(), "the plan approval travels over the same method")
	assert.NotNil(t, agenttest.ControlPromptOf(zcodeProvider{}, []byte(`{"method":"interaction/requestPermission","request":{"tool_name":"Bash"}}`)).GetPermission())
	assert.NotNil(t, agenttest.ControlPromptOf(zcodeProvider{}, []byte(`{"method":"interaction/requestPermission"}`)).GetPermission())
}

// The app-server raises a permission and a user input, and LeapMux asks the
// workspace-trust question. The resolution builds no reply to a request of
// another method, so a prompt with Allow and Deny would fail only after a
// click, and such a request reads as unrecognized.
func TestZCodeReadControlRequest_ReadsARequestOfAnotherMethodAsUnrecognized(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"type":"control_request","request_id":"req-1","id":7,"method":"unknown/method","request":{"tool_name":"AskUserQuestion"}}`,
		`{"type":"control_request","request_id":"req-1","id":7,"request":{"tool_name":"Bash"}}`,
		`{"method":7,"request":{"tool_name":"ExitPlanMode"}}`,
		`{}`,
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(zcodeProvider{}, []byte(payload)), payload)
	}
}

func TestZCodeReadControlRequest_ReadsAQuestionAndItsOptions(t *testing.T) {
	t.Parallel()
	got := zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{"questions": []any{
		map[string]any{"question": "Which database?", "options": []any{map[string]any{"value": "Postgres"}, map[string]any{"value": "MySQL", "description": "the other one"}}},
	}}, nil))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
		Question: "Which database?",
		Options:  []*leapmuxv1.QuestionOption{{Label: "Postgres"}, {Label: "MySQL", Description: "the other one"}},
	}}}}}, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: got}}})
}

// The native request keeps the option descriptions that the Worker's compact
// header leaves out, so its list wins.
func TestZCodeReadControlRequest_RecoversOptionDescriptionsFromTheNativeRequest(t *testing.T) {
	t.Parallel()
	native := map[string]any{
		"question": "Pick a color.", "header": "Color", "multiSelect": false,
		"options": []any{map[string]any{"label": "Blue", "value": "Blue", "description": "Choose the color blue."}},
	}
	got := zcodeQuestionsOf(t, zcodeQuestionControl(t,
		map[string]any{"questions": []any{map[string]any{"question": "Pick a color.", "header": "Color", "options": []any{map[string]any{"label": "Blue", "value": "Blue"}}}}},
		map[string]any{"questions": []any{native}, "schema": map[string]any{"toolName": "AskUserQuestion"}}))
	require.Len(t, got, 1)
	assert.Equal(t, "Color", got[0].GetHeader())
	agenttest.RequireProto(t,
		&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{Options: []*leapmuxv1.QuestionOption{{Label: "Blue", Description: "Choose the color blue."}}}}}}},
		&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{Options: got[0].GetOptions()}}}}})
}

func TestZCodeReadControlRequest_ReadsNativeQuestionsFromTheSchemaAndTheInput(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"schema", "input"} {
		got := zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{}, map[string]any{field: map[string]any{"questions": []any{
			map[string]any{"question": "Native question", "options": []any{map[string]any{"label": "A", "description": "Native description"}}},
		}}}))
		require.Len(t, got, 1, field)
		assert.Equal(t, "Native question", got[0].GetQuestion(), field)
		assert.Equal(t, "Native description", got[0].GetOptions()[0].GetDescription(), field)
	}
}

// The app-server that sent an empty list declares no question, so the reader
// does not fall back to the header's stale list.
func TestZCodeReadControlRequest_KeepsAnExplicitlyEmptyNativeListEmpty(t *testing.T) {
	t.Parallel()
	got := zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{"questions": []any{map[string]any{"question": "Stale question"}}}, map[string]any{"questions": []any{}}))
	assert.Empty(t, got)
}

func TestZCodeReadControlRequest_UsesTheFirstNativeListThatHasQuestions(t *testing.T) {
	t.Parallel()
	got := zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{}, map[string]any{
		"schema":    map[string]any{"questions": []any{}},
		"questions": []any{map[string]any{"question": "Native question"}},
		"input":     map[string]any{"questions": []any{map[string]any{"question": "Later question"}}},
	}))
	require.Len(t, got, 1)
	assert.Equal(t, "Native question", got[0].GetQuestion())
	assert.Empty(t, got[0].GetOptions())

	got = zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{"questions": []any{map[string]any{"question": "Header question"}}}, map[string]any{
		"schema": map[string]any{"questions": "not a list"},
	}))
	require.Len(t, got, 1, "a native value that is no list leaves the header's list")
	assert.Equal(t, "Header question", got[0].GetQuestion())
}

// ZCode's wire form sets an option's `value` to its own label, so either
// field stands in for the other.
func TestZCodeReadControlRequest_RepairsTheLabelOfAnOption(t *testing.T) {
	t.Parallel()
	got := zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{"questions": []any{map[string]any{"question": "Q", "options": []any{
		map[string]any{"label": "from label"},
		map[string]any{"value": "from value"},
		map[string]any{"label": "shown", "value": "ignored"},
		map[string]any{"description": "orphan"},
		map[string]any{},
		"not an object",
		map[string]any{"value": "A", "description": ""},
	}}}}, nil))
	agenttest.RequireProto(t,
		&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
			Question: "Q",
			Options:  []*leapmuxv1.QuestionOption{{Label: "from label"}, {Label: "from value"}, {Label: "shown"}, {Label: "A"}},
		}}}}},
		&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: got}}})
}

func TestZCodeReadControlRequest_ReadsAPreviewThatShowsSomething(t *testing.T) {
	t.Parallel()
	box := "┌────────┐\n│ sample │\n└────────┘"
	got := zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{}, map[string]any{"questions": []any{map[string]any{
		"question": "Choose a layout",
		"options": []any{
			map[string]any{"label": "Box", "value": "Box", "preview": box},
			map[string]any{"label": "Blank", "preview": "   "},
			map[string]any{"label": "Number", "preview": 12},
		},
	}}}))
	require.Len(t, got, 1)
	require.Len(t, got[0].GetOptions(), 3)
	assert.Equal(t, box, got[0].GetOptions()[0].GetPreview())
	assert.Empty(t, got[0].GetOptions()[1].GetPreview(), "a preview of only white space shows nothing")
	assert.Empty(t, got[0].GetOptions()[2].GetPreview(), "a preview that is no text shows nothing")
}

func TestZCodeReadControlRequest_CarriesTheHeaderAndALiteralMultipleChoice(t *testing.T) {
	t.Parallel()
	got := zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{"questions": []any{
		map[string]any{"question": "Pick any", "header": "Databases", "multiSelect": true, "options": []any{map[string]any{"value": "A"}}},
		map[string]any{"question": "Q1", "multiSelect": false},
		map[string]any{"question": "Q2", "multiSelect": "true"},
	}}, nil))
	require.Len(t, got, 3)
	assert.Equal(t, "Databases", got[0].GetHeader())
	assert.True(t, got[0].GetMultiSelect())
	assert.False(t, got[1].GetMultiSelect())
	assert.False(t, got[2].GetMultiSelect(), "only a literal true reads as multiple choice")
	for _, question := range got {
		assert.False(t, question.GetAllowEmpty(), "a ZCode question needs an answer")
		assert.Nil(t, question.AllowCustomText, "a ZCode question takes typed text")
	}
}

// The answer is keyed by the question's text, so a header-only question is
// answerable through its header, and a question with neither is dropped.
func TestZCodeReadControlRequest_KeysAQuestionByItsTextOrItsHeader(t *testing.T) {
	t.Parallel()
	got := zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{"questions": []any{
		map[string]any{"header": "Databases", "options": []any{map[string]any{"value": "A"}}},
		map[string]any{"options": []any{map[string]any{"value": "A"}}},
		"not an object",
		nil,
		map[string]any{"question": "kept"},
		map[string]any{"question": "Second"},
	}}, nil))
	agenttest.RequireProto(t,
		&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{
			{Question: "Databases", Header: "Databases", Options: []*leapmuxv1.QuestionOption{{Label: "A"}}},
			{Question: "kept"},
			{Question: "Second"},
		}}}},
		&leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: got}}})
}

func TestZCodeReadControlRequest_ReadsNoQuestionFromARequestThatDeclaresNone(t *testing.T) {
	t.Parallel()
	assert.Empty(t, zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{}, nil)))
	assert.Empty(t, zcodeQuestionsOf(t, zcodeQuestionControl(t, map[string]any{"questions": "not an array"}, nil)))
	assert.Empty(t, zcodeQuestionsOf(t, zcodeQuestionControl(t, "not an object", "not an object")))
}

func TestZCodeAnswerControlRequest_ShipsThePickedOptionInTheSharedAllowEnvelope(t *testing.T) {
	t.Parallel()
	input := map[string]any{"questions": []any{map[string]any{"question": "Which database?", "options": []any{map[string]any{"value": "Postgres"}, map[string]any{"value": "MySQL"}}}}}
	got, err := agenttest.AnswerControl(zcodeProvider{}, zcodeQuestionControl(t, input, nil), zcodeTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"MySQL"}}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"behavior": "allow",
		"updatedInput": map[string]any{
			"questions": input["questions"],
			"answers":   map[string]any{"Which database?": "MySQL"},
		},
	}, normalizeJSON(t, zcodeSentResponse(t, got.Content)))
	assert.Empty(t, got.FollowUp)
}

// normalizeJSON round-trips a value through JSON, so a test compares the
// decoded forms of both sides.
func normalizeJSON(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	return decoded
}

// The answer must be the bytes that the provider's resolution reads: the
// app-server takes each answer keyed by its question text, and by position.
func TestZCodeAnswerControlRequest_AQuestionAnswerResolvesToTheAppServerReply(t *testing.T) {
	t.Parallel()
	stored := storeZCodeControlRequest(t, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams, "req-q")
	questions := zcodeQuestionsOf(t, stored)
	require.Len(t, questions, 2)
	got, err := agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"Postgres"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"cache", "Metrics"}, CustomText: " Tracing "},
	))
	require.NoError(t, err)
	result, _ := zcodeReplyResult(t, stored, got.Content)
	assert.Equal(t, map[string]any{
		"action": "accept",
		"content": map[string]any{
			"answers":  map[string]any{"Which database?": "Postgres", "Which extras?": "cache, Metrics, Tracing"},
			"answer_0": "Postgres",
			"answer_1": "cache, Metrics, Tracing",
		},
	}, result)
}

// A multiple-choice question takes options AND typed text: the user chose
// both options and the text, so the answer lists all three.
func TestZCodeAnswerControlRequest_AMultipleChoiceAnswerSendsEachOptionAndTheTypedText(t *testing.T) {
	t.Parallel()
	payload := zcodeQuestionControl(t, map[string]any{"questions": []any{
		map[string]any{"question": "Which ones?", "multiSelect": true, "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}, map[string]any{"label": "C"}}},
	}}, nil)
	got, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A", "B"}, CustomText: " X "}))
	require.NoError(t, err)
	updated := zcodeSentResponse(t, got.Content)["updatedInput"].(map[string]any)
	assert.Equal(t, map[string]any{"Which ones?": "A, B, X"}, updated["answers"])

	got, err = agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A", "B"}, CustomText: "   "}))
	require.NoError(t, err)
	updated = zcodeSentResponse(t, got.Content)["updatedInput"].(map[string]any)
	assert.Equal(t, map[string]any{"Which ones?": "A, B"}, updated["answers"], "text of only white space is no text")
}

func TestZCodeAnswerControlRequest_SendsATypedAnswerTrimmed(t *testing.T) {
	t.Parallel()
	payload := zcodeQuestionControl(t, map[string]any{"questions": []any{map[string]any{"question": "Name?"}}}, nil)
	got, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "  Ada  "}))
	require.NoError(t, err)
	updated := zcodeSentResponse(t, got.Content)["updatedInput"].(map[string]any)
	assert.Equal(t, map[string]any{"Name?": "Ada"}, updated["answers"])
}

// The app-server reads a question with no answer as a silent refusal, so the
// browser's submit waits for every answer, and so does the Worker.
func TestZCodeAnswerControlRequest_RefusesAQuestionAnswerThatTheBrowserCouldNotSubmit(t *testing.T) {
	t.Parallel()
	payload := zcodeQuestionControl(t, map[string]any{"questions": []any{
		map[string]any{"question": "First", "options": []any{map[string]any{"label": "A"}}},
		map[string]any{"question": "Second", "options": []any{map[string]any{"label": "B"}}},
	}}, nil)
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"a question with no answer":      agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}}),
		"text of only white space":       agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}}, &leapmuxv1.QuestionAnswer{CustomText: "  "}),
		"an option that is not offered":  agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Z"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"B"}}),
		"two options on a single choice": agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A", "A"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"B"}}),
		"an option beside typed text":    agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: "note"}, &leapmuxv1.QuestionAnswer{Selected: []string{"B"}}),
		"a permission decision":          agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
	} {
		_, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

func TestZCodeAnswerControlRequest_ACancelledQuestionDeniesWithItsFeedback(t *testing.T) {
	t.Parallel()
	stored := storeZCodeControlRequest(t, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams, "req-q")
	got, err := agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "User stopped"}, zcodeSentResponse(t, got.Content))
	assert.Empty(t, got.FollowUp, "the resolution, not the answer, queues the text")
	result, res := zcodeReplyResult(t, stored, got.Content)
	assert.Equal(t, map[string]any{"action": "decline"}, result)
	assert.Equal(t, agent.FeedbackFollowUp("User stopped"), res.FollowUp)
}

// --- MCP forms ---

func TestZCodeReadControlRequest_ReadsTheServersOwnFormNotTheFlattenedQuestions(t *testing.T) {
	t.Parallel()
	stored := storeZCodeMCPForm(t, zcodeWithRequestID(t, zcodeMCPFormParams, "mcp-elicit-1"))
	got := agenttest.ControlPromptOf(zcodeProvider{}, stored).GetElicitation()
	require.NotNil(t, got, "the stored MCP form draws as a form, never as questions")
	assert.Equal(t, "form", got.GetMode())
	assert.Equal(t, "Choose the probe settings.", got.GetMessage())
	assert.Equal(t, "form_probe", got.GetServer())
	assert.JSONEq(t, zcodeMCPFormSchema, got.GetSchemaJson())
	assert.Empty(t, got.GetTitle())
	assert.Empty(t, got.GetUrl())
}

func TestZCodeReadControlRequest_ReadsTheFormOfTheInputWhenTheSchemaStatesOnlyTheInteraction(t *testing.T) {
	t.Parallel()
	schema := map[string]any{"type": "object"}
	got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeControlPayload(t, contracts.ZCodeMethodRequestUserInput, "McpElicitation", map[string]any{}, map[string]any{
		"prompt": "The prompt of the request.",
		"input":  map[string]any{"serverName": "input_server", "requestedSchema": schema},
		"schema": map[string]any{"interaction": contracts.ZCodeInteractionMcpElicitation, "requestedSchema": nil},
	}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Message: "The prompt of the request.", Server: "input_server", SchemaJson: `{"type":"object"}`,
	}}}, got)
}

func TestZCodeReadControlRequest_ReadsNoFormFromAnotherRequest(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string][]byte{
		"a question interaction":   zcodeControlPayload(t, contracts.ZCodeMethodRequestUserInput, "McpElicitation", nil, map[string]any{"schema": map[string]any{"interaction": "ask_user_question"}}),
		"a plan approval":          zcodeControlPayload(t, contracts.ZCodeMethodRequestUserInput, "McpElicitation", nil, map[string]any{"schema": map[string]any{"interaction": contracts.ZCodeInteractionPlanApproval}}),
		"a request with no schema": zcodeControlPayload(t, contracts.ZCodeMethodRequestUserInput, "McpElicitation", nil, map[string]any{"requestId": "mcp-elicit-1"}),
		"another method":           zcodeControlPayload(t, contracts.ZCodeMethodRequestPermission, "McpElicitation", nil, map[string]any{"schema": map[string]any{"interaction": contracts.ZCodeInteractionMcpElicitation}}),
		"a request with no params": []byte(`{"method":"interaction/requestUserInput"}`),
	} {
		assert.Nil(t, agenttest.ControlPromptOf(zcodeProvider{}, payload).GetElicitation(), name)
	}
}

// The question reader runs first, so a question stays on the question surface
// whatever its params state.
func TestZCodeReadControlRequest_KeepsAQuestionOnTheQuestionSurface(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeQuestionControl(t, map[string]any{"questions": []any{map[string]any{"question": "Which database?"}}}, map[string]any{
		"schema": map[string]any{"interaction": contracts.ZCodeInteractionMcpElicitation, "questions": []any{map[string]any{"question": "Which database?", "options": []any{map[string]any{"label": "Postgres"}}}}},
	}))
	assert.NotNil(t, got.GetQuestion())
}

func TestZCodeAnswerControlRequest_AnswersAnMCPFormWithItsActionAndContent(t *testing.T) {
	t.Parallel()
	stored := storeZCodeMCPForm(t, zcodeWithRequestID(t, zcodeMCPFormParams, "mcp-elicit-1"))
	got, err := agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"count":2,"enabled":true,"color":"r"}`))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"action": "accept", "content": map[string]any{"count": float64(2), "enabled": true, "color": "r"}}, zcodeSentResponse(t, got.Content))
	result, _ := zcodeReplyResult(t, stored, got.Content)
	assert.Equal(t, map[string]any{"action": "accept", "content": map[string]any{"answer_0": "2", "answer_1": "true", "answer_2": "r"}}, result,
		"the app-server reads the form's typed answers by position")

	got, err = agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL, ""))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"action": "cancel"}, zcodeSentResponse(t, got.Content))

	_, err = agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.ElicitationChoiceDecision("", "always"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "ZCode's form offers no accept choices")
}

// --- plan approval ---

// zcodePlanPromptOf is the prompt of a plan approval that carries text.
func zcodePlanPromptOf(text string) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: text}}}
}

// The approval states the plan that the user approves, so the browser and
// `leapmux control agent requests` show it.
func TestZCodeReadControlRequest_ReadsThePlanThatTheApprovalCarries(t *testing.T) {
	t.Parallel()
	stored := storeZCodeControlRequest(t, contracts.ZCodeMethodRequestUserInput, zcodePlanParams, "req-plan")
	agenttest.RequireProto(t, zcodePlanPromptOf("1. read\n2. write"), agenttest.ControlPromptOf(zcodeProvider{}, stored))
}

// The plan comes from ZCode's own request, by the rule that stores the
// canonical plan: the tool input's plan, then the context's. The prompt is no
// source: ZCode states the permission reason there ("Tool ExitPlanMode
// requires user interaction"). The Worker's header is never the source, so its
// question is no plan.
func TestZCodeReadControlRequest_ReadsThePlanFromZCodesOwnRequest(t *testing.T) {
	t.Parallel()
	header := map[string]any{"questions": []any{map[string]any{"question": "# Header question"}}}
	for name, test := range map[string]struct {
		params any
		want   string
	}{
		"the tool input's plan wins": {
			params: map[string]any{"input": map[string]any{"plan": "# Input plan"}, "context": map[string]any{"plan": "# Context plan"}, "prompt": "Tool ExitPlanMode requires user interaction"},
			want:   "# Input plan",
		},
		"the context's plan follows": {
			params: map[string]any{"input": map[string]any{"allowedPrompts": []any{}}, "context": map[string]any{"plan": "# Context plan"}, "prompt": "Review this implementation plan."},
			want:   "# Context plan",
		},
		"a blank tool input plan states none": {
			params: map[string]any{"input": map[string]any{"plan": " \n\t"}, "context": map[string]any{"plan": "# Context plan"}},
			want:   "# Context plan",
		},
		"the prompt is no plan": {
			params: map[string]any{"input": map[string]any{"plan": 7}, "context": "not an object", "prompt": "Tool ExitPlanMode requires user interaction"},
			want:   "",
		},
		"the plan keeps its own white space": {
			params: map[string]any{"input": map[string]any{"plan": "\n# Plan\n\n- Step one.\n"}},
			want:   "\n# Plan\n\n- Step one.\n",
		},
		"a plan beside other tool input": {
			params: map[string]any{"input": map[string]any{"plan": "# Plan", "allowedPrompts": "not a list"}},
			want:   "# Plan",
		},
		"a request that states no plan": {params: map[string]any{"requestId": "req-plan"}, want: ""},
		"a request with no params":      {params: nil, want: ""},
		"params that are no object":     {params: "not an object", want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeControlPayload(t, contracts.ZCodeMethodRequestUserInput, contracts.ZCodeToolNameExitPlanMode, header, test.params))
			agenttest.RequireProto(t, zcodePlanPromptOf(test.want), got)
		})
	}
}

func TestZCodeAnswerControlRequest_AnswersThePlanApprovalThroughTheSharedPlanRow(t *testing.T) {
	t.Parallel()
	stored := storeZCodeControlRequest(t, contracts.ZCodeMethodRequestUserInput, zcodePlanParams, "req-plan")
	require.NotNil(t, agenttest.ControlPromptOf(zcodeProvider{}, stored).GetPlan())

	got, err := agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: "yolo"}))
	require.NoError(t, err)
	response := zcodeSentResponse(t, got.Content)
	assert.Equal(t, "allow", response["behavior"])
	assert.JSONEq(t, string(agent.ControlToolInput(stored)), string(normalizeJSONBytes(t, response["updatedInput"])), "the approval sends the request's own input back")
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: "yolo"}, got.PlanApproval))
	result, res := zcodeReplyResult(t, stored, got.Content)
	assert.Equal(t, map[string]any{"action": "accept", "content": map[string]any{"answer": contracts.ZCodePlanControlApprove}}, result)
	assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)

	// The composer's typed refusal: the reply declines, and the resolution
	// queues the text as the user's next message.
	got, err = agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 1."}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Split step 1."}, zcodeSentResponse(t, got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)
	result, res = zcodeReplyResult(t, stored, got.Content)
	assert.Equal(t, map[string]any{"action": "decline"}, result)
	assert.Equal(t, agent.FeedbackFollowUp("Split step 1."), res.FollowUp)

	_, err = agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "fast"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "ZCode's plan offers no choices")
}

func normalizeJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

// --- permissions ---

func TestZCodeReadControlRequest_ReadsAPermissionWithItsReasonInputAndCommand(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeBashPermission(t, map[string]any{"reason": "deletes files"}))
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.JSONEq(t, `{"command":"rm -rf build"}`, permission.GetInputJson())
	permission.InputJson = ""
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title: "Bash", Reason: "deletes files", Command: "rm -rf build", Options: agent.AllowDenyOptions(),
	}}}, got)

	got = agenttest.ControlPromptOf(zcodeProvider{}, zcodeBashPermission(t, map[string]any{"reason": 7}))
	assert.Empty(t, got.GetPermission().GetReason(), "a reason that is no text states none")
	got = agenttest.ControlPromptOf(zcodeProvider{}, zcodeControlPayload(t, contracts.ZCodeMethodRequestPermission, contracts.ZCodeToolNameBash, map[string]any{"command": 7}, nil))
	assert.Empty(t, got.GetPermission().GetCommand(), "a command that is no text states none")
}

// A tool that ZCode adds later still gets an answerable prompt.
func TestZCodeReadControlRequest_ReadsAnUnknownToolAsAPermission(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeControlPayload(t, contracts.ZCodeMethodRequestPermission, "SomeToolAddedLater", map[string]any{"arg": 1, "command": "ls"}, map[string]any{"reason": "unfamiliar"}))
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "SomeToolAddedLater", permission.GetTitle())
	assert.Equal(t, "unfamiliar", permission.GetReason())
	assert.Empty(t, permission.GetCommand(), "only a shell call states a command")
	assert.JSONEq(t, `{"arg":1,"command":"ls"}`, permission.GetInputJson())

	got = agenttest.ControlPromptOf(zcodeProvider{}, []byte(`{"method":"interaction/requestPermission"}`))
	assert.Equal(t, "{}", got.GetPermission().GetInputJson(), "a request with no input states an empty object, which the allow sends back")
	assert.Empty(t, got.GetPermission().GetTitle())
}

func TestZCodeReadControlRequest_ExposesTheOfferedOptionsWithTheirNativeIDsAndNames(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeBashPermission(t, map[string]any{"options": zcodeCapturedOptions}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: []*leapmuxv1.PermissionOption{
		{OptionId: "allow_once", Kind: zcodeAllowOnce, Name: "Allow once"},
		{OptionId: "allow_project", Kind: zcodeAllowAlways, Name: "Always allow in this project"},
		{OptionId: "deny", Kind: zcodeRejectOnce, Name: "Deny"},
	}}}}, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: got.GetPermission().GetOptions()}}})
}

func TestZCodeReadControlRequest_MapsEachNativeOptionKind(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]leapmuxv1.PermissionOptionKind{
		"allow_once":    zcodeAllowOnce,
		"allow_always":  zcodeAllowAlways,
		"allow_project": zcodeAllowAlways,
		"allow_session": zcodeAllowAlways,
		"deny":          zcodeRejectOnce,
		"deny_once":     zcodeRejectOnce,
		"reject_once":   zcodeRejectOnce,
		"reject_always": leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS,
		"escalate":      zcodeOther,
		"":              zcodeOther,
	} {
		got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeBashPermission(t, map[string]any{"options": []any{map[string]any{"optionId": "x", "kind": kind, "name": "X"}}}))
		assert.Equal(t, want, got.GetPermission().GetOptions()[0].GetKind(), kind)
	}
}

// An unknown native kind stays apart instead of taking a guessed slot, and
// its label is its name, else its kind, else its ID.
func TestZCodeReadControlRequest_KeepsAnUnknownKindWithALabel(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeBashPermission(t, map[string]any{"options": []any{
		map[string]any{"optionId": "escalate", "kind": "escalate", "name": "Escalate"},
		map[string]any{"optionId": "modify", "kind": "modify"},
		map[string]any{"optionId": "bare"},
		map[string]any{"optionId": "", "kind": "allow_once", "name": "No id"},
		map[string]any{"kind": "allow_once", "name": "Absent id"},
		"not an option",
		map[string]any{"optionId": "deny", "kind": "deny"},
	}}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: []*leapmuxv1.PermissionOption{
		{OptionId: "escalate", Kind: zcodeOther, Name: "Escalate"},
		{OptionId: "modify", Kind: zcodeOther, Name: "modify"},
		{OptionId: "bare", Kind: zcodeOther, Name: "bare"},
		{OptionId: "deny", Kind: zcodeRejectOnce},
	}}}}, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: got.GetPermission().GetOptions()}}})
}

// The composer's typed refusal denies a request whose options offer no
// refusal, so the prompt states that refusal as an option.
func TestZCodeReadControlRequest_AddsARefusalToOptionsThatOfferNone(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(zcodeProvider{}, zcodeBashPermission(t, map[string]any{"options": []any{
		map[string]any{"optionId": "allow", "name": "Allow"},
		map[string]any{"optionId": "allow_once", "kind": "allow_once", "name": "Allow once"},
	}}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: []*leapmuxv1.PermissionOption{
		{OptionId: "allow", Kind: zcodeOther, Name: "Allow"},
		{OptionId: "allow_once", Kind: zcodeAllowOnce, Name: "Allow once"},
		{OptionId: "leapmux:deny", Kind: zcodeRejectOnce, Name: "Deny"},
	}}}}, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: got.GetPermission().GetOptions()}}})
}

// A request that offers no options takes the shared Allow and Deny.
func TestZCodeAnswerControlRequest_AnswersARequestWithNoOptionsAsTheSharedAllowAndDeny(t *testing.T) {
	t.Parallel()
	for _, options := range []any{nil, []any{}, "not a list", []any{map[string]any{"name": "No id"}}} {
		payload := zcodeBashPermission(t, map[string]any{"options": options})
		got, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
		require.NoError(t, err, "%v", options)
		assert.Equal(t, map[string]any{"behavior": "allow", "updatedInput": map[string]any{"command": "rm -rf build"}}, zcodeSentResponse(t, got.Content), "%v", options)

		got, err = agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"behavior": "deny", "message": "Rejected by user."}, zcodeSentResponse(t, got.Content), "an empty send is still a deny")

		got, err = agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "do not"))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"behavior": "deny", "message": "do not"}, zcodeSentResponse(t, got.Content))

		_, err = agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, "Why?"))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "feedback goes with a refusal")
	}
}

func TestZCodeAnswerControlRequest_AnswersAnOfferedOptionWithItsIDAsTheChoice(t *testing.T) {
	t.Parallel()
	payload := zcodeBashPermission(t, map[string]any{"options": zcodeCapturedOptions})
	for option, behavior := range map[string]string{"allow_once": "allow", "allow_project": "allow", "deny": "deny"} {
		got, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.Equal(t, map[string]any{"behavior": behavior, "choice": option}, zcodeSentResponse(t, got.Content), option)
		assert.Empty(t, got.FollowUp, option)
	}
}

// An ID outside ZCode's allow-class set answers deny, mirroring the backend's
// fail-safe: no answer may grant a choice that the backend never offered.
func TestZCodeAnswerControlRequest_AnswersANonAllowOptionIDWithADeny(t *testing.T) {
	t.Parallel()
	payload := zcodeBashPermission(t, map[string]any{"options": []any{
		map[string]any{"optionId": "proceed", "kind": "allow_once", "name": "Proceed"},
		map[string]any{"optionId": "escalate", "kind": "escalate", "name": "Escalate"},
		map[string]any{"optionId": "allowSession", "kind": "allow_session", "name": "Allow for the session"},
	}})
	for option, behavior := range map[string]string{"proceed": "deny", "escalate": "deny", "allowSession": "allow"} {
		got, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.Equal(t, map[string]any{"behavior": behavior, "choice": option}, zcodeSentResponse(t, got.Content), option)
	}
	_, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision("escalate", "Why?"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "feedback goes with a refusing option alone")
	_, err = agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision("proceed", "Why?"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "feedback goes with a refusing option alone")
}

// The answer must be the bytes that the provider's resolution reads: the
// chosen option's own response, which carries an always-allow's rule
// updates, with a refusal's reason added.
func TestZCodeAnswerControlRequest_AnOptionAnswerResolvesToTheOptionsOwnResponse(t *testing.T) {
	t.Parallel()
	stored := storeZCodeControlRequest(t, contracts.ZCodeMethodRequestPermission, zcodePermissionParams, "req-1")
	require.Equal(t, []string{"allow_once", "allow_always", "deny"}, zcodeOptionIDs(agenttest.ControlPromptOf(zcodeProvider{}, stored)))

	got, err := agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.PermissionDecision("allow_always", ""))
	require.NoError(t, err)
	result, _ := zcodeReplyResult(t, stored, got.Content)
	assert.Equal(t, map[string]any{"decision": "allow", "permissionUpdates": []any{map[string]any{"rule": "Bash(rm:*)"}}}, result)

	got, err = agenttest.AnswerControl(zcodeProvider{}, stored, zcodeTestRequestID, agenttest.PermissionDecision("deny", "Use the trash instead."))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Use the trash instead.", "choice": "deny"}, zcodeSentResponse(t, got.Content),
		"the chosen refusal stays beside the reason")
	result, res := zcodeReplyResult(t, stored, got.Content)
	assert.Equal(t, map[string]any{"decision": "deny", "reason": "Use the trash instead."}, result)
	assert.Empty(t, res.FollowUp, "the reply carries the reason")
}

func TestZCodeAnswerControlRequest_TheAddedRefusalIsTheComposersDeny(t *testing.T) {
	t.Parallel()
	payload := zcodeBashPermission(t, map[string]any{"options": []any{
		map[string]any{"optionId": "allow_once", "kind": "allow_once", "name": "Allow once", "response": map[string]any{"decision": "allow"}},
	}})
	got, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision("leapmux:deny", ""))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Rejected by user."}, zcodeSentResponse(t, got.Content))

	got, err = agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, agenttest.PermissionDecision("leapmux:deny", "Not now."))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Not now."}, zcodeSentResponse(t, got.Content))
	result, _ := zcodeReplyResult(t, payload, got.Content)
	assert.Equal(t, map[string]any{"decision": "deny", "reason": "Not now."}, result, "with no refusing option, the reply is a bare deny")
}

func TestZCodeAnswerControlRequest_RefusesAPermissionDecisionThatThePromptDoesNotOffer(t *testing.T) {
	t.Parallel()
	payload := zcodeBashPermission(t, map[string]any{"options": zcodeCapturedOptions})
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an option that is not offered": agenttest.PermissionDecision("allow_always", ""),
		"LeapMux's own allow":           agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
		"the added refusal":             agenttest.PermissionDecision("leapmux:deny", ""),
		"feedback with an allow":        agenttest.PermissionDecision("allow_once", "Why?"),
		"a plan decision":               agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}),
		"a decision of no kind":         {},
	} {
		_, err := agenttest.AnswerControl(zcodeProvider{}, payload, zcodeTestRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

func zcodeOptionIDs(prompt *leapmuxv1.ControlPrompt) []string {
	var ids []string
	for _, option := range prompt.GetPermission().GetOptions() {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

// --- workspace trust ---

func zcodeTrustQuestion(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		contracts.WorkspaceTrustFieldType:          contracts.WorkspaceTrustControlTypeQuestion,
		contracts.WorkspaceTrustFieldPath:          "/work/project",
		contracts.WorkspaceTrustFieldConfiguration: []any{"hooks"},
	})
	require.NoError(t, err)
	return payload
}

func TestZCodeReadControlRequest_DrawsTheHookTrustQuestionWithTheAgentName(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:  "Trust the folder /work/project",
		Reason: "ZCode loads the hooks of a project only from a folder that you trust, and it keeps your answer for the folder.",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "workspace-trust", Kind: zcodeAllowOnce, Name: "Trust this folder"},
			{OptionId: "workspace-reject", Kind: zcodeRejectOnce, Name: "Do not trust"},
		},
	}}, EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE}, agenttest.ControlPromptOf(zcodeProvider{}, zcodeTrustQuestion(t)))
}

func TestZCodeAnswerControlRequest_AnswersTheHookTrustQuestion(t *testing.T) {
	t.Parallel()
	payload := zcodeTrustQuestion(t)
	// The question is stored under the key of its session.
	const sessionID = "sess-1"
	requestID := providerkit.WorkspaceTrustRequestID(sessionID)
	for option, decision := range map[string]string{"workspace-trust": "trust", "workspace-reject": "reject"} {
		got, err := agenttest.AnswerControl(zcodeProvider{}, payload, requestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		res := agenttest.ResolveAnswerInSession(zcodeProvider{}, payload, sessionID, requestID, got)
		require.False(t, res.Withhold, option)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+requestID+`","response":{"decision":"`+decision+`"}}}`, string(res.Content), option)
	}
	_, err := agenttest.AnswerControl(zcodeProvider{}, payload, requestID, agenttest.PermissionDecision("workspace-reject", "Not here."))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the trust question takes no reason")
	_, err = agenttest.AnswerControl(zcodeProvider{}, payload, requestID, agenttest.PermissionDecision("allow_once", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the question offers its own two answers only")
}

func TestZCodeAnswerControlRequest_RefusesAPromptKindThatZCodeNeverStates(t *testing.T) {
	t.Parallel()
	_, err := zcodeProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: agent.NewControlRequestInput([]byte(`{}`)), RequestID: "req-1",
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{Title: "T"}}},
		Decision: agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, ""),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}
