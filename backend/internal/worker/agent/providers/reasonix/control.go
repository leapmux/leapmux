package reasonix

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

// reasonixPermissionRequest is the stored shape of a native plan approval.
type reasonixPermissionRequest struct {
	Method string `json:"method"`
	Params struct {
		ToolCall struct {
			Meta struct {
				Reasonix struct {
					Tool string `json:"tool"`
				} `json:"reasonix.io"`
			} `json:"_meta"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	} `json:"params"`
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

func decodeReasonixPermissionRequest(payload json.RawMessage) (reasonixPermissionRequest, bool) {
	var request reasonixPermissionRequest
	if json.Unmarshal(payload, &request) != nil {
		return reasonixPermissionRequest{}, false
	}
	return request, true
}

// observeControlRequest saves the proposal after the base has flushed it into
// the transcript. A later fresh-context approval executes that same text.
func (a *Agent) observeControlRequest(line *providerkit.ParsedLine) {
	if !isPlanApproval(line.Raw) {
		return
	}
	a.Mu.Lock()
	plan := a.planDraft
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
	result := agent.DefaultControlResponseResolution(ctx)
	requestID, behavior, message, ok := agent.DecodeControlBehavior(ctx.ResponseContent)
	id, storedID, found := agent.ExtractJSONRPCID(ctx.RequestPayload)
	switch {
	case !ok || requestID == "":
		result.Refuse(agent.RefusalNoDecision)
		return result
	case !found:
		result.Refuse(agent.RefusalUnreadableRequest)
		return result
	case requestID != agent.StoredControlRequestID(ctx, storedID):
		result.Refuse(agent.RefusalOtherRequest)
		return result
	}
	request, valid := decodeReasonixPermissionRequest(ctx.RequestPayload)
	if !valid {
		result.Refuse(agent.RefusalUnreadableRequest)
		return result
	}
	var option string
	switch behavior {
	case agent.ControlBehaviorAllow:
		option = "allow_once"
		result.PlanModeControl = agent.PlanModeControlExit
	case agent.ControlBehaviorDeny:
		option = "reject_once"
		result.FollowUp = agent.FeedbackFollowUp(message)
	default:
		result.Refuse(agent.RefusalNoDecision)
		return result
	}
	offered := false
	for _, candidate := range request.Params.Options {
		if candidate.OptionID == option && candidate.Kind == option {
			offered = true
			break
		}
	}
	if !offered {
		result.Refuse(agent.RefusalUnreadableRequest)
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
		result.Refuse(agent.RefusalUnencodableReply)
		return result
	}
	result.Content = content
	return result
}

// isReasonixJSONRPCReply distinguishes a native reply from the neutral
// allow/deny envelope that the shared plan surface sends.
func isReasonixJSONRPCReply(content []byte) bool {
	var reply map[string]json.RawMessage
	if json.Unmarshal(content, &reply) != nil {
		return false
	}
	_, hasResult := reply["result"]
	_, hasError := reply["error"]
	return hasResult || hasError
}
