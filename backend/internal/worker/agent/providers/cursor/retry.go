package cursor

import (
	"encoding/json"
	"regexp"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// cursor-agent 2026.09.28 never fails a prompt. processPrompt catches every
// error, writes ONE agent message chunk that states it, and answers
// {"stopReason":"end_turn"} (.tmp/retry-research/cursor-kiro-grok.md). That
// chunk, the last root update of the prompt, is the only record of the
// failure. Cursor states no reset instant and retries nothing in ACP mode.
var (
	// cursorRetriableCodeRe reads a RetriableError whose Connect code states
	// a server or network failure.
	cursorRetriableCodeRe = regexp.MustCompile(`^\n\nError: RetriableError: \[(?:unavailable|internal|unknown|deadline_exceeded|aborted)\]`)
	// cursorConnectionRe reads Cursor's connection watchdog, which gives up
	// on a stream that stalled or failed repeatedly.
	cursorConnectionRe = regexp.MustCompile(`RetriableError: Connection (?:stalled|failed)`)
	// cursorResourceExhaustedRe reads the Connect code of a throttle.
	cursorResourceExhaustedRe = regexp.MustCompile(`^\n\nError: RetriableError: \[resource_exhausted\]`)
)

// The whole chunk of a plan action that Cursor's backend asks for (the action
// texts of pretty/3115.js). "Upgrade your plan" covers a usage limit and a plan
// block alike, so it states no window.
const (
	cursorPaymentActionText = "\n\nAdd a payment method to continue"
	cursorUpgradeActionText = "\n\nUpgrade your plan to continue"
)

// recordCursorRootUpdate keeps the last agent message chunk of the root
// session for the turn-failure verdict. Any other update of the turn that
// follows the chunk clears it, because the turn went on after it.
func (a *Agent) recordCursorRootUpdate(updateType string, update json.RawMessage) {
	switch updateType {
	case contracts.ACPUpdateAgentMessageChunk:
		var chunk struct {
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		a.Mu.Lock()
		defer a.Mu.Unlock()
		if json.Unmarshal(update, &chunk) != nil || chunk.Content.Type != "text" {
			a.lastChunkText, a.lastChunkRaw = "", nil
			return
		}
		a.lastChunkText, a.lastChunkRaw = chunk.Content.Text, append(json.RawMessage(nil), update...)
	case contracts.ACPUpdateAgentThoughtChunk, contracts.ACPUpdateToolCall, contracts.ACPUpdateToolCallUpdate:
		a.clearCursorChunk()
	}
}

// clearCursorChunk drops the recorded chunk (Hooks.PromptPrepared, and
// ClearProviderState for the outgoing session).
func (a *Agent) clearCursorChunk() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.lastChunkText, a.lastChunkRaw = "", nil
}

// finishCursorPrompt returns the turn's retry verdict (Hooks.PromptEnded). A
// stop, and a prompt request that failed, give NotRetryable.
func (a *Agent) finishCursorPrompt(err error, stopped bool) acp.PromptEnding {
	a.Mu.Lock()
	text, raw := a.lastChunkText, a.lastChunkRaw
	a.lastChunkText, a.lastChunkRaw = "", nil
	a.Mu.Unlock()
	if stopped || err != nil {
		return acp.PromptEnding{Retry: &acp.TurnVerdict{}}
	}
	return acp.PromptEnding{Retry: &acp.TurnVerdict{Failure: cursorTurnFailure(text), Payload: raw}}
}

// cursorTurnFailure classifies the last root chunk of a prompt.
func cursorTurnFailure(text string) providerkit.TurnFailure {
	switch {
	case cursorRetriableCodeRe.MatchString(text) || cursorConnectionRe.MatchString(text):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Cursor reported a retriable failure"}
	case cursorResourceExhaustedRe.MatchString(text):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Cursor reported an exhausted resource"}
	case text == cursorPaymentActionText:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Cursor asks for a payment method"}
	case text == cursorUpgradeActionText:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated, Cause: "Cursor asks for a plan upgrade"}
	}
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
}
