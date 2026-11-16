package kimi

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
)

type kimiTranscriptRecordView struct {
	sessionID string
	agentID   string
	operation kimiTranscriptOperation
}

func readKimiTranscriptRecord(raw []byte) (kimiTranscriptRecordView, bool) {
	var row struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Payload   struct {
			Type    string                    `json:"type"`
			AgentID string                    `json:"agent_id"`
			Seq     *int64                    `json:"seq"`
			Ops     []kimiTranscriptOperation `json:"ops"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &row) != nil || row.Type != contracts.KimiEventTranscriptOps || row.SessionID == "" || row.Payload.Type != contracts.KimiEventTranscriptOps || row.Payload.AgentID == "" || len(row.Payload.Ops) != 1 || (row.Payload.Seq != nil && *row.Payload.Seq < 0) {
		return kimiTranscriptRecordView{}, false
	}
	return kimiTranscriptRecordView{sessionID: row.SessionID, agentID: row.Payload.AgentID, operation: row.Payload.Ops[0]}, true
}

type kimiTranscriptToolFrame struct {
	Kind       string          `json:"kind"`
	FrameID    string          `json:"frameId"`
	ToolCallID string          `json:"toolCallId"`
	Name       string          `json:"name"`
	State      string          `json:"state"`
	Input      json.RawMessage `json:"input"`
	InputText  *string         `json:"inputText"`
	Output     json.RawMessage `json:"output"`
	Progress   *struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"progress"`
	AgentRefs []struct {
		AgentID string `json:"agentId"`
		Role    string `json:"role"`
	} `json:"agentRefs"`
}

type kimiTranscriptToolIdentity struct {
	sessionID, agentID, turnID, stepID, frameID, toolCallID string
}

func (id kimiTranscriptToolIdentity) supplement(kind string, fields map[string]any, maximum int) ([]byte, error) {
	if id.sessionID == "" || id.agentID == "" || id.turnID == "" || id.stepID == "" || id.frameID == "" || id.toolCallID == "" {
		return nil, fmt.Errorf("the Kimi tool supplement has no complete identity")
	}
	value := map[string]any{
		contracts.KimiTranscriptSupplementSessionID:  id.sessionID,
		contracts.KimiTranscriptSupplementAgentID:    id.agentID,
		contracts.KimiTranscriptSupplementTurnID:     id.turnID,
		contracts.KimiTranscriptSupplementStepID:     id.stepID,
		contracts.KimiTranscriptSupplementFrameID:    id.frameID,
		contracts.KimiTranscriptSupplementToolCallID: id.toolCallID,
	}
	for field, data := range fields {
		if _, exists := value[field]; exists {
			return nil, fmt.Errorf("the Kimi tool supplement repeats an identity field")
		}
		value[field] = data
	}
	raw, err := json.Marshal(map[string]any{kind: value})
	if err != nil {
		return nil, err
	}
	if len(raw) > maximum {
		return nil, fmt.Errorf("the complete Kimi tool supplement exceeds the message size limit")
	}
	return raw, nil
}
