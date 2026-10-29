package fastagent

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// fast-agent 0.10.47 reports a failed model call in one of three shapes
// (.tmp/retry-research/fastagent.md):
//
//   - An Anthropic or OpenAI API error ends the prompt with stopReason
//     "refusal" after ONE agent message that fastagentInternalErrorRe reads.
//   - Any other exception fails the prompt with -32603 "Internal error" and
//     str(exc) in data.details, or as data itself when str(exc) is JSON.
//   - A Google API error fails the prompt with -32000 "Authentication
//     required", data.message "Google API Error: <status>", and the vendor's
//     message in data.details.
const (
	fastagentInternalErrorCode = -32603
	fastagentGoogleErrorCode   = -32000
)

var (
	// fastagentInternalErrorRe reads the head of the agent message of a
	// failed Anthropic or OpenAI call (build_stream_failure_response in
	// fast_agent/llm/provider/error_utils.py), and the HTTP status when the
	// SDK states one. The SDK's own message follows the head after ": ".
	fastagentInternalErrorRe = regexp.MustCompile(`^I hit an internal error while calling the model: \S+ request failed(?: for model '[^']*')?(?: \(code: [^)]*\))?(?: \(status=(\d{3})\))?`)
	// fastagentTransportMessages are the SDK messages of a call that never
	// got an HTTP response, each with the separator before it.
	fastagentTransportMessages = []string{": Connection error.", ": Request timed out."}
	// fastagentStreamFailureRe reads the details of a stream that broke or
	// never started.
	fastagentStreamFailureRe = regexp.MustCompile(`^(?:peer closed connection without sending complete message body|\w+ stream did not start within |No stream events were received for )`)
	fastagentGoogleStatusRe  = regexp.MustCompile(`^Google API Error: (\d{3})$`)
)

// observeSessionUpdate reads each session update before the base routes it
// (Hooks.SessionUpdateHandler). The model report of a native local command is
// consumed (observePromptUpdate), so it never becomes the failure record.
func (a *Agent) observeSessionUpdate(sessionID string, sink agent.ProviderServices, update json.RawMessage) bool {
	if a.observePromptUpdate(sessionID, sink, update) {
		return true
	}
	return a.observeFastagentSessionUpdate(sessionID, sink, update)
}

// observeFastagentSessionUpdate records the last non-empty agent message
// chunk of the main session and its raw update, for the turn-failure verdict.
// A chunk of another session, and one that childUpdateRoute routes to a
// subagent's transcript, belongs to a child and is skipped. It claims no
// update.
func (a *Agent) observeFastagentSessionUpdate(sessionID string, _ agent.ProviderServices, update json.RawMessage) bool {
	if sessionID != a.CurrentSessionID() {
		return false
	}
	var chunk struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(update, &chunk) != nil || chunk.SessionUpdate != contracts.ACPUpdateAgentMessageChunk ||
		chunk.Content.Type != "text" || chunk.Content.Text == "" {
		return false
	}
	if a.childUpdateRoute(chunk.SessionUpdate, chunk.Meta) != "" {
		return false
	}
	a.Mu.Lock()
	a.lastChunkText = chunk.Content.Text
	a.lastChunkRaw = append(json.RawMessage(nil), update...)
	a.Mu.Unlock()
	return false
}

// clearFastagentChunk drops the recorded chunk before a prompt starts
// (Hooks.PromptPrepared), so a chunk of an earlier turn never decides the next.
func (a *Agent) clearFastagentChunk() {
	a.Mu.Lock()
	a.lastChunkText, a.lastChunkRaw = "", nil
	a.Mu.Unlock()
}

// finishFastagentPrompt gives the completed children one more archive read,
// then returns the turn's retry verdict (Hooks.PromptEnded).
func (a *Agent) finishFastagentPrompt(err error, stopped bool) acp.PromptEnding {
	ending := a.retryChildArchives(err, stopped)
	a.Mu.Lock()
	text, raw := a.lastChunkText, a.lastChunkRaw
	a.Mu.Unlock()
	verdict := fastagentTurnFailure(err, stopped, text, raw, a.agentClock().Now())
	ending.Retry = &verdict
	return ending
}

// fastagentTurnFailure decides the retry verdict of one prompt end. text and
// raw are the last main-session chunk of the prompt.
func fastagentTurnFailure(err error, stopped bool, text string, raw json.RawMessage, observedAt time.Time) acp.TurnVerdict {
	if stopped {
		return acp.TurnVerdict{}
	}
	if err == nil {
		match := fastagentInternalErrorRe.FindStringSubmatchIndex(text)
		if match == nil {
			return acp.TurnVerdict{}
		}
		status := ""
		if match[2] >= 0 {
			status = text[match[2]:match[3]]
		}
		return acp.TurnVerdict{Failure: fastagentModelCallFailure(text, status, text[match[1]:], observedAt), Payload: raw}
	}
	responseErr, ok := providerkit.AsJSONRPCResponseError(err)
	if !ok {
		return acp.TurnVerdict{}
	}
	payload := responseErr.WireJSON()
	switch responseErr.Code {
	case fastagentInternalErrorCode:
		return acp.TurnVerdict{Failure: fastagentInternalFailure(fastagentErrorDetails(responseErr.Data), observedAt), Payload: payload}
	case fastagentGoogleErrorCode:
		return acp.TurnVerdict{Failure: fastagentGoogleFailure(responseErr.Data, observedAt), Payload: payload}
	}
	return acp.TurnVerdict{}
}

// fastagentModelCallFailure classifies the agent message of a failed
// Anthropic or OpenAI call. status is the HTTP status, empty when the call got
// no response, and tail is the rest of the message after its head.
func fastagentModelCallFailure(text, status, tail string, observedAt time.Time) providerkit.TurnFailure {
	verdict, vendorDecided := modelvendor.ClassifyError(text, observedAt)
	if vendorDecided && verdict.Definitive {
		return verdict.Failure
	}
	switch {
	case strings.HasPrefix(status, "5"):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "fast-agent reported a server error " + status}
	case status == "429" && vendorDecided:
		return verdict.Failure
	case status == "429":
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "fast-agent reported a 429"}
	case status == "" && fastagentTransportFailure(tail):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "fast-agent reported a failed connection"}
	case vendorDecided:
		return verdict.Failure
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}

// fastagentTransportFailure reports whether the tail of the message states a
// call that never got an HTTP response.
func fastagentTransportFailure(tail string) bool {
	for _, message := range fastagentTransportMessages {
		if strings.HasPrefix(tail, message) {
			return true
		}
	}
	return false
}

// fastagentInternalFailure classifies the details of a -32603 failure.
func fastagentInternalFailure(details string, observedAt time.Time) providerkit.TurnFailure {
	verdict, vendorDecided := modelvendor.ClassifyError(details, observedAt)
	switch {
	case vendorDecided && verdict.Definitive:
		return verdict.Failure
	case fastagentStreamFailureRe.MatchString(details):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "fast-agent reported a broken model stream"}
	case vendorDecided:
		return verdict.Failure
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}

// fastagentGoogleFailure classifies a -32000 failure. Only a Google API error
// states its HTTP status in data.message; a missing credential states none.
func fastagentGoogleFailure(data json.RawMessage, observedAt time.Time) providerkit.TurnFailure {
	if verdict, ok := modelvendor.ClassifyError(providerkit.StringField(data, "details"), observedAt); ok && verdict.Definitive {
		return verdict.Failure
	}
	match := fastagentGoogleStatusRe.FindStringSubmatch(providerkit.StringField(data, "message"))
	if match == nil {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	}
	switch {
	case strings.HasPrefix(match[1], "5"):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Google reported a server error " + match[1]}
	case match[1] == "429":
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Google reported a 429"}
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}

// fastagentErrorDetails reads str(exc) of a -32603 failure: data.details, or
// data itself when fast-agent sent the text as a bare string.
func fastagentErrorDetails(data json.RawMessage) string {
	if details := providerkit.StringField(data, "details"); details != "" {
		return details
	}
	text, _ := providerkit.RawString(data)
	return text
}
