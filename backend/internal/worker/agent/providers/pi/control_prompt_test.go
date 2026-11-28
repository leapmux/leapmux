package pi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// piPlanOptions are the options of Pi's plan menu: the three plan actions,
// and the answers that only Pi offers.
var piPlanOptions = []any{
	"Implement here",
	"Start fresh and implement",
	"Export plan…",
	"Save for later",
	"Stay in Plan mode",
	"Discard plan and exit",
}

func piJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

// piRequestInput is the input of one request whose source row, when source
// is not nil, is that frame.
func piRequestInput(t *testing.T, payload map[string]any, source map[string]any) agent.ControlRequestInput {
	t.Helper()
	input := agent.NewControlRequestInput(piJSON(t, payload))
	if source != nil {
		frame := piJSON(t, source)
		input.Source = func() (agent.MessageContent, bool) { return agent.MessageContent{Original: frame}, true }
	}
	return input
}

func readPiPrompt(t *testing.T, payload map[string]any, source map[string]any) *leapmuxv1.ControlPrompt {
	t.Helper()
	return piProvider{}.ReadControlRequest(piRequestInput(t, payload, source))
}

func answerPi(t *testing.T, payload map[string]any, source map[string]any, decision *leapmuxv1.ControlDecision) (agent.ControlAnswer, error) {
	t.Helper()
	input := piRequestInput(t, payload, source)
	return piProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: "req-1", Prompt: piProvider{}.ReadControlRequest(input), Decision: decision,
	})
}

func piStringPointer(value string) *string { return &value }

// piAskSource is the start frame of an ask_user_question call.
func piAskSource(questions ...any) map[string]any {
	return map[string]any{
		"type": "tool_execution_start", "toolCallId": "question-tool", "toolName": "ask_user_question",
		"args": map[string]any{"questions": questions},
	}
}

func piPlanMenu(title string, options []any) map[string]any {
	return map[string]any{"type": "extension_ui_request", "id": "plan-dialog", "method": "select", "title": title, "options": options}
}

func TestReadControlRequest_AnInputAndASelectAreQuestions(t *testing.T) {
	for _, method := range []string{"input", "select"} {
		got := readPiPrompt(t, map[string]any{"type": "extension_ui_request", "method": method, "title": "Approve?"}, nil)
		require.NotNil(t, got.GetQuestion(), method)
		assert.Equal(t, leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER, got.GetEditorPurpose(), method)
		assert.False(t, got.GetQuestion().GetPreservesSelectionNotes(), "a Pi answer is an option or a typed text, not both")
	}
	for _, method := range []string{"confirm", "editor"} {
		got := readPiPrompt(t, map[string]any{"type": "extension_ui_request", "method": method, "title": "Approve?"}, nil)
		assert.Nil(t, got.GetQuestion(), method)
		require.NotNil(t, got.GetDialog(), method)
		assert.Equal(t, "Approve?", got.GetDialog().GetTitle(), method)
	}
}

func TestReadControlRequest_ASelectAsksItsTitleWithItsOptions(t *testing.T) {
	got := readPiPrompt(t, map[string]any{
		"type": "extension_ui_request", "id": "req-1", "method": "select", "title": "Allow dangerous command?", "options": []any{"Allow", "Block"},
	}, nil)
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{
		Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
			Id: "req-1", Question: "Allow dangerous command?",
			Options: []*leapmuxv1.QuestionOption{{Label: "Allow"}, {Label: "Block"}},
		}}}},
		EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER,
	}, got)
	assert.Nil(t, got.GetQuestion().GetQuestions()[0].GetOptions()[0].Value, "an option with no value sends its label")
}

// An extension's select stays a question even when its words look like a
// permission: Pi states no permission, and the options are the extension's
// own answers.
func TestReadControlRequest_AnExtensionSelectIsAQuestionAndNoPermission(t *testing.T) {
	got := readPiPrompt(t, map[string]any{
		"type": "extension_ui_request", "id": "extension-select", "method": "select",
		"title":   "MCP: probe wants to run write\n\nArguments:\n{}",
		"options": []any{"Allow once", "Allow for session", "Deny"},
	}, nil)
	require.NotNil(t, got.GetQuestion())
	var labels []string
	for _, option := range got.GetQuestion().GetQuestions()[0].GetOptions() {
		labels = append(labels, option.GetLabel())
	}
	assert.Equal(t, []string{"Allow once", "Allow for session", "Deny"}, labels)
}

func TestReadControlRequest_ASelectWithNoTitleOrOptionsOfTheWrongType(t *testing.T) {
	question := func(payload map[string]any) *leapmuxv1.ControlQuestion {
		t.Helper()
		payload["type"] = "extension_ui_request"
		payload["method"] = "select"
		questions := readPiPrompt(t, payload, nil).GetQuestion().GetQuestions()
		require.Len(t, questions, 1)
		return questions[0]
	}
	labels := func(question *leapmuxv1.ControlQuestion) []string {
		labels := []string{}
		for _, option := range question.GetOptions() {
			labels = append(labels, option.GetLabel())
		}
		return labels
	}

	defaulted := question(map[string]any{"options": []any{"Yes"}})
	assert.Equal(t, "Choose an option", defaulted.GetQuestion())
	assert.Empty(t, defaulted.GetId())
	assert.Equal(t, []string{"Yes"}, labels(defaulted))

	assert.Empty(t, labels(question(map[string]any{"title": "Pick one"})), "a select with no options")
	assert.Empty(t, labels(question(map[string]any{"title": "Pick one", "options": "not an array"})), "options that are no list")
	assert.Equal(t, []string{"ok", "ok2"}, labels(question(map[string]any{
		"title": "Pick one", "options": []any{"ok", 42, nil, map[string]any{"label": "x"}, "ok2"},
	})), "an option that is no string is dropped")
	assert.Equal(t, []string{"Real", "Other"}, labels(question(map[string]any{
		"title": "Pick one", "options": []any{"Real", 42, nil, "Other"},
	})))
}

func TestReadControlRequest_AnInputIsAFreeTextQuestionThatAcceptsAnEmptyAnswer(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
		Id: "qi", Question: "Enter a value", AllowEmpty: true,
	}}}, readPiPrompt(t, map[string]any{"type": "extension_ui_request", "method": "input", "id": "qi"}, nil).GetQuestion())
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
		Id: "qi", Question: "Custom prompt", AllowEmpty: true,
	}}}, readPiPrompt(t, map[string]any{"type": "extension_ui_request", "method": "input", "id": "qi", "title": "Custom prompt"}, nil).GetQuestion())
}

// piQuestions keeps a free-text shape for a method that it does not know, so
// a method that Pi adds later still reads as a question. Only an input
// accepts an empty answer.
func TestPiQuestions_AnUnknownMethodTakesTheFreeTextShape(t *testing.T) {
	got := piQuestions(readPiControlPayload(piJSON(t, map[string]any{"method": "editor", "id": "qe", "title": "Edit"})), nil)
	require.Len(t, got, 1)
	agenttest.RequireProto(t, &leapmuxv1.ControlQuestion{Id: "qe", Question: "Edit"}, got[0])
}

// The dialog shows a short line for each option. The call's own arguments
// state the label, the description and the preview apart, and the answer
// still sends the dialog's line. The custom-answer line is no option: the
// typed text answers through it.
func TestReadControlRequest_ASourceBackedSelectStatesEachOptionApartFromItsLine(t *testing.T) {
	source := piAskSource(map[string]any{
		"question": "Choose",
		"options": []any{
			map[string]any{"label": "A", "description": "First", "preview": "A preview"},
			map[string]any{"label": "B", "description": "Second"},
		},
	})
	got := readPiPrompt(t, map[string]any{
		"type": "extension_ui_request", "method": "select", "id": "dialog", "title": "Choose",
		"options": []any{"1. A — First", "2. B — Second", "3. Other"},
	}, source)
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
		Id: "dialog", Question: "Choose",
		Options: []*leapmuxv1.QuestionOption{
			{Label: "A", Description: "First", Preview: "A preview", Value: piStringPointer("1. A — First")},
			{Label: "B", Description: "Second", Value: piStringPointer("2. B — Second")},
		},
	}}}, got.GetQuestion())
}

// The dialog's title cuts a long preview. The source states it whole.
func TestReadControlRequest_ASourceBackedSelectKeepsTheCompletePreview(t *testing.T) {
	preview := "```txt\n" + strings.Repeat("preview line\n", 80) + "complete-preview-end\n```"
	source := piAskSource(map[string]any{
		"question": "Choose a layout", "header": "Layout",
		"options": []any{
			map[string]any{"label": "Compact", "description": "Small", "preview": preview},
			map[string]any{"label": "Wide", "description": "Large"},
		},
	})
	got := readPiPrompt(t, map[string]any{
		"type": "extension_ui_request", "id": "dialog", "method": "select",
		"title":   "[Layout] Choose a layout\n\n--- 1. Compact preview ---\n" + preview[:600],
		"options": []any{"1. Compact — Small", "2. Wide — Large", "3. Type something."},
	}, source)
	questions := got.GetQuestion().GetQuestions()
	require.Len(t, questions, 1)
	assert.Equal(t, "Choose a layout", questions[0].GetQuestion())
	assert.Equal(t, "Layout", questions[0].GetHeader())
	require.Len(t, questions[0].GetOptions(), 2)
	assert.Equal(t, preview, questions[0].GetOptions()[0].GetPreview())
	assert.True(t, strings.HasSuffix(questions[0].GetOptions()[0].GetPreview(), "complete-preview-end\n```"))
}

func TestReadControlRequest_ASourceBackedMultipleChoiceTakesTheOptionNumbers(t *testing.T) {
	source := piAskSource(map[string]any{
		"question": "Choose", "multiSelect": true,
		"options": []any{
			map[string]any{"label": "A", "description": "first", "preview": "A preview"},
			map[string]any{"label": "B", "description": "second", "preview": "B preview"},
		},
	})
	got := readPiPrompt(t, map[string]any{
		"type": "extension_ui_request", "id": "dialog", "method": "input",
		"title":       "Choose\n\n1. A — first\n2. B — second\n\nChoose all that apply",
		"placeholder": contracts.PiQuestionDialogMultiSelectPlaceholder,
	}, source)
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
		Id: "dialog", Question: "Choose", MultiSelect: true, AllowEmpty: true,
		Options: []*leapmuxv1.QuestionOption{
			{Label: "A", Description: "first", Preview: "A preview", Value: piStringPointer("1")},
			{Label: "B", Description: "second", Preview: "B preview", Value: piStringPointer("2")},
		},
	}}}, got.GetQuestion())
}

// rpiv asks a custom answer to a single-choice question through an input
// that states the question again, so the question reads as its source
// title, with the header.
func TestReadControlRequest_ACustomAnswerInputAsksTheSourceTitle(t *testing.T) {
	source := piAskSource(map[string]any{
		"question": "Choose a layout", "header": "Layout",
		"options": []any{map[string]any{"label": "Compact", "description": "Small"}},
	})
	got := readPiPrompt(t, map[string]any{
		"type": "extension_ui_request", "id": "custom", "method": "input",
		"title": "[Layout] Choose a layout\n\nType your answer:", "placeholder": "",
	}, source)
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
		Id: "custom", Question: "[Layout] Choose a layout", AllowEmpty: true,
	}}}, got.GetQuestion())
}

func TestReadControlRequest_ASourceOfAnotherCallLeavesTheDialogAsItIs(t *testing.T) {
	payload := map[string]any{
		"type": "extension_ui_request", "id": "dialog", "method": "select", "title": "Choose",
		"options": []any{"1. A — first", "2. Other"},
	}
	question := map[string]any{"question": "Choose", "options": []any{map[string]any{"label": "A", "description": "first"}}}
	other := piAskSource(question)
	other["toolName"] = "read"
	ended := piAskSource(question)
	ended["type"] = "tool_execution_end"
	for name, source := range map[string]map[string]any{"another tool": other, "an end frame": ended, "no source": nil} {
		got := readPiPrompt(t, payload, source).GetQuestion().GetQuestions()
		require.Len(t, got, 1, name)
		require.Len(t, got[0].GetOptions(), 2, name)
		assert.Equal(t, "1. A — first", got[0].GetOptions()[0].GetLabel(), name)
		assert.Nil(t, got[0].GetOptions()[0].Value, name)
	}

	input := agent.NewControlRequestInput(piJSON(t, payload))
	input.Source = func() (agent.MessageContent, bool) { return agent.MessageContent{Original: []byte("not json")}, true }
	got := piProvider{}.ReadControlRequest(input).GetQuestion().GetQuestions()
	require.Len(t, got, 1)
	assert.Equal(t, "1. A — first", got[0].GetOptions()[0].GetLabel(), "a source row that is no JSON states nothing")

	input.Source = nil
	assert.NotNil(t, piProvider{}.ReadControlRequest(input).GetQuestion(), "an input with no source loader reads the dialog alone")
}

// This test replays every case of the fixture through piQuestionFromSource.
// The fixture states the question that a source supplies to one dialog, and
// control_source_test.go replays it too.
func TestPiQuestionFromSourceConformance(t *testing.T) {
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "pi_question_source_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name          string          `json:"name"`
			Dialog        json.RawMessage `json:"dialog"`
			Args          json.RawMessage `json:"args"`
			ExpectedIndex *int            `json:"expectedIndex"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, item := range fixture.Cases {
		t.Run(item.Name, func(t *testing.T) {
			frame := map[string]json.RawMessage{
				"type":       piJSON(t, "tool_execution_start"),
				"toolName":   piJSON(t, "ask_user_question"),
				"toolCallId": piJSON(t, "question"),
				"args":       item.Args,
			}
			got := piQuestionFromSource(readPiControlPayload(item.Dialog), frame)
			if item.ExpectedIndex == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *item.ExpectedIndex, got.index)
		})
	}
}

// A question that the dialog cannot have asked is skipped, and a later
// question of the same call can still match.
func TestPiQuestionFromSource_SkipsAQuestionThatTheDialogCannotHaveAsked(t *testing.T) {
	dialog := readPiControlPayload(piJSON(t, map[string]any{
		"method": "select", "title": "Choose", "options": []any{"1. A — first", "2. Other"},
	}))
	matching := map[string]any{"question": "Choose", "options": []any{map[string]any{"label": "A", "description": "first"}}}
	for name, skipped := range map[string]any{
		"an option with no description": map[string]any{"question": "Choose", "options": []any{map[string]any{"label": "A"}}},
		"an option that is no object":   map[string]any{"question": "Choose", "options": []any{"A — first"}},
		"no question text":              map[string]any{"question": "", "options": []any{map[string]any{"label": "A", "description": "first"}}},
		"a question that is no text":    map[string]any{"question": 7, "options": []any{map[string]any{"label": "A", "description": "first"}}},
		"no options":                    map[string]any{"question": "Choose", "options": []any{}},
		"a question that is no object":  "Choose",
	} {
		frame := map[string]json.RawMessage{}
		for key, value := range piAskSource(skipped, matching) {
			frame[key] = piJSON(t, value)
		}
		got := piQuestionFromSource(dialog, frame)
		require.NotNil(t, got, name)
		assert.Equal(t, 1, got.index, name)
	}
}

// TestPiPlanControlConformance replays testdata/pi_plan_control_conformance.json,
// the fixture of plan menus, through the Worker's reader of a plan approval.
func TestPiPlanControlConformance(t *testing.T) {
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "pi_plan_control_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name     string          `json:"name"`
			Payload  json.RawMessage `json:"payload"`
			Expected bool            `json:"expected"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, item := range fixture.Cases {
		t.Run(item.Name, func(t *testing.T) {
			assert.Equal(t, item.Expected, readPiControlPayload(item.Payload).isPlanApproval())
			prompt := agenttest.ControlPromptOf(piProvider{}, item.Payload)
			assert.Equal(t, item.Expected, prompt.GetPlan() != nil)
		})
	}
}

func TestReadControlRequest_ThePlanMenuIsAPlanWithItsDetailsAndItsOwnChoices(t *testing.T) {
	got := readPiPrompt(t, piPlanMenu("Proposed plan ready. What next?\nPlan details belong in the transcript.\n  Plan reinjection: on  \nModel: current\nModel: later", piPlanOptions), nil)
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{
		Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
			Details: []string{"Model: current", "Plan reinjection: on"},
			Choices: []*leapmuxv1.PlanChoice{
				{Id: "Export plan…", Label: "Export plan…", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_OTHER},
				{Id: "Save for later", Label: "Save for later", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_OTHER},
				{Id: "Discard plan and exit", Label: "Discard plan and exit", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_OTHER},
			},
		}},
		EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK,
	}, got)

	plain := readPiPrompt(t, piPlanMenu("Proposed plan ready. What next?", []any{"Implement here", "Start fresh and implement", "Stay in Plan mode"}), nil)
	require.NotNil(t, plain.GetPlan())
	assert.Empty(t, plain.GetPlan().GetDetails())
	assert.Empty(t, plain.GetPlan().GetChoices(), "the three plan actions are the approval and the refusal, not choices")
}

func TestReadControlRequest_ADialogStatesItsVariantAndItsFields(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{
		Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{
			Title: "Continue?", Message: "About to delete files.", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM,
		}},
		EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE,
	}, readPiPrompt(t, map[string]any{"type": "extension_ui_request", "id": "req-c", "method": "confirm", "title": "Continue?", "message": "About to delete files."}, nil))

	agenttest.RequireProto(t, &leapmuxv1.DialogPrompt{
		Title: "Edit text", Prefill: "  first\n  second\n", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR,
	}, readPiPrompt(t, map[string]any{"type": "extension_ui_request", "id": "req-e", "method": "editor", "title": "Edit text", "prefill": "  first\n  second\n"}, nil).GetDialog())

	// A frame that is no extension_ui_request is no question, so its input
	// draws as an input dialog with its hint.
	agenttest.RequireProto(t, &leapmuxv1.DialogPrompt{
		Title: "Pick a branch", Placeholder: "main", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT,
	}, readPiPrompt(t, map[string]any{"method": "input", "title": "Pick a branch", "placeholder": "main"}, nil).GetDialog())

	assert.Equal(t, "Approval Required", readPiPrompt(t, map[string]any{"type": "extension_ui_request", "id": "req-n", "method": "confirm"}, nil).GetDialog().GetTitle())

	textless := readPiPrompt(t, map[string]any{"type": "extension_ui_request", "method": "editor", "title": 7, "message": 3, "placeholder": nil, "prefill": []any{"x"}}, nil).GetDialog()
	agenttest.RequireProto(t, &leapmuxv1.DialogPrompt{Title: "Approval Required", Variant: leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR}, textless)
}

// Pi may add a dialog method that LeapMux does not know. It reads as a
// confirm, so the user can answer it, and the agent never waits on a dialog
// with no way out.
func TestReadControlRequest_AnUnknownMethodReadsAsAConfirm(t *testing.T) {
	for _, payload := range []map[string]any{
		{"type": "extension_ui_request", "id": "req-u", "method": "something_pi_added_later", "title": "Heads up"},
		{"type": "something", "title": "Heads up"},
		{"title": "Heads up"},
	} {
		got := readPiPrompt(t, payload, nil).GetDialog()
		require.NotNil(t, got, payload)
		assert.Equal(t, leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM, got.GetVariant(), payload)
		assert.Equal(t, "Heads up", got.GetTitle(), payload)
	}
	got := agenttest.ControlPromptOf(piProvider{}, []byte("not json"))
	require.NotNil(t, got.GetDialog(), "a payload that is no JSON still reads as a dialog the user can answer")
	assert.Equal(t, "Approval Required", got.GetDialog().GetTitle())
}

func TestReadControlRequest_ADialogStatesADeadlineOnlyWhenPiSetsOne(t *testing.T) {
	timeout := func(value any) *int64 {
		payload := map[string]any{"type": "extension_ui_request", "id": "req-t", "method": "confirm", "title": "T"}
		if value != nil {
			payload["timeout"] = value
		}
		return readPiPrompt(t, payload, nil).GetDialog().TimeoutMs
	}
	require.NotNil(t, timeout(30000))
	assert.EqualValues(t, 30000, *timeout(30000))
	for _, value := range []any{nil, 0, -1, "soon"} {
		assert.Nil(t, timeout(value), "%v states no deadline", value)
	}
}

func TestAnswerControlRequest_ASelectAnswersWithTheChosenOptionOrTheTypedText(t *testing.T) {
	payload := map[string]any{"type": "extension_ui_request", "id": "req-1", "method": "select", "title": "Allow dangerous command?", "options": []any{"Allow", "Block"}}
	for name, tc := range map[string]struct {
		answer *leapmuxv1.QuestionAnswer
		want   string
	}{
		"an option":                   {&leapmuxv1.QuestionAnswer{Selected: []string{"Block"}}, `{"type":"extension_ui_response","id":"req-1","value":"Block"}`},
		"an option beside blank text": {&leapmuxv1.QuestionAnswer{Selected: []string{"Block"}, CustomText: " \n"}, `{"type":"extension_ui_response","id":"req-1","value":"Block"}`},
		"typed text":                  {&leapmuxv1.QuestionAnswer{CustomText: "Allow"}, `{"type":"extension_ui_response","id":"req-1","value":"Allow"}`},
		"trimmed typed text":          {&leapmuxv1.QuestionAnswer{CustomText: " other "}, `{"type":"extension_ui_response","id":"req-1","value":"other"}`},
	} {
		decision := agenttest.QuestionDecision()
		if tc.answer != nil {
			decision = agenttest.QuestionDecision(tc.answer)
		}
		got, err := answerPi(t, payload, nil, decision)
		require.NoError(t, err, name)
		assert.JSONEq(t, tc.want, string(got.Content), name)
		assert.Nil(t, got.PlanApproval, name)
		assert.Empty(t, got.FollowUp, name)
	}
	// A select takes no empty value, so the Worker refuses one: a user who
	// gives no answer cancels instead.
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an empty answer":  agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{}),
		"white space":      agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "  \n"}),
		"no answer at all": agenttest.QuestionDecision(),
	} {
		_, err := answerPi(t, payload, nil, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// Pi tells an empty answer apart from a cancellation, so an input sends its
// text, the empty text included. The text is trimmed, and white space alone
// is the empty answer.
func TestAnswerControlRequest_AnInputSendsItsTypedText(t *testing.T) {
	for name, tc := range map[string]struct {
		payload map[string]any
		text    string
		want    string
	}{
		"text with white space":          {map[string]any{"type": "extension_ui_request", "method": "input"}, " typed text  ", "typed text"},
		"text on several lines":          {map[string]any{"type": "extension_ui_request", "method": "input"}, "first\nsecond", "first\nsecond"},
		"the empty text":                 {map[string]any{"type": "extension_ui_request", "method": "input"}, "", ""},
		"white space alone":              {map[string]any{"type": "extension_ui_request", "method": "input"}, " \t", ""},
		"an empty multiple choice":       {map[string]any{"type": "extension_ui_request", "method": "input", "title": "Choose any options", "placeholder": contracts.PiQuestionDialogMultiSelectPlaceholder}, "", ""},
		"text that looks like an option": {map[string]any{"type": "extension_ui_request", "method": "input", "placeholder": contracts.PiQuestionDialogMultiSelectPlaceholder}, "1. A — custom text", "1. A — custom text"},
	} {
		got, err := answerPi(t, tc.payload, nil, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: tc.text}))
		require.NoError(t, err, name)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":`+string(piJSON(t, tc.want))+`}`, string(got.Content), name)
	}
	got, err := answerPi(t, map[string]any{"type": "extension_ui_request", "method": "input"}, nil, agenttest.QuestionDecision())
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":""}`, string(got.Content), "an unanswered input is the empty answer")
}

// rpiv's multiple-choice input reads "1,3; custom answer": the numbers of
// the chosen options, then the user's own text after "; ".
func TestAnswerControlRequest_AMultipleChoiceSendsTheNumbersAndTheTypedText(t *testing.T) {
	source := piAskSource(map[string]any{
		"question": "Choose", "multiSelect": true,
		"options": []any{
			map[string]any{"label": "A", "description": "first"},
			map[string]any{"label": "B", "description": "second"},
		},
	})
	payload := map[string]any{
		"type": "extension_ui_request", "id": "dialog", "method": "input",
		"title":       "Choose\n\n1. A — first\n2. B — second\n\nChoose all that apply",
		"placeholder": contracts.PiQuestionDialogMultiSelectPlaceholder,
	}
	for name, tc := range map[string]struct {
		answer *leapmuxv1.QuestionAnswer
		want   string
	}{
		"two options":                 {&leapmuxv1.QuestionAnswer{Selected: []string{"1", "2"}}, "1,2"},
		"two options and typed text":  {&leapmuxv1.QuestionAnswer{Selected: []string{"1", "2"}, CustomText: " Zig too "}, "1,2; Zig too"},
		"typed text alone":            {&leapmuxv1.QuestionAnswer{CustomText: "Zig"}, "; Zig"},
		"typed text like a number":    {&leapmuxv1.QuestionAnswer{CustomText: "2"}, "; 2"},
		"an option beside blank text": {&leapmuxv1.QuestionAnswer{Selected: []string{"2"}, CustomText: "  "}, "2"},
		"no answer":                   {&leapmuxv1.QuestionAnswer{}, ""},
	} {
		got, err := answerPi(t, payload, source, agenttest.QuestionDecision(tc.answer))
		require.NoError(t, err, name)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":`+string(piJSON(t, tc.want))+`}`, string(got.Content), name)
	}
}

func TestAnswerControlRequest_ASourceBackedSelectSendsTheOptionLine(t *testing.T) {
	single := piAskSource(map[string]any{
		"question": "Choose",
		"options": []any{
			map[string]any{"label": "A", "description": "first"},
			map[string]any{"label": "B", "description": "second"},
		},
	})
	got, err := answerPi(t, map[string]any{
		"type": "extension_ui_request", "id": "dialog", "method": "select", "title": "Choose",
		"options": []any{"1. A — first", "2. B — second", "3. Type something."},
	}, single, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"2. B — second"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":"2. B — second"}`, string(got.Content))
}

// Pi's cancellation carries no reason, so a refusal's feedback goes nowhere:
// the "User stopped" of the browser's Stop button reaches nobody either.
func TestAnswerControlRequest_ARefusedQuestionCancelsTheDialog(t *testing.T) {
	for _, method := range []string{"select", "input"} {
		got, err := answerPi(t, map[string]any{"type": "extension_ui_request", "id": "req-1", "method": method, "options": []any{"Allow", "Block"}}, nil,
			agenttest.CancelQuestions("User stopped"))
		require.NoError(t, err, method)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","cancelled":true}`, string(got.Content), method)
		assert.Empty(t, got.FollowUp, method)
	}
}

// The browser could send the numbers of a multiple choice that it chose
// while the source was loaded. The prompt that the Worker reads with no
// source offers no options, so such an answer does not answer it.
func TestAnswerControlRequest_RefusesAnAnswerThatThePromptDoesNotOffer(t *testing.T) {
	_, err := answerPi(t, map[string]any{"type": "extension_ui_request", "method": "input", "placeholder": contracts.PiQuestionDialogMultiSelectPlaceholder}, nil,
		agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"1", "2"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)

	selectPayload := map[string]any{"type": "extension_ui_request", "method": "select", "options": []any{"Allow", "Block"}}
	_, err = answerPi(t, selectPayload, nil, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Nope"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	_, err = answerPi(t, selectPayload, nil, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Allow", "Block"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a select takes one option")
	_, err = answerPi(t, selectPayload, nil, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Block"}, CustomText: "note"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a select takes one value, so an option and typed text cannot both answer it")
	_, err = answerPi(t, selectPayload, nil, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a question takes a question decision")
}

func TestAnswerControlRequest_ThePlanMenuApprovesHereOrInAFreshContext(t *testing.T) {
	for clearContext, value := range map[bool]string{false: "Implement here", true: "Start fresh and implement"} {
		got, err := answerPi(t, piPlanMenu("Proposed plan ready. What next?\nPlan details belong in the transcript.", append([]any{"Implementation options…"}, piPlanOptions...)), nil,
			agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ClearContext: clearContext}))
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":"`+value+`"}`, string(got.Content))
		assert.Nil(t, got.PlanApproval, "Pi starts the fresh session itself, so the answer carries no LeapMux settings")
		assert.Empty(t, got.FollowUp)
	}
}

// The menu takes no text. A refusal stays in plan mode, and the user's
// feedback follows the answer as the next message.
func TestAnswerControlRequest_APlanRefusalStaysInPlanModeAndSendsTheFeedbackAfter(t *testing.T) {
	payload := piPlanMenu("Proposed plan ready. What next?", []any{"Implement here", "Start fresh and implement", "Stay in Plan mode"})
	for feedback, followUp := range map[string]string{"": "", "Revise the second step.": "Revise the second step.", "  \n": "", " Keep it. ": " Keep it. "} {
		got, err := answerPi(t, payload, nil, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: feedback}))
		require.NoError(t, err, feedback)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":"Stay in Plan mode"}`, string(got.Content), feedback)
		assert.Equal(t, followUp, got.FollowUp, feedback)
		assert.Nil(t, got.PlanApproval, feedback)
	}
	_, err := answerPi(t, payload, nil, agenttest.PlanDecision(&leapmuxv1.PlanDecision{ClearContext: true, PermissionMode: "default"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a refusal applies no settings")
}

func TestAnswerControlRequest_APlanChoiceSendsItsOption(t *testing.T) {
	for _, choice := range []string{"Export plan…", "Save for later", "Discard plan and exit"} {
		got, err := answerPi(t, piPlanMenu("Proposed plan ready. What next?", piPlanOptions), nil, agenttest.PlanDecision(&leapmuxv1.PlanDecision{ChoiceId: choice}))
		require.NoError(t, err, choice)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":`+string(piJSON(t, choice))+`}`, string(got.Content), choice)
		assert.Empty(t, got.FollowUp, choice)
	}
}

func TestAnswerControlRequest_RefusesAPlanDecisionThatTheMenuCannotSend(t *testing.T) {
	payload := piPlanMenu("Proposed plan ready. What next?", piPlanOptions)
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an approval with feedback":        agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, Feedback: "but faster"}),
		"an approval with a mode":          agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: "acceptEdits"}),
		"a choice with feedback":           agenttest.PlanDecision(&leapmuxv1.PlanDecision{ChoiceId: "Save for later", Feedback: "later"}),
		"a plan action as a choice":        agenttest.PlanDecision(&leapmuxv1.PlanDecision{ChoiceId: "Stay in Plan mode"}),
		"a choice that the menu lacks":     agenttest.PlanDecision(&leapmuxv1.PlanDecision{ChoiceId: "Ship it"}),
		"a refusing choice as an approval": agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "Export plan…"}),
		"a permission decision":            agenttest.PermissionDecision("allow", ""),
	} {
		_, err := answerPi(t, payload, nil, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

func TestAnswerControlRequest_ADialogAnswersWithPiOwnEnvelopes(t *testing.T) {
	confirm := map[string]any{"type": "extension_ui_request", "id": "req-c", "method": "confirm", "title": "Continue?", "message": "About to delete files."}
	editor := map[string]any{"type": "extension_ui_request", "id": "req-e", "method": "editor", "title": "Edit text", "prefill": "draft"}
	unknown := map[string]any{"type": "extension_ui_request", "id": "req-u", "method": "futureMethod", "title": "Heads up"}
	input := map[string]any{"type": "something", "method": "input", "title": "Pick a branch", "prefill": "main"}
	for name, tc := range map[string]struct {
		payload  map[string]any
		decision *leapmuxv1.ControlDecision
		want     string
	}{
		"a confirm approved":              {confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, ""), `{"type":"extension_ui_response","id":"req-1","confirmed":true}`},
		"a confirm denied":                {confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE, ""), `{"type":"extension_ui_response","id":"req-1","confirmed":false}`},
		"a confirm dismissed":             {confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, ""), `{"type":"extension_ui_response","id":"req-1","cancelled":true}`},
		"an editor's text":                {editor, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "multiline\ntext\n"), `{"type":"extension_ui_response","id":"req-1","value":"multiline\ntext\n"}`},
		"an editor's empty text":          {editor, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, ""), `{"type":"extension_ui_response","id":"req-1","value":""}`},
		"an editor dismissed":             {editor, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, ""), `{"type":"extension_ui_response","id":"req-1","cancelled":true}`},
		"an input's prefill":              {input, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "main"), `{"type":"extension_ui_response","id":"req-1","value":"main"}`},
		"an unknown method approved":      {unknown, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, ""), `{"type":"extension_ui_response","id":"req-1","confirmed":true}`},
		"an unknown method denied":        {unknown, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE, ""), `{"type":"extension_ui_response","id":"req-1","confirmed":false}`},
		"an unknown method keeps Pi free": {unknown, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, ""), `{"type":"extension_ui_response","id":"req-1","cancelled":true}`},
	} {
		got, err := answerPi(t, tc.payload, nil, tc.decision)
		require.NoError(t, err, name)
		assert.JSONEq(t, tc.want, string(got.Content), name)
		assert.Nil(t, got.PlanApproval, name)
		assert.Empty(t, got.FollowUp, name)
	}
}

func TestAnswerControlRequest_RefusesADialogDecisionThatDoesNotFitTheDialog(t *testing.T) {
	confirm := map[string]any{"type": "extension_ui_request", "method": "confirm", "title": "Continue?"}
	editor := map[string]any{"type": "extension_ui_request", "method": "editor", "title": "Edit text"}
	for name, tc := range map[string]struct {
		payload  map[string]any
		decision *leapmuxv1.ControlDecision
	}{
		"a value for a confirm":     {confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "yes")},
		"a confirmation of an edit": {editor, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, "")},
		"a value with a refusal":    {confirm, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE, "no")},
		"a value with a dismissal":  {editor, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, "draft")},
		"no action":                 {editor, agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_UNSPECIFIED, "")},
		"a question decision":       {editor, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "x"})},
	} {
		_, err := answerPi(t, tc.payload, nil, tc.decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

func TestAnswerControlRequest_RefusesAPromptKindThatPiDoesNotState(t *testing.T) {
	input := agent.NewControlRequestInput(piJSON(t, map[string]any{"type": "extension_ui_request", "method": "confirm"}))
	_, err := piProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: "req-1", Prompt: agent.GenericPermissionPrompt(),
		Decision: agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// The answer is the bytes that the Worker's SendRawInput reads today. A
// custom answer to a source-backed select reaches Pi as rpiv's custom-answer
// option, and the typed text then answers the input that follows it.
func TestAnswerControlRequest_ACustomAnswerTakesTheSelectThenInputExchange(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	published := sink.PublishedControls()
	require.Len(t, published, 1)
	agenttest.AssertPublishedControlKeys(t, piProvider{}, sink)
	input := agent.NewControlRequestInput(published[0].Payload)
	frame := []byte(`{"type":"tool_execution_start","toolCallId":"question","toolName":"ask_user_question","args":{"questions":[{"question":"Choose","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}}`)
	input.Source = func() (agent.MessageContent, bool) { return agent.MessageContent{Original: frame}, true }
	prompt := piProvider{}.ReadControlRequest(input)
	require.Len(t, prompt.GetQuestion().GetQuestions(), 1)
	require.Len(t, prompt.GetQuestion().GetQuestions()[0].GetOptions(), 2, "the custom-answer line is no option")

	answer, err := piProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: published[0].RequestID, Prompt: prompt,
		Decision: agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "My custom answer"}),
	})
	require.NoError(t, err)
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: answer.Content}))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"select","value":"3. Type something."}`, output.String())
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`))
	frames := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, frames, 2)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"input","value":"My custom answer"}`, frames[1])
}

// An approval in a fresh context is the answer that makes the Worker answer
// Pi's settings dialog itself.
func TestAnswerControlRequest_AFreshApprovalReachesTheFreshImplementationPath(t *testing.T) {
	t.Parallel()
	a, _, _ := piQuestionResponseFixture()
	payload := piPlanMenu("Proposed plan ready. What next?", []any{"Implement here", "Start fresh and implement", "Stay in Plan mode"})
	answer, err := answerPi(t, payload, nil, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ClearContext: true}))
	require.NoError(t, err)
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: answer.Content}))
	a.Mu.Lock()
	pending := a.freshImplementationPending
	a.Mu.Unlock()
	assert.True(t, pending)
}

func TestPiDialogResponse_EncodesEachAnswerShape(t *testing.T) {
	for name, tc := range map[string]struct {
		response piDialogResponse
		want     string
	}{
		"a value":        {piValueResponse("req-1", "Allow"), `{"type":"extension_ui_response","id":"req-1","value":"Allow"}`},
		"an empty value": {piValueResponse("req-1", ""), `{"type":"extension_ui_response","id":"req-1","value":""}`},
		"a confirmation": {piConfirmResponse("req-2", true), `{"type":"extension_ui_response","id":"req-2","confirmed":true}`},
		"a denial":       {piConfirmResponse("req-2", false), `{"type":"extension_ui_response","id":"req-2","confirmed":false}`},
		"a cancellation": {piCancelResponse("req-3"), `{"type":"extension_ui_response","id":"req-3","cancelled":true}`},
	} {
		got, err := tc.response.answer()
		require.NoError(t, err, name)
		assert.JSONEq(t, tc.want, string(got.Content), name)
	}
}

func TestReadControlRequest_QuestionSourceFallsBackToInputField(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"type":"extension_ui_request","id":"select","method":"select","title":"[Mode] Choose","options":["1. A — first","2. B — second","3. Type something."]}`)
	frame := []byte(`{"type":"tool_execution_start","toolCallId":"q-1","toolName":"ask_user_question","input":{"questions":[{"question":"Choose","header":"Mode","options":[{"label":"A","description":"first","preview":"preview-a"},{"label":"B","description":"second"}]}]}}`)
	input := agent.NewControlRequestInput(payload)
	input.Source = func() (agent.MessageContent, bool) { return agent.MessageContent{Original: frame}, true }
	prompt := piProvider{}.ReadControlRequest(input)
	require.Len(t, prompt.GetQuestion().GetQuestions(), 1)
	q := prompt.GetQuestion().GetQuestions()[0]
	assert.Equal(t, "Mode", q.GetHeader())
	require.Len(t, q.GetOptions(), 2)
	assert.Equal(t, "preview-a", q.GetOptions()[0].GetPreview())
}

func TestAnswerControlRequest_UsesJavaScriptWhitespaceForNELAndBOM(t *testing.T) {
	t.Parallel()
	// U+0085 (NEL) is non-whitespace in JS trim; U+FEFF (BOM) is whitespace in JS trim.
	selectPayload := map[string]any{"type": "extension_ui_request", "id": "req-1", "method": "select", "title": "Pick", "options": []any{"A"}}
	nelAnswer, err := answerPi(t, selectPayload, nil, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "\u0085"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":"\u0085"}`, string(nelAnswer.Content))

	planPayload := piPlanMenu("Proposed plan ready. What next?", []any{"Implement here", "Start fresh and implement", "Stay in Plan mode"})
	planNEL, err := answerPi(t, planPayload, nil, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: false, Feedback: "\u0085"}))
	require.NoError(t, err)
	assert.Equal(t, "\u0085", planNEL.FollowUp)

	planBOM, err := answerPi(t, planPayload, nil, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: false, Feedback: "\uFEFF"}))
	require.NoError(t, err)
	assert.Empty(t, planBOM.FollowUp)
}

func TestPiStrictStringOptionsAndDialogView(t *testing.T) {
	t.Parallel()

	t.Run("strictStringOptions", func(t *testing.T) {
		t.Parallel()
		opts, ok := strictStringOptions(json.RawMessage(`["A","B"]`))
		assert.True(t, ok)
		assert.Equal(t, []string{"A", "B"}, opts)

		opts, ok = strictStringOptions(json.RawMessage(`[]`))
		assert.True(t, ok)
		assert.Empty(t, opts)

		opts, ok = strictStringOptions(json.RawMessage(`null`))
		assert.True(t, ok)
		assert.Empty(t, opts)

		for name, raw := range map[string]string{
			"non-string element": `["A", 42]`,
			"null element":       `["A", null]`,
			"string non-array":   `"bad"`,
			"object non-array":   `{}`,
			"absent":             ``,
		} {
			opts, ok := strictStringOptions(json.RawMessage(raw))
			assert.False(t, ok, name)
			assert.Nil(t, opts, name)
		}
	})

	t.Run("dialogView", func(t *testing.T) {
		t.Parallel()
		withNonString := readPiControlPayload([]byte(`{"type":"extension_ui_request","method":"select","title":"Pick","options":["A",42]}`)).dialogView()
		assert.False(t, withNonString.optionsOK)
		assert.Empty(t, withNonString.options)

		for name, optionsJSON := range map[string]string{
			"string non-array": `"bad"`,
			"object non-array": `{}`,
			"null":             `null`,
			"empty array":      `[]`,
		} {
			view := readPiControlPayload([]byte(`{"type":"extension_ui_request","method":"select","title":"Pick","options":` + optionsJSON + `}`)).dialogView()
			assert.True(t, view.optionsOK, name)
			assert.Empty(t, view.options, name)
		}
	})
}

func TestPiPlanOptionsValidation(t *testing.T) {
	t.Parallel()

	whitespace := readPiControlPayload(piJSON(t, piPlanMenu("Proposed plan ready. What next?", []any{
		"Implement here", "Start fresh and implement", "Stay in Plan mode", "  \t\n ",
	})))
	_, ok := whitespace.planOptions()
	assert.False(t, ok, "whitespace-only option must be rejected")
	assert.False(t, whitespace.isPlanApproval())

	absent := readPiControlPayload([]byte(`{"type":"extension_ui_request","id":"plan","method":"select","title":"Proposed plan ready. What next?"}`))
	_, ok = absent.planOptions()
	assert.False(t, ok, "absent options must be rejected")
	assert.False(t, absent.isPlanApproval())

	nullOpts := readPiControlPayload([]byte(`{"type":"extension_ui_request","id":"plan","method":"select","title":"Proposed plan ready. What next?","options":null}`))
	opts, _ := nullOpts.planOptions()
	assert.Empty(t, opts)
	assert.False(t, nullOpts.isPlanApproval(), "null options must not match plan approval")
}

func TestReadControlRequest_QuestionSourceFallbackOnMissingOrNonObjectSource(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"extension_ui_request","id":"select","method":"select","title":"[Mode] Choose","options":["1. A — first","2. B — second","3. Type something."]}`)

	for name, source := range map[string]func() (agent.MessageContent, bool){
		"source returns false": func() (agent.MessageContent, bool) {
			return agent.MessageContent{Original: []byte(`{"type":"tool_execution_start"}`)}, false
		},
		"array json": func() (agent.MessageContent, bool) {
			return agent.MessageContent{Original: []byte(`[{"type":"tool_execution_start"}]`)}, true
		},
		"string json": func() (agent.MessageContent, bool) {
			return agent.MessageContent{Original: []byte(`"not-an-object"`)}, true
		},
		"malformed json": func() (agent.MessageContent, bool) {
			return agent.MessageContent{Original: []byte(`{bad`)}, true
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := agent.NewControlRequestInput(payload)
			input.Source = source
			prompt := piProvider{}.ReadControlRequest(input)
			require.Len(t, prompt.GetQuestion().GetQuestions(), 1)
			q := prompt.GetQuestion().GetQuestions()[0]
			assert.Equal(t, "[Mode] Choose", q.GetQuestion(), "must fall back to dialog title")
			assert.Empty(t, q.GetHeader())
			require.Len(t, q.GetOptions(), 3, "dialog-only fallback retains all three raw select options")
			assert.Equal(t, "1. A — first", q.GetOptions()[0].GetLabel())
		})
	}
}

func TestPiQuestionFromSource_ArgsAndInputPrecedence(t *testing.T) {
	t.Parallel()

	dialog := readPiControlPayload([]byte(`{"type":"extension_ui_request","id":"select","method":"select","title":"[Mode] Choose","options":["1. A — first","2. Type something."]}`))

	t.Run("args wins over input when both are present", func(t *testing.T) {
		t.Parallel()
		frame := map[string]json.RawMessage{
			"type":     json.RawMessage(`"tool_execution_start"`),
			"toolName": json.RawMessage(`"ask_user_question"`),
			"args":     json.RawMessage(`{"questions":[{"question":"Choose","header":"Mode","options":[{"label":"A","description":"first","preview":"from-args"}]}]}`),
			"input":    json.RawMessage(`{"questions":[{"question":"Choose","header":"Mode","options":[{"label":"A","description":"first","preview":"from-input"}]}]}`),
		}
		got := piQuestionFromSource(dialog, frame)
		require.NotNil(t, got)
		require.Len(t, got.options, 1)
		assert.Equal(t, "from-args", got.options[0].preview)
	})

	t.Run("explicit null args does not fall back to input", func(t *testing.T) {
		t.Parallel()
		frame := map[string]json.RawMessage{
			"type":     json.RawMessage(`"tool_execution_start"`),
			"toolName": json.RawMessage(`"ask_user_question"`),
			"args":     json.RawMessage(`null`),
			"input":    json.RawMessage(`{"questions":[{"question":"Choose","header":"Mode","options":[{"label":"A","description":"first"}]}]}`),
		}
		assert.Nil(t, piQuestionFromSource(dialog, frame))
	})

	t.Run("neither args nor input returns nil", func(t *testing.T) {
		t.Parallel()
		frame := map[string]json.RawMessage{
			"type":     json.RawMessage(`"tool_execution_start"`),
			"toolName": json.RawMessage(`"ask_user_question"`),
		}
		assert.Nil(t, piQuestionFromSource(dialog, frame))
	})
}

func TestAnswerControlRequest_SelectWhitespaceCancelsAndRejectsBlankCustomText(t *testing.T) {
	t.Parallel()

	selectPayload := map[string]any{
		"type":    "extension_ui_request",
		"id":      "req-1",
		"method":  "select",
		"title":   "Pick",
		"options": []any{"\uFEFF", " ", "\u0085"},
	}

	for name, option := range map[string]string{
		"BOM option":   "\uFEFF",
		"space option": " ",
	} {
		t.Run(name+" cancels select dialog", func(t *testing.T) {
			t.Parallel()
			got, err := answerPi(t, selectPayload, nil, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{option}}))
			require.NoError(t, err)
			assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","cancelled":true}`, string(got.Content))
		})
	}

	t.Run("NEL option sends value", func(t *testing.T) {
		t.Parallel()
		got, err := answerPi(t, selectPayload, nil, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"\u0085"}}))
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"extension_ui_response","id":"req-1","value":"\u0085"}`, string(got.Content))
	})

	t.Run("BOM custom text is rejected as invalid decision", func(t *testing.T) {
		t.Parallel()
		_, err := answerPi(t, selectPayload, nil, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "\uFEFF"}))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	})
}
