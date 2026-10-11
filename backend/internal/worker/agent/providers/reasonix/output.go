package reasonix

import (
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// handleExtraMethod routes Reasonix's own status and MCP requests.
func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	switch line.Method {
	case reasonixMethodStatusUpdate:
		a.handleReasonixStatusUpdate(line.Params)
	case contracts.ReasonixMethodMcpRequestInteraction:
		a.PublishSessionControlRequest(line, providerkit.MCPElicitationCancelAnswer())
	default:
		return false
	}
	return true
}
