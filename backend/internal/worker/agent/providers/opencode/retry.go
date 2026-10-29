package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// This file decides the automatic-retry verdict of a family turn end.
//
// The ACP adapter answers a failed prompt with -32603 "Internal error:
// <message>". That answer drops the HTTP status, the response headers, and the
// response body, which the verdict needs. The daemon keeps the whole error in
// two places: on the failed assistant message in its store, and in the
// session.error event on its own stream. The event stream and the ACP stream
// are two connections with no order between them. The event can arrive after
// the prompt response, or even during the next prompt. So the verdict reads the
// store first, and it accepts a source only when the source proves that the
// error belongs to the turn that ends.

const (
	// latestAssistantErrorReadTimeout limits the read of the stored error at a
	// turn end. The turn's rows wait for the read, so it stays short.
	latestAssistantErrorReadTimeout = 2 * time.Second
	// assistantOwnershipSlack admits a stored assistant message that the daemon
	// stamped slightly before the agent clock recorded the prompt's start. The
	// daemon and the agent read two different clocks.
	assistantOwnershipSlack = 2 * time.Second
)

// Zen gateway error types (R/opencode-kilo-mimo.md) that the recipe reads in a
// response body.
const (
	zenCreditsError        = "CreditsError"
	zenRateLimitError      = "RateLimitError"
	zenUsageLimitErrorTail = "UsageLimitError"
)

// openCodeEventSessionError is the daemon event that states the error of a
// failed turn.
const openCodeEventSessionError = "session.error"

// DaemonError is the error of a failed turn, as the daemon stores it on the
// assistant message and states it in session.error.
type DaemonError struct {
	Name string          `json:"name"`
	Data DaemonErrorData `json:"data"`
}

// DaemonErrorData holds the fields of the error that the verdict reads. An
// APIError states all of them. Every other error name states its message alone.
type DaemonErrorData struct {
	Message         string            `json:"message"`
	StatusCode      int               `json:"statusCode"`
	IsRetryable     bool              `json:"isRetryable"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
	ResponseBody    string            `json:"responseBody"`
}

// heldSessionError is the newest session.error of the current session.
type heldSessionError struct {
	raw json.RawMessage
	err DaemonError
	// afterAssistant states that the event arrived after the bridge observed
	// an assistant message of the current prompt. Only such an event can be
	// shown to belong to the prompt: an earlier one can be a late event of the
	// prompt before it.
	afterAssistant bool
}

// openCodeLongWait is the retry status that made LeapMux end the turn.
type openCodeLongWait struct {
	failure providerkit.TurnFailure
	raw     json.RawMessage
}

// holdSessionError keeps the newest session.error of the current session for
// the verdict of the turn that it fails. An event with no error decides nothing.
func (b *FamilyBase) holdSessionError(raw, properties json.RawMessage) {
	var payload struct {
		Error *DaemonError `json:"error"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil || payload.Error == nil {
		slog.Debug("ignore an OpenCode session.error with no readable error", "agent_id", b.AgentID(), "error", err)
		return
	}
	b.daemonMu.Lock()
	defer b.daemonMu.Unlock()
	b.heldSessionError = &heldSessionError{raw: slices.Clone(raw), err: *payload.Error, afterAssistant: b.lastAssistant != ""}
}

// openCodeRetryNext reads the `next` of a retry status: epoch milliseconds,
// with or without a fraction. It returns false for an absent, a malformed, or
// a non-positive value.
func openCodeRetryNext(next json.RawMessage) (time.Time, bool) {
	var milliseconds float64
	if len(next) == 0 || json.Unmarshal(next, &milliseconds) != nil || milliseconds <= 0 || math.IsInf(milliseconds, 0) {
		return time.Time{}, false
	}
	return time.UnixMicro(int64(milliseconds * 1000)), true
}

// openCodeSessionStatusRoute lists the status of every session of the daemon,
// keyed by session ID: busy, idle, or retry with its next instant.
const openCodeSessionStatusRoute = "/session/status"

// readMissedSessionStatus reads the current session's status when the event
// stream connects (openCodeQuestions.connected). The stream connects only after
// discovery, so a prompt can fail before it is open. A long wait states one
// retry status and then sleeps for hours, so a status that went out before the
// connection would never come again. The read closes that gap, as the pending
// question list does for questions; a status that both the read and the stream
// report stops the turn once (stopForLongWait).
func (b *FamilyBase) readMissedSessionStatus(ctx context.Context) {
	sessionID := b.CurrentSessionID()
	if sessionID == "" || !b.PromptActive() {
		return
	}
	raw, _, err := b.Questions.readRoute(ctx, openCodeSessionStatusRoute)
	if err != nil {
		slog.Debug("read the OpenCode session status", "agent_id", b.AgentID(), "error", err)
		return
	}
	var statuses map[string]json.RawMessage
	if json.Unmarshal(raw, &statuses) != nil {
		return
	}
	status, ok := statuses[sessionID]
	if !ok {
		return
	}
	var retry struct {
		Type string          `json:"type"`
		Next json.RawMessage `json:"next"`
	}
	if json.Unmarshal(status, &retry) != nil || retry.Type != contracts.OpenCodeStatusTypeRetry {
		return
	}
	// The payload restates the status as the event that the stream missed.
	frame, err := json.Marshal(map[string]any{
		"type":       contracts.OpenCodeEventSessionStatus,
		"properties": map[string]any{"sessionID": sessionID, "status": status},
	})
	if err != nil {
		return
	}
	b.stopForLongWait(frame, retry.Next)
}

// stopForLongWait ends the running turn when the daemon states its next retry
// more than providerkit.LongRetryWaitThreshold ahead. The daemon waits out a
// usage limit inside the open turn, for up to about 24.8 days. next is that
// instant in epoch milliseconds, and raw is the retry status. It acts once per
// prompt. An instant that the queue cannot schedule leaves the wait to the
// daemon, and so does a compaction prompt: a stop would only abandon the
// compaction, and the compaction's end schedules no retry (TurnEndVerdict).
func (b *FamilyBase) stopForLongWait(raw json.RawMessage, next json.RawMessage) {
	resetAt, ok := openCodeRetryNext(next)
	if !ok || !b.PromptActive() {
		return
	}
	failure, ok := providerkit.LongRetryWaitFailure(resetAt, b.Clock().Now())
	if !ok {
		return
	}
	b.daemonMu.Lock()
	stop := b.longWait == nil && !b.compactionPrompt
	if stop {
		b.longWait = &openCodeLongWait{failure: failure, raw: slices.Clone(raw)}
	}
	b.daemonMu.Unlock()
	if !stop {
		return
	}
	// The stop runs outside daemonMu. The base calls the family's hooks, and
	// several of them take daemonMu, which is not reentrant.
	if err := b.Base.Interrupt(agent.NewStopContext(nil)); err != nil {
		slog.Warn("stop the OpenCode turn for its long retry wait", "agent_id", b.AgentID(), "reset_at", resetAt, "error", err)
	}
}

// TurnEndVerdict returns the automatic-retry verdict of the turn that ends
// now. err is the error of the response that ends the turn, and stopped states
// that the agent stopped or that the turn was interrupted. A member that ends
// a turn of its own, such as Kilo's steer, calls it as the prompt end does.
//
// A compaction prompt gives CompactionEnd, whatever its outcome: a failed
// compaction leaves the task where the last turn left it, so no retry may
// resume it. A Kilo steer that joined the compaction prompt shares its verdict,
// in either order of the two ends. A stop of the reader gives NotRetryable, and
// so does a turn that ended well. A long wait that LeapMux ended gives a
// usage-limit retry at the stated instant. Any other failure goes through the
// family recipe (classifyDaemonError).
func (b *FamilyBase) TurnEndVerdict(err error, stopped bool) acp.TurnVerdict {
	b.daemonMu.Lock()
	compaction, userStop, wait := b.compactionPrompt, b.userStopRequested, b.longWait
	b.daemonMu.Unlock()
	switch {
	case compaction:
		return acp.TurnVerdict{Failure: providerkit.CompactionEnd()}
	case userStop:
		return acp.TurnVerdict{}
	case wait != nil:
		return acp.TurnVerdict{Failure: wait.failure, Payload: wait.raw}
	case stopped || err == nil:
		return acp.TurnVerdict{}
	}
	observedAt := b.Clock().Now()
	failure, raw, found := b.turnDaemonError(err)
	if !found {
		return acp.TurnVerdict{}
	}
	return acp.TurnVerdict{Failure: classifyDaemonError(failure, b.MemberFailure, observedAt), Payload: raw}
}

// turnDaemonError finds the error that failed the turn, and the raw record that
// holds it, in the first source that proves it belongs to the turn:
//
//   - The newest stored message, when it is this prompt's assistant message
//     that holds an error. It is the daemon's own record of the failure.
//   - The held session.error, when it arrived after an assistant message of
//     this prompt.
//   - The turn's own ACP answer, which states the message and the error name
//     alone.
func (b *FamilyBase) turnDaemonError(err error) (DaemonError, json.RawMessage, bool) {
	b.daemonMu.Lock()
	held, lastAssistant, startedAt := b.heldSessionError, b.lastAssistant, b.promptStartedAt
	b.daemonMu.Unlock()

	stored, readErr := b.Questions.readLatestAssistantError(b.Context(), b.Clock(), b.CurrentSessionID())
	switch {
	case readErr != nil:
		slog.Debug("read the OpenCode stored turn error", "agent_id", b.AgentID(), "error", readErr)
	case stored.ownedBy(lastAssistant, startedAt):
		return stored.err, stored.info, true
	default:
		slog.Debug("the newest OpenCode stored message belongs to an earlier prompt", "agent_id", b.AgentID(), "message_id", stored.id)
	}
	if held != nil && held.afterAssistant {
		return held.err, held.raw, true
	}
	return acpAnswerDaemonError(err)
}

// ownedBy reports whether the stored message belongs to the prompt that
// observed lastAssistant and started at startedAt: its id is that assistant
// message, or the daemon created it after the prompt started.
func (s storedAssistantError) ownedBy(lastAssistant string, startedAt time.Time) bool {
	if lastAssistant != "" && s.id == lastAssistant {
		return true
	}
	return !startedAt.IsZero() && !s.created.Before(startedAt.Add(-assistantOwnershipSlack))
}

// acpAnswerDaemonError reads the ACP adapter's answer to a failed prompt: the
// message, and the error name in data.errorName. The answer is the turn's own,
// so it needs no proof of ownership. Its raw form is the payload.
func acpAnswerDaemonError(err error) (DaemonError, json.RawMessage, bool) {
	rpcErr, ok := providerkit.AsJSONRPCResponseError(err)
	if !ok {
		return DaemonError{}, nil, false
	}
	var data struct {
		ErrorName string `json:"errorName"`
	}
	if providerkit.RawPresent(rpcErr.Data) {
		// Data of another shape states no error name, and the message still decides.
		_ = json.Unmarshal(rpcErr.Data, &data)
	}
	raw, marshalErr := json.Marshal(struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	}{rpcErr.Code, rpcErr.Message, rpcErr.Data})
	if marshalErr != nil {
		raw = nil
	}
	return DaemonError{Name: data.ErrorName, Data: DaemonErrorData{Message: rpcErr.Message}}, raw, true
}

// classifyDaemonError applies the family recipe to the error of a failed turn.
// The stages run in order, and the first that decides wins:
//
//  1. The vocabulary of the member's own gateway (member).
//  2. A definitive vendor verdict on the message, then on the response body.
//  3. A billing block: Zen's CreditsError, or HTTP 402.
//  4. An indicative vendor verdict on the message, then on the body.
//  5. A rate limit: HTTP 429, or a Zen usage or rate limit in the body.
//  6. A transient failure: HTTP 5xx, or a retryable error with no status,
//     such as a refused connection.
//
// Then a deadline in the response headers turns a rate limit, or a usage limit
// with no reset instant, into a usage limit that lifts at that deadline.
//
// The codes insufficient_quota and usage_not_included are billing codes of the
// vendor parser, so stage 2 decides them. A check of its own here would also
// match the DashScope throttle that the parser keeps apart.
func classifyDaemonError(failure DaemonError, member func(DaemonError) (providerkit.TurnFailure, bool), observedAt time.Time) providerkit.TurnFailure {
	if member != nil {
		if verdict, ok := member(failure); ok {
			return verdict
		}
	}
	data := failure.Data
	var indicative *providerkit.TurnFailure
	for _, text := range []string{data.Message, data.ResponseBody} {
		verdict, ok := modelvendor.ClassifyError(text, observedAt)
		if !ok {
			continue
		}
		if verdict.Definitive {
			return verdict.Failure
		}
		if indicative == nil {
			indicative = &verdict.Failure
		}
	}
	bodyType := zenErrorType(data.ResponseBody)
	switch {
	case bodyType == zenCreditsError:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the model gateway reported " + zenCreditsError}
	case data.StatusCode == http.StatusPaymentRequired:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the model provider answered HTTP 402"}
	}
	verdict := retryableDaemonFailure(data, bodyType, indicative)
	if resetAt, ok := providerkit.RetryAfterInstant(data.ResponseHeaders, observedAt); ok {
		verdict = providerkit.WithResetHint(verdict, resetAt, observedAt)
	}
	return verdict
}

// retryableDaemonFailure is stages 4 to 6 of classifyDaemonError. indicative is
// the first indicative vendor verdict, or nil.
func retryableDaemonFailure(data DaemonErrorData, bodyType string, indicative *providerkit.TurnFailure) providerkit.TurnFailure {
	switch {
	case indicative != nil:
		return *indicative
	case data.StatusCode == http.StatusTooManyRequests:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the model provider answered HTTP 429"}
	case bodyType == zenRateLimitError || strings.HasSuffix(bodyType, zenUsageLimitErrorTail):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the model gateway reported " + bodyType}
	case data.StatusCode >= http.StatusInternalServerError:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: fmt.Sprintf("the model provider answered HTTP %d", data.StatusCode)}
	case data.StatusCode == 0 && data.IsRetryable:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "the agent reported a retryable failure with no HTTP status"}
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}

// zenErrorType reads error.type of a Zen response body,
// {"type":"error","error":{"type":"GoUsageLimitError",...}}. It is "" for a body
// of another shape.
func zenErrorType(body string) string {
	if body == "" {
		return ""
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(body), &envelope) != nil || !providerkit.RawPresent(envelope.Error) {
		return ""
	}
	var gatewayError struct {
		Type string `json:"type"`
	}
	// An error that is a bare string, as {"error":"Internal Server Error"}, states no type.
	if json.Unmarshal(envelope.Error, &gatewayError) != nil {
		return ""
	}
	return gatewayError.Type
}
