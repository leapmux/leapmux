package agent

import "github.com/leapmux/leapmux/generated/contracts"

// handleExtraMethod routes native status notifications and MCP input requests.
func (a *ReasonixAgent) handleExtraMethod(line *parsedLine) bool {
	if line.Method == contracts.MCPElicitationMethodReasonix {
		if _, requestID, ok := ExtractJSONRPCID(line.Raw); ok && requestID != "" {
			a.publishControlRequest(a.sink, requestID, line.Raw)
		}
		return true
	}
	if line.Method != reasonixMethodStatusUpdate {
		return false
	}
	a.handleReasonixStatusUpdate(line.Params)
	return true
}
