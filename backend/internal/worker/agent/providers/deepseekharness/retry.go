package deepseekharness

import (
	"encoding/json"
	"math"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// dshTurnEndError is the reason of a turn/end event. A failed turn states
// kind "error" and the error that ended it.
type dshTurnEndError struct {
	Reason struct {
		Kind  string `json:"kind"`
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			// ProviderRetryAfterMs is the provider's Retry-After, in
			// milliseconds, when dsh ends the turn instead of waiting.
			ProviderRetryAfterMs *float64 `json:"providerRetryAfterMs"`
		} `json:"error"`
	} `json:"reason"`
}

// The kind of a turn/end reason that states a failure.
const dshReasonKindError = "error"

// The dsh error codes, by verdict.
var (
	dshBillingCodes   = map[string]bool{"QUOTA": true, "ACCOUNT_QUOTA": true}
	dshTransientCodes = map[string]bool{"SERVER": true, "TIMEOUT": true, "TRANSPORT": true, "EMPTY_RESPONSE": true}
)

// dshCodeRateLimit is the code of a rate limit, after dsh's own retries or
// with a Retry-After longer than dsh waits.
const dshCodeRateLimit = "RATE_LIMIT"

// dshTurnEndFailure decides the verdict of the turn/end event of the root
// stream. A turn that the user stopped never retries, even when its own
// failure won the race against the cancel. observedAt is the event's own time,
// which limits a vendor's reset instant and is the base of the provider's
// stated wait. A turn/end that does not decode states no failure.
func dshTurnEndFailure(data json.RawMessage, interrupted bool, observedAt time.Time) providerkit.TurnFailure {
	var end dshTurnEndError
	if interrupted || json.Unmarshal(data, &end) != nil || end.Reason.Kind != dshReasonKindError {
		return providerkit.TurnFailure{}
	}
	failure := dshErrorFailure(end.Reason.Error.Message, end.Reason.Error.Code, observedAt)
	if wait := end.Reason.Error.ProviderRetryAfterMs; wait != nil && *wait > 0 && *wait <= math.MaxInt64/float64(time.Millisecond) {
		failure = providerkit.WithResetHint(failure, observedAt.Add(time.Duration(*wait*float64(time.Millisecond))), observedAt)
	}
	return failure
}

// dshErrorFailure classifies the message and the code of a failed turn.
//
// Every vendor verdict decides before a dsh code, the indicative ones
// included. dsh files a Z.AI usage limit under QUOTA (1308) and under
// RATE_LIMIT (1310 on the pi-ai route). When its reset stamp is implausible,
// only the vendor's window wording tells the limit apart from a billing block
// or a rate limit.
func dshErrorFailure(message, code string, observedAt time.Time) providerkit.TurnFailure {
	if vendor, ok := modelvendor.ClassifyError(message, observedAt); ok {
		return vendor.Failure
	}
	switch {
	case dshBillingCodes[code]:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "DeepSeek Harness reported " + code}
	case dshTransientCodes[code]:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "DeepSeek Harness reported a transient failure"}
	case code == dshCodeRateLimit:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "DeepSeek Harness reported a rate limit"}
	}
	return providerkit.TurnFailure{}
}

// dshEventTime returns the event's own time, or fallback when the event states
// none.
func dshEventTime(event sessionEvent, fallback time.Time) time.Time {
	if event.Time <= 0 {
		return fallback
	}
	return time.UnixMilli(event.Time).UTC()
}
