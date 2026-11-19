package opencode

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// These cases read and answer the control requests of OpenCode's native
// protocol (opencodeProvider). family_control_prompt_test.go states the
// promises of the family's Agent Client Protocol transport, which Kilo
// speaks.

// nativeShellPermission is a stored `permission.asked` event of a shell call,
// as the native control registry publishes it.
const nativeShellPermission = `{"type":"permission.asked","data":{"id":"per_1","sessionID":"ses_1","action":"shell",
	"resources":["npm test -- --runInBand"],"save":[],"metadata":{"timeout":0,"quiet":false,"note":""}},
	"request":{"tool_name":"shell"}}`

// nativeSavablePermission states a pattern that an always allow saves.
const nativeSavablePermission = `{"type":"permission.asked","data":{"id":"per_1","sessionID":"ses_1","action":"shell",
	"resources":["pwd"],"save":["pwd *"]},"request":{"tool_name":"shell"}}`

// nativeQuestionForm is a stored form of the question tool.
const nativeQuestionForm = `{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","metadata":{"kind":"question"},"fields":[
	{"key":"task","type":"string","title":"Task","description":"Pick a task","required":true,"options":[{"value":"build","label":"Build","description":"Compile"},{"value":1,"label":"bad"},null]},
	{"key":"env","type":"multiselect","title":"Env","custom":true,"minItems":1,"maxItems":2,"options":[{"value":"dev","label":"Dev"},{"value":"prod","label":"Prod"}]},
	{"key":"note","type":"string"}]}},"request":{"tool_name":"question"}}`

// nativeTypedForm is a stored form of typed fields.
const nativeTypedForm = `{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","fields":[
	{"key":"count","type":"integer","title":"Count"},{"key":"enabled","type":"boolean","title":"Enabled"}]}},"request":{"tool_name":"form"}}`

// nativeURLForm is a stored form of one external field.
const nativeURLForm = `{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","title":"Auth",
	"metadata":{"kind":"mcp-elicitation","server":"github","message":"Sign in"},
	"fields":[{"key":"open","type":"external","url":"https://example.test/auth","description":"Open the page"}]}},"request":{"tool_name":"mcp-elicitation"}}`

var (
	nativePermissionRequestID = openCodeControlKey("permission", "ses_1", "per_1")
	nativeFormRequestID       = openCodeControlKey("form", "ses_1", "frm_1")
)

// nativeResolved resolves answer to payload as the Worker does before it
// delivers the answer, and requires that the resolver accepts it.
func nativeResolved(t *testing.T, payload, requestID string, answer agent.ControlAnswer) string {
	t.Helper()
	resolved := agenttest.ResolveAnswer(opencodeProvider{}, []byte(payload), requestID, answer)
	require.False(t, resolved.Withhold, "the resolver refuses the answer: %v", resolved.Refusal())
	return string(resolved.Content)
}

func nativeOptionIDs(prompt *leapmuxv1.ControlPrompt) []string {
	var ids []string
	for _, option := range prompt.GetPermission().GetOptions() {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

func TestReadControlRequest_AShellPermissionStatesItsCommandAndKeepsEmptyMetadata(t *testing.T) {
	payload := []byte(`{"type":"permission.asked","data":{"action":"shell","resources":["printf hello"],"save":[],"metadata":{"timeout":0,"quiet":false,"note":""}}}`)
	original := bytes.Clone(payload)
	permission := agenttest.ControlPromptOf(opencodeProvider{}, payload).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "printf hello", permission.GetCommand())
	assert.Equal(t, "shell", permission.GetTitle(), "the request identifies its operation")
	assert.JSONEq(t, `{"timeout":0,"quiet":false,"note":""}`, permission.GetInputJson(), "zero, false and empty values stay")
	assert.Equal(t, original, payload, "the reader leaves the stored payload unchanged")

	permission = agenttest.ControlPromptOf(opencodeProvider{}, []byte(nativeShellPermission)).GetPermission()
	assert.Equal(t, "npm test -- --runInBand", permission.GetCommand())
	assert.Equal(t, "shell", permission.GetTitle())
	assert.Equal(t, `{"timeout":0,"quiet":false,"note":""}`, permission.GetInputJson(), "the metadata keeps its own order")
	assert.Empty(t, permission.GetReason())
}

func TestReadControlRequest_AShellPermissionStatesEachResourceOnItsOwnLine(t *testing.T) {
	permission := agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"permission.asked","data":{"action":"shell","resources":["printf hello","ls"]}}`)).GetPermission()
	assert.Equal(t, "printf hello\nls", permission.GetCommand())
	assert.Equal(t, `{}`, permission.GetInputJson(), "a command states its resources, so the arguments repeat none")

	permission = agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"permission.asked","data":{"action":"shell","resources":[]}}`)).GetPermission()
	assert.Empty(t, permission.GetCommand(), "a shell permission with no resource states no command")
	assert.Equal(t, `{"resources":[]}`, permission.GetInputJson())
}

// The resources are what the user approves, so a metadata member of the same
// name cannot hide them.
func TestReadControlRequest_TheResourcesWinOverTheMetadata(t *testing.T) {
	permission := agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"permission.asked","data":{"action":"external_directory",
		"resources":["/outside/*"],"save":["/outside/*"],"metadata":{"resources":["unrelated"],"detail":"Inspect the directory."},"message":"The path is outside the project."}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "external_directory", permission.GetTitle())
	assert.Empty(t, permission.GetCommand(), "only a shell permission states a command")
	assert.Equal(t, `{"resources":["/outside/*"],"detail":"Inspect the directory."}`, permission.GetInputJson(), "the resources keep the place of the metadata member")
	assert.Equal(t, "The path is outside the project.", permission.GetReason())
}

// The buttons carry the words that a saved decision shows (Allow and Deny),
// and always allow appears only for a request that states what it saves.
func TestReadControlRequest_OffersTheNativeDecisions(t *testing.T) {
	want := []*leapmuxv1.PermissionOption{
		{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow"},
		{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
	}
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Options: want}),
		agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Options: agenttest.ControlPromptOf(opencodeProvider{}, []byte(nativeShellPermission)).GetPermission().GetOptions()}))

	got := agenttest.ControlPromptOf(opencodeProvider{}, []byte(nativeSavablePermission)).GetPermission().GetOptions()
	require.Len(t, got, 3)
	assert.True(t, proto.Equal(&leapmuxv1.PermissionOption{
		OptionId: "always", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Always allow",
		Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_PROJECT,
	}, got[1]), "%v", got[1])
	assert.Equal(t, []string{"once", "always", "reject"}, nativeOptionIDs(agenttest.ControlPromptOf(opencodeProvider{}, []byte(nativeSavablePermission))))
	assert.Equal(t, []string{"once", "reject"}, nativeOptionIDs(agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"permission.asked","data":{"action":"read","resources":["/a.ts"],"save":"pwd *"}}`))),
		"a save list of the wrong type states nothing to save")
}

func TestReadControlRequest_AMemberOfTheWrongTypeReadsAsAbsent(t *testing.T) {
	permission := agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"permission.asked","data":{"action":7,"resources":["a",3,null],"metadata":[1],"message":false}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Empty(t, permission.GetTitle())
	assert.Empty(t, permission.GetReason())
	assert.Equal(t, `{"resources":["a"]}`, permission.GetInputJson(), "a resource that is no string drops, and metadata that is no object reads as none")
	assert.Equal(t, []string{"once", "reject"}, nativeOptionIDs(agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"permission.asked","data":{}}`))))
}

// The registry publishes no other request, and the resolver refuses an answer
// to any other payload, so such a payload reads as unrecognized: a prompt with
// Allow and Deny would fail only after a click.
func TestReadControlRequest_AnyOtherPayloadIsUnrecognized(t *testing.T) {
	for _, payload := range []string{
		`{}`,
		`{"type":"permission.asked"}`,
		`{"type":"permission.asked","data":[]}`,
		`{"type":"question.asked","properties":{"questions":[]}}`,
		`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c","kind":"execute","rawInput":{"command":"ls"}}}}`,
		`{"type":"form.created","data":{"form":{"fields":[]}}}`,
		`{"type":"form.created","data":{"form":{"fields":[{"key":"a","type":"date"}]}}}`,
		`{"type":"form.created","data":{"form":{"fields":[{"key":1,"type":"string"}]}}}`,
		`{"type":"form.created","data":{"form":{"fields":[{"key":"a","type":"string"},null]}}}`,
		`{"type":"form.created","data":{"form":{"fields":{"key":"a","type":"string"}}}}`,
		`{"type":"form.created","data":{}}`,
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(opencodeProvider{}, []byte(payload)), payload)
	}
}

func TestReadControlRequest_AQuestionFormIsAQuestion(t *testing.T) {
	got := agent.ReadControlPrompt(opencodeProvider{}, agent.NewControlRequestInput([]byte(nativeQuestionForm)))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{
		EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER,
		Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{
			{
				Id: "task", Header: "Task", Question: "Pick a task",
				Options:         []*leapmuxv1.QuestionOption{{Value: proto.String("build"), Label: "Build", Description: "Compile"}},
				AllowCustomText: proto.Bool(false),
			},
			{
				Id: "env", Header: "Env", Question: "Env",
				Options:     []*leapmuxv1.QuestionOption{{Value: proto.String("dev"), Label: "Dev"}, {Value: proto.String("prod"), Label: "Prod"}},
				MultiSelect: true, AllowEmpty: true, AllowCustomText: proto.Bool(true),
				MinimumSelections: proto.Int32(1), MaximumSelections: proto.Int32(2),
			},
			{Id: "note", AllowEmpty: true, AllowCustomText: proto.Bool(false)},
		}}},
	}, got)
}

// A question that the question tool cannot ask is a form: a hidden or a
// conditional field, a field of another type, or a form that states no
// question kind.
func TestReadControlRequest_AFormThatNoQuestionCanAskIsAForm(t *testing.T) {
	for name, payload := range map[string]string{
		"a hidden field":         `{"type":"form.created","data":{"form":{"metadata":{"kind":"question"},"fields":[{"key":"a","type":"string","hidden":true}]}}}`,
		"a null condition":       `{"type":"form.created","data":{"form":{"metadata":{"kind":"question"},"fields":[{"key":"a","type":"string","when":null}]}}}`,
		"a boolean field":        `{"type":"form.created","data":{"form":{"metadata":{"kind":"question"},"fields":[{"key":"a","type":"boolean"}]}}}`,
		"no question kind":       `{"type":"form.created","data":{"form":{"fields":[{"key":"a","type":"string"}]}}}`,
		"metadata of wrong type": `{"type":"form.created","data":{"form":{"metadata":"question","fields":[{"key":"a","type":"string"}]}}}`,
	} {
		got := agenttest.ControlPromptOf(opencodeProvider{}, []byte(payload))
		assert.Nil(t, got.GetQuestion(), name)
		assert.NotNil(t, got.GetElicitation(), name)
	}
}

// A fraction of a count admits the same selections as the whole count that
// the reader states.
func TestReadControlRequest_AQuestionStatesWholeSelectionCounts(t *testing.T) {
	question := agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"form.created","data":{"form":{"metadata":{"kind":"question"},"fields":[
		{"key":"a","type":"multiselect","minItems":1.5,"maxItems":2.5}]}}}`)).GetQuestion().GetQuestions()[0]
	assert.Equal(t, int32(2), question.GetMinimumSelections())
	assert.Equal(t, int32(2), question.GetMaximumSelections())

	question = agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"form.created","data":{"form":{"metadata":{"kind":"question"},"fields":[
		{"key":"a","type":"multiselect","minItems":-1e12,"maxItems":1e12}]}}}`)).GetQuestion().GetQuestions()[0]
	assert.Equal(t, int32(-2147483648), question.GetMinimumSelections(), "a count saturates")
	assert.Equal(t, int32(2147483647), question.GetMaximumSelections())

	question = agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"form.created","data":{"form":{"metadata":{"kind":"question"},"fields":[
		{"key":"a","type":"multiselect","minItems":"1","maxItems":null}]}}}`)).GetQuestion().GetQuestions()[0]
	assert.Nil(t, question.MinimumSelections, "a count that is no number states none")
	assert.Nil(t, question.MaximumSelections)
}

func TestReadControlRequest_AFormStatesEachFieldInItsSchema(t *testing.T) {
	got := agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"form.created","data":{"form":{"title":"Setup","metadata":{"server":"srv","message":"Fill it"},"fields":[
		{"key":"name","type":"string","title":"Name","description":"Your name","default":"x","format":"email","minLength":3,"maxLength":9,"pattern":"^a","required":true},
		{"key":"n","type":"number","minimum":1,"maximum":5.5},
		{"key":"tags","type":"multiselect","title":"Tags","minItems":2,"maxItems":3,"required":true,"options":[{"value":"a","label":"A"},{"value":"b","label":"B","description":"bee"},"junk",{"label":"no value"}]},
		{"key":"free","type":"multiselect","custom":true,"options":[{"value":"a","label":"A"}]},
		{"key":"pick","type":"string","options":[{"value":"x","label":"X"}],"when":[{"key":"n","op":"eq","value":2},{"key":"n","op":"gt","value":1},{"key":7,"op":"eq","value":1},{"key":"n","op":"eq","value":null},"junk",{"key":"name","op":"neq","value":"a<b"}]},
		{"key":"link","type":"external","description":"Sign in","url":"https://example.test"}]}}}`)).GetElicitation()
	require.NotNil(t, got)
	assert.Equal(t, contracts.MCPElicitationModeForm, got.GetMode())
	assert.Equal(t, "Setup", got.GetTitle())
	assert.Equal(t, "Fill it", got.GetMessage())
	assert.Equal(t, "srv", got.GetServer())
	assert.Equal(t, `{"type":"object","properties":{`+
		`"name":{"title":"Name","description":"Your name","default":"x","format":"email","minLength":3,"maxLength":9,"pattern":"^a","type":"string"},`+
		`"n":{"minimum":1,"maximum":5.5,"type":"number"},`+
		`"tags":{"title":"Tags","type":"array","items":{"type":"string","enum":["a","b",null],"enumNames":["A","B","no value"]},"minItems":2,"maxItems":3},`+
		`"free":{"type":"array","items":{"type":"string"}},`+
		`"pick":{"type":"string","enum":["x"],"enumNames":["X"]},`+
		`"link":{"description":"Sign in\n\nhttps://example.test","type":"boolean","const":true}},`+
		`"required":["name","tags","link"]}`, got.GetSchemaJson(), "the schema keeps the order of the fields and of the members")
	require.Len(t, got.GetFieldRules(), 6)
	for i, key := range []string{"name", "n", "tags", "free", "pick", "link"} {
		assert.Equal(t, key, got.GetFieldRules()[i].GetKey())
		assert.False(t, got.GetFieldRules()[i].GetHidden())
	}
	agenttest.RequireProto(t, &leapmuxv1.ElicitationFieldRule{Key: "pick", Conditions: []*leapmuxv1.ElicitationFieldCondition{
		{Field: "n", Comparison: leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL, ValueJson: "2"},
		{Field: "name", Comparison: leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_NOT_EQUAL, ValueJson: `"a<b"`},
	}}, got.GetFieldRules()[4], "a condition of an unknown comparison, of no field key, or of a value that is no scalar drops")
}

func TestReadControlRequest_ARequiredFieldNeedsOneValueAtLeast(t *testing.T) {
	got := agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"form.created","data":{"form":{"fields":[
		{"key":"s","type":"string","required":true,"minLength":"x"},
		{"key":"t","type":"string","required":true,"minLength":0.5},
		{"key":"m","type":"multiselect","required":true,"minItems":4},
		{"key":"b","type":"boolean","options":[{"value":"y","label":"Y"}]}]}}}`)).GetElicitation()
	assert.Equal(t, `{"type":"object","properties":{`+
		`"s":{"minLength":1,"type":"string"},`+
		`"t":{"minLength":1,"type":"string"},`+
		`"m":{"type":"array","items":{"type":"string"},"minItems":4},`+
		`"b":{"type":"string","enum":["y"],"enumNames":["Y"]}},`+
		`"required":["s","t","m"]}`, got.GetSchemaJson(), "a bound that is no number or less than one becomes one, and options turn any field into a choice of text")
}

func TestReadControlRequest_AFormOfOneExternalFieldIsAURLElicitation(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: contracts.MCPElicitationModeURL, Title: "Auth", Message: "Sign in", Server: "github", Url: "https://example.test/auth",
	}}}, agenttest.ControlPromptOf(opencodeProvider{}, []byte(nativeURLForm)))

	got := agenttest.ControlPromptOf(opencodeProvider{}, []byte(`{"type":"form.created","data":{"form":{"fields":[{"key":"open","type":"external","url":"u"},{"key":"b","type":"boolean"}]}}}`)).GetElicitation()
	assert.Equal(t, contracts.MCPElicitationModeForm, got.GetMode(), "an external field beside another field is a confirmation of the form")
	assert.Equal(t, `{"type":"object","properties":{"open":{"type":"boolean","const":true,"description":"u"},"b":{"type":"boolean"}},"required":["open"]}`, got.GetSchemaJson())
}

// testdata/opencode_form_prompt_conformance.json states the prompt of each
// form, and the browser's half fills the form of that prompt.
func TestReadControlRequest_FormPromptConformance(t *testing.T) {
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "opencode_form_prompt_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name    string          `json:"name"`
			Payload json.RawMessage `json:"payload"`
			Prompt  json.RawMessage `json:"prompt"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			want := &leapmuxv1.ControlPrompt{}
			require.NoError(t, protojson.Unmarshal(tc.Prompt, want))
			agenttest.RequireProto(t, want, agent.ReadControlPrompt(opencodeProvider{}, agent.NewControlRequestInput(tc.Payload)))
		})
	}
}

func TestAnswerControlRequest_SelectsEachNativeDecision(t *testing.T) {
	for _, optionID := range []string{"once", "always", "reject"} {
		got, err := agenttest.AnswerControl(opencodeProvider{}, []byte(nativeSavablePermission), nativePermissionRequestID, agenttest.PermissionDecision(optionID, ""))
		require.NoError(t, err, optionID)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"`+nativePermissionRequestID+`","result":{"outcome":{"outcome":"selected","optionId":"`+optionID+`"}}}`, string(got.Content))
		assert.Empty(t, got.FollowUp)
		assert.JSONEq(t, `{"request_id":"`+nativePermissionRequestID+`","decision":"`+optionID+`"}`, nativeResolved(t, nativeSavablePermission, nativePermissionRequestID, got))
	}
}

// The composer's refusal and the agent's reply are bound to the request that
// the user answers, and the native reject carries the reason itself.
func TestAnswerControlRequest_ARefusalCarriesItsReasonInTheNativeReject(t *testing.T) {
	const requestID = "native-request"
	got, err := agenttest.AnswerControl(opencodeProvider{}, []byte(nativeShellPermission), requestID, agenttest.PermissionDecision("reject", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-request","response":{"behavior":"deny","message":"use the other file"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the reply carries the reason, so no message follows")

	got, err = agenttest.AnswerControl(opencodeProvider{}, []byte(nativeShellPermission), nativePermissionRequestID, agenttest.PermissionDecision("reject", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"request_id":"`+nativePermissionRequestID+`","decision":"reject","message":"use the other file"}`, nativeResolved(t, nativeShellPermission, nativePermissionRequestID, got))

	got, err = agenttest.AnswerControl(opencodeProvider{}, []byte(nativeShellPermission), requestID, agenttest.PermissionDecision("once", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"native-request","result":{"outcome":{"outcome":"selected","optionId":"once"}}}`, string(got.Content))
}

func TestAnswerControlRequest_RefusesAPermissionDecisionThatTheRequestDoesNotTake(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an approval with a reason":           agenttest.PermissionDecision("once", "a note"),
		"always allow with nothing to save":   agenttest.PermissionDecision("always", ""),
		"LeapMux's own allow":                 agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
		"a question decision to a permission": agenttest.QuestionDecision(),
		"no decision":                         nil,
	} {
		_, err := agenttest.AnswerControl(opencodeProvider{}, []byte(nativeShellPermission), nativePermissionRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// A request that is no native control takes no answer: its prompt states the
// fault, and even a permission prompt drawn for it answers nothing that the
// resolver would send.
func TestAnswerControlRequest_ARequestThatIsNoNativeControlTakesNoAnswer(t *testing.T) {
	for _, decision := range []*leapmuxv1.ControlDecision{
		agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
		agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "no"),
		agenttest.PermissionDecision("once", ""),
	} {
		_, err := agenttest.AnswerControl(opencodeProvider{}, []byte(`{}`), "r", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
		_, err = opencodeProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
			Request: agent.NewControlRequestInput([]byte(`{}`)), RequestID: "r",
			Prompt: agent.GenericPermissionPrompt(), Decision: decision,
		})
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	}
}

func TestAnswerControlRequest_AnswersAQuestionFormByFieldKey(t *testing.T) {
	got, err := agenttest.AnswerControl(opencodeProvider{}, []byte(nativeQuestionForm), nativeFormRequestID, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"build"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"dev"}, CustomText: " staging "},
		&leapmuxv1.QuestionAnswer{},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"`+nativeFormRequestID+`","result":{"answer":{"task":"build","env":["dev","staging"]}}}`, string(got.Content),
		"a multiple choice takes the typed text as one more value, and an unanswered text field is left out")
	assert.Empty(t, got.FollowUp)
	assert.JSONEq(t, `{"request_id":"`+nativeFormRequestID+`","answer":{"task":"build","env":["dev","staging"]}}`, nativeResolved(t, nativeQuestionForm, nativeFormRequestID, got))

	got, err = agenttest.AnswerControl(opencodeProvider{}, []byte(nativeQuestionForm), nativeFormRequestID, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"build"}},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"`+nativeFormRequestID+`","result":{"answer":{"task":"build","env":[]}}}`, string(got.Content),
		"an unanswered multiple choice sends an empty list")

	got, err = agenttest.AnswerControl(opencodeProvider{}, []byte(nativeQuestionForm), nativeFormRequestID, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"build"}},
		&leapmuxv1.QuestionAnswer{CustomText: "qa"},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"`+nativeFormRequestID+`","result":{"answer":{"task":"build","env":["qa"]}}}`, string(got.Content))
}

// A field key is data, so a key that names a prototype member is an answer
// like any other.
func TestAnswerControlRequest_SendsAFieldKeyAsData(t *testing.T) {
	payload := `{"type":"form.created","data":{"form":{"metadata":{"kind":"question"},"fields":[
		{"key":"__proto__","type":"string","title":"Choice","options":[{"value":"selected","label":"Selected"}]}]}}}`
	got, err := agenttest.AnswerControl(opencodeProvider{}, []byte(payload), "native-form", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"selected"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"native-form","result":{"answer":{"__proto__":"selected"}}}`, string(got.Content))
}

// The native cancellation has no place for text, so the reason of a
// cancelled question drops.
func TestAnswerControlRequest_ACancelledQuestionCancelsTheForm(t *testing.T) {
	got, err := agenttest.AnswerControl(opencodeProvider{}, []byte(nativeQuestionForm), nativeFormRequestID, agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"`+nativeFormRequestID+`","result":{"cancel":true}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
	assert.JSONEq(t, `{"request_id":"`+nativeFormRequestID+`","cancel":true}`, nativeResolved(t, nativeQuestionForm, nativeFormRequestID, got))
}

func TestAnswerControlRequest_RefusesAQuestionAnswerThatTheFormDoesNotTake(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an option that the field does not offer":   agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"deploy"}}),
		"typed text in a field that takes options":  agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "deploy"}),
		"no answer to a required field":             agenttest.QuestionDecision(),
		"one option chosen twice":                   agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"build"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"dev", "dev"}}),
		"two options of a single choice":            agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"build", "build"}}),
		"a label in place of the value":             agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Build"}}),
		"a permission decision to a question":       agenttest.PermissionDecision("once", ""),
		"an elicitation decision to a question":     agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{}`),
		"answers beside a cancel of a question set": {Kind: &leapmuxv1.ControlDecision_Question{Question: &leapmuxv1.QuestionDecision{Cancelled: true, Answers: []*leapmuxv1.QuestionAnswer{{}}}}},
	} {
		_, err := agenttest.AnswerControl(opencodeProvider{}, []byte(nativeQuestionForm), nativeFormRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

func TestAnswerControlRequest_AnswersAFormThatTheResolverTurnsIntoTheNativeReply(t *testing.T) {
	got, err := agenttest.AnswerControl(opencodeProvider{}, []byte(nativeTypedForm), nativeFormRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"count":0,"enabled":false}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+nativeFormRequestID+`","response":{"action":"accept","content":{"count":0,"enabled":false}}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
	assert.JSONEq(t, `{"request_id":"`+nativeFormRequestID+`","answer":{"count":0,"enabled":false}}`, nativeResolved(t, nativeTypedForm, nativeFormRequestID, got))

	for _, action := range []leapmuxv1.ElicitationAction{leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL} {
		got, err = agenttest.AnswerControl(opencodeProvider{}, []byte(nativeTypedForm), nativeFormRequestID, agenttest.ElicitationDecision(action, ""))
		require.NoError(t, err, action)
		assert.JSONEq(t, `{"request_id":"`+nativeFormRequestID+`","cancel":true}`, nativeResolved(t, nativeTypedForm, nativeFormRequestID, got), action)
	}
}

// The user completes a URL form at its page, so the accept carries no
// content, and the resolver confirms the external field.
func TestAnswerControlRequest_AcceptsAURLFormWithItsExternalFieldConfirmed(t *testing.T) {
	got, err := agenttest.AnswerControl(opencodeProvider{}, []byte(nativeURLForm), nativeFormRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"request_id":"`+nativeFormRequestID+`","answer":{"open":true}}`, nativeResolved(t, nativeURLForm, nativeFormRequestID, got))

	_, err = agenttest.AnswerControl(opencodeProvider{}, []byte(nativeURLForm), nativeFormRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"open":true}`))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a URL form has no fields to fill")

	_, err = agenttest.AnswerControl(opencodeProvider{}, []byte(nativeURLForm), nativeFormRequestID, agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a form takes an elicitation decision")
}
