package reasonix

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// observeReasonixNotice reads one conversation update of the main session
// (Hooks.SessionMetadataHandler). Reasonix emits its warnings and compaction
// notes as ordinary message chunks whose whole text starts with one of its two
// prefixes; each such chunk becomes its own native row, so the notice never
// joins the model's stream. It claims no other update.
func (a *Agent) observeReasonixNotice(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	if updateType != contracts.ACPUpdateAgentMessageChunk || len(metadata) != 0 {
		return false
	}
	text, ok := reasonixChunkText(update)
	if !ok {
		return false
	}
	if !strings.HasPrefix(text, contracts.ReasonixNoticePrefixWarning) &&
		!strings.HasPrefix(text, contracts.ReasonixNoticePrefixCompacted) {
		return false
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	if _, err := a.Sink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update, AgentSessionID: a.CurrentSessionID()}); err != nil {
		slog.Error("reasonix persist notice chunk", "agent_id", a.AgentID(), "error", err)
	}
	return true
}

// observeSessionMetadata keeps native notices out of the model stream and the
// current proposal candidate. The candidate is operational data for a possible
// fresh-context execution, not a second transcript row.
func (a *Agent) observeSessionMetadata(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	if a.observeReasonixNotice(updateType, metadata, update) {
		return true
	}
	if updateType != contracts.ACPUpdateUserMessageChunk && updateType != contracts.ACPUpdateAgentMessageChunk {
		return false
	}
	text, ok := reasonixChunkText(update)
	if !ok {
		return false
	}
	a.Mu.Lock()
	if updateType == contracts.ACPUpdateUserMessageChunk {
		a.planDraft.Reset()
	} else {
		a.planDraft.WriteString(text)
	}
	a.Mu.Unlock()
	return false
}

// resetPlanDraft drops the candidate of the session that ended.
func (a *Agent) resetPlanDraft() {
	a.Mu.Lock()
	a.planDraft.Reset()
	a.Mu.Unlock()
}

// reasonixChunkText reads the text projection shared by native conversation chunks and saved notices.
func reasonixChunkText(raw json.RawMessage) (string, bool) {
	var chunk struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &chunk) != nil {
		return "", false
	}
	return chunk.Content.Text, true
}
