package service

import (
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// This refusal proves that no provider start ran. A restart must retain its
// saved conversation even when it already stopped the previous process.
var errAgentProcessStartBlocked = errors.New("agent process startup is blocked by unresolved controls")

// The caller owns the root lifecycle lock or its unique startup claim.
// Exit cleanup can fail, so every start must retire remaining blocking
// controls before a replacement can publish its own. Conversation questions stay.
func (svc *Service) retireStoppedProcessControls(agentID string) error {
	if svc.Agents.HasAgent(agentID) {
		return fmt.Errorf("%w: cannot retire controls while the agent process is registered", inputqueue.ErrConflict)
	}
	agentIDs, err := svc.Queries.ListAgentTreeIDs(bgCtx(), agentID)
	if err != nil {
		return fmt.Errorf("read the stopped process's control owners: %w", err)
	}
	if len(agentIDs) == 0 {
		return fmt.Errorf("%w: the stopped process has no stored agent", inputqueue.ErrConflict)
	}
	var cleanupErr error
	for _, id := range agentIDs {
		if err := svc.Output.clearPendingControlRequests(id); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("retire blocking controls for agent %s: %w", id, err))
		}
	}
	return cleanupErr
}
