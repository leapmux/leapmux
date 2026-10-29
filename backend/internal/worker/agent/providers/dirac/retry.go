package dirac

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Dirac 0.5.23 turns no model failure into a JSON-RPC error. After its own
// retries it raises a tool card titled diracFailedRequestTitle and blocks the
// turn on a session/request_permission for the same tool call, whose
// rawInput.body is "<status> <message>" (.tmp/retry-research/dirac.md). A
// reject ends the turn with end_turn, an approve makes Dirac retry in the same
// turn and completes the card, and a stop cancels the turn. A 402 or a 401
// raises a sibling card first, with its own title.
const (
	diracFailedRequestTitle      = "API Request Failed"
	diracPaymentCardTitle        = "API Error (Payment Required)"
	diracAuthenticationCardTitle = "API Error (Authentication)"
)

// acpPermissionKindRejectOnce is the ACP permission option kind that declines
// one request without a lasting rule. The contract states only the approval
// kind that the worker and the browser select.
const acpPermissionKindRejectOnce = "reject_once"

var (
	diracServerStatusRe   = regexp.MustCompile(`^5\d\d\b`)
	diracOverloadedRe     = regexp.MustCompile(`(?i)overloaded`)
	diracTransportRe      = regexp.MustCompile(`^(?:Connection error|Request timed out)\.`)
	diracRateLimitRe      = regexp.MustCompile(`^429\b`)
	diracPaymentStatusRe  = regexp.MustCompile(`^402\b`)
	diracPaymentSuffixRe  = regexp.MustCompile(`Payment is required by the configured provider\.`)
	diracSiblingCardKinds = map[string]diracSiblingCard{
		diracPaymentCardTitle:        diracSiblingPayment,
		diracAuthenticationCardTitle: diracSiblingAuthentication,
	}
)

// diracSiblingCard is the card that Dirac raises before a failed-request card
// to state the class of the failure.
type diracSiblingCard int

const (
	diracSiblingNone diracSiblingCard = iota
	diracSiblingPayment
	diracSiblingAuthentication
)

// diracFailedRequest is the failed-request card of the running prompt.
type diracFailedRequest struct {
	toolCallID string
	// raw is the update that raised the card, the payload of a retry.
	raw json.RawMessage
	// sibling is the sibling card that preceded this card.
	sibling diracSiblingCard
	// failure is the classified verdict of the card's dialog body, which the
	// dialog records whether LeapMux or the reader answers it. declined states
	// that the base queued LeapMux's reject. The verdict of a declined card is
	// the prompt's verdict, and a billing block decides even when the reader
	// answered the dialog.
	failure  providerkit.TurnFailure
	declined bool
}

// observeDiracFailure records the cards of a failed model request, for the
// turn-failure verdict (called from observeDiracNotice). It claims nothing.
func (a *Agent) observeDiracFailure(updateType string, update json.RawMessage) {
	var card struct {
		ToolCallID string `json:"toolCallId"`
		Name       string `json:"name"`
		Title      string `json:"title"`
		Status     string `json:"status"`
	}
	if json.Unmarshal(update, &card) != nil || card.ToolCallID == "" {
		return
	}
	a.retryMu.Lock()
	defer a.retryMu.Unlock()
	switch updateType {
	case contracts.ACPUpdateToolCall:
		if card.Name != card.Title {
			return
		}
		if sibling, ok := diracSiblingCardKinds[card.Title]; ok {
			a.pendingSibling = sibling
			return
		}
		if card.Title != diracFailedRequestTitle {
			return
		}
		a.failedRequest = &diracFailedRequest{
			toolCallID: card.ToolCallID,
			raw:        append(json.RawMessage(nil), update...),
			sibling:    a.pendingSibling,
		}
		a.pendingSibling = diracSiblingNone
	case contracts.ACPUpdateToolCallUpdate:
		// The reader approved the dialog, and Dirac retries the request in
		// the same turn, so the failure is over.
		if card.Status == "completed" && a.failedRequest != nil && a.failedRequest.toolCallID == card.ToolCallID {
			a.failedRequest = nil
		}
	}
}

// diracDialog is the part of a session/request_permission that the
// failed-request answer reads.
type diracDialog struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
		Name       string `json:"name"`
		Title      string `json:"title"`
		RawInput   struct {
			Body string `json:"body"`
		} `json:"rawInput"`
	} `json:"toolCall"`
	Options []struct {
		ID   string `json:"optionId"`
		Kind string `json:"kind"`
	} `json:"options"`
}

// readDiracFailedRequestDialog reads a failed-request dialog of the current
// session. It returns false for any other control request, a subagent's
// dialog included.
func (a *Agent) readDiracFailedRequestDialog(line *providerkit.ParsedLine) (diracDialog, bool) {
	var dialog diracDialog
	if json.Unmarshal(line.Params, &dialog) != nil {
		return diracDialog{}, false
	}
	if dialog.SessionID == "" || dialog.SessionID != a.CurrentSessionID() || dialog.ToolCall.ToolCallID == "" ||
		dialog.ToolCall.Name != diracFailedRequestTitle || dialog.ToolCall.Title != diracFailedRequestTitle {
		return diracDialog{}, false
	}
	return dialog, true
}

// answerDiracFailedRequest rejects the failed-request dialog of a retryable
// failure (Hooks.AnswerControlRequest), so the turn ends and LeapMux schedules
// its own retry (user decision 1). Every other dialog, and a failure that a
// sibling card classifies, stays with the reader. So does every dialog of a
// compaction prompt: the compaction's end schedules no retry, so a reject would
// only end the turn.
func (a *Agent) answerDiracFailedRequest(line *providerkit.ParsedLine) (any, bool) {
	dialog, ok := a.readDiracFailedRequestDialog(line)
	if !ok {
		return nil, false
	}
	failure := diracBodyFailure(dialog.ToolCall.RawInput.Body, a.agentClock().Now())
	a.retryMu.Lock()
	defer a.retryMu.Unlock()
	record := a.failedRequest
	if record == nil || record.toolCallID != dialog.ToolCall.ToolCallID {
		// Dirac raises the card before the dialog. Without the card, the
		// dialog itself carries the failure.
		record = &diracFailedRequest{
			toolCallID: dialog.ToolCall.ToolCallID,
			raw:        append(json.RawMessage(nil), line.Raw...),
			sibling:    a.pendingSibling,
		}
		a.failedRequest = record
		a.pendingSibling = diracSiblingNone
	}
	record.failure = failure
	rejectID, ok := diracRejectOnceOption(dialog)
	if !ok || a.compactionPrompt || record.sibling != diracSiblingNone || !diracLeapMuxRetries(failure) {
		return nil, false
	}
	return map[string]any{"outcome": map[string]string{"outcome": contracts.ACPPermissionOutcomeSelected, "optionId": rejectID}}, true
}

// diracRejectOnceOption returns the id of the dialog's one reject_once
// option. A dialog with none, or with two, stays with the reader.
func diracRejectOnceOption(dialog diracDialog) (string, bool) {
	rejectID := ""
	for _, option := range dialog.Options {
		if option.Kind == acpPermissionKindRejectOnce {
			if rejectID != "" {
				return "", false
			}
			rejectID = option.ID
		}
	}
	return rejectID, rejectID != ""
}

// diracLeapMuxRetries reports whether LeapMux retries the failure itself, so
// the dialog's reject is LeapMux's to give.
func diracLeapMuxRetries(failure providerkit.TurnFailure) bool {
	switch failure.Kind {
	case providerkit.TurnFailureTransient, providerkit.TurnFailureRateLimited:
		return true
	case providerkit.TurnFailureUsageLimited:
		return !failure.ResetAt.IsZero() || failure.Window == providerkit.UsageWindowWeekOrLess
	case providerkit.TurnFailureNotRetryable, providerkit.TurnFailureBillingBlocked:
		return false
	}
	return false
}

// diracFailedRequestAnswered marks the failed-request card declined once the
// base queued LeapMux's reject (Hooks.ControlRequestAnswered). A reject that
// the base could not queue reaches the reader, whose answer decides.
func (a *Agent) diracFailedRequestAnswered(line *providerkit.ParsedLine, queued bool) {
	dialog, ok := a.readDiracFailedRequestDialog(line)
	if !ok || !queued {
		return
	}
	a.retryMu.Lock()
	defer a.retryMu.Unlock()
	if a.failedRequest != nil && a.failedRequest.toolCallID == dialog.ToolCall.ToolCallID {
		a.failedRequest.declined = true
	}
}

// diracBodyFailure classifies the body of a failed-request card.
func diracBodyFailure(body string, observedAt time.Time) providerkit.TurnFailure {
	verdict, vendorDecided := modelvendor.ClassifyError(body, observedAt)
	switch {
	case vendorDecided && verdict.Definitive:
		return verdict.Failure
	case diracServerStatusRe.MatchString(body) || diracOverloadedRe.MatchString(body) || diracTransportRe.MatchString(body):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Dirac reported a failed model request"}
	case diracRateLimitRe.MatchString(body) && vendorDecided:
		// The vendor's wording is more specific than the status: a monthly
		// limit behind a 429 must not retry.
		return verdict.Failure
	case diracRateLimitRe.MatchString(body):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Dirac reported a 429"}
	case diracPaymentStatusRe.MatchString(body) || diracPaymentSuffixRe.MatchString(body):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Dirac reported a required payment"}
	case vendorDecided:
		return verdict.Failure
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}

// clearDiracPromptState drops the cards and the compaction mark of an earlier
// prompt before a prompt starts (Hooks.PromptPrepared).
func (a *Agent) clearDiracPromptState() {
	a.retryMu.Lock()
	defer a.retryMu.Unlock()
	a.failedRequest, a.pendingSibling, a.compactionPrompt = nil, diracSiblingNone, false
}

// diracTurnFailure takes the cards and the compaction mark of the ending
// prompt and returns its retry verdict. A compaction prompt ends on
// CompactionEnd, whatever its outcome: a failed compaction leaves the task
// where the last turn left it, so no retry may resume it.
func (a *Agent) diracTurnFailure(err error, stopped bool) acp.TurnVerdict {
	a.retryMu.Lock()
	record, pending, compaction := a.failedRequest, a.pendingSibling, a.compactionPrompt
	a.failedRequest, a.pendingSibling, a.compactionPrompt = nil, diracSiblingNone, false
	a.retryMu.Unlock()
	if compaction {
		return acp.TurnVerdict{Failure: providerkit.CompactionEnd()}
	}
	if stopped || err != nil {
		return acp.TurnVerdict{}
	}
	if record != nil && (record.declined || record.failure.Kind == providerkit.TurnFailureBillingBlocked) {
		// A billing block holds whoever answered the dialog: a Z.AI balance
		// failure arrives with no Payment card, and its body alone states it.
		return acp.TurnVerdict{Failure: record.failure, Payload: record.raw}
	}
	if pending == diracSiblingPayment || (record != nil && record.sibling == diracSiblingPayment) {
		return acp.TurnVerdict{Failure: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Dirac reported a required payment"}}
	}
	return acp.TurnVerdict{}
}

// finishDiracPrompt gives the completed children an archive read, then
// returns the turn's retry verdict (Hooks.PromptEnded).
func (a *Agent) finishDiracPrompt(err error, stopped bool) acp.PromptEnding {
	ending := a.hydrateSubagentArchives(err, stopped)
	verdict := a.diracTurnFailure(err, stopped)
	ending.Retry = &verdict
	return ending
}
