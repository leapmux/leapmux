package service

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"google.golang.org/grpc/codes"
)

// sendInterruptError preserves a provider refusal instead of reporting a missing agent.
func sendInterruptError(sender channel.ResponseWriter, err error) {
	if err == nil {
		return
	}
	var unavailable *agent.InterruptUnavailableError
	switch {
	case errors.Is(err, agent.ErrAgentNotFound):
		sendNotFoundError(sender, "agent not found or not running")
	case errors.Is(err, agent.ErrChildOperationUnsupported):
		sendFailedPrecondition(sender, "this subagent cannot be interrupted")
	case errors.Is(err, agent.ErrChildRouteNotReady):
		// An owner restart can leave the child route temporarily unavailable.
		sendUnavailable(sender, "subagent route is not ready in the running owner process; retry")
	case errors.As(err, &unavailable):
		sendFailedPrecondition(sender, err.Error())
	case errors.Is(err, context.Canceled):
		_ = sender.SendError(int32(codes.Canceled), err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		_ = sender.SendError(int32(codes.DeadlineExceeded), err.Error())
	default:
		sendInternalError(sender, err.Error())
	}
}
