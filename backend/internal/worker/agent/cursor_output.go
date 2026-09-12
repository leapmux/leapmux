package agent

import (
	"log/slog"
	"strings"
)

const (
	CursorMethodAskQuestion   = "cursor/ask_question"
	CursorMethodCreatePlan    = "cursor/create_plan"
	cursorMethodUpdateTodos   = "cursor/update_todos"
	cursorMethodTask          = "cursor/task"
	cursorMethodGenerateImage = "cursor/generate_image"
)

func (a *CursorCLIAgent) handleExtraMethod(line *parsedLine) bool {
	if !strings.HasPrefix(line.Method, "cursor/") {
		return false
	}

	idRaw, _, ok := ExtractJSONRPCID(line.Raw)
	if !ok {
		return true
	}

	switch line.Method {
	case CursorMethodAskQuestion, CursorMethodCreatePlan:
		a.publishControlRequest(a.sink, line.Raw)
		return true
	case cursorMethodUpdateTodos, cursorMethodTask, cursorMethodGenerateImage:
		if err := a.sendResponse(idRaw, map[string]interface{}{}); err != nil {
			slog.Warn("cursor extension ack failed", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
		return true
	default:
		if err := a.sendErrorResponse(idRaw, -32601, "Method not supported: "+line.Method); err != nil {
			slog.Warn("cursor extension method-not-found failed", "agent_id", a.agentID, "method", line.Method, "error", err)
		}
		return true
	}
}
