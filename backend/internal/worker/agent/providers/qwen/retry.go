package qwen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Qwen Code states the cause of a failed prompt in `data.details` of a -32603
// JSON-RPC error, as the ACP TypeScript SDK answers an unexpected failure. These
// are the prefixes of the causes that its own code writes.
const (
	// qwenQuotaExhaustedPrefix opens a quota that Qwen does not retry. The
	// provider's own text follows, and it can state the reset instant.
	qwenQuotaExhaustedPrefix = "Quota exhausted: "
	// qwenOAuthDiscontinuedPrefix opens the refusal of the discontinued Qwen
	// OAuth free tier, which only another auth method lifts.
	qwenOAuthDiscontinuedPrefix = "Qwen OAuth free tier has been discontinued"
)

// The JSON-RPC codes of a failed prompt.
const (
	// qwenInternalErrorCode is the code that the ACP TypeScript SDK answers
	// for an unexpected failure, with the cause in its data.
	qwenInternalErrorCode = -32603
	// qwenRateLimitCode is the code of a rate limit that Qwen gave up on.
	// Qwen sends the same bytes for an OpenAI insufficient_quota, so the code
	// alone cannot separate a billing block.
	qwenRateLimitCode = 429
)

// qwenResetMaxAhead is the farthest a year-less reset stamp can lie ahead. The
// stamp states no year, so the year is the one that puts the instant inside
// this window.
const qwenResetMaxAhead = 366 * 24 * time.Hour

var (
	// qwenResetRe reads a reset stamp of a Bailian token plan, which states no
	// year: "The quota will reset at 07-27 09:25:00 UTC".
	qwenResetRe = regexp.MustCompile(`reset at (\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}) UTC`)
	// qwenTransientRe matches the causes of a transient failure: a 5xx from the
	// provider, the OpenAI SDK's network failure, and Qwen's request timeout.
	qwenTransientRe = regexp.MustCompile(`^(?:5\d\d\b|Connection error\.|Request timeout after)`)
	// qwenOverloadedRe matches an overload that the provider states in its
	// stream, with no HTTP status.
	qwenOverloadedRe = regexp.MustCompile(`(?i)overloaded`)
	// qwenClientErrorRe matches a cause that opens with a 4xx status, which an
	// overload wording does not make transient.
	qwenClientErrorRe = regexp.MustCompile(`^4\d\d\b`)
)

// finishQwenPrompt returns the retry verdict of a prompt that ended.
func (a *Agent) finishQwenPrompt(err error, stopped bool) acp.PromptEnding {
	a.stateMu.Lock()
	clock := a.clock
	a.stateMu.Unlock()
	failure, payload := qwenTurnFailure(err, stopped, clock.Now())
	return acp.PromptEnding{Retry: &acp.TurnVerdict{Failure: failure, Payload: payload}}
}

// qwenTurnFailure classifies the end of one prompt. observedAt is the instant
// the prompt ended, which dates a reset stamp. payload is the JSON-RPC error
// that carries the failure, or nil.
func qwenTurnFailure(err error, stopped bool, observedAt time.Time) (failure providerkit.TurnFailure, payload []byte) {
	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	if stopped || err == nil {
		return notRetryable, nil
	}
	rpcErr, ok := providerkit.AsJSONRPCResponseError(err)
	if !ok {
		return notRetryable, nil
	}
	payload = rpcErr.WireJSON()
	switch rpcErr.Code {
	case qwenRateLimitCode:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Qwen Code reported a rate limit"}, payload
	case qwenInternalErrorCode:
		return qwenInternalErrorFailure(rpcErr.Data, observedAt), payload
	}
	return notRetryable, payload
}

// qwenInternalErrorFailure classifies the data of a -32603 error. The SDK puts
// a cause that is no JSON into `details`, and a JSON cause in place of the
// whole data, so the vendor's wording is in one of the two.
func qwenInternalErrorFailure(data json.RawMessage, observedAt time.Time) providerkit.TurnFailure {
	details := providerkit.StringField(data, "details")
	vendorText := details
	if vendorText == "" {
		vendorText = string(data)
	}
	vendor, vendorMatched := modelvendor.ClassifyError(vendorText, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	switch {
	case strings.HasPrefix(details, qwenQuotaExhaustedPrefix):
		failure := providerkit.TurnFailure{
			Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowOf(details),
			Cause: "Qwen Code reported an exhausted quota",
		}
		if vendorMatched {
			failure = vendor.Failure
		}
		if resetAt, ok := qwenQuotaReset(details, observedAt); ok {
			failure = providerkit.WithResetHint(failure, resetAt, observedAt)
		}
		return failure
	case strings.HasPrefix(details, qwenOAuthDiscontinuedPrefix):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Qwen Code reported that its OAuth free tier is discontinued"}
	case qwenTransientRe.MatchString(details),
		qwenOverloadedRe.MatchString(details) && !qwenClientErrorRe.MatchString(details):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Qwen Code reported a transient model failure"}
	case vendorMatched:
		return vendor.Failure
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}

// qwenQuotaReset reads the year-less reset stamp of an exhausted quota. The
// year is the one that puts the instant after observedAt and at most
// qwenResetMaxAhead ahead. A date that the year lacks, such as 02-29 in a
// common year, takes the next year. The caller validates the instant further.
func qwenQuotaReset(details string, observedAt time.Time) (time.Time, bool) {
	match := qwenResetRe.FindStringSubmatch(details)
	if match == nil {
		return time.Time{}, false
	}
	year := observedAt.UTC().Year()
	for _, candidate := range []int{year, year + 1} {
		resetAt, err := time.Parse("2006-01-02 15:04:05", fmt.Sprintf("%04d-%s %s", candidate, match[1], match[2]))
		if err != nil {
			continue
		}
		if resetAt.After(observedAt) && !resetAt.After(observedAt.Add(qwenResetMaxAhead)) {
			return resetAt, true
		}
	}
	return time.Time{}, false
}
