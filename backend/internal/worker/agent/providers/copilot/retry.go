package copilot

import (
	"encoding/json"
	"math"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The errorType values of a session.error that the turn-failure verdict reads.
const (
	copilotErrorTypeQuery     = "query"
	copilotErrorTypeQuota     = "quota"
	copilotErrorTypeRateLimit = "rate_limit"
)

// copilotAutoModelReason is the reason of the cancellation that an accepted
// auto-mode switch makes. The Auto model serves the next turn, so a pending
// retry for the limit of the old model no longer has a purpose.
const copilotAutoModelReason = "the session switched to the Auto model"

// copilotCallFailureStatus states whether the last root model.call_failure of
// a turn carried an HTTP status. The zero value means that the turn reported
// no root call failure.
type copilotCallFailureStatus int

const (
	_ copilotCallFailureStatus = iota
	// copilotCallFailureWithStatus means the last one carried a status.
	copilotCallFailureWithStatus
	// copilotCallFailureWithoutStatus means the last one carried no status: a
	// broken stream, a reset connection, or a stream timeout.
	copilotCallFailureWithoutStatus
)

// copilotTurnFailure is what the root transcript of one turn states about its
// failure. The zero value states no failure.
type copilotTurnFailure struct {
	// frame is the raw root session.error. Nil means the turn reported none.
	frame     []byte
	errorType string
	errorCode string
	message   string
	// statusCode is the HTTP status of the session.error. hasStatus is false
	// when the field is absent or null, and then statusCode is zero.
	statusCode int64
	hasStatus  bool
	// lastCallFailure is the status of the last root model.call_failure.
	lastCallFailure copilotCallFailureStatus
}

// cause is the operator sentence of a verdict that the failure decides, such
// as `Copilot reported a quota error (quota_exceeded)`.
func (f copilotTurnFailure) cause() string {
	cause := "Copilot reported a " + f.errorType + " error"
	if f.errorCode != "" {
		cause += " (" + f.errorCode + ")"
	}
	return cause
}

// copilotAutoSwitch is the root auto-mode switch request of one turn and the
// answer that the runtime reported. The zero value states no request.
type copilotAutoSwitch struct {
	requestID string
	// resetAt is the request's own timestamp plus its retryAfterSeconds. It
	// is zero when the request states no positive wait.
	resetAt time.Time
	// response is the answer of auto_mode_switch.completed, empty until then.
	response string
}

// accepted reports whether the runtime moved the session to the Auto model.
func (s copilotAutoSwitch) accepted() bool {
	return s.response == contracts.CopilotAutoModeSwitchResponseYes || s.response == contracts.CopilotAutoModeSwitchResponseYesAlways
}

// declined reports whether the session kept the model that hit the limit.
func (s copilotAutoSwitch) declined() bool {
	return s.response == contracts.CopilotAutoModeSwitchResponseNo
}

// nativeEventTime returns the instant that the runtime stamped on an event.
// An event without a stamp that parses falls back to the agent clock, so a
// test that pins the clock pins this instant too.
func (a *Agent) nativeEventTime(event copilotEvent) time.Time {
	if text, ok := providerkit.RawString(event.Timestamp); ok {
		if stamp, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return stamp
		}
	}
	return a.Clock().Now()
}

// recordNativeSessionError keeps the root session.error of the current turn.
// A later one replaces an earlier one. The caller holds outputMu.
func (a *Agent) recordNativeSessionError(raw []byte, data json.RawMessage) {
	var fields struct {
		ErrorType  json.RawMessage `json:"errorType"`
		ErrorCode  json.RawMessage `json:"errorCode"`
		Message    json.RawMessage `json:"message"`
		StatusCode json.RawMessage `json:"statusCode"`
	}
	// Each field reads on its own, so one malformed field does not discard the
	// others. Data that is not an object records a failure with no fields,
	// which gives NotRetryable.
	_ = json.Unmarshal(data, &fields)
	failure := copilotTurnFailure{frame: append([]byte(nil), raw...), lastCallFailure: a.turnFailure.lastCallFailure}
	failure.errorType, _ = providerkit.RawString(fields.ErrorType)
	failure.errorCode, _ = providerkit.RawString(fields.ErrorCode)
	failure.message, _ = providerkit.RawString(fields.Message)
	// A status that is present but not an integer is neither a 5xx nor an
	// absent status, so it supports no retry.
	failure.hasStatus = providerkit.RawPresent(fields.StatusCode)
	failure.statusCode, _ = providerkit.RawInt64(fields.StatusCode)
	a.turnFailure = failure
}

// noteNativeCallFailure records whether the latest root model.call_failure
// carried an HTTP status. The caller holds outputMu.
func (a *Agent) noteNativeCallFailure(data json.RawMessage) {
	var fields struct {
		StatusCode json.RawMessage `json:"statusCode"`
	}
	_ = json.Unmarshal(data, &fields)
	if providerkit.RawPresent(fields.StatusCode) {
		a.turnFailure.lastCallFailure = copilotCallFailureWithStatus
	} else {
		a.turnFailure.lastCallFailure = copilotCallFailureWithoutStatus
	}
}

// recordNativeAutoSwitch keeps the root auto-mode switch of the current turn.
//
// A request records its reset instant, and its completion records the answer.
// A completion of another request changes nothing. The caller holds outputMu.
func (a *Agent) recordNativeAutoSwitch(event copilotEvent, requestID string, completed bool) {
	if event.AgentID != "" {
		return
	}
	if completed {
		if a.autoSwitch.requestID == requestID {
			a.autoSwitch.response = providerkit.StringField(event.Data, "response")
		}
		return
	}
	var fields struct {
		RetryAfterSeconds json.RawMessage `json:"retryAfterSeconds"`
	}
	_ = json.Unmarshal(event.Data, &fields)
	record := copilotAutoSwitch{requestID: requestID}
	if wait, ok := copilotRetryAfter(fields.RetryAfterSeconds); ok {
		record.resetAt = a.nativeEventTime(event).Add(wait)
	}
	a.autoSwitch = record
}

// copilotMaxRetryAfterSeconds is the first wait in seconds that a
// time.Duration cannot hold.
const copilotMaxRetryAfterSeconds = float64(math.MaxInt64 / int64(time.Second))

// copilotRetryAfter reads retryAfterSeconds as a positive wait. Null, a value
// that is not a number, zero, a negative value, and a value too large for a
// time.Duration give false. WithResetHint later rejects a wait longer than 31
// days.
func copilotRetryAfter(raw json.RawMessage) (time.Duration, bool) {
	var seconds json.Number
	if !providerkit.RawPresent(raw) || json.Unmarshal(raw, &seconds) != nil {
		return 0, false
	}
	value, err := seconds.Float64()
	if err != nil || !(value > 0) || value >= copilotMaxRetryAfterSeconds {
		return 0, false
	}
	return time.Duration(value * float64(time.Second)), true
}

// takeNativeTurnFailure returns the records of the turn that ends and clears
// them. The caller holds outputMu.
func (a *Agent) takeNativeTurnFailure() (copilotTurnFailure, copilotAutoSwitch) {
	failure, autoSwitch := a.turnFailure, a.autoSwitch
	a.clearNativeTurnFailure()
	return failure, autoSwitch
}

// clearNativeTurnFailure drops the records of the current turn. The caller
// holds outputMu.
func (a *Agent) clearNativeTurnFailure() {
	a.turnFailure = copilotTurnFailure{}
	a.autoSwitch = copilotAutoSwitch{}
}

// copilotTurnEndVerdict states which retry verdict a root turn end applies.
type copilotTurnEndVerdict int

const (
	// copilotTurnEndClassified applies the verdict of copilotTurnVerdict: the
	// turn is a conversation turn.
	copilotTurnEndClassified copilotTurnEndVerdict = iota
	// copilotTurnEndCompaction applies CompactionEnd: the turn is the
	// runtime's model turn for a manual compaction, and it is the first end of
	// that compaction.
	copilotTurnEndCompaction
	// copilotTurnEndCompactionApplied applies no verdict: the turn belongs to a
	// manual compaction whose verdict already applied.
	copilotTurnEndCompactionApplied
)

type copilotTurnEnd struct {
	failure    copilotTurnFailure
	autoSwitch copilotAutoSwitch
	aborted    bool
	observedAt time.Time
	verdict    copilotTurnEndVerdict
}

// applyNativeTurnVerdict applies the one verdict of a root turn end. The
// caller holds outputMu and has persisted the turn end, so the retry
// operations follow that row.
//
// An accepted switch to the Auto model cancels the usage-limit retry for every
// root turn, a compaction's model turn included: the retry no longer has a
// purpose once the Auto model serves the session.
func applyNativeTurnVerdict(writer agent.AutoRetryWriter, end copilotTurnEnd) {
	if !end.aborted && end.autoSwitch.accepted() {
		writer.CancelUsageLimitRetry(copilotAutoModelReason)
	}
	switch end.verdict {
	case copilotTurnEndClassified:
		providerkit.ApplyTurnFailure(writer, copilotTurnVerdict(end.failure, end.autoSwitch, end.aborted, end.observedAt), end.failure.frame)
	case copilotTurnEndCompaction:
		// A compaction never schedules a retry, whatever its outcome.
		providerkit.ApplyTurnFailure(writer, providerkit.CompactionEnd(), nil)
	case copilotTurnEndCompactionApplied:
		// An earlier end of the same compaction applied its one verdict.
	}
}

// copilotTurnVerdict classifies a root turn end. observedAt is the timestamp
// of the session.idle that ends the turn.
//
// The order:
//  1. A stop gives NotRetryable.
//  2. An accepted switch gives NotRetryable. The caller cancels the usage-limit
//     retry for it.
//  3. A definitive vendor verdict on the message.
//  4. A quota code: a billing block, or a usage limit longer than a week.
//  5. A rate limit whose switch the answer declined gives a usage-limit retry at
//     the request's reset instant, when that instant is plausible.
//  6. A user rate limit gives a usage limit within a week.
//  7. A plain rate limit gives RateLimited.
//  8. A query error with a 5xx, or with no status after a call failure that
//     had no status, gives Transient.
//  9. An indicative vendor verdict on the message.
//  10. NotRetryable.
func copilotTurnVerdict(failure copilotTurnFailure, autoSwitch copilotAutoSwitch, aborted bool, observedAt time.Time) providerkit.TurnFailure {
	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	if aborted || autoSwitch.accepted() || failure.frame == nil {
		return notRetryable
	}
	vendor, vendorMatched := modelvendor.ClassifyError(failure.message, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	cause := failure.cause()
	switch failure.errorType {
	case copilotErrorTypeQuota:
		switch failure.errorCode {
		case contracts.CopilotErrorCodeSessionQuotaExceeded, contracts.CopilotErrorCodeBillingNotConfigured:
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: cause}
		case contracts.CopilotErrorCodeQuotaExceeded, contracts.CopilotErrorCodeAdditionalSpendLimitReached:
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: cause}
		}
	case copilotErrorTypeRateLimit:
		if autoSwitch.declined() {
			// WithResetHint rejects a past or a far instant, so the codes below
			// decide then. The instant is past when the answer came after it.
			hinted := providerkit.WithResetHint(providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: cause}, autoSwitch.resetAt, observedAt)
			if !hinted.ResetAt.IsZero() {
				return hinted
			}
		}
		switch failure.errorCode {
		case contracts.CopilotErrorCodeUserWeeklyRateLimited, contracts.CopilotErrorCodeUserModelRateLimited, contracts.CopilotErrorCodeUserGlobalRateLimited:
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: cause}
		case contracts.CopilotErrorCodeRateLimited, contracts.CopilotErrorCodeIntegrationRateLimited:
			// A rate limit that states no reset instant (user decision 3).
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: cause}
		}
	case copilotErrorTypeQuery:
		serverError := failure.hasStatus && failure.statusCode >= 500 && failure.statusCode <= 599
		brokenStream := !failure.hasStatus && failure.lastCallFailure == copilotCallFailureWithoutStatus
		if serverError || brokenStream {
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: cause}
		}
	}
	if vendorMatched {
		return vendor.Failure
	}
	return notRetryable
}
