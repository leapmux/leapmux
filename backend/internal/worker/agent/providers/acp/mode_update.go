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
	b.ObserveCurrentMode(mode.CurrentModeID)
}

// ObserveCurrentMode records a mode that the agent reports it entered, on the
// secondary axis of the provider. The base reads the standard
// current_mode_update. A provider whose agent reports a mode change on a
// notification of its own calls this for it, so both reports reach one
// setting and one broadcast. An empty mode states nothing and changes
// nothing.
func (b *Base) ObserveCurrentMode(modeID string) {
	if modeID == "" {
		return
	}
	axis := b.secondaryChannel()
	b.Mu.Lock()
	changed := *axis.field != modeID
	*axis.field = modeID
	b.Mu.Unlock()
	if changed {
		b.BroadcastSettingsRefresh()
	}
}
