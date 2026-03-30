package agent

import "encoding/json"

func (a *CopilotCLIAgent) handleExtraSessionUpdate(sessionUpdate string, update json.RawMessage) bool {
	if sessionUpdate == acpUpdateConfigOptionUpdate {
		a.handleConfigOptionUpdate(update)
		return true
	}
	return false
}

func (a *CopilotCLIAgent) handleConfigOptionUpdate(update json.RawMessage) {
	options := parseACPConfigOptions(update)
	if len(options) == 0 {
		return
	}

	if mode := a.syncConfigOptions(options); mode != "" {
		a.sink.UpdatePermissionMode(mode)
	}
}

// syncConfigOptions updates the agent's model and mode from the given config options.
// It returns the updated mode value, or "" if no mode was found.
func (a *CopilotCLIAgent) syncConfigOptions(options []acpConfigOption) string {
	if len(options) == 0 {
		return ""
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	return syncACPConfigOptions(&a.model, &a.permissionMode, &a.availableModels, &a.availableModes, options, nil)
}
