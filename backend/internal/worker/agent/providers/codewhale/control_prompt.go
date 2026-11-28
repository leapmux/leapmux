package codewhale

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codewhaleUpdatedInputAnswers is the key of `updatedInput` that carries the
// runtime's own answer list. answersFromResponse reads it.
const codewhaleUpdatedInputAnswers = "answers"

// isCodewhaleCommandTool reports whether a call of toolName runs a command,
// so that its permission prompt states the command. The browser's Codewhale
// plugin gives these tools the `execute` kind in its tool-kind table.
func isCodewhaleCommandTool(toolName string) bool {
	switch toolName {
	case contracts.CodewhaleToolBash, contracts.CodewhaleToolBashAlias,
		contracts.CodewhaleToolTerminalRun, contracts.CodewhaleToolTerminalSend,
		contracts.CodewhaleToolCodeExecution, contracts.CodewhaleToolJsExecution,
		contracts.CodewhaleToolPandocConvert, contracts.CodewhaleToolGit, contracts.CodewhaleToolRun:
		return true
	default:
		return false
	}
}

// ReadControlRequest reads the two control requests that the Worker
// publishes for Codewhale (buildControlPayload): the runtime's event beside
// the shared `request` header.
//
//   - A `user_input.required` event of the `request_user_input` tool with a
//     thread ID and input ID is a question.
//   - An `approval.required` event with an approval ID is a permission for one
//     call. Codewhale states no options, so the prompt states LeapMux's allow
//     and deny.
//
// Any other payload reads as unrecognized: the runtime takes an answer only
// to a routable approval or question of that tool, and resolveControlReply
// refuses any request without those identifiers.
func (codewhaleProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	env, ok := storedControlEvent(input.Payload)
	if !ok {
		return agent.UnrecognizedControlPrompt()
	}
	switch env.Event {
	case contracts.CodewhaleEventUserInputRequired:
		if questions, ok := codewhaleQuestions(input.Payload, env); ok {
			return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: questions}}}
		}
	case contracts.CodewhaleEventApprovalRequired:
		if permission, ok := codewhalePermissionPrompt(input.Payload, env); ok {
			return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
		}
	}
	return agent.UnrecognizedControlPrompt()
}

// codewhalePermissionPrompt reads an approval: the tool, its input, the
// call's intent as the reason, and the command of a command tool. It
// reports false for any payload that resolveControlReply cannot route.
//
// The tool name comes from the shared header, else from the event. The
// reason is the call's intent and never the tool's static description.
func codewhalePermissionPrompt(payload []byte, env codewhaleEnvelope) (*leapmuxv1.PermissionPrompt, bool) {
	if env.Event != contracts.CodewhaleEventApprovalRequired {
		return nil, false
	}
	approval, ok := parseApprovalEventPayload(env.Payload)
	if !ok {
		return nil, false
	}
	toolName := agent.ControlToolName(payload)
	if toolName == "" {
		toolName = approval.ToolName
	}
	toolInput := agent.ControlToolInput(payload)
	permission := &leapmuxv1.PermissionPrompt{
		Title:     toolName,
		Reason:    providerkit.ObjectString(providerkit.RawFields(env.Payload), contracts.CodewhaleApprovalFieldIntentSummary),
		InputJson: string(toolInput),
		Options:   agent.AllowDenyOptions(),
	}
	if isCodewhaleCommandTool(toolName) {
		permission.Command = providerkit.ObjectString(providerkit.RawFields(toolInput), "command")
	}
	return permission, true
}

// codewhaleQuestions reads a stored question: a routable `user_input.required`
// event whose header specifies the `request_user_input` tool. It reports false
// for any payload that resolveControlReply cannot route.
//
// A question needs a non-blank id, because the runtime matches an answer by it
// and answersFromResponse rejects blank IDs, and its text: the sentence, or
// the header when it states only that. A record without either is dropped. The
// header is stated only when it adds words, and an option with no label is
// dropped.
func codewhaleQuestions(payload []byte, env codewhaleEnvelope) ([]*leapmuxv1.ControlQuestion, bool) {
	if env.Event != contracts.CodewhaleEventUserInputRequired ||
		env.ThreadID == "" ||
		agent.ControlToolName(payload) != contracts.CodewhaleToolRequestUserInput {
		return nil, false
	}
	userInput, ok := parseUserInputEventPayload(env.Payload)
	if !ok {
		return nil, false
	}
	questions := []*leapmuxv1.ControlQuestion{}
	for _, record := range storedRequestQuestions(payload, userInput.Request) {
		text := record.Question
		if text == "" {
			text = record.Header
		}
		if strings.TrimSpace(record.ID) == "" || text == "" {
			continue
		}
		question := &leapmuxv1.ControlQuestion{
			Id:          record.ID,
			Question:    text,
			Options:     codewhaleQuestionOptions(record.Options),
			MultiSelect: record.MultiSelect,
		}
		if record.Header != text {
			question.Header = record.Header
		}
		questions = append(questions, question)
	}
	return questions, true
}

// codewhaleQuestionOptions reads the options of one question. An option with
// no label has nothing to choose, so it is dropped. An option states no
// value, so its answer sends its label.
func codewhaleQuestionOptions(raw json.RawMessage) []*leapmuxv1.QuestionOption {
	elements, _ := providerkit.RawArray(raw)
	options := []*leapmuxv1.QuestionOption{}
	for _, element := range elements {
		option, ok := providerkit.RawObject(element)
		label := providerkit.ObjectString(option, contracts.CodewhaleQuestionFieldLabel)
		if !ok || label == "" {
			continue
		}
		options = append(options, &leapmuxv1.QuestionOption{
			Label:       label,
			Description: providerkit.ObjectString(option, contracts.CodewhaleQuestionFieldDescription),
		})
	}
	return options
}

// AnswerControlRequest builds Codewhale's answer in the neutral envelope,
// which ResolveControlResponse turns into the runtime's reply frame:
//
//   - A permission takes LeapMux's allow and deny. A refusal's feedback rides
//     the deny, and the Worker sends it as the user's next message.
//   - A question's answers ride `updatedInput.answers` in the runtime's own
//     list, beside the tool's input. A multiple choice answers with its
//     options and its typed text together. A cancelled question denies with
//     its feedback.
func (codewhaleProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return codewhaleQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return agent.AnswerAllowDeny(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Codewhale states no %T prompt", input.Prompt.GetKind())
	}
}

// codewhaleQuestionAnswer answers a question with the runtime's own answer
// list, `[{id, label, value}]`, one entry for each value of each answer
// (agent.QuestionAnswerValues):
//
//   - A chosen option answers with its label as both label and value. A
//     multiple choice sends one entry for each option that it holds, all
//     with the question's id.
//   - Typed text, trimmed, answers with the `Other` label: beside the chosen
//     options of a multiple choice, and alone for any other question.
//   - A question with neither sends no entry.
func codewhaleQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	answers := []userInputAnswer{}
	for i, question := range questions {
		answer := agent.QuestionAnswerAt(decision, i)
		for position, value := range agent.QuestionAnswerValues(question, answer) {
			label := value
			// QuestionAnswerValues lists the selected values first, so a
			// value after them is the typed text.
			if position >= len(answer.GetSelected()) {
				label = contracts.CodewhaleAnswerLabelOther
			}
			answers = append(answers, userInputAnswer{ID: question.GetId(), Label: label, Value: value})
		}
	}
	return providerkit.AllowWithToolAnswers(input.RequestID, agent.ControlToolInput(input.Request.Payload), answers)
}
