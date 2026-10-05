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
// Dirac 0.5.17 replays a loaded session's history after its session/load
// reply: cli/src/acp/AcpAgent.ts subscribes the session, awaits
// replayLoadedSessionHistory, and only then returns the response, and the
// journal emitter flushes the replayed notifications behind the reply on the
// wire. All of that arrives while no prompt runs. The Worker already stores the
// transcript of the session it reopened, so an idle conversation update of the
// current session is that replay and draws nothing. Upstream HEAD loads a
// session without replay, so this rule and its comment retire together with
// 0.5.17.
//
// Like Gemini's replay rule, this is no completion boundary: a prompt that
// starts before the replay ends receives the rest of the replay as its own
// output, and nothing can separate the two.
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
