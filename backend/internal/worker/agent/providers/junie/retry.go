package junie

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Junie states a failure of the session in a session_info_update, under
// `_meta.jetbrains.air.sessionFailure`, when the initialize request lists the
// sessionFailure capability. The prompt then ends with a plain `cancelled`.
const (
	junieMetaJetBrains   = "jetbrains"
	junieSeverityError   = "error"
	junieSeverityWarning = "warning"
)

// The sessionFailure categories that the retry verdict reads, as the
// SessionFailurePresentation of Junie 3419.7 assigns them (R/junie.md).
const (
	// junieCategoryConnection is a provider that did not answer: a 503, a
	// timeout, or a network failure.
	junieCategoryConnection = "connection"
	// junieCategoryService holds a 429 or 529 that Junie gave up on, with no
	// action, beside failures of other kinds that offer an action.
	junieCategoryService = "service"
	// junieCategoryUnknown holds a provider failure that offers a retry,
	// and a spent account balance, which offers no action.
	junieCategoryUnknown = "unknown"
	// junieActionRetry is the action that Junie offers for a failure that a
	// new attempt can pass.
	junieActionRetry = "retry"
)

// The JSON-RPC errors of a failed Junie prompt. Junie 3419.7 declares them,
// but no probe provoked them: a model failure ends with `cancelled`.
const (
	junieErrorInsufficientBalance  = -32010
	junieErrorRateLimitExceeded    = -32011
	junieReasonInsufficientBalance = "insufficient_account_balance"
	junieReasonRateLimitExceeded   = "rate_limit_exceeded"
)

// junieSessionFailure is one `sessionFailure` object.
type junieSessionFailure struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Severity string   `json:"severity"`
	Title    string   `json:"title"`
	Details  string   `json:"details"`
	Actions  []string `json:"actions"`
}

// text is the failure as the reader reads it: the title, then the details.
func (f junieSessionFailure) text() string {
	parts := make([]string, 0, 2)
	for _, part := range []string{f.Title, f.Details} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n\n")
}

// junieFailureRecord is the error that the last sessionFailure of the main
// session stated.
type junieFailureRecord struct {
	failure junieSessionFailure
	// frame is the session_info_update that carried it, the retry's source.
	frame json.RawMessage
}

// readJunieSessionFailure reads the sessionFailure of one session_info_update
// `_meta`. It reports false for metadata that holds none, or holds one that
// does not decode.
func readJunieSessionFailure(metadata map[string]json.RawMessage) (junieSessionFailure, bool) {
	raw, ok := metadata[junieMetaJetBrains]
	if !ok {
		return junieSessionFailure{}, false
	}
	var meta struct {
		Air struct {
			SessionFailure *junieSessionFailure `json:"sessionFailure"`
		} `json:"air"`
	}
	if json.Unmarshal(raw, &meta) != nil || meta.Air.SessionFailure == nil {
		return junieSessionFailure{}, false
	}
	return *meta.Air.SessionFailure, true
}

// observeJunieSessionFailure states a sessionFailure of the main session in
// the transcript, and records an error for the retry verdict of the turn's
// end. Junie writes no text of its own for a failure that it states this way,
// so the failure note is the reader's only record of it. It reports whether it
// consumed the update.
func (a *Agent) observeJunieSessionFailure(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	if updateType != contracts.ACPUpdateSessionInfoUpdate {
		return false
	}
	failure, ok := readJunieSessionFailure(metadata)
	if !ok {
		return false
	}
	switch failure.Severity {
	case junieSeverityError:
		a.Mu.Lock()
		repeated := a.failureRecord != nil && a.failureRecord.failure.ID == failure.ID &&
			a.failureRecord.failure.text() == failure.text()
		a.failureRecord = &junieFailureRecord{failure: failure, frame: append(json.RawMessage(nil), update...)}
		a.Mu.Unlock()
		// A revision of the same failure that states the same text adds
		// nothing for the reader.
		if !repeated {
			a.persistJunieFailureNote(contracts.NotificationTypeAgentError, contracts.NotificationFieldError, failure.text())
		}
	case junieSeverityWarning:
		a.persistJunieFailureNote(contracts.NotificationTypeAgentStatus, contracts.NotificationFieldText, failure.text())
	default:
		slog.Warn("junie session failure of an unknown severity", "agent_id", a.AgentID(), "severity", failure.Severity)
		a.persistJunieFailureNote(contracts.NotificationTypeAgentStatus, contracts.NotificationFieldText, failure.text())
	}
	return true
}

// persistJunieFailureNote states one sessionFailure as a LeapMux notification
// of kind, after the model output that preceded it. A failure with no text
// writes no row.
func (a *Agent) persistJunieFailureNote(kind, field, text string) {
	if text == "" {
		return
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	a.Sink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType: kind,
		field:                           text,
	})
}

// clearJunieFailure forgets the recorded failure, when a turn starts or a
// context clear replaces the session.
func (a *Agent) clearJunieFailure() {
	a.Mu.Lock()
	a.failureRecord = nil
	a.Mu.Unlock()
}

// finishJuniePrompt returns the retry verdict of a prompt that ended
// (Hooks.PromptEnded). It reads the recorded failure without taking it: when a
// steer-started turn waits behind the prompt, the record is that turn's, the
// base drops the prompt's verdict, and the turn's own marker reads the record
// (observeJunieTurnEnd).
//
// The base retains a queued turn's own completion and immutable verdict even
// when its marker arrives while the prompt's end is being stored.
func (a *Agent) finishJuniePrompt(err error, stopped bool) acp.PromptEnding {
	a.Mu.Lock()
	record := a.failureRecord
	a.Mu.Unlock()
	verdict := junieTurnVerdict(err, stopped, record, a.clockOrReal().Now())
	return acp.PromptEnding{Retry: verdict}
}

// junieTurnVerdict is the retry verdict of one turn end, with the frame that
// carries the failure as its source.
func junieTurnVerdict(err error, stopped bool, record *junieFailureRecord, observedAt time.Time) acp.TurnVerdict {
	verdict := acp.TurnVerdict{Failure: junieTurnFailure(err, stopped, record, observedAt)}
	switch rpcErr, ok := providerkit.AsJSONRPCResponseError(err); {
	case ok:
		verdict.Payload = rpcErr.WireJSON()
	case err == nil && record != nil:
		verdict.Payload = record.frame
	}
	return verdict
}

// junieTurnFailure classifies the end of one turn. A JSON-RPC error decides
// first. A turn that ended with a result decides from the recorded error.
func junieTurnFailure(err error, stopped bool, record *junieFailureRecord, observedAt time.Time) providerkit.TurnFailure {
	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	if stopped {
		return notRetryable
	}
	if err != nil {
		rpcErr, ok := providerkit.AsJSONRPCResponseError(err)
		if !ok {
			return notRetryable
		}
		return junieErrorFailure(rpcErr, observedAt)
	}
	if record == nil {
		return notRetryable
	}
	failure := record.failure
	if vendor, ok := modelvendor.ClassifyError(failure.text(), observedAt); ok && vendor.Definitive {
		return vendor.Failure
	}
	retryOffered := slices.Contains(failure.Actions, junieActionRetry)
	switch {
	case failure.Category == junieCategoryConnection:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Junie reported a connection failure"}
	case failure.Category == junieCategoryService && len(failure.Actions) == 0:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Junie gave up on a rate-limited provider"}
	case failure.Category == junieCategoryUnknown && retryOffered:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Junie reported a provider failure that a retry can pass"}
	case failure.Category == junieCategoryUnknown && len(failure.Actions) == 0:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Junie reported a spent account balance"}
	}
	return notRetryable
}

// junieErrorFailure classifies the JSON-RPC error of a failed prompt by its
// code, or by the reason of its data.
func junieErrorFailure(rpcErr *providerkit.JSONRPCResponseError, observedAt time.Time) providerkit.TurnFailure {
	if vendor, ok := modelvendor.ClassifyError(rpcErr.Error(), observedAt); ok && vendor.Definitive {
		return vendor.Failure
	}
	reason := providerkit.StringField(rpcErr.Data, "reason")
	switch {
	case rpcErr.Code == junieErrorRateLimitExceeded, reason == junieReasonRateLimitExceeded:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Junie reported an exceeded rate limit"}
	case rpcErr.Code == junieErrorInsufficientBalance, reason == junieReasonInsufficientBalance:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Junie reported an insufficient account balance"}
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}
