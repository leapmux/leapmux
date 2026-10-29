package amp

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The error texts of a failed Amp turn. Amp states the failure in the `error`
// string of its `result` line only: the CLI drops the server's code and its
// retryAt. For an unauthorized request, a full context, an overload, a stalled
// stream, and exhausted credits, the CLI prints its own title and description,
// joined by a space. Every other error passes through as the server wrote it.
var (
	// ampFreeTierRe is the hourly free-tier variant of the credits text. Amp
	// 0.0.1781034998 rewrites the stream-json error with freeTierEnabled set to
	// false, so the stream prints the paid variant. The free-tier text is the
	// wording of the same rewrite with the flag set. It must match before
	// ampBillingRe, because it starts with the same title.
	ampFreeTierRe = regexp.MustCompile(`^Out of Credits .*wait until the next hour starts`)
	ampBillingRe  = regexp.MustCompile(`^Out of Credits `)
	// ampTransientRes are Amp's own titles and the transport failures that it
	// passes through. None of them carries a server body.
	ampTransientRes = []*regexp.Regexp{
		regexp.MustCompile(`^Model Provider Overloaded `),
		regexp.MustCompile(`^Model Stream Timed Out `),
		regexp.MustCompile(`^fetch failed`),
		regexp.MustCompile(`ECONNRESET`),
		regexp.MustCompile(`^terminated$`),
		regexp.MustCompile(`response incomplete`),
	}
	ampQuotaRe = regexp.MustCompile(`You've exceeded your usage quota|free usage limit`)
	// ampServerErrorStatusRe and ampRateLimitRe match a status that Amp
	// passes through, with the server's body after it.
	ampServerErrorStatusRe = regexp.MustCompile(`^API error \(5\d\d\)|^5\d\d `)
	ampRateLimitRe         = regexp.MustCompile(`^Rate limit exceeded|^429 `)
)

// ampTurnFailure decides the verdict of one Amp turn end. Only an error
// completion can retry. A turn that the user stopped never retries, even when
// its own failure won the race against the stop signal. printedLine is the
// `result` line that Amp printed for the turn, or nil for a turn whose row the
// worker writes. That row states how the process exited, such as a crash with
// "read ECONNRESET" in its stderr, and not a model failure, so it never
// retries.
func ampTurnFailure(completion agent.MessageCompletion, stopRequested bool, printedLine []byte, observedAt time.Time) providerkit.TurnFailure {
	if completion != agent.MessageCompletionError || stopRequested || printedLine == nil {
		return providerkit.TurnFailure{}
	}
	var line ampLine
	if err := json.Unmarshal(printedLine, &line); err != nil {
		return providerkit.TurnFailure{}
	}
	return ampErrorFailure(line.Error, observedAt)
}

// ampErrorFailure classifies the error text of a failed Amp turn. observedAt
// limits a reset instant that a vendor's text states.
//
// Amp's own wording decides before the vendor's indicative verdict. A status
// that Amp passes through decides after it: the server's body follows the
// status, and a vendor rule that reads that body, such as a monthly usage
// limit, is more specific than the status.
func ampErrorFailure(errorText string, observedAt time.Time) providerkit.TurnFailure {
	vendor, vendorMatched := modelvendor.ClassifyError(errorText, observedAt)
	switch {
	case vendorMatched && vendor.Definitive:
		return vendor.Failure
	case ampFreeTierRe.MatchString(errorText):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Amp reported its hourly free usage limit"}
	case ampBillingRe.MatchString(errorText):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Amp reported that the account is out of credits"}
	case matchesAny(errorText, ampTransientRes):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Amp reported a transient failure"}
	case ampQuotaRe.MatchString(errorText):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated, Cause: "Amp reported an exhausted usage quota"}
	case vendorMatched:
		return vendor.Failure
	case ampServerErrorStatusRe.MatchString(errorText):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Amp reported a transient failure"}
	case ampRateLimitRe.MatchString(errorText):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Amp reported a rate limit"}
	}
	return providerkit.TurnFailure{}
}

// matchesAny reports whether any of patterns matches text.
func matchesAny(text string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}
