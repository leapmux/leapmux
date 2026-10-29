package dirac

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// diracAdvertisedSteerMethod reports the steer route that the initialize
// response advertises. Dirac states it as a bare `_meta["dev.dirac/whisper"]`
// capability flag, so this reads that flag rather than the sessionSteer.method
// shape of other agents.
func diracAdvertisedSteerMethod(response []byte) string {
	var init struct {
		AgentCapabilities struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	if json.Unmarshal(response, &init) != nil {
		return ""
	}
	raw, ok := init.AgentCapabilities.Meta[diracSteerMethod]
	if !ok {
		return ""
	}
	var flag bool
	if json.Unmarshal(raw, &flag) != nil || !flag {
		return ""
	}
	return diracSteerMethod
}

// handleExtraMethod consumes the `dev.dirac/*` notifications the agent sends:
// a steering acknowledgement, a pinned-messages update, and a client
// annotation. None carries conversation state, so each is logged and consumed.
// E57 also logs the reason a steering acknowledgement states. A request under
// a `dev.dirac/` prefix falls through, so the base refuses a method LeapMux
// does not answer.
func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	switch line.Method {
	case "dev.dirac/steering_status", "dev.dirac/pinned_messages_update", "dev.dirac/client_annotation":
		if line.HasID() {
			return false
		}
		reason := providerkit.StringField(line.Params, "reason")
		slog.Debug("dirac extension notification", "agent_id", a.AgentID(), "method", line.Method, "reason", reason)
		return true
	}
	return false
}

// diracNoticeMatches reports whether a chunk's whole text equals one of
// Dirac's notices.
func diracNoticeMatches(text string) bool {
	switch text {
	case contracts.DiracNoticeRetryingApiRequest,
		contracts.DiracNoticeHookCancelled,
		contracts.DiracNoticeShellIntegrationUnavailable,
		contracts.DiracNoticeShellIntegrationSuggestion:
		return true
	}
	return false
}

// observeDiracNotice reads one conversation update of the main session
// (Hooks.SessionMetadataHandler). A chunk whose whole text equals one of
// Dirac's notices becomes its own native row, so the notice never joins the
// model's stream. A retry card flushes the buffered text of the failed
// attempt first, marked interrupted, because Dirac replaces that attempt.
// It claims no other update. Every tool card also reaches the turn-failure
// record first (observeDiracFailure), which claims nothing.
//
// The metadata of an update plays no part here. Dirac stamps every update of
// the main session with `dev.dirac/seq`, and a chunk that a subagent tag
// routes to a child transcript never reaches this handler (see
// Hooks.ChildUpdateRoute, which the base runs first).
func (a *Agent) observeDiracNotice(updateType string, _ map[string]json.RawMessage, update json.RawMessage) bool {
	a.observeDiracFailure(updateType, update)
	switch updateType {
	case contracts.ACPUpdateAgentMessageChunk:
		var chunk struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(update, &chunk) != nil || !diracNoticeMatches(chunk.Content.Text) {
			return false
		}
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		a.persistDiracNotice(update)
		return true
	case contracts.ACPUpdateToolCall:
		var call struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(update, &call) != nil {
			return false
		}
		switch call.Title {
		case diracRetryCardTitle:
			// The failed attempt's text keeps its row, marked interrupted.
			a.FlushBufferedOutput(agent.MessageCompletionInterrupted)
			return false
		case diracExhaustedCardTitle:
			// The retries-exhausted card repeats the service error that the
			// question Dirac asks about the same failure also states, and the
			// question is the row the reader answers, so the summary card is
			// consumed rather than stored. Its updates are consumed with it.
			a.FlushBufferedOutput(agent.MessageCompletionInterrupted)
			a.claimExhaustedCard(update)
			return true
		}
		return false
	case contracts.ACPUpdateToolCallUpdate:
		return a.consumeClaimedUpdate(update)
	}
	return false
}

// claimExhaustedCard records the tool call of a consumed retries-exhausted
// card, so the updates of that call are consumed too.
func (a *Agent) claimExhaustedCard(update json.RawMessage) {
	var call struct {
		ToolCallID string `json:"toolCallId"`
	}
	if json.Unmarshal(update, &call) != nil || call.ToolCallID == "" {
		return
	}
	a.claimedMu.Lock()
	if a.claimedCards == nil {
		a.claimedCards = make(map[string]struct{})
	}
	a.claimedCards[call.ToolCallID] = struct{}{}
	a.claimedMu.Unlock()
}

// consumeClaimedUpdate drops one update of a tool call whose card was
// consumed. A final update ends the claim, so a repeated call id later in the
// conversation renders again.
func (a *Agent) consumeClaimedUpdate(update json.RawMessage) bool {
	var call struct {
		ToolCallID string `json:"toolCallId"`
		Status     string `json:"status"`
	}
	if json.Unmarshal(update, &call) != nil || call.ToolCallID == "" {
		return false
	}
	a.claimedMu.Lock()
	_, claimed := a.claimedCards[call.ToolCallID]
	if claimed && acp.StatusIsFinal(call.Status) {
		delete(a.claimedCards, call.ToolCallID)
	}
	a.claimedMu.Unlock()
	return claimed
}

// diracRetryCardTitle is the title of the tool card Dirac raises while it
// retries the model request.
const diracRetryCardTitle = "API Error (Retrying)"

// diracExhaustedCardTitle is the title of the tool card Dirac raises when
// every retry of the model request failed.
const diracExhaustedCardTitle = "API Error (Retries Exhausted)"

// persistDiracNotice stores one notice chunk byte for byte.
func (a *Agent) persistDiracNotice(update json.RawMessage) {
	if _, err := a.Sink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update, AgentSessionID: a.CurrentSessionID()}); err != nil {
		slog.Error("dirac persist notice chunk", "agent_id", a.AgentID(), "error", err)
	}
}
