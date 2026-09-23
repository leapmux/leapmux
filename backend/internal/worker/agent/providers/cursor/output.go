package cursor

import (
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	if !strings.HasPrefix(line.Method, "cursor/") {
		return false
	}

	// A `cursor/` NOTIFICATION reaches the shared default too. It needs no answer,
	// and RefuseUnsupportedRequest returns at once for a frame with no id, but the
	// frame still belongs in the transcript.
	idRaw, _, ok := agent.ExtractJSONRPCID(line.Raw)
	if !ok {
		return false
	}

	switch line.Method {
	case contracts.CursorMethodAskQuestion:
		// Cursor defines no outcome for a question the client withdraws, so LeapMux
		// sends none. The session cancel that follows a stop ends the turn.
		a.PublishControlRequest(a.Sink(), line.Raw, nil)
		return true
	case contracts.CursorMethodCreatePlan:
		a.PublishControlRequest(a.Sink(), line.Raw, cursorPlanCancelAnswer())
		return true
	}
	// Each extension frame describes a tool call that already has a row, so it is
	// stored ON that row rather than as a row of its own.
	if a.handleCursorExtension(line.Method, line.Params) {
		// Queued, not waited for: this runs on the goroutine that drains Cursor's
		// stdout, and an ack is a write to a stdin Cursor may not be reading.
		a.SendResponseDetached(idRaw, map[string]interface{}{}, "cursor ack "+line.Method)
		return true
	}
	// FALSE, so the shared ACP default answers -32601 AND persists the frame.
	// Answering here and returning true short-circuited that default, so Cursor
	// alone dropped the frame: the runtime got a correct error reply and the
	// reader got no transcript row and no way to see what Cursor sent. The
	// `cursor/` namespace is open and only five names are known -- the two
	// controls above and the three extension frames -- so this is the ordinary
	// case for a new one. Reasonix already returns false here for the same reason.
	return false
}
