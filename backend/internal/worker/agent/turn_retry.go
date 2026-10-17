package agent

import "errors"

// TurnRetrier is the one provider route for a turn retry: stop the running turn
// and send its input again on another model, in a new provider thread that
// excludes the stopped attempt.
//
// The worker offers the retry only while the running provider broadcasts a
// `turn_retry_offer` for this turn, so BeginTurnRetry refuses a turn or a model
// that the offer does not identify. File changes and other actions that the
// stopped attempt already took stay.
type TurnRetrier interface {
	// BeginTurnRetry validates the offer and marks the retry in flight, so the
	// input queue holds at once. It sends no request and returns without a
	// wait. It returns ErrTurnRetryUnavailable when the provider does not offer
	// this retry.
	BeginTurnRetry(turnID, model string) (TurnRetry, error)
}

// TurnRetry is one retry that BeginTurnRetry accepted. Manager.RetryTurn runs
// its phases in order: Replace under the lifecycle lock, then Start after the
// lock releases. Abandon replaces both when the provider instance changed
// before Replace could run.
//
// Each phase reports its own failure in the transcript, because no caller
// waits for the phases: the RPC answers when BeginTurnRetry returns.
type TurnRetry interface {
	// Replace stops the running turn, waits for it to end, and installs a new
	// provider thread without the stopped attempt. A failure ends the retry,
	// states the failure in the transcript, and returns the error. Start must
	// not run after a failure.
	Replace() error
	// Start sends the retained input on the new thread. A failure ends the
	// retry and states the failure in the transcript. The returned error is for
	// the log only.
	Start() error
	// Abandon ends the retry without a transcript notice. err states why, for
	// the log.
	Abandon(err error)
}

// ErrTurnRetryUnavailable means that the provider offers no retry for this turn
// and model. The service maps it to FailedPrecondition.
var ErrTurnRetryUnavailable = errors.New("the agent no longer offers this turn retry")
