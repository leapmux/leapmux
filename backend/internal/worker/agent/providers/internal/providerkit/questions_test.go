package providerkit

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestQuestionsFromWire_ReadsEveryField(t *testing.T) {
	questions := QuestionsFromWire(json.RawMessage(`[{
		"id":"q1","header":"Pick","question":"Which?","multiSelect":true,"allowEmpty":true,
		"allowCustomText":false,"minimumSelections":1,"maximumSelections":2,
		"options":[{"label":"A","description":"first","preview":"a.go","value":"a"},{"label":"B"}]
	}]`))
	require.Len(t, questions, 1)
	q := questions[0]
	assert.Equal(t, "q1", q.GetId())
	assert.Equal(t, "Pick", q.GetHeader())
	assert.Equal(t, "Which?", q.GetQuestion())
	assert.True(t, q.GetMultiSelect())
	assert.True(t, q.GetAllowEmpty())
	require.NotNil(t, q.AllowCustomText)
	assert.False(t, q.GetAllowCustomText())
	assert.EqualValues(t, 1, q.GetMinimumSelections())
	assert.EqualValues(t, 2, q.GetMaximumSelections())
	require.Len(t, q.GetOptions(), 2)
	assert.Equal(t, "a", q.GetOptions()[0].GetValue())
	assert.Equal(t, "a.go", q.GetOptions()[0].GetPreview())
	assert.Nil(t, q.GetOptions()[1].Value, "an option with no value sends its label")
}

func TestQuestionsFromWire_ToleratesWhatItCannotDraw(t *testing.T) {
	questions := QuestionsFromWire(json.RawMessage(`[null,"text",7,{"question":"Free?"},{"question":5,"options":"none","allowCustomText":"yes","minimumSelections":"1"}]`))
	require.Len(t, questions, 2, "an element that is no object is dropped")
	assert.Equal(t, "Free?", questions[0].GetQuestion())
	assert.Empty(t, questions[0].GetOptions(), "a question with no options is a free-text question")
	assert.Empty(t, questions[1].GetQuestion(), "a value of the wrong type reads as absent")
	assert.Nil(t, questions[1].AllowCustomText)
	assert.Nil(t, questions[1].MinimumSelections)

	for _, raw := range []string{``, `null`, `{}`, `"text"`} {
		assert.Empty(t, QuestionsFromWire(json.RawMessage(raw)), raw)
	}
}

func TestQuestionsFromWire_IgnoresNegativeCountsAndSaturatesAtMaxInt32(t *testing.T) {
	questions := QuestionsFromWire(json.RawMessage(`[{"minimumSelections":-1,"maximumSelections":-9999999999},{"minimumSelections":0,"maximumSelections":9999999999}]`))
	require.Len(t, questions, 2)
	assert.Nil(t, questions[0].MinimumSelections)
	assert.Nil(t, questions[0].MaximumSelections)
	require.NotNil(t, questions[1].MinimumSelections)
	assert.EqualValues(t, 0, questions[1].GetMinimumSelections())
	assert.EqualValues(t, math.MaxInt32, questions[1].GetMaximumSelections())
}

func TestQuestionsFromWire_AcceptsWholeNumberFloats(t *testing.T) {
	questions := QuestionsFromWire(json.RawMessage(`[{"minimumSelections":1.0,"maximumSelections":2.0}]`))
	require.Len(t, questions, 1)
	require.NotNil(t, questions[0].MinimumSelections)
	require.NotNil(t, questions[0].MaximumSelections)
	assert.EqualValues(t, 1, questions[0].GetMinimumSelections())
	assert.EqualValues(t, 2, questions[0].GetMaximumSelections())

	fractional := QuestionsFromWire(json.RawMessage(`[{"minimumSelections":1.5,"maximumSelections":2.5}]`))
	require.Len(t, fractional, 1)
	assert.Nil(t, fractional[0].MinimumSelections)
	assert.Nil(t, fractional[0].MaximumSelections)
}

func TestQuestionsFromWireWith_VisitsEachParsedQuestionInIndexOrder(t *testing.T) {
	var visited []bool
	questions := QuestionsFromWireWith(json.RawMessage(`[null,{"question":"First","isOther":true,"options":[{"label":"A"}]},{"question":"Second","isOther":true,"options":[null]},{"question":"Third","isOther":false,"options":[{"label":"B"}]}]`), func(fields map[string]json.RawMessage, q *leapmuxv1.ControlQuestion) {
		visited = append(visited, RawBool(fields["isOther"]) && len(q.GetOptions()) > 0)
	})
	require.Len(t, questions, 3)
	assert.Equal(t, []bool{true, false, false}, visited)
}

func TestAskUserQuestionAnswers_KeepsTheToolInputBesideTheAnswers(t *testing.T) {
	input := agent.ControlAnswerInput{
		Request:   agent.NewControlRequestInput([]byte(`{}`)),
		RequestID: "req-1",
		Prompt: &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: []*leapmuxv1.ControlQuestion{{Question: "Color?", Options: []*leapmuxv1.QuestionOption{{Label: "Red"}}}},
		}}},
		Decision: agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}}),
	}
	got, err := AskUserQuestionAnswers(input, json.RawMessage(`{"questions":[1],"answers":{"stale":"x"}}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{"questions":[1],"answers":{"Color?":"Red"}}}}}`, string(got.Content),
		"the answers replace the input's own")

	got, err = AskUserQuestionAnswers(input, json.RawMessage(`"no object"`))
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), `"updatedInput":{"answers":{"Color?":"Red"}}`, "an input that is no object starts from an empty one")
}

// The user chose options AND typed an answer of a multiple-choice question,
// so the answer lists the typed text after the options. A question that
// permits an empty answer and has none answers "", and a question of no text
// and no header takes its index as its key.
func TestAskUserQuestionAnswers_ListsTheTypedTextOfAMultipleChoiceAfterItsOptions(t *testing.T) {
	input := agent.ControlAnswerInput{
		Request:   agent.NewControlRequestInput([]byte(`{}`)),
		RequestID: "req-1",
		Prompt: &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: []*leapmuxv1.ControlQuestion{
				{Question: "Extras?", MultiSelect: true, Options: []*leapmuxv1.QuestionOption{{Label: "Cache"}, {Label: "Logs"}}},
				{Header: "Name"},
				{Question: "Skip?", AllowEmpty: true},
				{},
			},
		}}},
		Decision: agenttest.QuestionDecision(
			&leapmuxv1.QuestionAnswer{Selected: []string{"Cache", "Logs"}, CustomText: " Metrics "},
			&leapmuxv1.QuestionAnswer{CustomText: "svc"},
			&leapmuxv1.QuestionAnswer{},
			&leapmuxv1.QuestionAnswer{CustomText: "x"},
		),
	}
	got, err := AskUserQuestionAnswers(input, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{"answers":{"Extras?":"Cache, Logs, Metrics","Name":"svc","Skip?":"","q3":"x"}}}}}`, string(got.Content))

	input.Decision = agenttest.CancelQuestions("Not now.")
	got, err = AskUserQuestionAnswers(input, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), `"behavior":"deny"`)
	assert.Contains(t, string(got.Content), `"message":"Not now."`)

	input.Decision = agenttest.PermissionDecision("allow", "")
	_, err = AskUserQuestionAnswers(input, json.RawMessage(`{}`))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestAllowWithToolAnswers(t *testing.T) {
	t.Parallel()
	got, err := AllowWithToolAnswers("req-1", json.RawMessage(`{"questions":[1],"answers":"old"}`), []map[string]string{{"id": "q1", "value": "A"}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{"questions":[1],"answers":[{"id":"q1","value":"A"}]}}}}`, string(got.Content))

	for _, raw := range []json.RawMessage{json.RawMessage(`null`), nil, json.RawMessage(`{malformed`)} {
		got, err = AllowWithToolAnswers("req-2", raw, map[string]string{"Q": "Yes"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-2","response":{"behavior":"allow","updatedInput":{"answers":{"Q":"Yes"}}}}}`, string(got.Content))
	}

	got, err = AllowWithToolAnswers("req-nil", json.RawMessage(`{"keep":true}`), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-nil","response":{"behavior":"allow","updatedInput":{"keep":true,"answers":null}}}}`, string(got.Content))

	_, err = AllowWithToolAnswers("req-3", json.RawMessage(`{}`), make(chan int))
	assert.Error(t, err)
}
