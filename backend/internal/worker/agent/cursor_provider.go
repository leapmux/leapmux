package agent

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
)

// cursorPlanOutcome* are the words Cursor's create-plan request accepts back. The
// protocol defines no "cancelled" here, so a withdrawal reports a rejection.
const (
	cursorPlanOutcomeAccepted = "accepted"
	cursorPlanOutcomeRejected = "rejected"
)

// cursorProvider is the wire-format plugin for Cursor. It is an ACP provider, and it
// adds the one request Cursor answers in a shape of its own: cursor/create_plan.
//
// The named type is what keeps that shape out of acpProvider, which also serves Goose,
// Kilo, OpenCode and Reasonix. None of them sends a create-plan request, and a
// provider-enum test inside the shared type would state Cursor's protocol in code that
// belongs to five providers.
type cursorProvider struct {
	acpProvider
}

// ResolveControlResponse rewrites a create-plan answer and forwards every other one.
//
// It DELEGATES to the embedded acpProvider rather than repeating its body. The two
// branches cannot collide -- resolveMCPElicitationResponse answers only an MCP
// elicitation method, and the transform below refuses anything that is not
// contracts.CursorMethodCreatePlan -- so a third branch added to the shared ACP
// resolution reaches Cursor too, instead of serving Kilo, OpenCode, Goose and
// Reasonix while Cursor silently keeps a stale copy.
func (p cursorProvider) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	res := p.acpProvider.ResolveControlResponse(ctx)
	// Without this guard an empty payload would reach warnUnmarshal below and log a
	// failure for a request that carried nothing to parse.
	if len(ctx.RequestPayload) == 0 {
		return res
	}
	if transformed, ok := transformCursorControlResponse(ctx); ok {
		res.Content = transformed
	}
	return res
}

// cursorPlanCancelAnswer rejects a plan that the reader never answered.
//
// Cursor keeps the turn blocked until the create-plan request has an outcome, and its
// outcome vocabulary has no word for a withdrawal. A rejection with no reason is what
// transformCursorControlResponse already sends for a bare refusal, so a stop leaves
// the same record a refusal does.
func cursorPlanCancelAnswer() any {
	return map[string]any{"outcome": map[string]any{"outcome": cursorPlanOutcomeRejected}}
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
	if req.Method != contracts.CursorMethodCreatePlan {
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

	outcome := cursorPlanOutcomeAccepted
	reason := ""
	switch behavior {
	case ControlBehaviorAllow:
	case ControlBehaviorDeny:
		outcome = cursorPlanOutcomeRejected
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
