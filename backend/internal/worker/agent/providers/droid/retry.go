package droid

import (
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// droidRetryableReasons maps each turn end reason that a retry can lift to its
// verdict when the vendor's text decides nothing.
var droidRetryableReasons = map[string]providerkit.TurnFailure{
	contracts.DroidTurnEndReasonModelProviderUnavailable: {Kind: providerkit.TurnFailureTransient, Cause: "Droid reported that the model provider is unavailable"},
	contracts.DroidTurnEndReasonModelProviderUnreachable: {Kind: providerkit.TurnFailureTransient, Cause: "Droid reported that the model provider is unreachable"},
	contracts.DroidTurnEndReasonModelRateLimited:         {Kind: providerkit.TurnFailureRateLimited, Cause: "Droid reported a rate limit"},
}

// droidTurnFailure decides the verdict of one main-session turn end. reason is
// the reason of agent_turn_completed. errorMessage is the message of the error
// notification that the turn sent, or empty when it sent none. interrupted
// records that the user stopped the turn. Droid states no reset instant, so
// the verdict never schedules at a provider's deadline.
//
// Only a failed turn that the user did not stop can retry: a completed turn,
// a stop (cancelled or process_exit), and a failure that won the race against
// the user's interrupt give NotRetryable whatever the error said.
func droidTurnFailure(reason, errorMessage string, interrupted bool, observedAt time.Time) providerkit.TurnFailure {
	if completion, _ := droidTurnCompletion(reason); completion != agent.MessageCompletionError || interrupted {
		return providerkit.TurnFailure{}
	}
	// Droid merges an exhausted quota into model_rate_limited, so the vendor's
	// sentence in the error is the only sign of a billing block.
	vendor, vendorMatched := modelvendor.ClassifyError(errorMessage, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	if reason == contracts.DroidTurnEndReasonModelUsageExhausted {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Droid reported that the model usage is exhausted"}
	}
	// Droid derives each retryable reason from the status alone, and it merges
	// every 429 into model_rate_limited. The vendor's indicative verdict on the
	// error, such as a monthly usage limit, is more specific, so it decides
	// the kind of a retryable reason.
	failure, retryable := droidRetryableReasons[reason]
	if !retryable {
		return providerkit.TurnFailure{}
	}
	if vendorMatched {
		return vendor.Failure
	}
	return failure
}

// droidTurnError is what the error notifications of one main turn stated, for
// the retry verdict of that turn's agent_turn_completed.
type droidTurnError struct {
	// generation identifies the turn that emitted the error, even after the
	// error releases that turn and the queue admits its next input.
	generation uint64
	// message is the message of the turn's last error notification.
	message string
	// interruptRequested records that the user had stopped the turn when an
	// error notification arrived.
	interruptRequested bool
}

// recordTurnErrorLocked records an error notification of the running main
// turn. A record that no completion consumed yet belongs to the same turn, so
// it keeps a stop that an earlier error of the turn noted. The caller holds Mu.
func (a *Agent) recordTurnErrorLocked(message string) {
	generation := a.turnGeneration
	interrupted := a.interruptRequested
	if a.pendingTurnError != nil {
		generation = a.pendingTurnError.generation
		interrupted = a.pendingTurnError.interruptRequested || generation == a.turnGeneration && interrupted
	}
	a.pendingTurnError = &droidTurnError{generation: generation, message: message, interruptRequested: interrupted}
}
