package qwen

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Qwen raises its plan approval as a session/request_permission of its
// `exit_plan_mode` tool, with four options:
//
//   - `restore_previous`: return to the mode before plan mode.
//   - `proceed_always`: leave plan mode for auto-edit.
//   - `proceed_once`: leave plan mode for default.
//   - `cancel`: stay in plan mode and keep planning.
//
// The browser answers it through the shared plan approval, whose Approve
// carries the permission mode the reader chose, and whose Reject carries the
// reason the reader typed. resolvePlanApproval picks the option that states
// the same choice.

// storePlan records the plan that a plan approval carries as the argument of
// its `exit_plan_mode` tool, so LeapMux gives the plan its title, can show it
// again, and hands it to the new session when the reader approves it with a
// fresh context. Without it, that session received the bare instruction to
// implement a plan that it never saw.
func (a *Agent) storePlan(rawInput json.RawMessage) {
	var input struct {
		Plan string `json:"plan"`
	}
	if json.Unmarshal(rawInput, &input) != nil || strings.TrimSpace(input.Plan) == "" {
		return
	}
	compressed, compression := msgcodec.Compress([]byte(input.Plan))
	a.Sink().UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(input.Plan))
}

// qwenPermissionRequest is the part of a stored permission request that the
// plan approval reads.
type qwenPermissionRequest struct {
	Method string `json:"method"`
	Params struct {
		ToolCall struct {
			Meta json.RawMessage `json:"_meta"`
		} `json:"toolCall"`
	} `json:"params"`
}

// isPlanApproval reports whether a stored request is Qwen's plan approval.
func isPlanApproval(payload json.RawMessage) bool {
	var request qwenPermissionRequest
	if json.Unmarshal(payload, &request) != nil || request.Method != "session/request_permission" {
		return false
	}
	return qwenToolName(request.Params.ToolCall.Meta) == contracts.QwenToolExitPlanMode
}

// resolvePlanApproval answers a plan approval.
//
//   - Approve with auto-edit chosen: `proceed_always`, which Qwen maps onto
//     auto-edit itself.
//   - Approve otherwise: `proceed_once`, which leaves plan mode for default.
//     A stronger mode the reader chose follows as a settings change, which the
//     service applies after the answer.
//   - Reject: `cancel`. Qwen's reply has no field for a reason, so a reason the
//     reader typed follows as a message of its own.
func resolvePlanApproval(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := agent.DefaultControlResponseResolution(ctx)
	requestID, behavior, message, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	id, storedID, found := agent.ExtractJSONRPCID(ctx.RequestPayload)
	if !ok || !found || requestID == "" || requestID != agent.StoredControlRequestID(ctx, storedID) {
		result.Withhold = true
		return result
	}
	var option string
	switch behavior {
	case agent.ControlBehaviorAllow:
		option = contracts.QwenPermissionOptionProceedOnce
		if ctx.PlanApproval.GetPermissionMode() == contracts.QwenModeAutoEdit {
			option = contracts.QwenPermissionOptionProceedAlways
		}
		result.PlanModeControl = agent.PlanModeControlExit
	case agent.ControlBehaviorDeny:
		option = contracts.QwenPermissionOptionCancel
		result.Feedback = message
	default:
		result.Withhold = true
		return result
	}
	content, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"outcome": map[string]string{"outcome": contracts.ACPPermissionOutcomeSelected, "optionId": option},
	}})
	if err != nil {
		result.Withhold = true
		return result
	}
	result.Content = content
	return result
}
