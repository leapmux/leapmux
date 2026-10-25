package dirac

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// diracToolNamePattern accepts the identifiers used by native and custom tools.
// Human card headers are not tool identifiers.
var diracToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// permissionAllowsBypass accepts ordinary native tool approvals.
// A task transition uses its own approval option and still needs an explicit choice.
func permissionAllowsBypass(line *providerkit.ParsedLine) bool {
	var request struct {
		ToolCall struct {
			Name     string `json:"name"`
			Title    string `json:"title"`
			RawInput struct {
				Tool string `json:"tool"`
			} `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(line.Params, &request) != nil {
		return false
	}
	name := request.ToolCall.Name
	if !diracToolNamePattern.MatchString(name) || strings.TrimSpace(request.ToolCall.Title) == "" ||
		name == "new_task" || name == "respond" || name == "resolve_task_interaction" {
		return false
	}
	// Dirac substitutes the card header when it has no tool identity. That includes
	// retry and other manual dialogs, even though they offer ordinary approval IDs.
	if name == request.ToolCall.Title && request.ToolCall.RawInput.Tool != name {
		return false
	}
	for _, option := range request.Options {
		if option.ID == "allow_once" && option.Kind == contracts.ACPPermissionKindAllowOnce {
			return true
		}
	}
	return false
}
