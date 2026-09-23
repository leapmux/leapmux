package acp

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// handleACPModeUpdate preserves the native record and updates the provider's mode axis.
func (b *Base) handleACPModeUpdate(update json.RawMessage) {
	if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update}, agent.SpanInfo{}); err != nil {
		slog.Error("persist ACP mode update", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
	}
	var mode struct {
		CurrentModeID string `json:"currentModeId"`
	}
	if err := json.Unmarshal(update, &mode); err != nil {
		slog.Warn("decode ACP mode update", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
		return
	}
	if mode.CurrentModeID == "" {
		return
	}
	axis := b.secondaryChannel()
	b.Mu.Lock()
	changed := *axis.field != mode.CurrentModeID
	*axis.field = mode.CurrentModeID
	b.Mu.Unlock()
	if changed {
		b.BroadcastSettingsRefresh()
	}
}
