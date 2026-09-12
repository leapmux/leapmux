package agent

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// isPiPlanApproval validates the native plan-mode menu before giving it shared approval semantics.
func isPiPlanApproval(raw json.RawMessage) bool {
	var request struct {
		Type    string   `json:"type"`
		Method  string   `json:"method"`
		Title   string   `json:"title"`
		Options []string `json:"options"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Type != contracts.PiEventExtensionUIRequest || request.Method != contracts.PiDialogMethodSelect {
		return false
	}
	firstLine, _, _ := strings.Cut(request.Title, "\n")
	if strings.TrimSpace(firstLine) != contracts.PiPlanDialogReadyTitle {
		return false
	}
	options := make(map[string]bool)
	for _, option := range request.Options {
		if strings.TrimSpace(option) == "" || options[option] {
			return false
		}
		options[option] = true
	}
	return options[contracts.PiPlanActionImplementHere] && options[contracts.PiPlanActionImplementFresh] && options[contracts.PiPlanActionStay]
}
