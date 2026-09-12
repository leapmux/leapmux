package agent

import (
	"encoding/json"
	"fmt"
)

// copilotEvent keeps native data undecoded until its event handler needs specific fields.
type copilotEvent struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	AgentID  string          `json:"agentId"`
	ParentID string          `json:"parentId"`
	Data     json.RawMessage `json:"data"`
	// Ephemeral marks an event that the runtime itself does not write to its session
	// log. Every streaming delta carries it. LeapMux reports those as live progress and
	// never persists them, because the runtime sends the finished message separately and
	// a stored delta would repeat that text in the transcript.
	Ephemeral bool `json:"ephemeral"`
}

// decodeCopilotSessionEvent validates the root session before any child or tool identity can affect its transcript.
func decodeCopilotSessionEvent(params json.RawMessage, sessionID string) (copilotEvent, error) {
	var envelope struct {
		SessionID string       `json:"sessionId"`
		Event     copilotEvent `json:"event"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return copilotEvent{}, fmt.Errorf("decode Copilot session event: %w", err)
	}
	if sessionID == "" || envelope.SessionID != sessionID {
		return copilotEvent{}, fmt.Errorf("the Copilot event belongs to a different session")
	}
	if envelope.Event.Type == "" {
		return copilotEvent{}, fmt.Errorf("the Copilot event type is empty")
	}
	return envelope.Event, nil
}
