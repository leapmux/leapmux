package letta

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Letta Code has two control flows, and they differ in how the answer returns.
//
// A permission is a `can_use_tool` control request: the server asks the worker
// to approve a tool call and waits. The answer is a FLAT `approval_response`
// payload: `kind`, `request_id` and `decision` sit at the top level, never
// under a `response` key.
//
// A question is not a control request at all. Since Letta Code 0.34 the
// AskUserQuestion tool posts its questions, returns a receipt, and lets the turn
// go on. No call waits for the answer. The worker publishes the question from
// the receipt, and the answer returns as an ordinary user message that holds a
// `<task-notification>`.

// lettaQuestionVersion is the version of the receipt and of the response that
// Letta Code 0.34 reads and writes.
const lettaQuestionVersion = 2

// lettaRefusalIncompleteAnswers is the reason that the worker refuses an answer
// that Letta Code would refuse to read. It completes "The response was not sent: ".
const lettaRefusalIncompleteAnswers = "Letta Code accepts an answer only when every question has a nonempty answer"

// lettaControlRequest is the body of a control_request message. The frame
// names it in `request`, the discriminator is `subtype`, and the body sits
// FLAT: `tool_name`, `input` and the rest are direct members of `request`,
// never under a `payload` key. The request id is the frame's own
// `request_id`.
type lettaControlRequest struct {
	Subtype               string                      `json:"subtype"`
	ToolName              string                      `json:"tool_name"`
	ToolCallID            string                      `json:"tool_call_id"`
	Input                 json.RawMessage             `json:"input"`
	PermissionSuggestions []lettaPermissionSuggestion `json:"permission_suggestions"`
}

// lettaPermissionSuggestion is one offer a permission request carries.
type lettaPermissionSuggestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// onControlRequest publishes one permission request. `body` is the frame's
// `request` object and `requestID` the frame's `request_id`.
//
// A request for the question tool is a permission like any other: it asks
// whether the tool may run, and Strict mode asks it. The questions follow as a
// receipt after the reader allows the call.
func (a *Agent) onControlRequest(line []byte, body []byte, requestID string) {
	var req lettaControlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		slog.Debug("letta: bad control request", "agent_id", a.AgentID(), "error", err)
		return
	}
	if req.Subtype != "can_use_tool" {
		slog.Debug("letta: unknown control request", "agent_id", a.AgentID(), "subtype", req.Subtype)
		return
	}

	if requestID == "" {
		if req.ToolCallID == "" {
			slog.Warn("letta permission request has no native identity", "agent_id", a.AgentID())
			return
		}
		// Native Letta derives permission IDs from tool-call IDs with this prefix.
		requestID = "perm-" + req.ToolCallID
	}
	kind := lettaControlPermission
	slog.Info("letta: control request",
		"agent_id", a.AgentID(), "request_id", requestID, "kind", kind, "tool", req.ToolName)

	// The tool fields take the CONTRACT names (`tool_name`, `tool_call_id`,
	// `tool_input`): ReadControlRequest reads them under those names, the same
	// ones a stream_delta carries. A payload that spelled them differently drew
	// a banner titled "Tool" with no command.
	payloadBytes, err := json.Marshal(map[string]any{
		"type":                              string(kind),
		"requestId":                         requestID,
		contracts.LettaDeltaFieldToolName:   req.ToolName,
		contracts.LettaDeltaFieldToolCallID: req.ToolCallID,
		contracts.LettaDeltaFieldToolInput:  req.Input,
		"suggestions":                       req.PermissionSuggestions,
	})
	if err != nil {
		return
	}
	a.registerPermission(requestID, payloadBytes, req.ToolName != "")
}

// lettaQuestionRequestID is the request id of the question that one call posts.
// The native receipt states no request id, and a call ID names the call.
func lettaQuestionRequestID(toolCallID string) string {
	return "letta-question-" + toolCallID
}

// postQuestionRequest publishes the question that an accepted AskUserQuestion
// call posted. `tool` is the open call that the result closes, and `delta` is the
// result. It publishes nothing for a call that failed, for another tool, and for
// a result that is no receipt of this call: the same bytes that a Bash call
// returns are command output.
func (a *Agent) postQuestionRequest(tool *lettaTool, delta *lettaDelta) {
	if tool == nil || tool.name != contracts.LettaToolAskUserQuestion || delta.Status != contracts.LettaToolStatusSuccess {
		return
	}
	output, present, valid := lettaReturnedData(delta)
	if !present || !valid {
		return
	}
	// The receipt is JSON text inside the JSON string `tool_return`.
	text := []byte(output)
	var inner string
	if json.Unmarshal(output, &inner) == nil {
		text = []byte(inner)
	}
	var receipt contracts.LettaQuestionReceipt
	if json.Unmarshal(text, &receipt) != nil ||
		receipt.FieldType != contracts.LettaQuestionReceiptType ||
		receipt.FieldVersion != lettaQuestionVersion ||
		receipt.FieldToolCallID != tool.id {
		return
	}
	if _, ok := lettaQuestionTexts(receipt.FieldQuestions); !ok {
		slog.Warn("letta: question receipt holds no usable question", "agent_id", a.AgentID(), "tool_call_id", tool.id)
		return
	}

	requestID := lettaQuestionRequestID(tool.id)
	slog.Info("letta: question request",
		"agent_id", a.AgentID(), "request_id", requestID, "tool", tool.name)
	payloadBytes, err := json.Marshal(map[string]any{
		"type":                              string(lettaControlAskUser),
		"requestId":                         requestID,
		contracts.LettaDeltaFieldToolName:   tool.name,
		contracts.LettaDeltaFieldToolCallID: tool.id,
		contracts.LettaDeltaFieldToolInput: map[string]json.RawMessage{
			contracts.LettaQuestionFieldQuestions: receipt.FieldQuestions,
		},
	})
	if err != nil {
		return
	}
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		AgentSessionID: a.lettaSession(),
		RequestID:      requestID,
		Payload:        payloadBytes,
	}); err != nil {
		slog.Debug("letta: publish question failed", "agent_id", a.AgentID(), "error", err)
	}
}

// lettaQuestionTexts returns the text of each question, in order. It reports
// false when the list is empty, when a question has no text, and when two
// questions share one text, because an answer is keyed by that text.
func lettaQuestionTexts(questions json.RawMessage) ([]string, bool) {
	var list []struct {
		Question string `json:"question"`
	}
	if json.Unmarshal(questions, &list) != nil || len(list) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(list))
	texts := make([]string, 0, len(list))
	for _, question := range list {
		if strings.TrimSpace(question.Question) == "" {
			return nil, false
		}
		if _, repeated := seen[question.Question]; repeated {
			return nil, false
		}
		seen[question.Question] = struct{}{}
		texts = append(texts, question.Question)
	}
	return texts, true
}

// lettaResolveControlResponse turns the neutral answer of AnswerControlRequest
// into the bytes that the worker sends to Letta Code. An absent request leaves the response bytes
// alone, and a malformed request withholds the response: the two rules the
// shared suites pin.
//
// A permission becomes the flat approval_response payload. A question becomes
// the response that SendRawInput wraps in a task notification.
func lettaResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if len(ctx.RequestPayload) == 0 {
		return agent.ControlResponseResolution{Content: ctx.ResponseContent}
	}
	var request struct {
		Type       string          `json:"type"`
		RequestID  string          `json:"requestId"`
		ToolCallID string          `json:"tool_call_id"`
		ToolInput  json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(ctx.RequestPayload, &request); err != nil {
		resolution := agent.ControlResponseResolution{Content: ctx.ResponseContent}
		resolution.Refuse(agent.RefusalUnreadableRequest)
		return resolution
	}

	_, behavior, message, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !ok {
		var resolution agent.ControlResponseResolution
		resolution.Refuse(agent.RefusalNoDecision)
		return resolution
	}
	if request.Type == string(lettaControlAskUser) {
		// Only a question request states its input as the question call's own object.
		var input map[string]json.RawMessage
		if err := json.Unmarshal(request.ToolInput, &input); err != nil {
			var resolution agent.ControlResponseResolution
			resolution.Refuse(agent.RefusalUnreadableRequest)
			return resolution
		}
		return lettaResolveQuestion(ctx, request.ToolCallID, input[contracts.LettaQuestionFieldQuestions], behavior, message)
	}

	behaviorWord := "deny"
	if behavior == contracts.ControlAnswerBehaviorAllow {
		behaviorWord = "allow"
	}
	raw, err := lettaApprovalResponse(request.RequestID, behaviorWord, message)
	if err != nil {
		var resolution agent.ControlResponseResolution
		resolution.Refuse(agent.RefusalUnencodableReply)
		return resolution
	}
	return agent.ControlResponseResolution{Content: raw}
}

// lettaApprovalResponse encodes the approval_response payload that answers the
// permission `requestID` with a decision: `behavior` is `allow` or `deny`, and
// `message` is the reason that the model reads.
func lettaApprovalResponse(requestID, behavior, message string) ([]byte, error) {
	decision, err := json.Marshal(contracts.LettaApprovalDecision{Behavior: behavior, Message: message})
	if err != nil {
		return nil, err
	}
	return json.Marshal(contracts.LettaApprovalResponse{
		Kind:      contracts.LettaReplyKindApprovalResponse,
		RequestID: requestID,
		Decision:  decision,
	})
}

// lettaResolveQuestion turns the neutral answer to a posted question into the
// response of Letta Code. An allow answers each question, and any other
// decision dismisses the questions. Letta Code reads a dismissal without a
// reason, so a reason that the reader typed travels as the next user message.
func lettaResolveQuestion(ctx agent.ControlResponseContext, toolCallID string, questions json.RawMessage, behavior, message string) agent.ControlResponseResolution {
	texts, ok := lettaQuestionTexts(questions)
	if !ok || toolCallID == "" {
		var resolution agent.ControlResponseResolution
		resolution.Refuse(agent.RefusalUnreadableRequest)
		return resolution
	}
	response := contracts.LettaQuestionResponse{
		FieldType:       contracts.LettaQuestionResponseType,
		FieldVersion:    lettaQuestionVersion,
		FieldToolCallID: toolCallID,
		FieldQuestions:  questions,
		FieldStatus:     contracts.LettaQuestionStatusDismissed,
	}
	feedback := message
	if behavior == contracts.ControlAnswerBehaviorAllow {
		answers, complete := lettaAnswersFor(texts, ctx.ResponseContent)
		if !complete {
			var resolution agent.ControlResponseResolution
			resolution.Refuse(lettaRefusalIncompleteAnswers)
			return resolution
		}
		response.FieldStatus = contracts.LettaQuestionStatusAnswered
		response.FieldAnswers = answers
		feedback = ""
	}
	raw, err := providerkit.JSONStringify(response)
	if err != nil {
		var resolution agent.ControlResponseResolution
		resolution.Refuse(agent.RefusalUnencodableReply)
		return resolution
	}
	return agent.ControlResponseResolution{Content: raw, FollowUp: agent.FeedbackFollowUp(feedback)}
}

// lettaEnvelopeAnswersField is the member of `updatedInput` in which
// AnswerControlRequest folds the answers of a question form
// (providerkit.AskUserQuestionAnswers). It belongs to the neutral control
// response that several providers share. It is not a field of the Letta
// response, so the `question` table of the contract does not state it.
const lettaEnvelopeAnswersField = "answers"

// lettaAnswersFor reads the answers that AnswerControlRequest folded into the
// input of the question call. It reports false unless the answers hold exactly one nonempty
// text for each question.
func lettaAnswersFor(texts []string, content []byte) (map[string]string, bool) {
	given, _ := agent.DecodeControlUpdatedInput(content)[lettaEnvelopeAnswersField].(map[string]any)
	if len(given) != len(texts) {
		return nil, false
	}
	answers := make(map[string]string, len(texts))
	for _, text := range texts {
		answer, _ := given[text].(string)
		if strings.TrimSpace(answer) == "" {
			return nil, false
		}
		answers[text] = answer
	}
	return answers, true
}

// lettaQuestionNotification returns the text of the user message that answers a
// posted question. It is the text that `prepareAskUserQuestionNotif` of Letta
// Code writes for the same response: the call ID and the response, each escaped
// for XML, inside one task notification. `data` is a response that
// lettaResolveQuestion wrote. The function reads it again, because the bytes
// come back from the service and a response that Letta Code would refuse must
// not reach the server.
func lettaQuestionNotification(data []byte) (string, error) {
	var response contracts.LettaQuestionResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("the Letta question response is not JSON: %w", err)
	}
	texts, ok := lettaQuestionTexts(response.FieldQuestions)
	if response.FieldType != contracts.LettaQuestionResponseType || response.FieldVersion != lettaQuestionVersion ||
		response.FieldToolCallID == "" || !ok {
		return "", errors.New("the Letta question response does not state a posted question")
	}
	switch response.FieldStatus {
	case contracts.LettaQuestionStatusAnswered:
		if len(response.FieldAnswers) != len(texts) {
			return "", errors.New("the Letta question response does not answer every question")
		}
		for _, text := range texts {
			if strings.TrimSpace(response.FieldAnswers[text]) == "" {
				return "", errors.New("the Letta question response does not answer every question")
			}
		}
	case contracts.LettaQuestionStatusDismissed:
		if response.FieldAnswers != nil {
			return "", errors.New("a dismissed Letta question response holds answers")
		}
	default:
		return "", fmt.Errorf("the Letta question response has the unknown status %q", response.FieldStatus)
	}
	encoded, err := providerkit.JSONStringify(response)
	if err != nil {
		return "", err
	}
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	return lettaNotificationOpen + "\n" +
		"<task-id>" + escape(response.FieldToolCallID) + "</task-id>\n" +
		"<summary>User " + response.FieldStatus + " your questions.</summary>\n" +
		lettaQuestionResponseOpen + escape(string(encoded)) + "</ask-user-question-response>\n" +
		"</task-notification>", nil
}

// The tags of the task notification that carries a question response.
const (
	lettaNotificationOpen     = "<task-notification>"
	lettaQuestionResponseOpen = "<ask-user-question-response>"
)

// lettaSession reads the provider session id, which the control request must
// stamp so the service's delivery check matches it.
func (a *Agent) lettaSession() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.conversationID
}

// SendRawInput delivers one control answer to the App Server. It takes two
// answers, and both travel as the PAYLOAD of an `input` command, addressed to the
// runtime. The base implementation writes to the process's STDIN, which `letta
// server` does not read for protocol_v2: the answer then never arrives.
//
//   - The flat `approval_response` payload that lettaResolveControlResponse built
//     for a permission resolves the approval that the running turn waits for.
//   - The question response becomes a task notification in a `create_message`
//     input. A running turn queues it, and an idle conversation starts a turn.
func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	if a.IsStopped() {
		return errAgentStopped
	}
	var head struct {
		Kind string `json:"kind"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("the Letta control answer is not JSON: %w", err)
	}
	scope := a.runtime()
	if scope.AgentID == "" || scope.ConversationID == "" {
		return errors.New("the Letta runtime identity is not known yet; the answer cannot be addressed")
	}
	switch {
	case head.Kind == contracts.LettaReplyKindApprovalResponse:
		return a.answerPermission(data, scope)
	case head.Type == contracts.LettaQuestionResponseType:
		notification, err := lettaQuestionNotification(data)
		if err != nil {
			return err
		}
		messages, err := buildUserMessages(notification, nil)
		if err != nil {
			return err
		}
		cmd := a.createMessageCommand(scope, messages)
		slog.Info("letta: send question response",
			"agent_id", a.AgentID(), "request_id", cmd.RequestID,
			"runtime_conversation_id", scope.ConversationID)
		return a.sendCommand(cmd)
	default:
		return fmt.Errorf("the Letta control answer is neither an %s nor a question response", contracts.LettaReplyKindApprovalResponse)
	}
}
