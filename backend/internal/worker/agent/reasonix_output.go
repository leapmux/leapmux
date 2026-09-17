package agent

// handleExtraMethod routes Reasonix's own status notifications.
//
// MCP elicitation is NOT here: Reasonix sends the standard Agent Client Protocol
// `elicitation/create`, which the shared dispatcher already publishes. The vendor
// method this used to read exists nowhere in the Reasonix source, so the branch
// matched nothing while the live requests arrived through the shared path.
func (a *ReasonixAgent) handleExtraMethod(line *parsedLine) bool {
	if line.Method != reasonixMethodStatusUpdate {
		return false
	}
	a.handleReasonixStatusUpdate(line.Params)
	return true
}
