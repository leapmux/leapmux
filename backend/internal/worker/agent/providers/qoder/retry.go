package qoder

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qoderResultError is the part of a `result` that states why a turn failed.
type qoderResultError struct {
	IsError bool `json:"is_error"`
	// ErrorCode is a number for Qoder's own service, such as 10500, and a
	// string for a custom provider, such as "rate_limit_exceeded".
	ErrorCode json.RawMessage `json:"error_code"`
	// TerminalReason states a stop, such as "aborted_streaming". A failure
	// leaves it empty.
	TerminalReason string   `json:"terminal_reason"`
	Errors         []string `json:"errors"`
}

// The numeric error codes of Qoder's own service, by verdict.
var (
	qoderBillingCodes = map[int64]bool{112: true, 114: true, 115: true, 116: true, 117: true, 118: true, 119: true, 122: true, 130: true}
	// qoderTransientCodes are the classes of Qoder's model service: an
	// unavailable service, a timeout, and a busy model.
	qoderTransientCodes = map[int64]bool{10500: true, 10408: true, 10605: true}
)

const (
	// qoderCodeDailyLimit is the daily usage limit, which resets daily at an
	// instant that the wire does not state.
	qoderCodeDailyLimit = 110
	// qoderCodeUsageLimitExceeded is what Qoder writes for any 429: a rate
	// limit or an exhausted quota.
	qoderCodeUsageLimitExceeded = 113
	// qoderCodeUnclassified is Qoder's code for a failed request that it
	// cannot classify, such as a reset connection or an incomplete stream.
	qoderCodeUnclassified = 500
)

// The string error codes of a custom provider that decide a verdict.
const (
	qoderCodeInsufficientQuota = "insufficient_quota"
	qoderCodeRateLimitExceeded = "rate_limit_exceeded"
)

// qoderResultFailure decides the verdict of one `result` line. Only a failed
// result of a turn that the user did not stop can retry. observedAt limits a
// reset instant that a vendor's text states.
//
// Qoder's billing, model service, and daily limit codes decide before the
// vendor's indicative verdict. 113, rate_limit_exceeded, and 500 decide after
// it: each covers several classes, so a vendor's own wording, such as a
// monthly usage limit, is more specific.
func qoderResultFailure(raw []byte, interrupted bool, observedAt time.Time) providerkit.TurnFailure {
	var result qoderResultError
	if interrupted || json.Unmarshal(raw, &result) != nil || !result.IsError || result.TerminalReason != "" {
		return providerkit.TurnFailure{}
	}
	errorText := ""
	if len(result.Errors) > 0 {
		errorText = result.Errors[0]
	}
	vendor, vendorMatched := modelvendor.ClassifyError(errorText, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	number, isNumber := providerkit.RawInt64(result.ErrorCode)
	text, isText := providerkit.RawString(result.ErrorCode)
	switch {
	case isNumber && qoderBillingCodes[number]:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Qoder reported billing code " + strconv.FormatInt(number, 10)}
	case isText && text == qoderCodeInsufficientQuota:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the provider reported " + qoderCodeInsufficientQuota}
	case isNumber && qoderTransientCodes[number]:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Qoder reported a transient failure"}
	case isNumber && number == qoderCodeDailyLimit:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Qoder reported its daily usage limit"}
	case vendorMatched:
		return vendor.Failure
	case isNumber && number == qoderCodeUsageLimitExceeded, isText && text == qoderCodeRateLimitExceeded:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Qoder reported a rate limit"}
	case isNumber && number == qoderCodeUnclassified:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Qoder reported a transient failure"}
	}
	return providerkit.TurnFailure{}
}
