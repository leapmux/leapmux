package cline

import (
	"regexp"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The error texts of a failed Cline run. Cline states the failure in the
// `error` string of run.failed only, with the provider's message and, for an
// API error, a trailing request ID.
var (
	// clineRequestIDRe matches the request ID that Cline appends to the
	// provider's message.
	clineRequestIDRe = regexp.MustCompile(`\s*\(Request ID: [^()]*\)\s*$`)
	// clinePassResetRe matches a ClinePass limit and captures its delay, as in
	// "The limit resets in 5h".
	clinePassResetRe = regexp.MustCompile(`(?i)\bclinepass limit\b.*?\bresets in\s+(\d{1,6}\s*[dhm])\b`)
	// clineFreeResetRe matches the daily limit of a free model and captures
	// its delay, as in "Try again in 23h 59m".
	clineFreeResetRe = regexp.MustCompile(`(?i)\bfree limit reached on model\b.*?\btry again in\s+((?:\d{1,6}\s*[dhms]\b\s*)+)`)
	clineBillingRes  = []*regexp.Regexp{
		regexp.MustCompile(`No access to ClinePass subscription models yet`),
		regexp.MustCompile(`Organization accounts cannot use ClinePass subscriptions`),
	}
	clineTransientRes = []*regexp.Regexp{
		regexp.MustCompile(`^Overloaded\b`),
		regexp.MustCompile(`^Cannot connect to API:`),
		regexp.MustCompile(`^Response stream ended without a finish reason\.`),
		regexp.MustCompile(`(?i)<title>\s*5\d\d\b`),
		regexp.MustCompile(`(?i)\b(?:internal server error|service unavailable|bad gateway|gateway time-?out)\b`),
	}
)

// runEndFailure decides the verdict of the end event of a run. Only a run that
// failed with an error, and that the user did not interrupt, can retry.
// observedAt is the event's own timestamp, and it is the base of a delay that
// the error states.
func runEndFailure(end runEnd, interrupted bool, observedAt time.Time) providerkit.TurnFailure {
	if interrupted || end.Reason != contracts.ClineRunReasonError {
		return providerkit.TurnFailure{}
	}
	return clineErrorFailure(clineRequestIDRe.ReplaceAllString(end.Error, ""), observedAt)
}

// clineErrorFailure classifies the error text of a failed run, with the
// request ID removed.
func clineErrorFailure(errorText string, observedAt time.Time) providerkit.TurnFailure {
	vendor, vendorMatched := modelvendor.ClassifyError(errorText, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	if failure, ok := clineDelayedLimit(errorText, clinePassResetRe, "Cline reported its ClinePass limit", observedAt); ok {
		return failure
	}
	if failure, ok := clineDelayedLimit(errorText, clineFreeResetRe, "Cline reported the free limit of the model", observedAt); ok {
		return failure
	}
	for _, billing := range clineBillingRes {
		if billing.MatchString(errorText) {
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Cline reported that the account has no ClinePass access"}
		}
	}
	for _, transient := range clineTransientRes {
		if transient.MatchString(errorText) {
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Cline reported a transient failure"}
		}
	}
	if vendorMatched {
		return vendor.Failure
	}
	return providerkit.TurnFailure{}
}

// clineDelayedLimit reads a usage limit whose text states a delay, with
// pattern capturing the delay. The words before the delay state the window,
// such as "5-hour" or "Daily". The verdict lifts at observedAt plus the delay
// when the delay is valid, and it falls back to the window otherwise.
func clineDelayedLimit(errorText string, pattern *regexp.Regexp, cause string, observedAt time.Time) (providerkit.TurnFailure, bool) {
	match := pattern.FindStringSubmatchIndex(errorText)
	if match == nil {
		return providerkit.TurnFailure{}, false
	}
	failure := providerkit.TurnFailure{
		Kind:   providerkit.TurnFailureUsageLimited,
		Window: providerkit.UsageWindowOf(errorText[:match[2]]),
		Cause:  cause,
	}
	if delay, ok := providerkit.ParseRetryDelay(strings.TrimSpace(errorText[match[2]:match[3]])); ok {
		failure = providerkit.WithResetHint(failure, observedAt.Add(delay), observedAt)
	}
	return failure, true
}
