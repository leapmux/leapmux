package cursor

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// cursorQuestionOutcomeAnswered is the outcome of a cursor/ask_question that
// the user answered. A question that the user refuses takes the Agent Client
// Protocol's own `cancelled` outcome.
const cursorQuestionOutcomeAnswered = "answered"

// The words of Cursor's own plan buttons. A cursor/create_plan request offers
// LeapMux's allow and deny options, and these label them.
const (
	createPlanApproveLabel = "Approve"
	createPlanRejectLabel  = "Reject"
)

// ReadControlRequest reads Cursor's two requests of its own, a question and a
// plan to create. Every other request is the Agent Client Protocol's, and the
// base reads it. The three methods exclude each other, so the order of the
// checks changes no prompt.
func (cursorProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	method, params := acp.ReadControlRequestEnvelope(input.Payload)
	switch method {
	case contracts.CursorMethodAskQuestion:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: cursorQuestions(params),
		}}}
	case contracts.CursorMethodCreatePlan:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: createPlanPermission(params)}}
	default:
		return acp.ReadBaseControlRequest(input)
	}
}

// cursorQuestions reads the questions of a cursor/ask_question request.
//
//   - An element that is no object is dropped, because no reader can draw it.
//   - A question keeps its own words as its header, and its id when it states
//     no words.
//   - Every question accepts its options only: Cursor's terminal interface
//     takes typed text, but its ACP adapter copies the selected option ids and
//     drops the text.
func cursorQuestions(params map[string]json.RawMessage) []*leapmuxv1.ControlQuestion {
	elements, ok := providerkit.RawArray(params["questions"])
	if !ok {
		return nil
	}
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(elements))
	for _, element := range elements {
		fields, ok := providerkit.RawObject(element)
		if !ok {
			continue
		}
		id, _ := providerkit.RawString(fields["id"])
		prompt, _ := providerkit.RawString(fields["prompt"])
		header := prompt
		if header == "" {
			header = id
		}
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Id:              id,
			Header:          header,
			Question:        prompt,
			Options:         cursorQuestionOptions(fields["options"]),
			MultiSelect:     providerkit.RawBool(fields["allowMultiple"]),
			AllowCustomText: proto.Bool(false),
		})
	}
	return questions
}

// cursorQuestionOptions reads the options of one Cursor question. An option
// is labeled by its label, else by its id, and an option with neither is
// dropped. Its value is its id, else its label, so two options with the same
// label still send different answers.
func cursorQuestionOptions(raw json.RawMessage) []*leapmuxv1.QuestionOption {
	elements, ok := providerkit.RawArray(raw)
	if !ok {
		return nil
	}
	options := make([]*leapmuxv1.QuestionOption, 0, len(elements))
	for _, element := range elements {
		fields, ok := providerkit.RawObject(element)
		if !ok {
			continue
		}
		id, _ := providerkit.RawString(fields["id"])
		label, _ := providerkit.RawString(fields["label"])
		if label == "" {
			label = id
		}
		if label == "" {
			continue
		}
		value := id
		if value == "" {
			value = label
		}
		options = append(options, &leapmuxv1.QuestionOption{Value: proto.String(value), Label: label})
	}
	return options
}

// createPlanPermission reads a cursor/create_plan request as a permission.
// Cursor sends the plan beside its name and overview, and the reader must see
// the whole plan before an approval, so the prompt states all three. The
// request states no options: LeapMux offers its own allow and deny, under the
// words of Cursor's plan buttons.
func createPlanPermission(params map[string]json.RawMessage) *leapmuxv1.PermissionPrompt {
	name, _ := providerkit.RawString(params["name"])
	title := "Create Plan"
	if name != "" {
		title = "Create Plan: " + name
	}
	reason, _ := providerkit.RawString(params["overview"])
	text, _ := providerkit.RawString(params["plan"])
	return &leapmuxv1.PermissionPrompt{
		Title:  title,
		Reason: reason,
		Text:   text,
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: contracts.ControlAnswerOptionAllow, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: createPlanApproveLabel},
			{OptionId: contracts.ControlAnswerOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: createPlanRejectLabel},
		},
	}
}

// AnswerControlRequest answers Cursor's two requests of its own, and the base
// answers every other request. The method of the request decides, as it does
// for the prompt.
func (cursorProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	method, _ := acp.ReadControlRequestEnvelope(input.Request.Payload)
	switch method {
	case contracts.CursorMethodAskQuestion:
		return answerCursorQuestion(input)
	case contracts.CursorMethodCreatePlan:
		return answerCreatePlan(input)
	default:
		return acp.AnswerBaseControlRequest(input, nil)
	}
}

// answerCursorQuestion answers a cursor/ask_question request with the
// JSON-RPC result that Cursor reads:
//
//   - An answer states, for each question, its id (or `q<index>` for a
//     question with none) and the values of its selected options. Cursor
//     needs a selection for every question, so a question with none fails
//     the decision.
//   - The answer carries option ids only, so typed text cannot join a
//     selection here, as agent.QuestionAnswerValues would add it. Cursor's
//     own answer has a `freeformText` field, but its ACP adapter drops it.
//     The prompt therefore states that no question accepts typed text, and
//     QuestionDecisionOf refuses any, white space aside.
//   - A refusal is the `cancelled` outcome, with the user's feedback as its
//     reason when there is one.
func answerCursorQuestion(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		outcome := map[string]any{"outcome": contracts.ACPPermissionOutcomeCancelled}
		if reason := decision.GetFeedback(); reason != "" {
			outcome["reason"] = reason
		}
		return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{"outcome": outcome})}, nil
	}
	if len(questions) == 0 {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the request asks no question, so only a refusal answers it")
	}
	answers := make([]map[string]any, 0, len(questions))
	for i, question := range questions {
		selected := agent.QuestionAnswerAt(decision, i).GetSelected()
		if len(selected) == 0 {
			return agent.ControlAnswer{}, agent.InvalidControlDecision("Cursor needs an option for every question, and question %d has none", i+1)
		}
		questionID := question.GetId()
		if questionID == "" {
			questionID = fmt.Sprintf("q%d", i)
		}
		answers = append(answers, map[string]any{"questionId": questionID, "selectedOptionIds": selected})
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{
		"outcome": map[string]any{"outcome": cursorQuestionOutcomeAnswered, "answers": answers},
	})}, nil
}

// answerCreatePlan answers a cursor/create_plan request in the neutral
// envelope, as Cursor's own plan buttons do:
//
//   - Approve allows with an empty input. Feedback goes with a refusal only.
//   - Reject denies with the user's feedback, or with LeapMux's placeholder
//     when there is none.
//
// ResolveControlResponse turns either into Cursor's plan outcome, and the
// feedback becomes the outcome's reason, so no follow-up message carries it.
func answerCreatePlan(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if !agent.IsAllowOption(option) {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	if decision.GetFeedback() != "" {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("feedback goes with a refusal, and %q approves the plan", option.GetOptionId())
	}
	return agent.ControlAnswer{Content: agent.AllowResponse(input.RequestID, json.RawMessage("{}"))}, nil
}
