package acp

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// CancelOptionID is the option that LeapMux adds to a permission whose
// runtime offers no reject option, or no option at all. The protocol still
// accepts a refusal there, as the `cancelled` outcome, and the option is how a
// client chooses it. That outcome cancels the prompt turn, so the option ends
// the turn. The id carries a prefix that no runtime's option id uses.
const CancelOptionID = "leapmux:cancel"

// permissionParams reads the params of a session/request_permission. Each
// field is decoded on its own, so a malformed option list leaves the tool
// call readable.
func permissionParams(payload []byte) map[string]json.RawMessage {
	var request struct {
		Params map[string]json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(payload, &request)
	return request.Params
}

// PermissionToolCall returns the tool call that a permission request states,
// in its compact form, or nil for a request that states none.
func PermissionToolCall(payload []byte) map[string]json.RawMessage {
	var toolCall map[string]json.RawMessage
	if json.Unmarshal(permissionParams(payload)["toolCall"], &toolCall) != nil {
		return nil
	}
	return toolCall
}

// PermissionToolSpanID is the id of the tool call that a permission request
// is about, or "".
func PermissionToolSpanID(payload []byte) string {
	id, _ := providerkit.RawString(PermissionToolCall(payload)["toolCallId"])
	return id
}

// OptionKind maps an option kind of the protocol onto the neutral kinds.
// Any other kind is OTHER.
func OptionKind(kind string) leapmuxv1.PermissionOptionKind {
	switch kind {
	case contracts.ACPPermissionKindAllowOnce:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	case contracts.ACPPermissionKindAllowAlways:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS
	case contracts.ACPPermissionKindRejectOnce:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
	case contracts.ACPPermissionKindRejectAlways:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS
	default:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER
	}
}

// PermissionOptions reads the options that a permission request offers, in
// its order. An element that is no object, or that states no `optionId` as
// non-empty text, is no answer that the runtime accepts, so it is dropped. An
// option of an unknown kind keeps its runtime label, or its kind as the label
// when it states none.
func PermissionOptions(payload []byte) []*leapmuxv1.PermissionOption {
	var elements []json.RawMessage
	if json.Unmarshal(permissionParams(payload)["options"], &elements) != nil {
		return nil
	}
	var options []*leapmuxv1.PermissionOption
	for _, raw := range elements {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			continue
		}
		// The runtime accepts only the id of an option that it offered, so an
		// option that states no id as text is no answer.
		id, ok := providerkit.RawString(fields["optionId"])
		if !ok || id == "" {
			continue
		}
		kind, _ := providerkit.RawString(fields["kind"])
		name, _ := providerkit.RawString(fields["name"])
		option := &leapmuxv1.PermissionOption{OptionId: id, Kind: OptionKind(kind), Name: name}
		if option.Kind == leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER && name == "" {
			option.Name = kind
		}
		options = append(options, option)
	}
	return options
}

// ResolveToolCall merges a permission's compact tool call with the request
// row of the same call: the row's fields, overlaid by every field that the
// compact call states, with the two rawInput objects merged. A row of
// another call leaves the compact call alone.
func ResolveToolCall(toolCall map[string]json.RawMessage, request map[string]json.RawMessage) map[string]json.RawMessage {
	id, _ := providerkit.RawString(toolCall["toolCallId"])
	requestID, _ := providerkit.RawString(request["toolCallId"])
	if request == nil || id == "" || requestID != id {
		return toolCall
	}
	resolved := maps.Clone(request)
	for key, value := range toolCall {
		if providerkit.RawPresent(value) {
			resolved[key] = value
		}
	}
	var previous, current map[string]json.RawMessage
	if json.Unmarshal(request[contracts.ACPSupplementRequestRawInput], &previous) == nil && previous != nil &&
		json.Unmarshal(toolCall[contracts.ACPSupplementRequestRawInput], &current) == nil && current != nil {
		merged := maps.Clone(previous)
		maps.Copy(merged, current)
		encoded, err := json.Marshal(merged)
		if err == nil {
			resolved[contracts.ACPSupplementRequestRawInput] = encoded
		}
	}
	return resolved
}

// ReadPermissionPrompt reads one permission request, with its tool call
// merged from the call's own request row, where the arguments live. It
// returns nil for a request that states neither a tool call nor an option.
//
// The options are the runtime's own, and a list with no reject option, an
// empty list included, gains CancelOptionID (WithRefusal).
func ReadPermissionPrompt(input agent.ControlRequestInput) *leapmuxv1.PermissionPrompt {
	compact := PermissionToolCall(input.Payload)
	options := PermissionOptions(input.Payload)
	if compact == nil && len(options) == 0 {
		return nil
	}
	toolCall := compact
	if compact != nil {
		if row, ok := input.SpanRequest(PermissionToolSpanID(input.Payload)); ok {
			var request map[string]json.RawMessage
			if json.Unmarshal(row.Original, &request) == nil {
				toolCall = ResolveToolCall(compact, request)
			}
		}
	}
	kind, _ := providerkit.RawString(toolCall["kind"])
	title, _ := providerkit.RawString(toolCall["title"])
	if title == "" {
		title = kind
	}
	permission := &leapmuxv1.PermissionPrompt{Title: title, Options: WithRefusal(options)}
	if raw := toolCall[contracts.ACPSupplementRequestRawInput]; providerkit.RawPresent(raw) {
		permission.InputJson = string(raw)
	}
	if kind == contracts.ACPToolKindExecute {
		var rawInput map[string]json.RawMessage
		if json.Unmarshal(toolCall[contracts.ACPSupplementRequestRawInput], &rawInput) == nil {
			permission.Command, _ = providerkit.RawString(rawInput["command"])
		}
	}
	return permission
}

// WithRefusal completes a runtime's option list so that a client can always
// refuse: a list with no reject option, an empty list included, gains
// CancelOptionID.
//
// LeapMux adds no approval. The protocol's reply selects an option that the
// runtime offered, or states the `cancelled` outcome, and nothing else. So the
// only answer to a request that offers no option is CancelOptionID.
// agent.WithRefusal cannot serve this protocol: for an empty list it offers
// LeapMux's own allow and deny, which answer in the neutral envelope, and no
// resolution of the base turns that envelope into the protocol's reply.
func WithRefusal(options []*leapmuxv1.PermissionOption) []*leapmuxv1.PermissionOption {
	if slices.ContainsFunc(options, agent.IsRefusalOption) {
		return options
	}
	return append(options, &leapmuxv1.PermissionOption{
		OptionId: CancelOptionID,
		Kind:     leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
		Name:     "Cancel",
		EndsTurn: true,
	})
}

// ReadElicitation reads an MCP elicitation that the runtime relays as
// elicitation/create.
func ReadElicitation(payload []byte) (*leapmuxv1.ElicitationPrompt, bool) {
	var request struct {
		Method string                     `json:"method"`
		Params map[string]json.RawMessage `json:"params"`
	}
	if json.Unmarshal(payload, &request) != nil || request.Method != contracts.MCPElicitationMethodACP {
		return nil, false
	}
	return ElicitationFromParams(request.Params), true
}

// ElicitationFromParams reads the MCP form fields of an elicitation's
// params, for a runtime that relays one under another method.
func ElicitationFromParams(params map[string]json.RawMessage) *leapmuxv1.ElicitationPrompt {
	text := func(key string) string {
		value, _ := providerkit.RawString(params[key])
		return value
	}
	mode, ok := providerkit.RawString(params["mode"])
	if !ok {
		mode = contracts.MCPElicitationModeForm
	}
	prompt := &leapmuxv1.ElicitationPrompt{
		Mode:        mode,
		Message:     text("message"),
		Server:      text("server"),
		Url:         text("url"),
		Title:       text("title"),
		Description: text("description"),
	}
	if schema := params["requestedSchema"]; providerkit.RawPresent(schema) {
		prompt.SchemaJson = string(schema)
	}
	return prompt
}

// ReadControlRequest reads the base protocol's requests: an MCP elicitation,
// or a permission. A provider with prompts of its own reads those first and
// falls back to this.
func (Provider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	return ReadBaseControlRequest(input)
}

// ReadBaseControlRequest is the base reader, for a provider that overrides
// ReadControlRequest and falls back to it.
//
// Any other request reads as unrecognized. A runtime of the protocol reads
// only a JSON-RPC result for its request, and the answer to LeapMux's own
// Allow and Deny is the neutral envelope, which no resolution of the base
// rewrites into one: the runtime would receive bytes that it cannot read and
// go on waiting.
func ReadBaseControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if elicitation, ok := ReadElicitation(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: elicitation}}
	}
	if permission := ReadPermissionPrompt(input); permission != nil {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
	}
	return agent.UnrecognizedControlPrompt()
}

// RejectReason adds the user's reason for a refusal to the result of a
// permission answer, in a provider's own field, and returns the result. A
// provider without such a field passes nil, and the reason then follows the
// answer as a message of its own.
type RejectReason func(result map[string]any, reason string) map[string]any

// AnswerControlRequest answers the base protocol's requests.
func (Provider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	return AnswerBaseControlRequest(input, nil)
}

// AnswerBaseControlRequest is the base answer, for a provider that overrides
// AnswerControlRequest and falls back to it.
func AnswerBaseControlRequest(input agent.ControlAnswerInput, rejectReason RejectReason) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Elicitation:
		return agent.AnswerElicitation(input, nil)
	case *leapmuxv1.ControlPrompt_Permission:
		return AnswerPermission(input, rejectReason)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the Agent Client Protocol states no %T prompt", input.Prompt.GetKind())
	}
}

// AnswerPermission answers a permission prompt in the protocol's own reply:
//
//   - An option of the prompt is selected as it is: the protocol reads
//     nothing else. A provider that offers an option of its own rewrites the
//     selection in its resolution, as Kiro does for a consent scope.
//   - CancelOptionID answers with the `cancelled` outcome.
//   - A refusal's feedback rides in the provider's own field through
//     rejectReason, or else follows the answer as a message.
func AnswerPermission(input agent.ControlAnswerInput, rejectReason RejectReason) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	feedback := decision.GetFeedback()
	var result map[string]any
	if option.GetOptionId() == CancelOptionID {
		result = map[string]any{"outcome": map[string]any{"outcome": contracts.ACPPermissionOutcomeCancelled}}
	} else {
		result = map[string]any{"outcome": map[string]any{"outcome": contracts.ACPPermissionOutcomeSelected, "optionId": option.GetOptionId()}}
	}
	answer := agent.ControlAnswer{}
	if feedback != "" {
		if rejectReason != nil {
			result = rejectReason(result, feedback)
		} else {
			answer.FollowUp = feedback
		}
	}
	answer.Content = agent.JSONRPCResult(input.RequestID, result)
	return answer, nil
}

// SelectedOptionResult is the Agent Client Protocol answer that selects one
// permission option.
func SelectedOptionResult(requestID, optionID string) []byte {
	return agent.JSONRPCResult(requestID, map[string]any{
		"outcome": map[string]any{"outcome": contracts.ACPPermissionOutcomeSelected, "optionId": optionID},
	})
}
