package letta

import (
	"regexp"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The texts of a failed Letta turn. Letta Code states the failure in the
// `error` string of turn_finished, which holds the error object as JSON text.
var (
	// lettaBillingReasonRe matches a Letta Cloud billing reason as a whole
	// token. Letta Code reads these reasons from the error's `reasons` list and
	// from its message.
	lettaBillingReasonRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9-])(not-enough-credits|free-usage-exceeded|premium-usage-exceeded|standard-usage-exceeded|basic-usage-exceeded|agents-limit-exceeded)(?:$|[^a-z0-9-])`)
	// lettaTryAgainRe captures the delay of a ChatGPT plan limit, which Letta
	// Code rounds to whole minutes: "Try again in ~2263 min."
	lettaTryAgainRe = regexp.MustCompile(`\bTry again in (~\s*\d{1,9}\s*min)\b`)
	// lettaResetsRe captures the delay of a Letta Cloud quota, which Letta
	// Code rounds up to whole minutes: "Resets at 3:45 PM (2h 5m)".
	lettaResetsRe = regexp.MustCompile(`\bResets at [^()\n]{1,40}\((\d{1,6}h(?: \d{1,2}m)?|\d{1,6}m)\)`)
)

const (
	// lettaTryAgainMargin covers the rounding of "Try again in ~N min" to the
	// nearest minute.
	lettaTryAgainMargin = 30 * time.Second
	// lettaResetsMargin covers the rounding up of "Resets at … (Xh Ym)" to
	// the next minute.
	lettaResetsMargin = 60 * time.Second
)

// lettaTurnFailure decides the verdict of one root turn end. rootRetryable is
// the retryable flag of the turn's root error_message delta, or nil when the
// turn sent none. interrupted records that the user stopped the turn, so a
// failure that won the race against the abort does not retry. observedAt is
// the base of a delay that the error states.
//
// Letta calls a model API failure retryable by its status alone, a 429
// included. The vendor's indicative verdict on the text, such as a monthly
// usage limit, is more specific, so it decides before that class.
func lettaTurnFailure(stopReason, errorText string, rootRetryable *bool, interrupted bool, observedAt time.Time) providerkit.TurnFailure {
	if interrupted || !lettaStopReasonStatesFailure(stopReason) {
		return providerkit.TurnFailure{}
	}
	vendor, vendorMatched := modelvendor.ClassifyError(errorText, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	if stopReason == contracts.LettaStopReasonLLMAPIError {
		if rootRetryable != nil && !*rootRetryable {
			return providerkit.TurnFailure{}
		}
		if vendorMatched {
			return vendor.Failure
		}
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Letta reported a retryable model API failure"}
	}
	if match := lettaBillingReasonRe.FindStringSubmatch(errorText); match != nil {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Letta reported " + match[1]}
	}
	if failure, ok := lettaDelayedLimit(errorText, lettaTryAgainRe, lettaTryAgainMargin, observedAt); ok {
		return failure
	}
	if failure, ok := lettaDelayedLimit(errorText, lettaResetsRe, lettaResetsMargin, observedAt); ok {
		return failure
	}
	if vendorMatched {
		return vendor.Failure
	}
	return providerkit.TurnFailure{}
}

// lettaStopReasonStatesFailure reports whether a stop reason states a failed
// model request, which alone can retry.
func lettaStopReasonStatesFailure(stopReason string) bool {
	return stopReason == contracts.LettaStopReasonError || stopReason == contracts.LettaStopReasonLLMAPIError
}

// lettaDelayedLimit reads a usage limit whose text states a rounded delay,
// with pattern capturing the delay. The verdict lifts at observedAt plus the
// delay and margin when the delay is valid. The texts state no window, so the
// verdict falls back to an unstated window otherwise.
func lettaDelayedLimit(errorText string, pattern *regexp.Regexp, margin time.Duration, observedAt time.Time) (providerkit.TurnFailure, bool) {
	match := pattern.FindStringSubmatch(errorText)
	if match == nil {
		return providerkit.TurnFailure{}, false
	}
	failure := providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Cause: "Letta reported a usage limit"}
	if delay, ok := providerkit.ParseRetryDelay(match[1]); ok {
		failure = providerkit.WithResetHint(failure, observedAt.Add(delay+margin), observedAt)
	}
	return failure, true
}
