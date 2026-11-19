package ohmypi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// ompTestRequestID is the stored id of the request that answerOmp answers.
const ompTestRequestID = "r1"

// ompAskRequest is the question bridge's request for a two-question `ask`
// call. Its options state no values, so an answer selects each by its
// native label.
var ompAskRequest = map[string]any{
	"type": "leapmux_ask", "id": "158b2ba4d71bfb8f", "toolCallId": "call_3",
	"questions": []any{
		map[string]any{"id": "name", "question": "Project name?", "options": []any{
			map[string]any{"label": "alpha"}, map[string]any{"label": "beta"},
		}},
		map[string]any{"id": "langs", "question": "Which languages?", "header": "Languages", "multi": true, "options": []any{
			map[string]any{"label": "Go"},
			map[string]any{"label": "Rust Recommended", "markup": "<label><strong>Rust</strong> Recommended</label>"},
		}},
	},
}

// ompAskQuestion is the bridge's request with one question record.
func ompAskQuestion(record map[string]any) map[string]any {
	return map[string]any{"type": "leapmux_ask", "id": "q", "questions": []any{record}}
}

// readOmpQuestions reads the questions of payload, which must read as a
// question prompt.
func readOmpQuestions(t *testing.T, payload map[string]any) []*leapmuxv1.ControlQuestion {
	t.Helper()
	question := readOmpPrompt(t, payload, nil).GetQuestion()
	require.NotNil(t, question)
	return question.GetQuestions()
}

// ompOption is a question option that an answer selects by value.
func ompOption(value, label string) *leapmuxv1.QuestionOption {
	return &leapmuxv1.QuestionOption{Value: proto.String(value), Label: label}
}

// ompApproval is omp 18.2.11's own tool approval dialog (probe s2), with the
// given title.
func ompApproval(title string) map[string]any {
	return map[string]any{"type": "extension_ui_request", "id": "a1", "method": "select", "title": title, "options": []any{"Approve", "Deny"}}
}

func ompDialog(method string, extra map[string]any) map[string]any {
	payload := map[string]any{"type": "extension_ui_request", "id": "d1", "method": method}
	for key, value := range extra {
		payload[key] = value
	}
	return payload
}

func ompJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

// ompRequestInput is the input of one request whose source row, when source
// is not nil, is that frame.
func ompRequestInput(t *testing.T, payload map[string]any, source map[string]any) agent.ControlRequestInput {
	t.Helper()
	input := agent.NewControlRequestInput(ompJSON(t, payload))
	if source != nil {
		frame := ompJSON(t, source)
		input.Source = func() (agent.MessageContent, bool) { return agent.MessageContent{Original: frame}, true }
	}
	return input
}

func readOmpPrompt(t *testing.T, payload map[string]any, source map[string]any) *leapmuxv1.ControlPrompt {
	t.Helper()
	return ompProvider{}.ReadControlRequest(ompRequestInput(t, payload, source))
}

func answerOmp(t *testing.T, payload map[string]any, decision *leapmuxv1.ControlDecision) (agent.ControlAnswer, error) {
	t.Helper()
	return agenttest.AnswerControl(ompProvider{}, ompJSON(t, payload), ompTestRequestID, decision)
}

func ompApprovalPermission(permission *leapmuxv1.PermissionPrompt) *leapmuxv1.PermissionPrompt {
	permission.Options = []*leapmuxv1.PermissionOption{
		{OptionId: "Approve", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Approve"},
		{OptionId: "Deny", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
	}
	return permission
}

func ompStartFrame(toolName string, args any) map[string]any {
	frame := map[string]any{"type": "tool_execution_start", "toolCallId": "call_4", "toolName": toolName}
	if args != nil {
		frame["args"] = args
	}
	return frame
}

func TestReadControlRequest_EachRequestReadsAsItsOwnKind(t *testing.T) {
	for name, tc := range map[string]struct {
		payload map[string]any
		kind    string
		purpose leapmuxv1.ControlEditorPurpose
	}{
		"the bridge's request":          {map[string]any{"type": "leapmux_ask", "id": "q", "questions": []any{}}, "question", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER},
		"a select":                      {ompDialog("select", map[string]any{"title": "Pick", "options": []any{"a"}}), "question", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER},
		"a select titled as approval":   {ompDialog("select", map[string]any{"title": "Allow tool: x", "options": []any{"Yes", "No"}}), "question", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER},
		"the approval":                  {ompApproval("Allow tool: bash\nCommand: ls"), "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK},
		"a confirm":                     {ompDialog("confirm", map[string]any{"title": "Proceed?"}), "dialog", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"an input":                      {ompDialog("input", map[string]any{"title": "Proceed?"}), "dialog", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"an editor":                     {ompDialog("editor", map[string]any{"title": "Proceed?"}), "dialog", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"a notice":                      {ompDialog("notify", map[string]any{"message": "Done."}), "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"an unknown method":             {ompDialog("hologram", nil), "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"another frame with a method":   {map[string]any{"type": "response", "id": "x", "method": "confirm"}, "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"a select of another frame":     {map[string]any{"type": "response", "method": "select"}, "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"the bridge's answer":           {map[string]any{"type": "leapmux_ask_answer"}, "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"an empty request":              {map[string]any{}, "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"a method that a prototype has": {ompDialog("constructor", nil), "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
		"another method of a prototype": {ompDialog("__proto__", nil), "permission", leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE},
	} {
		got := readOmpPrompt(t, tc.payload, nil)
		kinds := map[string]bool{"question": got.GetQuestion() != nil, "permission": got.GetPermission() != nil, "dialog": got.GetDialog() != nil}
		assert.True(t, kinds[tc.kind], "%s reads as a %s, not %s", name, tc.kind, protojson.Format(got))
		assert.Equal(t, tc.purpose, got.GetEditorPurpose(), name)
	}
}

// A request that omp did not state takes LeapMux's own allow and deny
// (agent.GenericPermissionPrompt).
func TestReadControlRequest_ARequestOmpDidNotStateIsTheGenericPermission(t *testing.T) {
	want := agent.GenericPermissionPrompt()
	want.EditorPurpose = leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE
	agenttest.RequireProto(t, want, readOmpPrompt(t, ompDialog("hologram", nil), nil))
	agenttest.RequireProto(t, want, agenttest.ControlPromptOf(ompProvider{}, []byte("not json")))
}

// The option that the markup shows as "Rust" is the bridge's "Rust
// Recommended", so the prompt states that native label as the option's value,
// and an answer selects what the bridge matches.
func TestReadControlRequest_TheBridgeRequestStatesEveryQuestionOfTheCall(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{
		{Id: "name", Question: "Project name?", Options: []*leapmuxv1.QuestionOption{{Label: "alpha"}, {Label: "beta"}}},
		{
			Id: "langs", Question: "Which languages?", Header: "Languages", MultiSelect: true,
			Options: []*leapmuxv1.QuestionOption{
				{Label: "Go"},
				{Value: proto.String("Rust Recommended"), Label: "Rust", Description: "Recommended"},
			},
		},
	}}, readOmpPrompt(t, ompAskRequest, nil).GetQuestion())
}

func TestReadControlRequest_TheBridgeRequestStatesChoiceValuesFlagsAndInitialSelections(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "style", Question: "Choose a style", MultiSelect: true, AllowEmpty: true, AllowCustomText: proto.Bool(false),
		Options:           []*leapmuxv1.QuestionOption{ompOption("0", "Alpha"), ompOption("1", "Beta")},
		InitialSelections: &leapmuxv1.QuestionSelections{Values: []string{"0"}},
	}, readOmpQuestions(t, ompAskQuestion(map[string]any{
		"id": "style", "question": "Choose a style", "multi": true, "allowEmpty": true, "allowCustomText": false,
		"initialSelections": []any{"0", "missing", "0"},
		"options":           []any{map[string]any{"value": "0", "label": "Alpha"}, map[string]any{"value": "1", "label": "Beta"}},
	}))[0])
}

// The context is the form's text around the question, which the question
// shows above itself as the form states it.
func TestReadControlRequest_TheBridgeRequestKeepsTheFormContextBesideItsQuestion(t *testing.T) {
	context := "Policy  applies  everywhere\nKeep values."
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "target", Question: "Choose target", Context: context, MultiSelect: true,
		Options:           []*leapmuxv1.QuestionOption{ompOption("0", "Alpha")},
		InitialSelections: &leapmuxv1.QuestionSelections{Values: []string{"0"}},
	}, readOmpQuestions(t, ompAskQuestion(map[string]any{
		"id": "target", "question": "Choose target", "context": context, "multi": true,
		"initialSelections": []any{"0"},
		"options":           []any{map[string]any{"value": "0", "label": "Alpha"}},
	}))[0])
}

func TestReadControlRequest_TheBridgeRequestReadsEachOptionFromItsMarkup(t *testing.T) {
	questions := readOmpQuestions(t, ompAskQuestion(map[string]any{
		"id": "style", "question": "Choose a style", "allowCustomText": true,
		"options": []any{map[string]any{
			"value": "0", "label": "Alpha Fast const x = 1; x",
			"markup": `<label><input type="radio" name="style" value="alpha"><strong>Alpha</strong> <span>Fast</span> <pre>const x = 1;` + "\n" + `x</pre><script>DROP_SCRIPT_MARKER</script></label>`,
		}},
	}))
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "style", Question: "Choose a style", AllowCustomText: proto.Bool(true),
		Options: []*leapmuxv1.QuestionOption{{Value: proto.String("0"), Label: "Alpha", Description: "Fast", Preview: "```\nconst x = 1;\nx\n```"}},
	}, questions[0])
	assert.NotContains(t, protojson.Format(questions[0]), "DROP_SCRIPT_MARKER")
}

func TestReadControlRequest_TheBridgeRequestReadsRichMetadataAndDropsMalformedOptions(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "db", Question: "Which database?", Header: "Storage",
		Options: []*leapmuxv1.QuestionOption{
			{Value: proto.String("0"), Label: "SQLite", Description: "Single file · Recommended"},
			{Value: proto.String("1"), Label: "PostgreSQL", Preview: "```\nSELECT 1\n```"},
		},
	}, readOmpQuestions(t, ompAskQuestion(map[string]any{
		"id": "db", "question": "Which database?", "header": "Storage",
		"options": []any{
			map[string]any{"value": "0", "label": "SQLite Single file · Recommended", "markup": "<label><strong>SQLite</strong> Single file · Recommended</label>"},
			map[string]any{"value": "1", "label": "PostgreSQL SELECT 1", "markup": "<label><strong>PostgreSQL</strong><pre>SELECT 1</pre></label>"},
			map[string]any{"label": ""},
			map[string]any{"label": 7},
			"bad",
			nil,
		},
	}))[0])
}

func TestReadControlRequest_TheBridgeRequestKeepsARecommendationOfALabelAndDropsABlankPreview(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "a", Question: "Q?",
		Options: []*leapmuxv1.QuestionOption{
			ompOption("0", "x"),
			{Value: proto.String("1"), Label: "y", Description: "Recommended"},
		},
	}, readOmpQuestions(t, ompAskQuestion(map[string]any{
		"id": "a", "question": "Q?",
		"options": []any{
			map[string]any{"value": "0", "label": "x", "markup": "<label>x<pre> \n </pre></label>"},
			map[string]any{"value": "1", "label": "y Recommended", "markup": "<label><strong>y</strong> Recommended</label>"},
		},
	}))[0])
}

// The bridge adds a suffix to the label of the second option with the same
// text, so the two read apart.
func TestReadControlRequest_TheBridgeRequestKeepsTheSuffixOfARepeatedLabel(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "targets", Question: "Targets",
		Options: []*leapmuxv1.QuestionOption{
			ompOption("0", "Shared"), ompOption("1", "Shared (2)"),
			{Value: proto.String("2"), Label: "Alpha (2)", Description: "Fast"},
		},
	}, readOmpQuestions(t, ompAskQuestion(map[string]any{
		"id": "targets", "question": "Targets",
		"options": []any{
			map[string]any{"value": "0", "label": "Shared", "markup": "<label>Shared<input type=checkbox></label>"},
			map[string]any{"value": "1", "label": "Shared (2)", "markup": "<label>Shared<input type=checkbox></label>"},
			map[string]any{"value": "2", "label": "Alpha Fast (2)", "markup": "<label><strong>Alpha</strong> Fast</label>"},
		},
	}))[0])
}

// An option with no value, or an empty one, is the bridge's by its native
// label (askOptionValue). The prompt states that label as the value when the
// option shows another label, and leaves the value out otherwise.
func TestReadControlRequest_TheBridgeRequestSelectsAnOptionWithNoValueByItsNativeLabel(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "a", Question: "Q?",
		Options: []*leapmuxv1.QuestionOption{
			{Label: "plain"},
			{Label: "empty value"},
			{Value: proto.String("Rich Fast"), Label: "Rich", Description: "Fast"},
			{Value: proto.String("Rich Slow"), Label: "Rich", Description: "Slow"},
			ompOption("same", "same"),
		},
	}, readOmpQuestions(t, ompAskQuestion(map[string]any{
		"id": "a", "question": "Q?",
		"options": []any{
			map[string]any{"label": "plain"},
			map[string]any{"label": "empty value", "value": ""},
			map[string]any{"label": "Rich Fast", "markup": "<label><strong>Rich</strong> Fast</label>"},
			map[string]any{"label": "Rich Slow", "value": 3, "markup": "<label><strong>Rich</strong> Slow</label>"},
			map[string]any{"label": "same", "value": "same"},
		},
	}))[0])
}

func TestReadControlRequest_TheBridgeRequestReadsTheInitialSelections(t *testing.T) {
	options := []any{
		map[string]any{"value": "0", "label": "Alpha"},
		map[string]any{"value": "1", "label": "Beta"},
		map[string]any{"label": "Gamma"},
	}
	selections := func(multi bool, initial any) *leapmuxv1.QuestionSelections {
		t.Helper()
		record := map[string]any{"id": "q", "question": "Q?", "multi": multi, "options": options}
		if initial != nil {
			record["initialSelections"] = initial
		}
		return readOmpQuestions(t, ompAskQuestion(record))[0].GetInitialSelections()
	}
	assert.Nil(t, selections(true, nil), "a record with no list states no selection")
	assert.Nil(t, selections(true, "0"), "a list that is no array states no selection")
	assert.Nil(t, selections(true, json.RawMessage("null")), "a null list states no selection")
	for name, tc := range map[string]struct {
		multi   bool
		initial []any
		want    []string
	}{
		"an empty list is an empty selection":           {true, []any{}, []string{}},
		"values that no option offers":                  {true, []any{"9", "Alpha"}, []string{}},
		"each value once, in the order of the list":     {true, []any{"1", "0", "1"}, []string{"1", "0"}},
		"an option with no value, by its label":         {true, []any{"Gamma", 0, nil, "0"}, []string{"Gamma", "0"}},
		"the first offered value of a single choice":    {false, []any{"missing", "1", "0"}, []string{"1"}},
		"no offered value of a single choice":           {false, []any{"missing"}, []string{}},
		"a disabled option that the bridge also states": {true, []any{"0", "5"}, []string{"0"}},
	} {
		got := selections(tc.multi, tc.initial)
		require.NotNil(t, got, name)
		assert.Equal(t, tc.want, got.GetValues(), name)
	}
}

// A flag reads from a JSON boolean alone, and allowCustomText is stated only
// when the record states it.
func TestReadControlRequest_TheBridgeRequestReadsItsFlagsFromBooleansAlone(t *testing.T) {
	question := func(extra map[string]any) *leapmuxv1.ControlQuestion {
		t.Helper()
		record := map[string]any{"id": "q", "question": "Q?"}
		for key, value := range extra {
			record[key] = value
		}
		return readOmpQuestions(t, ompAskQuestion(record))[0]
	}
	assert.False(t, question(map[string]any{"multi": "yes", "allowEmpty": 1}).GetMultiSelect())
	assert.False(t, question(map[string]any{"allowEmpty": "true"}).GetAllowEmpty())
	assert.True(t, question(map[string]any{"allowEmpty": true}).GetAllowEmpty())
	assert.Nil(t, question(nil).AllowCustomText)
	assert.Nil(t, question(map[string]any{"allowCustomText": "false"}).AllowCustomText)
	assert.Equal(t, proto.Bool(true), question(map[string]any{"allowCustomText": true}).AllowCustomText)
	assert.Equal(t, proto.Bool(false), question(map[string]any{"allowCustomText": false}).AllowCustomText)
	assert.Empty(t, question(map[string]any{"context": 3, "header": false}).GetContext())
}

func TestReadControlRequest_TheBridgeRequestDropsWhatItCannotDraw(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{
		{Question: "Q?"},
	}}, readOmpPrompt(t, map[string]any{"type": "leapmux_ask", "id": "q", "questions": []any{
		nil, "text", []any{"an array"}, map[string]any{"id": "blank", "question": ""}, map[string]any{"id": "number", "question": 7},
		map[string]any{"id": "a", "options": []any{}},
		map[string]any{"question": "Q?", "multi": "yes", "options": "none"},
	}}, nil).GetQuestion())

	for _, payload := range []map[string]any{
		{"type": "leapmux_ask", "id": "q"},
		{"type": "leapmux_ask", "id": "q", "questions": []any{}},
		{"type": "leapmux_ask", "id": "q", "questions": "none"},
		{"type": "leapmux_ask", "id": "q", "questions": map[string]any{"questions": []any{}}},
	} {
		got := readOmpPrompt(t, payload, nil)
		require.NotNil(t, got.GetQuestion(), payload)
		assert.Empty(t, got.GetQuestion().GetQuestions(), payload)
	}
}

// A select takes one of its own options: omp sends no "Other" for it.
func TestReadControlRequest_ASelectStatesTheDescriptionBesideEachOption(t *testing.T) {
	// omp 18.2.11's own dialog (probe s2).
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
		Question:        "Which database?",
		Options:         []*leapmuxv1.QuestionOption{{Label: "SQLite (Recommended)", Description: "Single file"}, {Label: "PostgreSQL"}},
		AllowCustomText: proto.Bool(false),
	}}}, readOmpPrompt(t, ompDialog("select", map[string]any{
		"title": "Which database?", "options": []any{"SQLite (Recommended)", "PostgreSQL"},
		"optionDetails": []any{map[string]any{"description": "Single file"}, map[string]any{}},
	}), nil).GetQuestion())
}

func TestReadControlRequest_ASelectReadsWhatItCan(t *testing.T) {
	question := func(extra map[string]any) *leapmuxv1.ControlQuestion {
		t.Helper()
		questions := readOmpPrompt(t, ompDialog("select", extra), nil).GetQuestion().GetQuestions()
		require.Len(t, questions, 1)
		return questions[0]
	}
	selectQuestion := func(title string, labels ...string) *leapmuxv1.ControlQuestion {
		options := make([]*leapmuxv1.QuestionOption, 0, len(labels))
		for _, label := range labels {
			options = append(options, &leapmuxv1.QuestionOption{Label: label})
		}
		return &leapmuxv1.ControlQuestion{Question: title, Options: options, AllowCustomText: proto.Bool(false)}
	}
	agenttest.RequireProto(t, selectQuestion("Choose an option", "a"), question(map[string]any{"options": []any{"a"}}))
	agenttest.RequireProto(t, selectQuestion("Pick", "a", "b"),
		question(map[string]any{"title": "Pick", "options": []any{"a", "b"}, "optionDetails": "none"}))
	agenttest.RequireProto(t, selectQuestion("Pick", "a", "b", "c"),
		question(map[string]any{"title": "Pick", "options": []any{"a", "b", "c"}, "optionDetails": []any{"x", map[string]any{"description": ""}}}))
	agenttest.RequireProto(t, selectQuestion("Pick"), question(map[string]any{"title": "Pick"}))
	agenttest.RequireProto(t, selectQuestion("Pick", "kept"),
		question(map[string]any{"title": "Pick", "options": []any{1, map[string]any{"label": "x"}, "kept"}}))
}

func TestReadControlRequest_TheApprovalIsAPermissionWithItsTwoAnswers(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{
		Kind: &leapmuxv1.ControlPrompt_Permission{Permission: ompApprovalPermission(&leapmuxv1.PermissionPrompt{
			Title: "bash", Command: "echo approved-run",
		})},
		EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK,
	}, readOmpPrompt(t, ompApproval("Allow tool: bash\nCommand: echo approved-run"), nil))
}

func TestReadControlRequest_TheApprovalSplitsItsTitleIntoTheReasonAndTheCommand(t *testing.T) {
	for name, tc := range map[string]struct {
		title string
		want  *leapmuxv1.PermissionPrompt
	}{
		"a command on several lines, and safety checks": {
			"Allow tool: bash\nReason: Prompt required by bash pattern: rm\nCommand: printf a\nprintf b\nProvider safety checks:\n- sandbox off",
			&leapmuxv1.PermissionPrompt{
				Title:   "bash",
				Reason:  "Reason: Prompt required by bash pattern: rm\n\nProvider safety checks:\n- sandbox off",
				Command: "printf a\nprintf b",
			},
		},
		"details before the command": {
			"Allow tool: bash\nOrigin: agent\nCommand: git push --force",
			&leapmuxv1.PermissionPrompt{Title: "bash", Reason: "Origin: agent", Command: "git push --force"},
		},
		"no command": {
			"Allow tool: mcp__github_create_issue\nOrigin: MCP server tool\nReason: writes to GitHub",
			&leapmuxv1.PermissionPrompt{Title: "mcp__github_create_issue", Reason: "Origin: MCP server tool\nReason: writes to GitHub"},
		},
		"the tool alone":   {"Allow tool: write", &leapmuxv1.PermissionPrompt{Title: "write"}},
		"the prefix alone": {"Allow tool: ", &leapmuxv1.PermissionPrompt{}},
		"an empty command": {"Allow tool: bash\nCommand: ", &leapmuxv1.PermissionPrompt{Title: "bash"}},
		"a command of a shared test": {
			"Allow tool: bash\nCommand: printf \"first line\\n\"\nprintf \"second line\\n\"",
			&leapmuxv1.PermissionPrompt{Title: "bash", Command: "printf \"first line\\n\"\nprintf \"second line\\n\""},
		},
	} {
		got := readOmpPrompt(t, ompApproval(tc.title), nil).GetPermission()
		require.NotNil(t, got, name)
		agenttest.RequireProto(t, ompApprovalPermission(tc.want), got)
	}
}

func TestReadControlRequest_TheApprovalReadsTheArgumentsOfTheCallsStartFrame(t *testing.T) {
	for name, tc := range map[string]struct {
		title  string
		source map[string]any
		want   *leapmuxv1.PermissionPrompt
	}{
		"the call's own command wins over the shortened title": {
			"Allow tool: bash\nCommand: echo shor[…10ch elided…]",
			ompStartFrame("bash", map[string]any{"command": "echo short and complete"}),
			&leapmuxv1.PermissionPrompt{Title: "bash", Command: "echo short and complete", InputJson: `{"command":"echo short and complete"}`},
		},
		"a call that runs no command": {
			"Allow tool: write\nPath: notes.txt",
			ompStartFrame("write", map[string]any{"path": "notes.txt", "content": "x"}),
			&leapmuxv1.PermissionPrompt{Title: "write", Reason: "Path: notes.txt", InputJson: `{"content":"x","path":"notes.txt"}`},
		},
		"a start frame of another tool": {
			"Allow tool: bash\nCommand: ls",
			ompStartFrame("read", map[string]any{"path": "a"}),
			&leapmuxv1.PermissionPrompt{Title: "bash", Command: "ls"},
		},
		"an end frame of the same tool": {
			"Allow tool: bash\nCommand: ls",
			map[string]any{"type": "tool_execution_end", "toolCallId": "call_4", "toolName": "bash", "args": map[string]any{"command": "rm -rf /"}, "result": map[string]any{}},
			&leapmuxv1.PermissionPrompt{Title: "bash", Command: "ls"},
		},
		"a start frame with no arguments": {
			"Allow tool: bash\nCommand: ls",
			ompStartFrame("bash", nil),
			&leapmuxv1.PermissionPrompt{Title: "bash", Command: "ls"},
		},
		"arguments that are no object": {
			"Allow tool: bash\nCommand: ls",
			ompStartFrame("bash", []any{"ls"}),
			&leapmuxv1.PermissionPrompt{Title: "bash", Command: "ls"},
		},
		"arguments whose command is no text": {
			"Allow tool: bash\nCommand: ls",
			ompStartFrame("bash", map[string]any{"command": 7}),
			&leapmuxv1.PermissionPrompt{Title: "bash", Command: "ls", InputJson: `{"command":7}`},
		},
	} {
		got := readOmpPrompt(t, ompApproval(tc.title), tc.source).GetPermission()
		require.NotNil(t, got, name)
		if tc.want.GetInputJson() != "" {
			assert.JSONEq(t, tc.want.GetInputJson(), got.GetInputJson(), name)
			got.InputJson = tc.want.GetInputJson()
		}
		agenttest.RequireProto(t, ompApprovalPermission(tc.want), got)
	}
}

func TestIsApproval(t *testing.T) {
	approval := func(change func(map[string]any)) bool {
		payload := ompApproval("Allow tool: bash\nCommand: ls")
		change(payload)
		return readOmpControlPayload(ompJSON(t, payload)).isApproval()
	}
	assert.True(t, approval(func(map[string]any) {}))
	assert.True(t, approval(func(p map[string]any) { p["title"] = "Allow tool: " }), "an approval whose title is the prefix alone")
	assert.True(t, approval(func(p map[string]any) { p["options"] = []any{"Deny", "Later", "Approve"} }), "an approval that offers more options")
	for name, change := range map[string]func(map[string]any){
		"Approve alone":     func(p map[string]any) { p["options"] = []any{"Approve"} },
		"Deny alone":        func(p map[string]any) { p["options"] = []any{"Deny"} },
		"options as a word": func(p map[string]any) { p["options"] = "Approve Deny" },
		"another title":     func(p map[string]any) { p["title"] = "Pick one" },
		"no title":          func(p map[string]any) { delete(p, "title") },
		"a confirm":         func(p map[string]any) { p["method"] = "confirm" },
		"another frame":     func(p map[string]any) { p["type"] = "extension_ui_response" },
	} {
		assert.False(t, approval(change), name)
	}
	assert.False(t, readOmpControlPayload(nil).isApproval(), "no request")
}

func TestReadControlRequest_ConfirmInputAndEditorAreDialogsWithTheirFields(t *testing.T) {
	timeout := int64(30000)
	agenttest.RequireProto(t, &leapmuxv1.DialogPrompt{
		Title: "Proceed?", Message: "This deletes the branch.", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM, TimeoutMs: &timeout,
	}, readOmpPrompt(t, ompDialog("confirm", map[string]any{"title": "Proceed?", "message": "This deletes the branch.", "timeout": 30000}), nil).GetDialog())
	agenttest.RequireProto(t, &leapmuxv1.DialogPrompt{Title: "Branch name", Placeholder: "main", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT},
		readOmpPrompt(t, ompDialog("input", map[string]any{"title": "Branch name", "placeholder": "main"}), nil).GetDialog())
	agenttest.RequireProto(t, &leapmuxv1.DialogPrompt{Title: "Commit message", Prefill: "fix: ", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR},
		readOmpPrompt(t, ompDialog("editor", map[string]any{"title": "Commit message", "prefill": "fix: "}), nil).GetDialog())

	// The prompt states no difference between an empty field and an absent
	// one, so an empty message, hint and draft read as empty text.
	agenttest.RequireProto(t, &leapmuxv1.DialogPrompt{Title: "Commit message", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR},
		readOmpPrompt(t, ompDialog("editor", map[string]any{"title": "Commit message", "message": "", "placeholder": "", "prefill": ""}), nil).GetDialog())
	agenttest.RequireProto(t, &leapmuxv1.DialogPrompt{Title: "Name", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT},
		readOmpPrompt(t, ompDialog("input", map[string]any{"title": "Name", "message": 3, "placeholder": nil, "prefill": []any{"x"}}), nil).GetDialog())

	titles := []string{}
	for _, method := range []string{"confirm", "input", "editor"} {
		titles = append(titles, readOmpPrompt(t, ompDialog(method, nil), nil).GetDialog().GetTitle())
	}
	assert.Equal(t, []string{"Confirm", "Enter a value", "Enter your response"}, titles)
}

func TestReadControlRequest_ADialogStatesNoDeadlineForOneThatWaitsWithNoLimit(t *testing.T) {
	for _, value := range []any{nil, 0, -1, "soon"} {
		extra := map[string]any{"title": "Proceed?"}
		if value != nil {
			extra["timeout"] = value
		}
		assert.Nil(t, readOmpPrompt(t, ompDialog("confirm", extra), nil).GetDialog().TimeoutMs, "%v", value)
	}
}

func TestAnswerControlRequest_TheBridgeRequestTakesOneEnvelopeForTheWholeCall(t *testing.T) {
	got, err := answerOmp(t, map[string]any{"type": "leapmux_ask", "id": "q1", "questions": []any{
		map[string]any{"id": "db", "question": "Which database?", "options": []any{map[string]any{"label": "SQLite"}}},
	}}, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"SQLite"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"leapmux_ask_answer","id":"r1","answers":[{"id":"db","selected":["SQLite"]}]}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
	assert.Nil(t, got.PlanApproval)
}

// The bridge matches each answer to its question by id, and an empty half
// of an answer is left out.
func TestAnswerControlRequest_TheBridgeAnswerStatesEveryQuestionAndLeavesOutEmptyHalves(t *testing.T) {
	for name, tc := range map[string]struct {
		answers []*leapmuxv1.QuestionAnswer
		want    string
	}{
		"typed text and blank text": {
			[]*leapmuxv1.QuestionAnswer{{CustomText: "gamma"}, {Selected: []string{"Go"}, CustomText: "  "}},
			`[{"id":"name","custom":"gamma"},{"id":"langs","selected":["Go"]}]`,
		},
		"trimmed typed text": {
			[]*leapmuxv1.QuestionAnswer{{CustomText: " gamma \n"}, {Selected: []string{"Rust Recommended"}}},
			`[{"id":"name","custom":"gamma"},{"id":"langs","selected":["Rust Recommended"]}]`,
		},
	} {
		got, err := answerOmp(t, ompAskRequest, agenttest.QuestionDecision(tc.answers...))
		require.NoError(t, err, name)
		assert.JSONEq(t, `{"type":"leapmux_ask_answer","id":"r1","answers":`+tc.want+`}`, string(got.Content), name)
	}

	// The bridge takes an answer only when every question has one.
	for name, answers := range map[string][]*leapmuxv1.QuestionAnswer{
		"an unanswered question": {{}, {Selected: []string{"Go", "Rust Recommended"}}},
		"no answer at all":       nil,
		"blank text alone":       {{CustomText: " \n"}, {Selected: []string{"Go"}}},
	} {
		_, err := answerOmp(t, ompAskRequest, agenttest.QuestionDecision(answers...))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// A multiple choice takes options and typed text together: the user chose
// both, and the bridge sends omp the toggles and then the text.
func TestAnswerControlRequest_AMultipleChoiceSendsTheOptionsAndTheTypedText(t *testing.T) {
	got, err := answerOmp(t, ompAskRequest, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"alpha"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"Go", "Rust Recommended"}, CustomText: " Zig too "},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"leapmux_ask_answer","id":"r1","answers":[
		{"id":"name","selected":["alpha"]},
		{"id":"langs","selected":["Go","Rust Recommended"],"custom":"Zig too"}
	]}`, string(got.Content))
}

// ompChoiceRequest is the bridge's request for a form like the one that the
// bridge reads from omp's HTML: options that an answer selects by value, a
// single choice that takes typed text, and an optional multiple choice that
// takes its options alone and selects one before the user answers.
var ompChoiceRequest = map[string]any{
	"type": "leapmux_ask", "id": "html-first",
	"questions": []any{
		map[string]any{
			"id": "style", "question": "Choose a style", "allowEmpty": false, "allowCustomText": true,
			"options": []any{map[string]any{"value": "0", "label": "Alpha"}, map[string]any{"value": "1", "label": "Beta"}},
		},
		map[string]any{
			"id": "sizes", "question": "Choose sizes", "multi": true, "allowEmpty": true, "allowCustomText": false,
			"initialSelections": []any{"0"},
			"options":           []any{map[string]any{"value": "0", "label": "Small"}, map[string]any{"value": "1", "label": "Large"}},
		},
	},
}

// The answer states the values of the chosen options, which the bridge
// matches (newAskPlan), and the initial selection is no part of the answer.
func TestAnswerControlRequest_TheBridgeAnswerSelectsOptionsByTheirValues(t *testing.T) {
	for name, tc := range map[string]struct {
		answers []*leapmuxv1.QuestionAnswer
		want    string
	}{
		"an option of each question": {
			[]*leapmuxv1.QuestionAnswer{{Selected: []string{"1"}}, {Selected: []string{"1", "0"}}},
			`[{"id":"style","selected":["1"]},{"id":"sizes","selected":["1","0"]}]`,
		},
		"typed text and the initial selection": {
			[]*leapmuxv1.QuestionAnswer{{CustomText: "Gamma"}, {Selected: []string{"0"}}},
			`[{"id":"style","custom":"Gamma"},{"id":"sizes","selected":["0"]}]`,
		},
		"an empty answer to an optional multiple choice": {
			[]*leapmuxv1.QuestionAnswer{{Selected: []string{"0"}}, {}},
			`[{"id":"style","selected":["0"]},{"id":"sizes"}]`,
		},
	} {
		got, err := answerOmp(t, ompChoiceRequest, agenttest.QuestionDecision(tc.answers...))
		require.NoError(t, err, name)
		assert.JSONEq(t, `{"type":"leapmux_ask_answer","id":"r1","answers":`+tc.want+`}`, string(got.Content), name)
	}
}

func TestAnswerControlRequest_RefusesABridgeAnswerThatTheQuestionsCannotTake(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an option and text for a single choice": agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"alpha"}, CustomText: "gamma"}),
		"two options for a single choice":        agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"alpha", "beta"}}),
		"an option that the question lacks":      agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"gamma"}}),
		"an option by the label that it shows":   agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"alpha"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"Rust"}}),
		"more answers than questions":            agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{}, &leapmuxv1.QuestionAnswer{}, &leapmuxv1.QuestionAnswer{}),
		"a dialog decision":                      agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, ""),
	} {
		_, err := answerOmp(t, ompAskRequest, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an option by its label, not its value":       agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Beta"}}, &leapmuxv1.QuestionAnswer{}),
		"typed text for options alone":                agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"0"}}, &leapmuxv1.QuestionAnswer{CustomText: "Medium"}),
		"typed text beside options for options alone": agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"0"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"1"}, CustomText: "Medium"}),
		"no answer to a question that needs one":      agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{}, &leapmuxv1.QuestionAnswer{Selected: []string{"1"}}),
		"no answer at all":                            agenttest.QuestionDecision(),
	} {
		_, err := answerOmp(t, ompChoiceRequest, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// The bridge matches each answer to its question by id, so a question with
// no id takes an answer with an empty id. A request with no question takes
// no answer but a cancellation.
func TestAnswerControlRequest_TheBridgeAnswerStatesAnEmptyIdForAQuestionWithNone(t *testing.T) {
	got, err := answerOmp(t, ompAskQuestion(map[string]any{"question": "Pick", "options": []any{map[string]any{"label": "a"}}}),
		agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"a"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"leapmux_ask_answer","id":"r1","answers":[{"id":"","selected":["a"]}]}`, string(got.Content))

	_, err = answerOmp(t, map[string]any{"type": "leapmux_ask", "id": "q", "questions": []any{}}, agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// A dismissed question sends omp's own cancellation, which carries no
// reason. The `ask` tool then ends omp's run.
func TestAnswerControlRequest_ARefusedQuestionCancelsTheDialog(t *testing.T) {
	cancelled := agenttest.CancelQuestions("User stopped")
	for name, payload := range map[string]map[string]any{
		"the bridge's request": {"type": "leapmux_ask", "id": "q1", "questions": []any{}},
		"a select":             ompDialog("select", map[string]any{"title": "Pick", "options": []any{"a", "b"}}),
	} {
		got, err := answerOmp(t, payload, cancelled)
		require.NoError(t, err, name)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"r1","cancelled":true}`, string(got.Content), name)
		assert.Empty(t, got.FollowUp, name)
	}
}

func TestAnswerControlRequest_ASelectTakesOneOfItsOptions(t *testing.T) {
	payload := ompDialog("select", map[string]any{"title": "Pick", "options": []any{"a", "b", " \uFEFF"}})
	for name, tc := range map[string]struct {
		answer *leapmuxv1.QuestionAnswer
		want   string
	}{
		"an option":                   {&leapmuxv1.QuestionAnswer{Selected: []string{"b"}}, `{"type":"extension_ui_response","id":"r1","value":"b"}`},
		"an option beside blank text": {&leapmuxv1.QuestionAnswer{Selected: []string{"b"}, CustomText: " "}, `{"type":"extension_ui_response","id":"r1","value":"b"}`},
		// omp refuses a blank value for a select, so a blank option cancels it.
		"a blank option": {&leapmuxv1.QuestionAnswer{Selected: []string{" \uFEFF"}}, `{"type":"extension_ui_response","id":"r1","cancelled":true}`},
	} {
		got, err := answerOmp(t, payload, agenttest.QuestionDecision(tc.answer))
		require.NoError(t, err, name)
		assert.JSONEq(t, tc.want, string(got.Content), name)
	}
	// The select offers no "Other", so the prompt takes no typed text, and a
	// user who gives no answer cancels instead.
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"typed text":             agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "c"}),
		"padded typed text":      agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: " main "}),
		"an option and the text": agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"b"}, CustomText: "main"}),
		"two options":            agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"a", "b"}}),
		"no answer":              agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{}),
		"blank text":             agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "  \n"}),
		"no decision":            agenttest.QuestionDecision(),
	} {
		_, err := answerOmp(t, payload, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

func TestAnswerControlRequest_TheApprovalSendsTheChosenWordAsTheValue(t *testing.T) {
	for _, option := range []string{"Approve", "Deny"} {
		got, err := answerOmp(t, ompApproval("Allow tool: bash\nCommand: pwd"), agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"r1","value":"`+option+`"}`, string(got.Content), option)
		assert.Empty(t, got.FollowUp, option)
	}
}

// omp's Deny carries no reason, so the typed feedback follows the answer as
// the user's next message.
func TestAnswerControlRequest_ARefusedApprovalSendsTheFeedbackAfterTheDeny(t *testing.T) {
	payload := ompApproval("Allow tool: bash\nCommand: ls")
	for feedback, followUp := range map[string]string{"use a safer command": "use a safer command", "  \n": "", " keep it ": " keep it "} {
		got, err := answerOmp(t, payload, agenttest.PermissionDecision("Deny", feedback))
		require.NoError(t, err, feedback)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"r1","value":"Deny"}`, string(got.Content), feedback)
		assert.Equal(t, followUp, got.FollowUp, feedback)
	}
	_, err := answerOmp(t, payload, agenttest.PermissionDecision("Approve", "but carefully"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no feedback")
	_, err = answerOmp(t, payload, agenttest.PermissionDecision("Later", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the approval offers Approve and Deny alone")
	_, err = answerOmp(t, ompApproval("Allow tool: bash"), agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "LeapMux's own allow is no answer of omp's approval")
}

// A request that omp did not state takes the neutral envelope of LeapMux's
// generic Allow and Deny (agent.AnswerAllowDeny), and takes no feedback.
func TestAnswerControlRequest_ARequestOmpDidNotStateTakesTheNeutralAllowOrDeny(t *testing.T) {
	payload := ompDialog("hologram", nil)
	got, err := answerOmp(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	got, err = answerOmp(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"deny","message":"`+contracts.ControlAnswerMessageRejectedByUser+`"}}}`, string(got.Content))
	_, err = answerOmp(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "no"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestAnswerControlRequest_ADialogAnswersWithOmpOwnEnvelopes(t *testing.T) {
	confirm := ompDialog("confirm", map[string]any{"title": "Proceed?"})
	input := ompDialog("input", map[string]any{"title": "Branch name"})
	editor := ompDialog("editor", map[string]any{"title": "Commit message"})
	for name, tc := range map[string]struct {
		payload  map[string]any
		decision *leapmuxv1.ControlDecision
		want     string
	}{
		"a confirm approved":    {confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, ""), `{"type":"extension_ui_response","id":"r1","confirmed":true}`},
		"a confirm denied":      {confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE, ""), `{"type":"extension_ui_response","id":"r1","confirmed":false}`},
		"an empty input":        {input, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, ""), `{"type":"extension_ui_response","id":"r1","value":""}`},
		"an editor's text":      {editor, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "fix: typo"), `{"type":"extension_ui_response","id":"r1","value":"fix: typo"}`},
		"an input dismissed":    {input, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, ""), `{"type":"extension_ui_response","id":"r1","cancelled":true}`},
		"a confirm dismissed":   {confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, ""), `{"type":"extension_ui_response","id":"r1","cancelled":true}`},
		"an editor with spaces": {editor, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "  body\n"), `{"type":"extension_ui_response","id":"r1","value":"  body\n"}`},
	} {
		got, err := answerOmp(t, tc.payload, tc.decision)
		require.NoError(t, err, name)
		assert.JSONEq(t, tc.want, string(got.Content), name)
		assert.Empty(t, got.FollowUp, name)
	}
	_, err := answerOmp(t, confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "yes"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a confirm takes no value")
	_, err = answerOmp(t, input, agenttest.PermissionDecision("Approve", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a dialog takes a dialog decision")
}

func TestAnswerControlRequest_RefusesAPromptKindThatOmpDoesNotState(t *testing.T) {
	input := agent.NewControlRequestInput(ompJSON(t, ompDialog("confirm", nil)))
	_, err := ompProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: "r1",
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}},
		Decision: agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// The answer is the bytes that SendRawInput reads: the bridge turns one
// multiple-choice answer with typed text into omp's toggles, its "Other",
// the input's text, and "Done".
func TestAnswerControlRequest_TheBridgeAnswersOmpDialogChainFromTheAnswer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(askCallMessage("call_2", multiAskArgs), multiAskDialog1)
	published := r.sink.PublishedControls()
	require.Len(t, published, 1)
	assert.Equal(t, contracts.OhMyPiAskTypeRequest, readOmpControlPayload(published[0].Payload).text("type"))
	agenttest.AssertPublishedControlKeys(t, ompProvider{}, r.sink)
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "langs", Question: "Which languages?", MultiSelect: true, AllowCustomText: proto.Bool(true),
		Options: []*leapmuxv1.QuestionOption{ompOption("0", "Go"), ompOption("1", "Rust"), ompOption("2", "TypeScript")},
	}, agenttest.ControlPromptOf(ompProvider{}, published[0].Payload).GetQuestion().GetQuestions()[0])

	answer, err := agenttest.AnswerControl(ompProvider{}, published[0].Payload, published[0].RequestID,
		agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"0", "1"}, CustomText: "Zig too"}))
	require.NoError(t, err)
	require.NoError(t, r.agent.SendRawInput(agent.RawInput{Data: answer.Content}))
	r.waitForDialogAnswers(1)
	r.emit(askStart("call_2"), multiAskDialog2)
	r.waitForDialogAnswers(2)
	r.emit(multiAskDialog3)
	r.waitForDialogAnswers(3)
	r.emit(nativeAskDialog(t, "custom", "input", "Which languages?"))
	r.waitForDialogAnswers(4)
	r.emit(nativeAskDialog(t, "done", "select", "(3 selected) Which languages?", "Go", "Rust", "TypeScript", askOtherOption, "✔ Done selecting"))
	answers := r.waitForDialogAnswers(5)
	values := make([]any, 0, len(answers))
	for _, dialogAnswer := range answers {
		values = append(values, dialogAnswer["value"])
	}
	assert.Equal(t, []any{"Go", "Rust", askOtherOption, "Zig too", "✔ Done selecting"}, values)
	assert.Equal(t, 1, r.sink.PublishedControlCount(), "the later dialogs of the chain never reach the user")
}

// The form selects Go, and a disabled option, before the user answers. The
// prompt offers Go as the initial selection, and the user clears it: the
// bridge toggles Go off and finishes, and the disabled option stays as omp
// keeps it.
func TestAnswerControlRequest_TheBridgeClearsAnInitialSelectionThatTheUserClears(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	args := askChoiceFormArguments(t, `<form><fieldset><legend>Languages</legend><label><input type="checkbox" name="langs" value="go" checked>Go</label><label><input type="checkbox" name="langs" value="rust">Rust</label><label><input type="checkbox" name="langs" value="disabled" checked disabled>Disabled</label></fieldset></form>`)
	r.emit(askCallMessage("optional-languages", args),
		nativeAskDialog(t, "d1", "select", "(2 selected) Languages", "Go", "Rust", askOtherOption, "✔ Done selecting"))
	published := r.sink.PublishedControls()
	require.Len(t, published, 1)
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{
		Id: "langs", Question: "Languages", MultiSelect: true, AllowEmpty: true, AllowCustomText: proto.Bool(true),
		Options:           []*leapmuxv1.QuestionOption{ompOption("0", "Go"), ompOption("1", "Rust")},
		InitialSelections: &leapmuxv1.QuestionSelections{Values: []string{"0"}},
	}, agenttest.ControlPromptOf(ompProvider{}, published[0].Payload).GetQuestion().GetQuestions()[0])

	answer, err := agenttest.AnswerControl(ompProvider{}, published[0].Payload, published[0].RequestID,
		agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"leapmux_ask_answer","id":"d1","answers":[{"id":"langs"}]}`, string(answer.Content))
	require.NoError(t, r.agent.SendRawInput(agent.RawInput{Data: answer.Content}))
	assert.Equal(t, "Go", r.waitForDialogAnswers(1)[0]["value"])
	r.emit(askStart("optional-languages"),
		nativeAskDialog(t, "d2", "select", "(1 selected) Languages", "Go", "Rust", askOtherOption, "✔ Done selecting"))
	assert.Equal(t, "✔ Done selecting", r.waitForDialogAnswers(2)[1]["value"])
}
