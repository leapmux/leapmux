package service

import (
	"errors"

	"github.com/leapmux/leapmux/internal/worker/channel"
	"google.golang.org/grpc/codes"
)

// creationAbortedError identifies a proven rejection, cancellation, or unusable
// publication. Observation failures must never use this type. Clients can finish
// local attempt bookkeeping after this response without deleting canonical tabs.
type creationAbortedError struct{ message string }

func (e *creationAbortedError) Error() string { return e.message }

func sendCreationError(sender channel.ResponseWriter, err error) {
	var aborted *creationAbortedError
	if errors.As(err, &aborted) {
		_ = sender.SendError(int32(codes.Aborted), err.Error())
		return
	}
	sendFailedPrecondition(sender, err.Error())
}
