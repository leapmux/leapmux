package dirac

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

// handleSessionUpdate reads each update of a Dirac session before the base
// draws it. It returns true for an update that it consumes.
//
// Dirac 0.5.17 replays the history of a loaded session after its session/load
// reply. In cli/src/acp/AcpAgent.ts, loadSession subscribes the session and
// awaits replayLoadedSessionHistory. Only then does it return the response.
// The journal emitter flushes the replayed notifications behind the reply on
// the wire. All of them arrive while no prompt runs.
//
// The Worker already stores the transcript of the session that it reopened.
// An idle conversation update of the current session is therefore that replay,
// and it draws nothing. Upstream HEAD loads a session without replay, so this
// rule and its comment retire together with 0.5.17.
//
// This rule cannot find the end of the replay, and the Gemini rule cannot
// either. A prompt that starts before the replay ends receives the rest of the
// replay as its own output. Nothing can separate the two.
func (a *Agent) handleSessionUpdate(sessionID string, _ agent.ProviderServices, update json.RawMessage) bool {
	if a.PromptActive() || !a.IsCurrentSession(sessionID) {
		return false
	}
	var message struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(update, &message) != nil {
		return false
	}
	switch message.SessionUpdate {
	case contracts.ACPUpdateAgentMessageChunk, contracts.ACPUpdateAgentThoughtChunk,
		contracts.ACPUpdateToolCall, contracts.ACPUpdateToolCallUpdate, contracts.ACPUpdatePlan:
		slog.Debug("dirac idle replay update consumed", "agent_id", a.AgentID(), "update", message.SessionUpdate)
		return true
	default:
		return false
	}
}
