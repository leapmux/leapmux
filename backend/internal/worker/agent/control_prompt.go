package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/jsonfield"
)

// ControlRequestInput is one control request as a provider reads it.
//
// A provider reads its own payload. Some prompts state part of their content
// elsewhere, and the two loaders read it when the provider asks:
//
//   - Source reads the transcript row that the request came from (its
//     source seq): Pi and Oh My Pi read a dialog's question from it, and
//     Gemini its plan.
//   - SpanRequest reads the request row of one tool call: an Agent Client
//     Protocol permission states its tool call in a compact form, and the
//     arguments live on that call's own row.
//
// Each returns the row as ResolveMessageContent resolves it, with the
// provider section of its supplement, and false when no such row exists.
type ControlRequestInput struct {
	Payload        []byte
	AgentSessionID string
	Source         func() (MessageContent, bool)
	SpanRequest    func(spanID string) (MessageContent, bool)
}

// NewControlRequestInput is the input for payload alone: both loaders find
// no row. A test and a caller with no transcript start from it.
func NewControlRequestInput(payload []byte) ControlRequestInput {
	return ControlRequestInput{
		Payload:     payload,
		Source:      func() (MessageContent, bool) { return MessageContent{}, false },
		SpanRequest: func(string) (MessageContent, bool) { return MessageContent{}, false },
	}
}

// ControlAnswerInput is one decision about one control request.
type ControlAnswerInput struct {
	Request ControlRequestInput
	// RequestID is the stored id of the request, which the answer echoes.
	RequestID string
	// Prompt is what ReadControlRequest read from the same request.
	Prompt   *leapmuxv1.ControlPrompt
	Decision *leapmuxv1.ControlDecision
}

// ControlAnswer is a provider's answer to one control request: the content
// that the Worker claims and records for the request instance.
// ResolveControlResponse then turns that content into the runtime's own
// answer.
type ControlAnswer struct {
	Content []byte
	// DeliveredDecision supplies the neutral decision when the runtime
	// receives a different order or shape than the caller submitted.
	DeliveredDecision *leapmuxv1.ControlDecision
	// PlanApproval is the LeapMux settings that a plan approval applies.
	PlanApproval *leapmuxv1.PlanApprovalSettings
	// FollowUp is text that the Worker sends as a message after the answer,
	// for a runtime whose answer cannot carry the user's feedback.
	FollowUp string
}

// ErrInvalidControlDecision is the cause of a decision that does not answer
// its prompt: another kind, or an option that the prompt does not offer.
var ErrInvalidControlDecision = errors.New("the decision does not answer the control request")

// InvalidControlDecision returns ErrInvalidControlDecision with a reason.
func InvalidControlDecision(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidControlDecision, fmt.Sprintf(format, args...))
}

// AllowDenyOptions are LeapMux's own answers to a permission prompt whose
// runtime states no options: allow once, and deny. Their ids
// (contracts.ControlAnswerOption*) are also the refusal that a provider adds
// to a runtime's list with no refusal (WithRefusal).
func AllowDenyOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: contracts.ControlAnswerOptionAllow, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE},
		{OptionId: contracts.ControlAnswerOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE},
	}
}

// GenericPermissionPrompt is the prompt of a request that the provider does
// not recognize but can still answer: LeapMux's allow and deny, with no
// arguments. A provider whose answer to it does not reach its runtime reads
// such a request as UnrecognizedControlPrompt instead. The arguments
// stay out on purpose: the answer sends the request's own input back, and a
// prompt that drew the whole envelope as arguments would show one part of
// the payload while the answer sent another.
func GenericPermissionPrompt() *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{
		Permission: &leapmuxv1.PermissionPrompt{Options: AllowDenyOptions()},
	}}
}

// WithEditorPurpose states the editor purpose of prompt, so every client
// reads one: a purpose that the provider stated stays, and otherwise the kind
// decides it:
//
//   - A question takes the typed text as an answer. A client still offers
//     none for a question whose allow_custom_text is false.
//   - An MCP elicitation and a dialog draw their own fields and buttons, so
//     the text is no part of them.
//   - Any other request takes the text as the reason of a refusal.
func WithEditorPurpose(prompt *leapmuxv1.ControlPrompt) *leapmuxv1.ControlPrompt {
	if prompt == nil || prompt.GetEditorPurpose() != leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_UNSPECIFIED {
		return prompt
	}
	switch prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		prompt.EditorPurpose = leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER
	case *leapmuxv1.ControlPrompt_Elicitation, *leapmuxv1.ControlPrompt_Dialog, *leapmuxv1.ControlPrompt_Unreadable:
		prompt.EditorPurpose = leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE
	default:
		prompt.EditorPurpose = leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK
	}
	return prompt
}

// ReadControlPrompt is the prompt that the Worker records and sends for one
// control request, as every client draws it:
//
//   - A payload that is not a JSON object reaches no provider: its prompt
//     states the fault (UnreadableControlPrompt).
//   - Otherwise the provider reads the payload, and a provider that returns
//     no prompt states the generic one, so a client always has a prompt to
//     draw.
//
// Every prompt states its editor purpose (WithEditorPurpose).
func ReadControlPrompt(plugin Provider, input ControlRequestInput) *leapmuxv1.ControlPrompt {
	if unreadable, ok := UnreadableControlPrompt(input.Payload); ok {
		return unreadable
	}
	prompt := plugin.ReadControlRequest(input)
	if prompt.GetKind() == nil {
		prompt = GenericPermissionPrompt()
	}
	return WithEditorPurpose(prompt)
}

// UnreadableControlPrompt returns the prompt of a request whose payload is
// not a JSON object, and false for a payload that a provider can read. No
// provider reads such a payload: it states no option and no decision
// vocabulary, so the prompt offers no answer, and the composer's text is a
// message.
func UnreadableControlPrompt(payload []byte) (*leapmuxv1.ControlPrompt, bool) {
	fault := controlPayloadFault(payload)
	if fault == leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED {
		return nil, false
	}
	return controlFaultPrompt(fault), true
}

// UnrecognizedControlPrompt is the prompt of a request whose payload is a
// JSON object that its provider does not recognize, so the provider can build
// no answer that its runtime reads. Like the prompt of a payload that is no
// JSON object (UnreadableControlPrompt), it offers no answer: a client states
// the fault and offers a stop, and AnswerControl refuses every decision.
func UnrecognizedControlPrompt() *leapmuxv1.ControlPrompt {
	return controlFaultPrompt(leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNRECOGNIZED)
}

// controlFaultPrompt is the prompt of a request that the Worker cannot read
// for the reason fault. The request takes no text, so the composer's text is
// a message.
func controlFaultPrompt(fault leapmuxv1.ControlPayloadFault) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{
		Kind:          &leapmuxv1.ControlPrompt_Unreadable{Unreadable: fault},
		EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE,
	}
}

// controlPayloadFault is why payload cannot be read as a JSON object, or
// UNSPECIFIED for an object.
func controlPayloadFault(payload []byte) leapmuxv1.ControlPayloadFault {
	if !json.Valid(payload) {
		return leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_MALFORMED
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_NOT_AN_OBJECT
	}
	return leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED
}

func isJSONObject(payload []byte) bool {
	return controlPayloadFault(payload) == leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED
}

// PermissionDecisionOf returns the permission decision of input and the
// option that it chooses, checked against the options of its prompt. The
// feedback is the reason of a refusal, so only a refusing option takes it: no
// runtime's answer has a place for a reason beside an approval.
func PermissionDecisionOf(input ControlAnswerInput) (*leapmuxv1.PermissionDecision, *leapmuxv1.PermissionOption, error) {
	decision := input.Decision.GetPermission()
	prompt := input.Prompt.GetPermission()
	if decision == nil || prompt == nil {
		return nil, nil, InvalidControlDecision("a permission prompt takes a permission decision")
	}
	i := slices.IndexFunc(prompt.GetOptions(), func(option *leapmuxv1.PermissionOption) bool {
		return option.GetOptionId() == decision.GetOptionId()
	})
	if i < 0 {
		return nil, nil, InvalidControlDecision("the prompt offers no option %q", decision.GetOptionId())
	}
	option := prompt.GetOptions()[i]
	if decision.GetFeedback() != "" && !IsRefusalOption(option) {
		return nil, nil, InvalidControlDecision("feedback is the reason of a refusal, and %q does not refuse the call", option.GetOptionId())
	}
	return decision, option, nil
}

// IsAllowOption reports whether option allows the call.
func IsAllowOption(option *leapmuxv1.PermissionOption) bool {
	switch option.GetKind() {
	case leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS:
		return true
	default:
		return false
	}
}

// IsRefusalOption reports whether option refuses the call.
func IsRefusalOption(option *leapmuxv1.PermissionOption) bool {
	switch option.GetKind() {
	case leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS:
		return true
	default:
		return false
	}
}

// WithRefusal completes a runtime's option list so that a client can always
// refuse: an empty list becomes LeapMux's allow and deny, and a list with no
// refusing option gains refusal, a provider's own way to refuse there.
func WithRefusal(options []*leapmuxv1.PermissionOption, refusal *leapmuxv1.PermissionOption) []*leapmuxv1.PermissionOption {
	if len(options) == 0 {
		return AllowDenyOptions()
	}
	if slices.ContainsFunc(options, IsRefusalOption) {
		return options
	}
	return append(slices.Clone(options), refusal)
}

// ReadControlRequest defaults to the generic permission prompt.
func (ProviderDefaults) ReadControlRequest(ControlRequestInput) *leapmuxv1.ControlPrompt {
	return GenericPermissionPrompt()
}

// AnswerControlRequest defaults to the answer of the generic permission
// prompt: the neutral allow, which sends the request's input back unchanged,
// or the neutral deny.
func (ProviderDefaults) AnswerControlRequest(input ControlAnswerInput) (ControlAnswer, error) {
	return AnswerAllowDeny(input)
}

// AnswerAllowDeny answers a permission prompt of LeapMux's allow and deny
// options with the neutral envelope.
func AnswerAllowDeny(input ControlAnswerInput) (ControlAnswer, error) {
	decision, option, err := PermissionDecisionOf(input)
	if err != nil {
		return ControlAnswer{}, err
	}
	if IsAllowOption(option) {
		return ControlAnswer{Content: AllowResponse(input.RequestID, ControlToolInput(input.Request.Payload))}, nil
	}
	return ControlAnswer{Content: DenyResponse(input.RequestID, decision.GetFeedback())}, nil
}

// controlRequestField returns one field of payload.request, the request
// shape that Claude Code's control protocol defined and the neutral answers
// echo. Each field reads alone, so a sibling of the wrong type never hides it.
func controlRequestField(payload []byte, name string) json.RawMessage {
	value, err := jsonfield.Get(payload, "request", name)
	if err != nil {
		return nil
	}
	return value
}

// ControlToolName returns payload.request.tool_name, or "" when it is
// absent or not a string.
func ControlToolName(payload []byte) string {
	return controlRequestString(payload, "tool_name")
}

// ControlToolUseID returns payload.request.tool_use_id, or "" when it is
// absent or not a string.
func ControlToolUseID(payload []byte) string {
	return controlRequestString(payload, "tool_use_id")
}

// controlRequestString returns one text field of payload.request, or "" when
// it is absent or not a string.
func controlRequestString(payload []byte, name string) string {
	var value string
	if json.Unmarshal(controlRequestField(payload, name), &value) != nil {
		return ""
	}
	return value
}

// ControlToolInput returns payload.request.input as a JSON object, or {}
// when the request states none or states a value that is not an object.
func ControlToolInput(payload []byte) json.RawMessage {
	input := controlRequestField(payload, "input")
	if !isJSONObject(input) {
		return json.RawMessage("{}")
	}
	return input
}

// ControlResponseEnvelope wraps response in the control_response envelope
// that every neutral answer travels in.
func ControlResponseEnvelope(requestID string, response any) []byte {
	return mustJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	})
}

// ControlBehaviorResponse is the inner response object of a neutral allow or
// deny control answer. See ControlBehaviorEnvelope.
type ControlBehaviorResponse struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
	Choice       string          `json:"choice,omitempty"`
}

// DenyMessage returns message, or contracts.ControlAnswerMessageRejectedByUser
// when message is empty, because Claude Code turns a deny into an error tool
// result and the Anthropic API refuses an empty one.
func DenyMessage(message string) string {
	if message == "" {
		return contracts.ControlAnswerMessageRejectedByUser
	}
	return message
}

// AllowResponse is the neutral allow, with the input that the call runs with.
func AllowResponse(requestID string, updatedInput json.RawMessage) []byte {
	return ControlResponseEnvelope(requestID, ControlBehaviorResponse{
		Behavior:     contracts.ControlAnswerBehaviorAllow,
		UpdatedInput: updatedInput,
	})
}

// DenyResponse is the neutral deny. An empty message states
// contracts.ControlAnswerMessageRejectedByUser (DenyMessage).
func DenyResponse(requestID, message string) []byte {
	return ControlResponseEnvelope(requestID, ControlBehaviorResponse{
		Behavior: contracts.ControlAnswerBehaviorDeny,
		Message:  DenyMessage(message),
	})
}

// JSONRPCResult is a JSON-RPC result that answers the stored request
// requestID. The id stays the Worker's own key of the request, as a string:
// ResolveControlResponse restores the runtime's id from the stored request.
func JSONRPCResult(requestID string, result any) []byte {
	return mustJSON(map[string]any{"jsonrpc": "2.0", "id": requestID, "result": result})
}

func mustJSON(v any) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("agent: encode JSON: %v", err))
	}
	return encoded
}
