package agent

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
)

// Copilot's native control answers.
//
// The browser sends the neutral approve/reject envelope that every provider's control
// surface produces. This file turns one of those into the exact object the runtime's
// own remote procedure call takes, so the native vocabulary stays inside the provider
// and the browser states a decision rather than a wire value.
//
// Every decision word comes from a verified probe. CP-007 recorded what the installed
// runtime accepts, and it also recorded what it REFUSES: the completion value
// `approved` is in the schema and the permission processor rejects it, so
// `approve-once` is the word an approval sends. The four live in
// `contracts/copilot-protocol.json`, because the browser reads the same words back
// out of a saved answer to say what the reader decided.
const (
	copilotDecisionApproveOnce        = contracts.CopilotDecisionApproveOnce
	copilotDecisionApproveForSession  = contracts.CopilotDecisionApproveForSession
	copilotDecisionApproveForLocation = contracts.CopilotDecisionApproveForLocation
	copilotDecisionReject             = contracts.CopilotDecisionReject
)

// copilotLocationKeyField is the native field that names the project an approval
// applies to. Only the running agent can fill it, because only the runtime can
// resolve a working directory into its own key. See copilotCompleteLocationKey.
const copilotLocationKeyField = "locationKey"

// copilotPermissionRequest is the part of a permission request that decides which
// session-wide approval rule an "allow for this session" answer may carry.
type copilotPermissionRequest struct {
	Kind                    string `json:"kind"`
	CanOfferSessionApproval bool   `json:"canOfferSessionApproval"`
	ServerName              string `json:"serverName"`
	ToolName                string `json:"toolName"`
	Commands                []struct {
		Identifier string `json:"identifier"`
	} `json:"commands"`
}

// copilotSessionApproval builds the approval rule for a session-wide allow.
//
// A bare `approve-for-session` did not retain the read approval in CP-007, so the
// rule is required rather than optional. A request whose rule this build cannot
// construct answers nil, and the caller then sends a single approval instead of
// claiming a scope the runtime would not apply.
func copilotSessionApproval(request copilotPermissionRequest) map[string]any {
	switch request.Kind {
	case "read":
		return map[string]any{"kind": "read"}
	case "write":
		if !request.CanOfferSessionApproval {
			return nil
		}
		return map[string]any{"kind": "write"}
	case "shell":
		if !request.CanOfferSessionApproval || len(request.Commands) == 0 {
			return nil
		}
		identifiers := make([]string, 0, len(request.Commands))
		for _, command := range request.Commands {
			if command.Identifier == "" {
				return nil
			}
			identifiers = append(identifiers, command.Identifier)
		}
		return map[string]any{"kind": "commands", "commandIdentifiers": identifiers}
	case "mcp":
		if request.ServerName == "" {
			return nil
		}
		return map[string]any{"kind": "mcp", "serverName": request.ServerName, "toolName": request.ToolName}
	default:
		return nil
	}
}

// copilotControlDecision is the browser's answer, as the neutral envelope carries it.
type copilotControlDecision struct {
	allow bool
	// message is the rejection reason, already normalized: the placeholder that a
	// bare deny carries collapses to empty, so an untyped refusal stays untyped.
	message string
	// scope states how long an approval lasts. Empty is a single approval.
	scope string
	// answer is the text a question control submitted, and wasFreeform states
	// whether the user typed it or selected one of the offered choices.
	answer      string
	wasFreeform bool
}

// copilotNativeAnswer maps one neutral decision onto the native response value for
// the control kind that the stored request states.
//
// The second result reports whether this build can express the decision. A false
// answer withholds the forward, which leaves the runtime waiting and the request
// answerable rather than sending it a value it would refuse.
func copilotNativeAnswer(eventType string, data json.RawMessage, decision copilotControlDecision) (any, bool) {
	switch eventType {
	case contracts.CopilotEventPermissionRequested:
		if !decision.allow {
			// The feedback rides the decision. An empty message stays empty rather
			// than becoming a sentence the user did not write.
			answer := map[string]any{"kind": copilotDecisionReject}
			if decision.message != "" {
				answer["feedback"] = decision.message
			}
			return answer, true
		}
		scoped := decision.scope == contracts.CopilotApprovalScopeSession || decision.scope == contracts.CopilotApprovalScopeProject
		if !scoped {
			return map[string]any{"kind": copilotDecisionApproveOnce}, true
		}
		var request struct {
			PermissionRequest copilotPermissionRequest `json:"permissionRequest"`
		}
		if json.Unmarshal(data, &request) != nil {
			return nil, false
		}
		approval := copilotSessionApproval(request.PermissionRequest)
		if approval == nil {
			return nil, false
		}
		if decision.scope == contracts.CopilotApprovalScopeSession {
			return map[string]any{"kind": copilotDecisionApproveForSession, "approval": approval}, true
		}
		// The project key is absent here on purpose. This resolution is pure, and only
		// the runtime can turn a working directory into its own location key, so the
		// running agent fills the field before it sends the answer.
		return map[string]any{"kind": copilotDecisionApproveForLocation, "approval": approval}, true

	case contracts.CopilotEventUserInputRequested:
		// A question has one native answer field, and no decline. A refusal is the
		// text the user typed instead of an answer, because an invented decline
		// would answer a question the user refused to answer. An explicit empty
		// answer stays empty: the runtime accepts it, and the user chose it.
		if decision.allow {
			return map[string]any{"answer": decision.answer, "wasFreeform": decision.wasFreeform}, true
		}
		return map[string]any{"answer": decision.message, "wasFreeform": true}, true

	case contracts.CopilotEventExitPlanModeRequested:
		if !decision.allow {
			answer := map[string]any{"approved": false}
			if decision.message != "" {
				answer["feedback"] = decision.message
			}
			return answer, true
		}
		var request struct {
			RecommendedAction string `json:"recommendedAction"`
		}
		answer := map[string]any{"approved": true}
		if json.Unmarshal(data, &request) == nil && request.RecommendedAction != "" {
			answer["selectedAction"] = request.RecommendedAction
		}
		return answer, true

	case contracts.CopilotEventElicitationRequested:
		// A refusal declines rather than submitting an empty form. An acceptance
		// carries the form's own content, which copilotElicitationAnswer reads.
		if !decision.allow {
			return map[string]any{"action": contracts.MCPElicitationActionDecline}, true
		}
		return nil, false

	default:
		return nil, false
	}
}

// copilotControlResponseContent rewrites one neutral control response into the
// envelope that copilotAgent.SendRawInput forwards.
//
// The outer shape stays the shared one, so the request identity and the storage path
// are unchanged. Only the inner value becomes native.
func copilotControlResponseContent(requestID string, answer any) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   answer,
		},
	})
}

// copilotResolveControlAnswer translates the browser's decision for one stored
// request. It reports false when the stored request is not a Copilot control event,
// so the caller can leave the response alone.
func copilotResolveControlAnswer(ctx ControlResponseContext) (ControlResponseResolution, bool) {
	eventType, data, ok := copilotControlEvent(ctx.RequestPayload)
	if !ok {
		return ControlResponseResolution{}, false
	}
	result := defaultControlResponseResolution(ctx)
	if eventType == contracts.CopilotEventElicitationRequested {
		if native, ok := copilotElicitationAnswer(ctx.ResponseContent); ok {
			content, err := copilotControlResponseContent(ctx.RequestID, native)
			if err != nil {
				result.Withhold = true
				return result, true
			}
			result.Content = content
			return result, true
		}
	}
	requestID, behavior, message, decoded := DecodeControlBehavior(ctx.ResponseContent)
	if !decoded || (behavior != ControlBehaviorAllow && behavior != ControlBehaviorDeny) {
		// Not a decision this build can read. Withholding leaves the runtime waiting
		// and the request answerable, which a frame it cannot parse would not.
		slog.Warn("Copilot control response carried no decision", "request_id", ctx.RequestID)
		result.Withhold = true
		return result, true
	}
	if requestID != "" && ctx.RequestID != "" && requestID != ctx.RequestID {
		slog.Warn("Copilot control response addressed another request", "answered", requestID, "stored", ctx.RequestID)
		result.Withhold = true
		return result, true
	}
	decision := copilotControlDecision{allow: behavior == ControlBehaviorAllow, message: message}
	copilotReadDecisionFields(ctx.ResponseContent, &decision)
	native, expressible := copilotNativeAnswer(eventType, data, decision)
	if !expressible {
		slog.Warn("Copilot cannot express this control decision", "event", eventType, "request_id", ctx.RequestID)
		result.Withhold = true
		return result, true
	}
	content, err := copilotControlResponseContent(ctx.RequestID, native)
	if err != nil {
		result.Withhold = true
		return result, true
	}
	result.Content = content
	if eventType == contracts.CopilotEventExitPlanModeRequested {
		result.PlanModeControl = PlanModeControlExit
	}
	return result, true
}

// copilotControlEvent reports the event type and data of a stored Copilot control request.
func copilotControlEvent(payload json.RawMessage) (string, json.RawMessage, bool) {
	for _, eventType := range []string{
		contracts.CopilotEventPermissionRequested,
		contracts.CopilotEventUserInputRequested,
		contracts.CopilotEventExitPlanModeRequested,
		contracts.CopilotEventElicitationRequested,
	} {
		if data, ok := copilotEventOfType(payload, eventType); ok {
			return eventType, data, true
		}
	}
	return "", nil, false
}

// copilotElicitationAnswer reads the shared elicitation form's own answer, which is
// already the native shape: an action and the content the form produced.
func copilotElicitationAnswer(content []byte) (any, bool) {
	var envelope struct {
		Response struct {
			Response struct {
				Action  string          `json:"action"`
				Content json.RawMessage `json:"content"`
			} `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(content, &envelope) != nil {
		return nil, false
	}
	answer := envelope.Response.Response
	switch answer.Action {
	case contracts.MCPElicitationActionAccept:
		if len(answer.Content) == 0 {
			return nil, false
		}
		return map[string]any{"action": answer.Action, "content": answer.Content}, true
	case contracts.MCPElicitationActionDecline, contracts.MCPElicitationActionCancel:
		return map[string]any{"action": answer.Action}, true
	default:
		return nil, false
	}
}

// copilotReadDecisionFields reads the fields a control surface adds beside the
// neutral behavior: the approval scope, and a question's own answer.
//
// Every field is optional, so a control that offers none leaves the decision as its
// behavior alone.
func copilotReadDecisionFields(content []byte, decision *copilotControlDecision) {
	var envelope struct {
		Response struct {
			Response struct {
				Scope       string `json:"scope"`
				Answer      string `json:"answer"`
				WasFreeform bool   `json:"wasFreeform"`
			} `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(content, &envelope) != nil {
		return
	}
	decision.scope = envelope.Response.Response.Scope
	decision.answer = envelope.Response.Response.Answer
	decision.wasFreeform = envelope.Response.Response.WasFreeform
}
