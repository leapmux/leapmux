package kimi

import (
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"
)

type kimiTranscriptInteraction struct {
	ID         string `json:"interactionId"`
	Kind       string `json:"interactionKind"`
	ToolCallID string `json:"toolCallId"`
	State      string `json:"state"`
}

func decodeTranscriptInteraction(raw []byte) (kimiTranscriptInteraction, bool) {
	var value kimiTranscriptInteraction
	if json.Unmarshal(raw, &value) != nil || value.ID == "" {
		return value, false
	}
	switch value.Kind {
	case contracts.KimiInteractionKindApproval:
		return value, value.State == kimiInteractionPending || value.State == "approved" || value.State == "rejected" || value.State == "cancelled"
	case contracts.KimiInteractionKindQuestion:
		return value, value.State == kimiInteractionPending || value.State == "answered" || value.State == "dismissed" || value.State == "cancelled"
	default:
		return value, false
	}
}

func (value kimiTranscriptInteraction) identity(sessionID, agentID string) kimiInteraction {
	return kimiInteraction{SessionID: sessionID, AgentID: agentID, InteractionID: value.ID, Kind: value.Kind, ToolCallID: value.ToolCallID}
}

// reconcileTranscriptControls retires controls from one agent's complete interaction list.
// Kimi re-emits pending live frames after its subscription acknowledgement, so
// transcript requests never need conversion into a different wire format.
func (a *Agent) reconcileTranscriptControls(sessionID, agentID string, raw []byte) {
	if len(raw) == 0 {
		return
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil {
		return
	}
	byID := make(map[string]kimiTranscriptInteraction, len(items))
	for _, item := range items {
		value, valid := decodeTranscriptInteraction(item)
		_, duplicate := byID[value.ID]
		if !valid || duplicate {
			slog.Warn("Kimi transcript has an invalid interaction list", "agent_id", a.AgentID(), "kimi_agent", agentID)
			return
		}
		byID[value.ID] = value
	}
	a.Mu.Lock()
	var retired []kimiInteraction
	for id, pending := range a.controls {
		if pending.sessionID != sessionID || pending.agentID != agentID {
			continue
		}
		if value, exists := byID[id]; exists {
			if value.State != kimiInteractionPending {
				retired = append(retired, value.identity(sessionID, agentID))
			}
			continue
		}
		kind := contracts.KimiInteractionKindApproval
		if pending.kind == kimiControlQuestion {
			kind = contracts.KimiInteractionKindQuestion
		}
		retired = append(retired, kimiInteraction{SessionID: sessionID, AgentID: agentID, InteractionID: id, Kind: kind, ToolCallID: pending.toolCallID})
	}
	a.Mu.Unlock()
	sort.Slice(retired, func(i, j int) bool { return retired[i].InteractionID < retired[j].InteractionID })
	for _, interaction := range retired {
		a.resolveControl(interaction)
	}
}
