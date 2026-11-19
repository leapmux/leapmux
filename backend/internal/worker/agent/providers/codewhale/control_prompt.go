package codewhale

import (
	"encoding/json"
	"fmt"

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
//   - A `user_input.required` event of the `request_user_input` tool is a
//     question.
//   - An `approval.required` event is a permission for one call. Codewhale
//     states no options, so the prompt states LeapMux's allow and deny.
//
// Any other payload reads as the generic prompt.
func (codewhaleProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if questions, ok := codewhaleQuestions(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: questions}}}
	}
	if permission, ok := codewhalePermissionPrompt(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
	}
	return agent.GenericPermissionPrompt()
}

// codewhaleStoredEvent reads the runtime's event object of a stored payload,
// or nil when the payload states none.
func codewhaleStoredEvent(payload []byte) map[string]json.RawMessage {
	fields, _ := providerkit.RawObject(payload)
	event, _ := providerkit.RawObject(fields[contracts.CodewhaleControlPayloadEvent])
	return event
}

// codewhaleStoredEventName is the name of the stored event, such as
// `approval.required`, or "".
func codewhaleStoredEventName(payload []byte) string {
	return providerkit.ObjectString(codewhaleStoredEvent(payload), contracts.CodewhaleEnvelopeFieldEvent)
}

// codewhalePermissionPrompt reads an approval: the tool, its input, the
// call's intent as the reason, and the command of a command tool. It
// reports false for any other payload.
//
// The tool name comes from the shared header, else from the event. The
// reason is the call's intent and never the tool's static description.
func codewhalePermissionPrompt(payload []byte) (*leapmuxv1.PermissionPrompt, bool) {
	event := codewhaleStoredEvent(payload)
	if providerkit.ObjectString(event, contracts.CodewhaleEnvelopeFieldEvent) != contracts.CodewhaleEventApprovalRequired {
		return nil, false
	}
	approval, _ := providerkit.RawObject(event[contracts.CodewhaleEnvelopeFieldPayload])
	toolName := agent.ControlToolName(payload)
	if toolName == "" {
		toolName = providerkit.ObjectString(approval, contracts.CodewhaleApprovalFieldToolName)
	}
	toolInput := agent.ControlToolInput(payload)
	permission := &leapmuxv1.PermissionPrompt{
		Title:     toolName,
		Reason:    providerkit.ObjectString(approval, contracts.CodewhaleApprovalFieldIntentSummary),
		InputJson: string(toolInput),
		Options:   agent.AllowDenyOptions(),
	}
	if isCodewhaleCommandTool(toolName) {
		arguments, _ := providerkit.RawObject(toolInput)
		permission.Command = providerkit.ObjectString(arguments, "command")
	}
	return permission, true
}

// codewhaleQuestions reads a stored question: a `user_input.required` event
// whose header states the `request_user_input` tool. It reports false for
// any other payload.
//
// A question needs its id, because the runtime matches an answer by it, and
// its text: the sentence, or the header when it states only that. A record
// without either is dropped. The header is stated only when it adds words,
// and an option with no label is dropped.
func codewhaleQuestions(payload []byte) ([]*leapmuxv1.ControlQuestion, bool) {
	if codewhaleStoredEventName(payload) != contracts.CodewhaleEventUserInputRequired ||
		agent.ControlToolName(payload) != contracts.CodewhaleToolRequestUserInput {
		return nil, false
	}
	questions := []*leapmuxv1.ControlQuestion{}
	for _, record := range codewhaleQuestionRecords(payload) {
		id := providerkit.ObjectString(record, contracts.CodewhaleQuestionFieldID)
		header := providerkit.ObjectString(record, contracts.CodewhaleQuestionFieldHeader)
		text := providerkit.ObjectString(record, contracts.CodewhaleQuestionFieldQuestion)
		if text == "" {
			text = header
		}
		if id == "" || text == "" {
			continue
		}
		question := &leapmuxv1.ControlQuestion{
			Id:          id,
			Question:    text,
			Options:     codewhaleQuestionOptions(record[contracts.CodewhaleQuestionFieldOptions]),
			MultiSelect: providerkit.RawBool(record[contracts.CodewhaleQuestionFieldMultiSelect]),
		}
		if header != text {
			question.Header = header
		}
		questions = append(questions, question)
	}
	return questions, true
}

// codewhaleQuestionRecords reads the question records of a stored question,
// in the runtime's order. The shared header's `request.input.questions` is
// what the Worker stored for the control to answer, so it wins whenever it
// states a list, even an empty one. The stored event's
// `payload.request.questions` is the fallback. A record that is no object is
// dropped.
func codewhaleQuestionRecords(payload []byte) []map[string]json.RawMessage {
	header, _ := providerkit.RawObject(agent.ControlToolInput(payload))
	raw := header[contracts.CodewhaleQuestionFieldQuestions]
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil || elements == nil {
		eventPayload, _ := providerkit.RawObject(codewhaleStoredEvent(payload)[contracts.CodewhaleEnvelopeFieldPayload])
		request, _ := providerkit.RawObject(eventPayload[contracts.CodewhaleUserInputFieldRequest])
		elements = nil
		_ = json.Unmarshal(request[contracts.CodewhaleQuestionFieldQuestions], &elements)
	}
	records := make([]map[string]json.RawMessage, 0, len(elements))
	for _, element := range elements {
		if record, ok := providerkit.RawObject(element); ok {
			records = append(records, record)
		}
	}
	return records
}

// codewhaleQuestionOptions reads the options of one question. An option with
// no label has nothing to choose, so it is dropped. An option states no
// value, so its answer sends its label.
func codewhaleQuestionOptions(raw json.RawMessage) []*leapmuxv1.QuestionOption {
	var elements []json.RawMessage
	_ = json.Unmarshal(raw, &elements)
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
	answers := []map[string]string{}
	for i, question := range questions {
		answer := agent.QuestionAnswerAt(decision, i)
		for position, value := range agent.QuestionAnswerValues(question, answer) {
			label := value
			// QuestionAnswerValues lists the selected values first, so a
			// value after them is the typed text.
			if position >= len(answer.GetSelected()) {
				label = contracts.CodewhaleAnswerLabelOther
			}
			answers = append(answers, codewhaleAnswer(question.GetId(), label, value))
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(agent.ControlToolInput(input.Request.Payload), &fields); err != nil {
		return agent.ControlAnswer{}, fmt.Errorf("read the Codewhale question input: %w", err)
	}
	encoded, err := json.Marshal(answers)
	if err != nil {
		return agent.ControlAnswer{}, fmt.Errorf("encode the Codewhale answers: %w", err)
	}
	fields[codewhaleUpdatedInputAnswers] = encoded
	updated, err := json.Marshal(fields)
	if err != nil {
		return agent.ControlAnswer{}, fmt.Errorf("encode the Codewhale question input: %w", err)
	}
	return agent.ControlAnswer{Content: agent.AllowResponse(input.RequestID, updated)}, nil
}

// codewhaleAnswer is one answer of the runtime's list, keyed by the
// contract's field names, which the Worker forwards as they stand.
func codewhaleAnswer(id, label, value string) map[string]string {
	return map[string]string{
		contracts.CodewhaleAnswerFieldID:    id,
		contracts.CodewhaleAnswerFieldLabel: label,
		contracts.CodewhaleAnswerFieldValue: value,
	}
}
