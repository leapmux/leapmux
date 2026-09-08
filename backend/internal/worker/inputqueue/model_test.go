package inputqueue

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeliveryErrorStatesTheOutcomeItCarries(t *testing.T) {
	t.Parallel()

	// RequeueAndPause persists this text into the item's error column, and the
	// browser renders it. The provider's own wording says what went wrong on the
	// wire; the prefix says what it means for the queue, which is the part a
	// user can act on. A plain failure adds nothing, because there the cause
	// already reads as the whole answer.
	cause := errors.New("write |1: file already closed")
	for _, tc := range []struct {
		name    string
		outcome DispatchOutcome
		want    string
	}{
		{name: "busy", outcome: DispatchBusy, want: "agent input waits for the turn in flight: " + cause.Error()},
		{name: "not ready", outcome: DispatchNotReady, want: "agent cannot accept input yet: " + cause.Error()},
		{name: "uncertain", outcome: DispatchUncertain, want: "agent input delivery is uncertain: " + cause.Error()},
		{name: "failed", outcome: DispatchFailed, want: cause.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := &DeliveryError{Err: cause, Outcome: tc.outcome}
			assert.Equal(t, tc.want, err.Error())
			assert.ErrorIs(t, err, cause, "the cause stays reachable whatever the outcome")
			assert.Equal(t, tc.outcome, dispatchOutcome(err))
		})
	}

	// An error the classifier never built is a plain failure, so a provider
	// error that reaches the manager unwrapped fails the item rather than
	// silently taking a branch that requeues it.
	assert.Equal(t, DispatchFailed, dispatchOutcome(cause))
}
