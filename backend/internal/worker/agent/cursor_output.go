package agent

import (
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

const (
	// cursor/ask_question and cursor/create_plan live in contracts/cursor-protocol.json,
	// because the browser plugin dispatches on the same two names. The three below reach
	// the worker alone, so they stay here.
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
	case contracts.CursorMethodAskQuestion:
		// Cursor defines no outcome for a question the client withdraws, so LeapMux
		// sends none. The session cancel that follows a stop ends the turn.
		a.publishControlRequest(a.sink, line.Raw, nil)
		return true
	case contracts.CursorMethodCreatePlan:
		a.publishControlRequest(a.sink, line.Raw, cursorPlanCancelAnswer())
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
