package service

import (
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// sendTurnRetryError distinguishes a missing agent from a refused turn retry.
//
// Manager.RetryTurn returns when the provider accepts the retry, so the only
// failures it reports are a missing agent and a refused offer. A failure of a
// later phase reaches the transcript, not this response. Any other error is
// unexpected and maps to Internal.
func sendTurnRetryError(sender channel.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, agent.ErrTurnRetryUnavailable):
		sendFailedPrecondition(sender, "the agent no longer offers this retry")
	case errors.Is(err, agent.ErrAgentNotFound):
		sendNotFoundError(sender, "agent not found or not running")
	default:
		sendInternalError(sender, err.Error())
	}
}
