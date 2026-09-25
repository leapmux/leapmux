package codewhale

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

// Codewhale's control requests: an approval, and a question.
//
// The runtime states each one as an EVENT on the thread's stream, and it reads
// the answer from a REST route. LeapMux answers neither itself: it persists a
// control request, the reader decides, and the answer comes back through
// SendControlResponse -> ResolveControlResponse, which builds a reply frame, and
// SendRawInput, which posts that frame to the route.
//
// The stored payload is a HYBRID. The Claude-shaped `{request:{tool_name,
// tool_use_id, input}}` header is what the SHARED control surfaces read: the
// service takes the tool name from it, and the shared permission row draws the
// input. The runtime's own event rides beside it, whole, under the `event` key.
//
// An approval states no tool arguments up to 0.10.0, only the tool name and the
// call id, so the header takes the input from the call's own start event, which
// the stream delivered first.
//
// `remember` is never sent. Up to 0.10.0 a remembered approval switches the
// whole thread to full access, and on 0.9.13 it failed the very call it
// approved. No released version has the scoped session grant that fixes it, and
// the runtime states no capability that would tell LeapMux when one does.
//
// The runtime answers for the reader when nobody answers in time: it denies an
// approval, and it fails the call of a question. `[tools]
// user_input_timeout_seconds` in the user's Codewhale configuration sets the
// wait, 300 s by default and 0 for no limit. LeapMux cannot set it for its own
// sessions. The key exists in the configuration file alone: no environment
// variable and no `--set` key reaches it, `POST /v1/config` changes nothing in
// the running runtime, and CODEWHALE_CONFIG_PATH would replace the user's whole
// configuration. Nothing on the wire states the wait either. So the card goes
// when the runtime answered, and a row states why: `approval.timeout` for an
// approval, and the runtime's own status item for a question.

// codewhaleControlType is the `type` of a stored control request, which the
// shared control surfaces read.
const codewhaleControlType = "control_request"

// codewhalePendingControl is one control request LeapMux published and nothing
// resolved yet.
type codewhalePendingControl struct {
	turnID string
}

// codewhaleControlHeader is the shared header of a stored control request.
type codewhaleControlHeader struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Input     json.RawMessage `json:"input"`
}

// codewhaleReplyFrame is what ResolveControlResponse builds and SendRawInput
// posts. contract_tags_test.go pins its tags to contracts/codewhale-protocol.json,
// which the browser reads the saved answer back from.
type codewhaleReplyFrame struct {
	Frame      string            `json:"frame"`
	ApprovalID string            `json:"approval_id,omitempty"`
	Decision   string            `json:"decision,omitempty"`
	Remember   bool              `json:"remember,omitempty"`
	ThreadID   string            `json:"thread_id,omitempty"`
	InputID    string            `json:"input_id,omitempty"`
	Answers    []userInputAnswer `json:"answers,omitempty"`
	Declined   bool              `json:"declined,omitempty"`
}

// userInputAnswer is one answer to one question. A multiple choice sends one
// answer for each chosen option, with the same id.
type userInputAnswer struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// approvalRequestID keys an approval's control request.
func approvalRequestID(approvalID string) string {
	return contracts.CodewhaleReplyFrameApproval + ":" + approvalID
}

// userInputRequestID keys a question's control request.
func userInputRequestID(inputID string) string {
	return contracts.CodewhaleReplyFrameUserInput + ":" + inputID
}

// buildControlPayload builds the stored payload of one control request.
func buildControlPayload(requestID string, header codewhaleControlHeader, event []byte) ([]byte, error) {
	if len(header.Input) == 0 {
		header.Input = json.RawMessage(`{}`)
	}
	return json.Marshal(map[string]any{
		"type":                                 codewhaleControlType,
		"request_id":                           requestID,
		"request":                              header,
		contracts.CodewhaleControlPayloadEvent: json.RawMessage(event),
	})
}

// storedControlEvent reads the runtime's event back out of a stored payload.
func storedControlEvent(payload []byte) (codewhaleEnvelope, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return codewhaleEnvelope{}, false
	}
	return parseEnvelope(fields[contracts.CodewhaleControlPayloadEvent])
}

// --- approvals ---

// approvalEventPayload is the payload of approval.required and approval.decided.
type approvalEventPayload struct {
	ID         string `json:"id"`
	ApprovalID string `json:"approval_id"`
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
}

func (p approvalEventPayload) approvalID() string {
	if p.ApprovalID != "" {
		return p.ApprovalID
	}
	return p.ID
}

// handleApprovalRequired publishes an approval as a control request.
func (a *Agent) handleApprovalRequired(env codewhaleEnvelope) {
	var payload approvalEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.approvalID() == "" {
		// Without an id nothing can answer it. The runtime denies it after its
		// own timeout, which is the only outcome left.
		slog.Warn("codewhale approval carried no id", "agent_id", a.AgentID(), "error", err)
		return
	}
	approvalID := payload.approvalID()
	name, input := a.toolCallInput(payload.ToolCallID)
	if name == "" {
		name = payload.ToolName
	}
	requestID := approvalRequestID(approvalID)
	content, err := buildControlPayload(requestID, codewhaleControlHeader{
		ToolName: name, ToolUseID: payload.ToolCallID, Input: input,
	}, env.raw)
	if err != nil {
		slog.Error("codewhale marshal approval", "agent_id", a.AgentID(), "error", err)
		a.denyUnpublishedApproval(approvalID)
		return
	}
	if !a.publishControl(requestID, env.TurnID, content) {
		a.denyUnpublishedApproval(approvalID)
	}
}

// denyUnpublishedApproval denies an approval that no reader will ever see, so
// the turn goes on with the refusal instead of waiting for the runtime's own
// timeout.
func (a *Agent) denyUnpublishedApproval(approvalID string) {
	go func() {
		if err := a.postApproval(approvalID, approvalBody(contracts.CodewhaleDecisionDeny)); err != nil {
			slog.Warn("codewhale deny an unpublished approval", "agent_id", a.AgentID(), "approval_id", approvalID, "error", err)
		}
	}()
}

// handleApprovalDecided retires the card of an approval the runtime settled
// without LeapMux: its own timeout, an auto posture, or the end of the turn.
// An answer LeapMux sent retired its card already, so it finds nothing here.
func (a *Agent) handleApprovalDecided(env codewhaleEnvelope) {
	var payload approvalEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.approvalID() == "" {
		return
	}
	a.withdrawControl(approvalRequestID(payload.approvalID()))
}

// approvalBody is the body of POST /v1/approvals/{id}. `remember` stays false;
// see the file comment.
func approvalBody(decision string) map[string]any {
	return map[string]any{
		contracts.CodewhaleReplyFieldDecision: decision,
		contracts.CodewhaleReplyFieldRemember: false,
	}
}

// --- questions ---

// userInputEventPayload is the payload of the user_input.* events.
type userInputEventPayload struct {
	ID      string          `json:"id"`
	InputID string          `json:"input_id"`
	Request json.RawMessage `json:"request"`
}

func (p userInputEventPayload) inputID() string {
	if p.InputID != "" {
		return p.InputID
	}
	return p.ID
}

// handleUserInputRequired publishes a question as a control request.
func (a *Agent) handleUserInputRequired(env codewhaleEnvelope) {
	var payload userInputEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.inputID() == "" {
		slog.Warn("codewhale question carried no id", "agent_id", a.AgentID(), "error", err)
		return
	}
	inputID := payload.inputID()
	// The question is the proof that its call runs, so the call's own row goes
	// in before the card that asks it.
	a.releaseHeldCall(inputID)
	requestID := userInputRequestID(inputID)
	content, err := buildControlPayload(requestID, codewhaleControlHeader{
		ToolName: contracts.CodewhaleToolRequestUserInput, ToolUseID: inputID, Input: payload.Request,
	}, env.raw)
	if err != nil {
		slog.Error("codewhale marshal question", "agent_id", a.AgentID(), "error", err)
		a.declineUnpublishedQuestion(inputID, payload.Request)
		return
	}
	if !a.publishControl(requestID, env.TurnID, content) {
		a.declineUnpublishedQuestion(inputID, payload.Request)
	}
}

// declineUnpublishedQuestion answers a question no reader will ever see, so
// the turn does not wait for it.
func (a *Agent) declineUnpublishedQuestion(inputID string, request json.RawMessage) {
	threadID := a.currentThreadID()
	answers := declinedAnswers(request, "LeapMux could not show this question to the user.")
	go func() {
		if err := a.postUserInput(threadID, inputID, userInputBody(answers)); err != nil {
			slog.Warn("codewhale decline an unpublished question", "agent_id", a.AgentID(), "input_id", inputID, "error", err)
		}
	}()
}

// handleUserInputSettled retires the card of a question the runtime settled:
// answered through another client, or cancelled when its turn ended.
func (a *Agent) handleUserInputSettled(env codewhaleEnvelope) {
	var payload userInputEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.inputID() == "" {
		return
	}
	a.withdrawControl(userInputRequestID(payload.inputID()))
}

// withdrawQuestionOfCall retires the card of the question that a call asked,
// once the call ended. The question's id is its call's id.
//
// The call ends when the reader answered, which retired the card already, and
// also when the runtime's wait for an answer ran out (`[tools]
// user_input_timeout_seconds`, 300 s by default). The runtime then fails the
// call and states the timeout in a status item, but it keeps the question's
// registration until the turn ends: a late answer returns 200 and reaches no
// model. Without this the card would take that answer and report it delivered.
func (a *Agent) withdrawQuestionOfCall(callID string) {
	a.withdrawControl(userInputRequestID(callID))
}

// userInputBody is the body of POST /v1/user-input/{thread}/{id}.
func userInputBody(answers []userInputAnswer) map[string]any {
	return map[string]any{contracts.CodewhaleReplyFieldAnswers: answers}
}

// userInputRequest is the `request` of a user_input event: the questions the
// model asked. contract_tags_test.go pins its tags, and those of the two types
// below, to the contract, which the browser plugin reads the same names from.
type userInputRequest struct {
	Questions []questionRecord `json:"questions"`
}

// questionRecord is the part of one question that an answer addresses.
type questionRecord struct {
	ID       string           `json:"id"`
	Question string           `json:"question"`
	Header   string           `json:"header"`
	Options  []questionOption `json:"options"`
}

// questionOption is one choice of a question.
type questionOption struct {
	Label string `json:"label"`
}

// answerLabel is the label an answer of this value carries: the option's own
// label when the value is one, and the free-text label otherwise.
func (q questionRecord) answerLabel(value string) string {
	for _, option := range q.Options {
		if option.Label == value {
			return option.Label
		}
	}
	return contracts.CodewhaleAnswerLabelOther
}

// requestQuestions reads the questions of a user_input request.
func requestQuestions(request json.RawMessage) []questionRecord {
	var body userInputRequest
	if len(request) == 0 || json.Unmarshal(request, &body) != nil {
		return nil
	}
	return body.Questions
}

// declinedAnswers answers every question with the same free-text refusal.
func declinedAnswers(request json.RawMessage, text string) []userInputAnswer {
	questions := requestQuestions(request)
	answers := make([]userInputAnswer, 0, len(questions))
	for _, question := range questions {
		if question.ID == "" {
			continue
		}
		answers = append(answers, userInputAnswer{ID: question.ID, Label: contracts.CodewhaleAnswerLabelOther, Value: text})
	}
	return answers
}

// --- publication ---

// publishControl stores and broadcasts one control request, and reports whether
// it did. A request already published keeps its card: the stream can deliver an
// event twice across a reconnect.
func (a *Agent) publishControl(requestID, turnID string, payload []byte) bool {
	a.Mu.Lock()
	if _, pending := a.controls[requestID]; pending {
		a.Mu.Unlock()
		return true
	}
	if a.controls == nil {
		a.controls = make(map[string]codewhalePendingControl)
	}
	a.controls[requestID] = codewhalePendingControl{turnID: turnID}
	threadID := a.threadID
	a.Mu.Unlock()
	if err := a.sink.PublishControlRequest(agent.ControlRequest{
		RequestID:      requestID,
		Payload:        payload,
		AgentSessionID: threadID,
	}); err != nil {
		slog.Error("codewhale publish control request", "agent_id", a.AgentID(), "request_id", requestID, "error", err)
		a.forgetControl(requestID)
		return false
	}
	return true
}

// forgetControl drops a pending request and reports whether it was there. The
// answer path and the event path both retire a request, and only the one that
// took the record acts.
func (a *Agent) forgetControl(requestID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	_, pending := a.controls[requestID]
	delete(a.controls, requestID)
	return pending
}

// withdrawControl retires the card of one request that the runtime settled.
func (a *Agent) withdrawControl(requestID string) {
	if a.forgetControl(requestID) {
		a.sink.CancelControlRequest(requestID)
	}
}

// withdrawControlsOfTurn retires every card that belongs to a turn that ended.
func (a *Agent) withdrawControlsOfTurn(turnID string) {
	a.Mu.Lock()
	var stale []string
	for requestID, control := range a.controls {
		if turnID == "" || control.turnID == "" || control.turnID == turnID {
			stale = append(stale, requestID)
		}
	}
	for _, requestID := range stale {
		delete(a.controls, requestID)
	}
	a.Mu.Unlock()
	for _, requestID := range stale {
		a.sink.CancelControlRequest(requestID)
	}
}

// withdrawAllControls retires every card, for a process that exited.
func (a *Agent) withdrawAllControls() {
	a.withdrawControlsOfTurn("")
}

// --- answers ---

// SendRawInput posts a reply frame to its route, or runs an interrupt frame.
//
// ResolveControlResponse built the frame from the stored request, so the frame
// carries every id its route needs. A frame whose request is no longer pending
// is refused: the runtime settled it already, and its card is gone.
func (a *Agent) SendRawInput(raw []byte) error {
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	var frame codewhaleReplyFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return fmt.Errorf("decode the Codewhale reply frame: %w", err)
	}
	switch frame.Frame {
	case contracts.CodewhaleReplyFrameInterrupt:
		return a.Interrupt()
	case contracts.CodewhaleReplyFrameApproval:
		return a.sendApprovalReply(frame)
	case contracts.CodewhaleReplyFrameUserInput:
		return a.sendUserInputReply(frame)
	default:
		return fmt.Errorf("the Codewhale runtime takes no raw input of the frame %q", frame.Frame)
	}
}

// errControlNotPending reports an answer to a request the runtime already
// settled.
var errControlNotPending = errors.New("the Codewhale control request is no longer pending")

func (a *Agent) sendApprovalReply(frame codewhaleReplyFrame) error {
	if frame.ApprovalID == "" {
		return fmt.Errorf("the Codewhale approval reply identifies no approval")
	}
	requestID := approvalRequestID(frame.ApprovalID)
	if !a.controlPending(requestID) {
		return errControlNotPending
	}
	if err := a.postApproval(frame.ApprovalID, approvalBody(frame.Decision)); err != nil {
		if providerkit.IsHTTPStatus(err, httpStatusNotFound) {
			a.withdrawControl(requestID)
			return errControlNotPending
		}
		return err
	}
	a.forgetControl(requestID)
	return nil
}

func (a *Agent) sendUserInputReply(frame codewhaleReplyFrame) error {
	if frame.ThreadID == "" || frame.InputID == "" {
		return fmt.Errorf("the Codewhale answer identifies no question")
	}
	requestID := userInputRequestID(frame.InputID)
	if !a.controlPending(requestID) {
		return errControlNotPending
	}
	answers := frame.Answers
	if answers == nil {
		answers = []userInputAnswer{}
	}
	if err := a.postUserInput(frame.ThreadID, frame.InputID, userInputBody(answers)); err != nil {
		if providerkit.IsHTTPStatus(err, httpStatusNotFound) {
			a.withdrawControl(requestID)
			return errControlNotPending
		}
		return err
	}
	a.forgetControl(requestID)
	return nil
}

// controlPending reports whether a request is still waiting for an answer.
func (a *Agent) controlPending(requestID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	_, pending := a.controls[requestID]
	return pending
}

// --- resolution (pure) ---

// resolveControlReply turns the reader's neutral answer into the reply frame
// for the stored request. It reads nothing but its arguments.
//
// feedback is a deny reason the runtime's approval route cannot carry. The
// service queues it as the reader's next message instead.
func resolveControlReply(requestPayload, response []byte) (frame codewhaleReplyFrame, feedback string, ok bool) {
	requestID, behavior, message, decoded := agent.DecodeControlBehavior(response)
	if !decoded || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		return codewhaleReplyFrame{}, "", false
	}
	var stored struct {
		RequestID string                 `json:"request_id"`
		Request   codewhaleControlHeader `json:"request"`
	}
	if err := json.Unmarshal(requestPayload, &stored); err != nil {
		return codewhaleReplyFrame{}, "", false
	}
	if requestID != "" && stored.RequestID != "" && requestID != stored.RequestID {
		slog.Warn("codewhale control response addressed another request", "answered", requestID, "stored", stored.RequestID)
		return codewhaleReplyFrame{}, "", false
	}
	env, found := storedControlEvent(requestPayload)
	if !found {
		return codewhaleReplyFrame{}, "", false
	}
	switch env.Event {
	case contracts.CodewhaleEventApprovalRequired:
		var payload approvalEventPayload
		if json.Unmarshal(env.Payload, &payload) != nil || payload.approvalID() == "" {
			return codewhaleReplyFrame{}, "", false
		}
		frame = codewhaleReplyFrame{Frame: contracts.CodewhaleReplyFrameApproval, ApprovalID: payload.approvalID()}
		if behavior == agent.ControlBehaviorAllow {
			frame.Decision = contracts.CodewhaleDecisionAllow
			return frame, "", true
		}
		frame.Decision = contracts.CodewhaleDecisionDeny
		if message != "" {
			// The RAW reason, not the trimmed one: it becomes the reader's own
			// message, and LeapMux delivers a typed message byte for byte. The
			// trimmed value only decides whether a reason exists.
			var original agent.ControlBehaviorEnvelope
			if json.Unmarshal(response, &original) == nil {
				feedback = original.Response.Response.Message
			}
		}
		return frame, feedback, true
	case contracts.CodewhaleEventUserInputRequired:
		var payload userInputEventPayload
		if json.Unmarshal(env.Payload, &payload) != nil || payload.inputID() == "" || env.ThreadID == "" {
			return codewhaleReplyFrame{}, "", false
		}
		frame = codewhaleReplyFrame{Frame: contracts.CodewhaleReplyFrameUserInput, ThreadID: env.ThreadID, InputID: payload.inputID()}
		if behavior == agent.ControlBehaviorDeny {
			// The runtime has no decline route, so every question is answered with a
			// free-text refusal the model can read and act on. The contract holds the
			// default, which the browser reads back as a bare decline.
			text := contracts.CodewhaleAnswerTextDeclined
			if message != "" {
				text = message
			}
			frame.Answers = declinedAnswers(payload.Request, text)
			frame.Declined = true
			return frame, "", true
		}
		answers, valid := answersFromResponse(response, payload.Request)
		if !valid {
			return codewhaleReplyFrame{}, "", false
		}
		frame.Answers = answers
		return frame, "", true
	default:
		return codewhaleReplyFrame{}, "", false
	}
}

// answersFromResponse reads the answers the reader chose.
//
// The browser plugin sends the runtime's own answer list under
// `updatedInput.answers`. The shared question control's map, keyed by question
// text, is read too, so an answer from either surface reaches the runtime. Each
// value of that map becomes ONE answer: the map joins a multiple choice into one
// string and cannot say which options it held, so only a value that equals an
// option's label keeps that label, and anything else is free text.
func answersFromResponse(response []byte, request json.RawMessage) ([]userInputAnswer, bool) {
	var envelope struct {
		Response struct {
			Response struct {
				UpdatedInput struct {
					Answers json.RawMessage `json:"answers"`
				} `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, false
	}
	raw := envelope.Response.Response.UpdatedInput.Answers
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var list []userInputAnswer
	if json.Unmarshal(raw, &list) == nil {
		answers := make([]userInputAnswer, 0, len(list))
		for _, answer := range list {
			if strings.TrimSpace(answer.ID) == "" {
				return nil, false
			}
			answers = append(answers, answer)
		}
		return answers, true
	}
	var byText map[string]string
	if json.Unmarshal(raw, &byText) != nil {
		return nil, false
	}
	var answers []userInputAnswer
	for _, question := range requestQuestions(request) {
		value, found := byText[question.Question]
		if !found {
			value, found = byText[question.Header]
		}
		if !found || question.ID == "" {
			continue
		}
		answers = append(answers, userInputAnswer{ID: question.ID, Label: question.answerLabel(value), Value: value})
	}
	return answers, true
}
