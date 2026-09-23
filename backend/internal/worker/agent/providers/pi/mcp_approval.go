package pi

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type piMCPApprovalRequest struct {
	Type    string   `json:"type"`
	ID      string   `json:"id,omitempty"`
	Method  string   `json:"method"`
	Title   string   `json:"title"`
	Options []string `json:"options"`
}

func parsePiMCPApprovalRequest(raw []byte) (*piMCPApprovalRequest, bool) {
	var request piMCPApprovalRequest
	if json.Unmarshal(raw, &request) != nil || request.Type != contracts.PiEventExtensionUIRequest || request.Method != contracts.PiDialogMethodSelect ||
		!strings.HasPrefix(request.Title, contracts.PiMCPApprovalTextTitlePrefix) || !strings.Contains(request.Title, contracts.PiMCPApprovalTextArgumentsMarker) ||
		len(request.Options) != 3 || request.Options[0] != contracts.PiMCPApprovalChoiceAllowOnce || request.Options[1] != contracts.PiMCPApprovalChoiceAllowForSession || request.Options[2] != contracts.PiMCPApprovalChoiceDeny {
		return nil, false
	}
	return &request, true
}

// resolvePiMCPApprovalResponse maps shared approval controls to Pi's native select response.
func resolvePiMCPApprovalResponse(ctx agent.ControlResponseContext) (agent.ControlResponseResolution, bool) {
	request, ok := parsePiMCPApprovalRequest(ctx.RequestPayload)
	if !ok {
		return agent.ControlResponseResolution{}, false
	}
	result := agent.DefaultControlResponseResolution(ctx)
	result.Withhold = true
	var response providerkit.MCPElicitationControlResponse
	if request.ID == "" || json.Unmarshal(ctx.ResponseContent, &response) != nil || response.Response.RequestID != request.ID {
		return result, true
	}
	answer := response.Response.Response
	reply := map[string]any{"type": contracts.PiEventExtensionUIResponse, "id": request.ID}
	switch answer.Action {
	case contracts.MCPElicitationActionAccept:
		switch answer.Meta["persist"] {
		case "":
			reply["value"] = contracts.PiMCPApprovalChoiceAllowOnce
		case contracts.MCPElicitationApprovalScopeSession:
			reply["value"] = contracts.PiMCPApprovalChoiceAllowForSession
		default:
			return result, true
		}
	case contracts.MCPElicitationActionDecline:
		reply["value"] = contracts.PiMCPApprovalChoiceDeny
	case contracts.MCPElicitationActionCancel:
		reply["cancelled"] = true
	default:
		return result, true
	}
	content, err := json.Marshal(reply)
	if err != nil {
		return result, true
	}
	result.Content = content
	result.Withhold = false
	return result, true
}
