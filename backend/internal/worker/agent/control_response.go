package agent

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ControlResponseContext is the pure provider input for interpreting a frontend
// control response. The service loads the stored control request once, extracts the
// shared metadata, and passes both payloads here; providers must not perform I/O.
type ControlResponseContext struct {
	RequestID       string
	RequestPayload  json.RawMessage
	ResponseContent []byte
	ToolName        string
	PlanApproval    *leapmuxv1.PlanApprovalSettings
}

// ControlResponseResolution is the provider-owned interpretation of a control
// response. The service executes side effects from this plan: it persists the
// structured control-response row, deletes control requests, mutates plan-mode
// settings, and forwards Content.
type ControlResponseResolution struct {
	Content []byte
	// Feedback must use a separate user input when the native response cannot carry it safely.
	Feedback      string
	SelfDisplayed bool
	// Withhold prevents forwarding a response that the provider cannot read.
	// When the request still exists, the service returns an error and keeps the request available.
	// When the request is gone, the service can preserve the orphaned answer without forwarding it.
	// Empty Content cannot express this decision because the service restores the original response bytes.
	Withhold        bool
	PlanModeControl PlanModeControlKind
}

func defaultControlResponseResolution(ctx ControlResponseContext) ControlResponseResolution {
	return restoreControlResponseID(ctx)
}

// defaultControlResponseRequestID reads the stored-control-request lookup id from a raw frontend
// control response. It tries the neutral approve/reject envelope first
// (DecodeControlBehavior: {response:{request_id, ...}}, which every provider's frontend emits) and
// falls back to a top-level JSON-RPC id (ExtractJSONRPCID, used by the ACP family and Codex). When
// a payload carries BOTH -- a top-level id AND a nested response.request_id -- the nested envelope
// id wins, because the pending control_request row is keyed by response.request_id. Returns "" when
// neither shape yields a non-empty id. Shared by every provider's ControlResponseRequestID; no
// provider narrows it, since narrowing to one shape would drop the other's real flows.
func defaultControlResponseRequestID(content []byte) string {
	if requestID, _, _, ok := DecodeControlBehavior(content); ok && requestID != "" {
		return requestID
	}
	if _, requestID, ok := ExtractJSONRPCID(content); ok {
		return requestID
	}
	return ""
}

func (noopProvider) ControlResponseRequestID(content []byte) string {
	return defaultControlResponseRequestID(content)
}

func (codexProvider) ControlResponseRequestID(content []byte) string {
	return defaultControlResponseRequestID(content)
}

func (claudeProvider) ControlResponseRequestID(content []byte) string {
	return defaultControlResponseRequestID(content)
}

func (piProvider) ControlResponseRequestID(content []byte) string {
	return defaultControlResponseRequestID(content)
}

func (noopProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	return defaultControlResponseResolution(ctx)
}

func (p codexProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	if result, ok := resolveMCPElicitationResponse(ctx); ok {
		return result
	}
	res := defaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		return res
	}
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	return res
}

func (p claudeProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	if isClaudeMCPElicitation(ctx.RequestPayload) {
		return res
	}
	res.SelfDisplayed = p.IsSelfDisplayingControlTool(ctx.ToolName)
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	if !res.Withhold && res.PlanModeControl == PlanModeControlExit && ctx.PlanApproval.GetPermissionMode() != "" && !ctx.PlanApproval.GetClearContext() {
		if _, behavior, _, ok := DecodeControlBehavior(res.Content); ok && behavior == ControlBehaviorAllow {
			content, err := applyClaudePlanPermission(res.Content, ctx.PlanApproval.GetPermissionMode())
			if err != nil {
				res.Withhold = true
			} else {
				res.Content = content
			}
		}
	}
	return res
}

func (piProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	if result, ok := resolvePiMCPApprovalResponse(ctx); ok {
		return result
	}
	return defaultControlResponseResolution(ctx)
}

func (p acpProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	if result, ok := resolveMCPElicitationResponse(ctx); ok {
		return result
	}
	res := defaultControlResponseResolution(ctx)
	if p.provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR || len(ctx.RequestPayload) == 0 {
		return res
	}
	var request struct {
		Method string `json:"method"`
	}
	if !warnUnmarshal(ctx.RequestPayload, &request, "cursor control response request") {
		return res
	}
	if request.Method == CursorMethodCreatePlan {
		if transformed, ok := transformCursorControlResponse(ctx); ok {
			res.Content = transformed
		}
	}
	return res
}

// warnUnmarshal reports invalid provider JSON and returns whether decoding succeeded.
func warnUnmarshal(data []byte, v any, label string) bool {
	if err := json.Unmarshal(data, v); err != nil {
		slog.Warn(label+" unmarshal failed", "error", err)
		return false
	}
	return true
}

// ControlBehaviorEnvelope describes the shared approval and rejection fields.
// The decoder and plan service use this type. LeapMux plan settings remain outside provider JSON.
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
// feedback path, the Cursor create-plan transform, and the shared control-response request-id
// default (defaultControlResponseRequestID), so a sentinel change lands in exactly one place.
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

// transformCursorControlResponse rewrites the frontend's neutral approve/reject envelope for a
// Cursor create-plan control request into the ACP outcome Cursor expects on its stdin, returning
// ok=false (and the caller forwards the response unchanged) when the bytes aren't a create-plan
// decision that matches the stored request id.
func transformCursorControlResponse(ctx ControlResponseContext) ([]byte, bool) {
	var req struct {
		Method string `json:"method"`
	}
	if !warnUnmarshal(ctx.RequestPayload, &req, "cursor control response method") {
		return nil, false
	}
	if req.Method != CursorMethodCreatePlan {
		return nil, false
	}

	respRequestID, behavior, message, ok := DecodeControlBehavior(ctx.ResponseContent)
	if !ok {
		return nil, false
	}

	idRaw, requestID, ok := ExtractJSONRPCID(ctx.RequestPayload)
	if !ok {
		return nil, false
	}

	if respRequestID == "" || respRequestID != storedControlRequestID(ctx, requestID) {
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

// ControlResponseRequestID reads the pending request's id off the frontend answer.
func (zcodeProvider) ControlResponseRequestID(content []byte) string {
	return defaultControlResponseRequestID(content)
}

// ResolveControlResponse turns the frontend's neutral answer into the app-server's
// reply frame.
//
// This is where the two protocols meet. The frontend speaks allow/deny (plus the
// AskUserQuestion answers under `updatedInput.answers`); the app-server wants a
// permission decision or an accept/decline action, addressed by the WIRE id of its
// own request. The transformed bytes are what the service forwards to stdin, so the
// reply is a complete ZCode frame and not the envelope the frontend sent.
func (zcodeProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := defaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		// The pending request is gone (a teardown, or a duplicate answer that read it
		// after the winner deleted it). There is nothing to address the reply to, and
		// forwarding the frontend envelope would put a frame the app-server cannot
		// parse on its stdin. The answer still persists its row.
		res.Withhold = true
		return res
	}

	var stored zcodeControlRequestPayload
	if !warnUnmarshal(ctx.RequestPayload, &stored, "zcode control response request") {
		res.Withhold = true
		return res
	}
	res.PlanModeControl = zcodeProvider{}.PlanModeControl(stored.Request.ToolName)

	requestID, behavior, message, ok := DecodeControlBehavior(ctx.ResponseContent)
	if !ok || (behavior != ControlBehaviorAllow && behavior != ControlBehaviorDeny) {
		// Not a recognizable allow/deny. Withholding the forward is the safe answer:
		// the app-server keeps waiting (and the user can answer again) rather than
		// receiving a frame that means nothing.
		res.Withhold = true
		return res
	}
	if requestID != "" && stored.RequestID != "" && requestID != stored.RequestID {
		slog.Warn("zcode control response addressed another request",
			"answered", requestID, "stored", stored.RequestID)
		res.Withhold = true
		return res
	}
	if len(stored.WireID) == 0 {
		slog.Warn("zcode stored control request carried no wire id", "request_id", stored.RequestID)
		res.Withhold = true
		return res
	}

	reply, err := zcodeReplyForAnswer(stored, behavior, message, ctx.ResponseContent)
	if err != nil {
		slog.Warn("zcode build control reply failed", "request_id", stored.RequestID, "error", err)
		res.Withhold = true
		return res
	}
	encoded, err := json.Marshal(zcodeReplyFrame{ID: stored.WireID, Result: reply})
	if err != nil {
		slog.Warn("zcode marshal control reply failed", "request_id", stored.RequestID, "error", err)
		res.Withhold = true
		return res
	}
	res.Content = encoded
	if stored.Method == contracts.ZCodeMethodRequestUserInput && behavior == ControlBehaviorDeny && message != "" {
		var original ControlBehaviorEnvelope
		if json.Unmarshal(ctx.ResponseContent, &original) == nil {
			res.Feedback = original.Response.Response.Message
		}
	}
	return res
}
