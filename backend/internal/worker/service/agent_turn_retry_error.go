package service

import (
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// sendTurnRetryError distinguishes a missing agent from a refused turn retry.
//
// Manager.RetryTurn returns when the provider accepts the retry, so the only
// failures identify an absent agent, a refused offer, or pending input delivery.
// A later phase reports its failure in the transcript.
// Every other error is unexpected and maps to Internal.
func sendTurnRetryError(sender channel.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, agent.ErrAgentBusy):
		sendFailedPrecondition(sender, "Wait for pending input delivery before you retry the turn.")
	case errors.Is(err, agent.ErrTurnRetryUnavailable):
		sendFailedPrecondition(sender, "the agent no longer offers this retry")
	case errors.Is(err, agent.ErrAgentNotFound):
		sendNotFoundError(sender, "agent not found or not running")
	default:
		sendInternalError(sender, err.Error())
	}
}
