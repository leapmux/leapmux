package reasonix

import (
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// permissionAllowsBypass excludes the native ask and plan-exit dialogs.
// Both dialogs use session/request_permission, as ordinary tools do.
func permissionAllowsBypass(line *providerkit.ParsedLine) bool {
	var request struct {
		ToolCall struct {
			Meta struct {
				Reasonix struct {
					Tool string `json:"tool"`
				} `json:"reasonix.io"`
			} `json:"_meta"`
		} `json:"toolCall"`
	}
	if json.Unmarshal(line.Params, &request) != nil {
		return false
	}
	tool := request.ToolCall.Meta.Reasonix.Tool
	return tool != "" && tool != "ask" && tool != "exit_plan_mode"
}
