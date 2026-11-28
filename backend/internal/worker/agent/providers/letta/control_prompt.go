package letta

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// lettaToolTitleFallback is the title of a permission whose request states
// no tool name.
const lettaToolTitleFallback = "Tool"

// lettaControlPayload is a published control request, as the prompt and the
// answer read it. The Worker writes the payload itself (onControlRequest,
// postQuestionRequest), and its `type`, which kind holds, says which of the two
// requests it is.
type lettaControlPayload struct {
	kind       string
	requestID  string
	toolName   string
	toolCallID string
	toolInput  json.RawMessage
}

// readLettaControlPayload reads payload under the keys that the Worker writes.
// A field that is not text reads as empty, and a tool input that is not a JSON
// object reads as `{}`. It reports false when payload is not a JSON object, or
// when a text field holds a non-string, non-null value. The Worker writes no
// such payload, so an answer to it cannot be addressed.
func readLettaControlPayload(payload []byte) (lettaControlPayload, bool) {
	fields, ok := providerkit.RawObject(payload)
	if !ok {
		return lettaControlPayload{toolInput: json.RawMessage("{}")}, false
	}
	readable := true
	text := func(key string) string {
		value, ok := providerkit.RawString(fields[key])
		if !ok && providerkit.RawPresent(fields[key]) {
			readable = false
		}
		return value
	}
	// The calls of text set readable, so they run in a statement of their own: Go
	// does not order the read of a variable against a call in one return.
	read := lettaControlPayload{
		kind:       text(lettaControlFieldType),
		requestID:  text(lettaControlFieldRequestID),
		toolName:   text(contracts.LettaDeltaFieldToolName),
		toolCallID: text(contracts.LettaDeltaFieldToolCallID),
		toolInput:  providerkit.RawObjectOrEmpty(fields[contracts.LettaDeltaFieldToolInput]),
	}
	return read, readable
}

// ReadControlRequest reads the two requests that the Worker publishes for
// Letta Code:
//
//   - A question request (`ask_user`) states the questions of an
//     AskUserQuestion call that already returned its receipt.
//   - A permission request (`permission`) asks whether one tool call may run.
//     A call of the question tool is one of them: Strict mode asks it.
//
// Any other or unanswerable payload reads as unrecognized, matching the
// refusal rules of lettaResolveControlResponse.
func (lettaProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	payload, readable := readLettaControlPayload(input.Payload)
	if !readable {
		return agent.UnrecognizedControlPrompt()
	}
	switch payload.kind {
	case string(lettaControlAskUser):
		if payload.toolCallID == "" {
			return agent.UnrecognizedControlPrompt()
		}
		questions, ok := readLettaQuestions(providerkit.RawFields(payload.toolInput)[contracts.LettaQuestionFieldQuestions])
		if !ok {
			return agent.UnrecognizedControlPrompt()
		}
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: questions,
		}}}
	case string(lettaControlPermission):
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: lettaPermissionPrompt(payload)}}
	default:
		return agent.UnrecognizedControlPrompt()
	}
}

// lettaPermissionPrompt is the prompt of one permission request: the tool
// name as its title, the tool input, LeapMux's allow and deny, and for a Bash
// call the command it runs. The answer that the Worker sends
// (contracts.LettaApprovalDecision) states a behavior and a message alone, so
// the request's `suggestions` are not offered.
func lettaPermissionPrompt(payload lettaControlPayload) *leapmuxv1.PermissionPrompt {
	title := payload.toolName
	if title == "" {
		title = lettaToolTitleFallback
	}
	permission := &leapmuxv1.PermissionPrompt{
		Title:     title,
		InputJson: string(payload.toolInput),
		Options:   agent.AllowDenyOptions(),
	}
	if title == contracts.LettaToolBash {
		permission.Command = providerkit.ObjectString(providerkit.RawFields(payload.toolInput), "command")
	}
	return permission
}

// readLettaQuestions parses and validates a Letta question array (`questions`).
// It reports false when the list is not a nonempty JSON array of question
// objects, when any question has missing, non-string, or blank text, or when
// two questions share the same text (answers are keyed by question text).
//
// An option is an object with a label or a bare string; an option with no
// label is dropped, and the label is also the value that the answer sends.
// A question keeps no header and no description: the transcript row draws those.
func readLettaQuestions(raw json.RawMessage) ([]*leapmuxv1.ControlQuestion, bool) {
	elements, ok := providerkit.RawArray(raw)
	if !ok || len(elements) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(elements))
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(elements))
	for _, element := range elements {
		question, ok := providerkit.RawObject(element)
		if !ok {
			return nil, false
		}
		text, ok := providerkit.RawString(question["question"])
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false
		}
		if _, repeated := seen[text]; repeated {
			return nil, false
		}
		seen[text] = struct{}{}
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Question:    text,
			Options:     lettaPromptOptions(question["options"]),
			MultiSelect: providerkit.RawBool(question["multiSelect"]),
		})
	}
	return questions, true
}

// lettaPromptOptions reads the options of one question. An option is a bare
// string or an object with a `label`; an option whose label is empty or not
// text is dropped.
func lettaPromptOptions(raw json.RawMessage) []*leapmuxv1.QuestionOption {
	elements, ok := providerkit.RawArray(raw)
	if !ok {
		return nil
	}
	options := make([]*leapmuxv1.QuestionOption, 0, len(elements))
	for _, element := range elements {
		label, ok := providerkit.RawString(element)
		if !ok {
			label = providerkit.ObjectString(providerkit.RawFields(element), "label")
		}
		if label == "" {
			continue
		}
		options = append(options, &leapmuxv1.QuestionOption{Value: &label, Label: label})
	}
	return options
}

// AnswerControlRequest builds Letta Code's answer in the neutral envelope,
// which ResolveControlResponse turns into the bytes that Letta Code reads:
//
//   - A question allows with the input of the question call and the answers
//     beside it. ResolveControlResponse turns it into the question response,
//     and a refusal into a dismissal whose feedback follows as a message.
//   - A permission allows or denies, and the deny carries the feedback.
func (lettaProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		payload, _ := readLettaControlPayload(input.Request.Payload)
		return providerkit.AskUserQuestionAnswers(input, payload.toolInput)
	case *leapmuxv1.ControlPrompt_Permission:
		return agent.AnswerAllowDeny(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Letta Code states no %T prompt", input.Prompt.GetKind())
	}
}
