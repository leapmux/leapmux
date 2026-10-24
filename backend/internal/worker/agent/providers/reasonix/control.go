package reasonix

import (
	"encoding/json"
	"strings"

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
	case "ask", "exit_plan_mode", "remember", "forget", "sandbox_escape", "config_write":
		return false
	default:
		return true
	}
}
