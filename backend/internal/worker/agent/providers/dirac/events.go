package dirac

import (
	"encoding/json"
	"log/slog"

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

// handleExtraMethod consumes the `dev.dirac/*` notifications the agent sends.
// `dev.dirac/steering_status` acknowledges a whisper and
// `dev.dirac/pinned_messages_update` reports compaction; neither carries state
// LeapMux keeps, so both are logged and consumed. A request under a
// `dev.dirac/` prefix falls through, so the base refuses a method LeapMux does
// not answer.
func (a *Agent) handleExtraMethod(line *providerkit.ParsedLine) bool {
	switch line.Method {
	case "dev.dirac/steering_status", "dev.dirac/pinned_messages_update", "dev.dirac/client_annotation":
		if line.HasID() {
			return false
		}
		slog.Debug("dirac extension notification", "agent_id", a.AgentID(), "method", line.Method)
		return true
	}
	return false
}
