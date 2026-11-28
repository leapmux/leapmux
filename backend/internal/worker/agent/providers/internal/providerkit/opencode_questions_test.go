package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// openCodeQuestionEvent is a stored `question.asked` event whose properties
// are properties.
func openCodeQuestionEvent(t *testing.T, properties any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"type": "question.asked", "properties": properties})
	require.NoError(t, err)
	return payload
}

// openCodeQuestionsOf reads the questions of payload.
func openCodeQuestionsOf(t *testing.T, payload []byte) []*leapmuxv1.ControlQuestion {
	t.Helper()
	prompt := OpenCodeQuestionPrompt(payload).GetQuestion()
	require.NotNil(t, prompt, "a question event reads as a question prompt")
	return prompt.GetQuestions()
}

func requireOpenCodeQuestions(t *testing.T, want, got []*leapmuxv1.ControlQuestion) {
	t.Helper()
	agenttest.RequireProto(t, &leapmuxv1.QuestionPrompt{Questions: want}, &leapmuxv1.QuestionPrompt{Questions: got}, "questions mismatch")
}

func TestOpenCodeQuestionPrompt_ReadsTheQuestionsAndTheirOptions(t *testing.T) {
	prompt := OpenCodeQuestionPrompt(openCodeQuestionEvent(t, map[string]any{
		"id": "que_1", "sessionID": "ses_9",
		"questions": []any{map[string]any{
			"question": "Which one?", "header": "Task", "multiple": true,
			"options": []any{map[string]any{"label": "Inspect", "description": "Look only"}},
		}},
	})).GetQuestion()
	require.NotNil(t, prompt)
	assert.False(t, prompt.GetPreservesSelectionNotes(), "an option selection and typed text replace each other")
	requireOpenCodeQuestions(t, []*leapmuxv1.ControlQuestion{{
		Question: "Which one?", Header: "Task", MultiSelect: true,
		Options: []*leapmuxv1.QuestionOption{{Label: "Inspect", Description: "Look only"}},
	}}, prompt.GetQuestions())
}

// The one upstream guard tests `type`, which says nothing about
// `properties`, so a list of the wrong shape reads as no question.
func TestOpenCodeQuestionPrompt_ReadsNoQuestionFromAMalformedList(t *testing.T) {
	for name, questions := range map[string]any{
		"a string":  "Which parser?",
		"a number":  7,
		"an object": map[string]any{"question": "Which parser?"},
		"a boolean": true,
	} {
		assert.Empty(t, openCodeQuestionsOf(t, openCodeQuestionEvent(t, map[string]any{"questions": questions})), name)
	}
	for _, payload := range []string{
		`{"type":"question.asked"}`,
		`{"type":"question.asked","properties":"questions"}`,
		`{"type":"question.asked","properties":{}}`,
	} {
		assert.Empty(t, openCodeQuestionsOf(t, []byte(payload)), payload)
	}
}

// An element that the dialog cannot draw is dropped, and the others stay.
func TestOpenCodeQuestionPrompt_DropsAnElementThatIsNoObject(t *testing.T) {
	questions := openCodeQuestionsOf(t, openCodeQuestionEvent(t, map[string]any{"questions": []any{
		nil, "Which parser?", 42, map[string]any{"question": "Which parser?", "options": []any{}},
	}}))
	requireOpenCodeQuestions(t, []*leapmuxv1.ControlQuestion{{Question: "Which parser?"}}, questions)
}

// A question that states no options is a free-text question, which is a
// real one.
func TestOpenCodeQuestionPrompt_KeepsAQuestionWithNoOptions(t *testing.T) {
	questions := openCodeQuestionsOf(t, openCodeQuestionEvent(t, map[string]any{"questions": []any{
		map[string]any{"question": "Why?"},
		map[string]any{"question": "How?", "options": "many"},
	}}))
	requireOpenCodeQuestions(t, []*leapmuxv1.ControlQuestion{{Question: "Why?"}, {Question: "How?"}}, questions)
}

func TestOpenCodeQuestionPrompt_FoldsTheDaemonMultipleOntoMultiSelect(t *testing.T) {
	multiSelect := func(question map[string]any) bool {
		t.Helper()
		questions := openCodeQuestionsOf(t, openCodeQuestionEvent(t, map[string]any{"questions": []any{question}}))
		require.Len(t, questions, 1)
		return questions[0].GetMultiSelect()
	}
	assert.True(t, multiSelect(map[string]any{"question": "Which?", "options": []any{}, "multiple": true}))
	assert.False(t, multiSelect(map[string]any{"question": "Which?", "options": []any{}, "multiple": false}),
		"a false multiple states a single choice")
	assert.False(t, multiSelect(map[string]any{"question": "Which?", "options": []any{}, "multiSelect": false, "multiple": true}),
		"a stated multiSelect wins over multiple")
	assert.False(t, multiSelect(map[string]any{"question": "Which?", "options": []any{}, "multiSelect": nil, "multiple": true}),
		"a null multiSelect is stated, so multiple does not replace it")
	assert.False(t, multiSelect(map[string]any{"question": "Which?", "options": []any{}, "multiple": "yes"}),
		"a multiple that is no boolean states nothing")
	assert.True(t, multiSelect(map[string]any{"question": "Which?", "multiSelect": true}))
}

// The fold leaves every other field of a question as it is.
func TestOpenCodeQuestionPrompt_TheFoldKeepsTheOtherFields(t *testing.T) {
	questions := openCodeQuestionsOf(t, openCodeQuestionEvent(t, map[string]any{"questions": []any{
		"dropped",
		map[string]any{"id": "q1", "question": "Which?", "header": "Pick", "multiple": true, "allowEmpty": true,
			"options": []any{map[string]any{"label": "A", "value": "a"}}},
	}}))
	value := "a"
	requireOpenCodeQuestions(t, []*leapmuxv1.ControlQuestion{{
		Id: "q1", Question: "Which?", Header: "Pick", MultiSelect: true, AllowEmpty: true,
		Options: []*leapmuxv1.QuestionOption{{Label: "A", Value: &value}},
	}}, questions)
}

// answerOpenCodeQuestions answers the questions of payload with decision, as
// the request "que_1".
func answerOpenCodeQuestions(t *testing.T, payload []byte, decision *leapmuxv1.QuestionDecision) (agent.ControlAnswer, error) {
	t.Helper()
	input := agent.NewControlRequestInput(payload)
	return AnswerOpenCodeQuestions(agent.ControlAnswerInput{
		Request:   input,
		RequestID: "que_1",
		Prompt:    OpenCodeQuestionPrompt(payload),
		Decision:  &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Question{Question: decision}},
	})
}

// threeQuestions is a multiple-choice question, a free-text question and a
// single-choice question.
func threeQuestions(t *testing.T) []byte {
	t.Helper()
	return openCodeQuestionEvent(t, map[string]any{"questions": []any{
		map[string]any{"question": "Which database?", "multiple": true, "options": []any{
			map[string]any{"label": "SQLite"}, map[string]any{"label": "Postgres"},
		}},
		map[string]any{"question": "Anything else?", "options": []any{}},
		map[string]any{"question": "Which parser?", "options": []any{map[string]any{"label": "Pratt"}}},
	}})
}

func TestAnswerOpenCodeQuestions_AnswersEachQuestionInTheQuestionOrder(t *testing.T) {
	got, err := answerOpenCodeQuestions(t, threeQuestions(t), &leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{
		{Selected: []string{"SQLite", "Postgres"}},
		{CustomText: "  more tests  "},
		{Selected: []string{"Pratt"}},
	}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"que_1","result":{"answers":[["SQLite","Postgres"],["more tests"],["Pratt"]]}}`, string(got.Content),
		"each question takes its choices, or its typed words without surrounding white space")
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)
}

// A multiple-choice answer of options and typed text chose both, and the
// protocol has no note field, so the text is one more value of the answer.
// The browser sent the options alone; this differs from it on purpose.
func TestAnswerOpenCodeQuestions_SendsTypedTextAsOneMoreValueOfAMultipleChoice(t *testing.T) {
	got, err := answerOpenCodeQuestions(t, threeQuestions(t), &leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{
		{Selected: []string{"SQLite", "Postgres"}, CustomText: "  Oracle  "},
		{CustomText: "none"},
		{Selected: []string{"Pratt"}},
	}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"que_1","result":{"answers":[["SQLite","Postgres","Oracle"],["none"],["Pratt"]]}}`, string(got.Content))

	got, err = answerOpenCodeQuestions(t, threeQuestions(t), &leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{
		{Selected: []string{"SQLite"}, CustomText: "   "},
		{CustomText: "none"},
		{Selected: []string{"Pratt"}},
	}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"que_1","result":{"answers":[["SQLite"],["none"],["Pratt"]]}}`, string(got.Content),
		"text that is only white space is no value")
}

// The browser sends an answer only when every question has one, so the
// Worker refuses one that leaves a question empty or blank, and a request
// of no question takes a cancel alone.
func TestAnswerOpenCodeQuestions_RefusesAnAnswerThatLeavesAQuestionOpen(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.QuestionDecision{
		"an unanswered question": {Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"SQLite"}}, {}, {Selected: []string{"Pratt"}}}},
		"a blank question":       {Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"SQLite"}}, {CustomText: "   "}, {Selected: []string{"Pratt"}}}},
		"no answer at all":       {},
	} {
		_, err := answerOpenCodeQuestions(t, threeQuestions(t), decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
	_, err := answerOpenCodeQuestions(t, openCodeQuestionEvent(t, map[string]any{"questions": []any{}}), &leapmuxv1.QuestionDecision{})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a request of no question")
}

// The protocol's rejection carries no text, so the reader's words of a
// cancel reach nobody.
func TestAnswerOpenCodeQuestions_ACancelRejectsWithoutTheFeedback(t *testing.T) {
	for _, feedback := range []string{"", "not now"} {
		got, err := answerOpenCodeQuestions(t, threeQuestions(t), &leapmuxv1.QuestionDecision{Cancelled: true, Feedback: feedback})
		require.NoError(t, err, feedback)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"que_1","result":{"rejected":true}}`, string(got.Content), feedback)
		assert.Empty(t, got.FollowUp, feedback)
	}
}

func TestAnswerOpenCodeQuestions_RefusesADecisionThatDoesNotAnswerThePrompt(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.QuestionDecision{
		"an option the question does not offer":        {Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"Oracle"}}}},
		"two choices for a single choice":              {Answers: []*leapmuxv1.QuestionAnswer{{}, {}, {Selected: []string{"Pratt", "Pratt"}}}},
		"an option and typed text for a single choice": {Answers: []*leapmuxv1.QuestionAnswer{{}, {}, {Selected: []string{"Pratt"}, CustomText: "Earley"}}},
		"more answers than questions":                  {Answers: []*leapmuxv1.QuestionAnswer{{}, {}, {}, {}}},
		"a cancel with answers":                        {Cancelled: true, Answers: []*leapmuxv1.QuestionAnswer{{}}},
	} {
		_, err := answerOpenCodeQuestions(t, threeQuestions(t), decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}

	payload := threeQuestions(t)
	_, err := AnswerOpenCodeQuestions(agent.ControlAnswerInput{
		Request: agent.NewControlRequestInput(payload), RequestID: "que_1", Prompt: OpenCodeQuestionPrompt(payload),
		Decision: agenttest.PermissionDecision("once", ""),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a question takes a question decision")
}
