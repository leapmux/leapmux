package reasonix

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// handleExtraMethod routes Reasonix's own status and MCP requests.
func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	switch line.Method {
	case reasonixMethodStatusUpdate:
		a.handleReasonixStatusUpdate(line.Params)
	case contracts.ReasonixMethodMcpRequestInteraction:
		a.PublishSessionControlRequest(line, providerkit.MCPElicitationCancelAnswer())
	default:
		return false
	}
	return true
}

// observeReasonixNotice reads one conversation update of the main session
// (Hooks.SessionMetadataHandler). Reasonix emits its warnings and compaction
// notes as ordinary message chunks whose whole text starts with one of its two
// prefixes; each such chunk becomes its own native row, so the notice never
// joins the model's stream. It claims no other update.
func (a *Agent) observeReasonixNotice(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	if updateType != contracts.ACPUpdateAgentMessageChunk || len(metadata) != 0 {
		return false
	}
	var chunk struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(update, &chunk) != nil {
		return false
	}
	if !strings.HasPrefix(chunk.Content.Text, contracts.ReasonixNoticePrefixWarning) &&
		!strings.HasPrefix(chunk.Content.Text, contracts.ReasonixNoticePrefixCompacted) {
		return false
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	if _, err := a.Sink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update, AgentSessionID: a.CurrentSessionID()}); err != nil {
		slog.Error("reasonix persist notice chunk", "agent_id", a.AgentID(), "error", err)
	}
	return true
}
