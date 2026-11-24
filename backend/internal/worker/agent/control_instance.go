package agent

import (
	"errors"
	"fmt"
)

// ErrControlRequestNotPending reports an answer to a control request that the
// provider no longer waits on. A reader answer, a withdrawal, or a standing
// approval already retired the request, so the answer would reach a request
// that is closed. The provider sends nothing, and the Worker releases the
// answer.
var ErrControlRequestNotPending = errors.New("the control request is no longer pending")

// ErrControlInstanceReplaced reports an answer to a control request instance
// that the provider replaced with another instance under the same request ID
// (RawInput.ControlInstanceID). The provider sends nothing.
//
// It is the one refusal that the Worker reports in silence, as it does when
// it finds a replaced instance itself: once the answered instance reads
// canceled, the reader's card of it is gone, and the new instance has a card
// of its own. So no failure is left to show.
var ErrControlInstanceReplaced = fmt.Errorf("%w: the agent replaced it with another request", ErrControlRequestNotPending)

// ErrControlInstanceUnstated reports an answer that states no control request
// instance to a provider that keeps its own instances. The answer cannot
// prove which instance it is for, so the provider sends nothing. Only the
// Worker's delivery of an answer states an instance, so this is the fault of
// a caller that sends an answer on another path.
var ErrControlInstanceUnstated = errors.New("the answer states no control request instance")

// ControlInstanceRefusal is the refusal of an answer to the control instance
// answered, for a request whose current instance is current. It is nil when
// the answer is for the current instance. A provider that states its own
// instances (ControlRequest.InstanceID) calls it before it sends an answer,
// because a runtime that addresses an answer to a request ID would apply an
// answer to a replaced instance to its replacement.
func ControlInstanceRefusal(current, answered string) error {
	switch {
	case answered == "":
		return ErrControlInstanceUnstated
	case answered != current:
		return ErrControlInstanceReplaced
	default:
		return nil
	}
}
