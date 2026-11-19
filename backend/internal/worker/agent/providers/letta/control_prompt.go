package letta

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// lettaToolTitleFallback is the title of a permission whose request states
// no tool name.
const lettaToolTitleFallback = "Tool"

// lettaControlPayload is the part of a published control request that the
// prompt reads. The Worker writes the payload itself (onControlRequest,
// postQuestionRequest), and `type` says which of the two requests it is.
type lettaControlPayload struct {
	kind      string
	toolName  string
	toolInput json.RawMessage
}

// readLettaControlPayload reads payload. A field that is not text reads as
// empty, and a tool input that is not a JSON object reads as `{}`.
func readLettaControlPayload(payload []byte) lettaControlPayload {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(payload, &fields)
	kind, _ := providerkit.RawString(fields["type"])
	toolName, _ := providerkit.RawString(fields[contracts.LettaDeltaFieldToolName])
	toolInput := json.RawMessage("{}")
	var object map[string]json.RawMessage
	if raw := fields[contracts.LettaDeltaFieldToolInput]; json.Unmarshal(raw, &object) == nil && object != nil {
		toolInput = raw
	}
	return lettaControlPayload{kind: kind, toolName: toolName, toolInput: toolInput}
}

// ReadControlRequest reads the two requests that the Worker publishes for
// Letta Code:
//
//   - A question request (`ask_user`) states the questions of an
//     AskUserQuestion call that already returned its receipt.
//   - A permission request (`permission`) asks whether one tool call may run.
//     A call of the question tool is one of them: Strict mode asks it.
//
// Any other payload reads as the generic permission prompt.
func (lettaProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	payload := readLettaControlPayload(input.Payload)
	switch payload.kind {
	case string(lettaControlAskUser):
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: lettaPromptQuestions(payload.toolInput),
		}}}
	case string(lettaControlPermission):
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: lettaPermissionPrompt(payload)}}
	default:
		return agent.GenericPermissionPrompt()
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
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(payload.toolInput, &fields)
		permission.Command, _ = providerkit.RawString(fields["command"])
	}
	return permission
}

// lettaPromptQuestions reads the `questions` of a question call's input.
//
// Letta's question states its text, its options and whether it takes several
// of them. An element that is no object is dropped. An option is an object
// with a label, or a bare string; an option with no label is dropped, and the
// label is also the value that the answer sends. A question keeps no header
// and no description: the transcript row of the call draws those.
func lettaPromptQuestions(toolInput json.RawMessage) []*leapmuxv1.ControlQuestion {
	var fields map[string]json.RawMessage
	var elements []json.RawMessage
	if json.Unmarshal(toolInput, &fields) != nil || json.Unmarshal(fields[contracts.LettaQuestionFieldQuestions], &elements) != nil {
		return nil
	}
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(elements))
	for _, element := range elements {
		var question map[string]json.RawMessage
		if json.Unmarshal(element, &question) != nil || question == nil {
			continue
		}
		text, _ := providerkit.RawString(question["question"])
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Question:    text,
			Options:     lettaPromptOptions(question["options"]),
			MultiSelect: providerkit.RawBool(question["multiSelect"]),
		})
	}
	return questions
}

// lettaPromptOptions reads the options of one question. An option is a bare
// string or an object with a `label`; an option whose label is empty or not
// text is dropped.
func lettaPromptOptions(raw json.RawMessage) []*leapmuxv1.QuestionOption {
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil {
		return nil
	}
	options := make([]*leapmuxv1.QuestionOption, 0, len(elements))
	for _, element := range elements {
		label, ok := providerkit.RawString(element)
		if !ok {
			var option map[string]json.RawMessage
			_ = json.Unmarshal(element, &option)
			label, _ = providerkit.RawString(option["label"])
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
		return providerkit.AskUserQuestionAnswers(input, readLettaControlPayload(input.Request.Payload).toolInput)
	case *leapmuxv1.ControlPrompt_Permission:
		return agent.AnswerAllowDeny(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Letta Code states no %T prompt", input.Prompt.GetKind())
	}
}
