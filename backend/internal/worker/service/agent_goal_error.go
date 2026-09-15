package service

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"google.golang.org/grpc/codes"
)

// sendGoalUpdateError distinguishes a missing agent from a refused or failed operation.
func sendGoalUpdateError(sender channel.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, agent.ErrGoalControlUnsupported):
		sendFailedPrecondition(sender, "this agent cannot perform that session-goal action")
	case errors.Is(err, agent.ErrGoalObjectiveIsCommand):
		sendInvalidArgument(sender, "the provider treats this objective as a command argument. Write a longer objective.")
	case errors.Is(err, agent.ErrAgentBusy), errors.Is(err, agent.ErrDeliveryUncertain):
		sendFailedPrecondition(sender, err.Error())
	case errors.Is(err, agent.ErrAgentNotFound):
		sendNotFoundError(sender, "agent not found or not running")
	case errors.Is(err, context.Canceled), errors.Is(err, agent.ErrContextClearCancelled):
		_ = sender.SendError(int32(codes.Canceled), err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		_ = sender.SendError(int32(codes.DeadlineExceeded), err.Error())
	default:
		sendInternalError(sender, err.Error())
	}
}
