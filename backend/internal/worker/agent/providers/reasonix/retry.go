package reasonix

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// reasonixStatusEventError is the event of the status update that ends a
// failed turn. It arrives before the turn's session/prompt response.
const reasonixStatusEventError = "error"

// The diagnostic kinds of a failed turn that LeapMux retries. Reasonix 1.38.7
// classifies each failure into `turnOutcome.diagnostic.kind`; its
// `turnOutcome.kind` states only "error".
const (
	// reasonixKindRecoveryWaitExhausted is a provider that stayed
	// unreachable through Reasonix's own retries, for up to 10 minutes.
	reasonixKindRecoveryWaitExhausted = "recovery_wait_exhausted"
	// reasonixKindTemporary is a failure that Reasonix classifies as
	// transient, such as a stream that ended before its completion.
	reasonixKindTemporary = "temporary"
	// reasonixKindQuota is a 402, or a provider's quota wording. It covers a
	// billing block and a period limit alike.
	reasonixKindQuota = "quota"
)

// reasonixTransportKinds are the transient kinds that Reasonix adds after
// 1.39.10, beside "temporary".
var reasonixTransportKinds = map[string]bool{
	"transport_protocol":   true,
	"transport_eof":        true,
	"transport_reset":      true,
	"transport_timeout":    true,
	"transport_dns":        true,
	"request_send_timeout": true,
}

// The failure texts of Reasonix 2.53.1, which states no diagnostic: the
// provider client writes `<provider>: status <code>: <body>` for an HTTP
// failure (internal/contract/provider/retry.go), and `<name>: stream ended
// before <event>: unexpected EOF` for a stream that dropped
// (internal/model/*/stream*.go).
var (
	reasonixHTTPStatusRe  = regexp.MustCompile(`: status (\d{3})(?::|\n|$)`)
	reasonixStreamEndedRe = regexp.MustCompile(`: stream ended before (?:completion|message_stop|interaction\.completed)\b`)
	reasonixNetworkRe     = regexp.MustCompile(`(?i)\b(?:connection refused|connection reset by peer|no such host|i/o timeout|TLS handshake timeout)\b`)
)

// reasonixPromptErrorPrefix opens the message of a failed session/prompt,
// which then states the failure text of the turn outcome.
const reasonixPromptErrorPrefix = "session/prompt: "

// reasonixTurnOutcome is the `status.turnOutcome` of a status update.
type reasonixTurnOutcome struct {
	// Kind is "error" for a failed turn.
	Kind string `json:"kind"`
	// Reason is the failure text, which holds the provider's own wording.
	Reason     string `json:"reason"`
	Diagnostic struct {
		Kind   string `json:"kind"`
		Status int    `json:"status"`
	} `json:"diagnostic"`
}

// reasonixFailureRecord is the failure that the last status update of a
// failed turn stated.
type reasonixFailureRecord struct {
	outcome reasonixTurnOutcome
	// frame is the params of the status update, the retry's source.
	frame json.RawMessage
}

// observeReasonixFailure records the turn outcome of a status update that
// ends a failed turn of the current session. The caller checked the session.
func (a *Agent) observeReasonixFailure(update reasonixStatusUpdate, params json.RawMessage) {
	if update.Event != reasonixStatusEventError || update.Status == nil || update.Status.TurnOutcome == nil {
		return
	}
	record := &reasonixFailureRecord{outcome: *update.Status.TurnOutcome, frame: append(json.RawMessage(nil), params...)}
	a.Mu.Lock()
	a.failureRecord = record
	a.Mu.Unlock()
}

// clearReasonixFailure forgets the recorded failure, when a prompt starts or a
// context clear replaces the session.
func (a *Agent) clearReasonixFailure() {
	a.Mu.Lock()
	a.failureRecord = nil
	a.Mu.Unlock()
}

// finishReasonixPrompt returns the retry verdict of a prompt that ended
// (Hooks.PromptEnded). It takes the recorded failure, so a later prompt never
// reads it.
func (a *Agent) finishReasonixPrompt(err error, stopped bool) acp.PromptEnding {
	a.Mu.Lock()
	record := a.failureRecord
	a.failureRecord = nil
	a.Mu.Unlock()
	failure := reasonixTurnFailure(err, stopped, record, a.Clock().Now())
	verdict := &acp.TurnVerdict{Failure: failure}
	if record != nil {
		verdict.Payload = record.frame
	} else if responseErr, ok := providerkit.AsJSONRPCResponseError(err); ok {
		verdict.Payload = responseErr.WireJSON()
	}
	return acp.PromptEnding{Retry: verdict}
}

// reasonixTurnFailure classifies the end of one prompt. Reasonix 1.38 states
// the failure class in the diagnostic of its last status update. Reasonix
// 2.53.1 states none, so the failure text decides: the outcome's reason, or
// else the message of the failed session/prompt, which states the same text.
// A prompt that Reasonix rejects before the turn starts states neither a
// status nor a provider failure, so it is not retried.
func reasonixTurnFailure(err error, stopped bool, record *reasonixFailureRecord, observedAt time.Time) providerkit.TurnFailure {
	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	if stopped || err == nil {
		return notRetryable
	}
	if record == nil || record.outcome.Diagnostic.Kind == "" {
		return reasonixTextFailure(reasonixFailureText(err, record), observedAt)
	}
	if vendor, ok := modelvendor.ClassifyError(record.outcome.Reason, observedAt); ok && vendor.Definitive {
		return vendor.Failure
	}
	diagnostic := record.outcome.Diagnostic
	switch {
	case diagnostic.Kind == reasonixKindRecoveryWaitExhausted && diagnostic.Status == 429:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Reasonix gave up on a rate-limited provider"}
	case diagnostic.Kind == reasonixKindRecoveryWaitExhausted:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Reasonix gave up on an unreachable provider"}
	case diagnostic.Kind == reasonixKindTemporary || reasonixTransportKinds[diagnostic.Kind]:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Reasonix reported a transient failure (" + diagnostic.Kind + ")"}
	case diagnostic.Kind == reasonixKindQuota && diagnostic.Status == 402:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Reasonix reported exhausted credits (HTTP 402)"}
	case diagnostic.Kind == reasonixKindQuota:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated, Cause: "Reasonix reported an exhausted quota"}
	}
	return notRetryable
}

// reasonixFailureText returns the failure text of a turn: the reason of its
// turn outcome, or else the message of the failed session/prompt.
func reasonixFailureText(err error, record *reasonixFailureRecord) string {
	if record != nil && record.outcome.Reason != "" {
		return record.outcome.Reason
	}
	if responseErr, ok := providerkit.AsJSONRPCResponseError(err); ok {
		return strings.TrimPrefix(responseErr.Message, reasonixPromptErrorPrefix)
	}
	return ""
}

// reasonixTextFailure classifies a failure text of Reasonix 2.53.1.
func reasonixTextFailure(text string, observedAt time.Time) providerkit.TurnFailure {
	vendor, vendorMatched := modelvendor.ClassifyError(text, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	if match := reasonixHTTPStatusRe.FindStringSubmatch(text); match != nil {
		status, _ := strconv.Atoi(match[1])
		switch {
		case status == 402:
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Reasonix reported exhausted credits (HTTP 402)"}
		case vendorMatched:
			// The vendor's wording is more specific than the status: a monthly
			// limit behind a 429 must not retry.
			return vendor.Failure
		case status == 429:
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Reasonix reported HTTP 429"}
		case status == 408 || status >= 500 && status <= 599:
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Reasonix reported HTTP " + match[1]}
		}
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	}
	if reasonixStreamEndedRe.MatchString(text) || reasonixNetworkRe.MatchString(text) {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Reasonix reported a dropped connection"}
	}
	if vendorMatched {
		return vendor.Failure
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}
