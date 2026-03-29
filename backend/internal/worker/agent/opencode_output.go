package agent

import "log/slog"

func (a *OpenCodeAgent) handleOutput(line *parsedLine) {
	slog.Debug("opencode HandleOutput", "agent_id", a.agentID, "method", line.Method, "len", len(line.Raw))
	a.handleACPOutput(line, nil)
}

// HandleOutput processes a single JSONL notification from OpenCode.
func (a *OpenCodeAgent) HandleOutput(content []byte) {
	a.handleOutput(parseLine(content))
}
