package agent

import (
	"encoding/json"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ControlResponseContext is the pure provider input for interpreting a frontend
// control response. The service loads the stored control request once, extracts the
// shared metadata, and passes both payloads here; providers must not perform I/O.
type ControlResponseContext struct {
	RequestPayload  json.RawMessage
	ResponseContent []byte
	ToolName        string
}

// ControlResponseResolution is the provider-owned interpretation of a control
// response. The service executes side effects from this plan: it persists the
// structured control-response row, deletes control requests, mutates plan-mode
// settings, and forwards Content.
type ControlResponseResolution struct {
	Content []byte
	// RequestContext is the provider-pruned minimal request context persisted alongside the
	// native response so the frontend can render the answer AFTER the pending control request is
	// deleted (permission option names, question headers, the request method, ...). Provider code
	// owns the pruning -- what each wire shape keeps stays in that provider's resolver, never in
	// shared service code. Nil when the stored request is unavailable or unrecognized, in which
	// case the row persists with `request` omitted and the frontend degrades gracefully.
	RequestContext  json.RawMessage
	SelfDisplayed   bool
	PlanModeControl PlanModeControlKind
}

func defaultControlResponseResolution(ctx ControlResponseContext) ControlResponseResolution {
	return ControlResponseResolution{Content: ctx.ResponseContent}
}

func (noopProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	return defaultControlResponseResolution(ctx)
}

func (p codexProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		return res
	}
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	res.RequestContext = codexControlRequestContext(ctx.RequestPayload)
	return res
}

func (p claudeProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	res.SelfDisplayed = p.IsSelfDisplayingControlTool(ctx.ToolName)
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	// The tool name is all the frontend needs to render Claude's approve/reject/feedback answer;
	// the behavior itself lives in the native response payload.
	res.RequestContext = toolNameRequestContext(ctx.ToolName)
	return res
}

func (p piProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		return res
	}
	var req struct {
		Method string `json:"method"`
	}
	if !warnUnmarshal(ctx.RequestPayload, &req, "pi control response request") {
		return res
	}
	res.RequestContext = methodRequestContext(req.Method)
	return res
}

func (p acpProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		return res
	}

	var req struct {
		Method string `json:"method"`
		Type   string `json:"type"`
	}
	if !warnUnmarshal(ctx.RequestPayload, &req, "acp control response request") {
		return res
	}

	// Cursor's question / create-plan flows are keyed on the JSON-RPC method and are unique to
	// Cursor's ACP variant, so they stay Cursor-specific (a per-provider enum check) rather than a
	// registration hook shared across providers.
	if p.provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR {
		switch req.Method {
		case CursorMethodAskQuestion:
			res.RequestContext = cursorQuestionRequestContext(ctx.RequestPayload)
			return res
		case CursorMethodCreatePlan:
			if transformed, ok := transformCursorControlResponse(ctx.RequestPayload, ctx.ResponseContent); ok {
				// Persist the TRANSFORMED outcome that was forwarded to the agent -- the frontend
				// renders the plan decision (Accept / Reject / Cancel) from result.outcome alone.
				res.Content = transformed
				res.RequestContext = methodRequestContext(req.Method)
				return res
			}
		}
	}

	// The OpenCode-protocol `question.asked` request context dispatches through a registration-time
	// hook (set only for the providers that speak that protocol, see init()) rather than a provider
	// enum allowlist -- so the membership lives at the single registration site, mirroring the
	// frontend's registerOpenCodeProtocolProvider, not a second source of truth that drifts.
	if p.questionRequestContext != nil && req.Type == "question.asked" {
		res.RequestContext = p.questionRequestContext(ctx.RequestPayload)
		return res
	}

	res.RequestContext = acpPermissionRequestContext(ctx.RequestPayload)
	return res
}

// warnUnmarshal decodes JSON into v, logging a "<label> unmarshal failed" warning and returning
// false on error (the caller then returns its own zero result). The single home for the
// decode-and-warn preamble the control-response resolvers and answer summaries all repeat.
func warnUnmarshal(data []byte, v any, label string) bool {
	if err := json.Unmarshal(data, v); err != nil {
		slog.Warn(label+" unmarshal failed", "error", err)
		return false
	}
	return true
}

// marshalControlRequestContext encodes a provider-pruned request-context projection into the
// bytes persisted under the synthetic row's `request` field, returning nil (which omits `request`)
// when the value can't be encoded. The single home for the marshal-and-warn tail every context
// builder shares.
func marshalControlRequestContext(v any) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		slog.Warn("marshal control request context failed", "error", err)
		return nil
	}
	return encoded
}

// projectRequestContext decodes a stored control request into the pruned projection dst (a pointer to
// a projection struct), then re-marshals dst as the persisted request context -- the
// decode->warn->remarshal glue the per-provider question pruners share, so each caller's struct
// declaration stays the single spec of what survives. Nil when the payload doesn't parse
// (warnUnmarshal logs the reason).
func projectRequestContext(requestPayload []byte, dst any, label string) json.RawMessage {
	if !warnUnmarshal(requestPayload, dst, label) {
		return nil
	}
	return marshalControlRequestContext(dst)
}

// methodRequestContext is the pruned request context for a request whose only render-relevant
// field is its method (Codex approvals, Pi extension UI, Cursor create-plan): {"method": ...}.
// Nil when the method is blank.
func methodRequestContext(method string) json.RawMessage {
	if strings.TrimSpace(method) == "" {
		return nil
	}
	return marshalControlRequestContext(struct {
		Method string `json:"method"`
	}{Method: method})
}

// toolNameRequestContext is the pruned request context for a Claude-style control request (Claude
// permission tools, the synthesized Codex plan-mode prompt): {"request": {"tool_name": ...}}, which
// is all the frontend needs to render Approved / Rejected / feedback. Nil when the tool name is
// blank.
func toolNameRequestContext(toolName string) json.RawMessage {
	if strings.TrimSpace(toolName) == "" {
		return nil
	}
	var ctx struct {
		Request struct {
			ToolName string `json:"tool_name"`
		} `json:"request"`
	}
	ctx.Request.ToolName = toolName
	return marshalControlRequestContext(ctx)
}

// codexControlRequestContext prunes a stored Codex control request to the minimal context the
// frontend needs to render the answer: the question definitions for requestUserInput, the tool
// name for the synthesized plan-mode prompt, or just the method for a command-approval request.
// Nil when nothing render-relevant survives.
func codexControlRequestContext(requestPayload []byte) json.RawMessage {
	var req struct {
		Method  string `json:"method"`
		Request struct {
			ToolName string `json:"tool_name"`
		} `json:"request"`
	}
	if !warnUnmarshal(requestPayload, &req, "codex control request context") {
		return nil
	}
	if req.Method == "item/tool/requestUserInput" {
		return codexUserInputRequestContext(requestPayload)
	}
	if req.Method != "" {
		return methodRequestContext(req.Method)
	}
	// The plan-mode prompt is a synthesized Claude-style frame with no top-level method; its
	// request.tool_name (CodexPlanModePrompt) is what the frontend keys the plan answer off.
	return toolNameRequestContext(req.Request.ToolName)
}

// codexUserInputRequestContext keeps the requestUserInput question definitions (id + header) the
// frontend maps its answer values onto. It unmarshals the stored request into the projection shape
// and re-marshals it, so the struct declaration is the single spec of what survives.
func codexUserInputRequestContext(requestPayload []byte) json.RawMessage {
	var req struct {
		Method string `json:"method"`
		Params struct {
			Questions []struct {
				ID     string `json:"id,omitempty"`
				Header string `json:"header,omitempty"`
			} `json:"questions"`
		} `json:"params"`
	}
	return projectRequestContext(requestPayload, &req, "codex user input request context")
}

// ControlBehaviorEnvelope is the frontend's neutral approve/reject control-response envelope --
// {response:{request_id, response:{behavior, message}}}. The SINGLE Go home for this wire shape:
// DecodeControlBehavior decodes it directly, and the service's plan-mode payload EMBEDS it (adding
// the permissionMode/clearContext siblings) rather than re-declaring the same nested struct, so a
// wire-field rename lands in exactly one place instead of drifting between two hand-copied shapes.
type ControlBehaviorEnvelope struct {
	Response struct {
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior string `json:"behavior"`
			Message  string `json:"message"`
		} `json:"response"`
	} `json:"response"`
}

// DecodeControlBehavior decodes the frontend's neutral approve/reject control-response envelope
// (ControlBehaviorEnvelope), returning the trimmed request id, behavior, and rejection
// message. ok is false only when the bytes don't parse as JSON. The message is the user's typed
// rejection reason, with the ControlRejectedByUserMessage sentinel (an auto-filled placeholder,
// not a real reason) collapsed to "". The SINGLE home for the sentinel rule, shared by the Codex
// feedback path, the Cursor create-plan transform, and the service's request-id lookup, so a
// sentinel change lands in exactly one place.
func DecodeControlBehavior(content []byte) (requestID, behavior, message string, ok bool) {
	var cr ControlBehaviorEnvelope
	if err := json.Unmarshal(content, &cr); err != nil {
		return "", "", "", false
	}
	requestID = strings.TrimSpace(cr.Response.RequestID)
	behavior = strings.TrimSpace(cr.Response.Response.Behavior)
	message = NormalizeRejectionMessage(cr.Response.Response.Message)
	return requestID, behavior, message, true
}

// NormalizeRejectionMessage trims a control-response reject reason and collapses the
// ControlRejectedByUserMessage placeholder (the auto-filled "declined without a reason" text) to
// "". The SINGLE home for the deny-feedback rule, shared by DecodeControlBehavior (which decodes
// the raw wire bytes) and the service's controlResponsePlan.rejectionMessage accessor (which reads
// the already-decoded plan), so a sentinel-value change lands in exactly one place instead of two.
func NormalizeRejectionMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == ControlRejectedByUserMessage {
		return ""
	}
	return message
}

// opencodeQuestionRequestContext keeps the OpenCode-protocol `question.asked` question definitions
// (header + question) the frontend labels its answer values with. Registered as the ACP
// questionRequestContext hook for the providers that speak that protocol (OpenCode, Kilo).
func opencodeQuestionRequestContext(requestPayload []byte) json.RawMessage {
	var req struct {
		Type       string `json:"type"`
		Properties struct {
			Questions []struct {
				Header   string `json:"header,omitempty"`
				Question string `json:"question,omitempty"`
			} `json:"questions"`
		} `json:"properties"`
	}
	return projectRequestContext(requestPayload, &req, "opencode question request context")
}

// cursorQuestionRequestContext keeps the Cursor ask_question definitions (id + prompt + the option
// id/label map) the frontend needs to render selected options as their labels.
func cursorQuestionRequestContext(requestPayload []byte) json.RawMessage {
	var req struct {
		Method string `json:"method"`
		Params struct {
			Questions []struct {
				ID      string `json:"id,omitempty"`
				Prompt  string `json:"prompt,omitempty"`
				Options []struct {
					ID    string `json:"id,omitempty"`
					Label string `json:"label,omitempty"`
				} `json:"options,omitempty"`
			} `json:"questions"`
		} `json:"params"`
	}
	return projectRequestContext(requestPayload, &req, "cursor question request context")
}

// acpPermissionRequestContext keeps the ACP permission options (optionId + name) the frontend
// matches the selected optionId against to render its label. When the request carries no options
// (e.g. a create-plan request that failed the Cursor transform and fell through here), it degrades
// to method-only context, or nil when even the method is absent.
func acpPermissionRequestContext(requestPayload []byte) json.RawMessage {
	var req struct {
		Method string `json:"method"`
		Params struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Name     string `json:"name,omitempty"`
			} `json:"options"`
		} `json:"params"`
	}
	if !warnUnmarshal(requestPayload, &req, "acp permission request context") {
		return nil
	}
	if len(req.Params.Options) == 0 {
		return methodRequestContext(req.Method)
	}
	return marshalControlRequestContext(req)
}

// transformCursorControlResponse rewrites the frontend's neutral approve/reject envelope for a
// Cursor create-plan control request into the ACP outcome Cursor expects on its stdin, returning
// ok=false (and the caller forwards the response unchanged) when the bytes aren't a create-plan
// decision that matches the stored request id.
func transformCursorControlResponse(requestPayload, responseContent []byte) ([]byte, bool) {
	var req struct {
		Method string `json:"method"`
	}
	if !warnUnmarshal(requestPayload, &req, "cursor control response method") {
		return nil, false
	}
	if req.Method != CursorMethodCreatePlan {
		return nil, false
	}

	respRequestID, behavior, message, ok := DecodeControlBehavior(responseContent)
	if !ok {
		return nil, false
	}

	idRaw, requestID, ok := ExtractJSONRPCID(requestPayload)
	if !ok {
		return nil, false
	}

	if respRequestID == "" || respRequestID != requestID {
		return nil, false
	}

	outcome := "accepted"
	reason := ""
	switch behavior {
	case ControlBehaviorAllow:
	case ControlBehaviorDeny:
		outcome = "rejected"
		// message is already trimmed and the ControlRejectedByUserMessage placeholder collapsed
		// to "" by DecodeControlBehavior, so a bare rejection carries no reason.
		reason = message
	default:
		return nil, false
	}

	outcomeBody := map[string]interface{}{
		"outcome": outcome,
	}
	if reason != "" {
		outcomeBody["reason"] = reason
	}

	encoded, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(idRaw),
		"result":  map[string]interface{}{"outcome": outcomeBody},
	})
	if err != nil {
		return nil, false
	}
	return encoded, true
}
