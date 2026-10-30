package muse

import (
	"encoding/json"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// completionFrame is the part of a turn/completed frame that finishTurn reads.
type completionFrame struct {
	Params turnCompletion `json:"params"`
	// EmittedAtMs is the emission time in Unix milliseconds. Muse stamps a live
	// notification once, and a view/page replay omits the stamp.
	EmittedAtMs json.RawMessage `json:"emittedAtMs"`
}

// museUnixMilli turns a Unix millisecond stamp of Muse into an instant.
func museUnixMilli(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

// completionObservedAt returns the emission time of a completion. A frame
// without a positive integer stamp falls back to the agent clock, so a test that
// pins the clock pins this instant too.
func (a *Agent) completionObservedAt(frame completionFrame) time.Time {
	if ms, ok := providerkit.RawInt64(frame.EmittedAtMs); ok && ms > 0 {
		return museUnixMilli(ms)
	}
	return a.Clock().Now()
}

// museTurnError is the settled failure of a failed turn: the MSP TurnError.
type museTurnError struct {
	kind      string
	message   string
	retryable bool
	// quota states that the failure is of the quota class. MSP sets the quota
	// detail exactly then, so the presence decides, not the message.
	quota     bool
	limitKind string
	// resetsAtMs is the reset of the quota in Unix milliseconds. hasReset is
	// false when the detail states no integer reset.
	resetsAtMs int64
	hasReset   bool
}

// decodeMuseTurnError reads the error of a turn/completed. It returns false
// when the completion states none, as a synthesized host death does: that
// completion carries only a reason. Each field reads on its own, so one malformed
// field does not discard the others.
func decodeMuseTurnError(raw json.RawMessage) (museTurnError, bool) {
	var fields struct {
		Kind      json.RawMessage `json:"kind"`
		Message   json.RawMessage `json:"message"`
		Retryable json.RawMessage `json:"retryable"`
		Quota     json.RawMessage `json:"quota"`
	}
	if !providerkit.RawPresent(raw) || json.Unmarshal(raw, &fields) != nil {
		return museTurnError{}, false
	}
	var failure museTurnError
	failure.kind, _ = providerkit.RawString(fields.Kind)
	failure.message, _ = providerkit.RawString(fields.Message)
	failure.retryable = providerkit.RawBool(fields.Retryable)
	failure.quota = providerkit.RawPresent(fields.Quota)
	if failure.quota {
		var detail struct {
			LimitKind  json.RawMessage `json:"limitKind"`
			ResetsAtMs json.RawMessage `json:"resetsAtMs"`
		}
		_ = json.Unmarshal(fields.Quota, &detail)
		failure.limitKind, _ = providerkit.RawString(detail.LimitKind)
		failure.resetsAtMs, failure.hasReset = providerkit.RawInt64(detail.ResetsAtMs)
	}
	return failure, true
}

// museTurnVerdict classifies a root turn end. observedAt is the emission time of
// the completion.
//
// The order:
//  1. An outcome other than failed, a cancelled turn included, gives NotRetryable.
//  2. A definitive vendor verdict on the message.
//  3. A quota failure gives a usage-limit retry at its reset, when that reset is
//     plausible, and the window rule otherwise.
//  4. A failure that Muse calls retryable gives Transient. Muse states it after
//     its own ten provider attempts.
//  5. An indicative vendor verdict on the message.
//  6. NotRetryable.
func museTurnVerdict(completion turnCompletion, observedAt time.Time) providerkit.TurnFailure {
	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	if completion.Outcome != contracts.MuseTurnOutcomeFailed {
		return notRetryable
	}
	failure, ok := decodeMuseTurnError(completion.Error)
	if !ok {
		return notRetryable
	}
	vendor, vendorMatched := modelvendor.ClassifyError(failure.message, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	if failure.quota {
		cause := "Muse reported an exhausted quota"
		if failure.limitKind != "" {
			cause = "Muse reported an exhausted " + failure.limitKind + " quota"
		}
		limited := providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowOf(failure.message), Cause: cause}
		if !failure.hasReset {
			return limited
		}
		// WithResetHint rejects a past or a far reset, so the window rule decides
		// then, and no past-due retry fires at once and repeats.
		return providerkit.WithResetHint(limited, museUnixMilli(failure.resetsAtMs), observedAt)
	}
	if failure.retryable {
		cause := "Muse reported a retryable failure"
		if failure.kind != "" {
			cause = "Muse reported a retryable " + failure.kind + " failure"
		}
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: cause}
	}
	if vendorMatched {
		return vendor.Failure
	}
	return notRetryable
}
