package providerkit

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// QuestionsFromWire reads a JSON array of questions in the AskUserQuestion
// shape that Claude Code defined and several runtimes adopted: each element
// states its question, header and options, and may state an id, a multiple
// selection, and the answers that it accepts.
//
// It keeps a question that states no option, because a free-text question
// is a real one. It drops an element that is no object, which no reader can
// draw. A value of the wrong type reads as absent. A raw value that is no
// array reads as no question.
func QuestionsFromWire(raw json.RawMessage) []*leapmuxv1.ControlQuestion {
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil {
		return nil
	}
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(elements))
	for _, element := range elements {
		var fields map[string]json.RawMessage
		if json.Unmarshal(element, &fields) != nil || fields == nil {
			continue
		}
		question := &leapmuxv1.ControlQuestion{
			Id:          stringField(fields, "id"),
			Header:      stringField(fields, "header"),
			Question:    stringField(fields, "question"),
			Options:     questionOptionsFromWire(fields["options"]),
			MultiSelect: RawBool(fields["multiSelect"]),
			AllowEmpty:  RawBool(fields["allowEmpty"]),
		}
		question.AllowCustomText = RawOptionalBool(fields["allowCustomText"])
		if minimum, ok := RawInt64(fields["minimumSelections"]); ok {
			question.MinimumSelections = int32Of(minimum)
		}
		if maximum, ok := RawInt64(fields["maximumSelections"]); ok {
			question.MaximumSelections = int32Of(maximum)
		}
		questions = append(questions, question)
	}
	return questions
}

// questionOptionsFromWire reads the options of one wire question. An element
// that is no object is dropped.
func questionOptionsFromWire(raw json.RawMessage) []*leapmuxv1.QuestionOption {
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil {
		return nil
	}
	options := make([]*leapmuxv1.QuestionOption, 0, len(elements))
	for _, element := range elements {
		var fields map[string]json.RawMessage
		if json.Unmarshal(element, &fields) != nil || fields == nil {
			continue
		}
		option := &leapmuxv1.QuestionOption{
			Label:       stringField(fields, "label"),
			Description: stringField(fields, "description"),
			Preview:     stringField(fields, "preview"),
		}
		if value, ok := RawString(fields["value"]); ok {
			option.Value = &value
		}
		options = append(options, option)
	}
	return options
}

func stringField(fields map[string]json.RawMessage, key string) string {
	value, _ := RawString(fields[key])
	return value
}

// int32Of narrows a count to int32, saturating at the type's limits.
func int32Of(value int64) *int32 {
	narrowed := int32(max(math.MinInt32, min(math.MaxInt32, value)))
	return &narrowed
}

// AskUserQuestionAnswers answers an AskUserQuestion prompt as the runtimes
// of Claude Code's shape read it: an allow whose updated input is the tool's
// own input with an `answers` object beside it. Each question's key is its
// question text, else its header, else "q<index>". Its value is the
// question's answer values (agent.QuestionAnswerValues) joined with ", ", so
// a multiple-choice answer lists the typed text after the selected options.
// A question with no value gets "" when it permits an empty answer, and no
// key otherwise.
//
// A cancelled decision denies with its feedback.
func AskUserQuestionAnswers(input agent.ControlAnswerInput, toolInput json.RawMessage) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	answers := map[string]string{}
	for i, question := range questions {
		key := question.GetQuestion()
		if key == "" {
			key = question.GetHeader()
		}
		if key == "" {
			key = fmt.Sprintf("q%d", i)
		}
		values := agent.QuestionAnswerValues(question, agent.QuestionAnswerAt(decision, i))
		switch {
		case len(values) > 0:
			answers[key] = strings.Join(values, ", ")
		case question.GetAllowEmpty():
			answers[key] = ""
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(toolInput, &fields) != nil || fields == nil {
		fields = map[string]json.RawMessage{}
	}
	encoded, err := json.Marshal(answers)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	fields["answers"] = encoded
	updated, err := json.Marshal(fields)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	return agent.ControlAnswer{Content: agent.AllowResponse(input.RequestID, updated)}, nil
}
