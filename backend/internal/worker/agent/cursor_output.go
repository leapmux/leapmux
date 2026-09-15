package agent

import (
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

	// A `cursor/` NOTIFICATION reaches the shared default too. It needs no answer,
	// and refuseUnsupportedRequest returns at once for a frame with no id, but the
	// frame still belongs in the transcript.
	idRaw, _, ok := ExtractJSONRPCID(line.Raw)
	if !ok {
		return false
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
		// Queued, not waited for: this runs on the goroutine that drains Cursor's
		// stdout, and an ack is a write to a stdin Cursor may not be reading.
		a.sendResponseDetached(idRaw, map[string]interface{}{}, "cursor ack "+line.Method)
		return true
	default:
		// FALSE, so the shared ACP default answers -32601 AND persists the frame.
		// Answering here and returning true short-circuited that default, so Cursor
		// alone dropped the frame: the runtime got a correct error reply and the
		// reader got no transcript row and no way to see what Cursor sent. The
		// `cursor/` namespace is open and only the five names above are known, so
		// this is the ordinary case for a new one. Reasonix already returns false
		// here for the same reason.
		return false
	}
}
