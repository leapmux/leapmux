package agent

import (
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// handleACPModeUpdate preserves the native record and updates the provider's mode axis.
func (b *acpBase) handleACPModeUpdate(update json.RawMessage) {
	if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: update}, SpanInfo{}); err != nil {
		slog.Error("persist ACP mode update", "provider", b.providerName, "agent_id", b.agentID, "error", err)
	}
	var mode struct {
		CurrentModeID string `json:"currentModeId"`
	}
	if err := json.Unmarshal(update, &mode); err != nil {
		slog.Warn("decode ACP mode update", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if mode.CurrentModeID == "" {
		return
	}
	axis := b.secondaryChannel()
	b.mu.Lock()
	changed := *axis.field != mode.CurrentModeID
	*axis.field = mode.CurrentModeID
	b.mu.Unlock()
	if changed {
		b.broadcastSettingsRefresh()
	}
}
