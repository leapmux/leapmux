package mimo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Codes of the Xiaomi gateway (R/vendors.md section 4) in a response body.
// The gateway states each in error.code, and its number in error.biz_code.
const (
	// xiaomiCodeMembershipRequired is a missing or expired membership, which
	// only a subscription lifts.
	xiaomiCodeMembershipRequired = "membership_required"
	xiaomiBizCodeMembership      = "30012"
	// xiaomiCodeWeeklyQuota is the spent weekly quota. The gateway states no
	// reset instant for it.
	xiaomiCodeWeeklyQuota    = "subscription_quota_exhausted"
	xiaomiBizCodeWeeklyQuota = "30011"
)

// Zen gateway error types (R/opencode-kilo-mimo.md) in error.type of a
// response body. MiMo Code is a fork of OpenCode, and it serves Zen models too.
const (
	zenCreditsError        = "CreditsError"
	zenRateLimitError      = "RateLimitError"
	zenUsageLimitErrorTail = "UsageLimitError"
)

// mimoErrorNameInvalidOutput is the error of a turn whose model answered with
// nothing usable after MiMo's own continuations.
const mimoErrorNameInvalidOutput = "InvalidOutputError"

// gatewayError is the error object of a response body:
// {"error":{"code":...,"type":...,"biz_code":...}}. Each field is "" when the
// body does not state it.
type gatewayError struct {
	code      string
	errorType string
	bizCode   string
}

// decodeGatewayError reads the error object of a Xiaomi or Zen response body.
// A gateway states code and biz_code as a string or as a number, so each is
// read as text. A body of another shape gives the zero value.
func decodeGatewayError(body string) gatewayError {
	if body == "" {
		return gatewayError{}
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(body), &envelope) != nil || !providerkit.RawPresent(envelope.Error) {
		return gatewayError{}
	}
	var fields struct {
		Code    json.RawMessage `json:"code"`
		Type    json.RawMessage `json:"type"`
		BizCode json.RawMessage `json:"biz_code"`
	}
	// An error that is a bare string, as {"error":"Internal Server Error"}, states no fields.
	if json.Unmarshal(envelope.Error, &fields) != nil {
		return gatewayError{}
	}
	return gatewayError{code: scalarText(fields.Code), errorType: scalarText(fields.Type), bizCode: scalarText(fields.BizCode)}
}

// scalarText returns a JSON string's value, or a JSON number's literal. It is
// "" for an absent field and for any other JSON value.
func scalarText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

// mimoResponse is the HTTP answer that an APIError states.
type mimoResponse struct {
	statusCode int
	headers    map[string]string
	body       string
}

// responseOf decodes the HTTP answer of failure. A field that is absent, or of
// an unexpected type, reads as its zero value.
func responseOf(failure mimoError) mimoResponse {
	var response mimoResponse
	if json.Unmarshal(failure.Data.StatusCode, &response.statusCode) != nil {
		response.statusCode = 0
	}
	if json.Unmarshal(failure.Data.ResponseHeaders, &response.headers) != nil {
		response.headers = nil
	}
	if json.Unmarshal(failure.Data.ResponseBody, &response.body) != nil {
		response.body = ""
	}
	return response
}

// mimoTurnFailure classifies the error that failed a MiMo turn. The stages run
// in order, and the first that decides wins:
//
//  1. A definitive vendor verdict on the message, then on the response body.
//  2. A billing block: HTTP 402, Xiaomi's membership_required, or Zen's
//     CreditsError.
//  3. Xiaomi's spent weekly quota, which states no reset instant.
//  4. An indicative vendor verdict on the message, then on the body.
//  5. A rate limit: HTTP 429, or a Zen usage or rate limit in the body.
//  6. A transient failure: a retryable error, HTTP 5xx, or an answer with
//     nothing usable (InvalidOutputError).
//
// Then a deadline in the response headers turns a rate limit, or a usage limit
// with no reset instant, into a usage limit that lifts at that deadline.
func mimoTurnFailure(failure mimoError, observedAt time.Time) providerkit.TurnFailure {
	response := responseOf(failure)
	vendor, matched := modelvendor.ClassifyErrors([]string{failure.Data.Message, response.body}, observedAt)
	if matched && vendor.Definitive {
		return vendor.Failure
	}
	var indicative *providerkit.TurnFailure
	if matched {
		indicative = &vendor.Failure
	}
	body := decodeGatewayError(response.body)
	switch {
	case response.statusCode == http.StatusPaymentRequired:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the model provider answered HTTP 402"}
	case body.code == xiaomiCodeMembershipRequired || body.bizCode == xiaomiBizCodeMembership:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the Xiaomi gateway reported " + xiaomiCodeMembershipRequired}
	case body.errorType == zenCreditsError:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the model gateway reported " + zenCreditsError}
	}
	verdict := retryableMiMoFailure(failure, response, body, indicative)
	if resetAt, ok := providerkit.RetryAfterInstant(response.headers, observedAt); ok {
		verdict = providerkit.WithResetHint(verdict, resetAt, observedAt)
	}
	return verdict
}

// retryableMiMoFailure is stages 3 to 6 of mimoTurnFailure. indicative is the
// first indicative vendor verdict, or nil.
func retryableMiMoFailure(failure mimoError, response mimoResponse, body gatewayError, indicative *providerkit.TurnFailure) providerkit.TurnFailure {
	switch {
	case body.code == xiaomiCodeWeeklyQuota || body.bizCode == xiaomiBizCodeWeeklyQuota:
		return providerkit.TurnFailure{
			Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess,
			Cause: "the Xiaomi gateway reported its spent weekly quota",
		}
	case indicative != nil:
		return *indicative
	case response.statusCode == http.StatusTooManyRequests:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the model provider answered HTTP 429"}
	case body.errorType == zenRateLimitError || strings.HasSuffix(body.errorType, zenUsageLimitErrorTail):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the model gateway reported " + body.errorType}
	case response.statusCode >= http.StatusInternalServerError:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: fmt.Sprintf("the model provider answered HTTP %d", response.statusCode)}
	case failure.Data.IsRetryable:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "MiMo reported a retryable failure"}
	case failure.Name == mimoErrorNameInvalidOutput:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "the model answered with nothing usable"}
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}

// applyTurnVerdict applies the automatic-retry verdict of the turn of epoch,
// once. failure is the failure that the divider reports, or nil for a turn
// that ended well or by a stop. A second call for the same epoch, which only a
// repeated end of one turn makes, applies nothing.
func (a *Agent) applyTurnVerdict(epoch uint64, failure *mimoFailure) {
	if !a.decideTurnVerdict(epoch) {
		return
	}
	a.Mu.Lock()
	stopped := a.turnEpoch == epoch && a.retryStopRequested
	a.Mu.Unlock()
	if failure == nil || stopped || a.IntentionalStopRequested() {
		providerkit.ApplyTurnFailure(a.sink, providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}, nil)
		return
	}
	providerkit.ApplyTurnFailure(a.sink, mimoTurnFailure(failure.err, a.clock.Now()), failure.raw)
}

// applyCompactionVerdict applies the verdict of a manual compaction's end,
// providerkit.CompactionEnd: it schedules no retry, whatever the compaction's
// outcome. The caller spent manualCompactionVerdictDue, so each manual
// compaction applies it once. epoch is the turn that the compaction's end also
// ends, which then counts as decided, or 0 when no turn runs.
func (a *Agent) applyCompactionVerdict(epoch uint64) {
	if epoch != 0 && !a.decideTurnVerdict(epoch) {
		return
	}
	providerkit.ApplyTurnFailure(a.sink, providerkit.CompactionEnd(), nil)
}

// decideTurnVerdict records that the turn of epoch applied its verdict. It
// reports false when an earlier call already recorded that turn.
func (a *Agent) decideTurnVerdict(epoch uint64) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	decided := a.retryDecidedEpoch == epoch
	a.retryDecidedEpoch = epoch
	return !decided
}

// takeManualCompactionVerdictLocked spends the verdict mark of the manual
// compaction. due reports whether the mark stood. epoch is the running turn,
// which the compaction's end also ends, or 0 when no turn runs. The caller
// holds a.Mu.
func (a *Agent) takeManualCompactionVerdictLocked() (due bool, epoch uint64) {
	due = a.manualCompactionVerdictDue
	a.manualCompactionVerdictDue = false
	if a.turnActive {
		epoch = a.turnEpoch
	}
	return due, epoch
}
