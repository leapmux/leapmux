package commandcode

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// commandcodeRunError is params.error of a turn/completed that a run error
// ended. A transport error states Status: the HTTP status, or 0 for a network
// failure. Every other error states none.
type commandcodeRunError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Status  *int   `json:"status"`
}

// commandcodeRunErrorOf decodes params.error. Command Code may state the error
// as a string, which carries no status. It returns nil for an absent or
// unreadable error.
func commandcodeRunErrorOf(raw json.RawMessage) *commandcodeRunError {
	if len(raw) == 0 {
		return nil
	}
	var message string
	if json.Unmarshal(raw, &message) == nil {
		return &commandcodeRunError{Message: message}
	}
	var runError commandcodeRunError
	if json.Unmarshal(raw, &runError) != nil {
		return nil
	}
	return &runError
}

// The texts of a run error. Command Code writes a transport error as
// "POST /alpha/generate → <status> <type>: <server message>".
var (
	commandcodeBillingRe    = regexp.MustCompile(`(?i)premium_credits_exhausted|model_not_in_plan|insufficient credits`)
	commandcodeResetRe      = regexp.MustCompile(`(?i)\bresets at (\d{4}-\d{2}-\d{2}T[\d:.]+Z)`)
	commandcodeUsageLimitRe = regexp.MustCompile(`(?i)\busage limit\b`)
)

// commandcodeTurnFailure decides the verdict of one turn end. Only a turn that
// a run error ended can retry. runError is nil for a turn end that states no
// error, which includes the row that the worker writes when the process exits.
// interrupted records that the user stopped the turn: Command Code catches the
// error of a stopped request before it sets its abort flag, so a stopped turn
// can end as a run error. observedAt is the base of a reset instant.
func commandcodeTurnFailure(stopReason string, runError *commandcodeRunError, interrupted bool, observedAt time.Time) providerkit.TurnFailure {
	if stopReason != contracts.CommandCodeStopReasonRunError || runError == nil || interrupted {
		return providerkit.TurnFailure{}
	}
	message := runError.Message
	vendor, vendorMatched := modelvendor.ClassifyError(message, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	if commandcodeBillingRe.MatchString(message) {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Command Code reported that the plan does not cover the request"}
	}
	if runError.Status == nil {
		return providerkit.TurnFailure{}
	}
	// A status is generic. The vendor's indicative verdict on the text, such as
	// a monthly usage limit, is more specific, so it decides before the status
	// alone does. Command Code's own plan limit wording decides before both.
	switch status := *runError.Status; {
	case status == 429:
		if failure, ok := commandcodePlanLimitFailure(message, observedAt); ok {
			return failure
		}
		if vendorMatched {
			return vendor.Failure
		}
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Command Code reported a rate limit"}
	case status == 0, status == 408, status >= 500:
		if vendorMatched {
			return vendor.Failure
		}
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Command Code reported a transient failure"}
	}
	return providerkit.TurnFailure{}
}

// commandcodePlanLimitFailure reads the plan limit of a 429. A plan limit
// states its window, and it may state the instant it resets. It returns false
// for a 429 that states neither.
func commandcodePlanLimitFailure(message string, observedAt time.Time) (providerkit.TurnFailure, bool) {
	match := commandcodeResetRe.FindStringSubmatch(message)
	if match == nil && !commandcodeUsageLimitRe.MatchString(message) {
		return providerkit.TurnFailure{}, false
	}
	failure := providerkit.TurnFailure{
		Kind:   providerkit.TurnFailureUsageLimited,
		Window: providerkit.UsageWindowOf(message),
		Cause:  "Command Code reported its plan usage limit",
	}
	if match != nil {
		if resetAt, err := time.Parse(time.RFC3339Nano, match[1]); err == nil {
			failure = providerkit.WithResetHint(failure, resetAt, observedAt)
		}
	}
	return failure, true
}
