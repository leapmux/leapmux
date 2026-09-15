package agent

import "github.com/leapmux/leapmux/generated/contracts"

// handleExtraMethod routes native status notifications and MCP input requests.
func (a *ReasonixAgent) handleExtraMethod(line *parsedLine) bool {
	if line.Method == contracts.MCPElicitationMethodReasonix {
		a.publishControlRequest(a.sink, line.Raw, mcpElicitationCancelAnswer())
		return true
	}
	if line.Method != reasonixMethodStatusUpdate {
		return false
	}
	a.handleReasonixStatusUpdate(line.Params)
	return true
}
