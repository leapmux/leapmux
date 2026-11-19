package providerkit

import (
	"encoding/json"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The question protocol of OpenCode.
//
// OpenCode's `question` tool states its questions in a `question.asked` event,
// under `properties.questions`, and takes one list of chosen labels for each
// question, or a rejection. The Worker stores the event as `{type,
// properties}`, and the answer is a JSON-RPC result that the provider reads
// back: `{"answers": [[...], ...]}`, or `{"rejected": true}`. The two answer
// fields come from contracts/opencode-protocol.json.
//
// Two forks of OpenCode speak this protocol. Kilo reaches it through its
// daemon's own HTTP server. MiMo Code reaches it over its own HTTP routes.
// MiMo may not import the OpenCode package, so the reader and the answer live
// here, and each provider decides for itself which requests are questions.
// OpenCode itself asks its questions in native forms, which its own package
// reads (opencode/control_prompt.go).

// openCodeEventFields reads the members of a stored event, or nil for a
// payload that is no JSON object.
func openCodeEventFields(payload []byte) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return nil
	}
	return fields
}

// IsOpenCodeQuestionEvent reports whether payload is a stored
// `question.asked` event. A provider whose questions include requests that it
// answers on another surface, as MiMo's plan approval, narrows this further.
func IsOpenCodeQuestionEvent(payload []byte) bool {
	eventType, _ := RawString(openCodeEventFields(payload)["type"])
	return eventType == contracts.OpenCodeEventQuestionAsked
}

// OpenCodeQuestionPrompt reads the questions of a stored `question.asked`
// event into a question prompt.
//
// The questions are under `properties.questions`, in the shape that
// QuestionsFromWire reads, except that the daemon states a multiple selection
// as `multiple`. A `properties` or a `questions` of the wrong type reads as no
// question. The protocol's answer has no field for a note beside a
// selection, so the prompt does not preserve one: AnswerOpenCodeQuestions
// sends typed text as one more value of a multiple-choice question instead.
func OpenCodeQuestionPrompt(payload []byte) *leapmuxv1.ControlPrompt {
	var properties map[string]json.RawMessage
	_ = json.Unmarshal(openCodeEventFields(payload)["properties"], &properties)
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: QuestionsFromWire(foldOpenCodeMultiple(properties["questions"])),
	}}}
}

// foldOpenCodeMultiple copies the daemon's `multiple` onto `multiSelect` for
// each question that states no `multiSelect`, before QuestionsFromWire reads
// the questions. A question that states `multiSelect` keeps it, and a
// `multiple` that is no boolean states nothing. A raw value that is no array
// is returned unchanged, and QuestionsFromWire then reads no question.
func foldOpenCodeMultiple(raw json.RawMessage) json.RawMessage {
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil {
		return raw
	}
	for i, element := range elements {
		var fields map[string]json.RawMessage
		if json.Unmarshal(element, &fields) != nil || fields == nil {
			continue
		}
		if _, stated := fields["multiSelect"]; stated {
			continue
		}
		multiple := RawOptionalBool(fields["multiple"])
		if multiple == nil {
			continue
		}
		fields["multiSelect"] = json.RawMessage(strconv.FormatBool(*multiple))
		folded, err := json.Marshal(fields)
		if err != nil {
			continue
		}
		elements[i] = folded
	}
	folded, err := json.Marshal(elements)
	if err != nil {
		return raw
	}
	return folded
}

// AnswerOpenCodeQuestions answers a question prompt in the protocol's own
// result:
//
//   - A cancelled decision rejects the questions. The rejection carries no
//     text, so the protocol cannot deliver the decision's feedback, and the
//     answer drops it.
//   - Otherwise each question, in the prompt's order, takes the values of its
//     answer (agent.QuestionAnswerValues), or an empty list for a question
//     that the decision leaves unanswered. The typed text of a
//     multiple-choice question is one more value beside its selected
//     options, because the protocol takes no note.
func AnswerOpenCodeQuestions(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{
			contracts.OpenCodeAnswerFieldRejected: true,
		})}, nil
	}
	answers := make([][]string, len(questions))
	for i, question := range questions {
		answers[i] = agent.QuestionAnswerValues(question, agent.QuestionAnswerAt(decision, i))
		if answers[i] == nil {
			// The answer to each question is a list, so an unanswered question
			// takes an empty list, never null.
			answers[i] = []string{}
		}
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{
		contracts.OpenCodeAnswerFieldAnswers: answers,
	})}, nil
}
