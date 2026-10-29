package grok

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The JSON-RPC codes of a failed Grok prompt.
const (
	// grokInternalErrorCode is a model failure that Grok gave up on, with
	// its cause in the data.
	grokInternalErrorCode = -32603
	// grokRateLimitedCode is a 429 that Grok gave up on, with the
	// provider's text as the data.
	grokRateLimitedCode = -32003
)

// grokFreeUsageExhausted is the code of a 429 that only a subscription lifts.
const grokFreeUsageExhausted = "subscription:free-usage-exhausted"

// grokRunOutOfCredits is the wording of a 403 that only new credits lift.
const grokRunOutOfCredits = "run out of credits"

// grokTransientMessageRe matches the cause of a network or stream failure,
// which Grok states with no HTTP status.
var grokTransientMessageRe = regexp.MustCompile(`^(?:request error:|reqwest error stream:|stream error \(overloaded_error\))`)

// grokTransientFailureTypes are the error_type values of a failed request
// whose error can be transient. "" is a request that sent no failed frame, or
// one that stated no type.
var grokTransientFailureTypes = map[string]bool{
	"":             true,
	"api":          true,
	"http":         true,
	"idle_timeout": true,
}

// grokErrorData is the data of a JSON-RPC error of Grok. Grok states a model
// failure either as an object or as the bare text.
type grokErrorData struct {
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status"`
	ErrorKind  string `json:"error_kind"`
}

// readGrokErrorData reads the data of a JSON-RPC error. Text that is a JSON
// string becomes the message. Data of any other shape, absent data included,
// gives the zero value.
func readGrokErrorData(raw json.RawMessage) grokErrorData {
	var data grokErrorData
	if text, ok := providerkit.RawString(raw); ok {
		data.Message = text
		return data
	}
	_ = json.Unmarshal(raw, &data)
	return data
}

// finishGrokPrompt returns the retry verdict of a prompt that ended, and takes
// the error_type that the main session recorded for it and the compaction's
// mark. A compaction prompt ends on CompactionEnd, whatever its outcome: a
// failed compaction leaves the task where the last turn left it, so no retry
// may resume it.
func (a *Agent) finishGrokPrompt(err error, stopped bool) acp.PromptEnding {
	a.stateMu.Lock()
	failureType, compaction := a.lastFailureType, a.compactionPrompt
	a.lastFailureType, a.compactionPrompt = "", false
	a.stateMu.Unlock()
	if compaction {
		return acp.PromptEnding{Retry: &acp.TurnVerdict{Failure: providerkit.CompactionEnd()}}
	}
	failure, payload := grokTurnFailure(err, stopped, failureType, a.Clock().Now())
	return acp.PromptEnding{Retry: &acp.TurnVerdict{Failure: failure, Payload: payload}}
}

// clearGrokPromptState forgets the error_type of a failed request that no
// prompt end took, such as one of a turn that Grok started, and the mark of a
// compaction prompt whose end never ran, when a prompt starts.
func (a *Agent) clearGrokPromptState() {
	a.stateMu.Lock()
	a.lastFailureType, a.compactionPrompt = "", false
	a.stateMu.Unlock()
}

// grokTurnFailure classifies the end of one prompt. failureType is the
// error_type of the last failed or exhausted retry_state frame of the main
// session. payload is the JSON-RPC error that carries the failure, or nil.
func grokTurnFailure(err error, stopped bool, failureType string, observedAt time.Time) (failure providerkit.TurnFailure, payload []byte) {
	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	if stopped || err == nil {
		return notRetryable, nil
	}
	rpcErr, ok := providerkit.AsJSONRPCResponseError(err)
	if !ok {
		return notRetryable, nil
	}
	payload = rpcErr.WireJSON()
	data := readGrokErrorData(rpcErr.Data)
	vendorText := data.Message
	if vendorText == "" {
		vendorText = string(rpcErr.Data)
	}
	vendor, vendorMatched := modelvendor.ClassifyError(vendorText, observedAt)
	switch {
	case vendorMatched && vendor.Definitive:
		return vendor.Failure, payload
	case data.HTTPStatus == 402,
		data.HTTPStatus == 403 && strings.Contains(data.Message, grokRunOutOfCredits):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Grok reported exhausted credits"}, payload
	case rpcErr.Code == grokRateLimitedCode && strings.Contains(vendorText, grokFreeUsageExhausted):
		return providerkit.TurnFailure{
			Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated,
			Cause: "Grok reported exhausted free usage",
		}, payload
	case rpcErr.Code == grokInternalErrorCode && grokTransientFailureTypes[failureType] && grokTransientData(data):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Grok reported a transient model failure"}, payload
	case rpcErr.Code == grokRateLimitedCode && vendorMatched:
		return vendor.Failure, payload
	case rpcErr.Code == grokRateLimitedCode:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Grok reported a rate limit"}, payload
	}
	return notRetryable, payload
}

// grokTransientData reports whether the data of a -32603 error states a
// transient cause: a 5xx other than Cloudflare's TLS failures 525 and 526, a
// network or stream failure, or an idle stream.
func grokTransientData(data grokErrorData) bool {
	if data.HTTPStatus >= 500 && data.HTTPStatus <= 599 && data.HTTPStatus != 525 && data.HTTPStatus != 526 {
		return true
	}
	return grokTransientMessageRe.MatchString(data.Message) || data.ErrorKind == "idle_timeout"
}
