package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// The row loaders every handler starts with: look the request's row up, or answer
// the caller and stop. Grouped here because they are one family with one shape,
// and because service.go should hold the Service itself rather than every helper
// that hangs off it.

// requireAgent looks up the agent named by the request, or answers the caller.
// Returns ok=false on empty id, missing row, or db error; the returned Agent is
// the freshly-loaded row so callers can reuse it.
func (svc *Service) requireAgent(sender channel.ResponseWriter, agentID string) (db.Agent, bool) {
	return requireExistingRow(sender, agentID, "agent", svc.Queries.GetAgentByID)
}

// requireTerminal is requireAgent's terminal mirror.
func (svc *Service) requireTerminal(sender channel.ResponseWriter, terminalID string) (db.Terminal, bool) {
	return requireExistingRow(sender, terminalID, "terminal", svc.Queries.GetTerminal)
}

// requireTerminalForRestart is the narrow-query variant used by the
// RestartTerminal handler: returns metadata + length(screen) without loading
// the screen BLOB. See GetTerminalForRestart for why.
func (svc *Service) requireTerminalForRestart(sender channel.ResponseWriter, terminalID string) (db.GetTerminalForRestartRow, bool) {
	return requireExistingRow(sender, terminalID, "terminal", svc.Queries.GetTerminalForRestart)
}

// requireAgentID resolves the agent named by the request without loading it:
// GetAgentID fetches only the id column, skipping the options / option-group
// JSON blobs a full GetAgentByID deserializes. Both queries share the bare
// `id = ?` predicate, so the error mapping (empty id, missing row, db error) is
// identical to requireAgent — use that one instead when the handler body needs
// the row.
func (svc *Service) requireAgentID(sender channel.ResponseWriter, agentID string) bool {
	_, ok := requireExistingRow(sender, agentID, "agent", svc.Queries.GetAgentID)
	return ok
}

// requireTerminalID is the terminal mirror of requireAgentID: an id-only
// lookup that skips the screen BLOB a full GetTerminal would read.
func (svc *Service) requireTerminalID(sender channel.ResponseWriter, terminalID string) bool {
	_, ok := requireExistingRow(sender, terminalID, "terminal", svc.Queries.GetTerminalID)
	return ok
}

// requireExistingRow factors the error-mapping shell shared by every "load a
// row by id" helper. kind is the user-facing entity label embedded in error
// messages ("agent", "terminal"); fetch is the sqlc query.
//
// It carries no ownership check, and none is missing: the handler is already
// behind requireWorkerOwner, and every row on this worker belongs to that one
// owner. What survives is the NOT_FOUND / INVALID_ARGUMENT mapping, which the
// handlers still depend on -- a close or an input write against an id this
// worker never held must say so rather than fall through to a manager lookup
// that reports it as an internal fault.
func requireExistingRow[T any](
	sender channel.ResponseWriter,
	id, kind string,
	fetch func(context.Context, string) (T, error),
) (T, bool) {
	var zero T
	if id == "" {
		sendInvalidArgument(sender, kind+"_id is required")
		return zero, false
	}
	row, err := fetch(bgCtx(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			sendNotFoundError(sender, kind+" not found")
			return zero, false
		}
		slog.Error("failed to load "+kind, kind+"_id", id, "error", err)
		sendInternalError(sender, "failed to load "+kind)
		return zero, false
	}
	return row, true
}
