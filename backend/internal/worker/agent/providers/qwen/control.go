package qwen

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
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
// AnswerControlRequest answers it with the shared plan answer
// (agent.AnswerNeutralPlan), whose approval carries the permission mode the
// reader chose, and whose refusal carries the reason the reader typed. resolvePlanApproval picks the option that states
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

// permissionAllowsBypass keeps plan approvals and questions under user control.
func permissionAllowsBypass(line *providerkit.ParsedLine) bool {
	var request qwenPermissionRequest
	if json.Unmarshal(line.Raw, &request) != nil {
		return false
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(request.Params.ToolCall.Meta, &meta) != nil {
		return false
	}
	if _, interactive := meta[contracts.QwenMetaInteractionKind]; interactive {
		return false
	}
	tool := qwenToolName(request.Params.ToolCall.Meta)
	return tool != "" && tool != contracts.QwenToolExitPlanMode && tool != contracts.QwenToolAskUserQuestion
}

// isPlanApproval reports whether a stored request is Qwen's plan approval: a
// permission about `exit_plan_mode` that Qwen does not mark as a question
// dialog. A marked request is a question whatever tool it states, as
// ReadControlRequest reads it, so its answer is Qwen's own question reply.
func isPlanApproval(payload json.RawMessage) bool {
	var request qwenPermissionRequest
	if json.Unmarshal(payload, &request) != nil || request.Method != "session/request_permission" {
		return false
	}
	return qwenToolName(request.Params.ToolCall.Meta) == contracts.QwenToolExitPlanMode &&
		!readQwenPermissionToolCall(payload).asksQuestion()
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
	return acp.ResolvePlanPermission(ctx, acp.PlanPermissionResolver{
		AllowOption: func(ctx agent.ControlResponseContext) string {
			if ctx.PlanApproval.GetPermissionMode() == contracts.QwenModeAutoEdit {
				return contracts.QwenPermissionOptionProceedAlways
			}
			return contracts.QwenPermissionOptionProceedOnce
		},
		DenyOption: contracts.QwenPermissionOptionCancel,
	})
}
