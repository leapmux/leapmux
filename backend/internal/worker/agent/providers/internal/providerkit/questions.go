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
	return QuestionsFromWireWith(raw, nil)
}

// QuestionsFromWireWith decodes a JSON array of wire questions and optionally lets
// the caller inspect or adjust each decoded question from its raw fields in the
// same pass, keeping question and option filtering index-aligned with QuestionsFromWire.
func QuestionsFromWireWith(raw json.RawMessage, adjust func(fields map[string]json.RawMessage, question *leapmuxv1.ControlQuestion)) []*leapmuxv1.ControlQuestion {
	elements, ok := RawArray(raw)
	if !ok {
		return nil
	}
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(elements))
	for _, element := range elements {
		fields, ok := RawObject(element)
		if !ok {
			continue
		}
		question := &leapmuxv1.ControlQuestion{
			Id:          ObjectString(fields, "id"),
			Header:      ObjectString(fields, "header"),
			Question:    ObjectString(fields, "question"),
			Options:     questionOptionsFromWire(fields["options"]),
			MultiSelect: RawBool(fields["multiSelect"]),
			AllowEmpty:  RawBool(fields["allowEmpty"]),
		}
		question.AllowCustomText = RawOptionalBool(fields["allowCustomText"])
		if minimum, ok := RawWholeNumber(fields["minimumSelections"]); ok && minimum >= 0 {
			question.MinimumSelections = nonNegativeInt32Of(minimum)
		}
		if maximum, ok := RawWholeNumber(fields["maximumSelections"]); ok && maximum >= 0 {
			question.MaximumSelections = nonNegativeInt32Of(maximum)
		}
		if adjust != nil {
			adjust(fields, question)
		}
		questions = append(questions, question)
	}
	return questions
}

// questionOptionsFromWire reads the options of one wire question. An element
// that is no object is dropped.
func questionOptionsFromWire(raw json.RawMessage) []*leapmuxv1.QuestionOption {
	elements, ok := RawArray(raw)
	if !ok {
		return nil
	}
	options := make([]*leapmuxv1.QuestionOption, 0, len(elements))
	for _, element := range elements {
		fields, ok := RawObject(element)
		if !ok {
			continue
		}
		option := &leapmuxv1.QuestionOption{
			Label:       ObjectString(fields, "label"),
			Description: ObjectString(fields, "description"),
			Preview:     ObjectString(fields, "preview"),
		}
		if value, ok := RawString(fields["value"]); ok {
			option.Value = &value
		}
		options = append(options, option)
	}
	return options
}

// nonNegativeInt32Of narrows a non-negative count to int32, capping at math.MaxInt32.
func nonNegativeInt32Of(value int64) *int32 {
	narrowed := int32(min(math.MaxInt32, value))
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
	fields, ok := RawObject(toolInput)
	if !ok {
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
