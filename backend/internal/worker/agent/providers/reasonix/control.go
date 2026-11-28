package reasonix

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// permissionAllowsBypass admits only ordinary native tool approvals.
// Fresh approvals, directory expansion, and protected operations still need a person in Yolo.
func permissionAllowsBypass(line *providerkit.ParsedLine) bool {
	var request struct {
		ToolCall struct {
			Meta struct {
				Reasonix struct {
					Tool  string `json:"tool"`
					Fresh *bool  `json:"fresh"`
					Kind  string `json:"kind"`
				} `json:"reasonix.io"`
			} `json:"_meta"`
		} `json:"toolCall"`
	}
	if json.Unmarshal(line.Params, &request) != nil {
		return false
	}
	metadata := request.ToolCall.Meta.Reasonix
	if metadata.Fresh == nil || *metadata.Fresh || metadata.Kind != "" || strings.TrimSpace(metadata.Tool) == "" {
		return false
	}
	switch metadata.Tool {
	case "ask", contracts.ReasonixToolExitPlanMode, "remember", "forget", "sandbox_escape", "config_write":
		return false
	default:
		return true
	}
}

// isPlanApproval reports whether payload is Reasonix's plan gate, not another
// permission whose title happens to mention plan mode.
func isPlanApproval(payload json.RawMessage) bool {
	var request struct {
		Method string `json:"method"`
		Params struct {
			ToolCall struct {
				Meta struct {
					Reasonix struct {
						Tool string `json:"tool"`
					} `json:"reasonix.io"`
				} `json:"_meta"`
			} `json:"toolCall"`
		} `json:"params"`
	}
	return json.Unmarshal(payload, &request) == nil && request.Method == "session/request_permission" && request.Params.ToolCall.Meta.Reasonix.Tool == contracts.ReasonixToolExitPlanMode
}

func reasonixPlanOptionKind(optionID string) leapmuxv1.PermissionOptionKind {
	switch optionID {
	case "allow_once":
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	case "reject_once":
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
	default:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_UNSPECIFIED
	}
}

func reasonixOffersPlanOption(payload json.RawMessage, optionID string) bool {
	wantKind := reasonixPlanOptionKind(optionID)
	if wantKind == leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_UNSPECIFIED {
		return false
	}
	for _, option := range acp.PermissionOptions(payload) {
		if option.GetOptionId() == optionID && option.GetKind() == wantKind {
			return true
		}
	}
	return false
}

// isReasonixPlanPrompt reports whether payload is a plan gate offering both
// options that resolvePlanApproval selects.
func isReasonixPlanPrompt(payload json.RawMessage) bool {
	return isPlanApproval(payload) &&
		reasonixOffersPlanOption(payload, "allow_once") &&
		reasonixOffersPlanOption(payload, "reject_once")
}

// observeControlRequest saves the proposal after the base has flushed it into
// the transcript. A later fresh-context approval executes that same text.
func (a *Agent) observeControlRequest(_ string, line *providerkit.ParsedLine) {
	if !isPlanApproval(line.Raw) {
		return
	}
	a.Mu.Lock()
	plan := a.planDraft.String()
	a.Mu.Unlock()
	if strings.TrimSpace(plan) == "" {
		return
	}
	compressed, compression := msgcodec.Compress([]byte(plan))
	a.Sink().UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(plan))
}

// resolvePlanApproval answers Reasonix's two-option plan gate. Approval runs
// the plan. Rejection keeps Plan mode, with revision text following as input
// because the selected option cannot carry it.
func resolvePlanApproval(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	return acp.ResolvePlanPermission(ctx, acp.PlanPermissionResolver{
		AllowOption: func(agent.ControlResponseContext) string { return "allow_once" },
		DenyOption:  "reject_once",
		ValidateOption: func(ctx agent.ControlResponseContext, optionID string, result *agent.ControlResponseResolution) bool {
			if !reasonixOffersPlanOption(ctx.RequestPayload, optionID) {
				result.Refuse(agent.RefusalUnreadableRequest)
				return false
			}
			return true
		},
	})
}
