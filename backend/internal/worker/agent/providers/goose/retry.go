package goose

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The error messages that Goose 1.56.0 prints as an agent message in place of
// an answer (goose-provider-types conversation/message.rs and errors.rs). The
// contract holds the generic prefix, which the goal loop also prints; the
// others are read here only.
const (
	// gooseCompactionErrorPrefix opens the message of a failed automatic
	// compaction, which ends the round as the generic error does. A failed
	// manual compaction prints the bare error (ops_compaction.rs).
	gooseCompactionErrorPrefix = "Ran into this error trying to compact: "
	gooseNetworkErrorPrefix    = "Network error: "
	gooseContextExceededPrefix = "Context length exceeded: "
	gooseRefusalPrefix         = "The provider refused this request."
	gooseEmptyResponsePrefix   = "The model returned an empty response."
)

// The prefixes of Goose's server-error and rate-limit messages, which Goose
// prints under the generic error prefix.
const (
	gooseServerErrorPrefix = contracts.GooseGoalTextErrorPrefix + "Server error: "
	gooseRateLimitPrefix   = contracts.GooseGoalTextErrorPrefix + "Rate limit exceeded: "
)

// The `data.reason` values of a failed session/prompt.
const (
	// gooseReasonCreditsExhausted is the 402 of a provider whose credits ran
	// out. Goose 1.56.0 sends it with no agent message before it.
	gooseReasonCreditsExhausted = "credits_exhausted"
	// gooseReasonTurnFailed marks a turn that ended on an error. Goose source
	// after 1.56.0 (commit 122834086b) fails the prompt with it and repeats the
	// error message in `message`.
	gooseReasonTurnFailed = "turn_failed"
)

// gooseErrorText reports whether text is one of Goose's own error messages.
func gooseErrorText(text string) bool {
	for _, prefix := range []string{
		contracts.GooseGoalTextErrorPrefix,
		gooseCompactionErrorPrefix,
		gooseNetworkErrorPrefix,
		gooseContextExceededPrefix,
		gooseRefusalPrefix,
		gooseEmptyResponsePrefix,
	} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// gooseTurnFailure decides the retry verdict of one prompt end. last is the
// final agent message of the prompt, and observedAt is the agent clock at the
// end. The payload is the raw update of the error message, or the error
// response when no message carried the failure.
func gooseTurnFailure(err error, stopped bool, last gooseLastMessage, observedAt time.Time) acp.TurnVerdict {
	if stopped {
		return acp.TurnVerdict{}
	}
	text, payload := "", json.RawMessage(nil)
	if responseErr, ok := providerkit.AsJSONRPCResponseError(err); ok {
		switch providerkit.StringField(responseErr.Data, "reason") {
		case gooseReasonCreditsExhausted:
			return acp.TurnVerdict{
				Failure: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Goose reported exhausted provider credits"},
				Payload: responseErr.WireJSON(),
			}
		case gooseReasonTurnFailed:
			text, payload = responseErr.Message, last.update
			if len(payload) == 0 {
				payload = responseErr.WireJSON()
			}
		}
	} else if err == nil && !last.inference && gooseErrorText(last.text) {
		// Goose's own messages that are no error, such as the goal notice and
		// the max-turns question, must not reach the vendor parser.
		text, payload = last.text, last.update
	}
	if text == "" {
		return acp.TurnVerdict{}
	}
	return acp.TurnVerdict{Failure: gooseTextFailure(text, observedAt), Payload: payload}
}

// gooseTextFailure classifies the text of one Goose error message.
func gooseTextFailure(text string, observedAt time.Time) providerkit.TurnFailure {
	verdict, vendorDecided := modelvendor.ClassifyError(text, observedAt)
	if vendorDecided && verdict.Definitive {
		return verdict.Failure
	}
	switch {
	case strings.HasPrefix(text, gooseServerErrorPrefix):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Goose reported a server error"}
	case strings.HasPrefix(text, gooseNetworkErrorPrefix):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Goose reported a network error"}
	case vendorDecided:
		// The vendor's own wording is more specific than Goose's rate-limit
		// class: a monthly limit inside "Rate limit exceeded" must not retry.
		return verdict.Failure
	case strings.HasPrefix(text, gooseRateLimitPrefix):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Goose reported a rate limit"}
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}
