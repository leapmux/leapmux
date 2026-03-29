package agent

import (
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func (a *CopilotCLIAgent) handleExtraSessionUpdate(sessionUpdate string, update json.RawMessage) bool {
	if sessionUpdate == acpUpdateConfigOptionUpdate {
		a.handleConfigOptionUpdate(update)
		return true
	}
	return false
}

func (a *CopilotCLIAgent) handleConfigOptionUpdate(update json.RawMessage) {
	var payload struct {
		ConfigOptions []copilotCLIConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(update, &payload) != nil || len(payload.ConfigOptions) == 0 {
		return
	}

	if mode := a.syncConfigOptions(payload.ConfigOptions); mode != "" {
		a.sink.UpdatePermissionMode(mode)
	}
}

// syncConfigOptions updates the agent's model and mode from the given config options.
// It returns the updated mode value, or "" if no mode was found.
func (a *CopilotCLIAgent) syncConfigOptions(options []copilotCLIConfigOption) string {
	if len(options) == 0 {
		return ""
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var updatedMode string
	for _, option := range options {
		switch option.ID {
		case "model":
			if option.CurrentValue != "" {
				a.model = option.CurrentValue
			}
			if len(option.Options) > 0 {
				models := make([]*leapmuxv1.AvailableModel, 0, len(option.Options))
				for _, candidate := range option.Options {
					if candidate.Value == "" {
						continue
					}
					name := candidate.Name
					if name == "" {
						name = candidate.Value
					}
					models = append(models, &leapmuxv1.AvailableModel{
						Id:          candidate.Value,
						DisplayName: name,
						IsDefault:   candidate.Value == option.CurrentValue,
					})
				}
				if len(models) > 0 {
					a.availableModels = models
				}
			}
		case "mode":
			if option.CurrentValue != "" {
				a.permissionMode = option.CurrentValue
				updatedMode = option.CurrentValue
			}
			if len(option.Options) > 0 {
				modes := make([]*leapmuxv1.AvailableOption, 0, len(option.Options))
				for _, candidate := range option.Options {
					if candidate.Value == "" {
						continue
					}
					name := candidate.Name
					if name == "" {
						name = candidate.Value
					}
					modes = append(modes, &leapmuxv1.AvailableOption{
						Id:        candidate.Value,
						Name:      name,
						IsDefault: candidate.Value == option.CurrentValue,
					})
				}
				if len(modes) > 0 {
					a.availableModes = modes
				}
			}
		}
	}
	return updatedMode
}
